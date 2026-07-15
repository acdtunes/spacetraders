package harness

// Reusable scenario harness for the capacity reconciler (st-6wa). The scenario
// tests in scenarios_test.go drive the REAL sensor->planner->differ over a
// seeded test DB and assert observable outcomes at the actuation boundary. The
// only test doubles are the spies at that boundary (Actuator, ProposalChannel),
// the kill switch, and the Governor stand-in (st-x00 not merged). The real
// components are wrapped in DELEGATING counting spies so a "zero phase
// invocations" assertion is possible without replacing their behaviour.

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	capacityAdapters "github.com/andrescamacho/spacetraders-go/internal/adapters/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/capacity/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/dutycycle"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// t0 is the frozen clock base every scenario reads. The seeded world does not
// change between ticks, so every SENSE pass sees the identical snapshot.
var t0 = time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)

// Scenario waypoints. The convergence and capital worlds live in different
// systems and different test DBs; the symbols are kept distinct for readability.
const (
	convHub    = "X1-CV77-H1" // covered hub whose actual topology diverges from desired
	convSource = "X1-CV77-S1" // in-system IRON source market (distance 50 from the hub)
	convStray  = "X1-CV77-W9" // where the misplaced second warehouse sits (off-anchor)

	capitalHub    = "X1-CP88-H2" // demanded hub with NO cluster covering it (uncovered)
	capitalSource = "X1-CP88-S2" // in-system IRON source for the uncovered hub
	capitalDock   = "X1-CP88-D9" // where the reusable idle hulls wait (st-780)
)

// ---- test DB + seed helpers (mirroring the SENSE adapter's own harness) ------

func newScenarioDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	return db
}

func seedPlayer(t *testing.T, db *gorm.DB, agentSymbol string) int {
	t.Helper()
	player := persistence.PlayerModel{AgentSymbol: agentSymbol, Token: "tok", CreatedAt: t0}
	require.NoError(t, db.Create(&player).Error)
	return player.ID
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}

func rfc3339(ts time.Time) string { return ts.UTC().Format(time.RFC3339) }

// seedContract persists one completed contract; its deliveries drive per-hub
// demand and its payment drives the hub-coverage ranking.
func seedContract(t *testing.T, db *gorm.DB, playerID int, id string, deliveries []contract.Delivery, onAccepted, onFulfilled int, lastUpdated time.Time) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.ContractModel{
		ID:                 id,
		PlayerID:           playerID,
		FactionSymbol:      "COSMIC",
		Type:               "PROCUREMENT",
		Accepted:           true,
		Fulfilled:          true,
		DeadlineToAccept:   rfc3339(t0.Add(24 * time.Hour)),
		Deadline:           rfc3339(t0.Add(48 * time.Hour)),
		PaymentOnAccepted:  onAccepted,
		PaymentOnFulfilled: onFulfilled,
		DeliveriesJSON:     mustJSON(t, deliveries),
		LastUpdated:        rfc3339(lastUpdated),
	}).Error)
}

func seedWaypoint(t *testing.T, db *gorm.DB, symbol, system string, x, y float64) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.WaypointModel{
		WaypointSymbol: symbol,
		SystemSymbol:   system,
		Type:           "PLANET",
		X:              x,
		Y:              y,
		Traits:         "[]",
		Orbitals:       "[]",
	}).Error)
}

// seedMarketSelling seeds a market row that sells one good in-system, so the
// SENSE distance resolver can cost that good (a good with no market is skipped
// by the planner's buffer selection).
func seedMarketSelling(t *testing.T, db *gorm.DB, playerID int, waypoint, good string) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.MarketData{
		WaypointSymbol: waypoint,
		GoodSymbol:     good,
		PurchasePrice:  90,
		SellPrice:      100,
		TradeVolume:    60,
		LastUpdated:    t0,
		PlayerID:       playerID,
	}).Error)
}

// seedDepot persists one contract depot. The SENSE topology projects it into a
// cluster whose hub symbol is the FIRST warehouse's waypoint (the >=1-warehouse
// anchor invariant), so every depot must carry at least one warehouse.
func seedDepot(t *testing.T, db *gorm.DB, playerID int, id string, warehouses, stockers, workers []depot.Element) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.ContractDepotModel{
		ID:            id,
		PlayerID:      playerID,
		Warehouses:    mustJSON(t, warehouses),
		Stockers:      mustJSON(t, stockers),
		DeliveryHulls: mustJSON(t, workers),
		SourceHubs:    mustJSON(t, []depot.Element{}),
	}).Error)
}

// seedWarehouseContainer persists an ACTIVE warehouse container THROUGH the
// production repository, so its stored container_type is exactly what the SENSE
// caps query matches (uppercase container.ContainerTypeWarehouse) — the seed
// can never drift from the write path. Its target_units become the warehouse's
// actual per-good caps the differ compares against desired.
func seedWarehouseContainer(t *testing.T, db *gorm.DB, playerID int, id, shipSymbol, waypoint string, targetUnits map[string]int) {
	t.Helper()
	repo := persistence.NewContainerRepository(db)
	config := map[string]interface{}{
		"ship_symbol":     shipSymbol,
		"waypoint_symbol": waypoint,
		"target_units":    targetUnits,
		"operation":       "warehouse",
	}
	entity := container.NewContainer(id, container.ContainerTypeWarehouse, playerID, -1, nil, config, nil)
	require.NoError(t, repo.Add(context.Background(), entity, "warehouse"))
	require.NoError(t, repo.UpdateStatus(context.Background(), id, playerID, container.ContainerStatusRunning, nil, nil, ""))
}

// seedIdleHull persists one ship that is IDLE (no container flying it),
// UNDEDICATED, and absent from every depot — the tier-1 reuse-eligible class the
// SENSE lane must surface on TopologySignals.IdleHulls (st-780). Left invisible,
// the differ cannot reassign it and every role escalates to tier-4 capital.
func seedIdleHull(t *testing.T, db *gorm.DB, playerID int, symbol, location, system string) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:     symbol,
		PlayerID:       playerID,
		LocationSymbol: location,
		SystemSymbol:   system,
		ContainerID:    nil, // idle: no container flying it
		DedicatedFleet: "",  // undedicated: reuse may reassign it
		CargoCapacity:  80,
	}).Error)
}

// fakeTreasury doubles the sensor's ONLY live-API boundary (agent credit
// balance). The governor stand-ins ignore economics, so the value only needs to
// be non-failing.
type fakeTreasury struct{ credits int }

func (f fakeTreasury) LiveCredits(context.Context, shared.PlayerID) (int, error) {
	return f.credits, nil
}

// newRealSensor builds the PRODUCTION SENSE adapter over the seeded DB, frozen
// at t0 with an empty duty-cycle report (no daemon sampler in a test).
func newRealSensor(db *gorm.DB) *capacityAdapters.Sensor {
	return capacityAdapters.NewSensor(db, fakeTreasury{credits: 500000},
		capacityAdapters.WithSensorClock(&shared.MockClock{CurrentTime: t0}),
		capacityAdapters.WithDutyCycleReport(func() dutycycle.Report { return dutycycle.Report{} }),
	)
}

// ---- spies at the actuation boundary (the ONLY behaviour-replacing doubles) --

// spyActuator records every verb invocation and succeeds inertly. It never
// executes a real primitive, so a scenario can run any number of ticks without
// mutating the seeded world.
type spyActuator struct {
	mu           sync.Mutex
	byVerb       map[capacity.ActionVerb][]capacity.Action
	capitalCalls int // ExecuteCapital invocations specifically (invariant-4 witness)
}

func newSpyActuator() *spyActuator {
	return &spyActuator{byVerb: map[capacity.ActionVerb][]capacity.Action{}}
}

func (s *spyActuator) record(action capacity.Action) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byVerb[action.Verb] = append(s.byVerb[action.Verb], action)
	return nil
}

func (s *spyActuator) ReuseIdleHull(_ context.Context, a capacity.Action) error { return s.record(a) }
func (s *spyActuator) Rebalance(_ context.Context, a capacity.Action) error     { return s.record(a) }
func (s *spyActuator) AdjustBuffer(_ context.Context, a capacity.Action) error  { return s.record(a) }

func (s *spyActuator) ExecuteCapital(_ context.Context, a capacity.Action) error {
	s.mu.Lock()
	s.capitalCalls++
	s.mu.Unlock()
	return s.record(a)
}

func (s *spyActuator) calls(verb capacity.ActionVerb) []capacity.Action {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capacity.Action(nil), s.byVerb[verb]...)
}

func (s *spyActuator) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, actions := range s.byVerb {
		n += len(actions)
	}
	return n
}

func (s *spyActuator) executeCapitalCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.capitalCalls
}

// spyProposals records filed capital proposals.
type spyProposals struct {
	mu        sync.Mutex
	submitted []capacity.Proposal
}

func (s *spyProposals) Submit(_ context.Context, p capacity.Proposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.submitted = append(s.submitted, p)
	return nil
}

func (s *spyProposals) all() []capacity.Proposal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capacity.Proposal(nil), s.submitted...)
}

// toggleKillSwitch is the captain/DISABLED stand-in.
type toggleKillSwitch struct {
	mu      sync.Mutex
	engaged bool
}

func (k *toggleKillSwitch) Disabled() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.engaged
}

func (k *toggleKillSwitch) set(engaged bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.engaged = engaged
}

// ---- delegating counting spies over the REAL components ----------------------
// These wrap the production Sensor / Planner / Differ: behaviour is 100% real
// (every call delegates), the wrapper only counts invocations so a scenario can
// assert "SENSE/PLAN/DIFF did (not) run this tick".

type countingSensor struct {
	inner capacity.Sensor
	mu    sync.Mutex
	calls int
}

func (c *countingSensor) Sense(ctx context.Context, playerID int) (capacity.Signals, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.inner.Sense(ctx, playerID)
}

func (c *countingSensor) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// idleHullsSuppressingSensor wraps the REAL sensor and blanks
// TopologySignals.IdleHulls after each pass, reproducing the st-780 bug (the
// SENSE lane never filling the reuse-eligible idle subset). It is the mutation
// control for the reuse scenario: the SAME seed that closes a hull gap by
// REUSING idle hulls (tier-1) with a real sensor escalates the whole gap to
// tier-4 capital once the signal is blanked — proof the IdleHulls population is
// load-bearing, not decorative.
type idleHullsSuppressingSensor struct{ inner capacity.Sensor }

func (s idleHullsSuppressingSensor) Sense(ctx context.Context, playerID int) (capacity.Signals, error) {
	signals, err := s.inner.Sense(ctx, playerID)
	signals.Topology.IdleHulls = nil
	return signals, err
}

type countingPlanner struct {
	inner capacity.Planner
	mu    sync.Mutex
	calls int
}

func (c *countingPlanner) ComputeDesired(ctx context.Context, s capacity.Signals, cal capacity.Calibration) (capacity.DesiredTopology, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.inner.ComputeDesired(ctx, s, cal)
}

func (c *countingPlanner) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

type countingDiffer struct {
	inner capacity.Differ
	mu    sync.Mutex
	calls int
}

func (c *countingDiffer) Diff(ctx context.Context, desired capacity.DesiredTopology, actual capacity.TopologySignals, cal capacity.Calibration) ([]capacity.Action, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return c.inner.Diff(ctx, desired, actual, cal)
}

func (c *countingDiffer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// ---- governor stand-ins (st-x00 not merged) ----------------------------------

// autonomyGovernor is the faithful stand-in for the not-yet-merged st-x00
// capex governor: it applies the DOCUMENTED Govern contract (ports.go) and
// nothing more — autonomous tiers (1-3) pass through to Approved, tier-4
// capital becomes a Proposal. It NEVER auto-approves capital (v1: ALL tier-4
// -> proposal), so it is the loop's own CONVERGE that decides execute-vs-file.
// It computes no gap logic of its own — the real differ already produced the
// actions — so it introduces no circular verification.
type autonomyGovernor struct{}

func (autonomyGovernor) Govern(_ context.Context, actions []capacity.Action, _ capacity.EconomicsSignals, _ capacity.Calibration) (capacity.GovernResult, error) {
	var result capacity.GovernResult
	for _, action := range actions {
		if action.Tier.RequiresApproval() {
			result.Proposals = append(result.Proposals, capacity.Proposal{
				ID:     "prop-" + string(action.Verb) + "-" + action.HubSymbol,
				Action: action,
			})
			continue
		}
		result.Approved = append(result.Approved, action)
	}
	return result, nil
}

// capitalApprovingGovernor is a DELIBERATELY MISBEHAVING governor: it approves
// EVERY action, including the tier-4 capital it should have proposed. It exists
// only to prove the CONVERGE capital backstop (invariant 4) refuses
// wrongly-approved capital STRUCTURALLY — independent of governor correctness.
type capitalApprovingGovernor struct{}

func (capitalApprovingGovernor) Govern(_ context.Context, actions []capacity.Action, _ capacity.EconomicsSignals, _ capacity.Calibration) (capacity.GovernResult, error) {
	return capacity.GovernResult{Approved: append([]capacity.Action(nil), actions...)}, nil
}

// ---- the reconciler fixture --------------------------------------------------

type harness struct {
	db        *gorm.DB
	playerID  int
	sensor    *countingSensor
	planner   *countingPlanner
	differ    *countingDiffer
	governor  capacity.Governor
	actuator  *spyActuator
	proposals *spyProposals
	kill      *toggleKillSwitch
	clock     *shared.MockClock
}

type harnessOption func(*harness)

func withGovernor(g capacity.Governor) harnessOption { return func(h *harness) { h.governor = g } }

// withDiffer swaps the wrapped differ implementation (used by the mutation
// guard to neuter DIFF with capacity.NoOpDiffer).
func withDiffer(d capacity.Differ) harnessOption {
	return func(h *harness) { h.differ = &countingDiffer{inner: d} }
}

func withKillEngaged() harnessOption { return func(h *harness) { h.kill.set(true) } }

// withIdleHullsSuppressed re-empties TopologySignals.IdleHulls after a REAL
// SENSE pass (mutation control for the st-780 tier-1 reuse scenario).
func withIdleHullsSuppressed() harnessOption {
	return func(h *harness) {
		h.sensor = &countingSensor{inner: idleHullsSuppressingSensor{inner: newRealSensor(h.db)}}
	}
}

// newHarness wires the reconciler from the REAL components over the seeded DB.
// Defaults: real sensor/planner/differ (each behind a counting spy), the
// autonomy governor stand-in, spy actuator + proposal channel, a clear kill
// switch, and a frozen mock clock.
func newHarness(t *testing.T, db *gorm.DB, playerID int, opts ...harnessOption) *harness {
	t.Helper()
	h := &harness{
		db:        db,
		playerID:  playerID,
		sensor:    &countingSensor{inner: newRealSensor(db)},
		planner:   &countingPlanner{inner: capacity.NewHeuristicPlanner()},
		differ:    &countingDiffer{inner: capacity.NewLadderDiffer()},
		governor:  autonomyGovernor{},
		actuator:  newSpyActuator(),
		proposals: &spyProposals{},
		kill:      &toggleKillSwitch{},
		clock:     &shared.MockClock{CurrentTime: t0},
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

func (h *harness) handler() *commands.RunCapacityReconcilerCoordinatorHandler {
	return commands.NewRunCapacityReconcilerCoordinatorHandler(
		capacity.NewStaticDomain(capacity.ContractDeliveryDomainName, h.sensor, h.planner),
		h.differ,
		h.governor,
		h.actuator,
		h.proposals,
		h.kill,
		h.clock,
	)
}

func (h *harness) command(dryRun bool) *commands.RunCapacityReconcilerCoordinatorCommand {
	return &commands.RunCapacityReconcilerCoordinatorCommand{
		PlayerID:         shared.MustNewPlayerID(h.playerID),
		ContainerID:      "capacity-reconciler-harness",
		TickIntervalSecs: 60,
		DryRun:           dryRun,
	}
}

// ---- tick driver (SetTickObserver seam) --------------------------------------

// collectingObserver gathers outcomes and cancels the run from INSIDE
// ObserveTick once `stopAt` ticks arrived — the loop exits at its next context
// check without starting another tick, so every run is deterministic (no
// sleeps, no tick races).
type collectingObserver struct {
	mu       sync.Mutex
	outcomes []capacity.TickOutcome
	stopAt   int
	cancel   context.CancelFunc
}

func (o *collectingObserver) ObserveTick(out capacity.TickOutcome) {
	o.mu.Lock()
	o.outcomes = append(o.outcomes, out)
	n := len(o.outcomes)
	o.mu.Unlock()
	if n >= o.stopAt {
		o.cancel()
	}
}

func (o *collectingObserver) snapshot() []capacity.TickOutcome {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]capacity.TickOutcome(nil), o.outcomes...)
}

// runTicks drives Handle until the observer collected `ticks` outcomes, then
// waits for the loop to exit. It fails the test if the loop wedges.
func (h *harness) runTicks(t *testing.T, dryRun bool, ticks int) []capacity.TickOutcome {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	obs := &collectingObserver{stopAt: ticks, cancel: cancel}
	handler := h.handler()
	handler.SetTickObserver(obs)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = handler.Handle(ctx, h.command(dryRun))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("reconcile loop did not stop after the requested ticks — loop wedged")
	}
	outcomes := obs.snapshot()
	require.Len(t, outcomes, ticks)
	return outcomes
}

// ---- action projection for stable, intention-revealing assertions ------------

// actionSummary is the observable salient of one convergence action — the
// fields a stakeholder reads (tier, verb, subject, target). Asserting on this
// (never on the Reason prose or internal deltas) keeps the scenario tests
// robust to refactoring while still pinning WHAT converges and in WHAT order.
type actionSummary struct {
	Tier   capacity.Tier
	Verb   capacity.ActionVerb
	Hub    string
	Ship   string
	Good   string
	Cap    int
	Target string
}

func summarize(actions []capacity.Action) []actionSummary {
	out := make([]actionSummary, 0, len(actions))
	for _, a := range actions {
		out = append(out, actionSummary{
			Tier:   a.Tier,
			Verb:   a.Verb,
			Hub:    a.HubSymbol,
			Ship:   a.ShipSymbol,
			Good:   a.Good,
			Cap:    a.UnitsCap,
			Target: a.TargetWaypoint,
		})
	}
	return out
}

// ---- scenario worlds ---------------------------------------------------------

// seedConvergenceWorld builds a COVERED hub whose actual topology diverges from
// the planner's desired in two cheap-tier ways at once:
//   - a second warehouse (WH-2) parked OFF the hub anchor  -> tier-2 reposition;
//   - WH-1's active container over-caps IRON (120 vs the planner's
//     ceil(30*1.5)=45) and still whitelists an un-demanded COPPER (60)
//     -> tier-3 cap cut + tier-3 de-whitelist.
//
// Desired warehouse/stocker/worker counts (1/1/1) are <= the actual counts, so
// NO hull shortfall escalates to capital — the whole gap is free/cheap.
func seedConvergenceWorld(t *testing.T, db *gorm.DB) int {
	t.Helper()
	playerID := seedPlayer(t, db, "AGENT-CONVERGE")

	// One paying IRON contract at the hub: 1 contract in a >=1h window (LastUpdated
	// == now t0 floors the window to 1h) -> frequency 1.0/hr, 30 units, 40000 total.
	seedContract(t, db, playerID, "cv-1",
		[]contract.Delivery{{TradeSymbol: "IRON", DestinationSymbol: convHub, UnitsRequired: 30, UnitsFulfilled: 30}},
		10000, 30000, t0)

	// IRON sourced in-system 50 units away -> it clears the buffer floor and the
	// planner wants it capped at ceil(30 * 1.5) = 45.
	seedWaypoint(t, db, convHub, "X1-CV77", 0, 0)
	seedWaypoint(t, db, convSource, "X1-CV77", 30, 40)
	seedMarketSelling(t, db, playerID, convSource, "IRON")

	// Actual cluster: WH-1 correctly at the hub, WH-2 STRAY off-anchor, one
	// stocker, one worker. WH-1's active container carries the divergent caps.
	seedDepot(t, db, playerID, "depot-cv",
		[]depot.Element{{Waypoint: convHub, ShipSymbol: "WH-1"}, {Waypoint: convStray, ShipSymbol: "WH-2"}},
		[]depot.Element{{Waypoint: convSource, ShipSymbol: "ST-1"}},
		[]depot.Element{{Waypoint: convHub, ShipSymbol: "DL-1"}})
	seedWarehouseContainer(t, db, playerID, "wh-container-cv", "WH-1", convHub, map[string]int{"IRON": 120, "COPPER": 60})

	return playerID
}

// seedConvergedFixpointWorld builds the SAME demand world already AT its desired
// shape: a single warehouse on the anchor (no reposition), IRON capped at
// exactly 45 with no stray goods, and matching stocker + worker counts. A tick
// over it must produce ZERO actions.
func seedConvergedFixpointWorld(t *testing.T, db *gorm.DB) int {
	t.Helper()
	playerID := seedPlayer(t, db, "AGENT-FIXPOINT")

	seedContract(t, db, playerID, "fx-1",
		[]contract.Delivery{{TradeSymbol: "IRON", DestinationSymbol: convHub, UnitsRequired: 30, UnitsFulfilled: 30}},
		10000, 30000, t0)
	seedWaypoint(t, db, convHub, "X1-CV77", 0, 0)
	seedWaypoint(t, db, convSource, "X1-CV77", 30, 40)
	seedMarketSelling(t, db, playerID, convSource, "IRON")

	seedDepot(t, db, playerID, "depot-fx",
		[]depot.Element{{Waypoint: convHub, ShipSymbol: "WH-1"}},
		[]depot.Element{{Waypoint: convSource, ShipSymbol: "ST-1"}},
		[]depot.Element{{Waypoint: convHub, ShipSymbol: "DL-1"}})
	seedWarehouseContainer(t, db, playerID, "wh-container-fx", "WH-1", convHub, map[string]int{"IRON": 45})

	return playerID
}

// seedUncoveredCapitalWorld builds demand at an UNCOVERED hub — a paying IRON
// contract, but NO depot stands the hub up, so topology has zero clusters. The
// real sensor surfaces no reuse-eligible IdleHulls, so the ladder cannot close
// the shortfall for free and escalates to ONE tier-4 add_cluster (1 warehouse +
// 1 stocker + 1 worker, HullDelta 3, estimated cost 3 x 400000).
func seedUncoveredCapitalWorld(t *testing.T, db *gorm.DB) int {
	t.Helper()
	playerID := seedPlayer(t, db, "AGENT-CAPITAL")

	seedContract(t, db, playerID, "cap-1",
		[]contract.Delivery{{TradeSymbol: "IRON", DestinationSymbol: capitalHub, UnitsRequired: 30, UnitsFulfilled: 30}},
		10000, 30000, t0)
	seedWaypoint(t, db, capitalHub, "X1-CP88", 0, 0)
	seedWaypoint(t, db, capitalSource, "X1-CP88", 30, 40)
	seedMarketSelling(t, db, playerID, capitalSource, "IRON")

	return playerID
}

// seedReusableIdleWorld is seedUncoveredCapitalWorld PLUS three idle,
// undedicated, non-cluster hulls waiting in-system. The planner still wants the
// uncovered hub stood up at 1 warehouse + 1 stocker + 1 worker — idle hulls add
// no cluster (coverage unchanged) and no income (FleetPerHullCrHr stays 0, so the
// add gate is unchanged) — but now the ladder can REUSE the three free hulls
// (tier-1) instead of escalating the whole gap to a tier-4 add_cluster. This is
// the exact st-780 regression: without the SENSE lane filling IdleHulls, these
// free hulls are invisible and every role escalates to capital.
func seedReusableIdleWorld(t *testing.T, db *gorm.DB) int {
	t.Helper()
	playerID := seedUncoveredCapitalWorld(t, db)
	seedIdleHull(t, db, playerID, "IDLE-1", capitalDock, "X1-CP88")
	seedIdleHull(t, db, playerID, "IDLE-2", capitalDock, "X1-CP88")
	seedIdleHull(t, db, playerID, "IDLE-3", capitalDock, "X1-CP88")
	return playerID
}
