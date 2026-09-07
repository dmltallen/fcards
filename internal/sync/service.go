// Package sync implements the fcards data service on the offline-first sync
// contract used by the official mobile clients: bootstrap + hot-change pulls
// for reads, and atomic review pushes (review_event append + card upsert with
// a locally computed FSRS schedule) for writes. The scheduler port is parity
// proven against the project's golden vectors.
package sync

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"fcards/internal/api"
	"fcards/internal/scheduler"
	"fcards/internal/store"
)

func newID() string { return uuid.NewString() }

// isoMillis renders a timestamp the way the wire contract expects.
func isoMillis(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// Card is the TUI-facing view of a card snapshot.
type Card struct {
	CardID         string              `json:"card_id"`
	Front          string              `json:"front"`
	Back           string              `json:"back"`
	Tags           []string            `json:"tags"`
	DueAt          *time.Time          `json:"due_at,omitempty"`
	Reps           int                 `json:"reps"`
	Lapses         int                 `json:"lapses"`
	State          scheduler.CardState `json:"state"`
	StepIndex      *int                `json:"step_index,omitempty"`
	Stability      *float64            `json:"stability,omitempty"`
	Difficulty     *float64            `json:"difficulty,omitempty"`
	LastReviewedAt *time.Time          `json:"last_reviewed_at,omitempty"`
	ScheduledDays  *int                `json:"scheduled_days,omitempty"`
	Deleted        bool                `json:"deleted,omitempty"`
	CreatedAt      *time.Time          `json:"created_at,omitempty"`

	// Raw echo fields for card upsert payloads.
	CardType    string          `json:"card_type,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
	EffortLevel string          `json:"effort_level,omitempty"`
}

// ReviewEventRow is one history row used for streaks and stats.
type ReviewEventRow struct {
	Rating        int       `json:"rating"`
	ReviewedAtUTC time.Time `json:"reviewed_at"`
}

// Deck is a saved filter: effectively a named tag set.
type Deck struct {
	DeckID string   `json:"deck_id"`
	Name   string   `json:"name"`
	Tags   []string `json:"tags"`
}

type Service struct {
	Client         *api.Client
	Config         *store.Config
	ReplicaID      string
	InstallationID string
	AppVersion     string

	Cards    []Card
	Decks    []Deck
	Settings *scheduler.Settings
	History  []ReviewEventRow

	SelectedTags []string
}

func New(client *api.Client, cfg *store.Config, replicaID, installationID string) *Service {
	return &Service{
		Client: client, Config: cfg,
		ReplicaID: replicaID, InstallationID: installationID,
		AppVersion: "fcards 0.2",
		Settings:   scheduler.DefaultSettings(),
	}
}

// --- payload decoding ---

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	}
	return 0, false
}

func asInt(v any) (int, bool) {
	f, ok := asFloat(v)
	if !ok {
		return 0, false
	}
	return int(f), true
}

func asTime(v any) (*time.Time, error) {
	s, ok := v.(string)
	if !ok || s == "" {
		return nil, nil
	}
	for _, l := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(l, s); err == nil {
			return &t, nil
		}
	}
	return nil, fmt.Errorf("unparseable timestamp %q", s)
}

func asTags(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range arr {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func parseTimeField(payload map[string]any, key string) (*time.Time, error) {
	return asTime(payload[key])
}

func cardFromPayload(raw json.RawMessage) (Card, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Card{}, err
	}
	c := Card{
		CardID:      asString(payload["cardId"]),
		Front:       asString(payload["frontText"]),
		Back:        asString(payload["backText"]),
		Tags:        asTags(payload["tags"]),
		CardType:    asString(payload["cardType"]),
		EffortLevel: asString(payload["effortLevel"]),
	}
	if c.State == "" {
		c.State = scheduler.CardState(asString(payload["fsrsCardState"]))
	}
	if c.CardID == "" {
		return Card{}, fmt.Errorf("card payload without cardId")
	}
	var err error
	if c.DueAt, err = parseTimeField(payload, "dueAt"); err != nil {
		return c, err
	}
	if c.LastReviewedAt, err = parseTimeField(payload, "fsrsLastReviewedAt"); err != nil {
		return c, err
	}
	if c.CreatedAt, err = parseTimeField(payload, "createdAt"); err != nil {
		return c, err
	}
	if deletedAt, err := parseTimeField(payload, "deletedAt"); err == nil {
		c.Deleted = deletedAt != nil
	}
	if v, ok := asInt(payload["reps"]); ok {
		c.Reps = v
	}
	if v, ok := asInt(payload["lapses"]); ok {
		c.Lapses = v
	}
	if v, ok := asInt(payload["fsrsStepIndex"]); ok {
		c.StepIndex = &v
	}
	if v, ok := asFloat(payload["fsrsStability"]); ok {
		c.Stability = &v
	}
	if v, ok := asFloat(payload["fsrsDifficulty"]); ok {
		c.Difficulty = &v
	}
	if v, ok := asInt(payload["fsrsScheduledDays"]); ok {
		c.ScheduledDays = &v
	}
	if md, ok := payload["metadata"]; ok && md != nil {
		if b, err := json.Marshal(md); err == nil {
			c.Metadata = b
		}
	}
	return c, nil
}

func settingsFromPayload(raw json.RawMessage) (*scheduler.Settings, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	s := scheduler.DefaultSettings()
	if a := asString(payload["algorithm"]); a != "" {
		s.Algorithm = a
	}
	if v, ok := asFloat(payload["desiredRetention"]); ok {
		s.DesiredRetention = v
	}
	if v, ok := asInt(payload["maximumIntervalDays"]); ok {
		s.MaximumIntervalDays = v
	}
	if v, ok := payload["enableFuzz"].(bool); ok {
		s.EnableFuzz = v
	}
	if steps := stepsFromPayload(payload["learningStepsMinutes"]); len(steps) > 0 {
		s.LearningStepsMinutes = steps
	}
	if steps := stepsFromPayload(payload["relearningStepsMinutes"]); len(steps) > 0 {
		s.RelearningStepsMinutes = steps
	}
	if err := scheduler.ValidateSettings(s); err != nil {
		return nil, err
	}
	return s, nil
}

func stepsFromPayload(raw any) []int {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	var steps []int
	for _, item := range arr {
		if v, ok := asInt(item); ok {
			steps = append(steps, v)
		}
	}
	return steps
}

// --- cursor state (per workspace) ---

type workspaceState struct {
	HotCursor    int64            `json:"hot_cursor"`    // -1 = never bootstrapped
	ReviewCursor int64            `json:"review_cursor"` // -1 = history not loaded
	SelectedTags []string         `json:"selected_tags,omitempty"`
	Cards        []Card           `json:"cards,omitempty"`
	Decks        []Deck           `json:"decks,omitempty"`
	History      []ReviewEventRow `json:"history,omitempty"`
}

func statePath(workspaceID string) (string, error) {
	dir, err := store.DataDir()
	if err != nil {
		return "", err
	}
	if err := store.MkdirData(dir); err != nil {
		return "", err
	}
	return dir + "/state-" + workspaceID + ".json", nil
}

func loadState(workspaceID string) (*workspaceState, error) {
	path, err := statePath(workspaceID)
	if err != nil {
		return nil, err
	}
	data, err := readFile(path)
	if err != nil || len(data) == 0 {
		return &workspaceState{HotCursor: -1, ReviewCursor: -1}, nil
	}
	var st workspaceState
	if err := json.Unmarshal(data, &st); err != nil {
		return &workspaceState{HotCursor: -1, ReviewCursor: -1}, nil
	}
	return &st, nil
}

func saveState(workspaceID string, st *workspaceState) error {
	path, err := statePath(workspaceID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeFile(path, data)
}

// LoadCached hydrates the service from the on-disk state file without any
// network access. Used by `fcards status` and the omarchy bar widget.
func (s *Service) LoadCached() error {
	wsID := s.Config.WorkspaceID
	if wsID == "" {
		return fmt.Errorf("no workspace selected")
	}
	st, err := loadState(wsID)
	if err != nil {
		return err
	}
	s.SelectedTags = st.SelectedTags
	s.Cards = st.Cards
	s.Decks = st.Decks
	s.History = st.History
	if s.Settings == nil {
		s.Settings = scheduler.DefaultSettings()
	}
	return nil
}

// --- refresh (bootstrap + deltas + history) ---

// Refresh brings the workspace fully up to date: bootstrap when needed,
// then hot-change deltas, then incremental review history.
func (s *Service) Refresh() error {
	wsID := s.Config.WorkspaceID
	if wsID == "" {
		return fmt.Errorf("no workspace selected")
	}
	st, err := loadState(wsID)
	if err != nil {
		return err
	}
	s.SelectedTags = st.SelectedTags
	// Restore the local cache from disk before applying deltas.
	s.Cards = st.Cards
	s.Decks = st.Decks
	s.History = st.History

	if st.HotCursor < 0 {
		if err := s.bootstrap(wsID, st); err != nil {
			return err
		}
	}

	// Hot-change deltas.
	for i := 0; i < 100; i++ {
		res, err := s.Client.Pull(wsID, s.InstallationID, st.HotCursor, 500)
		if err != nil {
			return err
		}
		s.applyChanges(res.Changes)
		st.HotCursor = res.NextHotChangeID
		if !res.HasMore {
			break
		}
	}

	// Review history (append-only), paged by sequence cursor.
	if st.ReviewCursor < 0 {
		st.ReviewCursor = 0
		s.History = nil
	}
	for i := 0; i < 100; i++ {
		res, err := s.Client.ReviewHistoryPull(wsID, s.InstallationID, st.ReviewCursor, 500)
		if err != nil {
			return err
		}
		for _, ev := range res.ReviewEvents {
			at, err := time.Parse(time.RFC3339Nano, ev.ReviewedAtClient)
			if err != nil {
				continue
			}
			s.History = append(s.History, ReviewEventRow{Rating: ev.Rating, ReviewedAtUTC: at})
		}
		st.ReviewCursor = res.NextReviewSequenceID
		if !res.HasMore {
			break
		}
	}

	return saveState(wsID, &workspaceState{
		HotCursor:    st.HotCursor,
		ReviewCursor: st.ReviewCursor,
		SelectedTags: st.SelectedTags,
		Cards:        s.Cards,
		Decks:        s.Decks,
		History:      s.History,
	})
}

func (s *Service) bootstrap(wsID string, st *workspaceState) error {
	var cursor *string
	s.Cards = nil
	s.Settings = nil
	var hotCursor int64
	for i := 0; i < 100; i++ {
		res, err := s.Client.BootstrapPull(wsID, s.InstallationID, cursor, 1000)
		if err != nil {
			return err
		}
		s.applyEntries(res.Entries)
		hotCursor = res.BootstrapHotChangeID
		if res.NextCursor != nil && res.HasMore {
			cursor = res.NextCursor
			continue
		}
		break
	}
	st.HotCursor = hotCursor
	return nil
}

func (s *Service) applyEntries(entries []api.SyncEntry) {
	for _, e := range entries {
		s.applyEntry(e.EntityType, e.Payload)
	}
}

func (s *Service) applyChanges(changes []api.SyncEntry) {
	for _, e := range changes {
		s.applyEntry(e.EntityType, e.Payload)
	}
}

func (s *Service) applyEntry(entityType string, payload json.RawMessage) {
	switch entityType {
	case "card":
		c, err := cardFromPayload(payload)
		if err != nil {
			return
		}
		replaced := false
		for i := range s.Cards {
			if s.Cards[i].CardID == c.CardID {
				s.Cards[i] = c
				replaced = true
				break
			}
		}
		if !replaced {
			s.Cards = append(s.Cards, c)
		}
	case "workspace_scheduler_settings":
		if st, err := settingsFromPayload(payload); err == nil {
			s.Settings = st
		}
	case "deck":
		d, err := deckFromPayload(payload)
		if err != nil || d.DeckID == "" {
			return
		}
		replaced := false
		for i := range s.Decks {
			if s.Decks[i].DeckID == d.DeckID {
				s.Decks[i] = d
				replaced = true
				break
			}
		}
		if !replaced {
			s.Decks = append(s.Decks, d)
		}
	}
}

func deckFromPayload(raw json.RawMessage) (Deck, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Deck{}, err
	}
	d := Deck{
		DeckID: asString(payload["deckId"]),
		Name:   asString(payload["name"]),
	}
	if deletedAt, err := parseTimeField(payload, "deletedAt"); err == nil && deletedAt != nil {
		d.DeckID = "" // tombstoned
	}
	if fd, ok := payload["filterDefinition"].(map[string]any); ok {
		d.Tags = asTags(fd["tags"])
	}
	return d, nil
}

// liveCards returns non-deleted cards.
func (s *Service) liveCards() []Card {
	out := make([]Card, 0, len(s.Cards))
	for _, c := range s.Cards {
		if !c.Deleted {
			out = append(out, c)
		}
	}
	return out
}

// CardByID returns the live card (pointer into the cache) with this ID.
func (s *Service) CardByID(id string) *Card {
	for i := range s.Cards {
		if s.Cards[i].CardID == id && !s.Cards[i].Deleted {
			return &s.Cards[i]
		}
	}
	return nil
}

// SwitchWorkspace resets cached state for a new workspace selection.
func (s *Service) SwitchWorkspace(id string) {
	s.Config.WorkspaceID = id
	s.Cards = nil
	s.Decks = nil
	s.History = nil
	s.SelectedTags = nil
	s.Settings = scheduler.DefaultSettings()
}

// TagSelection returns the active study tags (empty = everything).
func (s *Service) TagSelection() []string {
	return s.SelectedTags
}

// SetTagSelection applies and persists the active study tags.
func (s *Service) SetTagSelection(tags []string) error {
	sorted := append([]string(nil), tags...)
	sort.Strings(sorted)
	s.SelectedTags = sorted
	wsID := s.Config.WorkspaceID
	if wsID == "" {
		return nil
	}
	st, err := loadState(wsID)
	if err != nil {
		return err
	}
	st.SelectedTags = sorted
	return saveState(wsID, st)
}

// cardMatchesSelection reports whether a card belongs to the active study
// selection (empty selection matches everything).
func (s *Service) cardMatchesSelection(c Card) bool {
	if len(s.SelectedTags) == 0 {
		return true
	}
	set := make(map[string]bool, len(s.SelectedTags))
	for _, t := range s.SelectedTags {
		set[t] = true
	}
	for _, t := range c.Tags {
		if set[t] {
			return true
		}
	}
	return false
}

// --- queue (documented presentation policy) ---

// Queue returns active cards in the canonical presentation order:
// 1) recently reviewed due cards, 2) other due cards, 3) new cards.
func (s *Service) Queue(now time.Time) []Card {
	var all []Card
	for _, c := range s.liveCards() {
		if s.cardMatchesSelection(c) {
			all = append(all, c)
		}
	}
	var recentDue, due, fresh []Card
	for _, c := range all {
		switch {
		case c.DueAt != nil && !c.DueAt.After(now):
			if c.LastReviewedAt != nil && !c.LastReviewedAt.Before(now.Add(-time.Hour)) {
				recentDue = append(recentDue, c)
			} else {
				due = append(due, c)
			}
		case c.DueAt == nil:
			fresh = append(fresh, c)
		}
	}
	less := func(a, b Card) bool {
		var aDue, bDue time.Time
		if a.DueAt != nil {
			aDue = *a.DueAt
		}
		if b.DueAt != nil {
			bDue = *b.DueAt
		}
		if !aDue.Equal(bDue) {
			return aDue.Before(bDue)
		}
		var aC, bC time.Time
		if a.CreatedAt != nil {
			aC = *a.CreatedAt
		}
		if b.CreatedAt != nil {
			bC = *b.CreatedAt
		}
		if !aC.Equal(bC) {
			return aC.After(bC) // createdAt DESC
		}
		return a.CardID < b.CardID
	}
	sort.SliceStable(recentDue, func(i, j int) bool { return less(recentDue[i], recentDue[j]) })
	sort.SliceStable(due, func(i, j int) bool { return less(due[i], due[j]) })
	sort.SliceStable(fresh, func(i, j int) bool { return less(fresh[i], fresh[j]) })
	return append(append(recentDue, due...), fresh...)
}

// Counts summarizes queue state for the dashboard.
type Counts struct {
	RecentDue     int
	Due           int
	New           int
	ReviewedToday int
}

func (s *Service) Counts(now time.Time) Counts {
	q := s.Queue(now)
	c := Counts{RecentDue: len(q) - s.countDue(q), Due: s.countDue(q)}
	for _, card := range q {
		if card.DueAt == nil {
			c.New++
		}
	}
	for _, ev := range s.History {
		if sameUTCDay(ev.ReviewedAtUTC, now) {
			c.ReviewedToday++
		}
	}
	return c
}

func (s *Service) countDue(q []Card) int {
	n := 0
	for _, c := range q {
		if c.DueAt != nil {
			n++
		}
	}
	return n
}

func sameUTCDay(a, b time.Time) bool {
	ay, am, ad := a.UTC().Date()
	by, bm, bd := b.UTC().Date()
	return ay == by && am == bm && ad == bd
}

// Streak counts consecutive UTC days ending today (or yesterday) with reviews.
func (s *Service) Streak(now time.Time) int {
	days := map[string]bool{}
	for _, ev := range s.History {
		days[ev.ReviewedAtUTC.UTC().Format("2006-01-02")] = true
	}
	streak := 0
	cursor := now.UTC()
	if !days[cursor.Format("2006-01-02")] {
		cursor = cursor.AddDate(0, 0, -1)
	}
	for ; days[cursor.Format("2006-01-02")]; cursor = cursor.AddDate(0, 0, -1) {
		streak++
	}
	return streak
}

// --- review submission ---

// Review computes the FSRS schedule locally (parity-proven port) and pushes
// one atomic batch: the review_event append plus the card upsert carrying the
// new schedule. On transport failure the batch lands in the outbox.
func (s *Service) Review(card *Card, rating scheduler.Rating, now time.Time) (*scheduler.Schedule, error) {
	sc := card.ToSchedulerCard()
	schedule, err := scheduler.ComputeReviewSchedule(sc, s.Settings, rating, now)
	if err != nil {
		return nil, err
	}

	reviewEventID, clientEventID, reviewOpID, cardOpID := newID(), newID(), newID(), newID()
	nowISO := isoMillis(now)

	reviewPayload, _ := json.Marshal(map[string]any{
		"reviewEventId":    reviewEventID,
		"cardId":           card.CardID,
		"clientEventId":    clientEventID,
		"rating":           int(rating),
		"reviewedAtClient": nowISO,
	})
	reviewOp := api.Operation{
		OperationID: reviewOpID, EntityID: reviewEventID, ClientUpdatedAt: nowISO,
		EntityType: "review_event", Action: "append", Payload: reviewPayload,
	}

	cardPayload, _ := json.Marshal(s.cardUpsertPayload(card, &schedule, now))
	cardOp := api.Operation{
		OperationID: cardOpID, EntityID: card.CardID, ClientUpdatedAt: nowISO,
		EntityType: "card", Action: "upsert", Payload: cardPayload,
	}

	ops := []api.Operation{reviewOp, cardOp}
	results, err := s.Client.Push(s.Config.WorkspaceID, s.InstallationID, ops)
	if err != nil {
		if queueErr := store.AppendOutbox(store.OutboxItem{ID: newID(), Ops: ops}); queueErr != nil {
			return nil, fmt.Errorf("review push failed (%v) and outbox write failed (%v)", err, queueErr)
		}
		return &schedule, fmt.Errorf("queued offline: %w", err)
	}
	if err := checkOpResults(results); err != nil {
		return nil, err
	}

	// Optimistically apply the schedule to the local view.
	s.applySchedule(card, &schedule)
	s.History = append([]ReviewEventRow{{Rating: int(rating), ReviewedAtUTC: now}}, s.History...)
	return &schedule, nil
}

func checkOpResults(results []api.OpResult) error {
	for _, r := range results {
		switch r.Status {
		case "applied", "ignored", "duplicate":
		case "rejected":
			msg := "operation rejected"
			if r.Error != nil {
				msg = *r.Error
			}
			return fmt.Errorf("%s %s rejected: %s", r.EntityType, r.EntityID, msg)
		}
	}
	return nil
}

// cardUpsertPayload builds the card snapshot with the new schedule applied.
func (s *Service) cardUpsertPayload(card *Card, schedule *scheduler.Schedule, now time.Time) map[string]any {
	payload := map[string]any{
		"cardId":             card.CardID,
		"frontText":          card.Front,
		"backText":           card.Back,
		"tags":               card.Tags,
		"dueAt":              timeOrNil(isoMillis(schedule.DueAt)),
		"createdAt":          isoMillis(timeOrNow(card.CreatedAt, now)),
		"reps":               schedule.Reps,
		"lapses":             schedule.Lapses,
		"fsrsCardState":      string(schedule.FsrsCardState),
		"fsrsStepIndex":      intOrNil(schedule.FsrsStepIndex),
		"fsrsStability":      schedule.FsrsStability,
		"fsrsDifficulty":     schedule.FsrsDifficulty,
		"fsrsLastReviewedAt": isoMillis(schedule.FsrsLastReviewedAt),
		"fsrsScheduledDays":  schedule.FsrsScheduledDays,
		"deletedAt":          nil,
	}
	if card.CardType != "" {
		payload["cardType"] = card.CardType
	}
	if len(card.Metadata) > 0 {
		var md any
		if json.Unmarshal(card.Metadata, &md) == nil {
			payload["metadata"] = md
		}
	}
	if card.EffortLevel != "" {
		payload["effortLevel"] = card.EffortLevel
	}
	return payload
}

func timeOrNil(iso string) any { return iso }

func intOrNil(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func timeOrNow(t *time.Time, fallback time.Time) time.Time {
	if t == nil {
		return fallback
	}
	return *t
}

func (s *Service) applySchedule(card *Card, schedule *scheduler.Schedule) {
	due := schedule.DueAt
	card.DueAt = &due
	card.Reps = schedule.Reps
	card.Lapses = schedule.Lapses
	card.State = schedule.FsrsCardState
	card.StepIndex = schedule.FsrsStepIndex
	stab := schedule.FsrsStability
	diff := schedule.FsrsDifficulty
	last := schedule.FsrsLastReviewedAt
	sdays := schedule.FsrsScheduledDays
	card.Stability, card.Difficulty = &stab, &diff
	card.LastReviewedAt, card.ScheduledDays = &last, &sdays
}

// --- offline outbox ---

// DrainOutbox flushes queued write batches; successfully applied items are dropped.
func (s *Service) DrainOutbox() (applied int, remaining int, err error) {
	items, loadErr := store.LoadOutbox()
	if loadErr != nil {
		return 0, 0, loadErr
	}
	if len(items) == 0 {
		return 0, 0, nil
	}
	var keep []store.OutboxItem
	for _, item := range items {
		results, execErr := s.Client.Push(s.Config.WorkspaceID, s.InstallationID, item.Ops)
		if execErr != nil {
			keep = append(keep, item)
			continue
		}
		if checkErr := checkOpResults(results); checkErr != nil {
			// Permanently invalid batch: drop it rather than retry forever.
			continue
		}
		applied++
	}
	if rewriteErr := store.RewriteOutbox(keep); rewriteErr != nil {
		return applied, len(keep), rewriteErr
	}
	return applied, len(keep), nil
}
