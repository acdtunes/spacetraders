package cli

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
)

var errWatchStoreTest = errors.New("watch store test error")
var errShipNavTest = errors.New("ship nav test error")

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

// fakeShipNavReader is the sp-970u test double for shipNavReader: a fixed
// in-transit/arrival/error triple, plus a call counter so tests can assert a
// path (e.g. container:terminal, or an explicit --by) never even consults it.
type fakeShipNavReader struct {
	inTransit   bool
	arrivalTime *time.Time
	err         error
	calls       int
}

func (f *fakeShipNavReader) ShipNav(ctx context.Context, shipSymbol string) (bool, *time.Time, error) {
	f.calls++
	return f.inTransit, f.arrivalTime, f.err
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

	err := runWatchAdd(context.Background(), store, nil, &bytes.Buffer{}, now, "ship:TORWIND-E:arrival", "+20m", watchkeeper.DefaultWatchDeadline, defaultWatchETAMargin)
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
// sp-970u regression: container:terminal has no ETA concept, so it must
// always get the flat default — even with a navReader that (if consulted)
// would report an in-transit ship, proving the container path never touches
// it.
func TestRunWatchAddDefaultsDeadlineWhenByOmitted(t *testing.T) {
	store := &fakeWatchPolicyStore{}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	arrival := now.Add(40 * time.Minute)
	nav := &fakeShipNavReader{inTransit: true, arrivalTime: &arrival}
	var buf bytes.Buffer

	err := runWatchAdd(context.Background(), store, nav, &buf, now, "container:c-9f2a:terminal", "", 45*time.Minute, defaultWatchETAMargin)
	require.NoError(t, err)
	require.NotNil(t, store.saved)
	require.Len(t, store.saved.Watches, 1)
	require.Equal(t, now.Add(45*time.Minute), store.saved.Watches[0].Deadline,
		"omitting --by must still deadline the watch, at now+default")
	require.Equal(t, 0, nav.calls, "container:terminal must never consult the ship nav reader")
	require.Empty(t, buf.String(), "container:terminal prints no ETA-derivation note")
}

// Acceptance (4)/coexistence: `watch add` is additive, not full-replace.
func TestRunWatchAddIsAdditive(t *testing.T) {
	now := time.Now()
	store := &fakeWatchPolicyStore{loaded: watchkeeper.WatchPolicy{Watches: []watchkeeper.Watch{
		{Subject: watchkeeper.WatchSubjectShip, ID: "SHIP-A", Predicate: watchkeeper.WatchPredicateArrival,
			Deadline: now.Add(time.Hour)},
	}}}

	err := runWatchAdd(context.Background(), store, nil, &bytes.Buffer{}, now, "ship:SHIP-B:arrival", "+1h", watchkeeper.DefaultWatchDeadline, defaultWatchETAMargin)
	require.NoError(t, err)
	require.NotNil(t, store.saved)
	require.Len(t, store.saved.Watches, 2, "watch add must add to, not replace, the armed list")
	require.Equal(t, "SHIP-A", store.saved.Watches[0].ID)
	require.Equal(t, "SHIP-B", store.saved.Watches[1].ID)
}

func TestRunWatchAddRejectsBadSpec(t *testing.T) {
	store := &fakeWatchPolicyStore{}
	err := runWatchAdd(context.Background(), store, nil, &bytes.Buffer{}, time.Now(), "ship:TORWIND-E:terminal", "", watchkeeper.DefaultWatchDeadline, defaultWatchETAMargin)
	require.Error(t, err, "predicate mismatched to subject must fail at add time")
	require.Nil(t, store.saved, "a bad spec must not save anything")
}

func TestRunWatchAddPropagatesLoadError(t *testing.T) {
	store := &fakeWatchPolicyStore{loadErr: errWatchStoreTest}
	err := runWatchAdd(context.Background(), store, nil, &bytes.Buffer{}, time.Now(), "ship:TORWIND-E:arrival", "", watchkeeper.DefaultWatchDeadline, defaultWatchETAMargin)
	require.Error(t, err)
	require.Nil(t, store.saved, "a load failure must not attempt a save")
}

// sp-970u: ship:arrival watch, --by omitted, ship IN_TRANSIT with a known
// arrival time → the deadline is derived from the live ETA plus margin, not
// the flat default.
func TestRunWatchAddDerivesETADeadlineForInTransitShip(t *testing.T) {
	store := &fakeWatchPolicyStore{}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	arrival := now.Add(40 * time.Minute) // 40m remaining transit
	nav := &fakeShipNavReader{inTransit: true, arrivalTime: &arrival}
	var buf bytes.Buffer

	err := runWatchAdd(context.Background(), store, nav, &buf, now, "ship:TORWIND-E:arrival", "", watchkeeper.DefaultWatchDeadline, 0.25)
	require.NoError(t, err)
	require.NotNil(t, store.saved)
	require.Len(t, store.saved.Watches, 1)

	wantDeadline := now.Add(50 * time.Minute) // 40m * 1.25 = 50m
	require.Equal(t, wantDeadline, store.saved.Watches[0].Deadline,
		"no --by on a ship:arrival watch must derive the deadline as ETA + 25%% margin")
	require.Contains(t, buf.String(), "derived from ETA")
}

// sp-970u: ANY nav-read failure (error, docked, no arrival time, or a
// past/invalid timestamp) must gracefully fall back to the flat default —
// never block or fail watch add.
func TestRunWatchAddFallsBackToFlatDefaultWhenNavUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	pastArrival := now.Add(-5 * time.Minute)

	cases := map[string]*fakeShipNavReader{
		"nav read error":         {err: errShipNavTest},
		"ship docked":            {inTransit: false},
		"in transit, no ETA":     {inTransit: true, arrivalTime: nil},
		"arrival already passed": {inTransit: true, arrivalTime: &pastArrival},
	}

	for name, nav := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeWatchPolicyStore{}
			var buf bytes.Buffer

			err := runWatchAdd(context.Background(), store, nav, &buf, now, "ship:TORWIND-E:arrival", "", 30*time.Minute, 0.25)
			require.NoError(t, err)
			require.NotNil(t, store.saved)
			require.Equal(t, now.Add(30*time.Minute), store.saved.Watches[0].Deadline,
				"a failed ETA derivation must fall back to the flat default")
			require.Contains(t, buf.String(), "flat default")
		})
	}
}

// sp-970u regression: a nil navReader (e.g. newShipNavReader itself failed —
// no DB, no player resolved) must behave exactly like any other derivation
// failure: flat default, no blocking, no error.
func TestRunWatchAddFallsBackToFlatDefaultWhenNavReaderNil(t *testing.T) {
	store := &fakeWatchPolicyStore{}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	var buf bytes.Buffer

	err := runWatchAdd(context.Background(), store, nil, &buf, now, "ship:TORWIND-E:arrival", "", 30*time.Minute, 0.25)
	require.NoError(t, err)
	require.NotNil(t, store.saved)
	require.Equal(t, now.Add(30*time.Minute), store.saved.Watches[0].Deadline)
	require.Contains(t, buf.String(), "flat default")
}

// sp-970u regression: an explicit --by always wins — the ETA derivation must
// not even be attempted, regardless of what the ship's nav looks like.
func TestRunWatchAddByGivenSkipsETADerivation(t *testing.T) {
	store := &fakeWatchPolicyStore{}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	arrival := now.Add(40 * time.Minute)
	nav := &fakeShipNavReader{inTransit: true, arrivalTime: &arrival}
	var buf bytes.Buffer

	err := runWatchAdd(context.Background(), store, nav, &buf, now, "ship:TORWIND-E:arrival", "+20m", watchkeeper.DefaultWatchDeadline, 0.25)
	require.NoError(t, err)
	require.NotNil(t, store.saved)
	require.Equal(t, now.Add(20*time.Minute), store.saved.Watches[0].Deadline,
		"an explicit --by must win outright, not be blended with the ETA derivation")
	require.Equal(t, 0, nav.calls, "an explicit --by must skip the nav read entirely")
	require.Empty(t, buf.String(), "an explicit --by prints no derivation note")
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
