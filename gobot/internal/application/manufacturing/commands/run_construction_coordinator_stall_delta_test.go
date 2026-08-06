package commands

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
)

// sp-zx0tu: THE DELIVERY SIGNAL IS THE REMAINING COUNT, NOT TASK BOOKKEEPING.
//
// TORWINDSTG-B carried 28 units into X1-KP46-I55 and the gate went 1435 -> 1463/1600. The delivery
// completed NO task, so the repaired actual_quantity write never fired and the watchdog kept
// climbing — while its own ERROR line went "still needs 165" -> "still needs 137" across the very
// same delivery. The authoritative figure was already in hand, one field away from the claim that
// contradicted it.

// stalledFor returns the reported stall for good, and whether it was reported at all.
func stalledFor(stalls []gate.StallVerdict, good string) (time.Duration, bool) {
	for _, v := range stalls {
		if v.Good == good {
			return v.StalledFor, true
		}
	}
	return 0, false
}

// CRITERION 1, THE INCIDENT ITSELF: remaining 165 -> 137 must clear the alarm.
func TestWatchGateProgress_AFallingRemainingCountClearsTheStall(t *testing.T) {
	h := &RunConstructionCoordinatorHandler{}
	const pipelineID, good = "PIPE-1", "FAB_MATS"
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)

	// Tick 1 seeds the baseline at 165.
	h.observeRemaining(pipelineID, good, 165, start)

	// It sits at 165 for well past the threshold: a genuine stall builds.
	stalled := start.Add(gate.StallThreshold + 30*time.Minute)
	delivered, lastChanged := h.observeRemaining(pipelineID, good, 165, stalled)
	if delivered != 0 {
		t.Fatalf("a flat remaining count reported %d units delivered", delivered)
	}
	if quiet := stalled.Sub(lastChanged); quiet < gate.StallThreshold {
		t.Fatalf("quiet = %s after sitting flat past the threshold — a flat count must accumulate", quiet)
	}

	// THE DELIVERY: 28 units land, the requirement falls to 137, no task completes.
	landed := stalled.Add(time.Minute)
	delivered, lastChanged = h.observeRemaining(pipelineID, good, 137, landed)

	if delivered != 28 {
		t.Fatalf("reported %d units delivered for a 165 -> 137 fall; the drop in the live requirement IS the delivery, whatever path moved the cargo and whether or not any task completed", delivered)
	}
	if quiet := landed.Sub(lastChanged); quiet != 0 {
		t.Fatalf("still reporting %s of quiet immediately after a 28-unit delivery — this is the false alarm: 30.8 hours claimed while the gate advanced 1435 -> 1463", quiet)
	}
}

// CRITERION 2: a FLAT remaining count across the threshold must still report. The fix must not be a
// disabled alarm.
func TestWatchGateProgress_AFlatRemainingCountStillReportsAStall(t *testing.T) {
	h := &RunConstructionCoordinatorHandler{}
	const pipelineID, good = "PIPE-1", "FAB_MATS"
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)
	now := start.Add(gate.StallThreshold + time.Hour)

	h.observeRemaining(pipelineID, good, 165, start)
	_, lastChanged := h.observeRemaining(pipelineID, good, 165, now)

	stalls := gate.DetectStalls(now, start, []gate.MaterialProgress{{
		Good: good, Remaining: 165, UnitsDelivered: 0, LastDeliveryAt: lastChanged,
	}}, gate.StallThreshold)

	quiet, reported := stalledFor(stalls, good)
	if !reported {
		t.Fatal("no stall reported for a requirement that has not moved in over an hour — fixing the false alarm must not silence the true one")
	}
	if quiet < gate.StallThreshold {
		t.Fatalf("reported only %s of quiet for a count flat since %s", quiet, start)
	}
}

// CRITERION 3: the FIRST tick after a restart must not raise an alarm from a missing memory.
//
// The observation ledger is in-memory and per-process, so a restart re-seeds it. With no prior tick
// there is no basis to claim anything has been quiet, and "stalled since the epoch" would be the
// worst possible cold-start behaviour for an alarm nobody has learned to trust yet.
func TestWatchGateProgress_TheFirstTickAfterARestartReportsNoStall(t *testing.T) {
	// A brand-new handler stands for a freshly restarted daemon: stallSeen is empty.
	restarted := &RunConstructionCoordinatorHandler{}
	const pipelineID, good = "PIPE-1", "FAB_MATS"
	// The pipeline has been running for days and the material has been stuck the whole time.
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

	delivered, lastChanged := restarted.observeRemaining(pipelineID, good, 165, now)

	if delivered != 0 {
		t.Fatalf("a first observation invented %d units of progress", delivered)
	}
	if quiet := now.Sub(lastChanged); quiet != 0 {
		t.Fatalf("the first tick after a restart reported %s of quiet from an empty ledger. A cold start must read as 'no evidence', never as a stall", quiet)
	}

	stalls := gate.DetectStalls(now, now.Add(-72*time.Hour), []gate.MaterialProgress{{
		Good: good, Remaining: 165, UnitsDelivered: delivered, LastDeliveryAt: lastChanged,
	}}, gate.StallThreshold)
	if _, reported := stalledFor(stalls, good); reported {
		t.Fatal("the first post-restart tick raised a stall from a missing prior observation")
	}
}

// A RISING requirement is not a delivery, and must not be reported as one — but it re-baselines, so
// the next genuine fall is measured from the new figure rather than as a phantom.
func TestWatchGateProgress_ARisingRequirementIsNotProgress(t *testing.T) {
	h := &RunConstructionCoordinatorHandler{}
	const pipelineID, good = "PIPE-1", "FAB_MATS"
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)

	h.observeRemaining(pipelineID, good, 137, start)
	// The site's bill is corrected upward.
	if delivered, _ := h.observeRemaining(pipelineID, good, 200, start.Add(time.Minute)); delivered != 0 {
		t.Fatalf("a RISING requirement reported %d units delivered", delivered)
	}
	// A real 20-unit delivery against the new baseline reads as 20, not as 137 - 180.
	if delivered, _ := h.observeRemaining(pipelineID, good, 180, start.Add(2*time.Minute)); delivered != 20 {
		t.Fatalf("reported %d units after 200 -> 180; the baseline must follow the raised bill or the next fall is a phantom", delivered)
	}
}

// PER MATERIAL AND PER PIPELINE: one material's progress must not clear another's stall.
func TestWatchGateProgress_ObservationsAreKeyedPerPipelineAndMaterial(t *testing.T) {
	h := &RunConstructionCoordinatorHandler{}
	start := time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)

	h.observeRemaining("PIPE-1", "FAB_MATS", 165, start)
	h.observeRemaining("PIPE-1", "ADVANCED_CIRCUITRY", 320, start)
	h.observeRemaining("PIPE-2", "FAB_MATS", 900, start)

	later := start.Add(gate.StallThreshold + time.Minute)
	// Only ADVANCED_CIRCUITRY moves.
	h.observeRemaining("PIPE-1", "ADVANCED_CIRCUITRY", 280, later)

	_, fabChanged := h.observeRemaining("PIPE-1", "FAB_MATS", 165, later)
	if quiet := later.Sub(fabChanged); quiet < gate.StallThreshold {
		t.Fatalf("PIPE-1 FAB_MATS quiet collapsed to %s when a SIBLING material was delivered — one material's progress must not clear another's stall", quiet)
	}
	_, otherChanged := h.observeRemaining("PIPE-2", "FAB_MATS", 900, later)
	if quiet := later.Sub(otherChanged); quiet < gate.StallThreshold {
		t.Fatalf("PIPE-2 FAB_MATS quiet collapsed to %s when the SAME material moved in another pipeline", quiet)
	}
}
