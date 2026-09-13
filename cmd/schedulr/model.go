package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pborges/tapo"
)

// plug is one controllable outlet, flattened across every configured strip.
// Only stripIndex and outletID are read from background tick commands; every
// other field is read and written exclusively from Update, so no locking is
// needed.
type plug struct {
	stripIndex int
	outletID   string
	alias      string
	seen       bool
	on         bool
	power      *float64 // watts; nil when unknown
	schedule   *plugSchedule
}

type viewMode int

const (
	viewList viewMode = iota
	viewEdit
	viewRename
)

type model struct {
	ctx        context.Context
	strips     []*tapo.Strip
	stripAddrs []string
	stripErrs  []error

	tick    time.Duration
	timeout time.Duration
	cfgPath string
	cfg     *scheduleFile

	plugs    []plug
	selected int
	busy     bool

	width  int // terminal width; defaultWidth until the first WindowSizeMsg arrives
	height int // terminal height; defaultHeight until the first WindowSizeMsg arrives

	mode   view
	edit   editState
	rename renameState

	status string
	err    error
	log    []logEntry
}

// defaultWidth and defaultHeight size the view before the first
// WindowSizeMsg arrives.
const (
	defaultWidth  = 80
	defaultHeight = 24
)

// logEntry is one line of the scrolling correction history shown at the
// bottom of the list screen.
type logEntry struct {
	at    time.Time
	text  string
	isErr bool
}

// maxLogEntries bounds how much history a long-running daemon keeps in
// memory; only the most recent ones are ever shown anyway.
const maxLogEntries = 500

func (m *model) appendLog(isErr bool, text string) {
	m.log = append(m.log, logEntry{at: time.Now(), text: text, isErr: isErr})
	if len(m.log) > maxLogEntries {
		m.log = m.log[len(m.log)-maxLogEntries:]
	}
}

type view = viewMode

type correction struct {
	plugIdx int
	strip   *tapo.Strip
	outlet  string
	target  bool
}

type stripResult struct {
	idx     int
	outlets []tapo.Outlet
	power   map[string]float64
	err     error
}

// correctionResult is one correction attempt paired with its outcome.
type correctionResult struct {
	correction
	err error
}

type refreshMsg struct {
	results []stripResult
	applied []correctionResult
}

type tickMsg time.Time

type renameMsg struct {
	plugIdx int
	alias   string
	err     error
}

// renameCmd asks the device to rename one outlet. It only reads immutable
// plug identity fields, so it is safe to run on Bubble Tea's background
// goroutine.
func (m model) renameCmd(plugIdx int, alias string) tea.Cmd {
	p := m.plugs[plugIdx]
	strip := m.strips[p.stripIndex]
	outletID := p.outletID
	ctx, timeout := m.ctx, m.timeout
	return func() tea.Msg {
		opCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		err := strip.RenameOutlet(opCtx, outletID, alias)
		return renameMsg{plugIdx: plugIdx, alias: alias, err: err}
	}
}

func newModel(ctx context.Context, strips []*tapo.Strip, stripAddrs []string, tick, timeout time.Duration, cfgPath string, cfg *scheduleFile) model {
	return model{
		ctx: ctx, strips: strips, stripAddrs: stripAddrs, stripErrs: make([]error, len(strips)),
		tick: tick, timeout: timeout, cfgPath: cfgPath, cfg: cfg,
		width:  defaultWidth,
		height: defaultHeight,
		status: "Connecting…",
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.refreshAndCorrect(nil), tickAfter(m.tick))
}

func tickAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// refreshAndCorrect performs every network operation for one tick: applying
// any previously-decided corrections, then polling every strip for current
// outlet state and power. It touches no model state directly so it is safe
// to run on Bubble Tea's background goroutine.
func (m model) refreshAndCorrect(corrections []correction) tea.Cmd {
	strips := m.strips
	timeout := m.timeout
	ctx := m.ctx
	return func() tea.Msg {
		opCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		var applied []correctionResult
		for _, c := range corrections {
			applied = append(applied, correctionResult{correction: c, err: c.strip.SetOutlet(opCtx, c.outlet, c.target)})
		}

		results := make([]stripResult, len(strips))
		var wg sync.WaitGroup
		for i, strip := range strips {
			i, strip := i, strip
			wg.Add(1)
			go func() {
				defer wg.Done()
				res := stripResult{idx: i}
				snap, err := strip.Snapshot(opCtx)
				if snap != nil {
					res.outlets = snap.Outlets
					res.power = make(map[string]float64, len(snap.Outlets))
					for _, o := range snap.Outlets {
						if o.Energy != nil {
							res.power[o.ID] = o.Energy.Power
						}
					}
				} else {
					res.err = err
				}
				results[i] = res
			}()
		}
		wg.Wait()
		return refreshMsg{results: results, applied: applied}
	}
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.mode {
		case viewEdit:
			return m.updateEdit(msg)
		case viewRename:
			return m.updateRename(msg)
		default:
			return m.updateList(msg)
		}
	case tickMsg:
		cmds := []tea.Cmd{tickAfter(m.tick)}
		if !m.busy {
			m.busy = true
			cmds = append(cmds, m.refreshAndCorrect(m.decideCorrections()))
		}
		return m, tea.Batch(cmds...)
	case refreshMsg:
		m.applyRefresh(msg)
		return m, nil
	case renameMsg:
		if msg.err != nil {
			m.err = msg.err
			m.status = ""
		} else {
			m.err = nil
			m.status = ""
			if msg.plugIdx >= 0 && msg.plugIdx < len(m.plugs) {
				m.plugs[msg.plugIdx].alias = msg.alias
			}
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	}
	return m, nil
}

// decideCorrections compares each plug's schedule against its last known
// state. It only reads model fields, so it must run from Update.
func (m model) decideCorrections() []correction {
	var out []correction
	now := time.Now()
	for i := range m.plugs {
		p := &m.plugs[i]
		if !p.seen {
			continue
		}
		desired, managed := p.schedule.desiredState(now)
		if managed && desired != p.on {
			out = append(out, correction{plugIdx: i, strip: m.strips[p.stripIndex], outlet: p.outletID, target: desired})
		}
	}
	return out
}

func (m *model) applyRefresh(msg refreshMsg) {
	m.busy = false

	for _, res := range msg.results {
		if res.err != nil {
			m.stripErrs[res.idx] = res.err
			continue
		}
		m.stripErrs[res.idx] = nil
		for _, outlet := range res.outlets {
			idx := m.findPlug(res.idx, outlet.ID)
			if idx < 0 {
				idx = m.addPlug(res.idx, outlet.ID, outlet.Alias)
			}
			p := &m.plugs[idx]
			p.alias = outlet.Alias
			p.on = outlet.On
			p.seen = true
			if w, ok := res.power[outlet.ID]; ok {
				w := w
				p.power = &w
			} else {
				p.power = nil
			}
		}
	}

	state := map[bool]string{true: "on", false: "off"}
	for _, r := range msg.applied {
		name := m.plugName(r.plugIdx)
		if r.err != nil {
			m.appendLog(true, fmt.Sprintf("%s → %s failed: %s", name, state[r.target], r.err.Error()))
		} else {
			m.appendLog(false, fmt.Sprintf("%s → %s", name, state[r.target]))
		}
	}
	if m.status == "Connecting…" {
		m.status = ""
	}
	if m.selected >= len(m.plugs) {
		m.selected = max(0, len(m.plugs)-1)
	}
}

func (m model) plugName(i int) string {
	if i < 0 || i >= len(m.plugs) {
		return "?"
	}
	return m.plugs[i].alias
}

func (m model) findPlug(stripIdx int, outletID string) int {
	for i := range m.plugs {
		if m.plugs[i].stripIndex == stripIdx && m.plugs[i].outletID == outletID {
			return i
		}
	}
	return -1
}

// addPlug appends a newly-discovered outlet, wiring it to an existing
// schedule from the config file or creating a fresh disabled one.
func (m *model) addPlug(stripIdx int, outletID, alias string) int {
	sched, ok := m.cfg.Plugs[outletID]
	if !ok {
		sched = &plugSchedule{}
		m.cfg.Plugs[outletID] = sched
	}
	m.plugs = append(m.plugs, plug{stripIndex: stripIdx, outletID: outletID, alias: alias, schedule: sched})
	return len(m.plugs) - 1
}

func (m *model) save() {
	if err := saveScheduleFile(m.cfgPath, m.cfg); err != nil {
		m.err = err
	} else {
		m.err = nil
	}
}

func (m model) View() string {
	switch m.mode {
	case viewEdit:
		return m.viewEdit()
	default:
		return m.viewList()
	}
}
