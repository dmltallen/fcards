package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"fcards/internal/sync"
)

// TagItem is one row in the study picker: a deck (toggles its tags) or a tag.
type TagItem struct {
	Label    string
	Tags     []string
	IsDeck   bool
	Due      int
	New      int
	Selected bool
}

// TagPicker is the keyboard-first study selector with autocomplete.
type TagPicker struct {
	theme  *Theme
	styles *Styles
	svc    *sync.Service

	filter    textinput.Model
	items     []TagItem
	shown     []int
	cursor    int
	statusMsg string
}

func NewTagPicker(t *Theme, s *Styles) TagPicker {
	f := textinput.New()
	f.Placeholder = "type to find decks or tags…"
	f.Prompt = "/"
	return TagPicker{theme: t, styles: s, filter: f}
}

// tagStat aggregates per-tag queue counts.
type tagStat struct {
	due int
	new int
}

// Rebuild recomputes items from the service's decks, tags, and selection.
func (m *TagPicker) Rebuild(svc *sync.Service) {
	m.svc = svc
	m.items = nil
	if svc == nil {
		m.refilter()
		return
	}
	now := timeNow()
	selection := map[string]bool{}
	for _, t := range svc.TagSelection() {
		selection[t] = true
	}

	// Per-tag counts across live cards.
	stats := map[string]*tagStat{}
	for _, c := range svc.Cards {
		if c.Deleted {
			continue
		}
		for _, tag := range c.Tags {
			st, ok := stats[tag]
			if !ok {
				st = &tagStat{}
				stats[tag] = st
			}
			switch {
			case c.DueAt != nil && !c.DueAt.After(now):
				st.due++
			case c.DueAt == nil:
				st.new++
			}
		}
	}

	// Decks first (a deck toggles all its tags at once).
	decks := append([]sync.Deck(nil), svc.Decks...)
	sort.Slice(decks, func(i, j int) bool {
		di, dj := deckDue(decks[i], stats), deckDue(decks[j], stats)
		if di != dj {
			return di > dj
		}
		return decks[i].Name < decks[j].Name
	})
	for _, d := range decks {
		if len(d.Tags) == 0 {
			continue
		}
		item := TagItem{Label: d.Name, Tags: d.Tags, IsDeck: true}
		for _, t := range d.Tags {
			if st, ok := stats[t]; ok {
				item.Due += st.due
				item.New += st.new
			}
		}
		item.Selected = containsAll(selection, d.Tags)
		m.items = append(m.items, item)
	}

	// Then individual tags.
	names := make([]string, 0, len(stats))
	for name := range stats {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		si, sj := stats[names[i]], stats[names[j]]
		if si.due != sj.due {
			return si.due > sj.due
		}
		if si.new != sj.new {
			return si.new > sj.new
		}
		return names[i] < names[j]
	})
	for _, name := range names {
		st := stats[name]
		m.items = append(m.items, TagItem{
			Label:    name,
			Tags:     []string{name},
			Due:      st.due,
			New:      st.new,
			Selected: selection[name],
		})
	}
	m.refilter()
}

func deckDue(d sync.Deck, stats map[string]*tagStat) int {
	total := 0
	for _, t := range d.Tags {
		if st, ok := stats[t]; ok {
			total += st.due
		}
	}
	return total
}

func containsAll(set map[string]bool, tags []string) bool {
	for _, t := range tags {
		if !set[t] {
			return false
		}
	}
	return len(tags) > 0
}

func (m *TagPicker) refilter() {
	needle := strings.ToLower(m.filter.Value())
	m.shown = m.shown[:0]
	for i, item := range m.items {
		if needle == "" || strings.Contains(strings.ToLower(item.Label), needle) {
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

// Toggle flips the item under the cursor in the service's selection.
func (m *TagPicker) Toggle() {
	if m.svc == nil || m.cursor >= len(m.shown) {
		return
	}
	item := m.items[m.shown[m.cursor]]
	current := map[string]bool{}
	for _, t := range m.svc.TagSelection() {
		current[t] = true
	}
	if containsAll(current, item.Tags) {
		for _, t := range item.Tags {
			delete(current, t)
		}
		m.statusMsg = "removed " + item.Label
	} else {
		for _, t := range item.Tags {
			current[t] = true
		}
		m.statusMsg = "studying " + item.Label
	}
	tags := make([]string, 0, len(current))
	for t := range current {
		tags = append(tags, t)
	}
	_ = m.svc.SetTagSelection(tags)
	m.Rebuild(m.svc)
}

// ClearAll empties the selection.
func (m *TagPicker) ClearAll() {
	if m.svc == nil {
		return
	}
	_ = m.svc.SetTagSelection(nil)
	m.Rebuild(m.svc)
	m.statusMsg = "selection cleared"
}

func (m TagPicker) Update(msg tea.Msg) (TagPicker, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "up", "ctrl+p":
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil
		case "down", "ctrl+n":
			if m.cursor < len(m.shown)-1 {
				m.cursor++
			}
			return m, nil
		case "enter":
			m.Toggle()
			return m, nil
		case "ctrl+x":
			m.ClearAll()
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.filter, cmd = m.filter.Update(msg)
	m.refilter()
	return m, cmd
}

func (m TagPicker) View(w, h int) string {
	var b strings.Builder
	title := m.styles.Title.Render("study what?") + m.styles.Dim.Render("  decks and tags · enter toggles")
	b.WriteString(title + "\n")
	b.WriteString("  " + m.filter.View() + "\n")

	if sel := m.selectedLine(); sel != "" {
		b.WriteString("  " + sel + "\n")
	}
	if m.statusMsg != "" {
		b.WriteString("  " + m.styles.Success.Render(m.statusMsg) + "\n")
	}
	b.WriteString("\n")

	rows := h - 10
	if rows < 3 {
		rows = 3
	}
	if len(m.shown) == 0 {
		b.WriteString("  " + m.styles.Dim.Render("nothing matches") + "\n")
	} else {
		start := m.cursor - rows/2
		if start < 0 {
			start = 0
		}
		end := start + rows
		if end > len(m.shown) {
			end = len(m.shown)
		}
		for idx := start; idx < end; idx++ {
			item := m.items[m.shown[idx]]
			cursorMark := "  "
			if idx == m.cursor {
				cursorMark = m.styles.Selected.Render("> ")
			}
			check := "[ ]"
			if item.Selected {
				check = m.styles.Success.Render("[x]")
			}
			kind := m.styles.Dim.Render("#")
			if item.IsDeck {
				kind = m.styles.Warn.Render("deck")
			}
			label := truncate(item.Label, max(12, w-40))
			counts := m.styles.Dim.Render(countSummary(item))
			b.WriteString(fmt.Sprintf("  %s%s %s %-s %s\n", cursorMark, check, kind, label, counts))
		}
		if len(m.shown) > end-start {
			b.WriteString(m.styles.Dim.Render(fmt.Sprintf("  %d of %d shown\n", end-start, len(m.shown))))
		}
	}
	b.WriteString("\n  " + m.styles.Dim.Render("enter toggle · ctrl+x clear · type to filter · esc done"))
	return b.String()
}

func (m TagPicker) selectedLine() string {
	if m.svc == nil {
		return ""
	}
	sel := m.svc.TagSelection()
	if len(sel) == 0 {
		return m.styles.Dim.Render("studying: everything")
	}
	shown := sel
	more := ""
	if len(sel) > 4 {
		shown = sel[:4]
		more = fmt.Sprintf(" +%d", len(sel)-4)
	}
	return m.styles.Success.Render("studying: "+strings.Join(shown, ", ")) + m.styles.Dim.Render(more)
}

func countSummary(item TagItem) string {
	var parts []string
	if item.Due > 0 {
		parts = append(parts, fmt.Sprintf("%d due", item.Due))
	}
	if item.New > 0 {
		parts = append(parts, fmt.Sprintf("%d new", item.New))
	}
	if len(parts) == 0 {
		return "clear"
	}
	return strings.Join(parts, " ")
}

var _ = lipgloss.NewStyle
