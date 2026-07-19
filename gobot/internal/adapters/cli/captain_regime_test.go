package cli

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	watchkeeper "github.com/andrescamacho/spacetraders-go/internal/captain"
)

var errRegimeStoreTest = errors.New("regime store test error")

// --- price-regime detector: `captain regime set` / `list` / `clear` ---

type fakeRegimePolicyStore struct {
	loaded  watchkeeper.RegimePolicy
	loadErr error
	saved   *watchkeeper.RegimePolicy
	saveErr error
}

func (f *fakeRegimePolicyStore) Load() (watchkeeper.RegimePolicy, error) {
	return f.loaded, f.loadErr
}

func (f *fakeRegimePolicyStore) Save(policy watchkeeper.RegimePolicy) error {
	f.saved = &policy
	return f.saveErr
}

func TestParseRegimeThresholdAbsoluteInteger(t *testing.T) {
	threshold, multiplier, err := parseRegimeThreshold("200")
	require.NoError(t, err)
	require.NotNil(t, threshold)
	require.Equal(t, 200, *threshold)
	require.Nil(t, multiplier)
}

func TestParseRegimeThresholdMultiplier(t *testing.T) {
	threshold, multiplier, err := parseRegimeThreshold("3x")
	require.NoError(t, err)
	require.Nil(t, threshold)
	require.NotNil(t, multiplier)
	require.Equal(t, 3.0, *multiplier)
}

func TestParseRegimeThresholdMultiplierDecimalUppercase(t *testing.T) {
	threshold, multiplier, err := parseRegimeThreshold("3.5X")
	require.NoError(t, err)
	require.Nil(t, threshold)
	require.NotNil(t, multiplier)
	require.Equal(t, 3.5, *multiplier)
}

func TestParseRegimeThresholdRejectsEmpty(t *testing.T) {
	_, _, err := parseRegimeThreshold("")
	require.Error(t, err)
}

func TestParseRegimeThresholdRejectsGarbage(t *testing.T) {
	_, _, err := parseRegimeThreshold("not-a-number")
	require.Error(t, err)
}

func TestRunRegimeSetAppendsToExistingTripwiresAndStampsCreatedAt(t *testing.T) {
	existingThreshold := 150
	store := &fakeRegimePolicyStore{loaded: watchkeeper.RegimePolicy{
		Tripwires: []watchkeeper.RegimeTripwire{
			{Good: "GAS", Direction: "bid-above", Threshold: &existingThreshold, Window: time.Hour},
		},
	}}
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	newThreshold := 200

	err := runRegimeSet(store, now, watchkeeper.RegimeTripwire{
		Good: "ORE", Direction: "bid-above", Threshold: &newThreshold, Window: 30 * time.Minute,
	})
	require.NoError(t, err)
	require.NotNil(t, store.saved)
	require.Len(t, store.saved.Tripwires, 2, "regime set must add to, not replace, the existing tripwire list")
	require.Equal(t, "GAS", store.saved.Tripwires[0].Good, "the previously declared tripwire must survive untouched")
	require.Equal(t, "ORE", store.saved.Tripwires[1].Good)
	require.Equal(t, now, store.saved.Tripwires[1].CreatedAt)
}

func TestRunRegimeSetPropagatesLoadError(t *testing.T) {
	store := &fakeRegimePolicyStore{loadErr: errRegimeStoreTest}
	threshold := 200
	err := runRegimeSet(store, time.Now(), watchkeeper.RegimeTripwire{Good: "ORE", Direction: "bid-above", Threshold: &threshold})
	require.Error(t, err)
	require.Nil(t, store.saved, "a load failure must not attempt a save")
}

func TestRunRegimeListPrintsNoTripwiresMessageWhenEmpty(t *testing.T) {
	store := &fakeRegimePolicyStore{}
	var buf bytes.Buffer
	err := runRegimeList(store, &buf)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "No price tripwires declared")
}

func TestRunRegimeListPrintsDeclaredTripwireValues(t *testing.T) {
	threshold := 200
	createdAt := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	store := &fakeRegimePolicyStore{loaded: watchkeeper.RegimePolicy{
		Tripwires: []watchkeeper.RegimeTripwire{
			{Good: "ORE", Direction: "bid-above", Threshold: &threshold, Window: 30 * time.Minute, CreatedAt: createdAt},
		},
	}}
	var buf bytes.Buffer
	err := runRegimeList(store, &buf)
	require.NoError(t, err)
	out := buf.String()
	require.Contains(t, out, "ORE")
	require.Contains(t, out, "bid-above")
	require.Contains(t, out, "200")
	require.Contains(t, out, "30m0s")
	require.Contains(t, out, "2026-07-08T12:00:00Z")
}

func TestRunRegimeListFormatsMultiplierThreshold(t *testing.T) {
	multiplier := 3.0
	store := &fakeRegimePolicyStore{loaded: watchkeeper.RegimePolicy{
		Tripwires: []watchkeeper.RegimeTripwire{
			{Good: "GAS", Direction: "bid-above", Multiplier: &multiplier, Window: 4 * time.Hour},
		},
	}}
	var buf bytes.Buffer
	err := runRegimeList(store, &buf)
	require.NoError(t, err)
	require.Contains(t, buf.String(), "3x baseline")
}

func TestRunRegimeClearSavesEmptyPolicy(t *testing.T) {
	threshold := 200
	store := &fakeRegimePolicyStore{loaded: watchkeeper.RegimePolicy{
		Tripwires: []watchkeeper.RegimeTripwire{
			{Good: "ORE", Direction: "bid-above", Threshold: &threshold},
		},
	}}
	err := runRegimeClear(store)
	require.NoError(t, err)
	require.NotNil(t, store.saved)
	require.Empty(t, store.saved.Tripwires)
}
