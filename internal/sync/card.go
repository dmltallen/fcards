package sync

import (
	"os"

	"fcards/internal/scheduler"
)

// ToSchedulerCard converts to the scheduler's persisted-state view.
func (c *Card) ToSchedulerCard() *scheduler.Card {
	return &scheduler.Card{
		CardID:             c.CardID,
		Reps:               c.Reps,
		Lapses:             c.Lapses,
		FsrsCardState:      c.State,
		FsrsStepIndex:      c.StepIndex,
		FsrsStability:      c.Stability,
		FsrsDifficulty:     c.Difficulty,
		FsrsLastReviewedAt: c.LastReviewedAt,
		FsrsScheduledDays:  c.ScheduledDays,
	}
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
