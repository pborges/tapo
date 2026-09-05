// Command tapo is an interactive terminal dashboard for a Kasa/Tapo HS300.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
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
	host               string
	port               int
	interval, timeout  time.Duration
	username, password string
}

func main() {
	log.SetFlags(0)
	opts, err := parseOptions(os.Args[1:], os.Stderr)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if opts.host == "" {
		fmt.Fprintln(os.Stderr, "Discovering Kasa/Tapo devices…")
		devices, err := tapo.Discover(ctx, opts.timeout)
		if err != nil {
			log.Fatal(err)
		}
		if len(devices) == 0 {
			log.Fatal("no devices found; pass -host with the strip's IP address")
		}
		device := chooseDevice(devices)
		opts.host, opts.port = device.Host, device.Port
		fmt.Fprintf(os.Stderr, "Using %s (%s) at %s\n", device.Device.Alias, device.Device.Model, device.Host)
	}

	clientOptions := []tapo.Option{tapo.WithPort(opts.port), tapo.WithTimeout(opts.timeout)}
	if opts.username != "" || opts.password != "" {
		clientOptions = append(clientOptions, tapo.WithCredentials(opts.username, opts.password))
	}
	strip, err := tapo.New(opts.host, clientOptions...)
	if err != nil {
		log.Fatal(err)
	}
	model := newModel(ctx, strip, opts.interval)
	program := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		log.Fatal(err)
	}
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("tapo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.host, "host", "", "HS300 hostname or IP (omit to discover)")
	flags.IntVar(&opts.port, "port", tapo.DefaultPort, "local protocol port")
	flags.DurationVar(&opts.interval, "interval", 2*time.Second, "meter refresh interval")
	flags.DurationVar(&opts.timeout, "timeout", 5*time.Second, "network/discovery timeout")
	flags.StringVar(&opts.username, "username", "", "TP-Link account email (or TAPO_USERNAME)")
	flags.StringVar(&opts.password, "password", "", "TP-Link account password (prefer TAPO_PASSWORD)")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if opts.username == "" {
		opts.username = os.Getenv("TAPO_USERNAME")
	}
	if opts.password == "" {
		opts.password = os.Getenv("TAPO_PASSWORD")
	}
	if opts.host == "" && flags.NArg() > 0 {
		opts.host = flags.Arg(0)
	}
	if flags.NArg() > 1 {
		return options{}, errors.New("usage: tapo [-host address] [-interval 2s]")
	}
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

func chooseDevice(devices []tapo.DiscoveredDevice) tapo.DiscoveredDevice {
	for _, device := range devices {
		if strings.HasPrefix(strings.ToUpper(device.Device.Model), "HS300") {
			return device
		}
	}
	return devices[0]
}

type model struct {
	ctx      context.Context
	strip    *tapo.Strip
	interval time.Duration
	snapshot *tapo.Snapshot
	selected int
	loading  bool
	toggling bool
	status   string
	err      error
}

type snapshotMsg struct {
	snapshot *tapo.Snapshot
	err      error
}
type toggleMsg struct{ err error }
type tickMsg time.Time

func newModel(ctx context.Context, strip *tapo.Strip, interval time.Duration) model {
	return model{ctx: ctx, strip: strip, interval: interval, loading: true, status: "Connecting…"}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.fetchSnapshot(), tickAfter(m.interval))
}

func (m model) fetchSnapshot() tea.Cmd {
	return func() tea.Msg {
		snapshot, err := m.strip.Snapshot(m.ctx)
		return snapshotMsg{snapshot: snapshot, err: err}
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
			if m.snapshot != nil && m.selected+1 < len(m.snapshot.Outlets) {
				m.selected++
			}
		case "r":
			if !m.loading && !m.toggling {
				m.loading = true
				m.status = "Refreshing…"
				return m, m.fetchSnapshot()
			}
		case " ", "enter":
			if m.snapshot == nil || m.toggling || m.selected >= len(m.snapshot.Outlets) {
				break
			}
			outlet := m.snapshot.Outlets[m.selected]
			m.toggling = true
			m.status = fmt.Sprintf("Switching %s…", outlet.Alias)
			return m, func() tea.Msg {
				return toggleMsg{err: m.strip.SetOutlet(m.ctx, outlet.ID, !outlet.On)}
			}
		}
	case snapshotMsg:
		m.loading = false
		m.err = msg.err
		if msg.snapshot != nil {
			m.snapshot = msg.snapshot
			if m.selected >= len(msg.snapshot.Outlets) {
				m.selected = max(0, len(msg.snapshot.Outlets)-1)
			}
			if !m.toggling {
				m.status = ""
			}
		}
	case toggleMsg:
		m.toggling = false
		m.err = msg.err
		if msg.err != nil {
			m.status = ""
			break
		}
		m.loading = true
		m.status = "Refreshing…"
		return m, m.fetchSnapshot()
	case tickMsg:
		cmds := []tea.Cmd{tickAfter(m.interval)}
		if !m.loading && !m.toggling {
			m.loading = true
			cmds = append(cmds, m.fetchSnapshot())
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Kasa / Tapo Power Strip"))
	b.WriteString("\n")
	if m.snapshot == nil {
		b.WriteString("\n" + m.status + "\n")
		if m.err != nil {
			b.WriteString(errorStyle.Render(m.err.Error()) + "\n")
		}
		b.WriteString("\n" + mutedStyle.Render("q quit"))
		return b.String()
	}

	s := m.snapshot
	fmt.Fprintf(&b, "%s  %s  %s\n", s.Device.Alias, mutedStyle.Render(s.Device.Model), mutedStyle.Render(m.strip.Address()))
	b.WriteString(mutedStyle.Render(fmt.Sprintf("Updated %s · RSSI %d dBm", s.UpdatedAt.Format("15:04:05"), s.Device.RSSI)))
	b.WriteString("\n\n")

	header := fmt.Sprintf("   %-3s %-22s %-5s %9s %9s %9s %10s", "#", "Outlet", "State", "Power", "Current", "Voltage", "Total")
	b.WriteString(mutedStyle.Render(header) + "\n")
	var totalPower, totalEnergy float64
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
			totalPower += outlet.Energy.Power
			totalEnergy += outlet.Energy.Total
		}
		name := truncate(outlet.Alias, 22)
		line := fmt.Sprintf("   %-3d %-22s %s %9s %9s %9s %10s", i+1, name, state, power, current, voltage, total)
		if i == m.selected {
			line = "›" + line[1:]
			line = selectedStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	fmt.Fprintf(&b, "\nTotal now: %.1f W    Meter totals: %.3f kWh\n", totalPower, totalEnergy)
	if m.status != "" {
		b.WriteString(mutedStyle.Render(m.status) + "\n")
	}
	if m.err != nil {
		b.WriteString(errorStyle.Render("Last refresh: "+m.err.Error()) + "\n")
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
