package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// recordingStallObserver is a fake health.StallObserver: it records the verdict of every tick so
// the tests can assert what the coordinator REPORTED, without depending on the escalator's own
// streak arithmetic (which health/stall_test.go owns).
type recordingStallObserver struct {
	keys     []health.StallKey
	outcomes []health.TickOutcome
}

func (r *recordingStallObserver) Observe(_ context.Context, key health.StallKey, outcome health.TickOutcome) {
	r.keys = append(r.keys, key)
	r.outcomes = append(r.outcomes, outcome)
}

func (r *recordingStallObserver) forScope(scope string) []health.TickOutcome {
	var out []health.TickOutcome
	for i, k := range r.keys {
		if k.Scope == scope {
			out = append(out, r.outcomes[i])
		}
	}
	return out
}

// blockedHandler wires a coordinator whose buy path is complete EXCEPT that no yard price can be
// read, so every candidate is refused by the price_read guard — deterministic, fail-closed, and
// exactly the shape of the production stall (a guard blocking every tick, forever, silently).
func blockedHandler(providers ...ClassDemandProvider) (*RunFleetAutosizerCoordinatorHandler, *recordingStallObserver) {
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	for _, p := range providers {
		h.AddDemandProvider(p)
	}
	h.SetTreasuryReader(&fakeTreasury{credits: 5000000, ok: true})
	h.SetAPIUtilizationReader(&fakeAPIUtil{pct: 40, ok: true})
	h.SetHeavyCensusReader(&fakeHeavyCensus{owned: 0})
	h.SetYardPriceReader(&fakeYardPrice{ok: false}) // the yard ask is unreadable -> price_read BLOCKS
	h.SetPurchaser(&recordingPurchaser{})
	// The tap is what carries the FIRST FAILING GUARD out of the (concurrently-owned) act path
	// and into the tick's stall verdict. Production wires it the same way.
	h.SetMetricsSink(NewBlockedGuardTap(&recordingMetrics{}))
	obs := &recordingStallObserver{}
	h.SetStallObserver(obs)
	return h, obs
}

// THE PRODUCTION REGRESSION, at the coordinator's own driving port. The autosizer's heavy
// decision was BLOCKED on the same guard every tick for hours and reported it as an INFO line.
// Every tick must now report BLOCKED naming that guard, so the escalator can page.
func TestAutosizerReportsBlockedNamingTheFirstFailingGuard(t *testing.T) {
	h, obs := blockedHandler(lightShortfall())
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 5, ContainerID: "c1"}

	for i := 0; i < health.StallEscalationTicks; i++ {
		if _, err := h.reconcileOnce(context.Background(), cmd); err != nil {
			t.Fatalf("reconcileOnce error: %v", err)
		}
	}

	got := obs.forScope(string(HullClassLight))
	if len(got) != health.StallEscalationTicks {
		t.Fatalf("expected one stall verdict per tick for the light class, got %d: %+v", len(got), got)
	}
	for i, o := range got {
		if o.Outcome != health.StallBlocked {
			t.Fatalf("tick %d: outcome = %s, want BLOCKED (there was unmet demand and no hull was bought)", i, o.Outcome)
		}
		if o.Reason != health.StallReason(GuardPrice) {
			t.Fatalf("tick %d: reason = %q, want the first failing guard %q", i, o.Reason, GuardPrice)
		}
	}
	if k := obs.keys[0]; k.Coordinator != autosizerStallCoordinator || k.ContainerID != "c1" || k.PlayerID != 5 {
		t.Fatalf("stall key must identify the coordinator, container and player, got %+v", k)
	}
}

// THE OTHER HALF. An autosizer with no shortfall is CORRECTLY idle: it must report IDLE forever
// and never look like a stall, no matter how many ticks pass.
func TestAutosizerReportsIdleWhenThereIsNoShortfall(t *testing.T) {
	satisfied := &fakeDemandProvider{class: HullClassLight, demand: ClassDemand{Demand: 3, Current: 3, Readable: true}}
	h, obs := blockedHandler(satisfied)
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 5, ContainerID: "c1"}

	for i := 0; i < health.StallEscalationTicks*5; i++ {
		if _, err := h.reconcileOnce(context.Background(), cmd); err != nil {
			t.Fatalf("reconcileOnce error: %v", err)
		}
	}

	got := obs.forScope(string(HullClassLight))
	if len(got) == 0 {
		t.Fatalf("a satisfied class must still report its verdict every tick, got none")
	}
	for i, o := range got {
		if o.Outcome != health.StallIdle {
			t.Fatalf("tick %d: outcome = %s (reason %q), want IDLE — a satisfied class has nothing to do and that is correct", i, o.Outcome, o.Reason)
		}
	}
}

// A tick that BUYS reports PROGRESS, which is what clears any accumulated streak.
func TestAutosizerReportsProgressWhenItBuys(t *testing.T) {
	h, _, _, _ := armedHandler(lightShortfall())
	obs := &recordingStallObserver{}
	h.SetStallObserver(obs)

	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 5, ContainerID: "c1"}); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}

	got := obs.forScope(string(HullClassLight))
	if len(got) != 1 || got[0].Outcome != health.StallProgress {
		t.Fatalf("a tick that bought a hull must report PROGRESS, got %+v", got)
	}
}

// An unreadable or erroring demand read is a BLOCK, not an idle tick: the class had a signal it
// could not read, which is precisely "I cannot do anything" wearing "nothing to do"'s clothes —
// the confusion this whole layer exists to end. Each gets its own stable reason.
func TestAutosizerReportsBlockedOnUnreadableDemand(t *testing.T) {
	cases := []struct {
		name     string
		provider *fakeDemandProvider
		want     health.StallReason
	}{
		{
			name:     "provider infra error",
			provider: &fakeDemandProvider{class: HullClassLight, err: errors.New("db down")},
			want:     stallReasonDemandError,
		},
		{
			name:     "demand signal unreadable",
			provider: &fakeDemandProvider{class: HullClassLight, demand: ClassDemand{Demand: 9, Current: 0, Readable: false, Reason: "treasury unreadable"}},
			want:     stallReasonDemandUnreadable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, obs := blockedHandler(tc.provider)
			if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 5, ContainerID: "c1"}); err != nil {
				t.Fatalf("reconcileOnce error: %v", err)
			}
			got := obs.forScope(string(HullClassLight))
			if len(got) != 1 {
				t.Fatalf("expected exactly one verdict, got %+v", got)
			}
			if got[0].Outcome != health.StallBlocked || got[0].Reason != tc.want {
				t.Fatalf("outcome/reason = %s/%q, want BLOCKED/%q", got[0].Outcome, got[0].Reason, tc.want)
			}
		})
	}
}

// Classes stall INDEPENDENTLY: a blocked heavy must not be closed out by a healthy light, and the
// escalation must name the class that is actually stuck.
func TestAutosizerScopesTheStallVerdictPerClass(t *testing.T) {
	blockedLight := lightShortfall()
	satisfiedHeavy := &fakeDemandProvider{class: HullClassHeavy, demand: ClassDemand{Demand: 2, Current: 2, Readable: true}}
	h, obs := blockedHandler(blockedLight, satisfiedHeavy)

	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 5, ContainerID: "c1"}); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}

	light := obs.forScope(string(HullClassLight))
	heavy := obs.forScope(string(HullClassHeavy))
	if len(light) != 1 || light[0].Outcome != health.StallBlocked {
		t.Fatalf("the blocked light class must report BLOCKED, got %+v", light)
	}
	if len(heavy) != 1 || heavy[0].Outcome != health.StallIdle {
		t.Fatalf("the satisfied heavy class must report IDLE, got %+v", heavy)
	}
}

// An unwired observer (tests, or a boot before DI completes) must degrade to silence, never a
// panic: observability is not a precondition for sizing the fleet (RULINGS #4).
func TestAutosizerRunsWithNoStallObserverWired(t *testing.T) {
	h, _, _, _ := armedHandler(lightShortfall())
	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 5, ContainerID: "c1"}); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}
}

// The tap is a PASS-THROUGH decorator: every MetricsSink call still reaches the real sink, so
// wiring it can never cost the autosizer an existing observation series.
func TestBlockedGuardTapForwardsEveryRecordingToTheInnerSink(t *testing.T) {
	inner := &recordingMetrics{}
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	h.AddDemandProvider(lightShortfall())
	h.SetTreasuryReader(&fakeTreasury{credits: 5000000, ok: true})
	h.SetAPIUtilizationReader(&fakeAPIUtil{pct: 40, ok: true})
	h.SetHeavyCensusReader(&fakeHeavyCensus{owned: 0})
	h.SetYardPriceReader(&fakeYardPrice{price: 437000, cheapest: 400000, yard: "KA42-A2", ok: true})
	h.SetPurchaser(&recordingPurchaser{})
	h.SetMetricsSink(NewBlockedGuardTap(inner))

	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{PlayerID: 5, ContainerID: "c1"}); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}

	if inner.demand != 1 {
		t.Errorf("RecordDemand not forwarded through the tap: got %d, want 1", inner.demand)
	}
	if inner.purchase != 1 {
		t.Errorf("RecordPurchase not forwarded through the tap: got %d, want 1", inner.purchase)
	}
	if inner.heavyReserveCalls != 1 {
		t.Errorf("RecordHeavyReserve not forwarded through the tap: got %d, want 1", inner.heavyReserveCalls)
	}
}

// A tap with a NIL inner sink is still safe: the decorator is installed for the reason-capture,
// so it must not turn "metrics disabled" into a crash.
func TestBlockedGuardTapIsNilInnerSafe(t *testing.T) {
	tap := NewBlockedGuardTap(nil)
	tap.RecordDemand(HullClassLight, 3, 1)
	tap.RecordPurchase(HullClassLight)
	tap.RecordBlocked(HullClassLight, GuardPrice)
	tap.RecordZeroEffectAlarm()
	tap.RecordHeavyReserve("5", 100, 1_000, 0, 5)
	tap.ObserveHeavyPricePremium("5", 100, 90)
}

// --- THE END-TO-END REGRESSION: coordinator -> real escalator -> durable surfaces ---

type stallSurfaceSpy struct {
	escalations []string
	events      []*captain.Event
}

func (s *stallSurfaceSpy) RecordStallStreak(string, string, string, int) {}

func (s *stallSurfaceSpy) RecordStallEscalation(_, scope, reason string) {
	s.escalations = append(s.escalations, scope+"/"+reason)
}

func (s *stallSurfaceSpy) Record(_ context.Context, e *captain.Event) error {
	s.events = append(s.events, e)
	return nil
}

// THE REGRESSION THAT MATTERS, end to end and through the real escalator: an autosizer BLOCKED on
// the same guard every tick escalates EXACTLY ONCE, on both durable surfaces, and keeps quiet for
// every tick after — the eighteen-tick production streak pages once, not sixteen times.
func TestAutosizerStallEscalatesExactlyOnceThroughTheRealEscalator(t *testing.T) {
	spy := &stallSurfaceSpy{}
	h, _ := blockedHandler(lightShortfall())
	h.SetStallObserver(health.NewStallEscalator(spy, spy))
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 5, ContainerID: "c1"}

	for i := 0; i < health.StallEscalationTicks*6; i++ {
		if _, err := h.reconcileOnce(context.Background(), cmd); err != nil {
			t.Fatalf("reconcileOnce error: %v", err)
		}
	}

	if len(spy.escalations) != 1 {
		t.Fatalf("expected exactly 1 escalation across %d blocked ticks, got %d: %v", health.StallEscalationTicks*6, len(spy.escalations), spy.escalations)
	}
	if spy.escalations[0] != string(HullClassLight)+"/"+string(GuardPrice) {
		t.Fatalf("escalation = %q, want the blocked class and its first failing guard", spy.escalations[0])
	}
	if len(spy.events) != 1 {
		t.Fatalf("expected exactly 1 captain_events row, got %d", len(spy.events))
	}
	if spy.events[0].Type != captain.EventCoordinatorStalled {
		t.Fatalf("event type = %q, want %q", spy.events[0].Type, captain.EventCoordinatorStalled)
	}
}

// …AND THE OTHER HALF, end to end: a correctly-idle autosizer escalates NOTHING, however long it
// idles. If this ever fires, the alarm is worthless.
func TestAutosizerIdleNeverEscalatesThroughTheRealEscalator(t *testing.T) {
	spy := &stallSurfaceSpy{}
	satisfied := &fakeDemandProvider{class: HullClassLight, demand: ClassDemand{Demand: 3, Current: 3, Readable: true}}
	h, _ := blockedHandler(satisfied)
	h.SetStallObserver(health.NewStallEscalator(spy, spy))
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 5, ContainerID: "c1"}

	for i := 0; i < health.StallEscalationTicks*20; i++ {
		if _, err := h.reconcileOnce(context.Background(), cmd); err != nil {
			t.Fatalf("reconcileOnce error: %v", err)
		}
	}

	if len(spy.escalations) != 0 || len(spy.events) != 0 {
		t.Fatalf("an idle autosizer must escalate nothing, got %d escalations / %d events", len(spy.escalations), len(spy.events))
	}
}

// A block cleared by a purchase resets the streak: one transient refusal must never accumulate
// toward a page across a working fleet.
func TestAutosizerPurchaseClearsTheStallStreakThroughTheRealEscalator(t *testing.T) {
	spy := &stallSurfaceSpy{}
	provider := lightShortfall()
	h, _, _, _ := armedHandler(provider)
	h.SetMetricsSink(NewBlockedGuardTap(&recordingMetrics{}))
	h.SetStallObserver(health.NewStallEscalator(spy, spy))
	yard := &fakeYardPrice{price: 437000, cheapest: 400000, yard: "KA42-A2", ok: true}
	h.SetYardPriceReader(yard)
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 5, ContainerID: "c1"}

	// Blocked to one tick short of the threshold, then a tick that buys, then blocked again to
	// one tick short. Neither run reaches the threshold on its own.
	for episode := 0; episode < 2; episode++ {
		yard.ok = false
		for i := 0; i < health.StallEscalationTicks-1; i++ {
			if _, err := h.reconcileOnce(context.Background(), cmd); err != nil {
				t.Fatalf("reconcileOnce error: %v", err)
			}
		}
		yard.ok = true
		if _, err := h.reconcileOnce(context.Background(), cmd); err != nil {
			t.Fatalf("reconcileOnce error: %v", err)
		}
	}

	if len(spy.escalations) != 0 {
		t.Fatalf("a purchase between two short block runs must reset the streak, got %v", spy.escalations)
	}
}

// The tap NEVER mis-attributes a guard across containers. The autosizer handler is a registered
// singleton serving every player's ticks, so two containers can be inside the act path at once;
// a stolen slot must degrade to the coarse fallback reason rather than pinning one container's
// refusal on another's stall streak (which would silently restart the wrong streak).
func TestBlockedGuardTapRefusesToAttributeAStolenSlot(t *testing.T) {
	tap := NewBlockedGuardTap(&recordingMetrics{}).(*blockedGuardTap)

	tap.expect("c1", HullClassHeavy)
	tap.expect("c2", HullClassHeavy) // a sibling container's tick interleaves
	tap.RecordBlocked(HullClassHeavy, GuardAffordability)

	if _, ok := tap.take("c1", HullClassHeavy); ok {
		t.Fatalf("c1 must NOT claim a verdict recorded after c2 took the slot")
	}
	guard, ok := tap.take("c2", HullClassHeavy)
	if !ok || guard != GuardAffordability {
		t.Fatalf("c2 owns the slot and must read its own verdict, got %q (ok=%v)", guard, ok)
	}
	if _, ok := tap.take("c2", HullClassHeavy); ok {
		t.Fatalf("take must consume the verdict: a second read in a later tick would replay a stale guard")
	}
}
