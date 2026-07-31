package queries

// Tests for the ONE property this query exists to preserve under the shipyard-read
// budget (sp-mb0er): its result is consumed by fail-closed money guards — the
// autosizer's price guard, the bootstrap capital gate, the probe-purchase treasury
// floor and purchase_ship's own pre-buy verification — so it must NEVER be
// answered from the persisted store (RULINGS #4).
//
// These run against the REAL scanner and the REAL budget rather than a spy, because
// the thing that could break is not "which constant is passed" but "what the budget
// does with it". A test that asserted the class would pass against a budget that
// had stopped honouring the class.

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

const (
	guardYard      = "X1-GUARD-Y1"
	guardHullType  = "SHIP_HEAVY_FREIGHTER"
	guardHullPrice = 1_918_293
)

type countingYardAPI struct {
	gets int
}

func (c *countingYardAPI) GetShipyard(context.Context, string, string, string) (*domainPorts.ShipyardData, error) {
	c.gets++
	return &domainPorts.ShipyardData{
		Symbol:    guardYard,
		ShipTypes: []domainPorts.ShipTypeInfo{{Type: guardHullType}},
		Ships: []domainPorts.ShipListingData{
			{Type: guardHullType, PurchasePrice: guardHullPrice, Supply: "MODERATE"},
		},
	}, nil
}

// freshInventory reports the yard as scanned moments ago — the state in which a
// discretionary read is unambiguously declined, which is what makes the Earning
// assertion below mean something.
type freshInventory struct{}

func (freshInventory) ReplaceScan(context.Context, int, string, string, []domainShipyard.ShipTypeAvailability, time.Time) error {
	return nil
}
func (freshInventory) HasAnyOfTypes(context.Context, int, []string) (bool, error) { return true, nil }
func (freshInventory) ListByTypes(context.Context, int, []string) ([]domainShipyard.ShipTypeAvailability, error) {
	return nil, nil
}
func (freshInventory) LastScannedAt(context.Context, int, string) (time.Time, bool, error) {
	return time.Now(), true, nil
}

type yardTraits struct{ isYard bool }

func (y yardTraits) HasWaypointTrait(context.Context, string, string) (bool, error) {
	return y.isYard, nil
}

// contendedScanner builds the scanner in the worst state a money guard can meet:
// the yard was scanned a moment ago (nowhere near due) and the allowance bucket is
// fully drained, so every deniable read is declined.
func contendedScanner(t *testing.T, isYard bool) (*ship.ShipyardScanner, *countingYardAPI) {
	t.Helper()
	api := &countingYardAPI{}
	heavy := domainShipyard.NewHeavyShipTypeSet([]string{guardHullType})
	// A one-nanosecond rescan window so the scanner's own recency floor can never
	// be what skips the read — the budget must be the only thing deciding.
	scanner := ship.NewShipyardScanner(api, freshInventory{}, yardTraits{isYard: isYard}, nil, heavy, time.Nanosecond)

	budget := ship.NewYardScanBudget(0.12, 8, heavy)
	for i := 0; i < 16; i++ {
		budget.Debit("X1-DRAIN-Y")
	}
	scanner.SetScanBudget(budget)
	return scanner, api
}

func guardCtx() context.Context {
	return common.WithPlayerToken(context.Background(), "test-token")
}

// THE PRE-BUY PRICE VERIFICATION IS NEVER SERVED FROM STORE.
//
// The control comes first and is load-bearing: it proves these exact conditions DO
// decline a deniable read. Without it the second assertion would pass against a
// budget that had simply stopped declining anything.
//
// This is the test that fails if the handler's read is reclassified from Earning to
// Discretionary — i.e. if a money guard is ever allowed to answer from the store.
func TestGetShipyardListings_PriceReadIsNeverServedFromStore(t *testing.T) {
	scanner, api := contendedScanner(t, true)

	require.NoError(t, scanner.ScanAndSaveShipyard(guardCtx(), 1, guardYard))
	require.Equal(t, 0, api.gets,
		"precondition: in these conditions a discretionary shipyard read IS declined")

	h := NewGetShipyardListingsHandler(scanner, nil)
	resp, err := h.Handle(guardCtx(), &GetShipyardListingsQuery{
		SystemSymbol:   "X1-GUARD",
		WaypointSymbol: guardYard,
		PlayerID:       shared.MustNewPlayerID(1),
	})

	require.NoError(t, err)
	require.Equal(t, 1, api.gets,
		"a money guard's price read must reach the API even on a drained budget at a just-scanned yard")

	listings, ok := resp.(*GetShipyardListingsResponse)
	require.True(t, ok)
	listing, found := listings.Shipyard.FindListingByType(guardHullType)
	require.True(t, found)
	require.Equal(t, guardHullPrice, listing.PurchasePrice, "the guard is handed the LIVE price")
}

// The SHIPYARD trait is read from a local cache that answers "not a shipyard" for
// any waypoint it has not warmed. Applying that filter to a money guard would let a
// cold cache silently answer a pre-buy price check with a not-a-shipyard error —
// a stale-store answer wearing an error's clothes. The caller of this query has
// already established it is standing at a yard.
func TestGetShipyardListings_PriceReadIgnoresAColdTraitCache(t *testing.T) {
	scanner, api := contendedScanner(t, false)

	require.NoError(t, scanner.ScanAndSaveShipyard(guardCtx(), 1, guardYard))
	require.Equal(t, 0, api.gets, "precondition: a discretionary read of an uncached waypoint is a no-op")

	h := NewGetShipyardListingsHandler(scanner, nil)
	_, err := h.Handle(guardCtx(), &GetShipyardListingsQuery{
		SystemSymbol:   "X1-GUARD",
		WaypointSymbol: guardYard,
		PlayerID:       shared.MustNewPlayerID(1),
	})

	require.NoError(t, err)
	require.Equal(t, 1, api.gets, "a cold trait cache must not stand in for a price the guard needs")
}

// Repeated verification is metered even though it is never denied, which is what
// keeps ONE number the honest total: a burst of pre-buy checks squeezes
// discretionary scanning rather than being added on top of the allowance.
func TestGetShipyardListings_EveryPriceReadDrawsOnTheOneAllowance(t *testing.T) {
	scanner, api := contendedScanner(t, true)
	before := scanner.ScanBudget().Snapshot()

	h := NewGetShipyardListingsHandler(scanner, nil)
	for i := 0; i < 3; i++ {
		_, err := h.Handle(guardCtx(), &GetShipyardListingsQuery{
			SystemSymbol:   "X1-GUARD",
			WaypointSymbol: guardYard,
			PlayerID:       shared.MustNewPlayerID(1),
		})
		require.NoError(t, err)
	}

	after := scanner.ScanBudget().Snapshot()
	require.Equal(t, 3, api.gets)
	require.Equal(t, before.Admitted+3, after.Admitted, "every guard read is charged to the allowance")
	require.Equal(t, before.Forced+3, after.Forced, "overdrafts on an empty bucket are recorded, not hidden")
}

// Without a scanner the query refuses rather than falling back to an unmetered API
// client — the bypass this change exists to close cannot be reopened by a missed
// wiring.
func TestGetShipyardListings_WithoutAScannerItRefusesRatherThanBypassing(t *testing.T) {
	h := NewGetShipyardListingsHandler(nil, nil)
	_, err := h.Handle(guardCtx(), &GetShipyardListingsQuery{
		SystemSymbol:   "X1-GUARD",
		WaypointSymbol: guardYard,
		PlayerID:       shared.MustNewPlayerID(1),
	})
	require.Error(t, err)
}
