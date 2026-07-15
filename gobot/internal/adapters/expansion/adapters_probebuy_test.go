package expansion

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// ---- port-boundary doubles (sp-hej4 demand-proximal probe buying) ----------
//
// The ProbePurchaser adapter is exercised through its PORT (QuoteProbe/BuyProbe) with doubles
// at its three collaborator boundaries: the mediator (shipyard listings + the purchase command),
// the ship repository (idle undedicated buyers), and the sp-42ow yard finder (scanned probe-yards
// near a target). Observable outcomes only — the yard a quote returns and the yard a buy executes
// at — never the adapter's internals.

// probeFakeMediator answers the two requests the adapter dispatches: a GetShipyardListingsQuery
// (the live in-place home-yard price) and a PurchaseShipCommand (the buy, captured for assertion).
// Embeds common.Mediator so any OTHER request nil-panics — the fake stays honest about what the
// adapter sends.
type probeFakeMediator struct {
	common.Mediator

	listings     map[string]int // waypoint -> live SHIP_PROBE price (the in-place fallback surface)
	purchases    []*shipyardCmd.PurchaseShipCommand
	boughtSymbol string
	boughtPrice  int
	deliverType  string // "" -> SHIP_PROBE (the honest delivery); set to model a substituted hull
}

func (m *probeFakeMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *shipyardQueries.GetShipyardListingsQuery:
		var listings []shipyard.ShipListing
		if price, ok := m.listings[cmd.WaypointSymbol]; ok {
			listings = append(listings, shipyard.NewShipListing(probeShipType, "Probe", "", price))
		}
		sy := shipyard.NewShipyard(cmd.WaypointSymbol, []string{probeShipType}, listings, 0)
		return &shipyardQueries.GetShipyardListingsResponse{Shipyard: sy}, nil
	case *shipyardCmd.PurchaseShipCommand:
		m.purchases = append(m.purchases, cmd)
		deliver := m.deliverType
		if deliver == "" {
			deliver = probeShipType
		}
		return &shipyardCmd.PurchaseShipResponse{
			Ship:          probeShip(nil, m.boughtSymbol, cmd.ShipyardWaypoint),
			PurchasePrice: m.boughtPrice,
			ShipType:      deliver,
		}, nil
	default:
		return nil, fmt.Errorf("probeFakeMediator: unexpected request %T", request)
	}
}

// probeFakeShipRepo answers FindIdleByPlayer (the only ship-repo method the adapter touches);
// every other method nil-panics via the embedded interface.
type probeFakeShipRepo struct {
	navigation.ShipRepository
	idle []*navigation.Ship
	err  error
}

func (r *probeFakeShipRepo) FindIdleByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return r.idle, r.err
}

// probeFakeYardFinder is the sp-42ow ReachableYardFinder double: it returns the scanned
// probe-yards near the target and records the query so a test can assert the reuse contract
// (the probe ship type + the target system as fromSystems).
type probeFakeYardFinder struct {
	candidates      []shipyardQueries.YardCandidate
	err             error
	lastShipTypes   []string
	lastFromSystems []string
	calls           int
}

func (f *probeFakeYardFinder) NearestYardsSelling(_ context.Context, _ int, shipTypes, fromSystems []string) ([]shipyardQueries.YardCandidate, error) {
	f.calls++
	f.lastShipTypes = shipTypes
	f.lastFromSystems = fromSystems
	return f.candidates, f.err
}

// probeShip builds an idle, undedicated satellite at waypoint — a valid in-place buyer and a
// valid hull to navigate to a target yard.
func probeShip(t *testing.T, symbol, waypoint string) *navigation.Ship {
	loc, err := shared.NewWaypoint(waypoint, 0, 0)
	requireNoErr(t, err)
	fuel, err := shared.NewFuel(100, 100)
	requireNoErr(t, err)
	cargo, err := shared.NewCargo(0, 0, nil)
	requireNoErr(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 100, 0, cargo, 30, "FRAME_PROBE", "SATELLITE", nil, navigation.NavStatusInOrbit)
	requireNoErr(t, err)
	return ship
}

// requireNoErr tolerates a nil *testing.T (probeShip is also called from the mediator fake, which
// has no t) — a fixture error there is a test-author bug that will surface as a nil ship anyway.
func requireNoErr(t *testing.T, err error) {
	if t != nil {
		require.NoError(t, err)
		return
	}
	if err != nil {
		panic(err)
	}
}

func yard(waypoint, system string, hops, price int) shipyardQueries.YardCandidate {
	return shipyardQueries.YardCandidate{
		WaypointSymbol: waypoint,
		SystemSymbol:   system,
		ShipType:       probeShipType,
		Hops:           hops,
		PurchasePrice:  price,
	}
}

// ---- tests -----------------------------------------------------------------

// Scenario 1: with a target that has a nearer scanned probe-yard, the buy spawns the probe THERE
// (the demand-proximal yard) instead of at the home yard the in-place hull sits at — and the
// finder is queried with the probe ship type and the target system, the sp-42ow reuse contract.
func TestBuyProbe_BuysAtProximalYard_NotHome_WhenTargetHasNearerYard(t *testing.T) {
	med := &probeFakeMediator{
		listings:     map[string]int{"X1-HOME-YD": 25_000}, // the in-place (home) surface
		boughtSymbol: "PROBE-NEW",
		boughtPrice:  30_000,
	}
	ships := &probeFakeShipRepo{idle: []*navigation.Ship{probeShip(t, "BUYER-1", "X1-HOME-YD")}}
	finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{
		yard("X1-NEAR-YD", "X1-NEAR", 1, 30_000),
	}}
	p := NewProbePurchaser(med, ships, finder)

	target := probebuy.ProbeTarget{System: "X1-NEAR", HopPenaltyCredits: probebuy.DefaultHopPenaltyCredits}
	price, symbol, err := p.BuyProbe(context.Background(), shared.MustNewPlayerID(1), 50_000, target)

	require.NoError(t, err)
	require.Equal(t, 30_000, price)
	require.Equal(t, "PROBE-NEW", symbol)
	require.Len(t, med.purchases, 1)
	require.Equal(t, "X1-NEAR-YD", med.purchases[0].ShipyardWaypoint,
		"the probe is bought at the yard NEAREST the target, not at the home yard the hull sits at")
	require.Equal(t, "BUYER-1", med.purchases[0].PurchasingShipSymbol, "an idle undedicated hull executes the buy")
	// sp-42ow reuse contract: finder queried with the probe ship type + the target system.
	require.Equal(t, []string{probeShipType}, finder.lastShipTypes)
	require.Equal(t, []string{"X1-NEAR"}, finder.lastFromSystems)
}

// Scenario 2: near-pricier vs far-cheaper — the tunable per-hop penalty arbitrates, and flipping
// it flips the chosen yard. Observed through QuoteProbe (the yard the guard would price), which
// needs no buyer. Same two candidates, opposite knob settings, opposite outcomes.
func TestQuoteProbe_PriceDistanceTradeoff_TunablePicksBothDirections(t *testing.T) {
	candidates := []shipyardQueries.YardCandidate{
		yard("X1-NEAR-YD", "X1-NEAR", 1, 200_000), // near but pricey
		yard("X1-FAR-YD", "X1-FAR", 5, 100_000),   // far but cheap
	}
	cases := []struct {
		name        string
		hopPenalty  int
		expectYard  string
		expectPrice int
	}{
		// Low penalty: a hop is cheap, so the 100k saving wins → the far-cheaper yard.
		//   near 200000 + 1*10000 = 210000  vs  far 100000 + 5*10000 = 150000
		{name: "low penalty prefers the far-cheaper yard", hopPenalty: 10_000, expectYard: "X1-FAR-YD", expectPrice: 100_000},
		// High penalty: a hop is dear, so proximity wins despite the premium → the near yard.
		//   near 200000 + 1*30000 = 230000  vs  far 100000 + 5*30000 = 250000
		{name: "high penalty prefers the near-pricier yard", hopPenalty: 30_000, expectYard: "X1-NEAR-YD", expectPrice: 200_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			med := &probeFakeMediator{listings: map[string]int{"X1-HOME-YD": 25_000}} // in-place surface (RED fallback)
			ships := &probeFakeShipRepo{idle: []*navigation.Ship{probeShip(t, "BUYER-1", "X1-HOME-YD")}}
			finder := &probeFakeYardFinder{candidates: candidates}
			p := NewProbePurchaser(med, ships, finder)

			target := probebuy.ProbeTarget{System: "X1-DEST", HopPenaltyCredits: tc.hopPenalty}
			price, gotYard, err := p.QuoteProbe(context.Background(), shared.MustNewPlayerID(1), target)

			require.NoError(t, err)
			require.Equal(t, tc.expectYard, gotYard, "the tunable picks the yard per its setting")
			require.Equal(t, tc.expectPrice, price, "the quoted price is the chosen yard's scanned ask")
		})
	}
}

// Scenarios 3 & 4 (and the fail-open contract generally): when NO proximal yard is known —
// empty scan store, nil result, an unreadable rank, or no target at all — the buy falls back to
// the home in-place yard with NO error. Missing shipyard data never fails a probe buy closed.
// Input variations of one behavior (Mandate 5, parametrized).
func TestBuyProbe_FailsOpenToHomeYard_WhenNoProximalYardKnown(t *testing.T) {
	cases := []struct {
		name       string
		target     probebuy.ProbeTarget
		candidates []shipyardQueries.YardCandidate
		finderErr  error
	}{
		{name: "empty inventory (no scanned yard)", target: probebuy.ProbeTarget{System: "X1-DEST", HopPenaltyCredits: 50_000}, candidates: []shipyardQueries.YardCandidate{}},
		{name: "nil inventory result", target: probebuy.ProbeTarget{System: "X1-DEST", HopPenaltyCredits: 50_000}, candidates: nil},
		{name: "unreadable rank (finder error)", target: probebuy.ProbeTarget{System: "X1-DEST", HopPenaltyCredits: 50_000}, finderErr: errors.New("scan store unreadable")},
		{name: "no target (aggregate caller)", target: probebuy.ProbeTarget{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			med := &probeFakeMediator{
				listings:     map[string]int{"X1-HOME-YD": 25_000},
				boughtSymbol: "PROBE-NEW",
				boughtPrice:  25_000,
			}
			ships := &probeFakeShipRepo{idle: []*navigation.Ship{probeShip(t, "BUYER-1", "X1-HOME-YD")}}
			finder := &probeFakeYardFinder{candidates: tc.candidates, err: tc.finderErr}
			p := NewProbePurchaser(med, ships, finder)

			price, symbol, err := p.BuyProbe(context.Background(), shared.MustNewPlayerID(1), 50_000, tc.target)

			require.NoError(t, err, "a missing proximal yard must never fail the buy closed")
			require.Equal(t, 25_000, price)
			require.Equal(t, "PROBE-NEW", symbol)
			require.Len(t, med.purchases, 1)
			require.Equal(t, "X1-HOME-YD", med.purchases[0].ShipyardWaypoint, "the buy falls back to the home in-place yard")
		})
	}
}
