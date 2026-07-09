package commands

import (
	"encoding/json"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// coordinatorErrorStreakThreshold is how many consecutive identical errors
// at one checkpoint trigger a captain event — and every further multiple of
// this re-triggers one, so a loop that is still stuck resurfaces
// periodically instead of alarming only once and going quiet again. At the
// fleet coordinator's fastest retry interval (10s), 5 crosses in well under
// a minute — far faster than the 18h the 2026-07-05 negotiate-nil incident
// ran silently — while still tolerating a handful of transient blips
// without alarming (sp-e2l1).
const coordinatorErrorStreakThreshold = 5

// errorStreakTracker counts consecutive identical-error occurrences at a
// single retry checkpoint and reports when the streak crosses a new
// multiple of the configured threshold. It is the primitive behind
// sp-e2l1: a coordinator retry loop that silently repeats the same error
// forever must become observable without emitting an event on every single
// iteration (edge-triggered, not per-iteration).
type errorStreakTracker struct {
	threshold int
	lastErr   string
	count     int
}

// newErrorStreakTracker returns a tracker that reports a crossing every
// threshold consecutive identical errors. threshold <= 0 disables
// crossings entirely (Note still tracks the streak length, but crossed is
// always false).
func newErrorStreakTracker(threshold int) *errorStreakTracker {
	return &errorStreakTracker{threshold: threshold}
}

// Note records this checkpoint's outcome for one loop iteration. Pass ""
// for errMsg to record a success. It returns the current
// consecutive-identical-error streak length and whether this call is the
// exact iteration the streak crossed a new multiple of the threshold
// (true only when count == threshold, 2*threshold, 3*threshold, ...).
//
// A success, or an errMsg that differs from the previous call's, resets the
// streak — so an intermittent/recovering loop, or a loop alternating
// between two distinct bugs, never falsely crosses.
func (t *errorStreakTracker) Note(errMsg string) (streak int, crossed bool) {
	if errMsg == "" {
		t.lastErr = ""
		t.count = 0
		return 0, false
	}
	if errMsg == t.lastErr {
		t.count++
	} else {
		t.lastErr = errMsg
		t.count = 1
	}
	crossed = t.threshold > 0 && t.count%t.threshold == 0
	return t.count, crossed
}

// coordinatorErrorMonitor tracks independent error streaks for multiple
// named checkpoints within a single coordinator loop invocation. Each
// checkpoint gets its own errorStreakTracker so a success or failure at one
// checkpoint never masks or contaminates a different checkpoint's ongoing
// streak, even though every checkpoint gets Noted on every loop iteration.
type coordinatorErrorMonitor struct {
	threshold int
	sites     map[string]*errorStreakTracker
}

// newCoordinatorErrorMonitor returns a monitor whose checkpoints each
// report a crossing every threshold consecutive identical errors.
func newCoordinatorErrorMonitor(threshold int) *coordinatorErrorMonitor {
	return &coordinatorErrorMonitor{threshold: threshold, sites: make(map[string]*errorStreakTracker)}
}

// Note records the outcome of one loop iteration at the named checkpoint
// (site), creating that checkpoint's tracker on first use. See
// errorStreakTracker.Note for the return semantics.
func (m *coordinatorErrorMonitor) Note(site, errMsg string) (streak int, crossed bool) {
	t, ok := m.sites[site]
	if !ok {
		t = newErrorStreakTracker(m.threshold)
		m.sites[site] = t
	}
	return t.Note(errMsg)
}

// buildErrorLoopEvent constructs the captain event to record when a
// coordinator checkpoint's error streak crosses a threshold multiple
// (sp-e2l1). Pure and deterministic, so it is fully unit-testable without a
// real EventRecorder; the Ship field carries the coordinator's own
// container id (this event is container-scoped, not ship-scoped — the
// fleet coordinator has no single ship of its own).
func buildErrorLoopEvent(containerID string, playerID int, checkpoint string, cause error, streak int) *captain.Event {
	payload, err := json.Marshal(map[string]any{
		"container_id": containerID,
		"checkpoint":   checkpoint,
		"error":        cause.Error(),
		"streak":       streak,
	})
	if err != nil {
		payload = []byte("{}")
	}
	return &captain.Event{
		Type:     captain.EventCoordinatorErrorLoop,
		Ship:     containerID,
		PlayerID: playerID,
		Payload:  string(payload),
	}
}
