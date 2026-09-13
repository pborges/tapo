package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const fieldsPerSegment = 4 // start, end, gap, repeat-count

const (
	editRowCount = 3                     // rows stacked vertically to cover the day
	editRowHours = 24 / editRowCount     // hours per row
	editRowCols  = editRowHours * 60 / 5 // bar columns per row (5m resolution)
)

var (
	editTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	editMutedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	editFocusStyle  = lipgloss.NewStyle().Reverse(true).Bold(true)
	editAddRowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// editState tracks a 2D cursor over the segment table: segIdx selects the
// row (a segment, or len(Segments) for the "+ add segment" row) and field
// selects the column within that row (ignored on the add row).
type editState struct {
	plugIdx int
	sched   *plugSchedule
	segIdx  int
	field   int
}

func newEditState(plugIdx int, sched *plugSchedule) editState {
	return editState{plugIdx: plugIdx, sched: sched}
}

func (e *editState) addRowIndex() int { return len(e.sched.Segments) }

func (e *editState) clamp() {
	if e.segIdx < 0 {
		e.segIdx = 0
	}
	if max := e.addRowIndex(); e.segIdx > max {
		e.segIdx = max
	}
	if e.field < 0 {
		e.field = 0
	}
	if e.field > fieldsPerSegment-1 {
		e.field = fieldsPerSegment - 1
	}
}

func (e *editState) focused() (segIdx, field int, isAddRow bool) {
	e.clamp()
	if e.segIdx == e.addRowIndex() {
		return -1, -1, true
	}
	return e.segIdx, e.field, false
}

// moveSegment moves the row cursor between segments (and the add row).
func (e *editState) moveSegment(delta int) {
	e.segIdx += delta
	e.clamp()
}

// moveField moves the column cursor between a segment's inputs.
func (e *editState) moveField(delta int) {
	e.field += delta
	e.clamp()
}

func (e *editState) addSegment() {
	start := 0
	if n := len(e.sched.Segments); n > 0 {
		last := e.sched.Segments[n-1]
		start = last.EndMinute
		if start > minutesPerDay-15 {
			start = 0
		}
	}
	end := start + 15
	if end > minutesPerDay {
		end = minutesPerDay
	}
	e.sched.Segments = append(e.sched.Segments, &segment{StartMinute: start, EndMinute: end})
	e.segIdx = len(e.sched.Segments) - 1
	e.field = 0
}

func (e *editState) removeFocusedSegment() {
	segIdx, _, isAddRow := e.focused()
	if isAddRow || segIdx < 0 || segIdx >= len(e.sched.Segments) {
		return
	}
	e.sched.Segments = append(e.sched.Segments[:segIdx], e.sched.Segments[segIdx+1:]...)
	e.clamp()
}

// adjust changes the focused field's value by step increments of dir (+1/-1).
func (e *editState) adjust(dir int) {
	segIdx, field, isAddRow := e.focused()
	if isAddRow {
		return
	}
	seg := e.sched.Segments[segIdx]
	switch field {
	case 0: // start
		newStart := clampInt(seg.StartMinute+5*dir, 0, minutesPerDay-5)
		if newStart > seg.EndMinute-5 {
			seg.EndMinute = clampInt(newStart+5, 5, minutesPerDay)
		}
		seg.StartMinute = newStart
	case 1: // end
		newEnd := clampInt(seg.EndMinute+5*dir, 5, minutesPerDay)
		if newEnd < seg.StartMinute+5 {
			seg.StartMinute = clampInt(newEnd-5, 0, minutesPerDay-5)
		}
		seg.EndMinute = newEnd
	case 2: // gap between pulses
		seg.GapMinutes = clampInt(seg.GapMinutes+5*dir, 0, 180)
	case 3: // repeat count
		seg.RepeatCount = clampInt(seg.RepeatCount+dir, 0, 999)
	}
}

func (m model) updateEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		m.save()
		m.mode = viewList
	case "up", "k":
		m.edit.moveSegment(-1)
	case "down", "j":
		m.edit.moveSegment(1)
	case "left", "h":
		m.edit.moveField(-1)
	case "right", "l":
		m.edit.moveField(1)
	case "+", "=":
		m.edit.adjust(1)
		m.save()
	case "-", "_":
		m.edit.adjust(-1)
		m.save()
	case "enter":
		if _, _, isAddRow := m.edit.focused(); isAddRow {
			m.edit.addSegment()
			m.save()
		}
	case "a":
		m.edit.addSegment()
		m.save()
	case "d", "x":
		m.edit.removeFocusedSegment()
		m.save()
	case "E":
		m.edit.sched.Enabled = !m.edit.sched.Enabled
		m.save()
	}
	return m, nil
}

func (m model) viewEdit() string {
	var b strings.Builder
	p := m.plugs[m.edit.plugIdx]
	sched := m.edit.sched
	now := time.Now()

	enabled := "disabled"
	style := editMutedStyle
	if sched.Enabled {
		enabled = "enabled"
		style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	}
	fmt.Fprintf(&b, "%s   %s\n", editTitleStyle.Render("Edit schedule — "+p.alias), style.Render(enabled))
	b.WriteString(editMutedStyle.Render("up/down select segment · left/right select field · +/- adjust value") + "\n")
	b.WriteString(editMutedStyle.Render("a add · d remove · E enable/disable · esc back") + "\n\n")

	// Stack the day as editRowCount fixed-span rows instead of one long
	// horizontally-scrolled bar, trading the terminal's usual excess of
	// vertical space for more per-hour resolution.
	visible := m.width - 1
	if visible > editRowCols {
		visible = editRowCols
	}
	if visible < 10 {
		visible = 10
	}
	segIdx := segIndexOf(m.edit)
	for r := 0; r < editRowCount; r++ {
		rowScroll := r * editRowCols
		label := formatMinute(absMinute(rowScroll)) + "-" + formatMinute(absMinute(rowScroll+editRowCols))
		b.WriteString(editMutedStyle.Render(label) + "\n")
		b.WriteString(renderSegmentBar(sched.Segments, segIdx, rowScroll, visible, now) + "\n")
		b.WriteString(editMutedStyle.Render(hourRuler(rowScroll, visible)) + "\n\n")
	}

	if len(sched.Segments) == 0 {
		b.WriteString(editMutedStyle.Render("no segments yet") + "\n\n")
	}
	for i, seg := range sched.Segments {
		b.WriteString(m.renderSegmentRow(i, seg) + "\n")
	}
	addRowLine := "+ add segment"
	if _, _, isAddRow := m.edit.focused(); isAddRow {
		addRowLine = editFocusStyle.Render(addRowLine)
	} else {
		addRowLine = editAddRowStyle.Render(addRowLine)
	}
	b.WriteString("\n" + addRowLine + "\n")

	if m.err != nil {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("save: "+m.err.Error()) + "\n")
	}
	return b.String()
}

func segIndexOf(e editState) int {
	segIdx, _, isAddRow := e.focused()
	if isAddRow {
		return -1
	}
	return segIdx
}

func (m model) renderSegmentRow(i int, seg *segment) string {
	segIdx, field, isAddRow := m.edit.focused()
	focusedHere := !isAddRow && segIdx == i

	part := func(f int, text string) string {
		if focusedHere && field == f {
			return editFocusStyle.Render(text)
		}
		return text
	}

	swatch := lipgloss.NewStyle().Foreground(segmentColor(i)).Render("■")
	start := part(0, formatMinute(seg.StartMinute))
	end := part(1, formatMinute(seg.EndMinute))
	duration := seg.duration()

	gap := "no repeat"
	if seg.GapMinutes > 0 {
		gap = fmt.Sprintf("%dm", seg.GapMinutes)
	}
	gap = part(2, gap)

	count := "rest of day"
	if seg.RepeatCount > 0 {
		count = fmt.Sprintf("%d", seg.RepeatCount)
	}
	count = part(3, count)

	runs := seg.runCount()
	return fmt.Sprintf(" %s %2d  %s -> %s (%dm)   gap %s x %s  (%d runs)",
		swatch, i+1, start, end, duration, gap, count, runs)
}
