package main

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/pborges/tapo"
)

func newTestModel(t *testing.T, aliases ...string) model {
	t.Helper()
	strip, err := tapo.New("192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &scheduleFile{Plugs: map[string]*plugSchedule{}}
	m := newModel(t.Context(), []*tapo.Strip{strip}, []string{strip.Address()}, 0, 0, "", cfg)
	for i, alias := range aliases {
		idx := m.addPlug(0, "outlet-"+string(rune('a'+i)), alias)
		m.plugs[idx].seen = true
	}
	return m
}

func TestRenameKeyEntersRenameMode(t *testing.T) {
	m := newTestModel(t, "Plug 1", "Plug 2")
	m.selected = 1
	got, _ := m.updateList(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	next := got.(model)
	if next.mode != viewRename {
		t.Fatalf("mode = %v, want viewRename", next.mode)
	}
	if next.rename.plugIdx != 1 || next.rename.input != "Plug 2" {
		t.Fatalf("rename = %+v", next.rename)
	}
}

func TestRenameTypingAndBackspace(t *testing.T) {
	m := newTestModel(t, "Plug 1")
	m.mode = viewRename
	m.rename = renameState{plugIdx: 0, input: "Plug 1"}

	got, _ := m.updateRename(tea.KeyMsg{Type: tea.KeyBackspace})
	m = got.(model)
	if m.rename.input != "Plug " {
		t.Fatalf("input = %q, want %q", m.rename.input, "Plug ")
	}

	got, _ = m.updateRename(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	m = got.(model)
	if m.rename.input != "Plug 2" {
		t.Fatalf("input = %q, want %q", m.rename.input, "Plug 2")
	}
}

func TestRenameEscCancelsWithoutSubmitting(t *testing.T) {
	m := newTestModel(t, "Plug 1")
	m.mode = viewRename
	m.rename = renameState{plugIdx: 0, input: "Something else"}

	got, cmd := m.updateRename(tea.KeyMsg{Type: tea.KeyEsc})
	m = got.(model)
	if m.mode != viewList {
		t.Fatalf("mode = %v, want viewList", m.mode)
	}
	if cmd != nil {
		t.Fatal("expected no command on cancel")
	}
	if m.plugs[0].alias != "Plug 1" {
		t.Fatalf("alias = %q, want unchanged %q", m.plugs[0].alias, "Plug 1")
	}
}

func TestRenameEnterWithUnchangedAliasIsNoop(t *testing.T) {
	m := newTestModel(t, "Plug 1")
	m.mode = viewRename
	m.rename = renameState{plugIdx: 0, input: "  Plug 1  "} // trims to the same alias

	got, cmd := m.updateRename(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(model)
	if m.mode != viewList {
		t.Fatalf("mode = %v, want viewList", m.mode)
	}
	if cmd != nil {
		t.Fatal("expected no rename command when alias is unchanged")
	}
}

func TestRenameEnterWithBlankInputIsNoop(t *testing.T) {
	m := newTestModel(t, "Plug 1")
	m.mode = viewRename
	m.rename = renameState{plugIdx: 0, input: "   "}

	_, cmd := m.updateRename(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("expected no rename command for blank input")
	}
}

func TestRenameEnterWithNewAliasDispatchesCommand(t *testing.T) {
	m := newTestModel(t, "Plug 1")
	m.mode = viewRename
	m.rename = renameState{plugIdx: 0, input: "Grow Fan"}

	got, cmd := m.updateRename(tea.KeyMsg{Type: tea.KeyEnter})
	m = got.(model)
	if m.mode != viewList {
		t.Fatalf("mode = %v, want viewList", m.mode)
	}
	if cmd == nil {
		t.Fatal("expected a rename command to be dispatched")
	}
	// Renaming is optimistic only on success; the alias should not change
	// until the renameMsg comes back.
	if m.plugs[0].alias != "Plug 1" {
		t.Fatalf("alias = %q, want unchanged until renameMsg arrives", m.plugs[0].alias)
	}
}

func TestApplyRenameMsgUpdatesAliasOnSuccess(t *testing.T) {
	m := newTestModel(t, "Plug 1")
	got, _ := m.Update(renameMsg{plugIdx: 0, alias: "Grow Fan"})
	m = got.(model)
	if m.plugs[0].alias != "Grow Fan" {
		t.Fatalf("alias = %q, want %q", m.plugs[0].alias, "Grow Fan")
	}
	if m.err != nil {
		t.Fatalf("err = %v, want nil", m.err)
	}
}

func TestApplyRenameMsgKeepsAliasOnError(t *testing.T) {
	m := newTestModel(t, "Plug 1")
	got, _ := m.Update(renameMsg{plugIdx: 0, alias: "Grow Fan", err: errors.New("boom")})
	m = got.(model)
	if m.plugs[0].alias != "Plug 1" {
		t.Fatalf("alias = %q, want unchanged %q", m.plugs[0].alias, "Plug 1")
	}
	if m.err == nil {
		t.Fatal("expected err to be set")
	}
}
