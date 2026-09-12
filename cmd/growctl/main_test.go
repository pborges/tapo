package main

import (
	"bytes"
	"testing"
	"time"
)

func TestParseOptionsDefaultsSchedulePath(t *testing.T) {
	t.Setenv("TAPO_USERNAME", "")
	t.Setenv("TAPO_PASSWORD", "")
	var stderr bytes.Buffer
	opts, err := parseOptions([]string{"-tick", "10s", "192.0.2.10"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.hosts) != 1 || opts.hosts[0] != "192.0.2.10" || opts.tick != 10*time.Second {
		t.Fatalf("parseOptions = %+v", opts)
	}
	if opts.schedulePath == "" {
		t.Fatal("expected a default schedule path")
	}
}

func TestParseOptionsHostsAndSubnetConflict(t *testing.T) {
	t.Setenv("TAPO_USERNAME", "")
	t.Setenv("TAPO_PASSWORD", "")
	var stderr bytes.Buffer
	if _, err := parseOptions([]string{"-hosts", "192.0.2.10", "-subnet", "192.0.2.0"}, &stderr); err == nil {
		t.Fatal("expected error combining -hosts and -subnet")
	}
}

func TestParseOptionsRejectsShortTick(t *testing.T) {
	var stderr bytes.Buffer
	if _, err := parseOptions([]string{"-tick", "100ms", "192.0.2.10"}, &stderr); err == nil {
		t.Fatal("expected error for sub-second tick")
	}
}
