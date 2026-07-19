package commands

// Behavioral tests for the capacity-reconciler loop scaffold. Every
// test drives the coordinator through its driving port (the command handler)
// and asserts observable outcomes: the TickOutcome stream, the state of the
// driven-port spies (actuator, proposal channel), and the injected clock.
// Nothing here reaches into loop internals.
//
// Test budget: 19 distinct behaviors × 2 = 38 max; 20 test functions below
// (the 19th behavior — DryRun observe-only CONVERGE — is one parametrized test
// with an observe case and its armed regression counterpart).

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- fakes at the port boundaries -------------------------------------------

// phaseLog records which phase components the loop invoked, in order.
type phaseLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *phaseLog) note(phase string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, phase)
}

func (l *phaseLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

type fakeSensor struct {
	log     *phaseLog
	signals capacity.Signals
	errAt   int // 1-based call number that fails (0 = never)
	calls   int
}

func (f *fakeSensor) Sense(_ context.Context, playerID int) (capacity.Signals, error) {
	f.calls++
	if f.log != nil {
		f.log.note("SENSE")
	}
	if f.errAt != 0 && f.calls == f.errAt {
		return capacity.Signals{}, errors.New("sense boom")
	}
	s := f.signals
	s.PlayerID = playerID
	return s, nil
}

type fakePlanner struct {
	log     *phaseLog
	desired capacity.DesiredTopology
	errAt   int
	calls   int

	gotSignals []capacity.Signals
	gotCals    []capacity.Calibration
}

func (f *fakePlanner) ComputeDesired(_ context.Context, signals capacity.Signals, cal capacity.Calibration) (capacity.DesiredTopology, error) {
	f.calls++
	if f.log != nil {
		f.log.note("PLAN")
	}
	f.gotSignals = append(f.gotSignals, signals)
	f.gotCals = append(f.gotCals, cal)
	if f.errAt != 0 && f.calls == f.errAt {
		return capacity.DesiredTopology{}, errors.New("plan boom")
	}
	return f.desired, nil
}

type fakeDiffer struct {
	log     *phaseLog
	actions []capacity.Action
	errAt   int
	calls   int

	gotDesired []capacity.DesiredTopology
	gotActual  []capacity.TopologySignals
}

func (f *fakeDiffer) Diff(_ context.Context, desired capacity.DesiredTopology, actual capacity.TopologySignals, _ capacity.Calibration) ([]capacity.Action, error) {
	f.calls++
	if f.log != nil {
		f.log.note("DIFF")
	}
	f.gotDesired = append(f.gotDesired, desired)
	f.gotActual = append(f.gotActual, actual)
	if f.errAt != 0 && f.calls == f.errAt {
		return nil, errors.New("diff boom")
	}
	return f.actions, nil
}

type fakeGovernor struct {
	log    *phaseLog
	result capacity.GovernResult
	errAt  int
	calls  int

	gotActions [][]capacity.Action
}

func (f *fakeGovernor) Govern(_ context.Context, actions []capacity.Action, _ capacity.EconomicsSignals, _ capacity.Calibration) (capacity.GovernResult, error) {
	f.calls++
	if f.log != nil {
		f.log.note("GOVERN")
	}
	f.gotActions = append(f.gotActions, actions)
	if f.errAt != 0 && f.calls == f.errAt {
		return capacity.GovernResult{}, errors.New("govern boom")
	}
	return f.result, nil
}

// spyActuator records every verb invocation; failVerb makes that one verb error.
type spyActuator struct {
	mu       sync.Mutex
	byVerb   map[capacity.ActionVerb][]capacity.Action
	failVerb capacity.ActionVerb
}

func newSpyActuator() *spyActuator {
	return &spyActuator{byVerb: map[capacity.ActionVerb][]capacity.Action{}}
}

func (s *spyActuator) record(action capacity.Action) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byVerb[action.Verb] = append(s.byVerb[action.Verb], action)
	if s.failVerb != "" && action.Verb == s.failVerb {
		return errors.New("actuator boom")
	}
	return nil
}

func (s *spyActuator) ReuseIdleHull(_ context.Context, a capacity.Action) error  { return s.record(a) }
func (s *spyActuator) Rebalance(_ context.Context, a capacity.Action) error      { return s.record(a) }
func (s *spyActuator) AdjustBuffer(_ context.Context, a capacity.Action) error   { return s.record(a) }
func (s *spyActuator) ExecuteCapital(_ context.Context, a capacity.Action) error { return s.record(a) }

func (s *spyActuator) calls(verb capacity.ActionVerb) []capacity.Action {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]capacity.Action(nil), s.byVerb[verb]...)
}

func (s *spyActuator) totalCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, actions := range s.byVerb {
		n += len(actions)
	}
	return n
}

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

type fakeKillSwitch struct {
	mu      sync.Mutex
	engaged bool
}

func (k *fakeKillSwitch) Disabled() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.engaged
}

func (k *fakeKillSwitch) set(engaged bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.engaged = engaged
}

// collectingObserver gathers outcomes and cancels the run's context from
// INSIDE ObserveTick once stopAt outcomes arrived — the loop then exits at
// its next context check without starting another tick, which makes every
// test fully deterministic (no sleeps, no tick races).
type collectingObserver struct {
	mu       sync.Mutex
	outcomes []capacity.TickOutcome
	stopAt   int
	cancel   context.CancelFunc
	onTick   func(out capacity.TickOutcome) // optional per-tick hook (runs before the stop check)
}

func (o *collectingObserver) ObserveTick(out capacity.TickOutcome) {
	o.mu.Lock()
	o.outcomes = append(o.outcomes, out)
	n := len(o.outcomes)
	o.mu.Unlock()
	if o.onTick != nil {
		o.onTick(out)
	}
	if n >= o.stopAt {
		o.cancel()
	}
}

func (o *collectingObserver) snapshot() []capacity.TickOutcome {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]capacity.TickOutcome(nil), o.outcomes...)
}

// ---- fixture ----------------------------------------------------------------

type loopFixture struct {
	log       *phaseLog
	sensor    *fakeSensor
	planner   *fakePlanner
	differ    *fakeDiffer
	governor  *fakeGovernor
	actuator  *spyActuator
	proposals *spyProposals
	kill      *fakeKillSwitch
	clock     *shared.MockClock
}

func newLoopFixture() *loopFixture {
	log := &phaseLog{}
	return &loopFixture{
		log:       log,
		sensor:    &fakeSensor{log: log},
		planner:   &fakePlanner{log: log},
		differ:    &fakeDiffer{log: log},
		governor:  &fakeGovernor{log: log},
		actuator:  newSpyActuator(),
		proposals: &spyProposals{},
		kill:      &fakeKillSwitch{},
		clock:     &shared.MockClock{CurrentTime: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)},
	}
}

func (f *loopFixture) handler() *RunCapacityReconcilerCoordinatorHandler {
	return NewRunCapacityReconcilerCoordinatorHandler(
		capacity.NewStaticDomain(capacity.ContractDeliveryDomainName, f.sensor, f.planner),
		f.differ,
		f.governor,
		f.actuator,
		f.proposals,
		f.kill,
		f.clock,
	)
}

func reconcilerCmd() *RunCapacityReconcilerCoordinatorCommand {
	return &RunCapacityReconcilerCoordinatorCommand{
		PlayerID:         shared.MustNewPlayerID(1),
		ContainerID:      "capacity-reconciler-1",
		TickIntervalSecs: 45,
	}
}

// runTicks drives Handle until the observer collected `ticks` outcomes, then
// waits for the loop to exit. It fails the test if the loop wedges.
func runTicks(t *testing.T, h *RunCapacityReconcilerCoordinatorHandler, cmd *RunCapacityReconcilerCoordinatorCommand, ticks int, onTick func(capacity.TickOutcome)) []capacity.TickOutcome {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	obs := &collectingObserver{stopAt: ticks, cancel: cancel, onTick: onTick}
	h.SetTickObserver(obs)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = h.Handle(ctx, cmd)
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

// ---- behaviors ---------------------------------------------------------------

// Behavior: each tick runs SENSE → PLAN → DIFF → GOVERN in order, and each
// phase receives the previous phase's output (the loop's whole job is this
// plumbing).
func TestCapacityReconciler_RunsPhasesInOrderAndPropagatesData(t *testing.T) {
	f := newLoopFixture()
	f.sensor.signals = capacity.Signals{
		Topology: capacity.TopologySignals{Clusters: []capacity.ClusterState{{HubSymbol: "X1-HUB-A"}}},
	}
	f.planner.desired = capacity.DesiredTopology{Hubs: []capacity.DesiredHub{{HubSymbol: "X1-HUB-B"}}}
	f.differ.actions = []capacity.Action{{Tier: capacity.TierReuseIdle, Verb: capacity.VerbReassignHull, ShipSymbol: "SHIP-1"}}

	runTicks(t, f.handler(), reconcilerCmd(), 1, nil)

	require.Equal(t, []string{"SENSE", "PLAN", "DIFF", "GOVERN"}, f.log.snapshot())
	// PLAN saw what SENSE collected (with the player stamped on).
	require.Len(t, f.planner.gotSignals, 1)
	require.Equal(t, 1, f.planner.gotSignals[0].PlayerID)
	require.Equal(t, "X1-HUB-A", f.planner.gotSignals[0].Topology.Clusters[0].HubSymbol)
	// DIFF compared PLAN's desired against SENSE's actual topology.
	require.Len(t, f.differ.gotDesired, 1)
	require.Equal(t, "X1-HUB-B", f.differ.gotDesired[0].Hubs[0].HubSymbol)
	require.Equal(t, "X1-HUB-A", f.differ.gotActual[0].Clusters[0].HubSymbol)
	// GOVERN judged DIFF's actions.
	require.Len(t, f.governor.gotActions, 1)
	require.Equal(t, f.differ.actions, f.governor.gotActions[0])
}

// Behavior: the loop ticks on the configured schedule (and on the documented
// 300s default when unset), measured on the injected clock.
func TestCapacityReconciler_TicksOnSchedule(t *testing.T) {
	cases := []struct {
		name         string
		tickSecs     int
		wantInterval time.Duration
	}{
		{name: "configured 45s", tickSecs: 45, wantInterval: 45 * time.Second},
		{name: "default 300s when unset", tickSecs: 0, wantInterval: 300 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoopFixture()
			start := f.clock.CurrentTime
			cmd := reconcilerCmd()
			cmd.TickIntervalSecs = tc.tickSecs

			outcomes := runTicks(t, f.handler(), cmd, 3, nil)

			for i, out := range outcomes {
				require.Equal(t, i+1, out.Sequence)
				require.Equal(t, start.Add(time.Duration(i)*tc.wantInterval), out.At,
					"tick %d must fire one interval after the previous", i+1)
			}
		})
	}
}

// Behavior: with the kill switch engaged, a tick idles — NO phase component
// is invoked, and the outcome says so. Idle ticks still pace the tick
// interval on the clock (no busy-spin while DISABLED).
func TestCapacityReconciler_KillSwitchIdlesTickWithoutInvokingPhases(t *testing.T) {
	f := newLoopFixture()
	f.kill.set(true)
	start := f.clock.CurrentTime

	outcomes := runTicks(t, f.handler(), reconcilerCmd(), 2, nil)

	require.Empty(t, f.log.snapshot(), "no phase may run while captain/DISABLED is present")
	for _, out := range outcomes {
		require.True(t, out.Idle)
		require.Empty(t, out.ActionsExecuted)
		require.Empty(t, out.ProposalsFiled)
	}
	require.Equal(t, start.Add(45*time.Second), outcomes[1].At,
		"an idle tick must still sleep the interval — never busy-spin while DISABLED")
	require.Zero(t, f.actuator.totalCalls())
	require.Empty(t, f.proposals.all())
}

// Behavior: the switch is re-read EVERY tick, not just at startup — releasing
// it mid-run resumes reconciling on the very next tick.
func TestCapacityReconciler_KillSwitchReleaseResumesNextTick(t *testing.T) {
	f := newLoopFixture()
	f.kill.set(true)

	outcomes := runTicks(t, f.handler(), reconcilerCmd(), 2, func(out capacity.TickOutcome) {
		if out.Sequence == 1 {
			f.kill.set(false) // Admiral clears DISABLED between ticks.
		}
	})

	require.True(t, outcomes[0].Idle, "tick 1 must idle under the engaged switch")
	require.False(t, outcomes[1].Idle, "tick 2 must reconcile after the switch is cleared")
	require.Equal(t, []string{"SENSE", "PLAN", "DIFF", "GOVERN"}, f.log.snapshot())
}

// Behavior (the operative safety direction): the Admiral drops
// captain/DISABLED while the engine is ACTIVELY reconciling — the very next
// tick must idle with zero phase invocations. Pins the per-tick re-read
// against a "latch enabled after first active tick" regression.
func TestCapacityReconciler_KillSwitchEngageMidRunIdlesNextTick(t *testing.T) {
	f := newLoopFixture()

	outcomes := runTicks(t, f.handler(), reconcilerCmd(), 2, func(out capacity.TickOutcome) {
		if out.Sequence == 1 {
			f.kill.set(true) // Admiral drops DISABLED while the engine is live.
		}
	})

	require.False(t, outcomes[0].Idle, "tick 1 must reconcile with the switch clear")
	require.True(t, outcomes[1].Idle, "tick 2 must idle after the switch engages mid-run")
	require.Equal(t, []string{"SENSE", "PLAN", "DIFF", "GOVERN"}, f.log.snapshot(),
		"the phase log must not grow on the engaged tick")
}

// Behavior (contract line: nil killSwitch ⇒ fail-closed): a handler built
// WITHOUT a kill switch runs but idles every tick — a mis-wired engine never
// reconciles unsupervised, and launch does not fail (validateWiring
// deliberately excludes the switch).
func TestCapacityReconciler_NilKillSwitchFailsClosed(t *testing.T) {
	f := newLoopFixture()
	h := NewRunCapacityReconcilerCoordinatorHandler(
		capacity.NewStaticDomain(capacity.ContractDeliveryDomainName, f.sensor, f.planner),
		f.differ,
		f.governor,
		f.actuator,
		f.proposals,
		nil, // the mis-wiring under test
		f.clock,
	)

	outcomes := runTicks(t, h, reconcilerCmd(), 2, nil)

	require.Empty(t, f.log.snapshot(), "a nil switch must fail CLOSED: no phase may ever run")
	for _, out := range outcomes {
		require.True(t, out.Idle)
	}
}

// Behavior: cancellation interrupts a REAL-clock sleep — `container stop` /
// daemon shutdown must not hang up to a full tick interval. The fixture's
// 45s interval on the real clock would trip runTicks' 5s wedge guard if the
// loop's sleep stopped honoring ctx.Done.
func TestCapacityReconciler_CancellationInterruptsRealClockSleep(t *testing.T) {
	f := newLoopFixture()
	h := NewRunCapacityReconcilerCoordinatorHandler(
		capacity.NewStaticDomain(capacity.ContractDeliveryDomainName, f.sensor, f.planner),
		f.differ,
		f.governor,
		f.actuator,
		f.proposals,
		f.kill,
		nil, // nil clock ⇒ the production real clock
	)

	outcomes := runTicks(t, h, reconcilerCmd(), 1, nil)

	require.False(t, outcomes[0].Idle, "tick 1 must reconcile; the run must then exit promptly mid-sleep")
}

// Behavior (acceptance): the fully-wired no-op chain provably emits ZERO
// actions end-to-end — every phase runs, nothing is executed, nothing is
// proposed.
func TestCapacityReconciler_NoOpPlannerEmitsZeroActionsEndToEnd(t *testing.T) {
	actuator := newSpyActuator()
	proposals := &spyProposals{}
	h := NewRunCapacityReconcilerCoordinatorHandler(
		capacity.NewStaticDomain(capacity.ContractDeliveryDomainName, capacity.NoOpSensor{}, capacity.NoOpPlanner{}),
		capacity.NoOpDiffer{},
		capacity.NoOpGovernor{},
		actuator,
		proposals,
		&fakeKillSwitch{},
		&shared.MockClock{CurrentTime: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)},
	)

	outcomes := runTicks(t, h, reconcilerCmd(), 3, nil)

	for _, out := range outcomes {
		require.False(t, out.Idle)
		require.Empty(t, out.FailedPhase, "no phase may fail on the no-op chain")
		require.Empty(t, out.ActionsExecuted)
		require.Empty(t, out.ProposalsFiled)
	}
	require.Zero(t, actuator.totalCalls(), "no actuator verb may fire on the no-op chain")
	require.Empty(t, proposals.all(), "no proposal may be filed on the no-op chain")
}

// Behavior: the loop is idempotent — two consecutive ticks over unchanged
// inputs produce identical zero-action outcomes (modulo sequence/time).
func TestCapacityReconciler_ConsecutiveTicksIdenticalOverUnchangedInputs(t *testing.T) {
	f := newLoopFixture()
	f.sensor.signals = capacity.Signals{
		Topology: capacity.TopologySignals{Clusters: []capacity.ClusterState{{HubSymbol: "X1-HUB-A"}}},
	}

	outcomes := runTicks(t, f.handler(), reconcilerCmd(), 2, nil)

	first, second := outcomes[0], outcomes[1]
	// Neutralize the only fields allowed to differ.
	second.Sequence = first.Sequence
	second.At = first.At
	require.Equal(t, first, second, "unchanged inputs must reproduce the identical outcome")
	require.Empty(t, first.ActionsExecuted)
	require.Empty(t, first.ProposalsFiled)
}

// Behavior: a failing phase skips the rest of THAT tick (downstream phases do
// not run) but never wedges the loop — the next tick runs clean.
func TestCapacityReconciler_FailingPhaseSkipsRestOfTickWithoutWedgingLoop(t *testing.T) {
	cases := []struct {
		name       string
		arrange    func(f *loopFixture)
		wantPhase  capacity.Phase
		wantTick1  []string // phases invoked during the failing tick
		wantErrSub string
	}{
		{
			name:       "SENSE fails",
			arrange:    func(f *loopFixture) { f.sensor.errAt = 1 },
			wantPhase:  capacity.PhaseSense,
			wantTick1:  []string{"SENSE"},
			wantErrSub: "sense boom",
		},
		{
			name:       "PLAN fails",
			arrange:    func(f *loopFixture) { f.planner.errAt = 1 },
			wantPhase:  capacity.PhasePlan,
			wantTick1:  []string{"SENSE", "PLAN"},
			wantErrSub: "plan boom",
		},
		{
			name:       "DIFF fails",
			arrange:    func(f *loopFixture) { f.differ.errAt = 1 },
			wantPhase:  capacity.PhaseDiff,
			wantTick1:  []string{"SENSE", "PLAN", "DIFF"},
			wantErrSub: "diff boom",
		},
		{
			name:       "GOVERN fails",
			arrange:    func(f *loopFixture) { f.governor.errAt = 1 },
			wantPhase:  capacity.PhaseGovern,
			wantTick1:  []string{"SENSE", "PLAN", "DIFF", "GOVERN"},
			wantErrSub: "govern boom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoopFixture()
			tc.arrange(f)

			outcomes := runTicks(t, f.handler(), reconcilerCmd(), 2, nil)

			require.Equal(t, tc.wantPhase, outcomes[0].FailedPhase)
			require.Contains(t, outcomes[0].Error, tc.wantErrSub)
			require.Empty(t, outcomes[0].ActionsExecuted, "a failed tick must execute nothing")
			require.Equal(t, tc.wantTick1, f.log.snapshot()[:len(tc.wantTick1)],
				"the failing tick must stop at the failing phase")
			// The loop recovered: tick 2 ran every phase cleanly.
			require.Empty(t, outcomes[1].FailedPhase)
			require.Equal(t, append(tc.wantTick1, "SENSE", "PLAN", "DIFF", "GOVERN"), f.log.snapshot())
		})
	}
}

// Behavior: CONVERGE dispatches each approved cheap-tier action to the
// actuator verb matching its tier, and files each capital proposal on the
// channel — and the outcome reports exactly what happened. Under the v1
// default calibration (approval threshold 0) capital flows ONLY via
// Proposals, never via Approved (safety invariant 4).
func TestCapacityReconciler_ConvergeDispatchesApprovedByTierAndFilesProposals(t *testing.T) {
	f := newLoopFixture()
	reuse := capacity.Action{Tier: capacity.TierReuseIdle, Verb: capacity.VerbReassignHull, ShipSymbol: "SHIP-1"}
	rebalance := capacity.Action{Tier: capacity.TierRebalance, Verb: capacity.VerbRepositionHull, ShipSymbol: "SHIP-2", TargetWaypoint: "X1-HUB-A"}
	buffer := capacity.Action{Tier: capacity.TierBufferAdjust, Verb: capacity.VerbAdjustBufferCap, HubSymbol: "X1-HUB-A", Good: "IRON", UnitsCap: 80}
	capital := capacity.Action{Tier: capacity.TierCapital, Verb: capacity.VerbBuyHull, EstimatedCostCredits: 120000}
	proposal := capacity.Proposal{ID: "prop-1", PlayerID: 1, Action: capital}
	f.governor.result = capacity.GovernResult{
		Approved:  []capacity.Action{reuse, rebalance, buffer},
		Proposals: []capacity.Proposal{proposal},
	}

	outcomes := runTicks(t, f.handler(), reconcilerCmd(), 1, nil)

	require.Equal(t, []capacity.Action{reuse}, f.actuator.calls(capacity.VerbReassignHull))
	require.Equal(t, []capacity.Action{rebalance}, f.actuator.calls(capacity.VerbRepositionHull))
	require.Equal(t, []capacity.Action{buffer}, f.actuator.calls(capacity.VerbAdjustBufferCap))
	require.Empty(t, f.actuator.calls(capacity.VerbBuyHull),
		"capital must never reach the actuator through the loop under the v1 threshold")
	require.Equal(t, []capacity.Proposal{proposal}, f.proposals.all())
	require.Empty(t, outcomes[0].FailedPhase)
	require.Equal(t, []capacity.Action{reuse, rebalance, buffer}, outcomes[0].ActionsExecuted)
	require.Equal(t, []capacity.Proposal{proposal}, outcomes[0].ProposalsFiled,
		"an already-attributed proposal passes verbatim (PlayerID untouched)")
}

// Behavior: DryRun makes CONVERGE observe-only. The SAME scenario that, armed,
// executes tiers 1-3 and files a tier-4 proposal instead touches NO actuator
// verb and NEVER calls ProposalChannel.Submit — yet the outcome still exposes
// the FULL planned set (WouldExecute / WouldFile) so a captain can watch what
// the engine WOULD do before arming it. The armed case is the byte-identical
// regression proof in the other direction: dry-run adds observability, it does
// not change what an armed engine does.
func TestCapacityReconciler_DryRunObservesWithoutActuatingElseArmsFully(t *testing.T) {
	reuse := capacity.Action{Tier: capacity.TierReuseIdle, Verb: capacity.VerbReassignHull, ShipSymbol: "SHIP-1"}
	rebalance := capacity.Action{Tier: capacity.TierRebalance, Verb: capacity.VerbRepositionHull, ShipSymbol: "SHIP-2", TargetWaypoint: "X1-HUB-A"}
	buffer := capacity.Action{Tier: capacity.TierBufferAdjust, Verb: capacity.VerbAdjustBufferCap, HubSymbol: "X1-HUB-A", Good: "IRON", UnitsCap: 80}
	capital := capacity.Action{Tier: capacity.TierCapital, Verb: capacity.VerbBuyHull, HubSymbol: "X1-HUB-B", EstimatedCostCredits: 120000}
	proposal := capacity.Proposal{ID: "prop-1", PlayerID: 1, Action: capital}
	governed := capacity.GovernResult{
		Approved:  []capacity.Action{reuse, rebalance, buffer},
		Proposals: []capacity.Proposal{proposal},
	}

	t.Run("dry run observes the whole plan but actuates nothing", func(t *testing.T) {
		f := newLoopFixture()
		f.governor.result = governed
		cmd := reconcilerCmd()
		cmd.DryRun = true

		outcomes := runTicks(t, f.handler(), cmd, 1, nil)

		// Not one side effect escaped the process.
		require.Zero(t, f.actuator.totalCalls(), "DryRun must invoke NO actuator verb — for ANY tier")
		require.Empty(t, f.proposals.all(), "DryRun must NEVER call ProposalChannel.Submit")
		require.Empty(t, outcomes[0].ActionsExecuted, "DryRun executes nothing")
		require.Empty(t, outcomes[0].ProposalsFiled, "DryRun files nothing")
		require.Empty(t, outcomes[0].FailedPhase, "observing is not failing")
		// ...but the observer sees exactly what it WOULD have done.
		require.Equal(t, []capacity.Action{reuse, rebalance, buffer}, outcomes[0].WouldExecute,
			"the observer must see every approved action the engine WOULD have executed")
		require.Equal(t, []capacity.Proposal{proposal}, outcomes[0].WouldFile,
			"the observer must see every proposal the engine WOULD have filed")
	})

	t.Run("armed executes tiers 1-3 and files the proposal (regression)", func(t *testing.T) {
		f := newLoopFixture()
		f.governor.result = governed
		cmd := reconcilerCmd()
		cmd.DryRun = false

		outcomes := runTicks(t, f.handler(), cmd, 1, nil)

		require.Equal(t, []capacity.Action{reuse}, f.actuator.calls(capacity.VerbReassignHull))
		require.Equal(t, []capacity.Action{rebalance}, f.actuator.calls(capacity.VerbRepositionHull))
		require.Equal(t, []capacity.Action{buffer}, f.actuator.calls(capacity.VerbAdjustBufferCap))
		require.Equal(t, []capacity.Proposal{proposal}, f.proposals.all())
		require.Equal(t, []capacity.Action{reuse, rebalance, buffer}, outcomes[0].ActionsExecuted)
		require.Equal(t, []capacity.Proposal{proposal}, outcomes[0].ProposalsFiled)
		// The armed engine never populates the observe-only fields.
		require.Empty(t, outcomes[0].WouldExecute, "an armed tick executes — it does not merely 'would'")
		require.Empty(t, outcomes[0].WouldFile)
	})
}

// Behavior (safety invariant 4, structural backstop): an Approved tier-4
// action costing at least the approval threshold is REFUSED by CONVERGE —
// recorded as a converge failure, never executed — regardless of the governor
// having (wrongly) auto-approved it. Below a raised threshold, graduated
// auto-approval passes.
func TestCapacityReconciler_ConvergeRefusesUnapprovedCapital(t *testing.T) {
	cases := []struct {
		name              string
		approvalThreshold int64
		capitalCost       int64
		wantExecuted      bool
	}{
		{name: "v1 default threshold 0 refuses ALL capital in Approved", approvalThreshold: 0, capitalCost: 120000, wantExecuted: false},
		{name: "raised threshold refuses at-threshold capital", approvalThreshold: 250000, capitalCost: 250000, wantExecuted: false},
		{name: "raised threshold passes graduated below-threshold capital", approvalThreshold: 250000, capitalCost: 100000, wantExecuted: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoopFixture()
			cheap := capacity.Action{Tier: capacity.TierReuseIdle, Verb: capacity.VerbReassignHull, ShipSymbol: "SHIP-1"}
			capital := capacity.Action{Tier: capacity.TierCapital, Verb: capacity.VerbBuyHull, EstimatedCostCredits: tc.capitalCost}
			f.governor.result = capacity.GovernResult{Approved: []capacity.Action{cheap, capital}}
			cmd := reconcilerCmd()
			cmd.ApprovalThresholdCredits = tc.approvalThreshold

			outcomes := runTicks(t, f.handler(), cmd, 1, nil)

			require.Equal(t, []capacity.Action{cheap}, f.actuator.calls(capacity.VerbReassignHull),
				"the cheap action must execute either way")
			if tc.wantExecuted {
				require.Equal(t, []capacity.Action{capital}, f.actuator.calls(capacity.VerbBuyHull))
				require.Empty(t, outcomes[0].FailedPhase)
				require.Equal(t, []capacity.Action{cheap, capital}, outcomes[0].ActionsExecuted)
				return
			}
			require.Empty(t, f.actuator.calls(capacity.VerbBuyHull),
				"unapproved capital must never reach ExecuteCapital")
			require.Equal(t, capacity.PhaseConverge, outcomes[0].FailedPhase,
				"the governor contradicting its own gate must be LOUD")
			require.Contains(t, outcomes[0].Error, "unapproved capital refused")
			require.Equal(t, []capacity.Action{cheap}, outcomes[0].ActionsExecuted)
		})
	}
}

// Behavior: dispatch verifies the documented verb → tier mapping — a
// mislabeled tier (buy_hull claiming tier-2) is refused, never executed, so
// tier mislabeling cannot bypass the capital gate as a "free" cheap action.
func TestCapacityReconciler_ConvergeRefusesVerbTierMismatch(t *testing.T) {
	f := newLoopFixture()
	mislabeled := capacity.Action{Tier: capacity.TierRebalance, Verb: capacity.VerbBuyHull, EstimatedCostCredits: 120000}
	f.governor.result = capacity.GovernResult{Approved: []capacity.Action{mislabeled}}

	outcomes := runTicks(t, f.handler(), reconcilerCmd(), 1, nil)

	require.Zero(t, f.actuator.totalCalls(), "a verb/tier-mismatched action must reach NO actuator verb")
	require.Equal(t, capacity.PhaseConverge, outcomes[0].FailedPhase)
	require.Contains(t, outcomes[0].Error, "verb/tier mismatch")
	require.Empty(t, outcomes[0].ActionsExecuted)
}

// Behavior: a proposal the governor left unattributed (PlayerID zero — its
// Govern inputs carry no player identity) is stamped with the reconciling
// player's ID before Submit, so downstream filers never see PlayerID=0.
func TestCapacityReconciler_ConvergeStampsPlayerOnUnattributedProposals(t *testing.T) {
	f := newLoopFixture()
	capital := capacity.Action{Tier: capacity.TierCapital, Verb: capacity.VerbBuyHull, EstimatedCostCredits: 120000}
	f.governor.result = capacity.GovernResult{
		Proposals: []capacity.Proposal{{ID: "prop-1", Action: capital}}, // PlayerID zero: the governor cannot know it
	}

	outcomes := runTicks(t, f.handler(), reconcilerCmd(), 1, nil)

	filed := f.proposals.all()
	require.Len(t, filed, 1)
	require.Equal(t, 1, filed[0].PlayerID,
		"the loop must stamp the reconciling player on an unattributed proposal before Submit")
	require.Equal(t, 1, outcomes[0].ProposalsFiled[0].PlayerID)
	require.Empty(t, outcomes[0].FailedPhase)
}

// Behavior: one failing action does not abort CONVERGE — the rest still
// execute, the failure is reported, and the loop continues.
func TestCapacityReconciler_ConvergeActionFailureIsIsolated(t *testing.T) {
	f := newLoopFixture()
	reuse := capacity.Action{Tier: capacity.TierReuseIdle, Verb: capacity.VerbReassignHull, ShipSymbol: "SHIP-1"}
	rebalance := capacity.Action{Tier: capacity.TierRebalance, Verb: capacity.VerbRepositionHull, ShipSymbol: "SHIP-2"}
	f.governor.result = capacity.GovernResult{Approved: []capacity.Action{reuse, rebalance}}
	f.actuator.failVerb = capacity.VerbReassignHull

	outcomes := runTicks(t, f.handler(), reconcilerCmd(), 2, nil)

	require.Equal(t, capacity.PhaseConverge, outcomes[0].FailedPhase)
	require.Contains(t, outcomes[0].Error, "actuator boom")
	require.Equal(t, []capacity.Action{rebalance}, outcomes[0].ActionsExecuted,
		"the non-failing action must still execute")
	// The loop is not wedged: tick 2 happened (and re-attempted the same plan
	// — statelessness means the failed action simply reappears).
	require.Equal(t, 2, outcomes[1].Sequence)
	require.Len(t, f.actuator.calls(capacity.VerbReassignHull), 2)
}

// Behavior: a zero-valued launch config resolves to the documented protective
// defaults — the spec's calibration set, per-decision cap 25% included.
func TestCapacityReconciler_CalibrationDefaultsResolvedFromZeroConfig(t *testing.T) {
	f := newLoopFixture()
	cmd := reconcilerCmd()
	cmd.TickIntervalSecs = 0 // everything unset → defaults

	runTicks(t, f.handler(), cmd, 1, nil)

	require.Len(t, f.planner.gotCals, 1)
	cal := f.planner.gotCals[0]
	// Spec-traced literals (2026-07-15 design spec, Calibration params) — not
	// echoes of the production defaults.
	require.Equal(t, int64(50000), cal.ReserveFloorCredits, "reserve floor defaults to the immutable 50k floor")
	require.Equal(t, 0.25, cal.SurplusFraction)
	require.Equal(t, 25, cal.PerDecisionCapPct, "per-decision cap defaults to 25%")
	require.Equal(t, 24*time.Hour, cal.ROIPaybackHorizon)
	require.Equal(t, float64(0), cal.AddThresholdPerHullCrHr)
	require.Equal(t, 0, cal.StockerCapacityBudget)
	require.Equal(t, 300*time.Second, cal.TickInterval)
	require.Equal(t, int64(0), cal.ApprovalThresholdCredits, "v1: every capital action needs approval")
}

// Behavior: explicit launch-config values override the defaults.
func TestCapacityReconciler_CalibrationExplicitValuesOverrideDefaults(t *testing.T) {
	f := newLoopFixture()
	cmd := reconcilerCmd()
	cmd.TickIntervalSecs = 60
	cmd.ReserveFloorCredits = 400000
	cmd.SurplusFraction = 0.1
	cmd.PerDecisionCapPct = 10
	cmd.ROIPaybackHorizonHours = 6
	cmd.AddThresholdPerHullCrHr = 1500
	cmd.StockerCapacityBudget = 240
	cmd.ApprovalThresholdCredits = 250000

	runTicks(t, f.handler(), cmd, 1, nil)

	require.Len(t, f.planner.gotCals, 1)
	cal := f.planner.gotCals[0]
	require.Equal(t, int64(400000), cal.ReserveFloorCredits)
	require.Equal(t, 0.1, cal.SurplusFraction)
	require.Equal(t, 10, cal.PerDecisionCapPct)
	require.Equal(t, 6*time.Hour, cal.ROIPaybackHorizon)
	require.Equal(t, float64(1500), cal.AddThresholdPerHullCrHr)
	require.Equal(t, 240, cal.StockerCapacityBudget)
	require.Equal(t, 60*time.Second, cal.TickInterval)
	require.Equal(t, int64(250000), cal.ApprovalThresholdCredits)
}

// Behavior: an invalid explicit calibration fails the launch LOUDLY — the
// loop never starts, no phase runs.
func TestCapacityReconciler_InvalidCalibrationFailsLaunch(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(cmd *RunCapacityReconcilerCoordinatorCommand)
		wantSub string
	}{
		{"surplus fraction above 1", func(c *RunCapacityReconcilerCoordinatorCommand) { c.SurplusFraction = 1.5 }, "surplus_fraction"},
		{"surplus fraction negative", func(c *RunCapacityReconcilerCoordinatorCommand) { c.SurplusFraction = -0.1 }, "surplus_fraction"},
		{"per-decision cap above 100", func(c *RunCapacityReconcilerCoordinatorCommand) { c.PerDecisionCapPct = 101 }, "per_decision_cap_pct"},
		{"per-decision cap negative", func(c *RunCapacityReconcilerCoordinatorCommand) { c.PerDecisionCapPct = -5 }, "per_decision_cap_pct"},
		{"reserve floor negative", func(c *RunCapacityReconcilerCoordinatorCommand) { c.ReserveFloorCredits = -1 }, "reserve_floor"},
		{"payback horizon negative", func(c *RunCapacityReconcilerCoordinatorCommand) { c.ROIPaybackHorizonHours = -2 }, "roi_payback_horizon"},
		{"add threshold negative", func(c *RunCapacityReconcilerCoordinatorCommand) { c.AddThresholdPerHullCrHr = -0.5 }, "add_threshold_per_hull_cr_hr"},
		{"stocker budget negative", func(c *RunCapacityReconcilerCoordinatorCommand) { c.StockerCapacityBudget = -3 }, "stocker_capacity_budget"},
		{"tick interval negative", func(c *RunCapacityReconcilerCoordinatorCommand) { c.TickIntervalSecs = -10 }, "tick_interval"},
		{"approval threshold negative", func(c *RunCapacityReconcilerCoordinatorCommand) { c.ApprovalThresholdCredits = -7 }, "approval_threshold"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLoopFixture()
			cmd := reconcilerCmd()
			tc.mutate(cmd)

			_, err := f.handler().Handle(context.Background(), cmd)

			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantSub)
			require.Zero(t, f.sensor.calls, "an invalid calibration must never start the loop")
		})
	}
}

// Behavior: a handler missing any wired component refuses to run — a
// mis-assembled engine fails loud at launch, not dark at converge time.
func TestCapacityReconciler_UnwiredComponentsFailLaunch(t *testing.T) {
	f := newLoopFixture()
	h := NewRunCapacityReconcilerCoordinatorHandler(nil, f.differ, f.governor, f.actuator, f.proposals, f.kill, f.clock)

	_, err := h.Handle(context.Background(), reconcilerCmd())

	require.Error(t, err)
	require.Contains(t, err.Error(), "not wired")
	require.Zero(t, f.sensor.calls)
}
