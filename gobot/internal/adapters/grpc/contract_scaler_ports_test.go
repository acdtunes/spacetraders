package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- fakes for the RoleResolver's three narrow read-ports ---

type fakeHomeReader struct {
	system   string
	readable bool
	err      error
}

func (f *fakeHomeReader) HomeSystem(ctx context.Context, playerID int) (string, bool, error) {
	return f.system, f.readable, f.err
}

type fakeWaypointLister struct {
	waypoints []*shared.Waypoint
	err       error
}

func (f *fakeWaypointLister) ListBySystem(ctx context.Context, systemSymbol string) ([]*shared.Waypoint, error) {
	return f.waypoints, f.err
}

// fakeMarketReader returns pre-built markets keyed by waypoint symbol; an unknown
// waypoint returns (nil, nil) — the "no market data" case the resolver treats as
// geometry-only (neither importer nor exporter).
type fakeMarketReader struct{ byWaypoint map[string]*market.Market }

func (f *fakeMarketReader) GetMarketData(ctx context.Context, waypointSymbol string, playerID int) (*market.Market, error) {
	return f.byWaypoint[waypointSymbol], nil
}

// goodSpec is a compact trade-good fixture.
type goodSpec struct {
	symbol string
	typ    market.TradeType
	volume int
}

func buildMarket(t *testing.T, waypoint string, goods ...goodSpec) *market.Market {
	t.Helper()
	tgs := make([]market.TradeGood, 0, len(goods))
	for _, g := range goods {
		tg, err := market.NewTradeGood(g.symbol, nil, nil, 100, 100, g.volume, g.typ)
		if err != nil {
			t.Fatalf("NewTradeGood(%s): %v", g.symbol, err)
		}
		tgs = append(tgs, *tg)
	}
	m, err := market.NewMarket(waypoint, tgs, time.Now())
	if err != nil {
		t.Fatalf("NewMarket(%s): %v", waypoint, err)
	}
	return m
}

func wp(symbol string, x, y float64) *shared.Waypoint {
	return &shared.Waypoint{Symbol: symbol, X: x, Y: y}
}

// eraFixture is the invariant home-system geometry (star at origin): a central cluster of contract
// sinks (importers, inner band <=300), a far ring of raw exporters, one far-outlier importer (the J
// sink), and an EXCHANGE-only waypoint (neither produced nor consumed → unclassified). Names are
// arbitrary per-era symbols; the lookup keys on MARKET ROLE + geometry, never a letter/number.
func newRoleResolverFixture(homeReadable bool) *contractScalerRoleResolver {
	waypoints := []*shared.Waypoint{
		wp("X1-SC-PARKHI", 100, 50),   // central importer, high demand
		wp("X1-SC-PARKLO", 150, 0),    // central importer, low demand
		wp("X1-SC-EXCHNGE", 120, 20),  // central EXCHANGE-only → unclassified
		wp("X1-SC-SOURCE1", 330, 0),   // far exporter → far source
		wp("X1-SC-JSINK70", 700, 100), // far outlier importer → far sink (served live)
	}
	markets := map[string]*market.Market{}
	return &contractScalerRoleResolver{
		home:      &fakeHomeReader{system: "X1-SC", readable: homeReadable},
		waypoints: &fakeWaypointLister{waypoints: waypoints},
		markets:   &fakeMarketReader{byWaypoint: markets},
	}
}

// TestRoleResolver_ResolvesEraRolesFromLiveMarketAndGeometry proves the once-at-arm lookup: this era's
// waypoints classify into central parks (inner-band importers), far sources (far-band exporters), and
// the single far sink (the farthest importer, served live) — from live market roles + geometry, never
// hardcoded names.
func TestRoleResolver_ResolvesEraRolesFromLiveMarketAndGeometry(t *testing.T) {
	r := newRoleResolverFixture(true)
	r.markets.(*fakeMarketReader).byWaypoint = map[string]*market.Market{
		"X1-SC-PARKHI":  buildMarket(t, "X1-SC-PARKHI", goodSpec{"FUEL", market.TradeTypeImport, 30}, goodSpec{"FOOD", market.TradeTypeImport, 20}),
		"X1-SC-PARKLO":  buildMarket(t, "X1-SC-PARKLO", goodSpec{"FOOD", market.TradeTypeImport, 10}),
		"X1-SC-EXCHNGE": buildMarket(t, "X1-SC-EXCHNGE", goodSpec{"FUEL", market.TradeTypeExchange, 99}),
		"X1-SC-SOURCE1": buildMarket(t, "X1-SC-SOURCE1", goodSpec{"IRON_ORE", market.TradeTypeExport, 40}),
		"X1-SC-JSINK70": buildMarket(t, "X1-SC-JSINK70", goodSpec{"ELECTRONICS", market.TradeTypeImport, 5}),
	}

	roles, demand, err := r.ResolveRoles(context.Background(), 1)
	if err != nil {
		t.Fatalf("ResolveRoles: %v", err)
	}

	if got := roles.CentralParks; len(got) != 2 || got[0] != "X1-SC-PARKHI" || got[1] != "X1-SC-PARKLO" {
		t.Fatalf("CentralParks = %v, want [X1-SC-PARKHI X1-SC-PARKLO] (inner-band importers, EXCHANGE excluded)", got)
	}
	if got := roles.FarSources; len(got) != 1 || got[0] != "X1-SC-SOURCE1" {
		t.Fatalf("FarSources = %v, want [X1-SC-SOURCE1] (far-band exporter)", got)
	}
	if roles.FarSink != "X1-SC-JSINK70" {
		t.Fatalf("FarSink = %q, want X1-SC-JSINK70 (the far-outlier importer, served live)", roles.FarSink)
	}
	// Demand proxy = import VOLUME (open Q a: prefer volume over count). PARKHI draws 30+20=50, PARKLO 10.
	if demand["X1-SC-PARKHI"] != 50 || demand["X1-SC-PARKLO"] != 10 {
		t.Fatalf("demand = %v, want PARKHI=50 PARKLO=10 (import-volume proxy)", demand)
	}
}

// TestRoleResolver_DemandFallsBackToImportCountWhenVolumeUnavailable proves the count fallback: a park
// whose import goods carry no volume signal still ranks (by number of distinct imports), so a
// zero-volume market never sinks to demand 0 (which would strip it from the spread).
func TestRoleResolver_DemandFallsBackToImportCountWhenVolumeUnavailable(t *testing.T) {
	r := newRoleResolverFixture(true)
	r.markets.(*fakeMarketReader).byWaypoint = map[string]*market.Market{
		"X1-SC-PARKHI": buildMarket(t, "X1-SC-PARKHI", goodSpec{"A", market.TradeTypeImport, 0}, goodSpec{"B", market.TradeTypeImport, 0}),
		"X1-SC-PARKLO": buildMarket(t, "X1-SC-PARKLO", goodSpec{"A", market.TradeTypeImport, 0}),
	}

	_, demand, err := r.ResolveRoles(context.Background(), 1)
	if err != nil {
		t.Fatalf("ResolveRoles: %v", err)
	}
	if demand["X1-SC-PARKHI"] != 2 || demand["X1-SC-PARKLO"] != 1 {
		t.Fatalf("demand = %v, want PARKHI=2 PARKLO=1 (import-count fallback when volume is 0)", demand)
	}
}

// TestRoleResolver_UnreadableHomeSystemYieldsEmptyEra proves the fail-safe: with no resolvable home
// system (cold pre-registration), the resolver returns empty roles + empty demand and NO error, so the
// scaler's armedPlan is empty and it no-ops (never buys against an unknown topology).
func TestRoleResolver_UnreadableHomeSystemYieldsEmptyEra(t *testing.T) {
	r := newRoleResolverFixture(false) // home unreadable

	roles, demand, err := r.ResolveRoles(context.Background(), 1)
	if err != nil {
		t.Fatalf("ResolveRoles: %v", err)
	}
	if len(roles.CentralParks) != 0 || len(roles.FarSources) != 0 || roles.FarSink != "" {
		t.Fatalf("unreadable home must yield empty roles, got %+v", roles)
	}
	if len(demand) != 0 {
		t.Fatalf("unreadable home must yield empty demand, got %v", demand)
	}
}

// --- Purchaser composition: buy+dedicate (kept primitive) then home demand-ranked ---

type fakeContractBuyer struct {
	order  fleetCmd.BuyOrder
	result fleetCmd.BuyResult
	err    error
}

func (f *fakeContractBuyer) BuyAndDedicate(ctx context.Context, order fleetCmd.BuyOrder) (fleetCmd.BuyResult, error) {
	f.order = order
	return f.result, f.err
}

type recordingSender struct{ sent []common.Request }

func (r *recordingSender) Send(ctx context.Context, request common.Request) (common.Response, error) {
	r.sent = append(r.sent, request)
	return nil, nil
}

// TestContractScalerPurchaser_BuysContractDedicatedThenHomesDemandRanked proves the composition: one
// scaler buy drives the kept buy primitive with the contract-delivery class (→ "contract" dedication)
// AND dispatches the HomeShipCommand carrying the spread standby set + per-park demand weights — the C1
// demand-ranked homing consumer. This is what makes the 3rd..Nth delivery hull pay (each covers a
// distinct central pickup region) instead of piling on one hub.
func TestContractScalerPurchaser_BuysContractDedicatedThenHomesDemandRanked(t *testing.T) {
	buyer := &fakeContractBuyer{result: fleetCmd.BuyResult{ShipSymbol: "SHIP-NEW", Price: 120000, Dedicated: true}}
	sender := &recordingSender{}
	p := &contractScalerPurchaser{buyer: buyer, med: sender, shipRepo: nil}

	order := contractScalerCmd.BuyOrder{
		PlayerID:        1,
		Unit:            contractscaler.PlanUnit{Role: contractscaler.DeliveryHauler, ShipType: "SHIP_LIGHT_HAULER", Target: "P2"},
		Yard:            "YARD-1",
		ExpectedPrice:   120000,
		DedicatedFleet:  "contract",
		StandbyStations: []string{"P1", "P2", "P3"},
		StandbyDemand:   map[string]float64{"P1": 3, "P2": 9, "P3": 5},
	}
	res, err := p.BuyAndHome(context.Background(), order)
	if err != nil {
		t.Fatalf("BuyAndHome: %v", err)
	}
	if res.ShipSymbol != "SHIP-NEW" || res.Price != 120000 {
		t.Fatalf("BuyResult = %+v, want SHIP-NEW @ 120000", res)
	}

	// The buy drives the kept primitive with the CONTRACT-DELIVERY class (→ "contract" exclusivity).
	if buyer.order.Class != fleetCmd.HullClassContractDelivery {
		t.Fatalf("buy class = %q, want the contract-delivery class (dedicates to \"contract\")", buyer.order.Class)
	}
	if buyer.order.ShipType != "SHIP_LIGHT_HAULER" || buyer.order.Yard != "YARD-1" {
		t.Fatalf("buy order didn't carry the plan unit's frame + yard: %+v", buyer.order)
	}

	// The fresh hull is homed demand-ranked: exactly one HomeShipCommand for it, carrying the spread set.
	if len(sender.sent) != 1 {
		t.Fatalf("dispatched %d commands, want exactly 1 (the HomeShipCommand)", len(sender.sent))
	}
	home, ok := sender.sent[0].(*contractCmd.HomeShipCommand)
	if !ok {
		t.Fatalf("dispatched %T, want *HomeShipCommand", sender.sent[0])
	}
	if home.ShipSymbol != "SHIP-NEW" {
		t.Fatalf("homed %q, want the freshly-bought SHIP-NEW", home.ShipSymbol)
	}
	if len(home.StandbyStations) != 3 {
		t.Fatalf("HomeShipCommand.StandbyStations = %v, want the 3-park spread set", home.StandbyStations)
	}
	if home.StandbyDemand["P2"] != 9 {
		t.Fatalf("HomeShipCommand.StandbyDemand missing the park weights (the homing consumer): %v", home.StandbyDemand)
	}
}

// TestContractScalerPurchaser_BuyFailureNeverHomes proves a failed buy short-circuits: no hull exists
// to home, so no HomeShipCommand is dispatched and the error propagates (the ramp records it).
func TestContractScalerPurchaser_BuyFailureNeverHomes(t *testing.T) {
	buyer := &fakeContractBuyer{err: context.DeadlineExceeded}
	sender := &recordingSender{}
	p := &contractScalerPurchaser{buyer: buyer, med: sender}

	_, err := p.BuyAndHome(context.Background(), contractScalerCmd.BuyOrder{PlayerID: 1, StandbyStations: []string{"P1"}})
	if err == nil {
		t.Fatal("a failed buy must propagate the error")
	}
	if len(sender.sent) != 0 {
		t.Fatalf("a failed buy must NOT dispatch a HomeShipCommand, got %d", len(sender.sent))
	}
}

// --- UNDEDICATED-REUSE TIER (sp-wfgsa): the reclaimer's find + re-dedicate + home ---

// fakeReclaimShipRepo serves a fixed fleet from FindAllByPlayer and records every AssignFleet call —
// the single write path a reclaim re-dedicates through (RULINGS #3). assignErr models a re-dedicate
// failure; findErr a fleet-read failure.
type fakeReclaimShipRepo struct {
	navigation.ShipRepository
	all       []*navigation.Ship
	findErr   error
	assignErr error
	assigned  []assignFleetCall
}

type assignFleetCall struct {
	symbol string
	fleet  string
}

func (r *fakeReclaimShipRepo) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	return r.all, r.findErr
}

func (r *fakeReclaimShipRepo) AssignFleet(ctx context.Context, shipSymbol, fleet string, playerID shared.PlayerID) error {
	if r.assignErr != nil {
		return r.assignErr
	}
	r.assigned = append(r.assigned, assignFleetCall{symbol: shipSymbol, fleet: fleet})
	return nil
}

// reclaimHull builds an owned hull across the four axes the reuse predicate reads: cargo capacity
// (0 = a probe / cargo-incapable), dedication tag ("" = undedicated), and nav status (in-transit =
// mid-task). A fresh hull is idle (no assignment) unless the caller assigns it.
func reclaimHull(t *testing.T, symbol string, cargoCapacity int, dedicatedFleet string, navStatus navigation.NavStatus) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(cargoCapacity, 0, nil)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	loc, err := shared.NewWaypoint("X1-SC-A1", 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), loc, fuel, 100, cargoCapacity, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navStatus,
	)
	require.NoError(t, err)
	if dedicatedFleet != "" {
		ship.SetDedicatedFleet(dedicatedFleet)
	}
	return ship
}

// THE REUSE-ELIGIBILITY PREDICATE (the load-bearing safety guard): a hull is reclaimable ONLY when it
// is idle, NOT in transit, UNDEDICATED (RULINGS #7 — never poach a dedicated hull of ANY fleet), and
// cargo-capable (a probe / 0-cargo hull re-dedicated to contract can't haul).
func TestIsReclaimable_ClassifiesReuseEligibility(t *testing.T) {
	assigned := reclaimHull(t, "BUSY", 40, "", navigation.NavStatusInOrbit)
	require.NoError(t, assigned.AssignToContainer("worker-1", shared.NewRealClock())) // mid-task under a coordinator

	cases := []struct {
		name string
		ship *navigation.Ship
		want bool
	}{
		{"idle undedicated cargo hull → reclaimable", reclaimHull(t, "FREE", 40, "", navigation.NavStatusInOrbit), true},
		{"dedicated to trade → never poached (RULINGS #7)", reclaimHull(t, "TRADE", 40, "trade", navigation.NavStatusInOrbit), false},
		{"dedicated to manufacturing → never poached", reclaimHull(t, "MFG", 40, "manufacturing", navigation.NavStatusInOrbit), false},
		{"already contract-dedicated → not undedicated", reclaimHull(t, "CONTRACT", 40, "contract", navigation.NavStatusInOrbit), false},
		{"undedicated probe / 0-cargo → cargo-incapable", reclaimHull(t, "PROBE", 0, "", navigation.NavStatusInOrbit), false},
		{"undedicated cargo hull in transit → mid-task", reclaimHull(t, "TRANSIT", 40, "", navigation.NavStatusInTransit), false},
		{"undedicated cargo hull assigned to a container → mid-task", assigned, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReclaimable(tc.ship); got != tc.want {
				t.Fatalf("isReclaimable(%s) = %v, want %v", tc.ship.ShipSymbol(), got, tc.want)
			}
		})
	}
}

// FindReclaimable selects the FIRST free idle cargo hull, skipping dedicated + probe hulls, and returns
// ok=false (NEVER poaches) when the fleet holds only dedicated idle hulls or is empty.
func TestFindReclaimable_SelectsOnlyFreeIdleCargoHulls(t *testing.T) {
	cases := []struct {
		name       string
		fleet      []*navigation.Ship
		wantOK     bool
		wantSymbol string
	}{
		{"skips dedicated + probe, returns the free cargo hull", []*navigation.Ship{
			reclaimHull(t, "TRADE-IDLE", 40, "trade", navigation.NavStatusInOrbit), // dedicated first → must be skipped
			reclaimHull(t, "PROBE", 0, "", navigation.NavStatusInOrbit),            // 0-cargo → excluded
			reclaimHull(t, "FREE-HAULER", 40, "", navigation.NavStatusInOrbit),     // ← the only eligible hull
		}, true, "FREE-HAULER"},
		{"only dedicated idle hulls → never poached (RULINGS #7)", []*navigation.Ship{
			reclaimHull(t, "TRADE-IDLE", 40, "trade", navigation.NavStatusInOrbit),
			reclaimHull(t, "MFG-IDLE", 40, "manufacturing", navigation.NavStatusInOrbit),
		}, false, ""},
		{"empty fleet → nothing to reuse", nil, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &contractScalerReclaimer{shipRepo: &fakeReclaimShipRepo{all: tc.fleet}}
			symbol, ok, err := r.FindReclaimable(context.Background(), 1)
			if err != nil {
				t.Fatalf("FindReclaimable: %v", err)
			}
			if ok != tc.wantOK || symbol != tc.wantSymbol {
				t.Fatalf("FindReclaimable = (%q, %v), want (%q, %v)", symbol, ok, tc.wantSymbol, tc.wantOK)
			}
		})
	}
}

// Fail-closed: a fleet-read error surfaces as an error (the coordinator falls through to a buy) — a read
// failure must NEVER be silently read as "no reusable hull" and must never risk a wrong reclaim.
func TestFindReclaimable_FleetReadErrorFailsClosed(t *testing.T) {
	r := &contractScalerReclaimer{shipRepo: &fakeReclaimShipRepo{findErr: errors.New("db down")}}
	if _, ok, err := r.FindReclaimable(context.Background(), 1); err == nil || ok {
		t.Fatalf("FindReclaimable = (ok=%v, err=%v), want (false, error) — a read failure fails closed", ok, err)
	}
}

// Reclaim RE-DEDICATES the hull to the exclusive "contract" fleet via the single write path (AssignFleet)
// and homes it demand-ranked exactly like a bought hull (one HomeShipCommand carrying the spread set +
// per-park demand weights). NO purchase — the hull already exists.
func TestReclaim_ReDedicatesToContractAndHomesDemandRanked(t *testing.T) {
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{reclaimHull(t, "FREE-HAULER", 40, "", navigation.NavStatusInOrbit)}}
	sender := &recordingSender{}
	r := &contractScalerReclaimer{shipRepo: repo, med: sender}

	err := r.Reclaim(context.Background(), contractScalerCmd.ReclaimOrder{
		PlayerID:        1,
		ShipSymbol:      "FREE-HAULER",
		StandbyStations: []string{"P1", "P2", "P3"},
		StandbyDemand:   map[string]float64{"P1": 3, "P2": 9, "P3": 5},
	})
	if err != nil {
		t.Fatalf("Reclaim: %v", err)
	}

	// Single-writer re-dedication to the exclusive "contract" fleet (RULINGS #3/#7).
	if len(repo.assigned) != 1 || repo.assigned[0].symbol != "FREE-HAULER" || repo.assigned[0].fleet != "contract" {
		t.Fatalf("AssignFleet calls = %+v, want one (FREE-HAULER → \"contract\")", repo.assigned)
	}

	// Homed demand-ranked, exactly like a bought hull: one HomeShipCommand carrying the spread + demand.
	if len(sender.sent) != 1 {
		t.Fatalf("dispatched %d commands, want exactly 1 (the HomeShipCommand)", len(sender.sent))
	}
	home, ok := sender.sent[0].(*contractCmd.HomeShipCommand)
	if !ok {
		t.Fatalf("dispatched %T, want *HomeShipCommand", sender.sent[0])
	}
	if home.ShipSymbol != "FREE-HAULER" {
		t.Fatalf("homed %q, want the reclaimed FREE-HAULER", home.ShipSymbol)
	}
	if len(home.StandbyStations) != 3 || home.StandbyDemand["P2"] != 9 {
		t.Fatalf("HomeShipCommand missing the spread set + park demand weights: stations=%v demand=%v", home.StandbyStations, home.StandbyDemand)
	}
}

// A failed re-dedicate leaves the hull UNTAKEN: Reclaim returns the error and dispatches NO homing (a
// non-dedicated hull must never be homed as contract) so the coordinator safely buys instead.
func TestReclaim_AssignFleetFailurePropagatesAndDoesNotHome(t *testing.T) {
	repo := &fakeReclaimShipRepo{assignErr: errors.New("CAS conflict")}
	sender := &recordingSender{}
	r := &contractScalerReclaimer{shipRepo: repo, med: sender}

	err := r.Reclaim(context.Background(), contractScalerCmd.ReclaimOrder{
		PlayerID:        1,
		ShipSymbol:      "FREE-HAULER",
		StandbyStations: []string{"P1", "P2", "P3"},
	})
	if err == nil {
		t.Fatal("a failed re-dedicate must propagate the error (hull NOT taken → the ramp buys)")
	}
	if len(sender.sent) != 0 {
		t.Fatalf("a failed re-dedicate must NOT home the hull, got %d dispatched", len(sender.sent))
	}
}
