package scheduler

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
	"time"
)

type vectorFile []vectorCase

type vectorCase struct {
	Name            string         `json:"name"`
	CardID          string         `json:"cardId"`
	Settings        vectorSettings `json:"settings"`
	Reviews         []vectorReview `json:"reviews"`
	Expected        vectorState    `json:"expected"`
	RebuiltExpected vectorState    `json:"rebuiltExpected"`
}

type vectorSettings struct {
	Algorithm              string  `json:"algorithm"`
	DesiredRetention       float64 `json:"desiredRetention"`
	LearningStepsMinutes   []int   `json:"learningStepsMinutes"`
	RelearningStepsMinutes []int   `json:"relearningStepsMinutes"`
	MaximumIntervalDays    int     `json:"maximumIntervalDays"`
	EnableFuzz             bool    `json:"enableFuzz"`
}

type vectorReview struct {
	At     string `json:"at"`
	Rating int    `json:"rating"`
}

type vectorState struct {
	DueAt              *string  `json:"dueAt"`
	Reps               int      `json:"reps"`
	Lapses             int      `json:"lapses"`
	FsrsCardState      string   `json:"fsrsCardState"`
	FsrsStepIndex      *int     `json:"fsrsStepIndex"`
	FsrsStability      *float64 `json:"fsrsStability"`
	FsrsDifficulty     *float64 `json:"fsrsDifficulty"`
	FsrsLastReviewedAt *string  `json:"fsrsLastReviewedAt"`
	FsrsScheduledDays  *int     `json:"fsrsScheduledDays"`
}

func loadVectors(t *testing.T) vectorFile {
	t.Helper()
	data, err := os.ReadFile("../../testdata/fsrs-full-vectors.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var v vectorFile
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	return v
}

func parseISO(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}

func mustParseISO(t *testing.T, s string) time.Time {
	t.Helper()
	p, err := parseISO(s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return p
}

func sameTime(a time.Time, b *string) bool {
	if b == nil {
		return false
	}
	p, err := parseISO(*b)
	if err != nil {
		return false
	}
	return a.UnixMilli() == p.UnixMilli()
}

func samePtrTime(a *time.Time, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return sameTime(*a, b)
}

func floatEq(a float64, b *float64) bool {
	if b == nil {
		return false
	}
	return a == *b || math.Abs(a-*b) < 1e-9
}

func ptrFloatEq(a *float64, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b || math.Abs(*a-*b) < 1e-9
}

func intPtrEq(a *int, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func compareState(t *testing.T, tc vectorCase, got Schedule, want vectorState) {
	t.Helper()
	if !sameTime(got.DueAt, want.DueAt) {
		t.Errorf("dueAt: got %s want %s", got.DueAt.UTC().Format(time.RFC3339Nano), deref(want.DueAt))
	}
	if got.Reps != want.Reps {
		t.Errorf("reps: got %d want %d", got.Reps, want.Reps)
	}
	if got.Lapses != want.Lapses {
		t.Errorf("lapses: got %d want %d", got.Lapses, want.Lapses)
	}
	if string(got.FsrsCardState) != want.FsrsCardState {
		t.Errorf("state: got %s want %s", got.FsrsCardState, want.FsrsCardState)
	}
	if !intPtrEq(got.FsrsStepIndex, want.FsrsStepIndex) {
		t.Errorf("stepIndex: got %v want %v", got.FsrsStepIndex, want.FsrsStepIndex)
	}
	if !floatEq(got.FsrsStability, want.FsrsStability) {
		t.Errorf("stability: got %.12f want %v", got.FsrsStability, derefF(want.FsrsStability))
	}
	if !floatEq(got.FsrsDifficulty, want.FsrsDifficulty) {
		t.Errorf("difficulty: got %.12f want %v", got.FsrsDifficulty, derefF(want.FsrsDifficulty))
	}
	if !samePtrTime(&got.FsrsLastReviewedAt, want.FsrsLastReviewedAt) {
		t.Errorf("lastReviewedAt: got %s want %s", got.FsrsLastReviewedAt.UTC().Format(time.RFC3339Nano), deref(want.FsrsLastReviewedAt))
	}
	if want.FsrsScheduledDays != nil && got.FsrsScheduledDays != *want.FsrsScheduledDays {
		t.Errorf("scheduledDays: got %d want %d", got.FsrsScheduledDays, *want.FsrsScheduledDays)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func derefF(f *float64) string {
	if f == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%.12f", *f)
}

// TestGoldenVectors replays each vector's review sequence from an empty card
// and compares the final live state plus the rebuilt-from-history state.
func TestGoldenVectors(t *testing.T) {
	for _, tc := range loadVectors(t) {
		t.Run(tc.Name, func(t *testing.T) {
			settings := &Settings{
				Algorithm:              tc.Settings.Algorithm,
				DesiredRetention:       tc.Settings.DesiredRetention,
				LearningStepsMinutes:   tc.Settings.LearningStepsMinutes,
				RelearningStepsMinutes: tc.Settings.RelearningStepsMinutes,
				MaximumIntervalDays:    tc.Settings.MaximumIntervalDays,
				EnableFuzz:             tc.Settings.EnableFuzz,
			}

			card := EmptyCard(tc.CardID)
			var last Schedule
			var events []ReviewEvent
			for _, r := range tc.Reviews {
				at := mustParseISO(t, r.At)
				sched, err := ComputeReviewSchedule(&card, settings, Rating(r.Rating), at)
				if err != nil {
					t.Fatalf("review %s: %v", r.At, err)
				}
				last = sched
				events = append(events, ReviewEvent{Rating: Rating(r.Rating), ReviewedAt: at})

				due := sched.DueAt
				stab := sched.FsrsStability
				diff := sched.FsrsDifficulty
				sdays := sched.FsrsScheduledDays
				cardAt := sched.FsrsLastReviewedAt
				card = Card{
					CardID:             tc.CardID,
					Reps:               sched.Reps,
					Lapses:             sched.Lapses,
					FsrsCardState:      sched.FsrsCardState,
					FsrsStepIndex:      sched.FsrsStepIndex,
					FsrsStability:      &stab,
					FsrsDifficulty:     &diff,
					FsrsLastReviewedAt: &cardAt,
					FsrsScheduledDays:  &sdays,
				}
				_ = due
			}

			t.Run("live", func(t *testing.T) {
				compareState(t, tc, last, tc.Expected)
			})

			t.Run("rebuilt", func(t *testing.T) {
				rebuilt, err := RebuildCardScheduleState(tc.CardID, settings, events)
				if err != nil {
					t.Fatalf("rebuild: %v", err)
				}
				want := tc.RebuiltExpected
				if !samePtrTime(rebuilt.DueAt, want.DueAt) {
					t.Errorf("dueAt: got %v want %s", rebuilt.DueAt, deref(want.DueAt))
				}
				if rebuilt.Reps != want.Reps {
					t.Errorf("reps: got %d want %d", rebuilt.Reps, want.Reps)
				}
				if rebuilt.Lapses != want.Lapses {
					t.Errorf("lapses: got %d want %d", rebuilt.Lapses, want.Lapses)
				}
				if string(rebuilt.FsrsCardState) != want.FsrsCardState {
					t.Errorf("state: got %s want %s", rebuilt.FsrsCardState, want.FsrsCardState)
				}
				if !intPtrEq(rebuilt.FsrsStepIndex, want.FsrsStepIndex) {
					t.Errorf("stepIndex: got %v want %v", rebuilt.FsrsStepIndex, want.FsrsStepIndex)
				}
				if !ptrFloatEq(rebuilt.FsrsStability, want.FsrsStability) {
					t.Errorf("stability: got %v want %v", rebuilt.FsrsStability, derefF(want.FsrsStability))
				}
				if !ptrFloatEq(rebuilt.FsrsDifficulty, want.FsrsDifficulty) {
					t.Errorf("difficulty: got %v want %v", rebuilt.FsrsDifficulty, derefF(want.FsrsDifficulty))
				}
				if !samePtrTime(rebuilt.FsrsLastReviewedAt, want.FsrsLastReviewedAt) {
					t.Errorf("lastReviewedAt: got %v want %s", rebuilt.FsrsLastReviewedAt, deref(want.FsrsLastReviewedAt))
				}
				if want.FsrsScheduledDays != nil && (rebuilt.FsrsScheduledDays == nil || *rebuilt.FsrsScheduledDays != *want.FsrsScheduledDays) {
					t.Errorf("scheduledDays: got %v want %d", rebuilt.FsrsScheduledDays, *want.FsrsScheduledDays)
				}
			})
		})
	}
}
