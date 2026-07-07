package captainsup

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// defaultMaxWakeIntervalMinutes is the never-wake safety ceiling used
// whenever config wiring leaves MaxWakeIntervalMinutes unset (<=0): a
// captain-declared NextWakeAt can delay a wake, but must never suppress one
// indefinitely.
const defaultMaxWakeIntervalMinutes = 180

// wakeGateInput bundles every input the wake-gate decision needs, so it can
// be evaluated as a pure function independent of the database, event store,
// or city gateway (spec: sp-sk68 wake model).
type wakeGateInput struct {
	Now    time.Time
	Events []*captain.Event
	Policy WakePolicy

	Credits int

	LastSession            time.Time
	HeartbeatMinutes       int
	MaxWakeIntervalMinutes int
}

// wakeGateDecision is the outcome of evaluateWakeGate: whether to wake, and
// a short human-readable reason (logging/test-failure messages only — never
// asserted on by callers).
type wakeGateDecision struct {
	ShouldWake bool
	Reason     string
}

// evaluateWakeGate decides whether the supervisor should wake the captain
// this tick. It never decides HOW to wake — that remains bridgeWake's job,
// unchanged, and is always handed the full unprocessed batch once a wake is
// decided. Deferred event types (workflow.finished, contract.completed,
// credits.threshold, ship.idle by default) never force a wake on their own;
// they simply ride whichever wake fires next for some other reason.
func evaluateWakeGate(in wakeGateInput) wakeGateDecision {
	interrupts, _ := partitionEvents(in.Events, in.Policy.InterruptTypes)
	if len(interrupts) > 0 {
		return wakeGateDecision{ShouldWake: true, Reason: "interrupt event"}
	}

	if !in.Now.Before(effectiveNextWake(in)) {
		return wakeGateDecision{ShouldWake: true, Reason: "cadence due"}
	}

	if in.Policy.CreditsAbove != nil && in.Credits >= *in.Policy.CreditsAbove {
		return wakeGateDecision{ShouldWake: true, Reason: "credits at/above CreditsAbove"}
	}
	if in.Policy.CreditsBelow != nil && in.Credits <= *in.Policy.CreditsBelow {
		return wakeGateDecision{ShouldWake: true, Reason: "credits at/below CreditsBelow"}
	}

	return wakeGateDecision{ShouldWake: false, Reason: "deferred only, cadence not due"}
}

// effectiveNextWake resolves the next-wake instant: the captain-declared
// NextWakeAt when present (capped at the never-wake ceiling), else the
// default heartbeat cadence from LastSession.
func effectiveNextWake(in wakeGateInput) time.Time {
	maxInterval := in.MaxWakeIntervalMinutes
	if maxInterval <= 0 {
		maxInterval = defaultMaxWakeIntervalMinutes
	}
	ceiling := in.LastSession.Add(time.Duration(maxInterval) * time.Minute)

	next := in.LastSession.Add(time.Duration(in.HeartbeatMinutes) * time.Minute)
	if in.Policy.NextWakeAt != nil {
		next = *in.Policy.NextWakeAt
	}
	if next.After(ceiling) {
		next = ceiling
	}
	return next
}

// partitionEvents splits events into (interrupts, deferred) using the
// domain classifier, honoring a captain-declared override when present.
func partitionEvents(events []*captain.Event, overrideTypes []string) (interrupts, deferred []*captain.Event) {
	override := toEventTypes(overrideTypes)
	for _, e := range events {
		if captain.IsInterrupt(e.Type, override) {
			interrupts = append(interrupts, e)
		} else {
			deferred = append(deferred, e)
		}
	}
	return interrupts, deferred
}

func toEventTypes(ss []string) []captain.EventType {
	if len(ss) == 0 {
		return nil
	}
	out := make([]captain.EventType, len(ss))
	for i, s := range ss {
		out[i] = captain.EventType(s)
	}
	return out
}
