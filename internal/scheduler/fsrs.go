// Package scheduler is a faithful Go port of the flashcards-open-source-app
// FSRS scheduler (apps/backend/src/scheduling/index.ts, ts-fsrs 5.2.3 flow).
// Parity is enforced against tests/fsrs-full-vectors.json (see vectors_test.go).
package scheduler

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"
)

// Rating is the user-facing 0..3 answer scale (Again, Hard, Good, Easy).
type Rating int

const (
	Again Rating = iota
	Hard
	Good
	Easy
)

// CardState mirrors fsrs_card_state persisted values.
type CardState string

const (
	StateNew        CardState = "new"
	StateLearning   CardState = "learning"
	StateReview     CardState = "review"
	StateRelearning CardState = "relearning"
)

// Settings mirrors WorkspaceSchedulerConfig.
type Settings struct {
	Algorithm              string
	DesiredRetention       float64
	LearningStepsMinutes   []int
	RelearningStepsMinutes []int
	MaximumIntervalDays    int
	EnableFuzz             bool
}

// Card mirrors ReviewableCardScheduleState (persisted card scheduler state).
type Card struct {
	CardID             string
	Reps               int
	Lapses             int
	FsrsCardState      CardState
	FsrsStepIndex      *int
	FsrsStability      *float64
	FsrsDifficulty     *float64
	FsrsLastReviewedAt *time.Time
	FsrsScheduledDays  *int
}

// Schedule mirrors ReviewSchedule, the result of one review computation.
type Schedule struct {
	DueAt              time.Time
	Reps               int
	Lapses             int
	FsrsCardState      CardState
	FsrsStepIndex      *int
	FsrsStability      float64
	FsrsDifficulty     float64
	FsrsLastReviewedAt time.Time
	FsrsScheduledDays  int
}

// ReviewEvent mirrors ReviewHistoryEvent for rebuilds.
type ReviewEvent struct {
	Rating     Rating
	ReviewedAt time.Time
}

// EmptyCard returns the untouched state for a new card.
func EmptyCard(cardID string) Card {
	return Card{CardID: cardID, FsrsCardState: StateNew}
}

var defaultW = [21]float64{
	0.212, 1.2931, 2.3065, 8.2956, 6.4133, 0.8334, 3.0194, 0.001, 1.8722,
	0.1666, 0.796, 1.4835, 0.0614, 0.2629, 1.6483, 0.6014, 1.8729, 0.5425,
	0.0912, 0.0658, 0.1542,
}

const sMin = 0.001
const w17W18Ceiling = 2.0

type fuzzRange struct {
	start, end, factor float64
}

var fuzzRanges = []fuzzRange{
	{2.5, 7.0, 0.15},
	{7.0, 20.0, 0.1},
	{20.0, math.Inf(1), 0.05},
}

var decay = -defaultW[20]

var factor = roundTo8(math.Exp(math.Pow(decay, -1)*math.Log(0.9)) - 1)

// --- JS numeric semantics helpers ---

// roundTo8 mirrors Number.parseFloat(value.toFixed(8)).
func roundTo8(v float64) float64 {
	s := strconv.FormatFloat(v, 'f', 8, 64)
	out, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return v
	}
	return out
}

// jsRound mirrors Math.round (half toward positive infinity).
func jsRound(v float64) float64 {
	return math.Floor(v + 0.5)
}

// toUint32 mirrors the JS >>> 0 coercion (trunc then mod 2^32).
func toUint32(f float64) uint32 {
	m := math.Mod(math.Trunc(f), 4294967296.0)
	if m < 0 {
		m += 4294967296.0
	}
	return uint32(m)
}

// clamp mirrors Math.min(Math.max(v, lo), hi).
func clamp(v, lo, hi float64) float64 {
	return math.Min(math.Max(v, lo), hi)
}

// jsNumberString mirrors String(number) for the value ranges used in seeds:
// shortest round-trip decimal. Zero (including -0) prints as "0".
func jsNumberString(v float64) string {
	if v == 0 {
		return "0"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// --- Alea PRNG (deterministic fuzz), ported exactly ---

type alea struct {
	c      float64
	s0, s1 float64
	s2     float64
}

func newAlea(seed string) *alea {
	mash := newMash()
	a := &alea{c: 1}
	a.s0 = mash(" ")
	a.s1 = mash(" ")
	a.s2 = mash(" ")
	a.s0 -= mash(seed)
	if a.s0 < 0 {
		a.s0++
	}
	a.s1 -= mash(seed)
	if a.s1 < 0 {
		a.s1++
	}
	a.s2 -= mash(seed)
	if a.s2 < 0 {
		a.s2++
	}
	return a
}

func (a *alea) next() float64 {
	t := 2091639*a.s0 + a.c*2.3283064365386963e-10
	a.s0 = a.s1
	a.s1 = a.s2
	a.c = float64(toUint32(t)) // JS: t | 0 (t is always small and non-negative here)
	a.s2 = t - a.c
	return a.s2
}

// newMash mirrors createMash.
func newMash() func(string) float64 {
	n := float64(0xefc8249d)
	return func(data string) float64 {
		next := n
		for i := 0; i < len(data); i++ {
			next += float64(data[i]) // seeds are ASCII, so byte == charCodeAt
			h := 0.02519603282416938 * next
			next = float64(toUint32(h))
			h -= next
			h *= next
			next = float64(toUint32(h))
			h -= next
			next += h * 4294967296.0
		}
		n = next
		return float64(toUint32(next)) * 2.3283064365386963e-10
	}
}

// --- time helpers ---

func addMinutes(t time.Time, minutes int) time.Time {
	return t.Add(time.Duration(minutes) * time.Minute)
}

func addDays(t time.Time, days int) time.Time {
	return t.Add(time.Duration(days) * 24 * time.Hour)
}

func dateDiffInDays(lastReviewedAt, now time.Time) (int, error) {
	if now.Before(lastReviewedAt) {
		return 0, fmt.Errorf(
			"review timestamp moved backwards: lastReviewedAt=%s now=%s",
			lastReviewedAt.UTC().Format(time.RFC3339Nano),
			now.UTC().Format(time.RFC3339Nano),
		)
	}
	utcDay := func(t time.Time) time.Time {
		t = t.UTC()
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}
	return int(utcDay(now).Sub(utcDay(lastReviewedAt)).Hours() / 24), nil
}

// --- core math (ported 1:1) ---

func stateRequiresMemory(state CardState) bool { return state != StateNew }

func getIntervalModifier(requestRetention float64) float64 {
	return roundTo8((math.Pow(requestRetention, 1/decay) - 1) / factor)
}

func mapRatingToFsrsGrade(r Rating) int { return int(r) + 1 }

func getStepsForState(s *Settings, state CardState) []int {
	if state == StateRelearning || state == StateReview {
		return s.RelearningStepsMinutes
	}
	return s.LearningStepsMinutes
}

func getCurrentStepIndex(c *Card) int {
	if c.FsrsStepIndex == nil {
		return 0
	}
	return *c.FsrsStepIndex
}

func getLearningStrategyStepIndex(c *Card, grade int) int {
	current := getCurrentStepIndex(c)
	if c.FsrsCardState == StateLearning && grade != 1 && grade != 2 {
		return current + 1
	}
	return current
}

func getHardStepMinutes(steps []int) int {
	if len(steps) == 1 {
		return int(jsRound(float64(steps[0]) * 1.5))
	}
	return int(jsRound(float64(steps[0]+steps[1]) / 2))
}

type learningStepResult struct {
	scheduledMinutes *int
	nextStepIndex    int
}

func getLearningStepResult(s *Settings, c *Card, grade int) (learningStepResult, error) {
	steps := getStepsForState(s, c.FsrsCardState)
	strategyStepIndex := getLearningStrategyStepIndex(c, grade)

	if len(steps) == 0 {
		return learningStepResult{}, errors.New("workspace scheduler steps must not be empty")
	}

	if c.FsrsCardState == StateReview {
		m := steps[0]
		return learningStepResult{scheduledMinutes: &m, nextStepIndex: 0}, nil
	}

	if grade == 1 {
		m := steps[0]
		return learningStepResult{scheduledMinutes: &m, nextStepIndex: 0}, nil
	}

	if grade == 2 {
		m := getHardStepMinutes(steps)
		return learningStepResult{scheduledMinutes: &m, nextStepIndex: strategyStepIndex}, nil
	}

	if grade == 4 {
		return learningStepResult{scheduledMinutes: nil, nextStepIndex: 0}, nil
	}

	nextStepIndex := strategyStepIndex + 1
	if nextStepIndex >= len(steps) {
		return learningStepResult{scheduledMinutes: nil, nextStepIndex: 0}, nil
	}
	m := steps[nextStepIndex]
	return learningStepResult{scheduledMinutes: &m, nextStepIndex: nextStepIndex}, nil
}

func initStability(grade int) float64 {
	return math.Max(defaultW[grade-1], 0.1)
}

func initDifficulty(grade int) float64 {
	return roundTo8(defaultW[4] - math.Exp(float64(grade-1)*defaultW[5]) + 1)
}

func meanReversion(initialDifficulty, currentDifficulty float64) float64 {
	return roundTo8(defaultW[7]*initialDifficulty + (1-defaultW[7])*currentDifficulty)
}

func linearDamping(deltaDifficulty, difficulty float64) float64 {
	return roundTo8(deltaDifficulty * (10 - difficulty) / 9)
}

func nextDifficulty(difficulty float64, grade int) float64 {
	deltaDifficulty := -defaultW[6] * float64(grade-3)
	next := difficulty + linearDamping(deltaDifficulty, difficulty)
	return clamp(meanReversion(initDifficulty(4), next), 1, 10)
}

func forgettingCurve(elapsedDays, stability float64) float64 {
	return roundTo8(math.Pow(1+factor*elapsedDays/stability, decay))
}

func nextRecallStability(difficulty, stability, retrievability float64, grade int) float64 {
	hardPenalty := 1.0
	if grade == 2 {
		hardPenalty = defaultW[15]
	}
	easyBound := 1.0
	if grade == 4 {
		easyBound = defaultW[16]
	}
	return roundTo8(clamp(
		stability*(1+
			math.Exp(defaultW[8])*
				(11-difficulty)*
				math.Pow(stability, -defaultW[9])*
				(math.Exp((1-retrievability)*defaultW[10])-1)*
				hardPenalty*
				easyBound),
		sMin, 36500,
	))
}

func nextForgetStability(difficulty, stability, retrievability float64) float64 {
	return roundTo8(clamp(
		defaultW[11]*
			math.Pow(difficulty, -defaultW[12])*
			(math.Pow(stability+1, defaultW[13])-1)*
			math.Exp((1-retrievability)*defaultW[14]),
		sMin, 36500,
	))
}

func getShortTermWeights(s *Settings) (w17, w18 float64) {
	if len(s.RelearningStepsMinutes) <= 1 {
		return defaultW[17], defaultW[18]
	}
	value := -(math.Log(defaultW[11]) +
		math.Log(math.Pow(2, defaultW[13])-1) +
		defaultW[14]*0.3) / float64(len(s.RelearningStepsMinutes))
	ceiling := clamp(roundTo8(value), 0.01, w17W18Ceiling)
	return clamp(defaultW[17], 0, ceiling), clamp(defaultW[18], 0, ceiling)
}

func nextShortTermStability(stability float64, grade int, s *Settings) float64 {
	w17, w18 := getShortTermWeights(s)
	sinc := math.Pow(stability, -defaultW[19]) *
		math.Exp(w17*(float64(grade)-3+w18))
	if grade >= 3 {
		sinc = math.Max(sinc, 1)
	}
	return roundTo8(clamp(stability*sinc, sMin, 36500))
}

type memoryState struct {
	difficulty float64
	stability  float64
}

func createInitialMemoryState(grade int) memoryState {
	return memoryState{
		stability:  initStability(grade),
		difficulty: clamp(initDifficulty(grade), 1, 10),
	}
}

func computeNextShortTermMemoryState(m memoryState, grade int, s *Settings) memoryState {
	return memoryState{
		stability:  nextShortTermStability(m.stability, grade, s),
		difficulty: nextDifficulty(m.difficulty, grade),
	}
}

func computeNextReviewMemoryState(m memoryState, elapsedDays float64, grade int, s *Settings) memoryState {
	retrievability := forgettingCurve(elapsedDays, m.stability)
	stabilityAfterSuccess := nextRecallStability(m.difficulty, m.stability, retrievability, grade)
	stabilityAfterFailure := nextForgetStability(m.difficulty, m.stability, retrievability)

	nextStability := stabilityAfterSuccess
	if grade == 1 {
		w17, w18 := getShortTermWeights(s)
		nextStabilityMin := m.stability / math.Exp(w17*w18)
		nextStability = clamp(roundTo8(nextStabilityMin), sMin, stabilityAfterFailure)
	}

	return memoryState{
		stability:  nextStability,
		difficulty: nextDifficulty(m.difficulty, grade),
	}
}

func getFuzzRange(interval, elapsedDays float64, maximumInterval int) (minInterval, maxInterval int) {
	delta := 1.0
	for _, r := range fuzzRanges {
		delta += r.factor * math.Max(math.Min(interval, r.end)-r.start, 0)
	}

	clampedInterval := math.Min(interval, float64(maximumInterval))
	minInterval = int(math.Max(2, jsRound(clampedInterval-delta)))
	maxInterval = int(math.Min(jsRound(clampedInterval+delta), float64(maximumInterval)))
	if clampedInterval > elapsedDays {
		minInterval = int(math.Max(float64(minInterval), elapsedDays+1))
	}

	if minInterval > maxInterval {
		minInterval = maxInterval
	}
	return minInterval, maxInterval
}

func getIntervalSeed(now time.Time, reps int, m *memoryState) string {
	var product float64
	if m != nil {
		product = m.difficulty * m.stability
	}
	return fmt.Sprintf("%d_%d_%s", now.UnixMilli(), reps, jsNumberString(product))
}

func nextInterval(stability, elapsedDays float64, s *Settings, intervalSeed string) int {
	intervalModifier := getIntervalModifier(s.DesiredRetention)
	nextRawInterval := int(clamp(jsRound(stability*intervalModifier), 1, float64(s.MaximumIntervalDays)))

	if !s.EnableFuzz || nextRawInterval < 3 {
		return nextRawInterval
	}

	prng := newAlea(intervalSeed)
	fuzzFactor := prng.next()
	minI, maxI := getFuzzRange(float64(nextRawInterval), elapsedDays, s.MaximumIntervalDays)
	return int(math.Floor(fuzzFactor*float64(maxI-minI+1) + float64(minI)))
}

// getMemoryState validates persisted state invariants and returns the memory
// state, or nil for a new card.
func getMemoryState(c *Card) (*memoryState, error) {
	if !stateRequiresMemory(c.FsrsCardState) {
		if c.FsrsStability != nil || c.FsrsDifficulty != nil ||
			c.FsrsLastReviewedAt != nil || c.FsrsScheduledDays != nil || c.FsrsStepIndex != nil {
			return nil, errors.New("new card must not have persisted FSRS state")
		}
		return nil, nil
	}

	if c.FsrsStability == nil || c.FsrsDifficulty == nil ||
		c.FsrsLastReviewedAt == nil || c.FsrsScheduledDays == nil {
		return nil, errors.New("persisted FSRS card state is incomplete")
	}

	if c.FsrsCardState == StateReview && c.FsrsStepIndex != nil {
		return nil, errors.New("review card must not persist fsrsStepIndex")
	}

	if (c.FsrsCardState == StateLearning || c.FsrsCardState == StateRelearning) && c.FsrsStepIndex == nil {
		return nil, errors.New("learning or relearning card is missing fsrsStepIndex")
	}

	return &memoryState{stability: *c.FsrsStability, difficulty: *c.FsrsDifficulty}, nil
}

func buildShortTermSchedule(
	c *Card,
	nextMemoryState memoryState,
	rating Rating,
	now time.Time,
	reps, lapses int,
	s *Settings,
	nextState CardState,
	elapsedDays float64,
	intervalSeed string,
) (Schedule, error) {
	grade := mapRatingToFsrsGrade(rating)
	learningStep, err := getLearningStepResult(s, c, grade)
	if err != nil {
		return Schedule{}, err
	}
	if learningStep.scheduledMinutes == nil {
		return buildGraduatedReviewSchedule(nextMemoryState, now, reps, lapses, s, elapsedDays, intervalSeed), nil
	}

	nextIdx := learningStep.nextStepIndex
	return Schedule{
		DueAt:              addMinutes(now, int(jsRound(float64(*learningStep.scheduledMinutes)))),
		Reps:               reps,
		Lapses:             lapses,
		FsrsCardState:      nextState,
		FsrsStepIndex:      &nextIdx,
		FsrsStability:      nextMemoryState.stability,
		FsrsDifficulty:     nextMemoryState.difficulty,
		FsrsLastReviewedAt: now,
		FsrsScheduledDays:  0,
	}, nil
}

func buildGraduatedReviewSchedule(
	nextMemoryState memoryState,
	now time.Time,
	reps, lapses int,
	s *Settings,
	elapsedDays float64,
	intervalSeed string,
) Schedule {
	scheduledDays := nextInterval(nextMemoryState.stability, elapsedDays, s, intervalSeed)
	return Schedule{
		DueAt:              addDays(now, scheduledDays),
		Reps:               reps,
		Lapses:             lapses,
		FsrsCardState:      StateReview,
		FsrsStepIndex:      nil,
		FsrsStability:      nextMemoryState.stability,
		FsrsDifficulty:     nextMemoryState.difficulty,
		FsrsLastReviewedAt: now,
		FsrsScheduledDays:  scheduledDays,
	}
}

func buildReviewSuccessSchedule(
	now time.Time,
	reps, lapses int,
	s *Settings,
	elapsedDays float64,
	hardMemoryState, goodMemoryState, easyMemoryState memoryState,
	rating Rating,
	intervalSeed string,
) Schedule {
	hardInterval := nextInterval(hardMemoryState.stability, elapsedDays, s, intervalSeed)
	goodInterval := nextInterval(goodMemoryState.stability, elapsedDays, s, intervalSeed)
	if hardInterval > goodInterval {
		hardInterval = goodInterval
	}
	if goodInterval < hardInterval+1 {
		goodInterval = hardInterval + 1
	}
	easyInterval := nextInterval(easyMemoryState.stability, elapsedDays, s, intervalSeed)
	if easyInterval < goodInterval+1 {
		easyInterval = goodInterval + 1
	}

	if rating == Hard {
		return Schedule{
			DueAt: addDays(now, hardInterval), Reps: reps, Lapses: lapses,
			FsrsCardState: StateReview, FsrsStepIndex: nil,
			FsrsStability: hardMemoryState.stability, FsrsDifficulty: hardMemoryState.difficulty,
			FsrsLastReviewedAt: now, FsrsScheduledDays: hardInterval,
		}
	}
	if rating == Good {
		return Schedule{
			DueAt: addDays(now, goodInterval), Reps: reps, Lapses: lapses,
			FsrsCardState: StateReview, FsrsStepIndex: nil,
			FsrsStability: goodMemoryState.stability, FsrsDifficulty: goodMemoryState.difficulty,
			FsrsLastReviewedAt: now, FsrsScheduledDays: goodInterval,
		}
	}
	return Schedule{
		DueAt: addDays(now, easyInterval), Reps: reps, Lapses: lapses,
		FsrsCardState: StateReview, FsrsStepIndex: nil,
		FsrsStability: easyMemoryState.stability, FsrsDifficulty: easyMemoryState.difficulty,
		FsrsLastReviewedAt: now, FsrsScheduledDays: easyInterval,
	}
}

// ComputeReviewSchedule ports computeReviewSchedule exactly.
func ComputeReviewSchedule(c *Card, s *Settings, rating Rating, now time.Time) (Schedule, error) {
	memoryStatePtr, err := getMemoryState(c)
	if err != nil {
		return Schedule{}, err
	}
	grade := mapRatingToFsrsGrade(rating)
	var elapsedDaysF float64
	var elapsedDays int
	if c.FsrsLastReviewedAt != nil {
		elapsedDays, err = dateDiffInDays(*c.FsrsLastReviewedAt, now)
		if err != nil {
			return Schedule{}, err
		}
		elapsedDaysF = float64(elapsedDays)
	}
	reps := c.Reps + 1
	lapses := c.Lapses
	if rating == Again && c.FsrsCardState == StateReview {
		lapses++
	}
	intervalSeed := getIntervalSeed(now, reps, memoryStatePtr)

	if c.FsrsCardState == StateNew {
		nextMemoryState := createInitialMemoryState(grade)
		return buildShortTermSchedule(c, nextMemoryState, rating, now, reps, lapses, s, StateLearning, 0, intervalSeed)
	}

	if memoryStatePtr == nil {
		return Schedule{}, errors.New("persisted FSRS card state is incomplete")
	}

	if c.FsrsCardState == StateLearning || c.FsrsCardState == StateRelearning {
		nextMemoryState := computeNextShortTermMemoryState(*memoryStatePtr, grade, s)
		return buildShortTermSchedule(c, nextMemoryState, rating, now, reps, lapses, s, c.FsrsCardState, elapsedDaysF, intervalSeed)
	}

	nextAgainMemoryState := computeNextReviewMemoryState(*memoryStatePtr, elapsedDaysF, 1, s)
	nextHardMemoryState := computeNextReviewMemoryState(*memoryStatePtr, elapsedDaysF, 2, s)
	nextGoodMemoryState := computeNextReviewMemoryState(*memoryStatePtr, elapsedDaysF, 3, s)
	nextEasyMemoryState := computeNextReviewMemoryState(*memoryStatePtr, elapsedDaysF, 4, s)

	if rating == Again {
		return buildShortTermSchedule(c, nextAgainMemoryState, rating, now, reps, lapses, s, StateRelearning, elapsedDaysF, intervalSeed)
	}

	return buildReviewSuccessSchedule(now, reps, lapses, s, elapsedDaysF,
		nextHardMemoryState, nextGoodMemoryState, nextEasyMemoryState, rating, intervalSeed), nil
}

// RebuiltState mirrors RebuiltCardScheduleState.
type RebuiltState struct {
	DueAt              *time.Time
	Reps               int
	Lapses             int
	FsrsCardState      CardState
	FsrsStepIndex      *int
	FsrsStability      *float64
	FsrsDifficulty     *float64
	FsrsLastReviewedAt *time.Time
	FsrsScheduledDays  *int
}

// RebuildCardScheduleState ports rebuildCardScheduleState.
func RebuildCardScheduleState(cardID string, s *Settings, reviewEvents []ReviewEvent) (RebuiltState, error) {
	if len(reviewEvents) == 0 {
		return RebuiltState{FsrsCardState: StateNew}, nil
	}

	state := EmptyCard(cardID)
	var dueAt *time.Time

	for _, ev := range reviewEvents {
		next, err := ComputeReviewSchedule(&state, s, ev.Rating, ev.ReviewedAt)
		if err != nil {
			return RebuiltState{}, err
		}
		due := next.DueAt
		dueAt = &due
		stab := next.FsrsStability
		diff := next.FsrsDifficulty
		step := next.FsrsStepIndex
		last := next.FsrsLastReviewedAt
		sdays := next.FsrsScheduledDays
		state = Card{
			CardID:             state.CardID,
			Reps:               next.Reps,
			Lapses:             next.Lapses,
			FsrsCardState:      next.FsrsCardState,
			FsrsStepIndex:      step,
			FsrsStability:      &stab,
			FsrsDifficulty:     &diff,
			FsrsLastReviewedAt: &last,
			FsrsScheduledDays:  &sdays,
		}
	}

	return RebuiltState{
		DueAt:              dueAt,
		Reps:               state.Reps,
		Lapses:             state.Lapses,
		FsrsCardState:      state.FsrsCardState,
		FsrsStepIndex:      state.FsrsStepIndex,
		FsrsStability:      state.FsrsStability,
		FsrsDifficulty:     state.FsrsDifficulty,
		FsrsLastReviewedAt: state.FsrsLastReviewedAt,
		FsrsScheduledDays:  state.FsrsScheduledDays,
	}, nil
}

// DefaultSettings returns the product default workspace scheduler config.
func DefaultSettings() *Settings {
	return &Settings{
		Algorithm:              "fsrs-6",
		DesiredRetention:       0.9,
		LearningStepsMinutes:   []int{1, 10},
		RelearningStepsMinutes: []int{10},
		MaximumIntervalDays:    36500,
		EnableFuzz:             true,
	}
}

// ValidateSettings mirrors parseSteps + validateWorkspaceSchedulerSettingsInput.
func ValidateSettings(s *Settings) error {
	if s.DesiredRetention <= 0 || s.DesiredRetention >= 1 {
		return errors.New("desiredRetention must be greater than 0 and less than 1")
	}
	if s.MaximumIntervalDays < 1 {
		return errors.New("maximumIntervalDays must be a positive integer")
	}
	if s.Algorithm != "fsrs-6" {
		return fmt.Errorf("unsupported scheduler algorithm: %s", s.Algorithm)
	}
	for name, steps := range map[string][]int{
		"learningStepsMinutes": s.LearningStepsMinutes, "relearningStepsMinutes": s.RelearningStepsMinutes,
	} {
		if len(steps) == 0 {
			return fmt.Errorf("%s must not be empty", name)
		}
		for i, v := range steps {
			if v <= 0 || v >= 1440 {
				return fmt.Errorf("%s must contain positive integer minutes under 1440", name)
			}
			if i > 0 && v <= steps[i-1] {
				return fmt.Errorf("%s must be strictly increasing", name)
			}
		}
	}
	return nil
}
