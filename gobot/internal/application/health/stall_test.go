package health

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// --- driven-port fakes (the two surfaces an escalation is loud on) ---

type recordedStreak struct {
	coordinator string
	scope       string
	reason      string
	streak      int
}

type fakeStallMetrics struct {
	streaks     []recordedStreak
	escalations []recordedStreak
}

func (f *fakeStallMetrics) RecordStallStreak(coordinator, scope, reason string, streak int) {
	f.streaks = append(f.streaks, recordedStreak{coordinator, scope, reason, streak})
}

func (f *fakeStallMetrics) RecordStallEscalation(coordinator, scope, reason string) {
	f.escalations = append(f.escalations, recordedStreak{coordinator: coordinator, scope: scope, reason: reason})
}

type fakeEventRecorder struct {
	events []*captain.Event
	err    error
}

func (f *fakeEventRecorder) Record(_ context.Context, e *captain.Event) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, e)
	return nil
}

func newTestEscalator() (*StallEscalator, *fakeStallMetrics, *fakeEventRecorder) {
	m := &fakeStallMetrics{}
	r := &fakeEventRecorder{}
	return NewStallEscalator(m, r), m, r
}

func testKey() StallKey {
	return StallKey{Coordinator: "fleet_autosizer", ContainerID: "c1", Scope: "heavy", PlayerID: 5}
}

// THE PRODUCTION REGRESSION. The autosizer's heavy branch was BLOCKED on the same guard for
// eighteen consecutive ticks and nothing escalated. A coordinator reporting BLOCKED with the
// SAME reason for StallEscalationTicks consecutive ticks must escalate — once, on both durable
// surfaces (the Prometheus counter and the captain_events outbox).
func TestEscalatorEscalatesOnceAfterConsecutiveBlockedTicksOnTheSameReason(t *testing.T) {
	esc, m, rec := newTestEscalator()
	key := testKey()

	for i := 0; i < StallEscalationTicks; i++ {
		esc.Observe(context.Background(), key, TickBlocked("treasury_floor", "treasury 40000 < price"))
	}

	if len(m.escalations) != 1 {
		t.Fatalf("expected exactly 1 metric escalation after %d consecutive BLOCKED ticks, got %d", StallEscalationTicks, len(m.escalations))
	}
	if m.escalations[0].coordinator != "fleet_autosizer" || m.escalations[0].scope != "heavy" || m.escalations[0].reason != "treasury_floor" {
		t.Fatalf("escalation must carry the coordinator, scope and machine-readable reason, got %+v", m.escalations[0])
	}
	if len(rec.events) != 1 {
		t.Fatalf("expected exactly 1 captain_events row, got %d", len(rec.events))
	}
	ev := rec.events[0]
	if ev.Type != captain.EventCoordinatorStalled {
		t.Fatalf("escalation event type = %q, want %q", ev.Type, captain.EventCoordinatorStalled)
	}
	if ev.PlayerID != 5 || ev.Ship != "c1" {
		t.Fatalf("escalation event must be scoped to the player and the container, got player=%d ship=%q", ev.PlayerID, ev.Ship)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(ev.Payload), &payload); err != nil {
		t.Fatalf("escalation payload is not JSON: %v (%s)", err, ev.Payload)
	}
	if payload["reason"] != "treasury_floor" {
		t.Fatalf("escalation payload must name the machine-readable reason, got %v", payload["reason"])
	}
	if payload["coordinator"] != "fleet_autosizer" {
		t.Fatalf("escalation payload must name the coordinator, got %v", payload["coordinator"])
	}
	if got, ok := payload["streak"].(float64); !ok || int(got) != StallEscalationTicks {
		t.Fatalf("escalation payload must carry the streak length, got %v", payload["streak"])
	}
}

// THE OTHER HALF OF THE DESIGN, and the one that makes the alarm worth having: a healthy IDLE
// coordinator must stay silent no matter how long it idles. An alarm that fires on correct
// idleness is noise, and noise is what made the original signals unreadable.
func TestEscalatorNeverEscalatesAHealthyIdleCoordinator(t *testing.T) {
	esc, m, rec := newTestEscalator()
	key := testKey()

	for i := 0; i < StallEscalationTicks*40; i++ {
		esc.Observe(context.Background(), key, TickIdle())
	}

	if len(m.escalations) != 0 {
		t.Fatalf("an idle coordinator must never escalate, got %d escalations", len(m.escalations))
	}
	if len(rec.events) != 0 {
		t.Fatalf("an idle coordinator must never write a captain event, got %d", len(rec.events))
	}
}

// A coordinator that keeps working must likewise never escalate.
func TestEscalatorNeverEscalatesAProgressingCoordinator(t *testing.T) {
	esc, m, rec := newTestEscalator()
	key := testKey()

	for i := 0; i < StallEscalationTicks*40; i++ {
		esc.Observe(context.Background(), key, TickProgress())
	}

	if len(m.escalations) != 0 || len(rec.events) != 0 {
		t.Fatalf("a progressing coordinator must never escalate, got %d escalations / %d events", len(m.escalations), len(rec.events))
	}
}

// A block cleared BEFORE the threshold resets the streak — otherwise one-off transient refusals
// accumulate forever and the alarm degenerates into noise.
func TestEscalatorClearedBlockResetsTheStreak(t *testing.T) {
	for _, tc := range []struct {
		name    string
		clearer TickOutcome
	}{
		{"progress clears", TickProgress()},
		{"idle clears", TickIdle()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			esc, m, rec := newTestEscalator()
			key := testKey()

			// One tick short of the threshold, then a clear, then the threshold again from scratch.
			for i := 0; i < StallEscalationTicks-1; i++ {
				esc.Observe(context.Background(), key, TickBlocked("api_util", ""))
			}
			esc.Observe(context.Background(), key, tc.clearer)
			for i := 0; i < StallEscalationTicks-1; i++ {
				esc.Observe(context.Background(), key, TickBlocked("api_util", ""))
			}

			if len(m.escalations) != 0 || len(rec.events) != 0 {
				t.Fatalf("a cleared block must reset the streak: %d BLOCKED ticks either side of a clear must not escalate, got %d escalations / %d events",
					StallEscalationTicks-1, len(m.escalations), len(rec.events))
			}
		})
	}
}

// A CHANGED block reason restarts the streak rather than continuing it: two different refusals in
// a row are two transients, not one sustained stall.
func TestEscalatorChangedReasonRestartsTheStreak(t *testing.T) {
	esc, m, rec := newTestEscalator()
	key := testKey()

	// Alternate two reasons for well past the threshold: neither ever runs consecutively.
	for i := 0; i < StallEscalationTicks*4; i++ {
		reason := StallReason("api_util")
		if i%2 == 1 {
			reason = "price_read"
		}
		esc.Observe(context.Background(), key, TickBlocked(reason, ""))
	}

	if len(m.escalations) != 0 || len(rec.events) != 0 {
		t.Fatalf("alternating block reasons must restart the streak each time, got %d escalations / %d events", len(m.escalations), len(rec.events))
	}

	// And the restarted streak still escalates once it becomes consecutive.
	for i := 0; i < StallEscalationTicks; i++ {
		esc.Observe(context.Background(), key, TickBlocked("price_read", ""))
	}
	if len(m.escalations) != 1 {
		t.Fatalf("a reason that becomes consecutive must escalate once, got %d", len(m.escalations))
	}
	if m.escalations[0].reason != "price_read" {
		t.Fatalf("escalation reason = %q, want price_read", m.escalations[0].reason)
	}
}

// Escalation is once per STREAK, not once per tick after the threshold: the eighteen-tick
// production streak must page once, not sixteen times.
func TestEscalatorFiresOncePerStreakNotPerTickPastTheThreshold(t *testing.T) {
	esc, m, rec := newTestEscalator()
	key := testKey()

	for i := 0; i < StallEscalationTicks*6; i++ {
		esc.Observe(context.Background(), key, TickBlocked("heavy_cap", ""))
	}

	if len(m.escalations) != 1 {
		t.Fatalf("expected 1 escalation across %d BLOCKED ticks, got %d", StallEscalationTicks*6, len(m.escalations))
	}
	if len(rec.events) != 1 {
		t.Fatalf("expected 1 captain event across %d BLOCKED ticks, got %d", StallEscalationTicks*6, len(rec.events))
	}
}

// A recovery RE-ARMS the alarm: a fresh episode after a clear escalates again, so a stall that
// returns is not silently swallowed by the first episode's one-shot.
func TestEscalatorReArmsAfterRecovery(t *testing.T) {
	esc, m, _ := newTestEscalator()
	key := testKey()

	for i := 0; i < StallEscalationTicks; i++ {
		esc.Observe(context.Background(), key, TickBlocked("heavy_cap", ""))
	}
	esc.Observe(context.Background(), key, TickProgress())
	for i := 0; i < StallEscalationTicks; i++ {
		esc.Observe(context.Background(), key, TickBlocked("heavy_cap", ""))
	}

	if len(m.escalations) != 2 {
		t.Fatalf("a fresh episode after a recovery must escalate again, got %d escalations", len(m.escalations))
	}
}

// The streak gauge is published on every observation, and DRAINS to zero when the block clears —
// a gauge that keeps its last non-zero value reads as a live stall forever.
func TestEscalatorPublishesTheStreakGaugeAndDrainsItOnClear(t *testing.T) {
	esc, m, _ := newTestEscalator()
	key := testKey()

	esc.Observe(context.Background(), key, TickBlocked("api_util", ""))
	esc.Observe(context.Background(), key, TickBlocked("api_util", ""))
	esc.Observe(context.Background(), key, TickProgress())

	if len(m.streaks) != 3 {
		t.Fatalf("expected a streak sample per observation, got %d: %+v", len(m.streaks), m.streaks)
	}
	if m.streaks[0].streak != 1 || m.streaks[1].streak != 2 {
		t.Fatalf("streak must count consecutive BLOCKED ticks, got %+v", m.streaks)
	}
	if m.streaks[2].streak != 0 || m.streaks[2].reason != "api_util" {
		t.Fatalf("a clear must drain the PREVIOUS reason's gauge to 0, got %+v", m.streaks[2])
	}
}

// An idle coordinator that was never blocked publishes nothing at all: there is no reason label
// to hang a zero on, and inventing one would create a permanently-zero series that reads as a
// real refusal never yet seen.
func TestEscalatorPublishesNothingForAnUnblockedIdleTick(t *testing.T) {
	esc, m, _ := newTestEscalator()

	esc.Observe(context.Background(), testKey(), TickIdle())

	if len(m.streaks) != 0 {
		t.Fatalf("an idle tick with no prior block must publish no streak sample, got %+v", m.streaks)
	}
}

// Streaks are per KEY: one container's stall must never be closed out by a sibling's healthy
// tick, and a coordinator's two scopes stall independently.
func TestEscalatorKeepsStreaksPerKey(t *testing.T) {
	esc, m, _ := newTestEscalator()
	stalled := StallKey{Coordinator: "fleet_autosizer", ContainerID: "c1", Scope: "heavy", PlayerID: 5}
	healthy := StallKey{Coordinator: "fleet_autosizer", ContainerID: "c1", Scope: "light", PlayerID: 5}
	other := StallKey{Coordinator: "fleet_autosizer", ContainerID: "c2", Scope: "heavy", PlayerID: 7}

	for i := 0; i < StallEscalationTicks; i++ {
		esc.Observe(context.Background(), stalled, TickBlocked("heavy_cap", ""))
		esc.Observe(context.Background(), healthy, TickProgress())
		esc.Observe(context.Background(), other, TickIdle())
	}

	if len(m.escalations) != 1 {
		t.Fatalf("only the stalled key may escalate, got %d escalations: %+v", len(m.escalations), m.escalations)
	}
	if m.escalations[0].scope != "heavy" {
		t.Fatalf("escalation must carry the stalled scope, got %q", m.escalations[0].scope)
	}
}

// A BLOCKED report with no reason still escalates, under an explicit sentinel: a stall reported
// carelessly must not be a stall reported silently.
func TestEscalatorNormalisesAnEmptyReason(t *testing.T) {
	esc, m, _ := newTestEscalator()

	for i := 0; i < StallEscalationTicks; i++ {
		esc.Observe(context.Background(), testKey(), TickBlocked("", ""))
	}

	if len(m.escalations) != 1 {
		t.Fatalf("expected 1 escalation for an unlabelled block, got %d", len(m.escalations))
	}
	if m.escalations[0].reason != string(StallReasonUnspecified) {
		t.Fatalf("an empty reason must normalise to %q, got %q", StallReasonUnspecified, m.escalations[0].reason)
	}
}

// Observability must never break a tick: unwired sinks, a nil escalator and a failing outbox all
// degrade to silence rather than panicking or erroring the caller (RULINGS #4 — a recording miss
// can never touch a decision).
func TestEscalatorIsBestEffortWhenUnwiredOrFailing(t *testing.T) {
	var nilEscalator *StallEscalator
	nilEscalator.Observe(context.Background(), testKey(), TickBlocked("x", ""))

	unwired := NewStallEscalator(nil, nil)
	for i := 0; i < StallEscalationTicks; i++ {
		unwired.Observe(context.Background(), testKey(), TickBlocked("x", ""))
	}

	failing := NewStallEscalator(&fakeStallMetrics{}, &fakeEventRecorder{err: context.DeadlineExceeded})
	for i := 0; i < StallEscalationTicks; i++ {
		failing.Observe(context.Background(), testKey(), TickBlocked("x", ""))
	}
}

// STRUCTURAL PROOF OF THE RULINGS #2 CARVE-OUT. The escalation streak is cross-tick state, which
// is only permissible because no decision can read it. That is enforced by the TYPE, not by
// convention: the escalator's entire exported surface is Observe, and Observe returns NOTHING —
// there is no value for a purchase, dispatch or route to branch on.
func TestStallCounterIsUnreadableByAnyDecisionPath(t *testing.T) {
	escalator := reflect.TypeOf(&StallEscalator{})
	var exported []string
	for i := 0; i < escalator.NumMethod(); i++ {
		exported = append(exported, escalator.Method(i).Name)
	}
	if len(exported) != 1 || exported[0] != "Observe" {
		t.Fatalf("*StallEscalator must expose exactly one exported method, Observe; got %v — any reader makes the streak branchable and breaks the RULINGS #2 carve-out", exported)
	}
	observe, _ := escalator.MethodByName("Observe")
	if observe.Type.NumOut() != 0 {
		t.Fatalf("StallEscalator.Observe must return nothing; got %d result(s) — a returned streak/bool is exactly what a decision would branch on", observe.Type.NumOut())
	}

	seam := reflect.TypeOf((*StallObserver)(nil)).Elem()
	if seam.NumMethod() != 1 || seam.Method(0).Name != "Observe" {
		t.Fatalf("StallObserver (the seam coordinators hold) must be write-only with exactly one method Observe, got %d methods", seam.NumMethod())
	}
	if seam.Method(0).Type.NumOut() != 0 {
		t.Fatalf("StallObserver.Observe must return nothing, got %d result(s)", seam.Method(0).Type.NumOut())
	}
}

// The threshold is a const (RULINGS #5) and is short enough to catch a stall within a couple of
// minutes on the 30s sensing tick while tolerating an ordinary one-off transient refusal.
func TestStallEscalationThresholdIsAShortConst(t *testing.T) {
	if StallEscalationTicks < 3 {
		t.Fatalf("StallEscalationTicks = %d: below 3 a single transient refusal plus its retry cries wolf", StallEscalationTicks)
	}
	if StallEscalationTicks > 4 {
		t.Fatalf("StallEscalationTicks = %d: above 4 the 30s sensing tick takes minutes too long to page", StallEscalationTicks)
	}
}
