package tapo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const defaultTimeout = 5 * time.Second

type config struct {
	port     int
	httpPort int
	timeout  time.Duration
	username string
	password string
}

// Option configures a Strip.
type Option func(*config) error

// WithPort overrides the legacy local protocol port (9999).
func WithPort(port int) Option {
	return func(c *config) error {
		if port < 1 || port > 65535 {
			return fmt.Errorf("tapo: invalid port %d", port)
		}
		c.port = port
		return nil
	}
}

// WithHTTPPort overrides the KLAP HTTP port (80).
func WithHTTPPort(port int) Option {
	return func(c *config) error {
		if port < 1 || port > 65535 {
			return fmt.Errorf("tapo: invalid HTTP port %d", port)
		}
		c.httpPort = port
		return nil
	}
}

// WithTimeout sets the maximum duration of an individual network operation.
func WithTimeout(timeout time.Duration) Option {
	return func(c *config) error {
		if timeout <= 0 {
			return errors.New("tapo: timeout must be positive")
		}
		c.timeout = timeout
		return nil
	}
}

// WithCredentials supplies the case-sensitive TP-Link account credentials
// required by authenticated KLAP devices. The credentials remain in memory
// and KLAP sends only derived hashes over the network.
func WithCredentials(username, password string) Option {
	return func(c *config) error {
		if (username == "") != (password == "") {
			return errors.New("tapo: both username and password are required")
		}
		c.username, c.password = username, password
		return nil
	}
}

// Strip is a client for a Kasa/Tapo power strip. It automatically detects the
// legacy XOR protocol on port 9999 or authenticated KLAP over HTTP on port 80.
// It is safe for concurrent use.
type Strip struct {
	address  string
	timeout  time.Duration
	dialer   net.Dialer
	klap     *klapTransport
	mode     atomic.Uint32
	detectMu sync.Mutex
	legacyMu sync.Mutex
}

// New creates a client for host. host may be a hostname, an IPv4 address, an
// IPv6 address, or a host:port pair. A port in host is used for the legacy
// protocol; configure the independent KLAP port with WithHTTPPort.
func New(host string, options ...Option) (*Strip, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil, errors.New("tapo: host is required")
	}
	cfg := config{port: DefaultPort, httpPort: defaultHTTPPort, timeout: defaultTimeout}
	for _, option := range options {
		if option == nil {
			continue
		}
		if err := option(&cfg); err != nil {
			return nil, err
		}
	}

	networkHost := strings.Trim(host, "[]")
	address := net.JoinHostPort(networkHost, strconv.Itoa(cfg.port))
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		networkHost = parsedHost
		address = host
	}
	strip := &Strip{address: address, timeout: cfg.timeout}
	strip.klap = newKlapTransport(networkHost, cfg.httpPort, cfg.timeout, cfg.username, cfg.password)
	return strip, nil
}

// Address returns the selected protocol endpoint. Before the first query it
// returns the configured legacy address.
func (s *Strip) Address() string {
	if s.mode.Load() == transportKLAP {
		return s.klap.address()
	}
	return s.address
}

// Query sends an arbitrary JSON-compatible local protocol request and decodes
// its top-level response. It is intended as an escape hatch for commands not
// represented by the high-level API.
func (s *Strip) Query(ctx context.Context, request any) (RawResponse, error) {
	var response RawResponse
	if err := s.query(ctx, request, &response); err != nil {
		return nil, err
	}
	return response, nil
}

func (s *Strip) query(ctx context.Context, request, response any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("tapo: encode request: %w", err)
	}
	reply, err := s.exchange(ctx, payload)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(reply, response); err != nil {
		return fmt.Errorf("tapo: decode response: %w", err)
	}
	return nil
}

const (
	transportUnknown uint32 = iota
	transportLegacy
	transportKLAP
)

func (s *Strip) exchange(ctx context.Context, payload []byte) ([]byte, error) {
	switch s.mode.Load() {
	case transportLegacy:
		return s.legacyExchange(ctx, payload)
	case transportKLAP:
		return s.klap.exchange(ctx, payload)
	}

	// Only the first query performs transport detection. Once selected, legacy
	// requests may run concurrently while KLAP serializes its sequence numbers.
	s.detectMu.Lock()
	defer s.detectMu.Unlock()
	switch s.mode.Load() {
	case transportLegacy:
		return s.legacyExchange(ctx, payload)
	case transportKLAP:
		return s.klap.exchange(ctx, payload)
	}

	reply, legacyErr := s.legacyExchange(ctx, payload)
	if legacyErr == nil {
		s.mode.Store(transportLegacy)
		return reply, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	reply, klapErr := s.klap.exchange(ctx, payload)
	if klapErr == nil {
		s.mode.Store(transportKLAP)
		return reply, nil
	}
	return nil, errors.Join(
		fmt.Errorf("legacy protocol at %s: %w", s.address, legacyErr),
		fmt.Errorf("KLAP protocol at %s: %w", s.klap.address(), klapErr),
	)
}

func (s *Strip) legacyExchange(ctx context.Context, payload []byte) ([]byte, error) {
	// Many HS300 firmware versions reset overlapping port-9999 connections.
	// Serialize the complete exchange, including the dial, so concurrent callers
	// remain safe without overwhelming the device's single-request service.
	s.legacyMu.Lock()
	defer s.legacyMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	conn, err := s.dialer.DialContext(dialCtx, "tcp", s.address)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	deadline := time.Now().Add(s.timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("set deadline: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	if err := writeFrame(conn, payload); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("send request: %w", err)
	}
	reply, err := readFrame(conn)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("read response: %w", err)
	}
	return reply, nil
}
