package ui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"fcards/internal/sync"
)

type TagCount struct {
	Tag string
	Due int
	New int
}

// DashboardModel is the home screen: queue snapshot, streak, tag breakdown.
type DashboardModel struct {
	theme    *Theme
	styles   *Styles
	loaded   bool
	counts   sync.Counts
	streak   int
	total    int
	tagRows  []TagCount
	nextDue  *time.Time
	selected []string
}

func NewDashboard(t *Theme, s *Styles) DashboardModel {
	return DashboardModel{theme: t, styles: s}
}

func (m *DashboardModel) UpdateData(svc *sync.Service) {
	if svc == nil {
		return
	}
	now := timeNow()
	m.counts = svc.Counts(now)
	m.streak = svc.Streak(now)
	m.total = len(svc.Cards)
	m.loaded = true
	m.tagRows = tagBreakdown(svc, now)
	m.selected = append([]string(nil), svc.TagSelection()...)
	for _, c := range svc.Cards {
		if c.DueAt != nil && c.DueAt.After(now) {
			if m.nextDue == nil || c.DueAt.Before(*m.nextDue) {
				t := *c.DueAt
				m.nextDue = &t
			}
		}
	}
}

func tagBreakdown(svc *sync.Service, now time.Time) []TagCount {
	byTag := map[string]*TagCount{}
	for _, c := range svc.Cards {
		tags := c.Tags
		if len(tags) == 0 {
			tags = []string{"untagged"}
		}
		for _, tag := range tags {
			tc, ok := byTag[tag]
			if !ok {
				tc = &TagCount{Tag: tag}
				byTag[tag] = tc
			}
			switch {
			case c.DueAt != nil && !c.DueAt.After(now):
				tc.Due++
			case c.DueAt == nil:
				tc.New++
			}
		}
	}
	out := make([]TagCount, 0, len(byTag))
	for _, tc := range byTag {
		out = append(out, *tc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Due != out[j].Due {
			return out[i].Due > out[j].Due
		}
		return out[i].New > out[j].New
	})
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func (m DashboardModel) View(w, h int) string {
	var b strings.Builder
	b.WriteString("\n")

	streak := fmt.Sprintf("%dd", m.streak)
	if m.streak == 1 {
		streak = "1d"
	}
	line1 := fmt.Sprintf("  %s   %s   %s   %s",
		stat(m.styles, "due", m.counts.Due+m.counts.RecentDue, m.theme.Red),
		stat(m.styles, "new", m.counts.New, m.theme.Accent),
		stat(m.styles, "today", m.counts.ReviewedToday, m.theme.Green),
		stat(m.styles, "streak", streak, m.theme.Yellow),
	)
	b.WriteString(line1 + "\n")

	if len(m.selected) > 0 {
		shown := m.selected
		more := ""
		if len(shown) > 5 {
			shown = shown[:5]
			more = fmt.Sprintf(" +%d", len(m.selected)-5)
		}
		b.WriteString("  " + m.styles.Success.Render("studying: "+strings.Join(shown, ", ")) +
			m.styles.Dim.Render(more+" · f to edit") + "\n")
	}
	b.WriteString("\n")

	if m.total == 0 && m.loaded {
		b.WriteString("  " + m.styles.Dim.Render("no cards in this workspace yet") + "\n")
		b.WriteString("  " + m.styles.Dim.Render("add cards on the web or phone app, then press R to sync") + "\n")
	}

	if len(m.tagRows) > 0 {
		b.WriteString("  " + m.styles.Header.Render("decks by tag") + "\n")
		widest := 0
		for _, r := range m.tagRows {
			if len(r.Tag) > widest {
				widest = len(r.Tag)
			}
		}
		if widest > 28 {
			widest = 28
		}
		for _, r := range m.tagRows {
			tag := r.Tag
			if len(tag) > widest {
				tag = tag[:widest-1] + "…"
			}
			if m.isSelectedTag(r.Tag) {
				tag = m.styles.Success.Render("● ") + tag
			} else {
				tag = "  " + tag
			}
			parts := []string{}
			if r.Due > 0 {
				parts = append(parts, m.styles.Error.Render(fmt.Sprintf("%d due", r.Due)))
			}
			if r.New > 0 {
				parts = append(parts, m.styles.Warn.Render(fmt.Sprintf("%d new", r.New)))
			}
			state := m.styles.Dim.Render("clear")
			if len(parts) > 0 {
				state = strings.Join(parts, " ")
			}
			b.WriteString(fmt.Sprintf("    %s  %s\n", tag+strings.Repeat(" ", max(0, widest+3-lipgloss.Width(tag))), state))
		}
	}

	if m.counts.Due+m.counts.RecentDue == 0 && m.nextDue != nil {
		b.WriteString("\n  " + m.styles.Dim.Render(
			"next card "+humanDelta(timeNow(), *m.nextDue)))
	}
	return b.String()
}

func (m DashboardModel) isSelectedTag(tag string) bool {
	for _, t := range m.selected {
		if t == tag {
			return true
		}
	}
	return false
}

func stat(s *Styles, label string, value any, color lipgloss.Color) string {
	v := fmt.Sprint(value)
	return s.Dim.Render(label) + " " + lipgloss.NewStyle().Bold(true).Foreground(color).Render(v)
}

func humanDelta(from, to time.Time) string {
	d := to.Sub(from)
	switch {
	case d < time.Minute:
		return "in <1m"
	case d < time.Hour:
		return fmt.Sprintf("in %dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("in %dh", int(d.Hours()))
	default:
		return fmt.Sprintf("in %dd", int(d.Hours()/24))
	}
}
