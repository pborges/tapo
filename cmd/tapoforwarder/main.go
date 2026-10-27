// Command tapoforwarder polls HS300 and P316M power strips and P110M plugs in
// the background, exposes their meter readings as Prometheus metrics, and
// serves a small web UI for discovering, managing, and switching them. The
// managed device list is kept in a JSON config file.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/pborges/tapo"
)

type options struct {
	listen             string
	configPath         string
	subnet             string
	interval, timeout  time.Duration
	username, password string
	enableLegacy       bool
}

func main() {
	log.SetFlags(log.LstdFlags)
	opts, err := parseOptions(os.Args[1:], os.Stderr)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store, err := loadConfig(opts.configPath)
	if err != nil {
		log.Fatal(err)
	}
	clientOptions := []tapo.Option{tapo.WithTimeout(opts.timeout)}
	if opts.username != "" {
		clientOptions = append(clientOptions, tapo.WithCredentials(opts.username, opts.password))
	}
	if !opts.enableLegacy {
		clientOptions = append(clientOptions, tapo.WithDisableLegacy())
	}
	manager := newManager(ctx, store, clientOptions, opts.interval)
	if err := manager.start(); err != nil {
		log.Fatal(err)
	}

	srv := &server{manager: manager, subnet: opts.subnet, timeout: opts.timeout}
	httpServer := &http.Server{Addr: opts.listen, Handler: srv.routes(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}()
	log.Printf("tapoforwarder listening on %s (config %s, %d strips)", opts.listen, opts.configPath, len(store.strips()))
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("tapoforwarder", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.listen, "listen", envOr("TAPO_LISTEN", ":9105"), "HTTP listen address for the web UI and /metrics (or TAPO_LISTEN)")
	flags.StringVar(&opts.configPath, "config", envOr("TAPO_CONFIG", "tapoforwarder.json"), "path to the strip config file (or TAPO_CONFIG)")
	flags.StringVar(&opts.subnet, "subnet", os.Getenv("TAPO_SUBNET"), "default IPv4 subnet for web UI scans; empty means broadcast (or TAPO_SUBNET)")
	flags.DurationVar(&opts.interval, "interval", 10*time.Second, "poll interval per strip")
	flags.DurationVar(&opts.timeout, "timeout", 5*time.Second, "network/discovery timeout")
	flags.StringVar(&opts.username, "username", "", "TP-Link account email (or TAPO_USERNAME)")
	flags.StringVar(&opts.password, "password", "", "TP-Link account password (prefer TAPO_PASSWORD)")
	flags.BoolVar(&opts.enableLegacy, "enable-legacy", false, "also try the legacy XOR protocol on port 9999 before KLAP (or TAPO_ENABLE_LEGACY)")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() > 0 {
		return options{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
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
	if opts.interval < time.Second {
		return options{}, errors.New("interval must be at least 1s")
	}
	if opts.timeout <= 0 {
		return options{}, errors.New("timeout must be positive")
	}
	if (opts.username == "") != (opts.password == "") {
		return options{}, errors.New("both TP-Link username and password are required")
	}
	return opts, nil
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
