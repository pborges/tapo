package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pborges/tapo"
)

func TestParseOptions(t *testing.T) {
	t.Setenv("TAPO_USERNAME", "")
	t.Setenv("TAPO_PASSWORD", "")
	var stderr bytes.Buffer
	opts, err := parseOptions([]string{"-interval", "3s", "192.0.2.10"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if opts.host != "192.0.2.10" || opts.interval != 3*time.Second {
		t.Fatalf("parseOptions = %+v", opts)
	}
}

func TestParseOptionsCredentialsFromEnvironment(t *testing.T) {
	t.Setenv("TAPO_USERNAME", "owner@example.com")
	t.Setenv("TAPO_PASSWORD", "secret")
	var stderr bytes.Buffer
	opts, err := parseOptions([]string{"192.0.2.10"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if opts.username != "owner@example.com" || opts.password != "secret" {
		t.Fatalf("credentials were not read from environment: %+v", opts)
	}
}

func TestChooseDevicePrefersHS300(t *testing.T) {
	t.Parallel()
	devices := []tapo.DiscoveredDevice{
		{Host: "192.0.2.1", Device: tapo.DeviceInfo{Model: "HS110"}},
		{Host: "192.0.2.2", Device: tapo.DeviceInfo{Model: "HS300(US)"}},
	}
	if got := chooseDevice(devices); got.Host != "192.0.2.2" {
		t.Fatalf("chooseDevice host = %q", got.Host)
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	if got := truncate("Workbench", 5); got != "Work…" {
		t.Fatalf("truncate = %q", got)
	}
}

func TestToggleAcceptedDuringRefresh(t *testing.T) {
	t.Parallel()
	strip, err := tapo.New("192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []tea.KeyType{tea.KeyEnter, tea.KeySpace} {
		m := model{
			ctx: context.Background(), strip: strip, interval: time.Second,
			snapshot: &tapo.Snapshot{Outlets: []tapo.Outlet{{ID: "outlet-1", Alias: "One", On: true}}},
			loading:  true,
		}
		updated, command := m.Update(tea.KeyMsg{Type: key})
		got := updated.(model)
		if !got.toggling || command == nil {
			t.Fatalf("key %v during refresh: toggling=%t command=%v", key, got.toggling, command)
		}
	}
}

func TestDuplicateToggleIgnored(t *testing.T) {
	t.Parallel()
	strip, err := tapo.New("192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	m := model{
		ctx: context.Background(), strip: strip,
		snapshot: &tapo.Snapshot{Outlets: []tapo.Outlet{{ID: "outlet-1"}}},
		toggling: true,
	}
	updated, command := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.(model).toggling != true || command != nil {
		t.Fatal("duplicate toggle was not ignored")
	}
}
