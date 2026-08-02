package commands

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contractscaler"
)

// --- fakes ---

type fakeRoleResolver struct {
	roles  contractscaler.EraRoles
	demand map[string]float64
	calls  int
	err    error
}

func (f *fakeRoleResolver) ResolveRoles(ctx context.Context, playerID int) (contractscaler.EraRoles, map[string]float64, error) {
	f.calls++
	return f.roles, f.demand, f.err
}

// blockingRoleResolver models a DB read wedged on an exhausted connection pool: ResolveRoles blocks
// until its ctx is cancelled, then returns ctx.Err() — exactly how database/sql QueryContext behaves
// waiting for a free connection with no server statement_timeout. Only a per-read deadline unblocks it.
type blockingRoleResolver struct{}

func (blockingRoleResolver) ResolveRoles(ctx context.Context, playerID int) (contractscaler.EraRoles, map[string]float64, error) {
	<-ctx.Done()
	return contractscaler.EraRoles{}, nil, ctx.Err()
}

type fakeTreasury struct {
	credits  int64
	readable bool
}

func (f *fakeTreasury) Treasury(ctx context.Context, playerID int) (int64, bool, error) {
	return f.credits, f.readable, nil
}

type fakePrice struct {
	price    int64
	yard     string
	readable bool
}

func (f *fakePrice) NextHullPrice(ctx context.Context, playerID int, shipType string) (int64, string, bool, error) {
	return f.price, f.yard, f.readable, nil
}

type fakeCounter struct{ n int }

func (f *fakeCounter) ContractHullCount(ctx context.Context, playerID int) (int, error) {
	return f.n, nil
}

type fakePurchaser struct {
	orders     []BuyOrder // delivery buys (BuyAndHome)
	hullOrders []BuyOrder // undedicated depot-hull buys (BuyHull)
	// each buy decrements treasury via the linked fakeTreasury and increments the counter,
	// so the ramp loop sees the fleet grow and the cushion shrink like production.
	treasury *fakeTreasury
	counter  *fakeCounter
}

func (f *fakePurchaser) BuyAndHome(ctx context.Context, order BuyOrder) (BuyResult, error) {
	f.orders = append(f.orders, order)
	if f.treasury != nil {
		f.treasury.credits -= order.ExpectedPrice
	}
	if f.counter != nil {
		f.counter.n++
	}
	return BuyResult{ShipSymbol: "SHIP-NEW", Price: order.ExpectedPrice}, nil
}

// BuyHull buys an UNDEDICATED depot-role hull: it decrements the treasury like a real buy but does NOT
// touch the "contract"-fleet counter (a depot hull joins the depot registry via the grower, not the
// contract fleet) — so the delivery Current stays honest across ticks.
func (f *fakePurchaser) BuyHull(ctx context.Context, order BuyOrder) (BuyResult, error) {
	f.hullOrders = append(f.hullOrders, order)
	if f.treasury != nil {
		f.treasury.credits -= order.ExpectedPrice
	}
	return BuyResult{ShipSymbol: "DEPOT-HULL", Price: order.ExpectedPrice}, nil
}

// fakeReclaimer is the FREE reuse tier: FindReclaimable hands out `available` symbols head-first, and
// Reclaim consumes the head (a reclaimed hull is now dedicated, so no longer "available"). reclaimErr
// models a re-dedicate failure — the hull is NOT taken, so nothing is consumed or recorded and the ramp
// safely buys. findErr models a scan error (fail-closed → fall through to buy).
type fakeReclaimer struct {
	available  []string
	findErr    error
	reclaimErr error
	reclaimed  []string
	orders     []ReclaimOrder
	findCalls  int
}

func (f *fakeReclaimer) FindReclaimable(ctx context.Context, playerID int) (string, bool, error) {
	f.findCalls++
	if f.findErr != nil {
		return "", false, f.findErr
	}
	if len(f.available) == 0 {
		return "", false, nil
	}
	return f.available[0], true, nil
}

func (f *fakeReclaimer) Reclaim(ctx context.Context, order ReclaimOrder) error {
	if f.reclaimErr != nil {
		return f.reclaimErr // re-dedicate failed → hull NOT taken (don't consume, don't record)
	}
	f.available = f.available[1:]
	f.reclaimed = append(f.reclaimed, order.ShipSymbol)
	f.orders = append(f.orders, order)
	return nil
}

// fakeCeiling implements liveconfig.Reader with a settable live ceiling.
type fakeCeiling struct{ value int }

func (f *fakeCeiling) Snapshot(ctx context.Context, containerID string, playerID int) (liveconfig.Snapshot, error) {
	if f.value <= 0 {
		return liveconfig.Snapshot{}, nil
	}
	return liveconfig.Snapshot{ceilingKey: f.value}, nil
}

func threeParkRoles() contractscaler.EraRoles {
	return contractscaler.EraRoles{CentralParks: []string{"P1", "P2", "P3"}, FarSink: "J1"}
}

// harness wires a handler with fakes; treasury starts rich, price cheap.
func newHarness(ceiling int) (*RunContractScalerHandler, *fakePurchaser, *fakeCeiling, *fakeRoleResolver) {
	treasury := &fakeTreasury{credits: 5_000_000, readable: true}
	counter := &fakeCounter{n: 0}
	pur := &fakePurchaser{treasury: treasury, counter: counter}
	ceil := &fakeCeiling{value: ceiling}
	rr := &fakeRoleResolver{roles: threeParkRoles(), demand: map[string]float64{"P1": 3, "P2": 9, "P3": 5}}

	h := NewRunContractScalerHandler(nil)
	h.SetRoleResolver(rr)
	h.SetTreasuryReader(treasury)
	h.SetPriceReader(&fakePrice{price: 100_000, yard: "YARD", readable: true})
	h.SetFleetCounter(counter)
	h.SetPurchaser(pur)
	h.SetCeilingReader(ceil)
	return h, pur, ceil, rr
}

func reconcile(t *testing.T, h *RunContractScalerHandler, ceiling int) int {
	t.Helper()
	cmd := &RunContractScalerCommand{PlayerID: 1, ContainerID: "cs-1"}
	n, err := h.reconcileOnce(context.Background(), cmd)
	if err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	return n
}

// TestReconcileOnce_BoundsHangingRead is the sp-ljvxa regression: a top-of-tick DB read that blocks
// (exhausted connection pool, no server statement_timeout) must surface as an error at the read
// deadline, NOT freeze the scaler forever (the observed 11h RUNNING-but-silent hang). reconcileOnce
// bounds its reads with boundReadCtx, so a wedged resolve errors → the loop logs it → retries next tick.
func TestReconcileOnce_BoundsHangingRead(t *testing.T) {
	h := NewRunContractScalerHandler(nil)
	h.SetRoleResolver(blockingRoleResolver{})
	h.SetCeilingReader(&fakeCeiling{value: 10})
	h.SetReadTimeout(50 * time.Millisecond) // short so the test is fast; 0 would use the 5s default

	cmd := &RunContractScalerCommand{PlayerID: 1, ContainerID: "cs-1"}
	done := make(chan error, 1)
	go func() {
		// context.Background() ⇒ NO caller deadline: only the reconcile's OWN per-read bound can save it.
		_, err := h.reconcileOnce(context.Background(), cmd)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("reconcileOnce returned nil on a wedged read — a hanging read must surface as an error")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("want context.DeadlineExceeded from the bounded read, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconcileOnce HUNG on a blocking read — top-of-tick reads are not bounded (sp-ljvxa regression)")
	}
}

// RAMP with NO per-tick cap: a rich treasury buys the whole target in one tick.
func TestReconcile_RampsToCeilingInOneTickNoPerTickCap(t *testing.T) {
	h, pur, _, _ := newHarness(3)

	bought := reconcile(t, h, 3)
	if bought != 3 {
		t.Fatalf("bought %d, want 3 in a single tick (no per-tick cap)", bought)
	}
	if len(pur.orders) != 3 {
		t.Fatalf("purchaser saw %d orders, want 3", len(pur.orders))
	}
	// Delivery hulls first, demand-ranked distinct parks.
	if pur.orders[0].Unit.Target != "P2" || pur.orders[1].Unit.Target != "P3" || pur.orders[2].Unit.Target != "P1" {
		t.Fatalf("delivery targets = %q/%q/%q, want P2/P3/P1 (demand-ranked)", pur.orders[0].Unit.Target, pur.orders[1].Unit.Target, pur.orders[2].Unit.Target)
	}
}

// The ceiling caps the ramp: min(plan, ceiling).
func TestReconcile_StopsAtLiveCeiling(t *testing.T) {
	h, pur, _, _ := newHarness(2)

	bought := reconcile(t, h, 2)
	if bought != 2 {
		t.Fatalf("bought %d, want 2 (ceiling caps the plan)", bought)
	}
	if len(pur.orders) != 2 {
		t.Fatalf("purchaser saw %d orders, want 2", len(pur.orders))
	}
}

// THE 200k CUSHION IS THE SOLE GUARD: a treasury that cannot leave 200000 after
// the buy halts the ramp — even with plan + ceiling headroom.
func TestReconcile_TwoHundredKCushionHaltsBuying(t *testing.T) {
	h, pur, _, _ := newHarness(3)
	// treasury 250k, price 100k: first buy leaves 150k (< 200000) → the buy is BLOCKED.
	h.SetTreasuryReader(&fakeTreasury{credits: 250_000, readable: true})
	// re-link the purchaser to this treasury so a (blocked) buy would have shown.
	tr := &fakeTreasury{credits: 250_000, readable: true}
	pur.treasury = tr
	h.SetTreasuryReader(tr)

	bought := reconcile(t, h, 3)
	if bought != 0 {
		t.Fatalf("bought %d, want 0 — treasury-price (250k-100k=150k) is below the 200000 cushion", bought)
	}
	if len(pur.orders) != 0 {
		t.Fatalf("purchaser saw %d orders, want 0 (cushion gates the buy)", len(pur.orders))
	}
}

// Just above the cushion: treasury 300k, price 100k leaves exactly 200000 → the
// buy proceeds (>= is allowed).
func TestReconcile_BuysWhenExactlyAtCushion(t *testing.T) {
	h, _, _, _ := newHarness(1)
	tr := &fakeTreasury{credits: 300_000, readable: true}
	h.SetTreasuryReader(tr)
	h.SetPurchaser(&fakePurchaser{treasury: tr, counter: &fakeCounter{}})

	if bought := reconcile(t, h, 1); bought != 1 {
		t.Fatalf("bought %d, want 1 — 300k-100k=200000 meets the cushion exactly", bought)
	}
}

// Fail-closed: an unreadable treasury never spends.
func TestReconcile_UnreadableTreasuryFailsClosed(t *testing.T) {
	h, pur, _, _ := newHarness(3)
	h.SetTreasuryReader(&fakeTreasury{credits: 5_000_000, readable: false})

	if bought := reconcile(t, h, 3); bought != 0 || len(pur.orders) != 0 {
		t.Fatalf("bought %d (orders %d), want 0 — unreadable treasury fails closed", bought, len(pur.orders))
	}
}

// Fail-closed: an unreadable price never spends.
func TestReconcile_UnreadablePriceFailsClosed(t *testing.T) {
	h, pur, _, _ := newHarness(3)
	h.SetPriceReader(&fakePrice{price: 0, yard: "", readable: false})

	if bought := reconcile(t, h, 3); bought != 0 || len(pur.orders) != 0 {
		t.Fatalf("bought %d (orders %d), want 0 — unreadable price fails closed", bought, len(pur.orders))
	}
}

// The ceiling is LIVE: raising it between ticks lets the standing scaler buy the
// next fixed-sequence units immediately, no restart.
func TestReconcile_LiveCeilingReReadEachTick(t *testing.T) {
	h, pur, ceil, _ := newHarness(1)

	if bought := reconcile(t, h, 1); bought != 1 {
		t.Fatalf("tick 1 bought %d, want 1 at ceiling 1", bought)
	}
	ceil.value = 3 // operator raises the live ceiling, no restart
	if bought := reconcile(t, h, 3); bought != 2 {
		t.Fatalf("tick 2 bought %d, want 2 more (ramp 1→3 at the new live ceiling)", bought)
	}
	if len(pur.orders) != 3 {
		t.Fatalf("total orders %d, want 3 across the two ticks", len(pur.orders))
	}
}

// Default-off: a disabled scaler never buys (byte-identical to not running).
func TestReconcile_DisabledNeverBuys(t *testing.T) {
	h, pur, _, _ := newHarness(3)
	cmd := &RunContractScalerCommand{PlayerID: 1, ContainerID: "cs-1", Disabled: true}
	n, err := h.reconcileOnce(context.Background(), cmd)
	if err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if n != 0 || len(pur.orders) != 0 {
		t.Fatalf("disabled scaler bought %d (orders %d), want 0", n, len(pur.orders))
	}
}

// The role lookup is a LOOKUP resolved ONCE at arm, not a per-tick solve.
func TestReconcile_RoleLookupMemoizedAtArm(t *testing.T) {
	h, _, _, rr := newHarness(1)
	for i := 0; i < 3; i++ {
		reconcile(t, h, 1)
	}
	if rr.calls != 1 {
		t.Fatalf("ResolveRoles called %d times, want exactly 1 (memoized at arm)", rr.calls)
	}
}

// EXCLUSIVITY + FIXED PLACEMENT: every scaler buy carries the dedicated "contract" fleet + the
// ≤6 FIXED placement slots (TopDeliverySlots, ranked highest-demand first), so the hull is homed
// to its permanent slot and never poached. No demand map — the runtime homing carries no demand.
func TestReconcile_BuyOrderCarriesContractDedicationAndFixedPlacementSlots(t *testing.T) {
	h, pur, _, _ := newHarness(1)
	reconcile(t, h, 1)

	if len(pur.orders) != 1 {
		t.Fatalf("orders = %d, want 1", len(pur.orders))
	}
	o := pur.orders[0]
	if o.DedicatedFleet != "contract" {
		t.Fatalf("order fleet = %q, want the exclusive \"contract\" dedication", o.DedicatedFleet)
	}
	// The 3 parks (P2>P3>P1 by demand) are the fixed placement set — all ≤ the knee, ranked.
	want := []string{"P2", "P3", "P1"}
	if !reflect.DeepEqual(o.StandbyStations, want) {
		t.Fatalf("order StandbyStations = %v, want the fixed ≤6 placement slots %v", o.StandbyStations, want)
	}
}

// --- UNDEDICATED-REUSE TIER (sp-wfgsa): reclaim a free idle undedicated cargo hull before spending ---

// REUSE BEFORE BUY: a free idle undedicated cargo hull is RECLAIMED into the contract fleet, and the
// buyer is NEVER called — reuse replaces the spend (current++ with zero purchase).
func TestReconcile_ReclaimsFreeIdleHullBeforeBuying(t *testing.T) {
	h, pur, _, _ := newHarness(1)
	rec := &fakeReclaimer{available: []string{"HULL-IDLE"}}
	h.SetIdleHullReclaimer(rec)

	bought := reconcile(t, h, 1)
	if bought != 0 {
		t.Fatalf("bought %d, want 0 — a free idle hull is reclaimed, never bought", bought)
	}
	if len(pur.orders) != 0 {
		t.Fatalf("buyer saw %d orders, want 0 — reuse must precede AND replace the buy", len(pur.orders))
	}
	if len(rec.reclaimed) != 1 || rec.reclaimed[0] != "HULL-IDLE" {
		t.Fatalf("reclaimed %v, want [HULL-IDLE] (the free hull reused into the contract fleet)", rec.reclaimed)
	}
	// Reclaim homes to the SAME fixed ≤6 placement set a bought hull gets (homing parity, no demand).
	if len(rec.orders) != 1 || !reflect.DeepEqual(rec.orders[0].StandbyStations, []string{"P2", "P3", "P1"}) {
		t.Fatalf("reclaim order = %+v, want the fixed placement slots [P2 P3 P1] (homed like a bought hull)", rec.orders)
	}
}

// COMPOSES WITH BUYS: with 1 reusable hull and a target of 3, the ramp reclaims the one free hull and
// BUYS the remaining two in the same tick — reuse strictly reduces the buy count, never the fleet size.
func TestReconcile_ReclaimsFreeHullThenBuysTheRemainder(t *testing.T) {
	h, pur, _, _ := newHarness(3)
	rec := &fakeReclaimer{available: []string{"HULL-IDLE"}}
	h.SetIdleHullReclaimer(rec)

	bought := reconcile(t, h, 3)
	if bought != 2 {
		t.Fatalf("bought %d, want 2 — 1 free hull reclaimed + 2 bought = target 3", bought)
	}
	if len(pur.orders) != 2 {
		t.Fatalf("buyer saw %d orders, want 2 (the remainder after the free reclaim)", len(pur.orders))
	}
	if len(rec.reclaimed) != 1 {
		t.Fatalf("reclaimed %v, want exactly 1 (the single free idle hull)", rec.reclaimed)
	}
}

// NO REUSABLE HULL: a wired reclaimer that finds nothing falls through to the buy path unchanged.
func TestReconcile_NoReusableHullFallsThroughToBuy(t *testing.T) {
	h, pur, _, _ := newHarness(1)
	rec := &fakeReclaimer{} // nothing available
	h.SetIdleHullReclaimer(rec)

	bought := reconcile(t, h, 1)
	if bought != 1 || len(pur.orders) != 1 {
		t.Fatalf("bought %d (orders %d), want 1 — no reusable hull ⇒ the ramp buys", bought, len(pur.orders))
	}
	if len(rec.reclaimed) != 0 {
		t.Fatalf("reclaimed %v, want none", rec.reclaimed)
	}
}

// REUSE IS FREE OF THE CUSHION (RULINGS #4): with the treasury BELOW the buy cushion (a buy would be
// blocked), a free reclaim STILL proceeds — the 200000 cushion gates BUYS only, never reuse. Reuse
// strictly reduces spend, so it may run when the treasury cannot afford a purchase.
func TestReconcile_ReuseProceedsBelowBuyCushion(t *testing.T) {
	h, pur, _, _ := newHarness(1)
	// Treasury 250k, price 100k → treasury-price = 150k < 200000 cushion: a BUY is blocked.
	tr := &fakeTreasury{credits: 250_000, readable: true}
	h.SetTreasuryReader(tr)
	pur.treasury = tr
	rec := &fakeReclaimer{available: []string{"HULL-IDLE"}}
	h.SetIdleHullReclaimer(rec)

	bought := reconcile(t, h, 1)
	if bought != 0 {
		t.Fatalf("bought %d, want 0 — reuse is free, it never spends", bought)
	}
	if len(pur.orders) != 0 {
		t.Fatalf("buyer saw %d orders, want 0 — the cushion blocks BUYS, never reuse", len(pur.orders))
	}
	if len(rec.reclaimed) != 1 {
		t.Fatalf("reclaimed %v, want 1 — reuse must proceed BELOW the buy cushion (free ⇒ ungated)", rec.reclaimed)
	}
}

// RECLAIM ERROR ⇒ FALL THROUGH TO BUY: a re-dedicate failure means the hull was NOT taken, so the ramp
// buys instead (no double-count, no stranded slot).
func TestReconcile_ReclaimErrorFallsThroughToBuy(t *testing.T) {
	h, pur, _, _ := newHarness(1)
	rec := &fakeReclaimer{available: []string{"HULL-IDLE"}, reclaimErr: errors.New("assign-fleet failed")}
	h.SetIdleHullReclaimer(rec)

	bought := reconcile(t, h, 1)
	if bought != 1 || len(pur.orders) != 1 {
		t.Fatalf("bought %d (orders %d), want 1 — a failed reclaim (hull NOT taken) falls through to a buy", bought, len(pur.orders))
	}
	if len(rec.reclaimed) != 0 {
		t.Fatalf("reclaimed %v, want none — a re-dedicate failure must not record a reuse", rec.reclaimed)
	}
}

// DEFAULT-OFF BYTE-IDENTICAL: a disabled scaler never even SCANS for reuse (no FindReclaimable call), so
// a wired reclaimer + an available free hull change nothing until the operator arms the scaler.
func TestReconcile_DisabledNeverReclaims(t *testing.T) {
	h, pur, _, _ := newHarness(3)
	rec := &fakeReclaimer{available: []string{"HULL-IDLE"}}
	h.SetIdleHullReclaimer(rec)

	cmd := &RunContractScalerCommand{PlayerID: 1, ContainerID: "cs-1", Disabled: true}
	n, err := h.reconcileOnce(context.Background(), cmd)
	if err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
	if n != 0 || len(pur.orders) != 0 || len(rec.reclaimed) != 0 || rec.findCalls != 0 {
		t.Fatalf("disabled scaler: bought %d, orders %d, reclaimed %d, findCalls %d — want all 0 (byte-identical)", n, len(pur.orders), len(rec.reclaimed), rec.findCalls)
	}
}

// --- ROLE-AWARE RAMP (sp-urpxy): fill delivery→warehouse→stocker, reconcile the EXISTING depot ---

// fakeDepotCounter reports the depot's actuated warehouse/stocker Current and records every read (so a
// test can prove the depot is UNTOUCHED at the default ceiling — byte-identical).
type fakeDepotCounter struct {
	warehouses, stockers int
	whCalls, stkCalls    int
}

func (f *fakeDepotCounter) WarehouseCount(ctx context.Context, playerID int) (int, error) {
	f.whCalls++
	return f.warehouses, nil
}

func (f *fakeDepotCounter) StockerCount(ctx context.Context, playerID int) (int, error) {
	f.stkCalls++
	return f.stockers, nil
}

// fakeGrower records each depot growth and (linked to a counter) increments it, so across ticks the
// depot Current reflects what the ramp already grew. err models a launch failure (the ramp falls
// through to a buy on a failed reclaim-grow, and stops on a failed buy-grow).
type fakeGrower struct {
	counter        *fakeDepotCounter
	warehouseGrows []DepotGrowOrder
	stockerGrows   []DepotGrowOrder
	growCalls      []contractscaler.UnitRole // role of each grow CALL in order (recorded pre-error) — proves the warehouse/stocker interleaving
	err            error
}

func (f *fakeGrower) GrowWarehouse(ctx context.Context, order DepotGrowOrder) error {
	f.growCalls = append(f.growCalls, contractscaler.Warehouse)
	if f.err != nil {
		return f.err
	}
	f.warehouseGrows = append(f.warehouseGrows, order)
	if f.counter != nil {
		f.counter.warehouses++
	}
	return nil
}

func (f *fakeGrower) GrowStocker(ctx context.Context, order DepotGrowOrder) error {
	f.growCalls = append(f.growCalls, contractscaler.Stocker)
	if f.err != nil {
		return f.err
	}
	f.stockerGrows = append(f.stockerGrows, order)
	if f.counter != nil {
		f.counter.stockers++
	}
	return nil
}

// fakeDepotReclaimer is the HOME-SCOPED reuse tier double (DepotHullReclaimer, sp-fihvy): it hands out a
// symbol only when a home-reachable candidate is queued, and records every homeSystem a role's fill
// queried it with — so a test can prove the SAME home-scoping notion sp-fihvy wired for the stocker is
// now ALSO consulted for the warehouse (sp-fis8y), never a second reachability mechanism invented here.
type fakeDepotReclaimer struct {
	available []string
	findErr   error
	calls     []string // homeSystem of each FindReclaimableForHome call, in order
}

func (f *fakeDepotReclaimer) FindReclaimableForHome(ctx context.Context, playerID int, homeSystem string) (string, bool, error) {
	f.calls = append(f.calls, homeSystem)
	if f.findErr != nil {
		return "", false, f.findErr
	}
	if len(f.available) == 0 {
		return "", false, nil
	}
	return f.available[0], true, nil
}

// fakeReleaser is the surplus-delivery RE-ROLE double (DeliverySurplusReleaser): on
// ReleaseSurplusDelivery it decrements the "contract" fleet Current by count (un-dedicate) and pushes
// count freshly-idle symbols into the depot reuse tier — modelling how a released delivery hull becomes
// reclaimable into the warehouse deficit the SAME pass. calls records each release count in order.
type fakeReleaser struct {
	counter   *fakeCounter        // the "contract" fleet Current the release un-dedicates from
	reclaimer *fakeDepotReclaimer // released hulls surface here for the depot reuse-before-buy tier
	calls     []int
	released  int
	err       error
}

func (f *fakeReleaser) ReleaseSurplusDelivery(ctx context.Context, playerID, count int) (int, error) {
	f.calls = append(f.calls, count)
	if f.err != nil {
		return 0, f.err
	}
	for i := 0; i < count; i++ {
		f.reclaimer.available = append(f.reclaimer.available, fmt.Sprintf("REROLED-%d", f.released+i))
	}
	if f.counter != nil {
		f.counter.n -= count
	}
	f.released += count
	return count, nil
}

// manyParkRoles builds an n-park era with strictly-descending demand (P00 highest), so the central hub
// (the top-demand park, ranked[0]) is the deterministic P00 the warehouse/stocker anchor at.
func manyParkRoles(n int) (contractscaler.EraRoles, map[string]float64) {
	parks := make([]string, n)
	demand := map[string]float64{}
	for i := range parks {
		parks[i] = fmt.Sprintf("P%02d", i)
		demand[parks[i]] = float64(n - i)
	}
	return contractscaler.EraRoles{CentralParks: parks}, demand
}

// newDepotHarness wires a role-aware handler: an n-park plan (n delivery + WarehouseUnits warehouse +
// StockerUnits stocker), a depot counter reporting the EXISTING depot, a grower, rich treasury, cheap
// hulls. contractHulls seeds the delivery Current; warehouses/stockers seed the depot Current.
func newDepotHarness(ceiling, parks, contractHulls, warehouses, stockers int) (*RunContractScalerHandler, *fakePurchaser, *fakeDepotCounter, *fakeGrower) {
	treasury := &fakeTreasury{credits: 5_000_000, readable: true}
	fleet := &fakeCounter{n: contractHulls}
	pur := &fakePurchaser{treasury: treasury, counter: fleet}
	dc := &fakeDepotCounter{warehouses: warehouses, stockers: stockers}
	gr := &fakeGrower{counter: dc}
	roles, demand := manyParkRoles(parks)
	rr := &fakeRoleResolver{roles: roles, demand: demand}

	h := NewRunContractScalerHandler(nil)
	h.SetRoleResolver(rr)
	h.SetTreasuryReader(treasury)
	h.SetPriceReader(&fakePrice{price: 100_000, yard: "YARD", readable: true})
	h.SetFleetCounter(fleet)
	h.SetPurchaser(pur)
	h.SetCeilingReader(&fakeCeiling{value: ceiling})
	h.SetDepotElementCounter(dc)
	h.SetDepotGrower(gr)
	return h, pur, dc, gr
}

// RECONCILE, NOT DUPLICATE: the depot already has 5 warehouses + 1 stocker and 6 delivery hulls exist
// (at the knee); at ceiling 13 the ramp budgets 6 warehouses, so it adds ONLY the plan-short unit —
// exactly 1 warehouse (5→6), 0 stocker (1 already meets 1), 0 delivery — never a duplicate.
func TestReconcile_ReconcilesExistingDepotAddsOnlyTheShortWarehouse(t *testing.T) {
	h, pur, _, gr := newDepotHarness(13, 7, 6, 5, 1)

	bought := reconcile(t, h, 13)

	if len(pur.orders) != 0 {
		t.Fatalf("delivery buys = %d, want 0 (delivery target 6 already met)", len(pur.orders))
	}
	if len(gr.warehouseGrows) != 1 {
		t.Fatalf("warehouse grows = %d, want exactly 1 (reconcile 5→6 — add only the short one, no duplicate)", len(gr.warehouseGrows))
	}
	if len(gr.stockerGrows) != 0 {
		t.Fatalf("stocker grows = %d, want 0 (stocker 1 already meets target 1)", len(gr.stockerGrows))
	}
	if gr.warehouseGrows[0].Hub != "P00" {
		t.Fatalf("warehouse grow hub = %q, want the central hub P00 (top-demand park)", gr.warehouseGrows[0].Hub)
	}
	if len(pur.hullOrders) != 1 || bought != 1 {
		t.Fatalf("depot-hull buys = %d (bought %d), want 1 (the short warehouse, bought then grown)", len(pur.hullOrders), bought)
	}
}

// RAISING THE CEILING ACTUATES THE DEPOT. The cold-start default is a small delivery-only operation, so the
// depot bundle is what the operator's post-gate tune buys: at a ceiling of 10 the ramp fills 6 delivery to
// the knee and then puts the remaining 4 units into the depot (functional-first: anchor warehouse, stocker,
// depth). A ramp that stayed delivery-only above the knee would fail here.
func TestReconcile_RaisedCeilingActuatesDepotBeyondTheDeliveryKnee(t *testing.T) {
	const raised = 10
	h, pur, _, gr := newDepotHarness(raised, 7, 0, 0, 0)

	bought := reconcile(t, h, raised)

	if bought != raised {
		t.Fatalf("bought = %d, want %d — a raised ceiling fills to saturation, not a delivery-only crawl", bought, raised)
	}
	if len(pur.orders) != 6 {
		t.Fatalf("delivery buys = %d, want 6 (delivery fills to the knee first)", len(pur.orders))
	}
	if len(gr.warehouseGrows) == 0 && len(gr.stockerGrows) == 0 {
		t.Fatalf("depot grows = (%d,%d), want >0 — budget past the delivery knee must ACTUATE the depot", len(gr.warehouseGrows), len(gr.stockerGrows))
	}
}

// FILL ORDER — FUNCTIONAL DEPOT FIRST: a cold op (no hulls, empty depot) fills the fixed plan in one
// tick as delivery first (homed), THEN a functional mini-depot ASAP — the ANCHOR warehouse, then the
// STOCKER that fills it, THEN the remaining warehouse depth. The stocker is grown SECOND among the depot
// units, never last: an empty trailing warehouse is useless until a stocker deposits into it.
func TestReconcile_FillsDeliveryThenAnchorWarehouseThenStockerThenDepth(t *testing.T) {
	// 3 parks → plan 3 delivery + WarehouseUnits warehouse + StockerUnits stocker; ceiling covers it all.
	planSize := 3 + contractscaler.WarehouseUnits + contractscaler.StockerUnits
	h, pur, _, gr := newDepotHarness(planSize, 3, 0, 0, 0)

	bought := reconcile(t, h, planSize)

	if len(pur.orders) != 3 {
		t.Fatalf("delivery buys = %d, want 3 (delivery fills first)", len(pur.orders))
	}
	// The depot grows FUNCTIONAL-FIRST: warehouse #1 (the anchor), THEN the stocker, THEN the depth.
	if len(gr.growCalls) != contractscaler.WarehouseUnits+contractscaler.StockerUnits {
		t.Fatalf("depot grows = %d, want %d (the full warehouse+stocker bundle)", len(gr.growCalls), contractscaler.WarehouseUnits+contractscaler.StockerUnits)
	}
	if gr.growCalls[0] != contractscaler.Warehouse {
		t.Fatalf("first depot grow = %v, want Warehouse (the anchor the stocker deposits into)", gr.growCalls[0])
	}
	if gr.growCalls[1] != contractscaler.Stocker {
		t.Fatalf("second depot grow = %v, want Stocker — the stocker follows the FIRST warehouse (functional depot ASAP), not all of them", gr.growCalls[1])
	}
	for i := 2; i < len(gr.growCalls); i++ {
		if gr.growCalls[i] != contractscaler.Warehouse {
			t.Fatalf("depot grow %d = %v, want Warehouse (remaining depth behind the functional mini-depot)", i, gr.growCalls[i])
		}
	}
	// Counts unchanged: still the full warehouse bundle + the stocker (RoleTargets = D, W, S).
	if len(gr.warehouseGrows) != contractscaler.WarehouseUnits || len(gr.stockerGrows) != contractscaler.StockerUnits {
		t.Fatalf("depot bundle = (W%d,S%d), want (W%d,S%d) — the reorder preserves the counts", len(gr.warehouseGrows), len(gr.stockerGrows), contractscaler.WarehouseUnits, contractscaler.StockerUnits)
	}
	if bought != planSize {
		t.Fatalf("bought = %d, want %d (3 delivery + %d warehouse + %d stocker, all bought)", bought, planSize, contractscaler.WarehouseUnits, contractscaler.StockerUnits)
	}
}

// BUDGET PRIORITY — STOCKER BEFORE WAREHOUSE #2: raising the ceiling degree by degree past the delivery
// target, the FIRST depot degree grows the anchor warehouse, and the NEXT grows the STOCKER — never
// warehouse #2. The stocker takes the depot budget before the extra warehouse depth, so a functional
// mini-depot (1 warehouse + 1 stocker) forms before the depth deepens.
func TestReconcile_SecondDepotDegreeGrowsStockerNotWarehouseTwo(t *testing.T) {
	// First depot degree = delivery knee (6) + 1: the anchor warehouse forms (no stocker budget yet).
	h1, _, _, gr1 := newDepotHarness(7, 7, 6, 0, 0)
	reconcile(t, h1, 7)
	if len(gr1.warehouseGrows) != 1 || len(gr1.stockerGrows) != 0 {
		t.Fatalf("first depot degree grows = (W%d,S%d), want (1,0) — the anchor warehouse forms first", len(gr1.warehouseGrows), len(gr1.stockerGrows))
	}

	// Second depot degree (+1) with the anchor warehouse already present: the STOCKER forms next — NOT
	// warehouse #2. This is the fix: the stocker precedes the extra warehouse depth.
	h2, _, _, gr2 := newDepotHarness(8, 7, 6, 1, 0)
	reconcile(t, h2, 8)
	if len(gr2.stockerGrows) != 1 {
		t.Fatalf("second depot degree stocker grows = %d, want 1 (the stocker fills the anchor before the depth)", len(gr2.stockerGrows))
	}
	if len(gr2.warehouseGrows) != 0 {
		t.Fatalf("second depot degree warehouse grows = %d, want 0 — the stocker precedes warehouse #2", len(gr2.warehouseGrows))
	}
}

// ANCHOR-FIRST SAFETY: if the anchor warehouse cannot form (the grower fails every launch), the stocker
// — which deposits INTO a warehouse — must NOT be actuated. No orphan stocker with nowhere to deposit,
// even when the ceiling budgets one.
func TestReconcile_NoStockerWhenAnchorWarehouseFailsToForm(t *testing.T) {
	planSize := 7 + contractscaler.WarehouseUnits + contractscaler.StockerUnits // ceiling budgets the full depot
	h, _, _, gr := newDepotHarness(planSize, 7, 7, 0, 0)
	gr.err = errors.New("launch failed")

	reconcile(t, h, planSize)

	for _, role := range gr.growCalls {
		if role == contractscaler.Stocker {
			t.Fatalf("stocker actuated with no warehouse to deposit into (grow calls = %v) — the anchor-warehouse-first invariant is broken", gr.growCalls)
		}
	}
}

// REUSE BEFORE BUY, PER ROLE: a free idle undedicated cargo hull is RECLAIMED into the short warehouse
// slot (the grower is called with it) and NO hull is bought. The depot reuse uses FindReclaimable +
// grow — NOT the delivery Reclaim (which contract-dedicates+homes); the grower re-dedicates to warehouse.
func TestReconcile_ReclaimsIdleHullForWarehouseSlotBeforeBuying(t *testing.T) {
	// Ceiling 13: 6 delivery (at the knee), the depot has 5 of the 6-warehouse budget (stocker already
	// at its target of 1), so exactly one short warehouse slot lands here.
	h, pur, _, gr := newDepotHarness(13, 7, 6, 5, 1)
	rec := &fakeReclaimer{available: []string{"HULL-IDLE"}}
	h.SetIdleHullReclaimer(rec)

	reconcile(t, h, 13)

	if len(gr.warehouseGrows) != 1 || gr.warehouseGrows[0].ShipSymbol != "HULL-IDLE" {
		t.Fatalf("warehouse grows = %+v, want 1 carrying the reclaimed HULL-IDLE (reuse before buy)", gr.warehouseGrows)
	}
	if len(pur.hullOrders) != 0 {
		t.Fatalf("depot-hull buys = %d, want 0 — a reclaimed hull replaces the buy", len(pur.hullOrders))
	}
	if len(rec.reclaimed) != 0 {
		t.Fatalf("reclaimer.Reclaim calls = %d, want 0 — a depot hull is GROWN (grower re-dedicates), never contract-homed", len(rec.reclaimed))
	}
}

// HOME-SCOPED REUSE EXTENDS TO THE WAREHOUSE (sp-fis8y, generalizing sp-fihvy's stocker-only scoping):
// once a DepotHullReclaimer is wired, the WAREHOUSE slot's reuse tier consults it too — NOT the
// fleet-wide IdleHullReclaimer — even though the fleet-wide reclaimer has a hull queued. This is what
// stops a stranded-but-idle-undedicated hull (e.g. the live TORWIND-19, evicted by
// launchDepotWarehouse's new home-reachability precondition) from being re-offered to the warehouse
// grow on the very next tick: the home-scoped reclaimer reports none available (it already excludes
// non-home-reachable hulls), so the ramp correctly falls through to a buy instead of looping the same
// unreachable hull back in.
func TestReconcile_WarehouseSlotUsesHomeScopedReclaimerWhenWired(t *testing.T) {
	// Ceiling 13: 6 delivery (at the knee), the depot has 5 of the 6-warehouse budget (stocker already at
	// its target of 1), so exactly one short warehouse slot lands here — same shape as the fleet-wide
	// reuse test above.
	h, pur, _, gr := newDepotHarness(13, 7, 6, 5, 1)
	fleetWide := &fakeReclaimer{available: []string{"STRANDED-FLEET-WIDE"}}
	homeScoped := &fakeDepotReclaimer{} // no home-reachable candidate available
	h.SetIdleHullReclaimer(fleetWide)
	h.SetDepotHullReclaimer(homeScoped)

	bought := reconcile(t, h, 13)

	if len(homeScoped.calls) != 1 || homeScoped.calls[0] != "P00" {
		t.Fatalf("home-scoped reclaimer calls = %v, want exactly 1 call scoped to the central hub's home system P00", homeScoped.calls)
	}
	if fleetWide.findCalls != 0 {
		t.Fatalf("fleet-wide reclaimer FindReclaimable calls = %d, want 0 — the warehouse role must consult the home-scoped tier exclusively once wired", fleetWide.findCalls)
	}
	if len(gr.warehouseGrows) != 1 || gr.warehouseGrows[0].ShipSymbol != "DEPOT-HULL" {
		t.Fatalf("warehouse grows = %+v, want exactly 1 carrying the BOUGHT DEPOT-HULL — never the fleet-wide STRANDED-FLEET-WIDE hull once a home-scoped reclaimer is wired", gr.warehouseGrows)
	}
	if len(pur.hullOrders) != 1 || bought != 1 {
		t.Fatalf("depot-hull buys = %d (bought %d), want 1 — no home-reachable reuse candidate, so the ramp falls through to a buy", len(pur.hullOrders), bought)
	}
}

// THE 200k CUSHION GATES DEPOT BUYS TOO: with the treasury unable to leave 200000 after a warehouse buy,
// the depot buy is HELD (no grow, no buy) — the sole money guard is not weakened for the depot roles.
func TestReconcile_WarehouseBuyHeldByCushion(t *testing.T) {
	// Ceiling 13: 6 delivery (at the knee), the depot has 5 of the 6-warehouse budget (stocker already at
	// its target of 1), so the cushion holds the one short warehouse slot.
	h, pur, _, gr := newDepotHarness(13, 7, 6, 5, 1)
	tr := &fakeTreasury{credits: 250_000, readable: true} // 250k - 100k = 150k < 200000 cushion
	h.SetTreasuryReader(tr)
	pur.treasury = tr

	bought := reconcile(t, h, 13)

	if bought != 0 || len(pur.hullOrders) != 0 {
		t.Fatalf("bought %d (depot-hull buys %d), want 0 — 250k-100k=150k is below the 200000 cushion", bought, len(pur.hullOrders))
	}
	if len(gr.warehouseGrows) != 0 {
		t.Fatalf("warehouse grows = %d, want 0 — the cushion holds the depot buy", len(gr.warehouseGrows))
	}
}

// --- COMPOSITION AT THE KNEE (sp-mtgje): delivery caps at 6, warehouse deepens to fill the ceiling ---

// COLD BUILD at ceiling 14 (≥8 central parks, empty depot) composes the NEW curve — 6 delivery + 7
// warehouse + 1 stocker (delivery capped at the knee; warehouse = ceiling − 6 − 1). NOT the old 8+5+1.
func TestReconcile_CeilingFourteenComposesSixDeliverySevenWarehouseOneStocker(t *testing.T) {
	h, pur, _, gr := newDepotHarness(14, 8, 0, 0, 0)

	bought := reconcile(t, h, 14)

	if len(pur.orders) != 6 {
		t.Fatalf("delivery buys = %d, want 6 (capped at the knee, NEVER 8)", len(pur.orders))
	}
	if len(gr.warehouseGrows) != 7 {
		t.Fatalf("warehouse grows = %d, want 7 (ceiling 14 − 6 delivery − 1 stocker; warehouse deepens to fill)", len(gr.warehouseGrows))
	}
	if len(gr.stockerGrows) != 1 {
		t.Fatalf("stocker grows = %d, want 1", len(gr.stockerGrows))
	}
	if bought != 14 {
		t.Fatalf("bought = %d, want 14 (6 delivery + 7 warehouse + 1 stocker)", bought)
	}
}

// RE-ROLE THE SURPLUS at ceiling 14: the live 8 delivery + 5 warehouse + 1 stocker re-composes to 6
// delivery + 7 warehouse + 1 stocker with ZERO new hulls — the 2 surplus delivery hulls are RELEASED
// (un-dedicated) and RECLAIMED into the warehouse deficit, never left idle, never bought new.
func TestReconcile_CeilingFourteenReRolesSurplusDeliveryIntoWarehouse(t *testing.T) {
	h, pur, _, gr := newDepotHarness(14, 8, 8, 5, 1) // live: 8 delivery, 5 warehouse, 1 stocker
	depotRec := &fakeDepotReclaimer{}
	rel := &fakeReleaser{counter: pur.counter, reclaimer: depotRec}
	h.SetDepotHullReclaimer(depotRec)
	h.SetDeliverySurplusReleaser(rel)

	bought := reconcile(t, h, 14)

	if len(rel.calls) != 1 || rel.calls[0] != 2 {
		t.Fatalf("release calls = %v, want a single release of the 2 surplus delivery hulls (8→6 knee)", rel.calls)
	}
	if len(pur.orders) != 0 {
		t.Fatalf("delivery buys = %d, want 0 (re-role, never re-buy)", len(pur.orders))
	}
	if len(pur.hullOrders) != 0 {
		t.Fatalf("depot-hull buys = %d, want 0 — the released hulls fill the warehouse deficit, none bought", len(pur.hullOrders))
	}
	if len(gr.warehouseGrows) != 2 {
		t.Fatalf("warehouse grows = %d, want 2 (5→7 from the 2 re-roled delivery hulls, reclaimed not bought)", len(gr.warehouseGrows))
	}
	if bought != 0 {
		t.Fatalf("bought = %d, want 0 — a pure re-composition of the live fleet spends nothing", bought)
	}
}

// RE-ROLE IS BOUNDED BY THE WAREHOUSE DEFICIT: with no warehouse room (ceiling leaves no deficit), a
// surplus delivery hull is NOT released — leaving it over-target is safe; stranding it idle is not.
func TestReconcile_SurplusDeliveryNotReleasedWithoutWarehouseDeficit(t *testing.T) {
	// Ceiling 7: takeDelivery=6, remaining=1 → anchor warehouse only (takeWarehouse=1). Depot already has
	// 1 warehouse, so deficit 0. 7 live delivery hulls (surplus 1) but nowhere to re-role → no release.
	h, _, _, gr := newDepotHarness(7, 8, 7, 1, 1)
	depotRec := &fakeDepotReclaimer{}
	rel := &fakeReleaser{counter: &fakeCounter{n: 7}, reclaimer: depotRec}
	h.SetDepotHullReclaimer(depotRec)
	h.SetDeliverySurplusReleaser(rel)

	reconcile(t, h, 7)

	if len(rel.calls) != 0 {
		t.Fatalf("release calls = %v, want none — no warehouse deficit to absorb the surplus (never strand a released hull idle)", rel.calls)
	}
	if len(gr.warehouseGrows) != 0 {
		t.Fatalf("warehouse grows = %d, want 0 (anchor already present, no deficit)", len(gr.warehouseGrows))
	}
}

// --- The era-5 cold-start retune: a SMALLER contract operation that reaches the gate sooner ---

// THE ADMIRAL'S ERA-5 CEILING. The shipped default sizes the whole contract operation at three hulls, so
// bootstrap's GATE-entry bar (the full fleet against the scaler's achievable target) is reached early. This
// pins the operational number; the operator raises it live when the gate is behind them.
func TestContractScaler_DefaultCeilingIsThree(t *testing.T) {
	if DefaultContractFleetMaxHulls != 3 {
		t.Fatalf("DefaultContractFleetMaxHulls = %d, want 3 (the era-5 cold-start contract operation)", DefaultContractFleetMaxHulls)
	}
	if got := ContractScalerTunableDefaults()[ceilingKey]; got != 3 {
		t.Fatalf("the tune registry's documented default for %s = %d, want 3", ceilingKey, got)
	}
}

// THE SEAM IS UNCHANGED — only the default moved. contract_fleet_max_hulls is still read LIVE from the
// container's own config every tick, so an operator raising it mid-era still lifts the ceiling above the
// default, and clearing it still falls back to the default. A retune that hardcoded the ceiling would fail.
func TestContractScaler_CeilingStaysLiveTunableAboveTheDefault(t *testing.T) {
	h := NewRunContractScalerHandler(nil)
	cmd := &RunContractScalerCommand{ContainerID: "c1", PlayerID: 1}

	for _, tuned := range []int{1, 6, 10, 16} {
		h.SetCeilingReader(&fakeCeiling{value: tuned})
		if got := h.liveCeiling(context.Background(), cmd); got != tuned {
			t.Fatalf("a live tune to %d must take effect, got ceiling %d", tuned, got)
		}
	}

	// Unset (0) reverts to the documented default rather than stalling the ramp at zero.
	h.SetCeilingReader(&fakeCeiling{value: 0})
	if got := h.liveCeiling(context.Background(), cmd); got != DefaultContractFleetMaxHulls {
		t.Fatalf("an unset ceiling must revert to the default %d, got %d", DefaultContractFleetMaxHulls, got)
	}
}

// At the shipped default the ramp fills DELIVERY hulls only — three central parks, no depot bundle. The
// fill order is structurally delivery-first, so a three-hull budget never strands capital in a warehouse
// that has no delivery fleet to serve.
func TestReconcile_DefaultCeilingBuysThreeDeliveryHullsAndNoDepot(t *testing.T) {
	h, pur, _, gr := newDepotHarness(DefaultContractFleetMaxHulls, 7, 0, 0, 0)

	bought := reconcile(t, h, DefaultContractFleetMaxHulls)

	if bought != DefaultContractFleetMaxHulls {
		t.Fatalf("bought = %d, want %d — the default ceiling fills its whole budget", bought, DefaultContractFleetMaxHulls)
	}
	if len(pur.orders) != DefaultContractFleetMaxHulls {
		t.Fatalf("delivery buys = %d, want %d (delivery fills first and the budget stops there)", len(pur.orders), DefaultContractFleetMaxHulls)
	}
	if len(gr.warehouseGrows) != 0 || len(gr.stockerGrows) != 0 {
		t.Fatalf("depot grows = (%d,%d), want (0,0) — a three-hull budget is spent entirely on delivery",
			len(gr.warehouseGrows), len(gr.stockerGrows))
	}
}
