// Command tapo is an interactive terminal dashboard for HS300 and P316M power strips.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/pborges/tapo"
)

var (
	titleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	mutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	onStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	offStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	selectedStyle = lipgloss.NewStyle().Background(lipgloss.Color("236"))
	errorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

type options struct {
	hosts              stringList
	subnet             string
	port               int
	interval, timeout  time.Duration
	username, password string
	enableLegacy       bool
}

type stringList []string

func (values *stringList) String() string { return strings.Join(*values, ",") }

func (values *stringList) Set(value string) error {
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			return errors.New("host cannot be empty")
		}
		*values = append(*values, item)
	}
	return nil
}

func main() {
	log.SetFlags(0)
	opts, err := parseOptions(os.Args[1:], os.Stderr)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type endpoint struct {
		host     string
		port     int
		httpPort int
	}
	endpoints := make([]endpoint, 0, len(opts.hosts))
	for _, host := range opts.hosts {
		endpoints = append(endpoints, endpoint{host: host, port: opts.port, httpPort: 80})
	}
	if len(endpoints) == 0 {
		fmt.Fprintln(os.Stderr, "Discovering Kasa/Tapo devices…")
		devices, err := tapo.Discover(ctx, opts.timeout, opts.subnet)
		if err != nil {
			log.Fatal(err)
		}
		for _, device := range devices {
			model := strings.ToUpper(device.Device.Model)
			if !strings.HasPrefix(model, "HS300") && !strings.HasPrefix(model, "P316M") {
				continue
			}
			endpoints = append(endpoints, endpoint{host: device.Host, port: opts.port, httpPort: device.HTTPPort})
			fmt.Fprintf(os.Stderr, "Found %s (%s) at %s\n", device.Device.Alias, device.Device.Model, device.Host)
		}
		if len(endpoints) == 0 {
			log.Fatal("no HS300 or P316M devices found; pass -hosts with one or more strip IP addresses")
		}
	}

	strips := make([]*tapo.Strip, 0, len(endpoints))
	for _, endpoint := range endpoints {
		clientOptions := []tapo.Option{
			tapo.WithPort(endpoint.port), tapo.WithHTTPPort(endpoint.httpPort), tapo.WithTimeout(opts.timeout),
		}
		if opts.username != "" || opts.password != "" {
			clientOptions = append(clientOptions, tapo.WithCredentials(opts.username, opts.password))
		}
		if !opts.enableLegacy {
			clientOptions = append(clientOptions, tapo.WithDisableLegacy())
		}
		strip, err := tapo.New(endpoint.host, clientOptions...)
		if err != nil {
			log.Fatal(err)
		}
		strips = append(strips, strip)
	}
	model := newModel(ctx, strips, opts.interval)
	program := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		log.Fatal(err)
	}
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("tapo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Var(&opts.hosts, "hosts", "HS300 or P316M hostnames/IPs, comma-separated or repeated (omit to discover)")
	flags.StringVar(&opts.subnet, "subnet", "", "IPv4 subnet to scan during discovery (bare address means /24)")
	flags.IntVar(&opts.port, "port", tapo.DefaultPort, "local protocol port")
	flags.DurationVar(&opts.interval, "interval", 2*time.Second, "meter refresh interval")
	flags.DurationVar(&opts.timeout, "timeout", 5*time.Second, "network/discovery timeout")
	flags.StringVar(&opts.username, "username", "", "TP-Link account email (or TAPO_USERNAME)")
	flags.StringVar(&opts.password, "password", "", "TP-Link account password (prefer TAPO_PASSWORD)")
	flags.BoolVar(&opts.enableLegacy, "enable-legacy", false, "also try the legacy XOR protocol on port 9999 before KLAP (or TAPO_ENABLE_LEGACY)")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if opts.username == "" {
		opts.username = os.Getenv("TAPO_USERNAME")
	}
	if opts.password == "" {
		opts.password = os.Getenv("TAPO_PASSWORD")
	}
	if !opts.enableLegacy {
		if value, err := strconv.ParseBool(os.Getenv("TAPO_ENABLE_LEGACY")); err == nil {
			opts.enableLegacy = value
		}
	}
	for _, host := range flags.Args() {
		if err := opts.hosts.Set(host); err != nil {
			return options{}, err
		}
	}
	if len(opts.hosts) > 0 && strings.TrimSpace(opts.subnet) != "" {
		return options{}, errors.New("hosts and subnet cannot be used together")
	}
	opts.hosts = uniqueStrings(opts.hosts)
	if opts.port < 1 || opts.port > 65535 {
		return options{}, fmt.Errorf("invalid port %d", opts.port)
	}
	if opts.interval < 250*time.Millisecond {
		return options{}, errors.New("interval must be at least 250ms")
	}
	if opts.timeout <= 0 {
		return options{}, errors.New("timeout must be positive")
	}
	if (opts.username == "") != (opts.password == "") {
		return options{}, errors.New("both TP-Link username and password are required")
	}
	return opts, nil
}

func uniqueStrings(values []string) stringList {
	result := make(stringList, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

type model struct {
	ctx       context.Context
	strips    []*tapo.Strip
	interval  time.Duration
	snapshots []*tapo.Snapshot
	errs      []error
	selected  int
	loading   bool
	toggling  bool
	status    string
	toggleErr error
}

type snapshotsMsg struct {
	snapshots []*tapo.Snapshot
	errs      []error
}
type toggleMsg struct{ err error }
type tickMsg time.Time

func newModel(ctx context.Context, strips []*tapo.Strip, interval time.Duration) model {
	return model{
		ctx: ctx, strips: strips, interval: interval,
		snapshots: make([]*tapo.Snapshot, len(strips)), errs: make([]error, len(strips)),
		loading: true, status: "Connecting…",
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.fetchSnapshots(), tickAfter(m.interval))
}

func (m model) fetchSnapshots() tea.Cmd {
	return func() tea.Msg {
		snapshots := make([]*tapo.Snapshot, len(m.strips))
		errs := make([]error, len(m.strips))
		var wg sync.WaitGroup
		for i, strip := range m.strips {
			i, strip := i, strip
			wg.Add(1)
			go func() {
				defer wg.Done()
				snapshots[i], errs[i] = strip.Snapshot(m.ctx)
			}()
		}
		wg.Wait()
		return snapshotsMsg{snapshots: snapshots, errs: errs}
	}
}

func tickAfter(interval time.Duration) tea.Cmd {
	return tea.Tick(interval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if m.selected > 0 {
				m.selected--
			}
		case "down", "j":
			if m.selected+1 < m.outletCount() {
				m.selected++
			}
		case "r":
			if !m.loading && !m.toggling {
				m.loading = true
				m.status = "Refreshing…"
				return m, m.fetchSnapshots()
			}
		case " ", "enter":
			stripIndex, outletIndex, ok := m.selectedOutlet()
			if !ok || m.toggling {
				break
			}
			outlet := m.snapshots[stripIndex].Outlets[outletIndex]
			strip := m.strips[stripIndex]
			m.toggling = true
			m.status = fmt.Sprintf("Switching %s…", outlet.Alias)
			return m, func() tea.Msg {
				return toggleMsg{err: strip.SetOutlet(m.ctx, outlet.ID, !outlet.On)}
			}
		}
	case snapshotsMsg:
		m.loading = false
		for i, snapshot := range msg.snapshots {
			if snapshot != nil {
				m.snapshots[i] = snapshot
			}
		}
		m.errs = msg.errs
		if m.selected >= m.outletCount() {
			m.selected = max(0, m.outletCount()-1)
		}
		if !m.toggling {
			m.status = ""
		}
	case toggleMsg:
		m.toggling = false
		m.toggleErr = msg.err
		if msg.err != nil {
			m.status = ""
			break
		}
		m.loading = true
		m.status = "Refreshing…"
		return m, m.fetchSnapshots()
	case tickMsg:
		cmds := []tea.Cmd{tickAfter(m.interval)}
		if !m.loading && !m.toggling {
			m.loading = true
			cmds = append(cmds, m.fetchSnapshots())
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

func (m model) outletCount() int {
	total := 0
	for _, snapshot := range m.snapshots {
		if snapshot != nil {
			total += len(snapshot.Outlets)
		}
	}
	return total
}

func (m model) selectedOutlet() (stripIndex, outletIndex int, ok bool) {
	selected := m.selected
	for i, snapshot := range m.snapshots {
		if snapshot == nil {
			continue
		}
		if selected < len(snapshot.Outlets) {
			return i, selected, true
		}
		selected -= len(snapshot.Outlets)
	}
	return 0, 0, false
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Kasa / Tapo Power Strips"))
	b.WriteString("\n")
	if m.outletCount() == 0 {
		b.WriteString("\n" + m.status + "\n")
		for i, strip := range m.strips {
			line := strip.Address()
			if i < len(m.errs) && m.errs[i] != nil {
				line += ": " + m.errs[i].Error()
			}
			b.WriteString(errorStyle.Render(line) + "\n")
		}
		b.WriteString("\n" + mutedStyle.Render("q quit"))
		return b.String()
	}

	header := fmt.Sprintf("   %-3s %-22s %-5s %9s %9s %9s %10s", "#", "Outlet", "State", "Power", "Current", "Voltage", "Total")
	globalIndex := 0
	var grandPower, grandEnergy float64
	for stripIndex, s := range m.snapshots {
		if s == nil {
			fmt.Fprintf(&b, "\n%s\n", mutedStyle.Render(m.strips[stripIndex].Address()))
			if stripIndex < len(m.errs) && m.errs[stripIndex] != nil {
				b.WriteString(errorStyle.Render("Last refresh: "+m.errs[stripIndex].Error()) + "\n")
			}
			continue
		}
		fmt.Fprintf(&b, "\n%s  %s  %s\n", s.Device.Alias, mutedStyle.Render(s.Device.Model), mutedStyle.Render(m.strips[stripIndex].Address()))
		b.WriteString(mutedStyle.Render(fmt.Sprintf("Updated %s · RSSI %d dBm", s.UpdatedAt.Format("15:04:05"), s.Device.RSSI)))
		b.WriteString("\n\n" + mutedStyle.Render(header) + "\n")

		var stripPower, stripEnergy float64
		for i, outlet := range s.Outlets {
			state := offStyle.Render(fmt.Sprintf("%-5s", "OFF"))
			if outlet.On {
				state = onStyle.Render(fmt.Sprintf("%-5s", "ON"))
			}
			power, current, voltage, total := "—", "—", "—", "—"
			if outlet.Energy != nil {
				power = fmt.Sprintf("%.1f W", outlet.Energy.Power)
				current = fmt.Sprintf("%.3f A", outlet.Energy.Current)
				voltage = fmt.Sprintf("%.1f V", outlet.Energy.Voltage)
				total = fmt.Sprintf("%.3f kWh", outlet.Energy.Total)
				stripPower += outlet.Energy.Power
				stripEnergy += outlet.Energy.Total
			}
			name := truncate(outlet.Alias, 22)
			line := fmt.Sprintf("   %-3d %-22s %s %9s %9s %9s %10s", i+1, name, state, power, current, voltage, total)
			if globalIndex == m.selected {
				line = "›" + line[1:]
				line = selectedStyle.Render(line)
			}
			b.WriteString(line + "\n")
			globalIndex++
		}
		grandPower += stripPower
		grandEnergy += stripEnergy
		fmt.Fprintf(&b, "\nStrip total: %.1f W    Meter totals: %.3f kWh\n", stripPower, stripEnergy)
		if stripIndex < len(m.errs) && m.errs[stripIndex] != nil {
			b.WriteString(errorStyle.Render("Last refresh: "+m.errs[stripIndex].Error()) + "\n")
		}
	}
	if len(m.strips) > 1 {
		fmt.Fprintf(&b, "\nAll strips: %.1f W    Meter totals: %.3f kWh\n", grandPower, grandEnergy)
	}
	if m.status != "" {
		b.WriteString(mutedStyle.Render(m.status) + "\n")
	}
	if m.toggleErr != nil {
		b.WriteString(errorStyle.Render("Toggle: "+m.toggleErr.Error()) + "\n")
	}
	b.WriteString("\n" + mutedStyle.Render("↑/↓ or j/k select · space/enter toggle · r refresh · q quit"))
	return b.String()
}

func truncate(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}
