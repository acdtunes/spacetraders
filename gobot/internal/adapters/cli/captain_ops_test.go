package cli

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type fakeEventStore struct {
	unprocessed []*captain.Event
	marked      []int64
	// lastPlayerID records the playerID FindUnprocessed was most recently
	// called with, so tests can assert a resolved --agent flag reached the
	// store as a concrete numeric ID (sp-yr3f).
	lastPlayerID int
}

func (f *fakeEventStore) FindUnprocessed(ctx context.Context, playerID, limit int) ([]*captain.Event, error) {
	f.lastPlayerID = playerID
	return f.unprocessed, nil
}

func (f *fakeEventStore) MarkProcessed(ctx context.Context, ids []int64, at time.Time) error {
	f.marked = append(f.marked, ids...)
	return nil
}

func TestCaptainEventsAckMarksParsedIDs(t *testing.T) {
	fs := &fakeEventStore{}
	err := runEventsAck(context.Background(), fs, "12,13,14")
	require.NoError(t, err)
	require.Equal(t, []int64{12, 13, 14}, fs.marked)
}

func TestCaptainEventsAckRejectsGarbage(t *testing.T) {
	fs := &fakeEventStore{}
	err := runEventsAck(context.Background(), fs, "12,abc")
	require.Error(t, err)
	require.Empty(t, fs.marked)
}

// --- sp-yr3f: captain events/report honor global --agent ---

// TestCaptainEventsListResolvedHonorsAgentFlagWithoutPlayerID reproduces the
// verified repro ("captain events list --agent TORWIND" -> "--player-id flag
// is required"): with only --agent set, resolution must succeed and the
// store must be queried with the resolved numeric player ID.
func TestCaptainEventsListResolvedHonorsAgentFlagWithoutPlayerID(t *testing.T) {
	setPlayerFlags(t, 0, "TORWIND")
	fs := &fakeEventStore{}
	repo := &fakePlayerRepo{bySymbol: map[string]*player.Player{
		"TORWIND": player.NewPlayer(shared.MustNewPlayerID(9), "TORWIND", "TOKEN-9"),
	}}

	err := runEventsListResolved(context.Background(), fs, repo, false)

	require.NoError(t, err)
	require.Equal(t, 9, fs.lastPlayerID)
}

// TestCaptainEventsListResolvedErrorsWhenNoPlayerIdentifiable confirms the
// helpful error remains when neither --player-id, --agent, nor a persisted
// default identifies a player.
func TestCaptainEventsListResolvedErrorsWhenNoPlayerIdentifiable(t *testing.T) {
	setPlayerFlags(t, 0, "")
	t.Setenv("HOME", t.TempDir())
	fs := &fakeEventStore{}
	repo := &fakePlayerRepo{}

	err := runEventsListResolved(context.Background(), fs, repo, false)

	require.Error(t, err)
}

// --- sp-yr3f: `captain events ack --all` / `--before` batch flags ---

func TestCaptainEventsAckAllMarksEveryUnprocessedEvent(t *testing.T) {
	fs := &fakeEventStore{unprocessed: []*captain.Event{{ID: 1}, {ID: 2}, {ID: 3}}}

	err := runEventsAckAll(context.Background(), fs, 7)

	require.NoError(t, err)
	require.Equal(t, 7, fs.lastPlayerID)
	require.ElementsMatch(t, []int64{1, 2, 3}, fs.marked)
}

func TestCaptainEventsAckAllNoPendingEventsIsNoop(t *testing.T) {
	fs := &fakeEventStore{}

	err := runEventsAckAll(context.Background(), fs, 7)

	require.NoError(t, err)
	require.Empty(t, fs.marked)
}

func TestCaptainEventsAckBeforeMarksOnlyOlderEvents(t *testing.T) {
	cutoff := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	fs := &fakeEventStore{unprocessed: []*captain.Event{
		{ID: 1, CreatedAt: cutoff.Add(-2 * time.Hour)},
		{ID: 2, CreatedAt: cutoff.Add(-1 * time.Hour)},
		{ID: 3, CreatedAt: cutoff.Add(1 * time.Hour)},
	}}

	err := runEventsAckBefore(context.Background(), fs, 7, cutoff)

	require.NoError(t, err)
	require.ElementsMatch(t, []int64{1, 2}, fs.marked)
}

func TestCaptainEventsAckBeforeExcludesEventsAtOrAfterCutoff(t *testing.T) {
	cutoff := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	fs := &fakeEventStore{unprocessed: []*captain.Event{
		{ID: 1, CreatedAt: cutoff},
		{ID: 2, CreatedAt: cutoff.Add(time.Hour)},
	}}

	err := runEventsAckBefore(context.Background(), fs, 7, cutoff)

	require.NoError(t, err)
	require.Empty(t, fs.marked)
}

// --- sp-sk68 wake model: `captain wake set` / `captain wake show` ---

type fakeWakePolicyStore struct {
	loaded  watchkeeper.WakePolicy
	loadErr error
	saved   *watchkeeper.WakePolicy
	saveErr error
}

func (f *fakeWakePolicyStore) Load() (watchkeeper.WakePolicy, error) {
	return f.loaded, f.loadErr
}

func (f *fakeWakePolicyStore) Save(policy watchkeeper.WakePolicy) error {
	f.saved = &policy
	return f.saveErr
}

func TestParseNextWakeAtRelativeDurationAddsToNow(t *testing.T) {
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	got, err := parseNextWakeAt("+3h", now)
	require.NoError(t, err)
	require.Equal(t, now.Add(3*time.Hour), got)
}

func TestParseNextWakeAtAbsoluteRFC3339(t *testing.T) {
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	got, err := parseNextWakeAt("2026-07-06T18:00:00Z", now)
	require.NoError(t, err)
	require.True(t, got.Equal(time.Date(2026, 7, 6, 18, 0, 0, 0, time.UTC)))
}

func TestParseNextWakeAtRejectsGarbage(t *testing.T) {
	_, err := parseNextWakeAt("not-a-time", time.Now())
	require.Error(t, err)
}

func TestParseNextWakeAtRejectsEmpty(t *testing.T) {
	_, err := parseNextWakeAt("", time.Now())
	require.Error(t, err)
}

func TestParseInterruptTypesSplitsAndTrims(t *testing.T) {
	got := parseInterruptTypes("workflow.failed, container.crashed ,stream.down")
	require.Equal(t, []string{"workflow.failed", "container.crashed", "stream.down"}, got)
}

func TestParseInterruptTypesEmptyReturnsNil(t *testing.T) {
	got := parseInterruptTypes("")
	require.Nil(t, got)
}

func TestRunWakeSetStampsDeclaredAtAndPersistsPolicy(t *testing.T) {
	store := &fakeWakePolicyStore{}
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	above := 500000
	err := runWakeSet(store, now, watchkeeper.WakePolicy{CreditsAbove: &above})
	require.NoError(t, err)
	require.NotNil(t, store.saved)
	require.Equal(t, &above, store.saved.CreditsAbove)
	require.Equal(t, now, store.saved.DeclaredAt)
	require.Nil(t, store.saved.NextWakeAt)
}

func TestRunWakeShowPrintsNotSetForUndeclaredPolicy(t *testing.T) {
	store := &fakeWakePolicyStore{}
	var buf bytes.Buffer
	err := runWakeShow(store, &buf)
	require.NoError(t, err)
	out := buf.String()
	require.Contains(t, out, "next_wake_at:")
	require.Contains(t, out, "(not set)")
	require.Contains(t, out, "never declared")
}

func TestRunWakeShowPrintsDeclaredPolicyValues(t *testing.T) {
	above := 500000
	below := 1000
	declaredAt := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	nextWake := time.Date(2026, 7, 6, 15, 0, 0, 0, time.UTC)
	store := &fakeWakePolicyStore{loaded: watchkeeper.WakePolicy{
		NextWakeAt:     &nextWake,
		CreditsAbove:   &above,
		CreditsBelow:   &below,
		InterruptTypes: []string{"workflow.failed", "container.crashed"},
		DeclaredAt:     declaredAt,
	}}
	var buf bytes.Buffer
	err := runWakeShow(store, &buf)
	require.NoError(t, err)
	out := buf.String()
	require.Contains(t, out, "2026-07-06T15:00:00Z")
	require.Contains(t, out, "500000")
	require.Contains(t, out, "1000")
	require.Contains(t, out, "workflow.failed,container.crashed")
	require.Contains(t, out, "2026-07-06T12:00:00Z")
}
