package health

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// stall.go answers the one question nothing in this fleet could answer: did this tick do
// nothing because there was NOTHING TO DO, or because it COULD NOT DO ANYTHING?
//
// Every layer here fails closed — correctly, RULINGS #4 — and then reports the refusal as a log
// line nobody consumes. A healthy idle tick and a permanently wedged tick therefore produce
// identical output, which is how eighteen consecutive failed heavy purchases, a universe roster
// that had NEVER once been read, and a shipyard sensor that had never existed all ran for hours
// without anything escalating. "0 discovered" is exactly what a fully-charted galaxy looks like.
//
// The fix is a three-way tick verdict every coordinator can report — PROGRESS, IDLE,
// BLOCKED(reason) — and an escalation that makes the third one LOUD on the two durable surfaces
// this fleet already has: the Prometheus series and the captain_events outbox.
//
// It is the third member of the coordinator self-observability family in this package, and it
// covers the gap the other two leave. StreakTracker (streak.go) catches the loop that keeps
// ERRORING; EffectTracker (effect.go) catches the loop that keeps DETECTING work and taking no
// action inside one coordinator's own tick. Neither sees the loop that is neither erroring nor
// inert but simply REFUSING — every guard fails closed, every port returns "unreadable", and the
// tick completes cleanly having accomplished nothing. That is this file.

// StallEscalationTicks is how many CONSECUTIVE ticks a coordinator must report BLOCKED with the
// SAME reason before the stall escalates. A const, never a config key or a tune seam
// (RULINGS #5), and armed on deploy — an alarm that ships behind a knob is the exact failure this
// file exists to end.
//
// WHY THREE. The bound is squeezed from both sides. Below it, ordinary transient refusals cry
// wolf: a yard ask that missed one read, a treasury blip, a purchase settling across a tick
// boundary are all single-tick events, and 2 leaves no margin over a single retry. Above it the
// alarm arrives too late to be a stall alarm at all. Three consecutive identical refusals is not
// a blip — the coordinator has had three full chances to make progress from freshly re-derived
// state (RULINGS #2) and taken none.
//
// The unit is TICKS, not wall-clock, because a tick is this fleet's unit of "another full chance
// from scratch". On the 30s parked-sensing tick — which drives the sensing pass AND the off-gate
// expansion pass, the two that hid the 33-system region behind an unread jump gate — three ticks
// is 90 seconds, so a real stall pages within minutes. On the fleet autosizer's deliberately
// strategic 15-minute tick it is 45 minutes: slower in absolute terms, but the measured
// production failure there was eighteen consecutive blocked ticks (4.5 HOURS) with nothing
// raised at all, so this catches it at tick three with fifteen still to run. The codebase's own
// anti-thrash streaks sit in the same family (heavy_unserved_lanes_min = 3, crashLoopThreshold =
// 5, zero_effect_alarm_ticks = 4).
const StallEscalationTicks = 3

// stallRecordTimeout bounds the detached outbox write so a stalled recorder can never wedge the
// coordinator tick that is reporting a stall. Mirrors errorLoopRecordTimeout (emit.go).
const stallRecordTimeout = 5 * time.Second

// StallOutcome is the three-way verdict of one coordinator tick.
type StallOutcome string

const (
	// StallProgress means work was done this tick.
	StallProgress StallOutcome = "PROGRESS"
	// StallIdle means there was nothing to do, and that is CORRECT. Idle must stay silent
	// forever — an alarm that fires on correct idleness is worthless.
	StallIdle StallOutcome = "IDLE"
	// StallBlocked means there WAS work to do and it could not be done.
	StallBlocked StallOutcome = "BLOCKED"
)

// StallReason is the machine-readable cause of a BLOCKED tick — a guard name, a port that could
// not be read, a pass that could not select a target. It keys the streak, so it must be drawn
// from a small stable vocabulary: a reason that varies tick to tick (an error string, a count, a
// timestamp) restarts the streak every tick and can never escalate.
type StallReason string

// StallReasonUnspecified is the sentinel for a BLOCKED report that named no reason. A stall
// reported carelessly must not become a stall reported silently, so it still escalates — under a
// label that makes the missing vocabulary entry obvious.
const StallReasonUnspecified StallReason = "unspecified"

// TickOutcome is one coordinator tick's verdict. Detail is free text for the alarm's payload and
// log line ONLY — it never keys the streak, so a detail that carries live numbers cannot stop an
// otherwise-consecutive stall from escalating.
type TickOutcome struct {
	Outcome StallOutcome
	Reason  StallReason
	Detail  string
}

// TickProgress reports that the tick did work.
func TickProgress() TickOutcome { return TickOutcome{Outcome: StallProgress} }

// TickIdle reports that the tick had nothing to do, correctly.
func TickIdle() TickOutcome { return TickOutcome{Outcome: StallIdle} }

// TickBlocked reports that the tick had work to do and could not do it.
func TickBlocked(reason StallReason, detail string) TickOutcome {
	return TickOutcome{Outcome: StallBlocked, Reason: reason, Detail: detail}
}

// StallKey identifies one independently-stalling thing: a coordinator, the container instance
// running it, and an optional sub-scope within it (a hull class, a named pass). Streaks are kept
// per key, so one container's stall is never closed out by a sibling's healthy tick and one
// blocked hull class never masks another.
type StallKey struct {
	Coordinator string
	ContainerID string
	Scope       string
	PlayerID    int
}

// StallObserver is the WRITE-ONLY seam a coordinator holds. It has exactly one method and that
// method returns NOTHING: see the RULINGS #2 note on StallEscalator for why that is load-bearing
// rather than stylistic.
type StallObserver interface {
	Observe(ctx context.Context, key StallKey, outcome TickOutcome)
}

// StallMetricsSink is the Prometheus surface an escalation is published on. Pure observation and
// nil-safe by contract, mirroring every other metrics port in this codebase.
type StallMetricsSink interface {
	RecordStallStreak(coordinator, scope, reason string, streak int)
	RecordStallEscalation(coordinator, scope, reason string)
}

// StallEscalator counts consecutive BLOCKED ticks per key and escalates ONCE per streak.
//
// RULINGS #2 — THE FLEET RE-DERIVES FROM DURABLE STATE EACH TICK AND HOLDS NOTHING BETWEEN
// TICKS. This escalation counter IS cross-tick state and is a deliberate, bounded exception to
// that rule. It is admissible for exactly one reason: it is OBSERVABILITY and it can never
// influence a decision. No purchase, no dispatch, no route, no guard verdict and no phase
// derivation may branch on it.
//
// That is guaranteed STRUCTURALLY, not by convention. The streak map is unexported, its value
// type is unexported, and the escalator's ENTIRE exported surface is Observe — which returns
// nothing at all: no streak, no bool, no error. There is therefore no value in the language for
// a decision path to read, and StallObserver (the interface the coordinators actually hold)
// narrows it to that one write-only method again. A regression test asserts the exported method
// set and its zero return arity, so adding a reader fails the build's tests rather than quietly
// opening the door.
//
// Losing the counter is harmless by construction: a daemon restart simply re-derives the streak
// from the ticks that follow, and a stall that is still a stall re-escalates within
// StallEscalationTicks. Nothing durable depends on it surviving.
type StallEscalator struct {
	mu      sync.Mutex
	streaks map[StallKey]stallStreak

	metrics StallMetricsSink
	events  captain.EventRecorder
}

// stallStreak is one key's live episode. Unexported, with an unexported type, so it cannot leave
// this file even by accident.
type stallStreak struct {
	reason    StallReason
	count     int
	escalated bool
}

// NewStallEscalator wires the escalator to its two durable surfaces. Either may be nil (metrics
// disabled, or a boot before DI completes): emission is then silently skipped, exactly as the
// sibling collectors and RecordErrorLoop behave.
func NewStallEscalator(metrics StallMetricsSink, events captain.EventRecorder) *StallEscalator {
	return &StallEscalator{
		streaks: make(map[StallKey]stallStreak),
		metrics: metrics,
		events:  events,
	}
}

// Observe records ONE tick's verdict for one key and, on the exact tick a BLOCKED streak reaches
// StallEscalationTicks, escalates it loudly and durably.
//
// It RETURNS NOTHING, deliberately and permanently — see the type comment. Callers must invoke it
// exactly once per key per tick: it is the tick counter itself, so a double call inflates the
// streak and a skipped call stalls it.
//
// PROGRESS and IDLE both clear the streak and re-arm the one-shot. They are distinguished on the
// caller's side and in the log, but they mean the same thing here: this tick is not evidence of a
// wedge. That is what stops a one-off transient from accumulating forever, and it is what keeps a
// correctly-idle coordinator silent no matter how long it idles.
func (e *StallEscalator) Observe(ctx context.Context, key StallKey, outcome TickOutcome) {
	if e == nil {
		return // unwired: observability is never a precondition for running a tick.
	}

	step := e.advance(key, outcome)
	if !step.publish {
		return
	}

	if e.metrics != nil {
		e.metrics.RecordStallStreak(key.Coordinator, key.Scope, string(step.reason), step.count)
	}
	if !step.escalate {
		return
	}
	e.escalate(ctx, key, step)
}

// stallStep is what one Observe decided, computed under the lock and acted on outside it so a
// slow outbox write can never block another coordinator's tick.
type stallStep struct {
	reason   StallReason
	detail   string
	count    int
	escalate bool
	// publish is false when there is nothing to say — a clear on a key that was never blocked.
	// Inventing a zero sample there would create a permanently-zero series that reads as a real
	// refusal simply never yet seen.
	publish bool
}

func (e *StallEscalator) advance(key StallKey, outcome TickOutcome) stallStep {
	e.mu.Lock()
	defer e.mu.Unlock()

	prev, had := e.streaks[key]

	if outcome.Outcome != StallBlocked {
		// PROGRESS or IDLE: the episode is over. Drain the PREVIOUS reason's gauge to zero
		// rather than leaving it holding its last non-zero value, which reads as a live stall.
		delete(e.streaks, key)
		if !had {
			return stallStep{}
		}
		return stallStep{reason: prev.reason, count: 0, publish: true}
	}

	reason := outcome.Reason
	if reason == "" {
		reason = StallReasonUnspecified
	}

	next := stallStreak{reason: reason, count: 1}
	if had && prev.reason == reason {
		// Same reason as last tick: the episode continues, one-shot state carried with it.
		next.count = prev.count + 1
		next.escalated = prev.escalated
	}
	// A CHANGED reason falls through with count=1 and escalated=false: two different refusals in
	// a row are two transients, not one sustained stall, so the streak restarts rather than
	// continuing — and the fresh episode is armed to escalate on its own merits.

	escalate := next.count >= StallEscalationTicks && !next.escalated
	if escalate {
		next.escalated = true // one-shot: once per STREAK, never once per tick past the threshold.
	}
	e.streaks[key] = next

	return stallStep{
		reason:   reason,
		detail:   outcome.Detail,
		count:    next.count,
		escalate: escalate,
		publish:  true,
	}
}

// escalate makes the stall loud on both durable surfaces and in the log. Every failure here is
// swallowed: an alarm that panics or errors a coordinator tick is worse than the silence it was
// added to break.
func (e *StallEscalator) escalate(ctx context.Context, key StallKey, step stallStep) {
	if e.metrics != nil {
		e.metrics.RecordStallEscalation(key.Coordinator, key.Scope, string(step.reason))
	}

	logger := common.LoggerFromContext(ctx)
	message := fmt.Sprintf(
		"STALL ESCALATION: %s%s has reported BLOCKED(%s) for %d consecutive ticks — it has work to do and cannot do it",
		key.Coordinator, scopeSuffix(key.Scope), step.reason, step.count)
	if step.detail != "" {
		message += ": " + step.detail
	}
	logger.Log("WARN", message, map[string]interface{}{
		"action":       "coordinator_stall_escalation",
		"coordinator":  key.Coordinator,
		"container_id": key.ContainerID,
		"scope":        key.Scope,
		"reason":       string(step.reason),
		"streak":       step.count,
		"threshold":    StallEscalationTicks,
	})

	if e.events == nil {
		return
	}
	// Detached, short-timeout context: the caller's ctx may be cancelled the instant a container
	// stops, and the one signal this file exists to provide must not be lost to that race.
	recordCtx, cancel := context.WithTimeout(context.Background(), stallRecordTimeout)
	defer cancel()
	if err := e.events.Record(recordCtx, NewCoordinatorStalledEvent(key, step.reason, step.detail, step.count)); err != nil {
		logger.Log("WARNING", fmt.Sprintf("captain outbox: failed to record %s for %s: %v", captain.EventCoordinatorStalled, key.Coordinator, err), nil)
	}
}

func scopeSuffix(scope string) string {
	if scope == "" {
		return ""
	}
	return "/" + scope
}

// NewCoordinatorStalledEvent builds the captain event for an escalated stall. Pure and
// deterministic so it is fully unit-testable without a real recorder; the Ship field carries the
// coordinator's own container id, matching NewErrorLoopEvent (these events are container-scoped,
// not ship-scoped).
func NewCoordinatorStalledEvent(key StallKey, reason StallReason, detail string, streak int) *captain.Event {
	payload, err := json.Marshal(map[string]any{
		"coordinator":  key.Coordinator,
		"container_id": key.ContainerID,
		"scope":        key.Scope,
		"reason":       string(reason),
		"detail":       detail,
		"streak":       streak,
		"threshold":    StallEscalationTicks,
	})
	if err != nil {
		payload = []byte("{}")
	}
	return &captain.Event{
		Type:     captain.EventCoordinatorStalled,
		Ship:     key.ContainerID,
		PlayerID: key.PlayerID,
		Payload:  string(payload),
	}
}
