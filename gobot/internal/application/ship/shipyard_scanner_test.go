package ship

// Unit tests for the ShipyardScanner (sp-42ow), through its driving port
// ScanAndSaveShipyard, asserting at the inventory-store / event-recorder /
// API driven ports. Test budget: 4 distinct behaviors here — trait-gated
// no-op, heavy milestone emitted once per era, configurable heavy set,
// persist error surfaces to the caller (who treats it as non-fatal).

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// --- fakes -------------------------------------------------------------------

type fakeShipyardAPI struct {
	data *domainPorts.ShipyardData
	err  error
	gets int
}

func (f *fakeShipyardAPI) GetShipyard(context.Context, string, string, string) (*domainPorts.ShipyardData, error) {
	f.gets++
	return f.data, f.err
}

type fakeInventory struct {
	rows       map[string][]shipyard.ShipTypeAvailability
	hasTypes   bool
	replaceErr error
}

func newFakeInventory() *fakeInventory {
	return &fakeInventory{rows: map[string][]shipyard.ShipTypeAvailability{}}
}

func (f *fakeInventory) ReplaceScan(_ context.Context, _ int, _, waypointSymbol string, availabilities []shipyard.ShipTypeAvailability, _ time.Time) error {
	if f.replaceErr != nil {
		return f.replaceErr
	}
	f.rows[waypointSymbol] = availabilities
	return nil
}

func (f *fakeInventory) HasAnyOfTypes(context.Context, int, []string) (bool, error) {
	return f.hasTypes, nil
}

func (f *fakeInventory) ListByTypes(context.Context, int, []string) ([]shipyard.ShipTypeAvailability, error) {
	return nil, nil
}

type fakeRecorder struct {
	events []*captain.Event
}

func (f *fakeRecorder) Record(_ context.Context, e *captain.Event) error {
	f.events = append(f.events, e)
	return nil
}

type fakeTraits struct {
	waypoints map[string]*shared.Waypoint
}

// HasWaypointTrait mirrors the immutable-trait read (era/TTL-agnostic): a symbol
// present in the fixture answers from its traits, an absent one is (false, nil)
// — "not cached yet", which the scanner treats as "not a shipyard".
func (f *fakeTraits) HasWaypointTrait(_ context.Context, waypointSymbol, trait string) (bool, error) {
	wp, ok := f.waypoints[waypointSymbol]
	if !ok {
		return false, nil
	}
	return wp.HasTrait(trait), nil
}

func waypointWithTraits(t *testing.T, symbol string, traits ...string) *shared.Waypoint {
	t.Helper()
	wp, err := shared.NewWaypoint(symbol, 0, 0)
	require.NoError(t, err)
	wp.Traits = traits
	return wp
}

func scanCtx() context.Context {
	return common.WithPlayerToken(context.Background(), "test-token")
}

func heavyYardData(waypoint string) *domainPorts.ShipyardData {
	return &domainPorts.ShipyardData{
		Symbol:    waypoint,
		ShipTypes: []domainPorts.ShipTypeInfo{{Type: "SHIP_BULK_FREIGHTER"}},
		Ships: []domainPorts.ShipListingData{
			{Type: "SHIP_BULK_FREIGHTER", PurchasePrice: 2_000_000, Supply: "LOW"},
		},
	}
}

// --- behavior: trait-gated no-op ----------------------------------------------

// The scanner is invoked on EVERY scout market visit; a waypoint without the
// SHIPYARD trait (or with no cached waypoint at all) must be a clean no-op —
// no API call spent, nothing persisted. Parametrized over the two no-op inputs.
func TestShipyardScanner_NonShipyardWaypoint_NoAPICallNoPersist(t *testing.T) {
	cases := []struct {
		name      string
		waypoints map[string]*shared.Waypoint
	}{
		{"marketplace without shipyard trait", map[string]*shared.Waypoint{
			"X1-AA-M1": waypointWithTraits(t, "X1-AA-M1", "MARKETPLACE"),
		}},
		{"waypoint not in cache", map[string]*shared.Waypoint{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeShipyardAPI{data: heavyYardData("X1-AA-M1")}
			inventory := newFakeInventory()
			s := NewShipyardScanner(api, inventory, &fakeTraits{waypoints: tc.waypoints}, &fakeRecorder{}, shipyard.NewHeavyShipTypeSet(nil))

			require.NoError(t, s.ScanAndSaveShipyard(scanCtx(), 1, "X1-AA-M1"))

			require.Zero(t, api.gets, "no shipyard API call may be spent on a non-shipyard visit")
			require.Empty(t, inventory.rows)
		})
	}
}

// --- behavior: heavy milestone emitted once per era ---------------------------

// The FIRST heavy discovery of the era emits the milestone; once the store
// already knows any heavy yard, a re-discovery stays silent.
func TestShipyardScanner_HeavyMilestone_EmittedOnlyOnFirstDiscovery(t *testing.T) {
	yard := "X1-AA-Y1"
	traits := &fakeTraits{waypoints: map[string]*shared.Waypoint{
		yard: waypointWithTraits(t, yard, "SHIPYARD"),
	}}

	t.Run("first discovery emits", func(t *testing.T) {
		events := &fakeRecorder{}
		inventory := newFakeInventory() // store empty: nothing heavy known yet
		s := NewShipyardScanner(&fakeShipyardAPI{data: heavyYardData(yard)}, inventory, traits, events, shipyard.NewHeavyShipTypeSet(nil))

		require.NoError(t, s.ScanAndSaveShipyard(scanCtx(), 1, yard))

		require.Len(t, events.events, 1)
		require.Equal(t, captain.EventHeavyYardDiscovered, events.events[0].Type)
		require.Equal(t, 1, events.events[0].PlayerID)
		require.Contains(t, events.events[0].Payload, "SHIP_BULK_FREIGHTER")
	})

	t.Run("re-discovery stays silent", func(t *testing.T) {
		events := &fakeRecorder{}
		inventory := newFakeInventory()
		inventory.hasTypes = true // a heavy yard is already known this era
		s := NewShipyardScanner(&fakeShipyardAPI{data: heavyYardData(yard)}, inventory, traits, events, shipyard.NewHeavyShipTypeSet(nil))

		require.NoError(t, s.ScanAndSaveShipyard(scanCtx(), 1, yard))

		require.Empty(t, events.events, "an already-known heavy class must not re-emit the milestone")
		require.Len(t, inventory.rows[yard], 1, "the scan itself must still persist")
	})
}

// --- behavior: configurable heavy set -----------------------------------------

// The heavy classification is configuration, not a hardcoded pair: a custom set
// reclassifies which types trigger the milestone, and types outside the set
// never do — even the default heavies.
func TestShipyardScanner_HeavySetConfigurable(t *testing.T) {
	yard := "X1-AA-Y1"
	traits := &fakeTraits{waypoints: map[string]*shared.Waypoint{
		yard: waypointWithTraits(t, yard, "SHIPYARD"),
	}}
	cases := []struct {
		name       string
		configured []string
		scannedTyp string
		wantEvent  bool
	}{
		{"custom set matches scanned type", []string{"SHIP_ORE_HOUND"}, "SHIP_ORE_HOUND", true},
		{"default heavy not in custom set stays silent", []string{"SHIP_ORE_HOUND"}, "SHIP_BULK_FREIGHTER", false},
		{"empty config falls back to default set", nil, "SHIP_HEAVY_FREIGHTER", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeShipyardAPI{data: &domainPorts.ShipyardData{
				Symbol:    yard,
				ShipTypes: []domainPorts.ShipTypeInfo{{Type: tc.scannedTyp}},
				Ships:     []domainPorts.ShipListingData{{Type: tc.scannedTyp, PurchasePrice: 500_000}},
			}}
			events := &fakeRecorder{}
			s := NewShipyardScanner(api, newFakeInventory(), traits, events, shipyard.NewHeavyShipTypeSet(tc.configured))

			require.NoError(t, s.ScanAndSaveShipyard(scanCtx(), 1, yard))

			if tc.wantEvent {
				require.Len(t, events.events, 1, "type %s under set %v must emit", tc.scannedTyp, tc.configured)
			} else {
				require.Empty(t, events.events, "type %s under set %v must stay silent", tc.scannedTyp, tc.configured)
			}
		})
	}
}

// --- behavior: persist failure surfaces (caller decides non-fatality) ---------

// A store write failure must surface as an error — the tour treats it as
// non-fatal, but silently swallowing it here would hide a broken store forever.
func TestShipyardScanner_PersistFailure_SurfacesError(t *testing.T) {
	yard := "X1-AA-Y1"
	inventory := newFakeInventory()
	inventory.replaceErr = errors.New("db down")
	s := NewShipyardScanner(
		&fakeShipyardAPI{data: heavyYardData(yard)},
		inventory,
		&fakeTraits{waypoints: map[string]*shared.Waypoint{yard: waypointWithTraits(t, yard, "SHIPYARD")}},
		&fakeRecorder{},
		shipyard.NewHeavyShipTypeSet(nil),
	)

	err := s.ScanAndSaveShipyard(scanCtx(), 1, yard)
	require.Error(t, err)
	require.Contains(t, err.Error(), "db down")
}
