package commands

import (
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// hullQuarantineMessage is the loud, human-readable line the captain sees when a hull is
// quarantined. It names the hull, the evidence, and WHEN the hull comes back, so a mispinned probe,
// a hull stuck in a bad local state and a hull unreadable upstream are told apart from the event
// alone — and so nobody reads a quarantine as a permanent write-off.
func hullQuarantineMessage(hull string, outcome spawnOutcome) string {
	evidence := fmt.Sprintf("%d instant worker deaths — check hull class/state", outcome.InstantDeaths)
	if outcome.Cause == quarantineCauseRepeatedError {
		evidence = fmt.Sprintf(
			"%d consecutive workers failed with the SAME error — check the hull upstream (unreadable ship, stuck server-side state)",
			outcome.IdenticalErrors)
	}
	return fmt.Sprintf(
		"hull %s quarantined: %s (skipped for %s, then re-probed with one worker)",
		hull, evidence, outcome.Cooldown)
}

// buildHullQuarantineEvent constructs the ONE loud captain event emitted when a hull crosses into
// spawn quarantine. It reuses the interrupt-class coordinator.error_loop type rather than minting a
// new one: a quarantine IS a coordinator detecting its own repeated-failure loop and refusing to
// keep feeding it, exactly the family EventCoordinatorErrorLoop models, and that type is already
// interrupt-class so the signal is never silently deferred. The Ship field stays container-scoped
// by the same convention health.NewErrorLoopEvent follows (the coordinator has no ship of its own);
// the affected HULL rides both the human message and a structured payload field so consumers can
// key on it without parsing prose. Pure and deterministic, so it is unit-testable without a real
// EventRecorder.
//
// `cause` and `identical_errors` sit alongside `instant_deaths` because the two quarantine causes
// have completely different remedies — a bad hull class versus a hull the API cannot read — and a
// consumer that cannot see which one fired cannot triage.
func buildHullQuarantineEvent(containerID string, playerID int, hull string, outcome spawnOutcome) *captain.Event {
	payload, err := json.Marshal(map[string]any{
		"container_id":     containerID,
		"checkpoint":       "hull_quarantine",
		"hull":             hull,
		"cause":            outcome.Cause,
		"instant_deaths":   outcome.InstantDeaths,
		"identical_errors": outcome.IdenticalErrors,
		"cooldown_seconds": outcome.Cooldown.Seconds(),
		"message":          hullQuarantineMessage(hull, outcome),
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
