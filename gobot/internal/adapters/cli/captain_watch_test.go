package cli

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
)

var errWatchStoreTest = errors.New("watch store test error")

// --- sp-oyer one-shot wake watches: `captain wake watch add` / `list` / `clear` ---

type fakeWatchPolicyStore struct {
	loaded  watchkeeper.WatchPolicy
	loadErr error
	saved   *watchkeeper.WatchPolicy
	saveErr error
}

func (f *fakeWatchPolicyStore) Load() (watchkeeper.WatchPolicy, error) {
	return f.loaded, f.loadErr
}

func (f *fakeWatchPolicyStore) Save(policy watchkeeper.WatchPolicy) error {
	f.saved = &policy
	return f.saveErr
}

func TestParseWatchDeadlineRelative(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	got, err := parseWatchDeadline("+20m", now)
	require.NoError(t, err)
	require.Equal(t, now.Add(20*time.Minute), got)
}

func TestParseWatchDeadlineAbsoluteRFC3339(t *testing.T) {
	now := time.Now()
	got, err := parseWatchDeadline("2026-07-10T18:00:00Z", now)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 7, 10, 18, 0, 0, 0, time.UTC), got)
}

func TestParseWatchDeadlineRejectsGarbage(t *testing.T) {
	_, err := parseWatchDeadline("soon", time.Now())
	require.Error(t, err)
}

// Acceptance (1): `watch add ship:X:arrival` arms it; a subsequent `list` shows it.
func TestRunWatchAddArmsAndListShowsIt(t *testing.T) {
	store := &fakeWatchPolicyStore{}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	err := runWatchAdd(store, now, "ship:TORWIND-E:arrival", "+20m", watchkeeper.DefaultWatchDeadline)
	require.NoError(t, err)
	require.NotNil(t, store.saved)
	require.Len(t, store.saved.Watches, 1)
	w := store.saved.Watches[0]
	require.Equal(t, watchkeeper.WatchSubjectShip, w.Subject)
	require.Equal(t, "TORWIND-E", w.ID)
	require.Equal(t, watchkeeper.WatchPredicateArrival, w.Predicate)
	require.Equal(t, now.Add(20*time.Minute), w.Deadline)
	require.Equal(t, now, w.ArmedAt)

	// list renders the armed watch.
	listStore := &fakeWatchPolicyStore{loaded: *store.saved}
	var buf bytes.Buffer
	require.NoError(t, runWatchList(listStore, &buf))
	out := buf.String()
	require.Contains(t, out, "ship")
	require.Contains(t, out, "TORWIND-E")
	require.Contains(t, out, "arrival")
}

// Acceptance: --by omitted still deadlines the watch (mandatory-default).
func TestRunWatchAddDefaultsDeadlineWhenByOmitted(t *testing.T) {
	store := &fakeWatchPolicyStore{}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	err := runWatchAdd(store, now, "container:c-9f2a:terminal", "", 45*time.Minute)
	require.NoError(t, err)
	require.NotNil(t, store.saved)
	require.Len(t, store.saved.Watches, 1)
	require.Equal(t, now.Add(45*time.Minute), store.saved.Watches[0].Deadline,
		"omitting --by must still deadline the watch, at now+default")
}

// Acceptance (4)/coexistence: `watch add` is additive, not full-replace.
func TestRunWatchAddIsAdditive(t *testing.T) {
	now := time.Now()
	store := &fakeWatchPolicyStore{loaded: watchkeeper.WatchPolicy{Watches: []watchkeeper.Watch{
		{Subject: watchkeeper.WatchSubjectShip, ID: "SHIP-A", Predicate: watchkeeper.WatchPredicateArrival,
			Deadline: now.Add(time.Hour)},
	}}}

	err := runWatchAdd(store, now, "ship:SHIP-B:arrival", "+1h", watchkeeper.DefaultWatchDeadline)
	require.NoError(t, err)
	require.NotNil(t, store.saved)
	require.Len(t, store.saved.Watches, 2, "watch add must add to, not replace, the armed list")
	require.Equal(t, "SHIP-A", store.saved.Watches[0].ID)
	require.Equal(t, "SHIP-B", store.saved.Watches[1].ID)
}

func TestRunWatchAddRejectsBadSpec(t *testing.T) {
	store := &fakeWatchPolicyStore{}
	err := runWatchAdd(store, time.Now(), "ship:TORWIND-E:terminal", "", watchkeeper.DefaultWatchDeadline)
	require.Error(t, err, "predicate mismatched to subject must fail at add time")
	require.Nil(t, store.saved, "a bad spec must not save anything")
}

func TestRunWatchAddPropagatesLoadError(t *testing.T) {
	store := &fakeWatchPolicyStore{loadErr: errWatchStoreTest}
	err := runWatchAdd(store, time.Now(), "ship:TORWIND-E:arrival", "", watchkeeper.DefaultWatchDeadline)
	require.Error(t, err)
	require.Nil(t, store.saved, "a load failure must not attempt a save")
}

func TestRunWatchListPrintsEmptyMessage(t *testing.T) {
	store := &fakeWatchPolicyStore{}
	var buf bytes.Buffer
	require.NoError(t, runWatchList(store, &buf))
	require.Contains(t, buf.String(), "No wake watches armed")
}

func TestRunWatchClearSavesEmptyPolicy(t *testing.T) {
	store := &fakeWatchPolicyStore{loaded: watchkeeper.WatchPolicy{Watches: []watchkeeper.Watch{
		{Subject: watchkeeper.WatchSubjectShip, ID: "SHIP-A", Predicate: watchkeeper.WatchPredicateArrival},
	}}}
	require.NoError(t, runWatchClear(store))
	require.NotNil(t, store.saved)
	require.Empty(t, store.saved.Watches)
}
