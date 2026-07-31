package commands

// Tests that the shipyard search inside purchase_ship draws on the fleet's ONE
// shipyard-read budget instead of reaching the API itself (sp-mb0er).
//
// This search used to issue one live GET /shipyard PER SHIPYARD IN THE SYSTEM,
// uncached and unmetered, every time a hull had to be discovered — one of the four
// bypasses that put shipyard reads at 0.844 req/s, 44.7% of the whole server
// ceiling.
//
// THE FIXTURE USES TWO SEPARATE API COUNTERS, and that is the whole design. One
// sits behind the scanner and counts METERED reads; the other is the handler's own
// apiClient and counts BYPASSES. A restored bypass moves the second counter, which
// no assertion about the first could ever notice.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainShipyard "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

const searchHullType = "SHIP_HEAVY_FREIGHTER"

// meteredYardAPI sits BEHIND the scanner: every call it sees was admitted by the
// budget.
type meteredYardAPI struct {
	gets int
	// sells is what a live read reports the yard offering. Configurable because the
	// demand test needs live reads to contribute NOTHING to the wanted set, so that
	// the only thing that can move the count is the demand signal itself.
	sells string
}

func (m *meteredYardAPI) GetShipyard(_ context.Context, _, waypoint, _ string) (*domainPorts.ShipyardData, error) {
	m.gets++
	sells := m.sells
	if sells == "" {
		sells = searchHullType
	}
	return &domainPorts.ShipyardData{
		Symbol:    waypoint,
		ShipTypes: []domainPorts.ShipTypeInfo{{Type: sells}},
		Ships:     []domainPorts.ShipListingData{{Type: sells, PurchasePrice: 2_000_000}},
	}, nil
}

// bypassAPI is the handler's OWN api client. Any call it records is a shipyard read
// that escaped the allowance, which is the defect these tests exist to detect.
type bypassAPI struct {
	domainPorts.APIClient
	gets int
}

func (b *bypassAPI) GetShipyard(_ context.Context, _, waypoint, _ string) (*domainPorts.ShipyardData, error) {
	b.gets++
	return &domainPorts.ShipyardData{
		Symbol:    waypoint,
		ShipTypes: []domainPorts.ShipTypeInfo{{Type: searchHullType}},
		Ships:     []domainPorts.ShipListingData{{Type: searchHullType, PurchasePrice: 2_000_000}},
	}, nil
}

type searchInventory struct {
	lastScanned time.Time
	known       bool
	rows        []domainShipyard.ShipTypeAvailability
}

func (s *searchInventory) ReplaceScan(context.Context, int, string, string, []domainShipyard.ShipTypeAvailability, time.Time) error {
	return nil
}
func (s *searchInventory) HasAnyOfTypes(context.Context, int, []string) (bool, error) {
	return true, nil
}

// ListByTypes HONOURS the type filter, and that is load-bearing rather than
// faithful-for-its-own-sake: the demand test below asserts that shopping for a hull
// makes yards selling it count as wanted, and a stub that returned every row
// regardless of the filter would pass that test with the demand signal deleted.
func (s *searchInventory) ListByTypes(_ context.Context, _ int, shipTypes []string) ([]domainShipyard.ShipTypeAvailability, error) {
	wanted := make(map[string]struct{}, len(shipTypes))
	for _, t := range shipTypes {
		wanted[t] = struct{}{}
	}
	out := make([]domainShipyard.ShipTypeAvailability, 0, len(s.rows))
	for _, row := range s.rows {
		if _, ok := wanted[row.ShipType]; ok {
			out = append(out, row)
		}
	}
	return out, nil
}
func (s *searchInventory) LastScannedAt(context.Context, int, string) (time.Time, bool, error) {
	return s.lastScanned, s.known, nil
}

type alwaysYardTraits struct{}

func (alwaysYardTraits) HasWaypointTrait(context.Context, string, string) (bool, error) {
	return true, nil
}

func searchCtx() context.Context {
	return common.WithPlayerToken(context.Background(), "test-token")
}

func yardWaypoints(t *testing.T, n int) []*shared.Waypoint {
	t.Helper()
	out := make([]*shared.Waypoint, 0, n)
	for i := 0; i < n; i++ {
		wp, err := shared.NewWaypoint("X1-SEARCH-Y"+string(rune('A'+i)), float64(i), 0)
		require.NoError(t, err)
		out = append(out, wp)
	}
	return out
}

// newSearchHandler wires a handler whose hull search runs through a real scanner
// and a real budget, with a separate counter watching for bypasses.
func newSearchHandler(t *testing.T, inv *searchInventory, drained bool) (*PurchaseShipHandler, *meteredYardAPI, *bypassAPI) {
	t.Helper()
	metered := &meteredYardAPI{}
	bypass := &bypassAPI{}
	heavy := domainShipyard.NewHeavyShipTypeSet([]string{searchHullType})
	// A one-nanosecond rescan window so the scanner's own recency floor can never be
	// what suppresses a read — the budget must be the only thing deciding.
	scanner := ship.NewShipyardScanner(metered, inv, alwaysYardTraits{}, nil, heavy, time.Nanosecond)

	budget := ship.NewYardScanBudget(0.12, 8, heavy)
	if drained {
		for i := 0; i < 16; i++ {
			budget.Debit(1, "X1-DRAIN-Y")
		}
	}
	scanner.SetScanBudget(budget)

	return &PurchaseShipHandler{apiClient: bypass, yards: scanner}, metered, bypass
}

// THE BYPASS PROBE. On a drained allowance, at yards scanned moments ago, the
// search must spend nothing at all — and in particular must not reach its own API
// client. Restoring the direct apiClient.GetShipyard call moves bypass.gets and
// fails this test; no assertion about the metered counter could catch that, which
// is why both are checked.
func TestFilterShipyards_DeclinedByBudget_ReachesNoAPIAtAll(t *testing.T) {
	inv := &searchInventory{lastScanned: time.Now(), known: true}
	h, metered, bypass := newSearchHandler(t, inv, true)
	waypoints := yardWaypoints(t, 4)

	candidates, err := h.filterShipyardsBySupportedType(
		searchCtx(), waypoints, searchHullType, shared.MustNewPlayerID(1), waypoints[0],
	)

	require.NoError(t, err)
	require.Empty(t, candidates, "a declined read yields no candidate this tick, never a blind one")
	require.Equal(t, 0, bypass.gets,
		"the hull search must never reach the API outside the shipyard-read budget")
	require.Equal(t, 0, metered.gets,
		"and on a drained budget at just-scanned yards it must not spend a metered read either")
}

// The metered path is real: when the budget admits (these yards have never been
// scanned, so the anti-starvation escape applies), the reads happen — through the
// scanner, never around it.
func TestFilterShipyards_AdmittedByBudget_GoesThroughTheScannerNotAroundIt(t *testing.T) {
	inv := &searchInventory{known: false}
	h, metered, bypass := newSearchHandler(t, inv, false)
	waypoints := yardWaypoints(t, 3)

	candidates, err := h.filterShipyardsBySupportedType(
		searchCtx(), waypoints, searchHullType, shared.MustNewPlayerID(1), waypoints[0],
	)

	require.NoError(t, err)
	require.Len(t, candidates, 3)
	require.Equal(t, 3, metered.gets, "never-scanned yards are admitted and read through the scanner")
	require.Equal(t, 0, bypass.gets, "not one of them may reach the API unmetered")
}

// STORE-FIRST. A yard the persisted catalogue already says sells the type costs no
// request at all — which is what turns the old per-yard burst into a single query
// on the ordinary path.
func TestFilterShipyards_StoredCatalogueCostsNoRequest(t *testing.T) {
	waypoints := yardWaypoints(t, 3)
	rows := make([]domainShipyard.ShipTypeAvailability, 0, len(waypoints))
	for _, wp := range waypoints {
		rows = append(rows, domainShipyard.ShipTypeAvailability{
			WaypointSymbol: wp.Symbol, ShipType: searchHullType, PurchasePrice: 1_918_293,
		})
	}
	inv := &searchInventory{known: false, rows: rows}
	h, metered, bypass := newSearchHandler(t, inv, false)

	candidates, err := h.filterShipyardsBySupportedType(
		searchCtx(), waypoints, searchHullType, shared.MustNewPlayerID(1), waypoints[0],
	)

	require.NoError(t, err)
	require.Len(t, candidates, 3, "the store answers the whole system")
	require.Equal(t, 0, metered.gets, "a catalogued yard needs no live read")
	require.Equal(t, 0, bypass.gets)
}

// Shopping for a hull IS the demand signal: it must reach the budget, or the yards
// the fleet is about to buy from stay at the bottom of the rotation — the exact
// failure that left 80 of 84 heavy-selling yards unpriced for a day.
//
// DIFFERENTIAL, because an absolute count could not tell the signal from the
// scenery. Both runs are identical except for which hull is shopped for, and the
// store holds one yard selling SHIP_EXPLORER — a type that is NOT a structural
// heavy, so it can only count as wanted if the search registered demand for it.
// Delete the NoteDemand call and the two runs return the same number.
func TestFilterShipyards_ShoppingRegistersDemandWithTheBudget(t *testing.T) {
	wantedCount := func(shopFor string) int {
		rows := []domainShipyard.ShipTypeAvailability{
			{WaypointSymbol: "X1-SEARCH-YA", ShipType: "SHIP_EXPLORER", PurchasePrice: 0},
		}
		h, metered, _ := newSearchHandler(t, &searchInventory{known: false, rows: rows}, false)
		// Live reads report a hull nobody wants, so a yard can only enter the wanted
		// set through the STORE — and only if the search told the budget what it was
		// shopping for.
		metered.sells = "SHIP_PROBE"
		waypoints := yardWaypoints(t, 1)

		_, err := h.filterShipyardsBySupportedType(
			searchCtx(), waypoints, shopFor, shared.MustNewPlayerID(1), waypoints[0],
		)
		require.NoError(t, err)

		scanner, ok := h.yards.(*ship.ShipyardScanner)
		require.True(t, ok)
		// The demand picture is rebuilt on the admission path, so drive one admission
		// — which is what the rotation does continuously in production.
		require.NoError(t, scanner.ScanAndSaveShipyard(searchCtx(), 1, "X1-SEARCH-OTHER"))
		return scanner.ScanBudget().Snapshot().YardsWanted
	}

	baseline := wantedCount(searchHullType)
	shopped := wantedCount("SHIP_EXPLORER")

	require.Equal(t, baseline+1, shopped,
		"shopping for SHIP_EXPLORER must make the yard that sells it count as wanted")
}

// Without a scanner the search finds nothing rather than falling back to an
// unmetered per-yard sweep. Failing to discover a yard is recoverable; quietly
// restoring the burst is not.
func TestFilterShipyards_WithoutAScannerItFindsNothingRatherThanBypassing(t *testing.T) {
	bypass := &bypassAPI{}
	h := &PurchaseShipHandler{apiClient: bypass}
	waypoints := yardWaypoints(t, 3)

	candidates, err := h.filterShipyardsBySupportedType(
		searchCtx(), waypoints, searchHullType, shared.MustNewPlayerID(1), waypoints[0],
	)

	require.NoError(t, err)
	require.Empty(t, candidates)
	require.Equal(t, 0, bypass.gets)
}
