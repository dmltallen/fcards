package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"fcards/internal/media"
	"fcards/internal/scheduler"
	"fcards/internal/sync"
)

type reviewSubmittedMsg struct{ err error }

// imageReadyMsg delivers a rendered terminal block for one card image.
type imageReadyMsg struct {
	cardID  string
	assetID string
	block   string
	err     error
}

// reviewFinishedMsg tells the app a session step completed (for dash refresh).
type reviewFinishedMsg struct{ err error }

// imageKickMsg nudges the review screen to (re)consider pending image loads.
type imageKickMsg struct{}

func imageKick() tea.Cmd {
	return func() tea.Msg { return imageKickMsg{} }
}

// ReviewModel runs one review session over a snapshot queue.
type ReviewModel struct {
	theme  *Theme
	styles *Styles
	svc    *sync.Service

	queue    []sync.Card
	idx      int
	revealed bool
	busy     bool

	renderer *media.Renderer
	images   map[string]string // "cardID|assetID" -> rendered terminal block
	pending  map[string]bool
	skipped  int

	sessionReviewed int
	sessionAgain    int
	lastRating      scheduler.Rating
	previews        [4]string
	finished        bool
	nextDue         *time.Time
}

func NewReview(t *Theme, s *Styles) ReviewModel {
	return ReviewModel{theme: t, styles: s}
}

func (m *ReviewModel) Begin(svc *sync.Service) {
	m.svc = svc
	m.renderer = media.NewRenderer(svc.Client, svc.Config.WorkspaceID)
	m.images = map[string]string{}
	m.pending = map[string]bool{}
	m.queue = svc.Queue(timeNow())
	m.idx = 0
	m.revealed = false
	m.busy = false
	m.sessionReviewed = 0
	m.sessionAgain = 0
	m.skipped = 0
	m.finished = false
	m.computePreviews()
}

// SetCards refreshes data without killing an active session.
func (m *ReviewModel) SetCards(svc *sync.Service) {
	m.svc = svc
	if m.renderer == nil {
		m.renderer = media.NewRenderer(svc.Client, svc.Config.WorkspaceID)
	}
	if m.images == nil {
		m.images = map[string]string{}
		m.pending = map[string]bool{}
	}
	if m.finished || len(m.queue) == 0 {
		m.queue = svc.Queue(timeNow())
		m.idx = 0
		m.finished = false
		m.computePreviews()
	}
}

// applyImage stores a finished image render and clears its pending mark.
func (m *ReviewModel) applyImage(msg imageReadyMsg) {
	if m.images == nil {
		m.images = map[string]string{}
		m.pending = map[string]bool{}
	}
	delete(m.pending, msg.cardID+"|"+msg.assetID)
	if msg.err == nil {
		m.images[msg.cardID+"|"+msg.assetID] = msg.block
		return
	}
	// Failed renders leave a visible placeholder so decks with broken media
	// still read cleanly.
	m.images[msg.cardID+"|"+msg.assetID] = m.styles.Dim.Render("[image unavailable]")
}

// imageCmds kicks off async fetch+render for any images on the current card
// that are neither cached nor in flight.
func (m ReviewModel) imageCmds(card *sync.Card, includeBack bool) tea.Cmd {
	if m.renderer == nil || !m.renderer.Available() || card == nil {
		return nil
	}
	var cmds []tea.Cmd
	text := card.Front
	if includeBack {
		text += "\n" + card.Back
	}
	for _, assetID := range media.AssetIDs(text) {
		key := card.CardID + "|" + assetID
		if _, ok := m.images[key]; ok {
			continue
		}
		if m.pending[key] {
			continue
		}
		if m.pending == nil {
			m.pending = map[string]bool{}
		}
		m.pending[key] = true
		renderer, cardID := m.renderer, card.CardID
		cmds = append(cmds, func() tea.Msg {
			path, err := renderer.FetchCached(assetID)
			if err != nil {
				return imageReadyMsg{cardID: cardID, assetID: assetID, err: err}
			}
			block, err := renderer.RenderBlock(path, 36, 10)
			return imageReadyMsg{cardID: cardID, assetID: assetID, block: block, err: err}
		})
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// imageBlocksCached joins already-rendered image blocks for a text's asset refs.
func (m ReviewModel) imageBlocksCached(card *sync.Card, text string) string {
	if m.renderer == nil || card == nil {
		return ""
	}
	var blocks []string
	for _, assetID := range media.AssetIDs(text) {
		if block, ok := m.images[card.CardID+"|"+assetID]; ok {
			blocks = append(blocks, block)
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	return strings.Join(blocks, "\n")
}

func (m *ReviewModel) computePreviews() {
	if m.svc == nil || m.idx >= len(m.queue) {
		return
	}
	now := timeNow()
	card := m.queue[m.idx]
	sc := card.ToSchedulerCard()
	for r := scheduler.Again; r <= scheduler.Easy; r++ {
		sched, err := scheduler.ComputeReviewSchedule(sc, m.svc.Settings, r, now)
		if err != nil {
			m.previews[r] = "?"
			continue
		}
		if sched.FsrsCardState == scheduler.StateLearning || sched.FsrsCardState == scheduler.StateRelearning {
			delta := sched.DueAt.Sub(now)
			m.previews[r] = fmt.Sprintf("%dm", int(delta.Minutes()+0.5))
			if int(delta.Minutes()+0.5) <= 0 {
				m.previews[r] = "<1m"
			}
		} else {
			m.previews[r] = fmt.Sprintf("%dd", sched.FsrsScheduledDays)
		}
	}
}

func (m ReviewModel) current() *sync.Card {
	if m.idx < len(m.queue) {
		return &m.queue[m.idx]
	}
	return nil
}

// Update routes messages; it also kicks off async image renders for whatever
// the current card is showing.
func (m ReviewModel) Update(msg tea.Msg) (ReviewModel, tea.Cmd) {
	if ready, ok := msg.(imageReadyMsg); ok {
		m.applyImage(ready)
		return m, nil
	}
	return m.update(msg)
}

func (m ReviewModel) update(msg tea.Msg) (ReviewModel, tea.Cmd) {
	m2, cmd := m.updateInner(msg)
	if !m2.finished && m2.renderer != nil {
		if cur := m2.current(); cur != nil {
			if ic := m2.imageCmds(cur, m2.revealed); ic != nil {
				cmd = tea.Batch(cmd, ic)
			}
		}
	}
	return m2, cmd
}

func (m ReviewModel) updateInner(msg tea.Msg) (ReviewModel, tea.Cmd) {
	switch msg := msg.(type) {
	case reviewSubmittedMsg:
		m.busy = false
		offlineQueued := msg.err != nil && strings.HasPrefix(msg.err.Error(), "queued offline")
		if msg.err != nil && !offlineQueued {
			// Hard failure: stay on this card; the app surfaces the error.
			return m, func() tea.Msg { return reviewFinishedMsg{err: msg.err} }
		}
		if m.lastRating == scheduler.Again {
			m.sessionAgain++
		}
		m.sessionReviewed++
		m.idx++
		m.revealed = false
		if m.idx >= len(m.queue) {
			m.finished = true
		} else {
			m.computePreviews()
		}
		return m, func() tea.Msg { return reviewFinishedMsg{err: msg.err} }
	}

	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "s":
			// Skip: defer this card to the end of the session queue.
			if m.busy || m.finished || m.current() == nil || len(m.queue) <= 1 {
				return m, nil
			}
			wasLast := m.idx == len(m.queue)-1
			cur := m.queue[m.idx]
			rest := append(m.queue[:m.idx], m.queue[m.idx+1:]...)
			m.queue = append(rest, cur)
			if wasLast {
				m.idx = 0
			}
			m.revealed = false
			m.skipped++
			m.computePreviews()
			return m, nil
		case " ":
			if !m.revealed && !m.busy && m.current() != nil {
				m.revealed = true
				return m, nil
			}
		case "1", "2", "3", "4":
			if !m.revealed || m.busy || m.current() == nil {
				return m, nil
			}
			rating := scheduler.Rating(key.String()[0] - '1')
			card := *m.current()
			m.busy = true
			m.lastRating = rating
			return m, m.submitCmd(card, rating)
		}
	}
	return m, nil
}

func (m ReviewModel) submitCmd(card sync.Card, rating scheduler.Rating) tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		live := svc.CardByID(card.CardID)
		if live == nil {
			return reviewSubmittedMsg{err: fmt.Errorf("card vanished during sync — press R")}
		}
		_, err := svc.Review(live, rating, timeNow())
		return reviewSubmittedMsg{err: err}
	}
}

func (m ReviewModel) View(w, h int) string {
	var b strings.Builder
	b.WriteString("\n")

	if m.finished || m.current() == nil {
		b.WriteString("  " + m.styles.Success.Render("queue clear") + m.styles.Dim.Render(" · nice work") + "\n\n")
		b.WriteString(fmt.Sprintf("  reviewed this session: %s", m.styles.Selected.Render(fmt.Sprint(m.sessionReviewed))))
		if m.nextDue != nil {
			b.WriteString(m.styles.Dim.Render(" · next card " + humanDelta(timeNow(), *m.nextDue)))
		}
		b.WriteString("\n")
		return b.String()
	}

	card := m.current()
	progress := fmt.Sprintf("%d/%d", m.idx+1, len(m.queue))
	if m.skipped > 0 {
		progress += m.styles.Dim.Render(fmt.Sprintf(" · %d skipped", m.skipped))
	}
	meta := fmt.Sprintf("  %s %s  ·  session +%s",
		m.styles.Dim.Render(progress),
		m.stateChip(card),
		m.styles.Success.Render(fmt.Sprint(m.sessionReviewed)),
	)
	b.WriteString(meta + "\n\n")

	inner := w - 8
	if inner < 20 {
		inner = 20
	}
	frontText := media.StripImages(card.Front)
	backText := media.StripImages(card.Back)
	content := wrap(frontText, inner)
	if block := m.imageBlocksCached(card, card.Front); block != "" {
		content += "\n\n" + indent(block, 1)
	}
	if m.revealed {
		if block := m.imageBlocksCached(card, card.Back); block != "" {
			content += "\n" + block
		}
		content = content + "\n\n" + m.styles.Dim.Render(strings.Repeat("─", max(4, inner-2))) + "\n\n" + wrap(backText, inner)
	}
	box := m.styles.CardBox.Render(content)
	b.WriteString(indent(box, 2) + "\n")

	if !m.revealed {
		b.WriteString("\n  " + m.styles.Dim.Render("space reveal · s skip") + "\n")
		return b.String()
	}

	labels := [4]string{"again", "hard", "good", "easy"}
	colors := [4]lipgloss.Color{m.theme.Red, m.theme.Yellow, m.theme.Green, m.theme.Cyan}
	var parts []string
	for i := 0; i < 4; i++ {
		key := lipgloss.NewStyle().Bold(true).Foreground(colors[i]).Render(fmt.Sprintf("[%d]", i+1))
		label := lipgloss.NewStyle().Foreground(colors[i]).Render(labels[i])
		interval := m.styles.Dim.Render(m.previews[i])
		parts = append(parts, key+" "+label+" "+interval)
	}
	sep := "  "
	if w < 90 {
		sep = "\n  "
	}
	b.WriteString("\n  " + strings.Join(parts, sep) + "\n")
	if m.busy {
		b.WriteString("\n  " + m.styles.Warn.Render("saving…"))
	}
	return b.String()
}

func (m ReviewModel) stateChip(c *sync.Card) string {
	now := timeNow()
	switch {
	case c.DueAt == nil:
		return m.styles.Warn.Render("new")
	case !c.DueAt.After(now):
		return m.styles.Error.Render("due")
	default:
		return m.styles.Dim.Render("review")
	}
}

// --- shared helpers ---

func wrap(s string, width int) string {
	if width < 10 {
		width = 10
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		line := ""
		for _, word := range strings.Fields(para) {
			if line == "" {
				line = word
			} else if lipgloss.Width(line)+1+lipgloss.Width(word) <= width {
				line += " " + word
			} else {
				out = append(out, line)
				line = word
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func indent(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i := range lines {
		if lines[i] != "" {
			lines[i] = pad + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
