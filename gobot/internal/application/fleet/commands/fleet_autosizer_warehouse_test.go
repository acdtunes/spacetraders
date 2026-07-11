package commands

import (
	"context"
	"errors"
	"testing"
)

// --- fakes ------------------------------------------------------------------

type fakeWarehousePortfolio struct {
	chains   []PortfolioChain
	readable bool
	err      error
	calls    int
}

func (f *fakeWarehousePortfolio) RunningChains(ctx context.Context, playerID int) ([]PortfolioChain, bool, error) {
	f.calls++
	return f.chains, f.readable, f.err
}

type fakeWarehouseHulls struct {
	hulls []WarehouseHull
	err   error
}

func (f *fakeWarehouseHulls) WarehouseHulls(ctx context.Context, playerID int) ([]WarehouseHull, error) {
	return f.hulls, f.err
}

type dispatchCall struct {
	ship     string
	waypoint string
	goods    []string
}

type fakeWarehouseDispatch struct {
	calls []dispatchCall
	err   error
}

func (f *fakeWarehouseDispatch) DispatchWarehouse(ctx context.Context, playerID int, shipSymbol, waypoint string, goods []string) error {
	f.calls = append(f.calls, dispatchCall{ship: shipSymbol, waypoint: waypoint, goods: goods})
	return f.err
}

// whParams builds a DemandParams with the warehouse knobs set (the coordinator fills these from
// the live config each tick).
func whParams(minPersist int, floor float64, maxHulls int) DemandParams {
	return DemandParams{
		WarehouseMinTickPersistence: minPersist,
		WarehouseMinRealizedPerHour: floor,
		MaxWarehouseHulls:           maxHulls,
	}
}

func newWarehouseProvider(chains []PortfolioChain, hulls []WarehouseHull) (*WarehouseDemandProvider, *fakeWarehouseDispatch) {
	pf := &fakeWarehousePortfolio{chains: chains, readable: true}
	hs := &fakeWarehouseHulls{hulls: hulls}
	dp := &fakeWarehouseDispatch{}
	return NewWarehouseDemandProvider(pf, hs, dp), dp
}

// --- demand: hysteresis + pay gate + dedupe ---------------------------------

// A chain that has only just appeared in the portfolio is NOT yet a warehouse target: the
// tick-persistence hysteresis holds until it has been in the top-K for min ticks. This is the whole
// point — warehouses must not chase a chain vdld might retire on the very next tick.
func TestWarehouse_HysteresisHoldsBeforePersistence(t *testing.T) {
	chains := []PortfolioChain{
		{Good: "CLOTHING", ExportWaypoint: "X1-FD2-A1", RealizedPerHour: 500000, RealizedReadable: true},
	}
	p, _ := newWarehouseProvider(chains, nil)

	// Tick 1: persistence == 1, min == 2 → not durable → zero demand.
	d, err := p.Demand(context.Background(), 1, whParams(2, 100000, 8))
	if err != nil {
		t.Fatalf("Demand error: %v", err)
	}
	if !d.Readable {
		t.Fatalf("a readable portfolio must yield Readable=true even with zero durable targets")
	}
	if d.Demand != 0 {
		t.Fatalf("demand = %d, want 0 (chain not yet persisted min ticks)", d.Demand)
	}
}

// After the chain has persisted for min ticks it becomes a durable target and demand appears.
func TestWarehouse_DurableAfterPersistence(t *testing.T) {
	chains := []PortfolioChain{
		{Good: "CLOTHING", ExportWaypoint: "X1-FD2-A1", RealizedPerHour: 500000, RealizedReadable: true},
	}
	p, _ := newWarehouseProvider(chains, nil)
	params := whParams(2, 100000, 8)

	if d, _ := p.Demand(context.Background(), 1, params); d.Demand != 0 {
		t.Fatalf("tick 1 demand = %d, want 0", d.Demand)
	}
	d, _ := p.Demand(context.Background(), 1, params)
	if d.Demand != 1 {
		t.Fatalf("tick 2 demand = %d, want 1 (chain now persisted 2 ticks)", d.Demand)
	}
	if d.Current != 0 {
		t.Fatalf("current = %d, want 0 (no warehouse hull placed yet)", d.Current)
	}
}

// A chain earning below the realized-per-hour floor is never a warehouse target, no matter how long
// it persists — the pay gate keeps warehouses off unprofitable chains (the export-ask-subsidy leak
// only pays back on a genuinely earning chain).
func TestWarehouse_PayGateExcludesSubFloorChain(t *testing.T) {
	chains := []PortfolioChain{
		{Good: "CLOTHING", ExportWaypoint: "X1-FD2-A1", RealizedPerHour: 500000, RealizedReadable: true},
		{Good: "FUEL", ExportWaypoint: "X1-FD2-B2", RealizedPerHour: 50000, RealizedReadable: true}, // below floor
	}
	p, _ := newWarehouseProvider(chains, nil)
	params := whParams(1, 100000, 8)

	d, _ := p.Demand(context.Background(), 1, params)
	if d.Demand != 1 {
		t.Fatalf("demand = %d, want 1 (only CLOTHING clears the 100k floor; FUEL excluded)", d.Demand)
	}
}

// A chain whose realized rate could not be read fails the pay gate CLOSED — an unproven earner never
// pulls a warehouse (RULINGS #4: the buy path fails closed on an unreadable input).
func TestWarehouse_UnreadableRate_ExcludedFromTargets(t *testing.T) {
	chains := []PortfolioChain{
		{Good: "CLOTHING", ExportWaypoint: "X1-FD2-A1", RealizedPerHour: 0, RealizedReadable: false},
	}
	p, _ := newWarehouseProvider(chains, nil)

	d, _ := p.Demand(context.Background(), 1, whParams(1, 100000, 8))
	if d.Demand != 0 {
		t.Fatalf("demand = %d, want 0 (unreadable realized rate fails the pay gate closed)", d.Demand)
	}
}

// Two chains that EXPORT FROM THE SAME WAYPOINT are one warehouse target (co-export dedupe): one hull
// buffers both goods. Demand counts distinct export waypoints, not chains.
func TestWarehouse_CoExportDedupe_OneHullTwoGoods(t *testing.T) {
	chains := []PortfolioChain{
		{Good: "CLOTHING", ExportWaypoint: "X1-FD2-A1", RealizedPerHour: 500000, RealizedReadable: true},
		{Good: "FABRICS", ExportWaypoint: "X1-FD2-A1", RealizedPerHour: 300000, RealizedReadable: true},
	}
	p, _ := newWarehouseProvider(chains, nil)

	d, _ := p.Demand(context.Background(), 1, whParams(1, 100000, 8))
	if d.Demand != 1 {
		t.Fatalf("demand = %d, want 1 (two co-exported goods at one waypoint = one warehouse)", d.Demand)
	}
}

// Demand is capped at max_warehouse_hulls — the autosizer never wants more warehouses than the
// captain's ceiling, however many durable chains exist.
func TestWarehouse_DemandCappedAtMaxHulls(t *testing.T) {
	chains := []PortfolioChain{
		{Good: "A", ExportWaypoint: "WP1", RealizedPerHour: 500000, RealizedReadable: true},
		{Good: "B", ExportWaypoint: "WP2", RealizedPerHour: 400000, RealizedReadable: true},
		{Good: "C", ExportWaypoint: "WP3", RealizedPerHour: 300000, RealizedReadable: true},
	}
	p, _ := newWarehouseProvider(chains, nil)

	d, _ := p.Demand(context.Background(), 1, whParams(1, 100000, 2)) // max 2
	if d.Demand != 2 {
		t.Fatalf("demand = %d, want 2 (3 durable targets capped at max_warehouse_hulls=2)", d.Demand)
	}
}

// --- fail-closed reads ------------------------------------------------------

// An unreadable portfolio fails the whole warehouse pass CLOSED: no demand (no buy), and — proven in
// the dispatch tests — no hull is moved. A missing portfolio signal must never spend or strand.
func TestWarehouse_UnreadablePortfolio_FailsClosed(t *testing.T) {
	pf := &fakeWarehousePortfolio{readable: false}
	hs := &fakeWarehouseHulls{}
	p := NewWarehouseDemandProvider(pf, hs, &fakeWarehouseDispatch{})

	d, err := p.Demand(context.Background(), 1, whParams(1, 100000, 8))
	if err != nil {
		t.Fatalf("a portfolio read miss must fail closed, not error the tick; got %v", err)
	}
	if d.Readable {
		t.Fatalf("unreadable portfolio must yield Readable=false (fail-closed)")
	}
}

func TestWarehouse_PortfolioError_FailsClosed(t *testing.T) {
	pf := &fakeWarehousePortfolio{err: errors.New("db down")}
	p := NewWarehouseDemandProvider(pf, &fakeWarehouseHulls{}, &fakeWarehouseDispatch{})

	d, err := p.Demand(context.Background(), 1, whParams(1, 100000, 8))
	if err != nil {
		t.Fatalf("a portfolio error must fail closed, not error the tick; got %v", err)
	}
	if d.Readable {
		t.Fatalf("errored portfolio must yield Readable=false (fail-closed)")
	}
}

// An unreadable warehouse-hull count fails the buy path closed too: without knowing the current pool
// the coordinator cannot size a shortfall without risking over-buying.
func TestWarehouse_HullReadError_FailsClosed(t *testing.T) {
	chains := []PortfolioChain{{Good: "A", ExportWaypoint: "WP1", RealizedPerHour: 500000, RealizedReadable: true}}
	pf := &fakeWarehousePortfolio{chains: chains, readable: true}
	hs := &fakeWarehouseHulls{err: errors.New("ship repo down")}
	p := NewWarehouseDemandProvider(pf, hs, &fakeWarehouseDispatch{})

	d, _ := p.Demand(context.Background(), 1, whParams(1, 100000, 8))
	if d.Readable {
		t.Fatalf("unreadable warehouse-hull count must fail the buy path closed (Readable=false)")
	}
}

// --- dispatch: placement + co-export goods ----------------------------------

// An idle warehouse hull is dispatched to the highest-earning uncovered durable target, carrying the
// full co-exported goods list for that waypoint.
func TestWarehouse_DispatchesIdleHullToUncoveredTarget(t *testing.T) {
	chains := []PortfolioChain{
		{Good: "CLOTHING", ExportWaypoint: "X1-FD2-A1", RealizedPerHour: 500000, RealizedReadable: true},
		{Good: "FABRICS", ExportWaypoint: "X1-FD2-A1", RealizedPerHour: 300000, RealizedReadable: true},
	}
	hulls := []WarehouseHull{{ShipSymbol: "ENDURANCE-9", ParkedWaypoint: ""}} // idle
	p, dispatch := newWarehouseProvider(chains, hulls)
	params := whParams(1, 100000, 8)

	if _, err := p.Demand(context.Background(), 1, params); err != nil {
		t.Fatalf("Demand error: %v", err)
	}
	p.DispatchPlanned(context.Background(), 1, false)

	if len(dispatch.calls) != 1 {
		t.Fatalf("expected 1 dispatch call, got %d", len(dispatch.calls))
	}
	c := dispatch.calls[0]
	if c.ship != "ENDURANCE-9" || c.waypoint != "X1-FD2-A1" {
		t.Fatalf("dispatched %s→%s, want ENDURANCE-9→X1-FD2-A1", c.ship, c.waypoint)
	}
	if !goodsEqual(c.goods, []string{"CLOTHING", "FABRICS"}) {
		t.Fatalf("goods = %v, want [CLOTHING FABRICS] (co-export union)", c.goods)
	}
}

// A hull already parked at a durable target is NOT re-dispatched — no churn on a steady portfolio.
func TestWarehouse_CoveredTargetNotRedispatched(t *testing.T) {
	chains := []PortfolioChain{{Good: "CLOTHING", ExportWaypoint: "X1-FD2-A1", RealizedPerHour: 500000, RealizedReadable: true}}
	hulls := []WarehouseHull{{ShipSymbol: "ENDURANCE-9", ParkedWaypoint: "X1-FD2-A1"}}
	p, dispatch := newWarehouseProvider(chains, hulls)
	params := whParams(1, 100000, 8)

	p.Demand(context.Background(), 1, params)
	p.DispatchPlanned(context.Background(), 1, false)

	if len(dispatch.calls) != 0 {
		t.Fatalf("a hull already at its durable target must not be re-dispatched, got %d calls", len(dispatch.calls))
	}
}

// A dry run computes and would-dispatch but never calls the dispatch port (no-silent-dry-run: it
// still logs, but spends/moves nothing).
func TestWarehouse_DryRun_NoDispatchCall(t *testing.T) {
	chains := []PortfolioChain{{Good: "CLOTHING", ExportWaypoint: "X1-FD2-A1", RealizedPerHour: 500000, RealizedReadable: true}}
	hulls := []WarehouseHull{{ShipSymbol: "ENDURANCE-9", ParkedWaypoint: ""}}
	p, dispatch := newWarehouseProvider(chains, hulls)

	p.Demand(context.Background(), 1, whParams(1, 100000, 8))
	p.DispatchPlanned(context.Background(), 1, true) // dry run

	if len(dispatch.calls) != 0 {
		t.Fatalf("dry-run dispatch must not call the dispatch port, got %d calls", len(dispatch.calls))
	}
}

// An unreadable portfolio must not move any hull — dispatch is a no-op on the fail-closed pass.
func TestWarehouse_UnreadablePortfolio_NoDispatch(t *testing.T) {
	pf := &fakeWarehousePortfolio{readable: false}
	hs := &fakeWarehouseHulls{hulls: []WarehouseHull{{ShipSymbol: "ENDURANCE-9", ParkedWaypoint: "X1-OLD-Z9"}}}
	dispatch := &fakeWarehouseDispatch{}
	p := NewWarehouseDemandProvider(pf, hs, dispatch)

	p.Demand(context.Background(), 1, whParams(1, 100000, 8))
	p.DispatchPlanned(context.Background(), 1, false)

	if len(dispatch.calls) != 0 {
		t.Fatalf("an unreadable portfolio must never move a hull (fail-closed), got %d calls", len(dispatch.calls))
	}
}

// ACCEPTANCE (the bead's root-cause fix): after vdld resites a chain, the warehouse hull stranded on
// the RETIRED chain is moved to the newly-uncovered durable chain — never left on the dead chain, and
// the hull still serving a live chain is left in place. This is the anti-stranding invariant.
func TestWarehouse_ResiteMovesStrandedHullToNewDurableChain(t *testing.T) {
	ctx := context.Background()
	params := whParams(1, 100000, 8) // min-persistence 1 to isolate the stranding logic

	pf := &fakeWarehousePortfolio{readable: true}
	hs := &fakeWarehouseHulls{}
	dispatch := &fakeWarehouseDispatch{}
	p := NewWarehouseDemandProvider(pf, hs, dispatch)

	// Tick 1: two durable chains, each already covered by a co-located warehouse hull.
	pf.chains = []PortfolioChain{
		{Good: "CLOTHING", ExportWaypoint: "WP1", RealizedPerHour: 500000, RealizedReadable: true},
		{Good: "ELECTRONICS", ExportWaypoint: "WP2", RealizedPerHour: 400000, RealizedReadable: true},
	}
	hs.hulls = []WarehouseHull{
		{ShipSymbol: "H1", ParkedWaypoint: "WP1"},
		{ShipSymbol: "H2", ParkedWaypoint: "WP2"},
	}
	p.Demand(ctx, 1, params)
	p.DispatchPlanned(ctx, 1, false)
	if len(dispatch.calls) != 0 {
		t.Fatalf("steady state: both targets covered, expected no dispatch, got %d", len(dispatch.calls))
	}

	// Tick 2: vdld RESITES — WP2's chain retired, a new durable chain appears at WP3. H2 is now
	// stranded on the dead WP2.
	pf.chains = []PortfolioChain{
		{Good: "CLOTHING", ExportWaypoint: "WP1", RealizedPerHour: 500000, RealizedReadable: true},
		{Good: "MACHINERY", ExportWaypoint: "WP3", RealizedPerHour: 450000, RealizedReadable: true},
	}
	// H2 is still physically at WP2 (the retired chain) until we move it.
	hs.hulls = []WarehouseHull{
		{ShipSymbol: "H1", ParkedWaypoint: "WP1"},
		{ShipSymbol: "H2", ParkedWaypoint: "WP2"},
	}
	p.Demand(ctx, 1, params)
	p.DispatchPlanned(ctx, 1, false)

	if len(dispatch.calls) != 1 {
		t.Fatalf("resite must move exactly the stranded hull, got %d dispatch calls", len(dispatch.calls))
	}
	c := dispatch.calls[0]
	if c.ship != "H2" {
		t.Fatalf("the STRANDED hull H2 must be the one moved, got %s (H1 serves a live chain and must stay)", c.ship)
	}
	if c.waypoint != "WP3" {
		t.Fatalf("stranded hull must move to the new durable chain WP3, got %s (never left on the retired WP2)", c.waypoint)
	}
	if !goodsEqual(c.goods, []string{"MACHINERY"}) {
		t.Fatalf("goods = %v, want [MACHINERY]", c.goods)
	}
}

// --- coordinator integration ------------------------------------------------

// End-to-end through reconcileOnce: with the warehouse provider wired and the class enabled, the
// coordinator's DISPATCH step runs after the buy pass and places an idle warehouse hull on the
// durable chain — and because the idle hull is part of the pool, NO new hull is bought (demand==pool).
func TestReconcile_WarehouseDispatchesIdleHull(t *testing.T) {
	chains := []PortfolioChain{{Good: "CLOTHING", ExportWaypoint: "X1-FD2-A1", RealizedPerHour: 500000, RealizedReadable: true}}
	hulls := []WarehouseHull{{ShipSymbol: "ENDURANCE-9", ParkedWaypoint: ""}}
	provider, dispatch := newWarehouseProvider(chains, hulls)

	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	h.SetWarehouseProvider(provider)

	cmd := &RunFleetAutosizerCoordinatorCommand{
		PlayerID:                         7,
		ContainerID:                      "wh1",
		WarehouseHullsEnabled:            true,
		WarehouseMinChainTickPersistence: 1,
		WarehouseMinChainRealizedPerHour: 100000,
		MaxWarehouseHulls:                8,
	}

	if _, err := h.reconcileOnce(context.Background(), cmd); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(dispatch.calls) != 1 {
		t.Fatalf("expected the coordinator to dispatch the idle warehouse hull once, got %d", len(dispatch.calls))
	}
	if dispatch.calls[0].waypoint != "X1-FD2-A1" {
		t.Fatalf("dispatched to %s, want X1-FD2-A1", dispatch.calls[0].waypoint)
	}
}

// The dispatch step must NOT run when the warehouse class is disabled (opt-in): a wired provider on a
// coordinator with warehouse_hulls_enabled unset places nothing.
func TestReconcile_WarehouseDisabled_NoDispatch(t *testing.T) {
	chains := []PortfolioChain{{Good: "CLOTHING", ExportWaypoint: "X1-FD2-A1", RealizedPerHour: 500000, RealizedReadable: true}}
	hulls := []WarehouseHull{{ShipSymbol: "ENDURANCE-9", ParkedWaypoint: ""}}
	provider, dispatch := newWarehouseProvider(chains, hulls)

	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	h.SetWarehouseProvider(provider)

	// warehouse_hulls_enabled unset → opt-in class is off.
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 7, ContainerID: "wh1"}
	if _, err := h.reconcileOnce(context.Background(), cmd); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if len(dispatch.calls) != 0 {
		t.Fatalf("a disabled warehouse class must dispatch nothing, got %d calls", len(dispatch.calls))
	}
}

// goodsEqual compares two goods lists as ordered sequences (the provider returns a sorted, deduped
// list, so equality is order-sensitive against the sorted expectation).
func goodsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
