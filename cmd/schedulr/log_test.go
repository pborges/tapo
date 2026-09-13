package main

import (
	"errors"
	"strings"
	"testing"
)

func TestAppendLogCapsSize(t *testing.T) {
	m := &model{}
	for i := 0; i < maxLogEntries+10; i++ {
		m.appendLog(false, "event")
	}
	if len(m.log) != maxLogEntries {
		t.Fatalf("len(log) = %d, want %d", len(m.log), maxLogEntries)
	}
}

func TestRenderLogReturnsNewestLast(t *testing.T) {
	m := &model{}
	m.appendLog(false, "first")
	m.appendLog(true, "second")
	m.appendLog(false, "third")

	lines := m.renderLog(2)
	if len(lines) != 2 {
		t.Fatalf("len(lines) = %d, want 2", len(lines))
	}
	if !strings.Contains(lines[0], "second") || !strings.Contains(lines[1], "third") {
		t.Fatalf("lines = %v, want [second, third]", lines)
	}
}

func TestRenderLogZeroOrNegativeBudget(t *testing.T) {
	m := &model{}
	m.appendLog(false, "event")
	if lines := m.renderLog(0); lines != nil {
		t.Fatalf("renderLog(0) = %v, want nil", lines)
	}
	if lines := m.renderLog(-5); lines != nil {
		t.Fatalf("renderLog(-5) = %v, want nil", lines)
	}
}

func TestApplyRefreshLogsCorrectionsWithoutCorrectedPrefix(t *testing.T) {
	m := newTestModel(t, "Plug 1")
	m.applyRefresh(refreshMsg{
		applied: []correctionResult{{correction: correction{plugIdx: 0, target: true}}},
	})
	if len(m.log) != 1 {
		t.Fatalf("len(log) = %d, want 1", len(m.log))
	}
	if strings.Contains(m.log[0].text, "corrected") {
		t.Fatalf("log text = %q, should not contain %q", m.log[0].text, "corrected")
	}
	if m.log[0].text != "Plug 1 → on" {
		t.Fatalf("log text = %q, want %q", m.log[0].text, "Plug 1 → on")
	}
	if m.log[0].isErr {
		t.Fatal("expected a successful correction to not be flagged as an error")
	}
}

func TestApplyRefreshLogsFailedCorrectionAsError(t *testing.T) {
	m := newTestModel(t, "Plug 1")
	m.applyRefresh(refreshMsg{
		applied: []correctionResult{{correction: correction{plugIdx: 0, target: false}, err: errors.New("timeout")}},
	})
	if len(m.log) != 1 || !m.log[0].isErr {
		t.Fatalf("log = %+v, want one error entry", m.log)
	}
	if !strings.Contains(m.log[0].text, "timeout") {
		t.Fatalf("log text = %q, want it to mention the error", m.log[0].text)
	}
}
