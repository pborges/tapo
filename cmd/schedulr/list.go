package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	listTitleStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	listMutedStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	listCursorStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	listOnStyle       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	listOffStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	listDisabledStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203"))
	listErrorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
)

const (
	stateColWidth  = 4  // " off", "  on", "  …?"
	powerColWidth  = 8  // "  12.3 W", "   —    "
	renameMaxRunes = 40 // generous cap so a typo-fueled paste can't run away
)

// renameState holds the in-progress text while renaming the plug at
// plugIdx from the list screen.
type renameState struct {
	plugIdx int
	input   string
}

func (m model) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit
	case "up", "k":
		if m.selected > 0 {
			m.selected--
		}
	case "down", "j":
		if m.selected+1 < len(m.plugs) {
			m.selected++
		}
	case " ":
		if m.selected < len(m.plugs) {
			sched := m.plugs[m.selected].schedule
			sched.Enabled = !sched.Enabled
			m.save()
		}
	case "enter":
		if m.selected < len(m.plugs) {
			m.mode = viewEdit
			m.edit = newEditState(m.selected, m.plugs[m.selected].schedule)
		}
	case "r":
		if m.selected < len(m.plugs) {
			m.mode = viewRename
			m.rename = renameState{plugIdx: m.selected, input: m.plugs[m.selected].alias}
		}
	}
	return m, nil
}

func (m model) updateRename(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.mode = viewList
	case tea.KeyEnter:
		m.mode = viewList
		plugIdx := m.rename.plugIdx
		alias := strings.TrimSpace(m.rename.input)
		if alias == "" || (plugIdx < len(m.plugs) && alias == m.plugs[plugIdx].alias) {
			return m, nil
		}
		m.status = "Renaming…"
		return m, m.renameCmd(plugIdx, alias)
	case tea.KeyBackspace, tea.KeyCtrlH:
		if r := []rune(m.rename.input); len(r) > 0 {
			m.rename.input = string(r[:len(r)-1])
		}
	case tea.KeyRunes:
		if len([]rune(m.rename.input))+len(msg.Runes) <= renameMaxRunes {
			m.rename.input += string(msg.Runes)
		}
	case tea.KeySpace:
		if len([]rune(m.rename.input)) < renameMaxRunes {
			m.rename.input += " "
		}
	}
	return m, nil
}

func (m model) viewList() string {
	var lines []string
	lines = append(lines, listTitleStyle.Render("schedulr — deterministic plug scheduler"))

	if len(m.plugs) == 0 {
		lines = append(lines, "", m.status)
		for i, addr := range m.stripAddrs {
			if i < len(m.stripErrs) && m.stripErrs[i] != nil {
				lines = append(lines, listErrorStyle.Render(addr+": "+m.stripErrs[i].Error()))
			}
		}
		lines = append(lines, "", listMutedStyle.Render("q quit"))
		return strings.Join(lines, "\n")
	}

	now := time.Now()
	nameWidth := 0
	for _, p := range m.plugs {
		if len(p.alias) > nameWidth {
			nameWidth = len(p.alias)
		}
	}
	if m.mode == viewRename && len([]rune(m.rename.input)) > nameWidth {
		nameWidth = len([]rune(m.rename.input))
	}
	if nameWidth > 24 {
		nameWidth = 24
	}

	// " " + cursor(1) + " " + enabled(1) + " " + name + " " + state + " " + power + "  " + bar
	prefixWidth := 1 + 1 + 1 + 1 + nameWidth + 1 + stateColWidth + 1 + powerColWidth + 2
	visible := m.width - prefixWidth - 2 // 2 for scroll marks either side of the bar
	if visible < 10 {
		visible = 10
	}
	// Always centered on the current time rather than a stored scroll
	// position, so the window keeps up with "now" on its own.
	scroll := clampBarScroll(nowColumn(now)-visible/2, visible)
	leftMark, rightMark := scrollMarks(scroll, visible)

	for i, p := range m.plugs {
		selected := i == m.selected
		renaming := m.mode == viewRename && i == m.rename.plugIdx

		var nameField string
		if renaming {
			text := truncate(m.rename.input, nameWidth)
			nameField = lipgloss.NewStyle().Underline(true).Bold(true).Render(fmt.Sprintf("%-*s", nameWidth, text))
		} else {
			nameField = fmt.Sprintf("%-*s", nameWidth, truncate(p.alias, nameWidth))
		}

		stateText := " off"
		stateStyle := listOffStyle
		if !p.seen {
			stateText, stateStyle = "  …?", listMutedStyle
		} else if p.on {
			stateText, stateStyle = "  on", listOnStyle
		}

		powerText := "   —    "
		if p.power != nil {
			powerText = fmt.Sprintf("%6.1f W", *p.power)
		}

		enabledStyle := listDisabledStyle
		if p.schedule.Enabled {
			enabledStyle = listOnStyle
		}

		bar := renderBar(p.schedule, scroll, visible, now, p.schedule.Enabled)

		cursor := " "
		if selected {
			cursor = listCursorStyle.Render("▶")
		}

		line := fmt.Sprintf(" %s %s %s %s %s  %s%s%s",
			cursor, enabledStyle.Render("●"), nameField, stateStyle.Render(stateText), powerText, leftMark, bar, rightMark)
		lines = append(lines, line)
	}

	lines = append(lines, strings.Repeat(" ", prefixWidth)+" "+listMutedStyle.Render(hourRuler(scroll, visible)))
	lines = append(lines, strings.Repeat(" ", prefixWidth)+" "+listMutedStyle.Render(scrollRangeLabel(scroll, visible)))

	for i, addr := range m.stripAddrs {
		if i < len(m.stripErrs) && m.stripErrs[i] != nil {
			lines = append(lines, listErrorStyle.Render(fmt.Sprintf("%s: %s", addr, m.stripErrs[i].Error())))
		}
	}
	if m.status != "" {
		lines = append(lines, "", listMutedStyle.Render(m.status))
	}
	if m.err != nil {
		lines = append(lines, listErrorStyle.Render("save: "+m.err.Error()))
	}
	if m.mode == viewRename {
		lines = append(lines, "", listMutedStyle.Render("type a new name · enter save · esc cancel"))
	} else {
		lines = append(lines, "", listMutedStyle.Render("↑/↓ select · space enable/disable · enter edit schedule · r rename · q quit"))
	}

	// The correction log fills whatever rows are left in the window.
	if logLines := m.renderLog(m.height - len(lines) - 1); len(logLines) > 0 {
		lines = append(lines, "")
		lines = append(lines, logLines...)
	}

	return strings.Join(lines, "\n")
}

// renderLog returns the most recent up-to-n correction log entries,
// newest last, timestamped.
func (m model) renderLog(n int) []string {
	if n <= 0 || len(m.log) == 0 {
		return nil
	}
	if n > len(m.log) {
		n = len(m.log)
	}
	entries := m.log[len(m.log)-n:]
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		text := e.at.Format("15:04:05") + "  " + e.text
		if e.isErr {
			text = listErrorStyle.Render(text)
		} else {
			text = listMutedStyle.Render(text)
		}
		out = append(out, text)
	}
	return out
}

func truncate(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}
