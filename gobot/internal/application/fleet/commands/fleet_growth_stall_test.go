package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/health"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// Stall reporting for the fleet-growth coordinator — the fleet's only heavy buyer, and after
// sp-5pclx deleted the autosizer the only coordinator in this package that reports one.
//
// These tests were the autosizer's; they were REWRITTEN onto growth rather than deleted with it,
// because the subject is the shared verdict logic (classStallVerdict + observeClassStall) and the
// escalation contract on top of it, all of which growth still runs. Deleting them would have
// retired the coordinator AND silently retired the only coverage of the escalator's once-only
// guarantee.

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

// blockedGrowthHandler wires a growth coordinator on a HEAVY wave whose buy is refused every tick:
// the lane surface is capacity-short and the anti-thrash streak is already satisfied, so demand is
// real, but the treasury cannot reach the ask. Deterministic, fail-closed, and exactly the shape of
// the production stall (a guard blocking every tick, forever, silently).
func blockedGrowthHandler(t *testing.T) (*RunFleetGrowthCoordinatorHandler, *recordingStallObserver) {
	t.Helper()
	h := newGrowthHandlerWith(t, growthFixture{
		lanes:         &fakeLanes{count: 9, readable: true},
		treasury:      12_000_000,
		yardAsk:       1_000_000,
		shortfallHeld: growthSettledWindow,
	})
	obs := &recordingStallObserver{}
	h.SetStallObserver(obs)
	return h, obs
}

// --- what the coordinator REPORTS, per outcome ---

// A HEAVY wave with unmet demand and a guard refusing the buy reports BLOCKED, naming the guard.
func TestGrowthReportsBlockedNamingTheFailingGuard(t *testing.T) {
	h, obs := blockedGrowthHandler(t)
	h.SetYardPriceReader(&growthYardPriceCounter{}) // no readable price ⇒ the price guard refuses

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}

	heavy := obs.forScope(string(HullClassHeavy))
	if len(heavy) != 1 || heavy[0].Outcome != health.StallBlocked {
		t.Fatalf("an unmet shortfall that bought nothing must report BLOCKED, got %+v", heavy)
	}
	if k := obs.keys[0]; k.Coordinator != growthStallCoordinator || k.ContainerID != "growth-1" || k.PlayerID != 1 {
		t.Fatalf("the stall key must carry the growth coordinator, container and player, got %+v", k)
	}
}

// A PROBE wave has nothing outstanding: that is IDLE, not BLOCKED. This is the branch that has to
// stay silent forever or the alarm is worthless.
func TestGrowthReportsIdleWhenThereIsNoShortfall(t *testing.T) {
	h := newGrowthHandlerWith(t, growthFixture{lanes: &fakeLanes{count: 0, readable: true}})
	obs := &recordingStallObserver{}
	h.SetStallObserver(obs)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}

	heavy := obs.forScope(string(HullClassHeavy))
	if len(heavy) != 1 || heavy[0].Outcome != health.StallIdle {
		t.Fatalf("a coordinator with nothing outstanding must report IDLE, got %+v", heavy)
	}
}

// An unreadable lane surface reports IDLE, and that is CORRECT here — it is the one place growth's
// verdict deliberately differs from a per-class sizer's.
//
// The autosizer read demand per class, so an unreadable signal was a class that wanted to know its
// shortfall and could not find out: BLOCKED. Growth reads the same surface through the WAVE, and an
// unreadable surface derives PROBE (see UnservedLaneReader: "readable=false ⇒ PROBE and no heavy
// bought — fail-closed on the buy, release on the wave"). A PROBE wave means the coordinator has been
// told to stand down, which is idle-BY-INSTRUCTION, not a refusal. Reporting BLOCKED here would page
// an operator every time the market cache went cold, for a coordinator behaving exactly as designed.
//
// The fail-closed property is NOT weakened by this: nothing is bought on an unreadable surface. Only
// the ESCALATION verdict differs, and this test exists to pin that distinction so a future change
// cannot quietly turn a designed stand-down into a page.
func TestGrowthReportsIdleOnUnreadableDemandBecauseTheWaveStandsDown(t *testing.T) {
	buyer := &growthPurchaseRecorder{}
	h := newGrowthHandlerWith(t, growthFixture{lanes: &fakeLanes{readable: false}})
	h.SetPurchaser(buyer)
	obs := &recordingStallObserver{}
	h.SetStallObserver(obs)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("reconcileOnce error: %v", err)
	}

	heavy := obs.forScope(string(HullClassHeavy))
	if len(heavy) != 1 || heavy[0].Outcome != health.StallIdle {
		t.Fatalf("an unreadable lane surface derives PROBE, which is idle-by-instruction, got %+v", heavy)
	}
	// The half that must never move: unreadable still means unbought.
	if buyer.calls != 0 {
		t.Fatalf("an unreadable lane surface must buy NOTHING, got %d purchases", buyer.calls)
	}
}

// An unwired observer (tests, or a boot before DI completes) must degrade to silence, never a
// panic: observability is not a precondition for growing the fleet (RULINGS #4).
func TestGrowthRunsWithNoStallObserverWired(t *testing.T) {
	h, _ := blockedGrowthHandler(t)
	h.SetStallObserver(nil)

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("an unwired stall observer must not break the tick: %v", err)
	}
}

// --- end to end, through the REAL escalator ---

// stallSurfaceSpy captures both durable escalation surfaces.
type stallSurfaceSpy struct {
	escalations []string
	events      []*captain.Event
}

func (s *stallSurfaceSpy) RecordStallStreak(_, _, _ string, _ int) {}

func (s *stallSurfaceSpy) RecordStallEscalation(_, scope, reason string) {
	s.escalations = append(s.escalations, scope+"/"+reason)
}

func (s *stallSurfaceSpy) Record(_ context.Context, e *captain.Event) error {
	s.events = append(s.events, e)
	return nil
}

// THE REGRESSION THAT MATTERS, end to end and through the real escalator: a coordinator BLOCKED on
// the same guard every tick escalates EXACTLY ONCE, on both durable surfaces, and keeps quiet for
// every tick after — an eighteen-tick production streak pages once, not sixteen times.
func TestGrowthStallEscalatesExactlyOnceThroughTheRealEscalator(t *testing.T) {
	spy := &stallSurfaceSpy{}
	h, _ := blockedGrowthHandler(t)
	h.SetYardPriceReader(&growthYardPriceCounter{})
	h.SetStallObserver(health.NewStallEscalator(spy, spy))

	for i := 0; i < health.StallEscalationTicks*6; i++ {
		if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
			t.Fatalf("reconcileOnce error: %v", err)
		}
	}

	if len(spy.escalations) != 1 {
		t.Fatalf("expected exactly 1 escalation across %d blocked ticks, got %d: %v", health.StallEscalationTicks*6, len(spy.escalations), spy.escalations)
	}
	if len(spy.events) != 1 {
		t.Fatalf("expected exactly 1 captain_events row, got %d", len(spy.events))
	}
	if spy.events[0].Type != captain.EventCoordinatorStalled {
		t.Fatalf("event type = %q, want %q", spy.events[0].Type, captain.EventCoordinatorStalled)
	}
}

// …AND THE OTHER HALF, end to end: a correctly-idle coordinator escalates NOTHING, however long it
// idles. If this ever fires, the alarm is worthless.
func TestGrowthIdleNeverEscalatesThroughTheRealEscalator(t *testing.T) {
	spy := &stallSurfaceSpy{}
	h := newGrowthHandlerWith(t, growthFixture{lanes: &fakeLanes{count: 0, readable: true}})
	h.SetStallObserver(health.NewStallEscalator(spy, spy))

	for i := 0; i < health.StallEscalationTicks*20; i++ {
		if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
			t.Fatalf("reconcileOnce error: %v", err)
		}
	}

	if len(spy.escalations) != 0 || len(spy.events) != 0 {
		t.Fatalf("an idle coordinator must escalate nothing, got %d escalations / %d events", len(spy.escalations), len(spy.events))
	}
}
