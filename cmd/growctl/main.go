// Command growctl is a TUI daemon that keeps Tapo/Kasa power strip outlets in
// sync with a deterministic, time-of-day schedule. It polls every outlet on
// an interval and corrects any relay that has drifted from what its schedule
// calls for.
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
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pborges/tapo"
)

type options struct {
	hosts              stringList
	subnet             string
	port               int
	tick, timeout      time.Duration
	username, password string
	enableLegacy       bool
	schedulePath       string
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
	addrs := make([]string, 0, len(endpoints))
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
		addrs = append(addrs, strip.Address())
	}

	cfg, err := loadScheduleFile(opts.schedulePath)
	if err != nil {
		log.Fatalf("load schedule file %s: %v", opts.schedulePath, err)
	}
	fmt.Fprintf(os.Stderr, "Schedule file: %s\n", opts.schedulePath)

	model := newModel(ctx, strips, addrs, opts.tick, opts.timeout, opts.schedulePath, cfg)
	program := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := program.Run(); err != nil {
		log.Fatal(err)
	}
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("growctl", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Var(&opts.hosts, "hosts", "HS300 or P316M hostnames/IPs, comma-separated or repeated (omit to discover)")
	flags.StringVar(&opts.subnet, "subnet", "", "IPv4 subnet to scan during discovery (bare address means /24)")
	flags.IntVar(&opts.port, "port", tapo.DefaultPort, "local protocol port")
	flags.DurationVar(&opts.tick, "tick", 5*time.Second, "how often to compare actual outlet state against the schedule and correct drift")
	flags.DurationVar(&opts.timeout, "timeout", 5*time.Second, "network/discovery timeout")
	flags.StringVar(&opts.username, "username", "", "TP-Link account email (or TAPO_USERNAME)")
	flags.StringVar(&opts.password, "password", "", "TP-Link account password (prefer TAPO_PASSWORD)")
	flags.BoolVar(&opts.enableLegacy, "enable-legacy", false, "also try the legacy XOR protocol on port 9999 before KLAP (or TAPO_ENABLE_LEGACY)")
	flags.StringVar(&opts.schedulePath, "schedule", "", "path to the schedule JSON file (default: ./growctl-schedule.json in the working directory)")
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
	if opts.tick < time.Second {
		return options{}, errors.New("tick must be at least 1s")
	}
	if opts.timeout <= 0 {
		return options{}, errors.New("timeout must be positive")
	}
	if (opts.username == "") != (opts.password == "") {
		return options{}, errors.New("both TP-Link username and password are required")
	}
	if opts.schedulePath == "" {
		path, err := defaultSchedulePath()
		if err != nil {
			return options{}, fmt.Errorf("resolve default schedule path: %w", err)
		}
		opts.schedulePath = path
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
