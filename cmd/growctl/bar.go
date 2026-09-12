package main

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	barOnStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	barOffStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	barNowStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)
	barSegPalette = []lipgloss.Color{"210", "157", "222", "117", "183", "150"}
)

const barOnRune = '█'
const barOffRune = '·'
const barNowRune = '┃'

// barTotalWidth is the fixed resolution of the 24h timeline, shared by the
// plug list and the schedule editor: one column per 5 minutes, matching the
// snap granularity segments are edited at.
const barTotalWidth = minutesPerDay / 5

// clampBarScroll keeps a horizontal scroll offset (in columns) within the
// range that still shows a full page of barTotalWidth given a viewport of
// visible columns.
func clampBarScroll(scroll, visible int) int {
	max := barTotalWidth - visible
	if max < 0 {
		max = 0
	}
	if scroll > max {
		scroll = max
	}
	if scroll < 0 {
		scroll = 0
	}
	return scroll
}

func absMinute(col int) int { return col * minutesPerDay / barTotalWidth }

func nowColumn(now time.Time) int { return minuteOfDay(now) * barTotalWidth / minutesPerDay }

// renderBar draws `visible` columns of a schedule's on/off state, starting at
// column `scroll` of the full barTotalWidth timeline. When the schedule is
// disabled, "on" cells are drawn faint rather than in the live vivid green,
// since growctl isn't actually enforcing that plan right now.
func renderBar(sched *plugSchedule, scroll, visible int, now time.Time, enabled bool) string {
	onStyle := barOnStyle
	if !enabled {
		onStyle = onStyle.Faint(true)
	}
	var b strings.Builder
	nowCol := nowColumn(now)
	for i := 0; i < visible; i++ {
		col := scroll + i
		on := sched != nil && sched.isOnAtMinute(absMinute(col))
		isNow := col == nowCol
		switch {
		case isNow && on:
			b.WriteString(barNowStyle.Render(string(barOnRune)))
		case isNow:
			b.WriteString(barNowStyle.Render(string(barNowRune)))
		case on:
			b.WriteString(onStyle.Render(string(barOnRune)))
		default:
			b.WriteString(barOffStyle.Render(string(barOffRune)))
		}
	}
	return b.String()
}

// renderSegmentBar draws a windowed view of the editor's bar, coloring each
// segment's pulses with its own color (by index into barSegPalette) so
// overlapping segments stay distinguishable, and highlighting the selected
// segment's cells.
func renderSegmentBar(segs []*segment, selected int, scroll, visible int, now time.Time) string {
	cellSeg := make([]int, visible)
	for i := range cellSeg {
		col := scroll + i
		minute := absMinute(col)
		cellSeg[i] = -1
		for si, seg := range segs {
			active := false
			for _, pulse := range seg.pulses() {
				if minute >= pulse[0] && minute < pulse[1] {
					active = true
					break
				}
			}
			if !active {
				continue
			}
			if cellSeg[i] == -1 || si == selected {
				cellSeg[i] = si
			}
		}
	}

	nowCol := nowColumn(now)

	var b strings.Builder
	for i := 0; i < visible; i++ {
		col := scroll + i
		si := cellSeg[i]
		switch {
		case col == nowCol && si >= 0:
			b.WriteString(barNowStyle.Underline(true).Render(string(barOnRune)))
		case col == nowCol:
			b.WriteString(barNowStyle.Underline(true).Render(string(barNowRune)))
		case si >= 0:
			style := lipgloss.NewStyle().Foreground(barSegPalette[si%len(barSegPalette)])
			if si == selected {
				style = style.Bold(true)
			}
			b.WriteString(style.Render(string(barOnRune)))
		default:
			b.WriteString(barOffStyle.Render(string(barOffRune)))
		}
	}
	return b.String()
}

// hourRuler renders "00 01 02 ..." axis labels for the visible window, with
// a tick every hour (12 bar columns apart at the 5-minute resolution).
func hourRuler(scroll, visible int) string {
	cells := make([]byte, visible)
	for i := range cells {
		cells[i] = ' '
	}
	for hour := 0; hour < 24; hour++ {
		col := hour * barTotalWidth / 24
		i := col - scroll
		if i < 0 || i >= visible {
			continue
		}
		label := formatMinute(hour * 60)[:2]
		for j := 0; j < len(label) && i+j < visible; j++ {
			cells[i+j] = label[j]
		}
	}
	return string(cells)
}

// scrollMarks returns single-character indicators for whether the window is
// scrolled past its start/end, for display just outside the bar.
func scrollMarks(scroll, visible int) (left, right string) {
	left, right = " ", " "
	if scroll > 0 {
		left = "‹"
	}
	if scroll+visible < barTotalWidth {
		right = "›"
	}
	return left, right
}

// scrollRangeLabel describes the visible time window, e.g. "07:15-13:55 of 24h".
func scrollRangeLabel(scroll, visible int) string {
	start := formatMinute(absMinute(scroll))
	end := formatMinute(absMinute(scroll + visible))
	return start + "-" + end + " of 24h"
}

func segmentColor(index int) lipgloss.Color {
	return barSegPalette[index%len(barSegPalette)]
}
