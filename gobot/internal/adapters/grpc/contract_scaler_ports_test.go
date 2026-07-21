package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
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
