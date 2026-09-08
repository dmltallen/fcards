package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"fcards/internal/sync"
)

// BrowseModel is the searchable card list.
type BrowseModel struct {
	theme  *Theme
	styles *Styles

	filter    textinput.Model
	filtering bool
	cards     []sync.Card
	shown     []int
	cursor    int
	detail    bool
}

func NewBrowse(t *Theme, s *Styles) BrowseModel {
	f := textinput.New()
	f.Placeholder = "filter cards…"
	f.Prompt = "/"
	return BrowseModel{theme: t, styles: s, filter: f, detail: false}
}

func (m *BrowseModel) SetCards(svc *sync.Service) {
	if svc == nil {
		m.cards = nil
		return
	}
	m.cards = append([]sync.Card(nil), svc.Cards...)
	m.refilter()
}

func (m BrowseModel) IsFiltering() bool { return m.filtering }

func (m *BrowseModel) refilter() {
	needle := strings.ToLower(m.filter.Value())
	m.shown = m.shown[:0]
	for i, c := range m.cards {
		if needle == "" || strings.Contains(strings.ToLower(c.Front), needle) ||
			strings.Contains(strings.ToLower(c.Back), needle) ||
			strings.Contains(strings.ToLower(strings.Join(c.Tags, " ")), needle) {
			m.shown = append(m.shown, i)
		}
	}
	if m.cursor >= len(m.shown) {
		m.cursor = len(m.shown) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *BrowseModel) Focus() tea.Cmd {
	m.filtering = true
	m.filter.Focus()
	return textinput.Blink
}

func (m BrowseModel) Update(msg tea.Msg) (BrowseModel, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.detail {
				m.detail = false
				return m, nil
			}
			if m.filtering {
				m.filtering = false
				m.filter.Blur()
				return m, nil
			}
			return m, nil
		case "enter":
			if len(m.shown) > 0 {
				m.detail = !m.detail
			}
			return m, nil
		case "up", "k":
			if !m.filtering && m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down", "j":
			if !m.filtering && m.cursor < len(m.shown)-1 {
				m.cursor++
			}
			return m, nil
		case "/":
			if !m.filtering {
				return m, m.Focus()
			}
		}
	}

	if m.filtering {
		var cmd tea.Cmd
		var model tea.Model
		m.filter, cmd = m.filter.Update(msg)
		_ = model
		m.refilter()
		return m, cmd
	}
	return m, nil
}

func (m BrowseModel) View(w, h int) string {
	var b strings.Builder
	b.WriteString("\n")
	if m.filtering || m.filter.Value() != "" {
		b.WriteString("  " + m.filter.View() + "\n\n")
	} else {
		b.WriteString("  " + m.styles.Dim.Render("press / to filter") + "\n\n")
	}

	if len(m.shown) == 0 {
		b.WriteString("  " + m.styles.Dim.Render("no cards match") + "\n")
		return b.String()
	}

	rows := h - 8
	if rows < 3 {
		rows = 3
	}
	start := m.cursor - rows/2
	if start < 0 {
		start = 0
	}
	end := start + rows
	if end > len(m.shown) {
		end = len(m.shown)
	}

	if m.detail && m.cursor < len(m.shown) {
		c := m.cards[m.shown[m.cursor]]
		inner := w - 8
		if inner < 20 {
			inner = 20
		}
		due := "new"
		if c.DueAt != nil {
			due = humanDelta(timeNow(), *c.DueAt)
			if !c.DueAt.After(timeNow()) {
				due = "due " + due
			}
		}
		meta := fmt.Sprintf("state %s · reps %d · lapses %d · due %s",
			string(c.State), c.Reps, c.Lapses, due)
		content := wrap(c.Front, inner) + "\n\n" +
			m.styles.Dim.Render(strings.Repeat("─", max(4, inner-2))) + "\n\n" +
			wrap(c.Back, inner) + "\n\n" + m.styles.Dim.Render(meta)
		b.WriteString(indent(m.styles.CardBox.Render(content), 2))
		b.WriteString("\n\n  " + m.styles.Dim.Render("enter close · esc back"))
		return b.String()
	}

	for i := start; i < end; i++ {
		c := m.cards[m.shown[i]]
		line := "  "
		if i == m.cursor {
			line += m.styles.Selected.Render("> ")
		} else {
			line += "  "
		}
		front := c.Front
		if front == "" {
			front = "(empty)"
		}
		maxw := w - 34
		if maxw < 12 {
			maxw = 12
		}
		front = truncate(front, maxw)
		line += front
		line += "  " + m.stateChipBrowse(&c)
		b.WriteString(line + "\n")
	}
	b.WriteString("\n  " + m.styles.Dim.Render(fmt.Sprintf("%d of %d cards", len(m.shown), len(m.cards))))
	return b.String()
}

func (m BrowseModel) stateChipBrowse(c *sync.Card) string {
	now := timeNow()
	switch {
	case c.DueAt == nil:
		return m.styles.Warn.Render("new")
	case !c.DueAt.After(now):
		return m.styles.Error.Render("due")
	default:
		return m.styles.Dim.Render(humanDelta(now, *c.DueAt))
	}
}

func truncate(s string, w int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if lipgloss.Width(s) <= w {
		return s
	}
	runes := []rune(s)
	if w <= 1 {
		return "…"
	}
	return string(runes[:w-1]) + "…"
}
