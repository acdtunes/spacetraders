package commands

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	mfgServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The ROLE-AWARE DRAIN. Two properties are load-bearing here and both fail SILENTLY:
//
//  1. CLAIM IDENTITY. ClaimShip authorizes a NEW claim only when the hull's dedicated_fleet
//     EQUALS the operation string (adapters/api/ship_repository_claims.go). A role-tagged hull
//     claimed under the drain's DEFAULT identity is rejected at the DB, so the hull is
//     discovered, paired with a lot, dispatched — and then never works. That defect ships green.
//
//  2. DISCOVERY. FindIdleLightHaulers drops every dedicated hull by design, so a role tag that
//     discovery never queries is a hull that is invisible to the drain forever. Task 7 tags hulls
//     at purchase; until this task lands, those hulls are in NEITHER pool.
//
// Everything is driven through the drain's own seams — drainOnce, selectHaulers, supplyTask,
// deliverGateLeg — never through the gate policy objects, which have their own package tests.

const (
	gateTestSystem     = "X1-GT"
	gateTestWaypoint   = "X1-GT-A1"
	gateTestSite       = "X1-GT-GATE"
	gateTestPipelineID = "gate-pipeline-1"

	// The two gate materials, with DIFFERENT outstanding bills so PlanFill's
	// remaining-descending order is deterministic and FAB_MATS (the lot's own material) is
	// planned first.
	gateMaterialPrimary   = "FAB_MATS"
	gateMaterialSecondary = "ADVANCED_CIRCUITRY"

	gateTestHoldCapacity = 80
	gateTestTradeVolume  = 20
)

// ---------------------------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------------------------

// gateTestHull builds an idle, in-system, cargo-capable hauler carrying the given
// dedicated_fleet tag ("" = undedicated/opportunistic). Mirrors newTestHaulerInFleet but places
// the hull in the GATE system with the delivery fleet's larger hold.
func gateTestHull(t *testing.T, symbol, fleet string) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(gateTestHoldCapacity, 0, nil)
	if err != nil {
		t.Fatalf("failed to build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("failed to build fuel: %v", err)
	}
	waypoint, err := shared.NewWaypoint(gateTestWaypoint, 0, 0)
	if err != nil {
		t.Fatalf("failed to build waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), waypoint, fuel, 100, gateTestHoldCapacity, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("failed to build ship: %v", err)
	}
	ship.SetDedicatedFleet(fleet)
	return ship
}

// gateTestLadenHull is a delivery hull that ARRIVES CARRYING units of good — the state left by a
// delivery that errored after a successful buy, by a task deadline, or by a restart mid-leg. At
// units == capacity it has NO free hold, which is the wedge case.
func gateTestLadenHull(t *testing.T, symbol, good string, units int) *navigation.Ship {
	t.Helper()
	ship := gateTestHull(t, symbol, gate.DeliveryFleetTag)
	item, err := shared.NewCargoItem(good, good, "", units)
	if err != nil {
		t.Fatalf("failed to build cargo item: %v", err)
	}
	cargo, err := shared.NewCargo(gateTestHoldCapacity, units, []*shared.CargoItem{item})
	if err != nil {
		t.Fatalf("failed to build laden cargo: %v", err)
	}
	ship.SetCargo(cargo)
	return ship
}

func gateTestCmd() *RunConstructionCoordinatorCommand {
	return &RunConstructionCoordinatorCommand{PlayerID: 1, SystemSymbol: gateTestSystem, ContainerID: "C-1"}
}

// freshAggregatePipelineRepo models the PRODUCTION repository: every FindByID builds a NEW
// aggregate from the persisted counters and hands that back, so a pre-delivery read and a
// post-delivery read are DIFFERENT objects.
//
// This matters. drainStubPipelineRepo returns the same pointer to every caller, which makes a stale
// snapshot and a fresh read indistinguishable and silently hides every defect in this class — a
// wrong-pipeline bug would be green in the suite and wrong in production. The gate leg hands its
// pipeline to completeSupply, which sizes replenishment off it, so that distinction is load-bearing.
//
// The rebuilt aggregate's own ID differs per read; nothing on this path reads it (recordDelivery,
// enqueueReplenishmentIfNeeded and nextConstructionDeliveryTask all key off task.PipelineID()), so
// FindByID answers for whatever id it is asked.
type freshAggregatePipelineRepo struct {
	manufacturing.PipelineRepository
	mu          sync.Mutex
	order       []string
	target      map[string]int
	delivered   map[string]int
	buyFloor    string
	resumeFloor string
	// goodOverrides is the per-good override map the row carries, stamped onto every rebuilt
	// aggregate. It is set on the ROW rather than on a returned pointer because each FindByID
	// hands back a NEW aggregate: a test that mutated one read's pipeline would be describing a
	// value the next read discards, which is exactly the staleness this repo models away.
	goodOverrides manufacturing.GoodGatingOverrides
}

func newFreshAggregatePipelineRepo() *freshAggregatePipelineRepo {
	return &freshAggregatePipelineRepo{
		order:     []string{gateMaterialPrimary, gateMaterialSecondary},
		target:    map[string]int{gateMaterialPrimary: 400, gateMaterialSecondary: 200},
		delivered: map[string]int{},
	}
}

func (r *freshAggregatePipelineRepo) FindByID(_ context.Context, _ string) (*manufacturing.ManufacturingPipeline, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pipeline := manufacturing.NewConstructionPipeline(gateTestSite, 1, 1, 5)
	materials := make([]*manufacturing.ConstructionMaterialTarget, 0, len(r.order))
	for _, good := range r.order {
		materials = append(materials, manufacturing.ReconstructConstructionMaterialTarget(good, r.target[good], r.delivered[good]))
	}
	pipeline.SetMaterials(materials)
	pipeline.SetDeliveryFloors(r.buyFloor, r.resumeFloor)
	pipeline.SetGoodOverrides(r.goodOverrides)
	return pipeline, nil
}

// Update persists the aggregate's counters back, as the real repository does.
func (r *freshAggregatePipelineRepo) Update(_ context.Context, pipeline *manufacturing.ManufacturingPipeline) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, material := range pipeline.Materials() {
		r.delivered[material.TradeSymbol()] = material.DeliveredQuantity()
	}
	return nil
}

func (r *freshAggregatePipelineRepo) deliveredUnits(good string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.delivered[good]
}

func (r *freshAggregatePipelineRepo) setFloors(buyFloor, resumeFloor string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buyFloor, r.resumeFloor = buyFloor, resumeFloor
}

func (r *freshAggregatePipelineRepo) setBill(good string, target int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.target[good] = target
}

// setDelivered pins what the SERVER has already accepted for good, so a test can stage the exact
// mid-build state the sp-v2a2h ledger recorded (364 of 400 fulfilled, 36 outstanding).
func (r *freshAggregatePipelineRepo) setDelivered(good string, delivered int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delivered[good] = delivered
}

// gateTestLot is a lot whose task is ALREADY EXECUTING — the state claimTaskForSupply leaves
// behind before supplyTask routes to the leg. Tests that call deliverGateLeg directly must start
// from that state, or the completion path's Complete/ParkForResupply transitions are illegal and
// the test would be asserting against a state production never reaches.
func gateTestLot(t *testing.T, ship *navigation.Ship) constructionLot {
	t.Helper()
	lot := gateReadyLot(t, ship)
	if err := lot.task.AssignShip(ship.ShipSymbol()); err != nil {
		t.Fatalf("AssignShip: %v", err)
	}
	if err := lot.task.StartExecution(); err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	return lot
}

// gateReadyLot is a lot whose task is READY — the state supplyTask itself expects.
func gateReadyLot(t *testing.T, ship *navigation.Ship) constructionLot {
	t.Helper()
	task := manufacturing.NewDeliverToConstructionTask(gateTestPipelineID, 1, gateMaterialPrimary, "", "", gateTestSite, nil)
	if err := task.MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	return constructionLot{task: task, ship: ship, claimIdentity: gate.DeliveryFleetTag}
}

// stubGateTopology answers TerminalFactory per good: a market quote, or a refusal. It never
// substitutes a waypoint — that is the production contract this stub must not soften.
type stubGateTopology struct {
	mu           sync.Mutex
	supply       string
	supplyByGood map[string]string
	errByGood    map[string]error
	tradeVolume  int
}

func (s *stubGateTopology) TerminalFactory(_ context.Context, good, _ string, _ int) (*mfgServices.MarketLocatorResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err, ok := s.errByGood[good]; ok {
		return nil, err
	}
	supply := s.supply
	if level, ok := s.supplyByGood[good]; ok {
		supply = level
	}
	volume := s.tradeVolume
	if volume == 0 {
		volume = gateTestTradeVolume
	}
	return &mfgServices.MarketLocatorResult{
		WaypointSymbol: good + "-EXPORTER",
		Supply:         supply,
		Price:          100,
		TradeVolume:    volume,
	}, nil
}

// countingGateBuyer records every pinned terminal-factory buy. It reports acquiring exactly the
// units the trip allocated, so a fill that is planned but never spent is visible as a zero.
type countingGateBuyer struct {
	mu     sync.Mutex
	bought map[string]int
	// attempts counts BuyAtTerminalFactory CALLS per good, recorded before any outcome. It is the
	// only way to assert "no purchase was even attempted" — sp-v2a2h acceptance 2 asks for the API
	// call count, not the spend, because a fill the guards happen to refuse and a fill that was
	// never planned are the same zero in `bought` and completely different bugs.
	attempts map[string]int
	seen     int
	err      error
	// sinks records the TrancheSink each call declared, so a test can assert the delivery fleet
	// buys as a CONSTRUCTION sink rather than inheriting the factory fleet's saturation floor
	// (sp-lpy9i). Recorded per call rather than as a single value: a leg makes one buy per stop,
	// and every one of them must declare the same sink.
	sinks []mfgServices.TrancheSink
	// acquireZero models the money/price guards stopping the fill: the buy is ATTEMPTED and returns
	// a zero-quantity result, which is the shape fillFromSource produces when spendFloorBreached or
	// the price ceiling trips. Deliberately distinct from err, which is a failed CALL — a caller
	// that conflates the two cannot honour a refusal differently from an outage.
	acquireZero bool
	// zeroReason scripts the ZERO-quantity result's ZeroReason (sp-0u1yd), meaningful only when
	// acquireZero is set. The default (zero value, ZeroReasonUnspecified) models a dry/no-eligible
	// source — the shape every acquireZero test written before sp-0u1yd already expects.
	// ZeroReasonCapitalDeclined models the working-capital reserve refusing the spend on an
	// otherwise-fine source, which the drain must route differently: keep the source, skip the
	// deferred-resourcing dance.
	zeroReason mfgServices.AcquisitionZeroReason
	// spendHeadroom is the largest projected cost SpendFloorWouldBreach will admit — treasury minus
	// reserve, expressed directly so a test states the headroom instead of staging a balance.
	//
	// ZERO MEANS PERMISSIVE, not broke. That is the real probe's fail-OPEN direction when no
	// treasury source is wired (the optional-port contract these fixtures rely on), and it is what
	// keeps every test written before the precheck existed behaving exactly as it did.
	spendHeadroom int
	// probes records every projected cost the planner asked about, so a test can prove the precheck
	// ran AT ALL rather than inferring it from an outcome a broken leg also produces.
	probes []int
}

// gateTestReserve is the floor countingGateBuyer reports alongside a refusal. Its only job is to be
// a recognisable non-zero number in the decline's log line.
const gateTestReserve = 100_000

// SpendFloorWouldBreach is the fake's pre-dispatch floor test: it refuses anything past
// spendHeadroom. It spends nothing and records nothing but the question, mirroring the real probe,
// which is a pure read of treasury against the reserve.
func (b *countingGateBuyer) SpendFloorWouldBreach(_ context.Context, _ int, projectedCost int) (bool, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probes = append(b.probes, projectedCost)
	if b.spendHeadroom <= 0 {
		return false, gateTestReserve
	}
	return projectedCost > b.spendHeadroom, gateTestReserve
}

// probed returns every projected cost the planner priced this run.
func (b *countingGateBuyer) probed() []int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]int(nil), b.probes...)
}

// resultFor is the acquisition this buyer reports for a requested lot.
func (b *countingGateBuyer) resultFor(units int) *mfgServices.ProductionResult {
	if b.acquireZero {
		return &mfgServices.ProductionResult{QuantityAcquired: 0, ZeroReason: b.zeroReason}
	}
	return &mfgServices.ProductionResult{QuantityAcquired: units}
}

func (b *countingGateBuyer) BuyAtTerminalFactory(_ context.Context, _ *navigation.Ship, good string, source *mfgServices.MarketLocatorResult, units int, _ string, _ int, _ *shared.OperationContext, sink mfgServices.TrancheSink) (*mfgServices.ProductionResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sinks = append(b.sinks, sink)
	b.seen++
	if b.attempts == nil {
		b.attempts = make(map[string]int)
	}
	b.attempts[good]++
	if b.err != nil {
		return nil, b.err
	}
	if source == nil || source.WaypointSymbol == "" {
		return nil, errors.New("buy attempted without a pinned terminal factory")
	}
	if b.bought == nil {
		b.bought = make(map[string]int)
	}
	// goods() means what was ACQUIRED; calls() means what was ATTEMPTED. Keeping them separate is
	// what lets a test tell "the guards refused the fill" from "the leg never tried to buy".
	result := b.resultFor(units)
	if result.QuantityAcquired > 0 {
		b.bought[good] += result.QuantityAcquired
	}
	return result, nil
}

func (b *countingGateBuyer) calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seen
}

func (b *countingGateBuyer) goods() map[string]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[string]int, len(b.bought))
	for good, units := range b.bought {
		out[good] = units
	}
	return out
}

// declaredSinks is every TrancheSink this leg's buys declared, in call order (sp-lpy9i).
func (b *countingGateBuyer) declaredSinks() []mfgServices.TrancheSink {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]mfgServices.TrancheSink(nil), b.sinks...)
}

// attemptsFor is how many times the leg CALLED the buyer for good, whatever the outcome.
func (b *countingGateBuyer) attemptsFor(good string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.attempts[good]
}

func (b *countingGateBuyer) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seen = 0
	b.bought = nil
	b.attempts = nil
	b.probes = nil
}

// gateLegFixture is one wired delivery-fleet drain plus every seam a test needs to assert on.
//
// It returns a struct rather than the plan's 4-tuple because the amendment's completion tests
// must reach the TASK REPO (a leg that skips completeSupply persists no status and enqueues no
// replenishment — the silent stall), and the log stream lives on the CONTEXT, not the handler.
type gateLegFixture struct {
	handler  *RunConstructionCoordinatorHandler
	pipeline *freshAggregatePipelineRepo
	taskRepo *drainStubTaskRepo
	shipRepo *drainFakeShipRepo
	producer *fakeConstructionProducer
	topo     *stubGateTopology
	buyer    *countingGateBuyer
	logger   *capturingLogger
}

func newGateDeliveryHandler(t *testing.T) *gateLegFixture {
	t.Helper()
	taskRepo := &drainStubTaskRepo{}
	pipelineRepo := newFreshAggregatePipelineRepo()
	shipRepo := newDrainShipRepo()
	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	topo := &stubGateTopology{supply: "MODERATE"}
	buyer := &countingGateBuyer{}

	handler := NewRunConstructionCoordinatorHandler(
		taskRepo, pipelineRepo, shipRepo, producer,
		staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{},
	)
	handler.SetGateDelivery(topo, buyer)

	return &gateLegFixture{
		handler: handler, pipeline: pipelineRepo, taskRepo: taskRepo, shipRepo: shipRepo,
		producer: producer, topo: topo, buyer: buyer, logger: &capturingLogger{},
	}
}

// resyncedHulls is the hulls the leg forced back to server truth after a phantom-cargo 4219.
func (f *gateLegFixture) resyncedHulls() []string {
	f.shipRepo.mu.Lock()
	defer f.shipRepo.mu.Unlock()
	return append([]string(nil), f.shipRepo.resyncs...)
}

// createdTasks is how many replenishment tasks the leg enqueued.
func (f *gateLegFixture) createdTasks() int {
	f.taskRepo.mu.Lock()
	defer f.taskRepo.mu.Unlock()
	return len(f.taskRepo.created)
}

// ctx carries the capturing logger, so every decision the leg records reaches the assertions the
// same way it reaches an operator's container log.
func (f *gateLegFixture) ctx() context.Context {
	return common.WithLogger(context.Background(), f.logger)
}

// logLines is this fixture's log stream, rendered the one way every assertion in the package
// renders it (capturingLogger.joined).
func (f *gateLegFixture) logLines() string { return f.logger.joined() }

// deliveries is the goods this leg actually unloaded at the construction site, read off the
// producer terminal both the flush and the buy loop go through.
func (f *gateLegFixture) deliveries() []string {
	f.producer.mu.Lock()
	defer f.producer.mu.Unlock()
	out := make([]string, 0, len(f.producer.deliverCalls))
	for _, call := range f.producer.deliverCalls {
		out = append(out, call.good)
	}
	return out
}

func (f *gateLegFixture) deliveredGood(good string) bool {
	for _, delivered := range f.deliveries() {
		if delivered == good {
			return true
		}
	}
	return false
}

// run drives ONE delivery leg with a freshly staged lot, as a tick would.
func (f *gateLegFixture) run(t *testing.T, hull string) bool {
	t.Helper()
	ship := gateTestHull(t, hull, gate.DeliveryFleetTag)
	return f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, gateTestLot(t, ship), shared.MustNewPlayerID(1))
}

// ---------------------------------------------------------------------------------------------
// CLAIM IDENTITY — the defect that ships green and does nothing
// ---------------------------------------------------------------------------------------------

// ClaimShip authorizes a NEW claim only when the hull's tag EQUALS the operation, so a
// role-tagged hull MUST be claimed under its own tag or it is rejected at the DB and silently
// never works.
func TestClaimIdentityFor_ARoleTaggedHullClaimsUnderItsOwnTag(t *testing.T) {
	h := &RunConstructionCoordinatorHandler{}
	cmd := &RunConstructionCoordinatorCommand{}

	for _, tag := range []string{gate.DeliveryFleetTag, gate.FactoryFleetTag, gate.LegacyFleetTag} {
		got := h.claimIdentityFor(cmd, gateTestHull(t, "GATE-7", tag))
		if got != tag {
			t.Fatalf("claimIdentityFor(tag %q) = %q; ClaimShip authorizes only when tag == operation, so a mismatch means the hull is rejected at the DB and never works", tag, got)
		}
	}
}

// An UNDEDICATED hull is opportunistic capacity: it claims under the drain's own identity,
// exactly as before.
func TestClaimIdentityFor_AnUndedicatedHullClaimsUnderTheDrainIdentity(t *testing.T) {
	h := &RunConstructionCoordinatorHandler{}
	cmd := &RunConstructionCoordinatorCommand{}

	if got := h.claimIdentityFor(cmd, gateTestHull(t, "FREE-7", "")); got != operationManufacturing {
		t.Fatalf("claimIdentityFor(undedicated) = %q, want the drain identity %q", got, operationManufacturing)
	}
}

// THE ALLOWLIST IS THE POINT. Claiming under "whatever tag the hull carries" would let the drain
// claim a CONTRACT or TRADE hull under that fleet's identity and pass the no-poach guard —
// defeating it entirely. Only GATE tags are honoured.
func TestClaimIdentityFor_AForeignPinnedHullIsNeverClaimedUnderItsOwnTag(t *testing.T) {
	h := &RunConstructionCoordinatorHandler{}
	cmd := &RunConstructionCoordinatorCommand{}

	for _, tag := range []string{"contract", "trade", "purchasing", "warehouse"} {
		got := h.claimIdentityFor(cmd, gateTestHull(t, "FOREIGN-7", tag))
		if got == tag {
			t.Fatalf("claimIdentityFor(foreign tag %q) = %q — claiming under a foreign fleet's identity defeats the no-poach guard", tag, got)
		}
		if got != operationManufacturing {
			t.Fatalf("claimIdentityFor(foreign tag %q) = %q, want the drain identity so ClaimShip REJECTS it", tag, got)
		}
	}
}

// A per-launch DedicatedFleet override still wins for undedicated hulls, so the existing
// per-launch pinning is unaffected.
func TestClaimIdentityFor_HonoursThePerLaunchDrainIdentity(t *testing.T) {
	h := &RunConstructionCoordinatorHandler{}
	cmd := &RunConstructionCoordinatorCommand{DedicatedFleet: "gate-alt"}

	if got := h.claimIdentityFor(cmd, gateTestHull(t, "FREE-7", "")); got != "gate-alt" {
		t.Fatalf("claimIdentityFor = %q, want the per-launch identity %q", got, "gate-alt")
	}
}

// END TO END, THROUGH THE DRAIN: a gate-delivery hull is DISCOVERED (it is in neither pool
// without the widened lookup) and CLAIMED UNDER ITS OWN TAG. The fake ship repo mirrors the real
// repository's atomic dedication guard, so a claim under the drain's default identity is rejected
// there exactly as it would be at the DB — which is what makes this test the one that catches the
// defect that ships green.
func TestConstructionDrain_DiscoversAndClaimsARoleTaggedHullUnderItsOwnTag(t *testing.T) {
	pipeline := newDrainPipeline(t, gateMaterialPrimary, 40)
	task := readyConstructionTask(t, pipeline, gateMaterialPrimary)

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}

	delivery := newTestHaulerInFleet(t, "GATE-7", gate.DeliveryFleetTag)
	shipRepo := newDrainShipRepo(delivery)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	shipRepo.mu.Lock()
	claims := append([]drainClaim(nil), shipRepo.claims...)
	shipRepo.mu.Unlock()

	if len(claims) != 1 {
		t.Fatalf("expected the gate-delivery hull discovered and claimed, got %d claim(s) — a role-tagged hull is in NEITHER discovery pool until its own tag is queried", len(claims))
	}
	if claims[0].symbol != "GATE-7" || claims[0].operation != gate.DeliveryFleetTag {
		t.Fatalf("claim %+v: a gate-delivery hull must be claimed under %q; under any other identity ClaimShip rejects it and the hull silently never works", claims[0], gate.DeliveryFleetTag)
	}
}

// ---------------------------------------------------------------------------------------------
// DISCOVERY — every gate pool, deduplicated
// ---------------------------------------------------------------------------------------------

// Discovery must reach EVERY gate tag. FindIdleLightHaulers drops every dedicated hull by design,
// so a role-tagged hull that no lookup names appears in NO pool and is invisible to the drain
// forever — which is exactly what task 7's purchase-time tagging created.
func TestSelectHaulers_DiscoversEveryGateFleetTag(t *testing.T) {
	shipRepo := newDrainShipRepo(
		gateTestHull(t, "GATE-7", gate.DeliveryFleetTag),
		gateTestHull(t, "GATE-8", gate.FactoryFleetTag),
		gateTestHull(t, "MFG-9", gate.LegacyFleetTag),
	)
	h := &RunConstructionCoordinatorHandler{shipRepo: shipRepo}

	ships, err := h.selectHaulers(context.Background(), gateTestCmd(), shared.MustNewPlayerID(1), gateTestSystem)
	if err != nil {
		t.Fatalf("selectHaulers returned error: %v", err)
	}

	found := make(map[string]int, len(ships))
	for _, ship := range ships {
		found[ship.ShipSymbol()]++
	}
	for _, symbol := range []string{"GATE-7", "GATE-8", "MFG-9"} {
		if found[symbol] == 0 {
			t.Fatalf("selectHaulers never surfaced %s (found %v); its pool was not queried, so the hull is invisible to the drain", symbol, found)
		}
	}
	if len(ships) != 3 {
		t.Fatalf("selectHaulers returned %d hull(s) %v, want all 3 gate-tagged hulls", len(ships), found)
	}
}

// ONE HULL, ONE LOT. A per-launch identity that happens to equal a role tag must not make that
// pool be scanned twice — a duplicated hull would be paired with two lots and dispatched twice.
func TestSelectHaulers_NeverReturnsAHullTwiceWhenTheLaunchIdentityIsARoleTag(t *testing.T) {
	shipRepo := newDrainShipRepo(gateTestHull(t, "GATE-7", gate.DeliveryFleetTag))
	h := &RunConstructionCoordinatorHandler{shipRepo: shipRepo}

	cmd := gateTestCmd()
	cmd.DedicatedFleet = gate.DeliveryFleetTag

	ships, err := h.selectHaulers(context.Background(), cmd, shared.MustNewPlayerID(1), gateTestSystem)
	if err != nil {
		t.Fatalf("selectHaulers returned error: %v", err)
	}
	if len(ships) != 1 {
		t.Fatalf("selectHaulers returned %d entries for ONE hull — a duplicate is dispatched to two lots", len(ships))
	}
}

// EXCLUSIVE MODE must seal against the WHOLE gate workforce. Membership checked only against the
// drain's default identity would read "no dedicated fleet" while gate-delivery hulls exist, and
// the sealed drain would fall through and draft opportunistic hulls anyway.
func TestSelectHaulers_ExclusiveModeSealsWhenAnyGatePoolHasMembers(t *testing.T) {
	roleTagged := gateTestHull(t, "GATE-7", gate.DeliveryFleetTag)
	roleTagged.SetNavStatus(navigation.NavStatusInTransit) // a member, but not dispatchable this tick
	shipRepo := newDrainShipRepo(roleTagged, gateTestHull(t, "FREE-8", ""))
	h := &RunConstructionCoordinatorHandler{shipRepo: shipRepo}

	cmd := gateTestCmd()
	cmd.ExclusiveDedicatedFleet = true

	ships, err := h.selectHaulers(context.Background(), cmd, shared.MustNewPlayerID(1), gateTestSystem)
	if err != nil {
		t.Fatalf("selectHaulers returned error: %v", err)
	}
	for _, ship := range ships {
		if ship.ShipSymbol() == "FREE-8" {
			t.Fatal("exclusive mode drafted an opportunistic hull while a gate ROLE pool has members — the seal only saw the drain's own identity")
		}
	}
}

// ---------------------------------------------------------------------------------------------
// THE DELIVERY LEG
// ---------------------------------------------------------------------------------------------

// THE PER-LEG FLOOR READ. The floors are pattern-C live knobs: they must be re-read off the
// pipeline row on EVERY leg. Caching them at handler construction, or hoisting the read out of
// the leg, silently turns a live knob into a restart-only one — which looks identical from the
// outside until an operator tunes it and nothing happens.
func TestDeliverGateLeg_ReadsTheFloorsOffThePipelineRowOnEveryLeg(t *testing.T) {
	f := newGateDeliveryHandler(t)

	// Leg 1 under the ARMED defaults (unset row): MODERATE supply is at the MODERATE buy floor.
	f.topo.supply = "MODERATE"
	if !f.run(t, "GATE-7") {
		t.Fatal("leg 1: MODERATE is at the default MODERATE buy floor and must buy")
	}
	if f.buyer.calls() == 0 {
		t.Fatal("leg 1 bought nothing")
	}

	// An operator raises the buy floor between legs — the live tune.
	f.pipeline.setFloors("HIGH", "ABUNDANT")
	f.buyer.reset()

	// Leg 2 must honour the NEW floor: MODERATE is now below it, so the leg stands down.
	f.run(t, "GATE-8")
	if f.buyer.calls() != 0 {
		t.Fatalf("leg 2 bought %d time(s) after the buy floor was raised to HIGH — the floors were not re-read off the pipeline row, so the knob is restart-only", f.buyer.calls())
	}
}

// ONE BUY POLICY, HELD ACROSS LEGS. The pause state IS the hysteresis, and it lives on the
// *BuyPolicy instance. A leg that builds a fresh policy resets `paused` and degrades the two
// floors back to the single threshold that chatters — pause, one unit regenerates, resume,
// immediately deplete — with every other test in this file still green.
//
// Leg 1 pauses at LIMITED. Leg 2 observes MODERATE: at the buy floor, but BELOW the HIGH resume
// floor, so a policy that remembers stays paused and a rebuilt one buys.
func TestDeliverGateLeg_HoldsOneBuyPolicyAcrossLegsSoTheHysteresisSurvives(t *testing.T) {
	f := newGateDeliveryHandler(t)

	f.topo.supply = "LIMITED"
	f.run(t, "GATE-7")
	if f.buyer.calls() != 0 {
		t.Fatalf("leg 1: LIMITED is below the MODERATE buy floor and must not buy, got %d call(s)", f.buyer.calls())
	}

	// Supply recovers to the BUY floor but not to the RESUME floor. The floors are unchanged, so
	// the policy must be the same instance, still holding leg 1's pause.
	f.topo.supply = "MODERATE"
	f.run(t, "GATE-8")

	if f.buyer.calls() != 0 {
		t.Fatalf("leg 2 bought %d time(s) at MODERATE while paused — a paused material resumes only at the HIGH resume floor. The pause state was reset, so a fresh BuyPolicy is being built per leg and the hysteresis is gone", f.buyer.calls())
	}
}

// STANDING DOWN. When EVERY material is paused the leg spends nothing — and says so. A paused
// fleet and an idle one must not look identical.
func TestDeliverGateLeg_StandsDownAndRecordsWhenEveryMaterialIsPaused(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "SCARCE"

	f.run(t, "GATE-7")

	if f.buyer.calls() != 0 {
		t.Fatalf("a fully paused fleet bought %d time(s); it must spend nothing", f.buyer.calls())
	}
	logs := f.logLines()
	for _, want := range []string{"PAUSED", gateMaterialPrimary, "SCARCE", "MODERATE", "HIGH"} {
		if !strings.Contains(logs, want) {
			t.Fatalf("the pause was not recorded (%q missing) — a paused fleet and an idle one would look identical:\n%s", want, logs)
		}
	}
}

// ONE PAUSED MATERIAL IS NOT A PAUSED FLEET: the hull loads the other one and departs.
func TestDeliverGateLeg_OnePausedMaterialStillDepartsWithTheOther(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supplyByGood = map[string]string{gateMaterialPrimary: "SCARCE", gateMaterialSecondary: "HIGH"}

	// The leg DEPARTS (asserted on the buys below) but reports its own task as NOT drained: this lot
	// is a FAB_MATS task and the trip moved zero FAB_MATS. Completion is decided on the lot's OWN
	// material, never on whatever else the trip happened to carry — a FAB_MATS task that completes
	// having moved no FAB_MATS is the same lie as logging another good's units under its name.
	if f.run(t, "GATE-7") {
		t.Fatal("the leg reported its FAB_MATS task drained while moving zero FAB_MATS — completion must be decided on the lot's own material, not on the trip total")
	}
	bought := f.buyer.goods()
	if bought[gateMaterialSecondary] == 0 {
		t.Fatalf("the eligible material was not bought — the fleet must never idle on capacity it can use: %v", bought)
	}
	if bought[gateMaterialPrimary] != 0 {
		t.Fatalf("the PAUSED material was bought: %v", bought)
	}
}

// sp-lpy9i: THE DELIVERY FLEET BUYS AS A CONSTRUCTION SINK, and this is a WIRING pin — the floor
// itself is proved in the services package, but a correct floor reached with the wrong sink is the
// exact defect this bead exists for, and it would be invisible there.
//
// The zero value is SinkFactoryFeed, so a call site that simply forgets the argument compiles,
// runs, and silently re-inherits the factory's saturation floor. That is why this asserts the
// declared value rather than merely that a buy happened.
func TestDeliverGateLeg_BuysAsAConstructionSinkNotAFactoryFeed(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "HIGH"

	f.run(t, "GATE-7")

	sinks := f.buyer.declaredSinks()
	if len(sinks) == 0 {
		t.Fatal("the leg made no buys at all, so this pin cannot observe which sink it declared")
	}
	for i, sink := range sinks {
		if sink != mfgServices.SinkConstructionSite {
			t.Fatalf("buy %d of %d declared sink %v, want SinkConstructionSite. This hold is delivered to a construction site and consumed against a bill — a factory's activity-saturation floor does not apply to it, and inheriting it refused 52 buys over 2h33m with 80 units left", i+1, len(sinks), sink)
		}
	}
}

// sp-0u1yd — a capital-declined buy (the working-capital reserve refused the spend, not the
// topology or the market) must NOT get the same clear-and-park treatment as a genuinely
// unsourceable material. The task's resolved source survives the park, so it reads NOT deferred —
// the very next activation pass republishes it straight to READY on the SAME source
// (task_activator_construction.go's own IsDeferredConstruction() short-circuit, independently
// pinned by TestActivateConstructionTasks_RetriesFailedDeliveries in the services package) instead
// of paying for a re-resolution that cannot fix a capital shortfall.
func TestDeliverGateLeg_CapitalDeclinedBuyKeepsTheResolvedSource(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.buyer.acquireZero = true
	f.buyer.zeroReason = mfgServices.ZeroReasonCapitalDeclined

	ship := gateTestHull(t, "GATE-7", gate.DeliveryFleetTag)
	// A task with a REAL resolved source, unlike gateTestLot/gateReadyLot's empty-source
	// simplification (every OTHER test in this file uses it because deliverGateLeg never reads
	// task.SourceMarket() for routing — it re-resolves fresh every leg) — needed here so this test
	// can prove the source actually SURVIVES rather than merely observing a no-op clear.
	task := manufacturing.NewDeliverToConstructionTask(gateTestPipelineID, 1, gateMaterialPrimary, "X1-GT-REAL-SOURCE", "", gateTestSite, nil)
	if err := task.MarkReady(); err != nil {
		t.Fatalf("MarkReady: %v", err)
	}
	if err := task.AssignShip(ship.ShipSymbol()); err != nil {
		t.Fatalf("AssignShip: %v", err)
	}
	if err := task.StartExecution(); err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	lot := constructionLot{task: task, ship: ship, claimIdentity: gate.DeliveryFleetTag}

	drained := f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1))

	if drained {
		t.Fatal("a capital-declined leg delivered nothing and must not report a drain")
	}
	if got := task.Status(); got != manufacturing.TaskStatusPending {
		t.Fatalf("expected the task parked PENDING (same recoverable park a dry source would get), got %s", got)
	}
	if task.IsDeferredConstruction() {
		t.Fatal("a capital-declined park must NOT clear the resolved source — the source was fine, only the treasury wasn't; clearing it forces a wasted re-resolution")
	}
	if task.SourceMarket() != "X1-GT-REAL-SOURCE" {
		t.Fatalf("expected the resolved source market preserved, got %q", task.SourceMarket())
	}
	logs := f.logLines()
	if strings.Contains(logs, "unsourceable") {
		t.Fatalf("a capital-declined park must not be logged as unsourceable (misdiagnoses a treasury problem as a supply one):\n%s", logs)
	}
	if !strings.Contains(logs, "working-capital") {
		t.Fatalf("expected a log line naming the working-capital cause:\n%s", logs)
	}
}

// A topology refusal (nothing exports the good this era) is recorded and skipped — never
// substituted with another waypoint, and never fatal to the whole leg.
func TestDeliverGateLeg_RecordsATopologyRefusalAndKeepsGoing(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "HIGH"
	f.topo.errByGood = map[string]error{gateMaterialPrimary: errors.New("no market in the gate system exports it")}

	f.run(t, "GATE-7")

	bought := f.buyer.goods()
	if bought[gateMaterialPrimary] != 0 {
		t.Fatalf("a good with no resolvable exporter was bought anyway: %v", bought)
	}
	if bought[gateMaterialSecondary] == 0 {
		t.Fatalf("one unresolvable material aborted the whole leg; the other was still buyable: %v", bought)
	}
	// Assert the REFUSAL's own message, not merely the good's name: the trip line already carries
	// "skipped FAB_MATS: no_supply", so a test that only looks for the symbol stays green even if
	// the refusal warning is deleted outright.
	if !strings.Contains(f.logLines(), "no terminal factory for "+gateMaterialPrimary) {
		t.Fatalf("the topology refusal itself was not recorded:\n%s", f.logLines())
	}
}

// A MIXED TRIP: two stops on one hull. Both materials are bought, but only the LOT'S OWN is
// recorded against this task — recording the other would double-count against the next tick's
// reconcilePipelinesFromSite, which raises it from the live site.
func TestDeliverGateLeg_AMixedTripBuysBothMaterialsAndRecordsOnlyItsOwn(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "HIGH"
	// TradeVolume x gateMaxTranchesPerStop (4) = 40, HALF the hold, so the first material cannot
	// take the whole hull and the second stop is actually planned.
	f.topo.tradeVolume = 10

	ship := gateTestHull(t, "GATE-7", gate.DeliveryFleetTag)
	lot := gateTestLot(t, ship)
	if !f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1)) {
		t.Fatal("the mixed trip delivered nothing")
	}

	bought := f.buyer.goods()
	if bought[gateMaterialPrimary] == 0 || bought[gateMaterialSecondary] == 0 {
		t.Fatalf("a mixed trip must visit BOTH terminal factories, got %v", bought)
	}

	// The producer reports 40 units accepted per delivery call, and both stops delivered.
	if got := f.pipeline.deliveredUnits(gateMaterialPrimary); got != 40 {
		t.Fatalf("the lot's own material records %d delivered, want 40 — recording every stop against this task double-counts it against reconcilePipelinesFromSite", got)
	}
	if got := f.pipeline.deliveredUnits(gateMaterialSecondary); got != 0 {
		t.Fatalf("the OTHER material recorded %d against this lot's task; it is raised from the live site next tick, not booked here", got)
	}
}

// ---------------------------------------------------------------------------------------------
// THE LADEN HULL — this role's only unload path
// ---------------------------------------------------------------------------------------------

// A FULL HOLD MUST NOT WEDGE THE HULL. supplyTask routes a delivery lot here BEFORE the shared
// deliver-on-hand phase, so this leg is the only place a laden delivery hull can empty. A hull that
// arrives full has free capacity 0, and PlanFill returns ZERO stops for any capacity <= 0 — so
// without a flush the leg defers, the hull is re-discovered next tick, haulerPool.take re-pairs it
// with the same material because it is already holding it, and it burns a dispatch slot forever
// while the drain reports RUNNING. Reachable from a delivery error after a successful buy, a task
// deadline, or a restart mid-leg.
func TestDeliverGateLeg_AFullHoldIsUnloadedAndTheLegStillBuys(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "HIGH"

	ship := gateTestLadenHull(t, "GATE-7", gateMaterialPrimary, gateTestHoldCapacity) // 80/80: no free hold
	lot := gateTestLot(t, ship)

	if !f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1)) {
		t.Fatal("a full hull delivered nothing — its cargo was never unloaded")
	}

	if !f.deliveredGood(gateMaterialPrimary) {
		t.Fatalf("the hold aboard was never unloaded; this role has no other unload path, so the hull is wedged at a full hold forever. deliveries=%v", f.deliveries())
	}
	// EXACTLY the freed units, not merely "something". The flush returned 40, the hold was full, so
	// the fill planner must see 40 units of capacity and buy 40 — no more. Asserting only that a buy
	// happened would leave the add-back arithmetic free to drift (double-counting freed, say) with
	// nothing local to catch it.
	if got := f.buyer.goods()[gateMaterialPrimary]; got != 40 {
		t.Fatalf("the leg bought %d units after freeing 40, want exactly 40 — the freed hold was mis-added to the fill capacity (0 means it was not returned to the planner at all)", got)
	}
	if got := lot.task.Status(); got != manufacturing.TaskStatusCompleted {
		t.Fatalf("the task is %s; a leg that unloaded a full hold advanced the gate and must complete, not park with zero stops", got)
	}
}

// THE TRIP IS SIZED OFF THE POST-FLUSH BILL. The flush just met part of the bill; planning against
// the PRE-flush remaining buys toward a bill this leg already closed. Here the flush closes it
// outright, so a pre-flush sizing plans a full stop and the leg BUYS a material nobody needs — the
// site then rejects the load and it becomes met-bill residue occupying the hold.
func TestDeliverGateLeg_NeverBuysAMaterialTheFlushJustCompleted(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "HIGH"
	f.pipeline.setBill(gateMaterialPrimary, 40)  // the 40 units aboard close this bill exactly
	f.pipeline.setBill(gateMaterialSecondary, 0) // already complete: not part of this trip

	ship := gateTestLadenHull(t, "GATE-7", gateMaterialPrimary, 40)
	lot := gateTestLot(t, ship)
	f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1))

	if !f.deliveredGood(gateMaterialPrimary) {
		t.Fatalf("the flush never ran, so this test is not exercising what it claims: deliveries=%v", f.deliveries())
	}
	if got := f.buyer.goods()[gateMaterialPrimary]; got != 0 {
		t.Fatalf("the leg bought %d units of %s after its own flush met that bill — the trip was sized off the PRE-flush remaining. The site rejects the load and it becomes residue in the hold", got, gateMaterialPrimary)
	}
}

// A hull holding material whose bill is already MET is not flown anywhere — the site would reject
// the supply, so the flight is a guaranteed-failed cost. That decision is only visible through this
// WARNING, and it is the ONLY observable for a residue this phase consciously defers rather than
// fixes, so it must be pinned or it can be deleted green.
func TestDeliverGateLeg_RecordsMaterialAboardWhoseBillIsAlreadyMet(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "HIGH"
	f.pipeline.setBill(gateMaterialPrimary, 0) // met: nothing aboard can be delivered

	ship := gateTestLadenHull(t, "GATE-7", gateMaterialPrimary, gateTestHoldCapacity)
	lot := gateTestLot(t, ship)
	f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1))

	if len(f.deliveries()) != 0 {
		t.Fatalf("the leg flew a delivery of material the site cannot accept: %v", f.deliveries())
	}
	logs := f.logLines()
	if !strings.Contains(logs, "bill is already MET") || !strings.Contains(logs, gateMaterialPrimary) {
		t.Fatalf("a hull stuck holding material nobody needs was not recorded — this is the only signal that residue exists:\n%s", logs)
	}
}

// PHANTOM CARGO (API 4219) IS NOT A GENERIC DELIVERY ERROR. It means the cached row claims cargo
// the server says is not there. This role returns at supplyTask's gate-delivery branch and never
// reaches handlePhantomCargo/resyncShipCargo, so if the flush swallows a 4219 as a generic warning
// NOTHING in the system ever corrects the row: next tick the flush 4219s again, capacity stays 0,
// and the hull is wedged permanently. Reachable because writeBackDeliveredCargo is best-effort and
// this leg's own buy loop can mint the phantom.
func TestDeliverGateLeg_ResyncsTheHullWhenAFlushIsRejectedAsPhantomCargo(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "HIGH"
	f.producer.deliverErr = errors.New(`{"error":{"code":4219,"message":"ship has 0 units"}}`)

	ship := gateTestLadenHull(t, "GATE-7", gateMaterialPrimary, gateTestHoldCapacity)
	lot := gateTestLot(t, ship)
	f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1))

	resynced := f.resyncedHulls()
	if len(resynced) == 0 || resynced[0] != "GATE-7" {
		t.Fatalf("a 4219 phantom left the hull unresynced (resyncs=%v) — the cached row still reads laden, so the next leg repeats this failure forever and no other path can correct it for this role", resynced)
	}
	if !strings.Contains(f.logLines(), "PHANTOM") {
		t.Fatalf("the phantom was recorded as a generic delivery failure, which is the exact conflation this package documents against:\n%s", f.logLines())
	}
}

// THE BUY LOOP'S 4219 IS THE WORSE HALF, and the flush test cannot reach it: a full hull frees
// nothing, so PlanFill returns zero stops and the buy loop never executes. Here the hull is EMPTY,
// so the flush no-ops, the buy runs, and the delivery of freshly-paid-for units is rejected as
// phantom — units already paid for, recorded aboard a hull the server says is empty. Without a
// resync the next leg's flush inherits that lie and the wedge with it.
func TestDeliverGateLeg_ResyncsTheHullWhenTheBuyLegDeliveryIsRejectedAsPhantomCargo(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "HIGH"
	f.producer.deliverErr = errors.New(`{"error":{"code":4219,"message":"ship has 0 units"}}`)

	ship := gateTestHull(t, "GATE-7", gate.DeliveryFleetTag) // EMPTY: the flush has nothing to do
	lot := gateTestLot(t, ship)
	f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1))

	if f.buyer.calls() == 0 {
		t.Fatal("the buy loop never ran, so this test is not exercising the branch it names")
	}
	resynced := f.resyncedHulls()
	if len(resynced) == 0 || resynced[0] != "GATE-7" {
		t.Fatalf("a 4219 on the BUY leg's delivery left the hull unresynced (resyncs=%v) — the units just paid for are recorded aboard a hull the server says is empty, and the next leg's flush inherits the phantom", resynced)
	}
}

// The flush runs BEFORE the pause check. Cargo already aboard has zero market impact and always
// advances the gate, so a paused fleet must still unload — the same rule PHASE 1 of supplyTask
// states for every other role. A flush ordered after the pause check would strand a full hull for
// exactly as long as the market stays low, which is when the wedge is most likely.
func TestDeliverGateLeg_AFullHoldIsUnloadedEvenWhenTheFleetIsPaused(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "SCARCE" // every material below its buy floor

	ship := gateTestLadenHull(t, "GATE-7", gateMaterialPrimary, gateTestHoldCapacity)
	lot := gateTestLot(t, ship)

	if !f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1)) {
		t.Fatal("a paused leg holding a full hold reported no delivery — it never unloaded")
	}

	if !f.deliveredGood(gateMaterialPrimary) {
		t.Fatalf("a paused fleet did not unload the cargo already aboard; delivering it spends nothing and always advances the gate. deliveries=%v", f.deliveries())
	}
	if f.buyer.calls() != 0 {
		t.Fatalf("a paused fleet bought %d time(s) while unloading; the flush must not re-open the buy gate", f.buyer.calls())
	}
	if got := lot.task.Status(); got != manufacturing.TaskStatusCompleted {
		t.Fatalf("the task is %s; the leg advanced the gate by unloading, so it completes rather than parking", got)
	}
}

// The trip outcome is recorded: capacity, what was loaded, what was skipped and why.
func TestDeliverGateLeg_RecordsTheTripOutcome(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supplyByGood = map[string]string{gateMaterialPrimary: "SCARCE", gateMaterialSecondary: "HIGH"}

	f.run(t, "GATE-7")

	logs := f.logLines()
	if !strings.Contains(logs, gate.SkipPaused) || !strings.Contains(logs, gateMaterialPrimary) {
		t.Fatalf("the trip did not record what it skipped and why:\n%s", logs)
	}
	if !strings.Contains(logs, "units of hold loaded") || !strings.Contains(logs, gateMaterialSecondary) {
		t.Fatalf("the trip did not record its capacity and load:\n%s", logs)
	}
}

// A MIXED TRIP MUST NOT ATTRIBUTE ANOTHER MATERIAL'S UNITS TO THIS TASK. completeSupply logs its
// count against task.Good() ("Supplied %d %s"), so a cross-material total reports the wrong good's
// units under this one's name — a decision lying about itself, which is the exact failure this
// phase exists to remove. The pipeline counters were always safe; the LOG and the completion
// DECISION were not.
func TestDeliverGateLeg_CompletesOnItsOwnMaterialsUnitsNotTheTripTotal(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "HIGH"
	f.topo.tradeVolume = 10 // two stops: 40 units of each material, 80 across the trip

	ship := gateTestHull(t, "GATE-7", gate.DeliveryFleetTag)
	lot := gateTestLot(t, ship)
	if !f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1)) {
		t.Fatal("the mixed trip delivered nothing")
	}

	// Both materials really did move, so the trip total is 80 — but only 40 of them are FAB_MATS.
	if bought := f.buyer.goods(); bought[gateMaterialPrimary] == 0 || bought[gateMaterialSecondary] == 0 {
		t.Fatalf("this test needs a genuinely MIXED trip; got %v", bought)
	}

	logs := f.logLines()
	if !strings.Contains(logs, "Supplied 40 "+gateMaterialPrimary) {
		t.Fatalf("the completion did not report this task's OWN units (40 %s):\n%s", gateMaterialPrimary, logs)
	}
	if strings.Contains(logs, "Supplied 80 "+gateMaterialPrimary) {
		t.Fatalf("the completion credited the whole 80-unit trip to %s — 40 of those were %s:\n%s", gateMaterialPrimary, gateMaterialSecondary, logs)
	}
	// The cross-material units are not silently dropped either: the trip says what it really moved.
	if !strings.Contains(logs, "moved 80 unit(s)") {
		t.Fatalf("the trip's true cross-material total was never recorded, so the other material's units look unaccounted for:\n%s", logs)
	}
}

// THE FLUSH MUST NOT ATTRIBUTE ITS UNITS TO THIS TASK EITHER. A hull paired with a FAB_MATS lot can
// arrive holding ADVANCED_CIRCUITRY — haulerPool.take only PREFERS a matching hull, it does not
// require one. Unloading that circuitry advances the gate and frees the hold, but it moves zero
// FAB_MATS, so it must not complete the FAB_MATS task.
func TestDeliverGateLeg_AFlushOfAnotherMaterialDoesNotCompleteThisTask(t *testing.T) {
	f := newGateDeliveryHandler(t)
	// This lot's own material is PAUSED, so the leg can never buy it — anything credited to the task
	// could only have come from the other material.
	f.topo.supplyByGood = map[string]string{gateMaterialPrimary: "SCARCE", gateMaterialSecondary: "HIGH"}

	ship := gateTestLadenHull(t, "GATE-7", gateMaterialSecondary, gateTestHoldCapacity)
	lot := gateTestLot(t, ship) // a FAB_MATS task on a hull full of ADVANCED_CIRCUITRY

	drained := f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1))

	if !f.deliveredGood(gateMaterialSecondary) {
		t.Fatalf("the flush never unloaded the other material, so this test is not exercising what it names: %v", f.deliveries())
	}
	if drained {
		t.Fatal("the leg reported its FAB_MATS task drained on units of ADVANCED_CIRCUITRY that its flush unloaded")
	}
	if got := lot.task.Status(); got != manufacturing.TaskStatusPending {
		t.Fatalf("the FAB_MATS task is %s; it moved zero FAB_MATS, so it must PARK for re-activation, not complete on another material's units", got)
	}
	if strings.Contains(f.logLines(), "Supplied 40 "+gateMaterialPrimary) {
		t.Fatalf("the completion logged another material's units under %s:\n%s", gateMaterialPrimary, f.logLines())
	}
}

// ---------------------------------------------------------------------------------------------
// THE COMPLETION PATH (the REQUIRED AMENDMENT)
// ---------------------------------------------------------------------------------------------

// A leg that returns WITHOUT the shared completion machinery leaves its task EXECUTING forever:
// nothing persists a terminal status, nothing re-stages the next single load, the ready queue
// drains to nothing, and the drain goes quiet while still reporting RUNNING — a stall
// indistinguishable from a finished gate.
func TestDeliverGateLeg_ACompletedLegCompletesItsTaskAndEnqueuesTheNextLoad(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "HIGH"
	ship := gateTestHull(t, "GATE-7", gate.DeliveryFleetTag)
	lot := gateTestLot(t, ship)

	if !f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1)) {
		t.Fatal("the leg delivered nothing at HIGH supply")
	}

	if got := lot.task.Status(); got != manufacturing.TaskStatusCompleted {
		t.Fatalf("the delivered task is %s, not COMPLETED — a leg that skips the completion path strands its task EXECUTING forever", got)
	}
	if _, persisted := f.taskRepo.snapshot(lot.task.ID()); !persisted {
		t.Fatal("the completed task was never persisted; a restart re-reads it as EXECUTING and nothing ever picks it up")
	}
	f.taskRepo.mu.Lock()
	created := len(f.taskRepo.created)
	f.taskRepo.mu.Unlock()
	if created != 1 {
		t.Fatalf("the leg enqueued %d replenishment task(s) against an unmet bill, want 1 — without it the ready queue drains to nothing and the drain goes quiet while reporting RUNNING", created)
	}
}

// REPLENISHMENT IS SIZED OFF THE POST-DELIVERY PIPELINE. completeSupply asks the leg's pipeline
// how much of the bill is left; recordDelivery does its OWN FindByID and the production repository
// hands back a fresh aggregate, so the object read at the top of the leg is a PRE-delivery
// snapshot. Sizing off it overstates the remaining bill by exactly the units just delivered and
// enqueues a refill for a bill that is already met — and nothing ever cleans that task up, so it
// lingers READY forever and the drain can never report the gate done.
//
// The bill here is met EXACTLY by this leg's delivery, so the pre/post distinction is the whole
// answer: post-delivery says 0 remaining (no refill), pre-delivery says 40 (a phantom refill).
func TestDeliverGateLeg_SizesReplenishmentOffThePostDeliveryPipeline(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "HIGH"
	f.pipeline.setBill(gateMaterialPrimary, 40)  // one 40-unit delivery meets it exactly
	f.pipeline.setBill(gateMaterialSecondary, 0) // already complete: not part of this trip

	ship := gateTestHull(t, "GATE-7", gate.DeliveryFleetTag)
	lot := gateTestLot(t, ship)
	if !f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1)) {
		t.Fatal("the leg delivered nothing")
	}

	if got := f.pipeline.deliveredUnits(gateMaterialPrimary); got != 40 {
		t.Fatalf("the delivery recorded %d units, want 40 — the fixture is not exercising the bill this test is about", got)
	}
	if got := f.createdTasks(); got != 0 {
		t.Fatalf("the leg enqueued %d replenishment task(s) for a bill this very delivery MET; that task is never cleaned up, so it lingers READY forever and the drain can never report done. The completion path was handed the pre-delivery pipeline", got)
	}
}

// A PAUSED fleet PARKS its task for the SupplyMonitor to re-activate. Failing it instead spends a
// retry on a market condition that is not the task's fault and walks the leg toward permanent
// death; returning without either strands it EXECUTING.
func TestDeliverGateLeg_APausedLegParksItsTaskRatherThanFailingIt(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "SCARCE"
	ship := gateTestHull(t, "GATE-7", gate.DeliveryFleetTag)
	lot := gateTestLot(t, ship)

	if f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1)) {
		t.Fatal("a fully paused leg reported a delivery")
	}

	if got := lot.task.Status(); got != manufacturing.TaskStatusPending {
		t.Fatalf("the paused task is %s, want PENDING (parked for resupply) — EXECUTING strands it, FAILED spends a retry on a market condition", got)
	}
	if lot.task.RetryCount() != 0 {
		t.Fatalf("parking spent %d retry/retries; a supply pause is not a task fault", lot.task.RetryCount())
	}
	if _, persisted := f.taskRepo.snapshot(lot.task.ID()); !persisted {
		t.Fatal("the parked task was never persisted, so the SupplyMonitor never sees it")
	}
}

// An EMPTY TRIP (nothing planned) parks too, on the same machinery.
func TestDeliverGateLeg_AnEmptyTripParksItsTask(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "HIGH"
	f.topo.tradeVolume = -1 // the market quotes no per-transaction volume: nothing is buyable
	ship := gateTestHull(t, "GATE-7", gate.DeliveryFleetTag)
	lot := gateTestLot(t, ship)

	if f.handler.deliverGateLeg(f.ctx(), gateTestCmd(), gateTestSystem, lot, shared.MustNewPlayerID(1)) {
		t.Fatal("an empty trip reported a delivery")
	}
	if f.buyer.calls() != 0 {
		t.Fatalf("an empty trip bought %d time(s)", f.buyer.calls())
	}
	if got := lot.task.Status(); got != manufacturing.TaskStatusPending {
		t.Fatalf("the empty-trip task is %s, want PENDING (parked) — anything else stalls the drain silently", got)
	}
}

// A hull whose hold is FULL of material every ready bill is already satisfied for can neither
// deliver nor buy. Left in the pool it takes a lot, plans zero stops and parks — and because
// haulerPool.take PREFERS a hull already holding the good, it is handed the same lot again every
// tick, burning a dispatch slot forever while the drain reports RUNNING.
//
// This is the gate's EXPECTED end state, not an edge case: when a material's bill closes, every
// in-flight hull still holding it lands here, and reconcilePipelinesFromSite only ever RAISES
// delivered counters, so the bill never re-opens. Declining the hull converts a fleet-wide stall
// into one degraded hull. (Getting the cargo OFF it needs a sell/dump path — money-adjacent, and
// deliberately out of scope here.)
func TestConstructionDrain_DoesNotDispatchAHullWedgedAtAFullHold(t *testing.T) {
	pipeline := manufacturing.NewConstructionPipeline(constructionSiteWP, 1, 1, 5)
	if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget(gateMaterialPrimary, 40)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget(gateMaterialSecondary, 200)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	// The primary's bill is CLOSED — exactly the state a completed material leaves behind.
	if err := pipeline.RecordMaterialDelivery(gateMaterialPrimary, 40); err != nil {
		t.Fatalf("RecordMaterialDelivery: %v", err)
	}
	// The tick still has real work: the secondary's bill is wide open.
	task := readyConstructionTask(t, pipeline, gateMaterialSecondary)

	stranded, err := shared.NewCargoItem(gateMaterialPrimary, gateMaterialPrimary, "", 40)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	hull := newTestHauler(t, "TORWIND-9", []*shared.CargoItem{stranded}) // 40/40: no free hold
	hull.SetDedicatedFleet(gate.DeliveryFleetTag)                        // the role whose leg is flush-then-buy

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hull)
	logger := &capturingLogger{}

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetGateDelivery(&stubGateTopology{supply: "HIGH"}, &countingGateBuyer{})
	if _, err := drainSettled(t, handler, common.WithLogger(context.Background(), logger), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := shipRepo.claimCount(); got != 0 {
		t.Fatalf("a hull with a full hold of material nobody needs was claimed %d time(s); it can neither deliver nor buy, so it consumes a dispatch slot every tick forever", got)
	}
	joined := logger.joined()
	if !strings.Contains(joined, "TORWIND-9") || !strings.Contains(joined, "FULL hold") {
		t.Fatalf("the wedged hull was dropped from the pool SILENTLY — an undiagnosable missing hauler is the exact invisibility this phase removes:\n%s", joined)
	}
}

// THE DECLINE IS SCOPED TO THE DELIVERY ROLE, and that scoping is the whole of its correctness.
// "Full hold + nothing aboard is wanted = can do no work" is true only of the gate leg, whose
// entire repertoire is flush-then-buy.
//
// The LEGACY fabricate role recovers exactly this hull by a route the predicate cannot see:
// sourceAndDeliverRemainder has no free-capacity precheck, and fabricateGood navigates to the
// factory and unloads on-hand INPUTS there before harvesting. A hull idled with a full hold of
// fabrication inputs — what an abandoned supplyTask leaves behind, "the hull keeps its load" —
// carries no material any task NAMES, so an unscoped decline would call it dead and drop it every
// tick forever, relocating this very failure mode onto a role that had a working recovery.
func TestConstructionDrain_StillDispatchesALegacyHullFullOfFabricationInputs(t *testing.T) {
	pipeline := newDrainPipeline(t, gateMaterialSecondary, 200)
	task := readyConstructionTask(t, pipeline, gateMaterialSecondary)

	// A full hold of an INPUT — never the task's own good, so nothing the predicate matches on.
	inputs, err := shared.NewCargoItem("IRON_ORE", "IRON_ORE", "", 40)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	hull := newTestHauler(t, "TORWIND-3", []*shared.CargoItem{inputs}) // 40/40: no free hold
	hull.SetDedicatedFleet(gate.LegacyFleetTag)

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hull)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetGateDelivery(&stubGateTopology{supply: "HIGH"}, &countingGateBuyer{})
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := shipRepo.claimCount(); got != 1 {
		t.Fatalf("a LEGACY hull full of fabrication inputs was claimed %d time(s), want 1 — the fabricate path unloads those inputs at the factory, so declining it makes a recoverable hull permanently invisible", got)
	}
}

// THE DECLINE ALSO REQUIRES THE GATE LEG TO BE WIRED, because that is the other half of
// supplyTask's routing condition. With the collaborator unset, a delivery-TAGGED hull does not run
// the gate leg at all — it takes the shared fabricate path and recovers there like any legacy hull.
// Declining it would rest on the behaviour of a leg that is not going to run. Every pre-existing
// coordinator test builds the handler without SetGateDelivery, so this configuration is real.
func TestConstructionDrain_StillDispatchesAFullDeliveryHullWhenTheGateLegIsUnwired(t *testing.T) {
	pipeline := newDrainPipeline(t, gateMaterialSecondary, 200)
	task := readyConstructionTask(t, pipeline, gateMaterialSecondary)

	inputs, err := shared.NewCargoItem("IRON_ORE", "IRON_ORE", "", 40)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	hull := newTestHauler(t, "TORWIND-5", []*shared.CargoItem{inputs}) // 40/40: no free hold
	hull.SetDedicatedFleet(gate.DeliveryFleetTag)

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hull)

	// Deliberately NO SetGateDelivery: the gate leg is unwired.
	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := shipRepo.claimCount(); got != 1 {
		t.Fatalf("a delivery-tagged hull was declined %d claim(s) while the gate leg is UNWIRED — it runs the shared fabricate path in that configuration and recovers there, so the decline was made about a leg that never executes", got)
	}
}

// DEMAND, NOT BUY BUDGET. The decline asks whether the SITE still wants what the hull carries.
// `budget.remaining` is a BUY budget — already net of what in-flight workers are authorized to
// purchase — and unloading cargo already aboard is not a purchase. Asking the buy budget declares a
// hull carrying genuinely-wanted material to be carrying nothing: it is declined, while the
// in-flight worker it was netted against goes and PAYS MARKET for the very units it was holding.
func TestConstructionDrain_StillDispatchesAFullHullWhoseMaterialIsFullyReservedByWorkersInFlight(t *testing.T) {
	// TWO materials, and the second is what makes this observable: with the hull's own material
	// fully reserved there is no lot for IT, so the tick must still have other work or nothing is
	// dispatched to anyone and the predicate is never consulted.
	pipeline := manufacturing.NewConstructionPipeline(constructionSiteWP, 1, 1, 5)
	if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget(gateMaterialPrimary, 40)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget(gateMaterialSecondary, 200)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	reserved := readyConstructionTask(t, pipeline, gateMaterialPrimary)
	open := readyConstructionTask(t, pipeline, gateMaterialSecondary)

	aboard, err := shared.NewCargoItem(gateMaterialPrimary, gateMaterialPrimary, "", 40)
	if err != nil {
		t.Fatalf("NewCargoItem: %v", err)
	}
	hull := newTestHauler(t, "TORWIND-7", []*shared.CargoItem{aboard}) // 40/40, holding the wanted good
	hull.SetDedicatedFleet(gate.DeliveryFleetTag)

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{reserved, open}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hull)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	handler.SetGateDelivery(&stubGateTopology{supply: "HIGH"}, &countingGateBuyer{})

	// Another drain's worker is in flight and has RESERVED the whole outstanding bill for the
	// material this hull is CARRYING, so its buy budget is 0 while the site's demand is still 40.
	// Registered under a DIFFERENT container: reservations are fleet-wide (keyed by material) but
	// the tick's join is per-container, so this models a real concurrent reservation without the
	// test waiting forever on a worker that has no goroutine behind it.
	if _, admitted := handler.supplies.admit("OTHER-9", "other-container", materialKey(reserved), 40, handler.clock.Now().Add(time.Hour)); !admitted {
		t.Fatal("could not register the in-flight worker this test depends on")
	}
	if got := handler.supplies.reservedUnits(materialKey(reserved)); got != 40 {
		t.Fatalf("the in-flight reservation this test depends on is %d, want 40 — the fixture is not creating the condition under test", got)
	}

	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := shipRepo.claimCount(); got != 1 {
		t.Fatalf("a hull holding 40 units the site still WANTS was claimed %d time(s), want 1 — the decline asked the BUY BUDGET (fully reserved) instead of the outstanding bill, so a free unload was refused while the in-flight worker pays market for the same units", got)
	}
}

// THE DECLINE MUST REST ON POSITIVE EVIDENCE. A hull whose cargo is UNREADABLE — the row advertises
// a hold but the cargo value object reports none — has not been shown to be wedged; it has only
// failed to answer. Dropping it would make a working hauler invisible to the drain with nothing but
// one warning to say why, which is the same absence-of-evidence mistake that produces deadlocks
// exactly when the system is cold. It stays in the pool.
func TestConstructionDrain_StillDispatchesAHullWhoseHoldCannotBeRead(t *testing.T) {
	pipeline := manufacturing.NewConstructionPipeline(constructionSiteWP, 1, 1, 5)
	if err := pipeline.AddMaterial(manufacturing.NewConstructionMaterialTarget(gateMaterialSecondary, 200)); err != nil {
		t.Fatalf("AddMaterial: %v", err)
	}
	if err := pipeline.Start(); err != nil {
		t.Fatalf("pipeline.Start: %v", err)
	}
	task := readyConstructionTask(t, pipeline, gateMaterialSecondary)

	// The ship ROW advertises a 40-unit hold; the cargo value object reports capacity 0 — a desync,
	// not a full hull. It passes discovery (which reads the row's CargoCapacity) and reaches the
	// dispatch planner, which must not conclude "wedged" from a hold it cannot read.
	unreadable, err := shared.NewCargo(0, 0, nil)
	if err != nil {
		t.Fatalf("NewCargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("NewFuel: %v", err)
	}
	waypoint, err := shared.NewWaypoint(testFactoryWaypoint, 0, 0)
	if err != nil {
		t.Fatalf("NewWaypoint: %v", err)
	}
	hull, err := navigation.NewShip(
		"TORWIND-4", shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, unreadable, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("NewShip: %v", err)
	}

	producer := &fakeConstructionProducer{acquire: 40, delivered: 40}
	taskRepo := &drainStubTaskRepo{tasks: []*manufacturing.ManufacturingTask{task}}
	pipelineRepo := &drainStubPipelineRepo{pipelines: map[string]*manufacturing.ManufacturingPipeline{pipeline.ID(): pipeline}}
	shipRepo := newDrainShipRepo(hull)

	handler := NewRunConstructionCoordinatorHandler(taskRepo, pipelineRepo, shipRepo, producer, staticActivator(&fakeConstructionActivator{}), &factoryFakeClock{})
	if _, err := drainSettled(t, handler, context.Background(), newDrainCommand()); err != nil {
		t.Fatalf("drainOnce: %v", err)
	}

	if got := shipRepo.claimCount(); got != 1 {
		t.Fatalf("a hull whose hold could not be read was claimed %d time(s), want 1 — it was DECLINED on absence of evidence, which silently removes working capacity from the fleet", got)
	}
}

// ---------------------------------------------------------------------------------------------
// ROUTING
// ---------------------------------------------------------------------------------------------

// Only a DELIVERY-role lot takes the gate leg. It buys terminal-factory output and hauls it; it
// never fabricates and never walks the recipe graph, so it must not reach the shared
// source-then-deliver engine. Every other lot must still take that engine, or wiring the delivery
// fleet would silently re-route the legacy gate workforce.
func TestSupplyTask_RoutesOnlyDeliveryRoleLotsToTheGateLeg(t *testing.T) {
	f := newGateDeliveryHandler(t)
	f.topo.supply = "HIGH"

	delivery := gateReadyLot(t, gateTestHull(t, "GATE-7", gate.DeliveryFleetTag))
	f.handler.supplyTask(f.ctx(), gateTestCmd(), gateTestSystem, delivery, shared.MustNewPlayerID(1))

	if f.buyer.calls() == 0 {
		t.Fatal("a gate-delivery lot never reached the delivery leg")
	}
	if len(f.producer.produceGoods) != 0 {
		t.Fatalf("a gate-delivery lot walked the fabricate engine: %v", f.producer.produceGoods)
	}

	f.buyer.reset()
	legacy := gateReadyLot(t, gateTestHull(t, "MFG-9", gate.LegacyFleetTag))
	legacy.claimIdentity = operationManufacturing
	f.handler.supplyTask(f.ctx(), gateTestCmd(), gateTestSystem, legacy, shared.MustNewPlayerID(1))

	if f.buyer.calls() != 0 {
		t.Fatalf("a legacy gate lot was re-routed to the delivery leg (%d buy call(s)); wiring the delivery fleet must not change the existing workforce's path", f.buyer.calls())
	}
	if len(f.producer.produceGoods) == 0 {
		t.Fatal("a legacy gate lot never reached the shared source-then-deliver engine")
	}
}

// The delivery leg resolves every location through GateTopology at runtime. A waypoint
// literal here would pin the fleet to one era's map and then quietly send hulls nowhere.
func TestGateDeliveryLeg_ContainsNoWaypointLiterals(t *testing.T) {
	assertNoWaypointLiterals(t, "run_construction_coordinator_gate_delivery.go", "deliverGateLeg")
}

// assertNoWaypointLiterals is the era-invariance source sweep, over one file, proving it read that
// file by requiring a symbol only that file defines.
//
// Extracted so the FACTORY leg's own file is swept by the SAME calibrated pattern rather than a
// second copy free to drift. The gate package's sweep (fill_test.go) cannot be reused: it globs
// *.go RELATIVE TO ITS OWN PACKAGE DIR, so it covers only internal/domain/manufacturing/gate/ and
// nothing in this package.
func assertNoWaypointLiterals(t *testing.T, file, mustContain string) {
	t.Helper()
	waypointLiteral := regexp.MustCompile(`"[A-Z]\d+-[A-Z0-9]+-[A-Z0-9]+"`)

	// Four shapes, not one. A regression that TIGHTENS the pattern still matches X1-KP23-F46,
	// so a single-string calibration stays green while the guard goes blind to the rest.
	for _, known := range []string{
		`x := "X1-KP23-F46"`,
		`"X1-UM5-J59"`,
		`"X1-DR-GATE"`,
		`"X1-BETA-MARKETPLACE"`,
	} {
		if !waypointLiteral.MatchString(known) {
			t.Fatalf("waypoint-literal pattern failed its own calibration on %s", known)
		}
	}
	// The goods are the invariant. A pattern that flagged them would be unusable and deleted.
	for _, invariant := range []string{`good := "FAB_MATS"`, `good := "ADVANCED_CIRCUITRY"`} {
		if waypointLiteral.MatchString(invariant) {
			t.Fatalf("waypoint-literal pattern flags %s; goods are era-invariant and must be nameable directly", invariant)
		}
	}

	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	// Prove the sweep read the leg itself. An emptied or renamed file would otherwise pass
	// this guard forever while scanning nothing.
	if !strings.Contains(string(src), mustContain) {
		t.Fatalf("%s does not contain %s; the guard is reading the wrong file and would pass vacuously", file, mustContain)
	}
	if found := waypointLiteral.FindAllString(string(src), -1); len(found) > 0 {
		t.Fatalf("%s contains hardcoded waypoint symbols %v — the terminal factory is resolved by EXPORT role, per era", file, found)
	}
	if strings.Contains(string(src), "X1-") {
		t.Fatalf("%s references an X1- prefixed symbol", file)
	}
}
