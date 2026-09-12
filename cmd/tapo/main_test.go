package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
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
	if len(opts.hosts) != 1 || opts.hosts[0] != "192.0.2.10" || opts.interval != 3*time.Second {
		t.Fatalf("parseOptions = %+v", opts)
	}
}

func TestParseOptionsHostsAndSubnet(t *testing.T) {
	t.Setenv("TAPO_USERNAME", "")
	t.Setenv("TAPO_PASSWORD", "")
	var stderr bytes.Buffer
	opts, err := parseOptions([]string{"-hosts", "192.0.2.10,192.0.2.11", "-hosts", "strip.local"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.0.2.10", "192.0.2.11", "strip.local"}
	if len(opts.hosts) != len(want) {
		t.Fatalf("hosts = %v, want %v", opts.hosts, want)
	}
	for i := range want {
		if opts.hosts[i] != want[i] {
			t.Fatalf("hosts = %v, want %v", opts.hosts, want)
		}
	}

	if _, err := parseOptions([]string{"-hosts", "192.0.2.10", "-subnet", "192.0.2.0"}, &stderr); err == nil {
		t.Fatal("hosts with subnet unexpectedly succeeded")
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
			ctx: context.Background(), strips: []*tapo.Strip{strip}, interval: time.Second,
			snapshots: []*tapo.Snapshot{{Outlets: []tapo.Outlet{{ID: "outlet-1", Alias: "One", On: true}}}},
			loading:   true,
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
		ctx: context.Background(), strips: []*tapo.Strip{strip},
		snapshots: []*tapo.Snapshot{{Outlets: []tapo.Outlet{{ID: "outlet-1"}}}},
		toggling:  true,
	}
	updated, command := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if updated.(model).toggling != true || command != nil {
		t.Fatal("duplicate toggle was not ignored")
	}
}

func TestMultipleStripSelection(t *testing.T) {
	t.Parallel()
	first, err := tapo.New("192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := tapo.New("192.0.2.2")
	if err != nil {
		t.Fatal(err)
	}
	m := model{
		strips: []*tapo.Strip{first, second},
		snapshots: []*tapo.Snapshot{
			{Device: tapo.DeviceInfo{Alias: "Office"}, Outlets: []tapo.Outlet{{Alias: "One"}, {Alias: "Two"}}},
			{Device: tapo.DeviceInfo{Alias: "Lab"}, Outlets: []tapo.Outlet{{Alias: "Three"}}},
		},
		selected: 2,
	}
	stripIndex, outletIndex, ok := m.selectedOutlet()
	if !ok || stripIndex != 1 || outletIndex != 0 {
		t.Fatalf("selectedOutlet = %d, %d, %t", stripIndex, outletIndex, ok)
	}
	view := m.View()
	if !strings.Contains(view, "Office") || !strings.Contains(view, "Lab") || !strings.Contains(view, "All strips") {
		t.Fatalf("multi-strip view is missing content:\n%s", view)
	}
}

func TestRenameKeyEntersRenameMode(t *testing.T) {
	t.Parallel()
	strip, err := tapo.New("192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	m := model{
		ctx: context.Background(), strips: []*tapo.Strip{strip},
		snapshots: []*tapo.Snapshot{{Outlets: []tapo.Outlet{{ID: "outlet-1", Alias: "One"}}}},
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	got := updated.(model)
	if !got.renaming || got.rename.outletID != "outlet-1" || got.rename.input != "One" {
		t.Fatalf("rename state = %+v, renaming=%t", got.rename, got.renaming)
	}
}

func TestRenameTypingAndBackspace(t *testing.T) {
	t.Parallel()
	m := model{renaming: true, rename: renameState{input: "One"}}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = updated.(model)
	if m.rename.input != "On" {
		t.Fatalf("input = %q, want %q", m.rename.input, "On")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	m = updated.(model)
	if m.rename.input != "One" {
		t.Fatalf("input = %q, want %q", m.rename.input, "One")
	}
}

func TestRenameEscCancelsWithoutSubmitting(t *testing.T) {
	t.Parallel()
	m := model{renaming: true, rename: renameState{input: "Something else"}}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := updated.(model)
	if got.renaming || cmd != nil {
		t.Fatalf("renaming=%t cmd=%v, want cancelled with no command", got.renaming, cmd)
	}
}

func TestRenameEnterWithUnchangedAliasIsNoop(t *testing.T) {
	t.Parallel()
	strip, err := tapo.New("192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	m := model{
		ctx: context.Background(), strips: []*tapo.Strip{strip},
		snapshots: []*tapo.Snapshot{{Outlets: []tapo.Outlet{{ID: "outlet-1", Alias: "One"}}}},
		renaming:  true,
		rename:    renameState{stripIndex: 0, outletIndex: 0, outletID: "outlet-1", input: "  One  "},
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("expected no rename command when alias is unchanged")
	}
}

func TestRenameEnterWithNewAliasDispatchesCommand(t *testing.T) {
	t.Parallel()
	strip, err := tapo.New("192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	m := model{
		ctx: context.Background(), strips: []*tapo.Strip{strip},
		snapshots: []*tapo.Snapshot{{Outlets: []tapo.Outlet{{ID: "outlet-1", Alias: "One"}}}},
		renaming:  true,
		rename:    renameState{stripIndex: 0, outletIndex: 0, outletID: "outlet-1", input: "Grow Fan"},
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(model)
	if got.renaming {
		t.Fatal("expected renaming to end")
	}
	if cmd == nil {
		t.Fatal("expected a rename command to be dispatched")
	}
	if got.snapshots[0].Outlets[0].Alias != "One" {
		t.Fatalf("alias = %q, want unchanged until renameMsg arrives", got.snapshots[0].Outlets[0].Alias)
	}
}

func TestApplyRenameMsgUpdatesAliasOnSuccess(t *testing.T) {
	t.Parallel()
	m := model{snapshots: []*tapo.Snapshot{{Outlets: []tapo.Outlet{{ID: "outlet-1", Alias: "One"}}}}}
	updated, _ := m.Update(renameMsg{stripIndex: 0, outletIndex: 0, alias: "Grow Fan"})
	got := updated.(model)
	if got.snapshots[0].Outlets[0].Alias != "Grow Fan" {
		t.Fatalf("alias = %q, want %q", got.snapshots[0].Outlets[0].Alias, "Grow Fan")
	}
	if got.renameErr != nil {
		t.Fatalf("renameErr = %v, want nil", got.renameErr)
	}
}

func TestApplyRenameMsgKeepsAliasOnError(t *testing.T) {
	t.Parallel()
	m := model{snapshots: []*tapo.Snapshot{{Outlets: []tapo.Outlet{{ID: "outlet-1", Alias: "One"}}}}}
	updated, _ := m.Update(renameMsg{stripIndex: 0, outletIndex: 0, alias: "Grow Fan", err: errors.New("boom")})
	got := updated.(model)
	if got.snapshots[0].Outlets[0].Alias != "One" {
		t.Fatalf("alias = %q, want unchanged %q", got.snapshots[0].Outlets[0].Alias, "One")
	}
	if got.renameErr == nil {
		t.Fatal("expected renameErr to be set")
	}
}
