package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"fcards/internal/sync"
)

func keyRunes(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestReviewSkipMovesCardToEnd(t *testing.T) {
	m := ReviewModel{}
	m.queue = []sync.Card{{CardID: "a"}, {CardID: "b"}, {CardID: "c"}}
	m.idx = 0
	m.revealed = true

	nm, _ := m.Update(keyRunes('s'))
	m2 := nm

	if m2.skipped != 1 {
		t.Errorf("skipped = %d, want 1", m2.skipped)
	}
	if m2.idx != 0 {
		t.Errorf("idx = %d, want 0 (next card is now first)", m2.idx)
	}
	if got := m2.queue[2].CardID; got != "a" {
		t.Errorf("skipped card position = %s, want moved to end", got)
	}
	if m2.queue[0].CardID != "b" || m2.queue[1].CardID != "c" {
		t.Errorf("remaining order wrong: %v", m2.queue)
	}
	if m2.revealed {
		t.Error("skip should hide the revealed answer")
	}
	if m2.current().CardID != "b" {
		t.Errorf("current = %s, want b", m2.current().CardID)
	}
}

func TestReviewSkipLastWrapsToFront(t *testing.T) {
	m := ReviewModel{}
	m.queue = []sync.Card{{CardID: "x"}, {CardID: "y"}}
	m.idx = 1 // skipping the last card

	nm, _ := m.Update(keyRunes('s'))
	m2 := nm

	if m2.idx != 0 {
		t.Errorf("idx = %d, want 0 (wrap to front)", m2.idx)
	}
	if m2.queue[1].CardID != "y" {
		t.Errorf("skipped card should sit at end, got %s at end", m2.queue[1].CardID)
	}
	if m2.current().CardID != "x" {
		t.Errorf("current = %s, want x", m2.current().CardID)
	}
}

func TestReviewSkipSingleCardNoop(t *testing.T) {
	m := ReviewModel{}
	m.queue = []sync.Card{{CardID: "only"}}
	m.idx = 0

	nm, _ := m.Update(keyRunes('s'))
	m2 := nm

	if m2.skipped != 0 {
		t.Errorf("skipped = %d, want 0 (single card no-op)", m2.skipped)
	}
	if m2.current().CardID != "only" {
		t.Errorf("current = %s, want only", m2.current().CardID)
	}
}
