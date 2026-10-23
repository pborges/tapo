package main

import (
	"context"
	"errors"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/pborges/tapo"
)

// manager owns one background poller per configured strip and caches the most
// recent snapshot of each, so HTTP and metrics requests never wait on a device.
type manager struct {
	ctx           context.Context
	store         *configStore
	clientOptions []tapo.Option
	interval      time.Duration

	mu     sync.RWMutex
	states map[string]*stripState // keyed by host
}

type stripState struct {
	host    string
	strip   *tapo.Strip
	refresh chan struct{}
	cancel  context.CancelFunc

	// Guarded by manager.mu.
	config      stripConfig
	snapshot    *tapo.Snapshot
	err         error
	lastSuccess time.Time
	duration    time.Duration
}

// stripStatus is a point-in-time copy of a strip's state, safe to read
// without locks.
type stripStatus struct {
	Host        string
	Config      stripConfig
	Snapshot    *tapo.Snapshot
	Err         error
	LastSuccess time.Time
	Duration    time.Duration
}

var errUnknownStrip = errors.New("strip is not configured")

func newManager(ctx context.Context, store *configStore, clientOptions []tapo.Option, interval time.Duration) *manager {
	return &manager{
		ctx: ctx, store: store, clientOptions: clientOptions, interval: interval,
		states: map[string]*stripState{},
	}
}

// start launches pollers for every strip already in the config.
func (m *manager) start() error {
	for _, cfg := range m.store.strips() {
		if err := m.launch(cfg); err != nil {
			return err
		}
	}
	return nil
}

// add persists a new strip and starts polling it. It reports whether the strip
// was new.
func (m *manager) add(cfg stripConfig) (bool, error) {
	if cfg.Port == tapo.DefaultPort {
		cfg.Port = 0
	}
	if cfg.HTTPPort == 80 {
		cfg.HTTPPort = 0
	}
	if _, err := m.newClient(cfg); err != nil {
		return false, err
	}
	added, err := m.store.add(cfg)
	if err != nil || !added {
		return false, err
	}
	return true, m.launch(cfg)
}

func (m *manager) remove(host string) (bool, error) {
	m.mu.Lock()
	state, ok := m.states[host]
	delete(m.states, host)
	m.mu.Unlock()
	if ok {
		state.cancel()
	}
	return m.store.remove(host)
}

func (m *manager) newClient(cfg stripConfig) (*tapo.Strip, error) {
	options := append([]tapo.Option(nil), m.clientOptions...)
	if cfg.Port != 0 {
		options = append(options, tapo.WithPort(cfg.Port))
	}
	if cfg.HTTPPort != 0 {
		options = append(options, tapo.WithHTTPPort(cfg.HTTPPort))
	}
	return tapo.New(cfg.Host, options...)
}

func (m *manager) launch(cfg stripConfig) error {
	strip, err := m.newClient(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(m.ctx)
	state := &stripState{
		host: cfg.Host, strip: strip, config: cfg,
		refresh: make(chan struct{}, 1), cancel: cancel,
	}
	m.mu.Lock()
	m.states[cfg.Host] = state
	m.mu.Unlock()
	go m.poll(ctx, state)
	return nil
}

func (m *manager) poll(ctx context.Context, state *stripState) {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		m.pollOnce(ctx, state)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-state.refresh:
		}
	}
}

func (m *manager) pollOnce(ctx context.Context, state *stripState) {
	started := time.Now()
	snapshot, err := state.strip.Snapshot(ctx)
	if ctx.Err() != nil {
		return // strip was removed or we're shutting down
	}
	if err != nil {
		log.Printf("%s: %v", state.host, err)
	}

	m.mu.Lock()
	state.err = err
	state.duration = time.Since(started)
	if snapshot != nil {
		state.snapshot = snapshot
		state.lastSuccess = snapshot.UpdatedAt
	}
	m.mu.Unlock()

	if snapshot != nil {
		m.syncConfig(state.host, snapshot)
	}
}

// syncConfig records the device's current names and outlets in the config.
func (m *manager) syncConfig(host string, snapshot *tapo.Snapshot) {
	outlets := make([]outletConfig, len(snapshot.Outlets))
	for i, outlet := range snapshot.Outlets {
		outlets[i] = outletConfig{Index: outlet.Index, ID: outlet.ID, Alias: outlet.Alias}
	}
	var updated stripConfig
	err := m.store.update(host, func(cfg *stripConfig) bool {
		changed := cfg.Alias != snapshot.Device.Alias || cfg.Model != snapshot.Device.Model ||
			cfg.DeviceID != snapshot.Device.DeviceID || !equalOutlets(cfg.Outlets, outlets)
		cfg.Alias, cfg.Model, cfg.DeviceID, cfg.Outlets = snapshot.Device.Alias, snapshot.Device.Model, snapshot.Device.DeviceID, outlets
		updated = *cfg
		return changed
	})
	if err != nil {
		log.Printf("%s: save config: %v", host, err)
		return
	}
	m.mu.Lock()
	if state, ok := m.states[host]; ok && updated.Host != "" {
		state.config = updated
	}
	m.mu.Unlock()
}

func equalOutlets(a, b []outletConfig) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// requestRefresh asks host's poller to poll now rather than at the next tick.
func (m *manager) requestRefresh(host string) error {
	m.mu.RLock()
	state, ok := m.states[host]
	m.mu.RUnlock()
	if !ok {
		return errUnknownStrip
	}
	select {
	case state.refresh <- struct{}{}:
	default: // a refresh is already pending
	}
	return nil
}

func (m *manager) client(host string) (*tapo.Strip, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.states[host]
	if !ok {
		return nil, errUnknownStrip
	}
	return state.strip, nil
}

func (m *manager) hosts() map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	hosts := make(map[string]bool, len(m.states))
	for host := range m.states {
		hosts[host] = true
	}
	return hosts
}

// statuses returns every strip's state, sorted by name and then host.
func (m *manager) statuses() []stripStatus {
	m.mu.RLock()
	result := make([]stripStatus, 0, len(m.states))
	for _, state := range m.states {
		result = append(result, stripStatus{
			Host: state.host, Config: state.config, Snapshot: state.snapshot, Err: state.err,
			LastSuccess: state.lastSuccess, Duration: state.duration,
		})
	}
	m.mu.RUnlock()
	sort.Slice(result, func(i, j int) bool {
		if result[i].Config.Alias != result[j].Config.Alias {
			return result[i].Config.Alias < result[j].Config.Alias
		}
		return result[i].Host < result[j].Host
	})
	return result
}

// fresh reports whether status has a snapshot recent enough to report. Stale
// data is withheld so Grafana shows a gap instead of a frozen value.
func (m *manager) fresh(status stripStatus, now time.Time) bool {
	return status.Snapshot != nil && now.Sub(status.LastSuccess) <= 3*m.interval
}
