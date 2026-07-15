package expansion

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
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

	listings     map[string]int // waypoint -> live SHIP_PROBE price (the dock re-check + in-place surface)
	purchases    []*shipyardCmd.PurchaseShipCommand
	navigations  []*shipNav.NavigateRouteCommand // sp-iqv2: buyer relays to the winning yard (spy)
	navErr       error                           // set to model an unroutable relay (fail-closed)
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
	case *shipNav.NavigateRouteCommand:
		// sp-iqv2: the buyer relay. Record it for assertion and, unless a relay error is
		// modelled, report arrival at the destination so the dock price re-check runs.
		if m.navErr != nil {
			return nil, m.navErr
		}
		m.navigations = append(m.navigations, cmd)
		return &shipNav.NavigateRouteResponse{Status: "completed", CurrentLocation: cmd.Destination}, nil
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

// Scenario 1: with a target that has a nearer scanned probe-yard, the buy RELAYS the idle hull to
// that yard (sp-iqv2) and spawns the probe THERE — not movement-free at the home yard the hull sits
// at — and the finder is queried with the probe ship type and the target system, the sp-42ow reuse
// contract. Updated for sp-iqv2: resolveProximalBuy now drives the relay + dock re-check.
func TestBuyProbe_BuysAtProximalYard_NotHome_WhenTargetHasNearerYard(t *testing.T) {
	med := &probeFakeMediator{
		listings: map[string]int{
			"X1-HOME-YD": 25_000, // the in-place (home) surface the hull sits at
			"X1-NEAR-YD": 30_000, // the demand-proximal yard, re-priced at the dock after the relay
		},
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
	// The idle home hull is relayed to the proximal yard before the buy (sp-iqv2 — not movement-free).
	require.Len(t, med.navigations, 1)
	require.Equal(t, "X1-NEAR-YD", med.navigations[0].Destination)
	require.Equal(t, "BUYER-1", med.navigations[0].ShipSymbol)
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
			require.Empty(t, med.navigations, "the home in-place fallback is movement-free — no relay")
		})
	}
}

// ---- sp-iqv2: NAVIGATING probe buyer (movement-free spiral fix) -------------
//
// The live 4.2x overpayment: the buyer bought where an idle hull already SAT (all clustered at
// the home hub X1-VB74-A2, spiking it 20k→86k) instead of NAVIGATING a hull to the genuinely
// cheapest/nearest-target yard sp-hej4 had already selected. These tests drive the fix through
// the ProbePurchaser port with a spy on the buyer relay (NavigateRouteCommand) and the dock
// price re-check (GetShipyardListingsQuery).

// Test 1 (THE LIVE BUG, RED-first): a target frontier yard (X1-QA71-A1 @ 20k, scanned) with NO
// idle hull parked there and an idle hull sitting at the home hub yard (X1-VB74-A2 @ 86k) → the
// buyer NAVIGATES the hull to the frontier yard and buys at 20k, NOT movement-free at the home
// hub's spiked 86k. Mutation: revert to "buy where the hull sits" and the relay assertion fails.
func TestBuyProbe_NavigatesHullToFrontierYard_NotMovementFreeAtSpikedHome(t *testing.T) {
	const (
		homeYard    = "X1-VB74-A2" // the spiked home hub the idle hull sits at (live 86k)
		frontierYd  = "X1-QA71-A1" // the scanned frontier yard sp-hej4 selects (live 20k)
		frontierSys = "X1-QA71"
	)
	med := &probeFakeMediator{
		listings: map[string]int{
			homeYard:   86_000, // the depleted home hub — where the hull sits
			frontierYd: 20_000, // the cheap frontier yard — priced at the dock after the relay
		},
		boughtSymbol: "PROBE-NEW",
		boughtPrice:  20_000, // the frontier yard's live ask (not the home 86k)
	}
	ships := &probeFakeShipRepo{idle: []*navigation.Ship{probeShip(t, "IDLE-AT-HOME", homeYard)}}
	finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{
		yard(frontierYd, frontierSys, 0, 20_000),
	}}
	p := NewProbePurchaser(med, ships, finder)

	target := probebuy.ProbeTarget{System: frontierSys, HopPenaltyCredits: probebuy.DefaultHopPenaltyCredits}
	price, symbol, err := p.BuyProbe(context.Background(), shared.MustNewPlayerID(1), 90_000, target)

	require.NoError(t, err)
	require.Equal(t, 20_000, price, "the buy pays the frontier yard's 20k, not the home hub's spiked 86k")
	require.Equal(t, "PROBE-NEW", symbol)
	// The relay: the idle home hull is NAVIGATED to the frontier yard before the buy.
	require.Len(t, med.navigations, 1, "an idle hull is relayed to the winning yard — never a movement-free home buy")
	require.Equal(t, frontierYd, med.navigations[0].Destination, "the buyer navigates to the frontier yard")
	require.Equal(t, "IDLE-AT-HOME", med.navigations[0].ShipSymbol, "the idle undedicated home hull is the relay buyer")
	// The buy lands AT the frontier yard through that same relayed hull.
	require.Len(t, med.purchases, 1)
	require.Equal(t, frontierYd, med.purchases[0].ShipyardWaypoint, "the probe is bought at the frontier yard, not the home hub")
	require.Equal(t, "IDLE-AT-HOME", med.purchases[0].PurchasingShipSymbol)
}

// Test 2: among several reachable scanned yards the buy picks the lowest price+relay score and
// RELAYS the hull there (sp-iqv2). Candidates span systems; the cheapest reachable wins and the
// idle home hull is navigated to it.
func TestBuyProbe_SelectsCheapestReachableYard_AndRelaysThere(t *testing.T) {
	const homeYard = "X1-HOME-YD"
	med := &probeFakeMediator{
		listings: map[string]int{
			homeYard:     99_000,  // where the idle hull sits — never bought at
			"X1-FAR-A1":  100_000, // cheapest scanned ask (2 hops)
			"X1-NEAR-A1": 130_000, // nearer but pricier (1 hop)
			"X1-MID-A1":  140_000, // 1 hop, priciest
		},
		boughtSymbol: "PROBE-NEW",
		boughtPrice:  100_000,
	}
	ships := &probeFakeShipRepo{idle: []*navigation.Ship{probeShip(t, "IDLE-AT-HOME", homeYard)}}
	finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{
		yard("X1-NEAR-A1", "X1-NEAR", 1, 130_000),
		yard("X1-MID-A1", "X1-MID", 1, 140_000),
		yard("X1-FAR-A1", "X1-FAR", 2, 100_000),
	}}
	p := NewProbePurchaser(med, ships, finder)

	// Low hop penalty → price dominates: the far-cheaper yard wins the price+relay score.
	target := probebuy.ProbeTarget{System: "X1-DEST", HopPenaltyCredits: 10_000, SiblingPriceMarginCredits: probebuy.DefaultSiblingPriceMarginCredits}
	price, _, err := p.BuyProbe(context.Background(), shared.MustNewPlayerID(1), 200_000, target)

	require.NoError(t, err)
	require.Equal(t, 100_000, price, "the buy pays the cheapest reachable yard's ask")
	require.Len(t, med.navigations, 1)
	require.Equal(t, "X1-FAR-A1", med.navigations[0].Destination, "the hull is relayed to the cheapest reachable yard")
	require.Len(t, med.purchases, 1)
	require.Equal(t, "X1-FAR-A1", med.purchases[0].ShipyardWaypoint)
}

// Test 3 (supply-depletion / load-balance): a NEAR yard whose repeated buys spiked its price
// (86k) loses to a cheaper FAR sibling (22k) once the gap exceeds the tunable margin — so a market
// can never spiral to 4x. Below the margin the proximity preference (high hop penalty) still keeps
// the near yard (no thrash). Observed through QuoteProbe (the yard the guard would price).
func TestQuoteProbe_SpreadsToCheaperSibling_WhenNearYardExceedsMargin(t *testing.T) {
	// hop penalty 50k makes proximity dominate: without the margin override the near-but-spiked
	// yard (86k+1*50k=136k) beats the far-cheap yard (22k+3*50k=172k) → the 4x spiral.
	candidates := []shipyardQueries.YardCandidate{
		yard("X1-NEAR-A1", "X1-NEAR", 1, 86_000), // depleted near yard (the spiral)
		yard("X1-FAR-A1", "X1-FAR", 3, 22_000),   // cheap far sibling
	}
	cases := []struct {
		name       string
		margin     int
		expectYard string
		expectPx   int
	}{
		// gap = 86k − 22k = 64k. Margin below the gap → abandon the depleted near yard, spread to far.
		{name: "gap exceeds margin -> spreads to the cheaper sibling", margin: 30_000, expectYard: "X1-FAR-A1", expectPx: 22_000},
		// Margin above the gap → tolerate the premium for proximity, keep the near yard (no thrash).
		{name: "gap within margin -> keeps the near yard", margin: 100_000, expectYard: "X1-NEAR-A1", expectPx: 86_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			med := &probeFakeMediator{listings: map[string]int{"X1-HOME-YD": 25_000}}
			ships := &probeFakeShipRepo{idle: []*navigation.Ship{probeShip(t, "BUYER-1", "X1-HOME-YD")}}
			finder := &probeFakeYardFinder{candidates: candidates}
			p := NewProbePurchaser(med, ships, finder)

			target := probebuy.ProbeTarget{System: "X1-DEST", HopPenaltyCredits: 50_000, SiblingPriceMarginCredits: tc.margin}
			price, gotYard, err := p.QuoteProbe(context.Background(), shared.MustNewPlayerID(1), target)

			require.NoError(t, err)
			require.Equal(t, tc.expectYard, gotYard, "the margin decides whether a depleted near yard is abandoned")
			require.Equal(t, tc.expectPx, price)
		})
	}
}

// Test 5: the money guard fires on the ACTUAL re-checked dock price, not the stale scan. A yard
// scanned cheap (20k, passes the caller's QuoteProbe guard) but LIVE-priced dear at the dock (90k,
// depleted since the scan) is REFUSED — never a blind 4x overpay past the treasury ceiling.
func TestBuyProbe_GuardsOnRecheckedDockPrice_RefusesStaleCheapScan(t *testing.T) {
	const yardWp, yardSys = "X1-QA71-A1", "X1-QA71"
	med := &probeFakeMediator{
		listings: map[string]int{
			"X1-HOME-YD": 40_000,
			yardWp:       90_000, // LIVE dock price — spiked since the 20k scan
		},
		boughtSymbol: "PROBE-NEW",
		boughtPrice:  90_000,
	}
	ships := &probeFakeShipRepo{idle: []*navigation.Ship{probeShip(t, "IDLE-AT-HOME", "X1-HOME-YD")}}
	finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{
		yard(yardWp, yardSys, 0, 20_000), // STALE cheap scan
	}}
	p := NewProbePurchaser(med, ships, finder)

	target := probebuy.ProbeTarget{System: yardSys, HopPenaltyCredits: probebuy.DefaultHopPenaltyCredits, SiblingPriceMarginCredits: probebuy.DefaultSiblingPriceMarginCredits}
	_, _, err := p.BuyProbe(context.Background(), shared.MustNewPlayerID(1), 50_000, target) // budget = 25% treasury

	require.Error(t, err, "the re-checked 90k dock price exceeds the 50k budget → refuse")
	require.Contains(t, err.Error(), "exceeds budget")
	require.Empty(t, med.purchases, "no purchase is issued when the actual dock price blows the guard")
	require.Len(t, med.navigations, 1, "the relay happened before the dock re-check exposed the spike")
}

// Test (movement-free branch): when an idle hull already sits at the winning yard, no relay is
// issued — the buy is movement-free there. Guards against a needless relay regression.
func TestBuyProbe_SkipsRelay_WhenHullAlreadyAtWinningYard(t *testing.T) {
	const yardWp, yardSys = "X1-QA71-A1", "X1-QA71"
	med := &probeFakeMediator{
		listings:     map[string]int{yardWp: 20_000},
		boughtSymbol: "PROBE-NEW",
		boughtPrice:  20_000,
	}
	ships := &probeFakeShipRepo{idle: []*navigation.Ship{probeShip(t, "IDLE-AT-YARD", yardWp)}}
	finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{
		yard(yardWp, yardSys, 0, 20_000),
	}}
	p := NewProbePurchaser(med, ships, finder)

	target := probebuy.ProbeTarget{System: yardSys, HopPenaltyCredits: probebuy.DefaultHopPenaltyCredits, SiblingPriceMarginCredits: probebuy.DefaultSiblingPriceMarginCredits}
	price, _, err := p.BuyProbe(context.Background(), shared.MustNewPlayerID(1), 50_000, target)

	require.NoError(t, err)
	require.Equal(t, 20_000, price)
	require.Empty(t, med.navigations, "a hull already at the winning yard needs no relay")
	require.Len(t, med.purchases, 1)
	require.Equal(t, yardWp, med.purchases[0].ShipyardWaypoint)
}

// Test 6: the navigating buyer is SHARED by both consumers — the frontier coordinator (explicit
// tuned hop penalty + margin) and the freshness sizer (the package defaults). The SAME adapter
// relays to the proximal yard under both target shapes (main.go wires one NewProbePurchaser to
// both handlers). Input variations of one behavior, parametrized.
func TestBuyProbe_NavigatingBuyer_SharedByBothConsumerTargetShapes(t *testing.T) {
	const frontierYd, frontierSys = "X1-QA71-A1", "X1-QA71"
	cases := []struct {
		name   string
		target probebuy.ProbeTarget
	}{
		{name: "frontier-shaped target (tuned penalty + margin)", target: probebuy.ProbeTarget{System: frontierSys, HopPenaltyCredits: 40_000, SiblingPriceMarginCredits: 25_000}},
		{name: "freshsizer-shaped target (package defaults)", target: probebuy.ProbeTarget{System: frontierSys, HopPenaltyCredits: probebuy.DefaultHopPenaltyCredits, SiblingPriceMarginCredits: probebuy.DefaultSiblingPriceMarginCredits}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			med := &probeFakeMediator{
				listings:     map[string]int{"X1-HOME-YD": 86_000, frontierYd: 20_000},
				boughtSymbol: "PROBE-NEW",
				boughtPrice:  20_000,
			}
			ships := &probeFakeShipRepo{idle: []*navigation.Ship{probeShip(t, "IDLE-AT-HOME", "X1-HOME-YD")}}
			finder := &probeFakeYardFinder{candidates: []shipyardQueries.YardCandidate{yard(frontierYd, frontierSys, 0, 20_000)}}
			p := NewProbePurchaser(med, ships, finder)

			price, _, err := p.BuyProbe(context.Background(), shared.MustNewPlayerID(1), 90_000, tc.target)

			require.NoError(t, err)
			require.Equal(t, 20_000, price, "both consumer shapes buy at the cheap frontier yard, not the spiked home")
			require.Len(t, med.navigations, 1)
			require.Equal(t, frontierYd, med.navigations[0].Destination)
		})
	}
}
