package cli

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

type fakeEventStore struct {
	unprocessed []*captain.Event
	marked      []int64
}

func (f *fakeEventStore) FindUnprocessed(ctx context.Context, playerID, limit int) ([]*captain.Event, error) {
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
