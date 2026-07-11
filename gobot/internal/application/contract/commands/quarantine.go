package commands

import (
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// hullQuarantineMessage is the loud, human-readable line the captain sees when
// a hull is quarantined for repeated instant worker deaths (sp-lybx). It names
// the hull and the count and points at the likeliest cause, so a mispinned
// probe or a hull stuck in a bad state is diagnosable from the event alone.
func hullQuarantineMessage(hull string, instantDeaths int) string {
	return fmt.Sprintf(
		"hull %s quarantined: %d instant worker deaths — check hull class/state (skipped for the rest of this coordinator run)",
		hull, instantDeaths)
}

// buildHullQuarantineEvent constructs the ONE loud captain event emitted when a
// hull crosses into spawn quarantine (sp-lybx). It reuses the interrupt-class
// coordinator.error_loop type rather than minting a new one: a quarantine IS a
// coordinator detecting its own repeated-failure loop and refusing to keep
// feeding it, exactly the family EventCoordinatorErrorLoop already models, and
// the type is already interrupt-class so the signal is never silently deferred.
// The Ship field stays container-scoped by the same convention health.NewErrorLoopEvent
// follows (the coordinator has no single ship of its own); the affected HULL is
// carried both in the human message and as a structured payload field so
// consumers can key on it without parsing prose. Pure and deterministic, so it
// is unit-testable without a real EventRecorder.
func buildHullQuarantineEvent(containerID string, playerID int, hull string, instantDeaths int) *captain.Event {
	payload, err := json.Marshal(map[string]any{
		"container_id":   containerID,
		"checkpoint":     "hull_quarantine",
		"hull":           hull,
		"instant_deaths": instantDeaths,
		"message":        hullQuarantineMessage(hull, instantDeaths),
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
