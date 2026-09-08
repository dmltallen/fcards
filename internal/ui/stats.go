package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"fcards/internal/sync"
)

// StatsModel shows streak, rating distribution, and a 12-week heatmap.
type StatsModel struct {
	theme  *Theme
	styles *Styles
	loaded bool

	streak   int
	today    int
	allTime  int
	byDay    map[string]int
	byRating [4]int
}

func NewStats(t *Theme, s *Styles) StatsModel {
	return StatsModel{theme: t, styles: s, byDay: map[string]int{}}
}

func (m *StatsModel) UpdateData(svc *sync.Service) {
	if svc == nil {
		return
	}
	now := timeNow()
	m.loaded = true
	m.byDay = map[string]int{}
	m.byRating = [4]int{}
	m.allTime = len(svc.History)
	for _, ev := range svc.History {
		key := ev.ReviewedAtUTC.UTC().Format("2006-01-02")
		m.byDay[key]++
		if ev.Rating >= 0 && ev.Rating <= 3 {
			m.byRating[ev.Rating]++
		}
	}
	m.today = m.byDay[now.UTC().Format("2006-01-02")]
	m.streak = svc.Streak(now)
}

func (m StatsModel) View(w, h int) string {
	var b strings.Builder
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("  %s   %s   %s\n\n",
		stat(m.styles, "streak", fmt.Sprintf("%dd", m.streak), m.theme.Yellow),
		stat(m.styles, "today", m.today, m.theme.Green),
		stat(m.styles, "reviews", m.allTime, m.theme.Accent),
	))

	labels := [4]string{"again", "hard", "good", "easy"}
	colors := [4]lipgloss.Color{m.theme.Red, m.theme.Yellow, m.theme.Green, m.theme.Cyan}
	maxCount := 1
	for _, v := range m.byRating {
		if v > maxCount {
			maxCount = v
		}
	}
	barWidth := 24
	b.WriteString("  " + m.styles.Header.Render("rating distribution") + "\n")
	for i := 0; i < 4; i++ {
		filled := m.byRating[i] * barWidth / maxCount
		bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
		styled := lipgloss.NewStyle().Foreground(colors[i]).Render(bar)
		b.WriteString(fmt.Sprintf("    %-6s %s %d\n", labels[i], styled, m.byRating[i]))
	}

	b.WriteString("\n  " + m.styles.Header.Render("last 12 weeks") + "\n")
	b.WriteString(m.renderHeatmap(w))
	return b.String()
}

func (m StatsModel) renderHeatmap(w int) string {
	now := timeNow()
	// Columns = weeks, rows = weekdays (Mon..Sun). 12 weeks.
	weeks := 12
	chars := []string{"·", "░", "▒", "▓", "█"}

	// Find the Monday of the week 11 weeks ago.
	today := now.UTC()
	offset := (int(today.Weekday()) + 6) % 7 // days since Monday
	start := today.AddDate(0, 0, -(offset + (weeks-1)*7))

	var rows [7][]string
	for wk := 0; wk < weeks; wk++ {
		for d := 0; d < 7; d++ {
			day := start.AddDate(0, 0, wk*7+d)
			key := day.Format("2006-01-02")
			count := m.byDay[key]
			idx := 0
			switch {
			case count >= 20:
				idx = 4
			case count >= 10:
				idx = 3
			case count >= 5:
				idx = 2
			case count >= 1:
				idx = 1
			}
			cell := chars[idx]
			if idx == 0 {
				rows[d] = append(rows[d], m.styles.Dim.Render(cell))
			} else {
				rows[d] = append(rows[d], m.styles.Selected.Render(cell))
			}
		}
	}
	var out []string
	for d := 0; d < 7; d++ {
		out = append(out, "    "+strings.Join(rows[d], ""))
	}
	out = append(out, "\n    "+m.styles.Dim.Render(fmt.Sprintf("%s → %s · · = 0", start.Format("Jan 2"), now.Format("Jan 2"))))
	return strings.Join(out, "\n")
}
