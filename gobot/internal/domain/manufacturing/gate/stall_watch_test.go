package gate

import (
	"strings"
	"testing"
	"time"
)

// sp-63r4f: MAKE THE SILENCE LOUD.
//
// Three defects this week produced the same symptom — the gate stopped and every log line stayed
// true and reassuring. Roughly 10 of 14 hours stalled, and every one was found by a human asking
// why, not by anything saying so. These pin a check that fires on the ABSENCE of progress, without
// needing to know the cause in advance.

var watchNow = time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

func windowStart() time.Time { return watchNow.Add(-24 * time.Hour) }

// CRITERION 1 + 3 + 4: an unmet material with zero deliveries past the threshold escalates, and the
// escalation reports what was OBSERVED.
func TestDetectStalls_FiresOnAnUnmetMaterialWithNoDeliveries(t *testing.T) {
	stalls := DetectStalls(watchNow, windowStart(), []MaterialProgress{{
		Good: "FAB_MATS", Remaining: 165, UnitsDelivered: 0,
		LastDeliveryAt: watchNow.Add(-90 * time.Minute), SourceSupply: "SCARCE",
	}}, StallThreshold)

	if len(stalls) != 1 {
		t.Fatalf("stalls = %+v; an unmet material with zero units in 90 minutes is the exact shape of all three incidents", stalls)
	}
	v := stalls[0]
	if v.StalledFor < 90*time.Minute {
		t.Fatalf("StalledFor = %s, want the full 90 minutes since the last delivery", v.StalledFor)
	}

	line := v.LogLine("X1-KP46-I55")
	// CRITERION 4: every fact in the line must be one the watchdog MEASURED.
	for _, want := range []string{"GATE STALLED", "FAB_MATS", "X1-KP46-I55", "0 units", "1h30m0s", "165", "SCARCE"} {
		if !strings.Contains(line, want) {
			t.Fatalf("the escalation omits %q — it must name the material, the site, the units, the duration, the outstanding bill and the source supply: %q", want, line)
		}
	}
	// CRITERION 4, THE NEGATIVE HALF: it must not name a cause. Three causes produced identical
	// silence, so a guess would have been wrong two times in three.
	for _, mustNot := range []string{"unaffordable", "head-of-line", "hysteresis", "because"} {
		if strings.Contains(strings.ToLower(line), mustNot) {
			t.Fatalf("the escalation guesses a cause (%q): %q", mustNot, line)
		}
	}
}

// CRITERION 5, THE REGRESSION THAT MATTERS: a pipeline making slow but REAL progress must stay
// silent, or the watchdog becomes the noise it exists to cut through.
func TestDetectStalls_ATrickleOfProgressIsNotAStall(t *testing.T) {
	stalls := DetectStalls(watchNow, windowStart(), []MaterialProgress{{
		Good: "FAB_MATS", Remaining: 165, UnitsDelivered: 1, // one single unit in the window
		LastDeliveryAt: watchNow.Add(-8 * time.Hour), SourceSupply: "LIMITED",
	}}, StallThreshold)

	if len(stalls) != 0 {
		t.Fatalf("stalls = %+v; ONE unit delivered is slow, not stopped. Firing here trains the reader to ignore the line, which is how the real stalls hid", stalls)
	}
}

// CRITERION 1: a MET material is never stalled, however long it has been quiet. Silence on a
// finished bill is success.
func TestDetectStalls_AMetMaterialIsNeverStalled(t *testing.T) {
	stalls := DetectStalls(watchNow, windowStart(), []MaterialProgress{{
		Good: "ADVANCED_CIRCUITRY", Remaining: 0, UnitsDelivered: 0,
		LastDeliveryAt: watchNow.Add(-12 * time.Hour),
	}}, StallThreshold)

	if len(stalls) != 0 {
		t.Fatalf("stalls = %+v; a satisfied material that receives nothing is CORRECT, and reporting it would bury the real signal", stalls)
	}
}

// The threshold is honoured in full: a pipeline mid-leg must not be accused.
func TestDetectStalls_DoesNotFireBeforeTheThreshold(t *testing.T) {
	stalls := DetectStalls(watchNow, windowStart(), []MaterialProgress{{
		Good: "FAB_MATS", Remaining: 165, UnitsDelivered: 0,
		LastDeliveryAt: watchNow.Add(-StallThreshold + time.Minute),
	}}, StallThreshold)

	if len(stalls) != 0 {
		t.Fatalf("fired at %s, inside the threshold; a single supply leg may legitimately take that long and a buy floor may idle while a market regenerates", StallThreshold-time.Minute)
	}
}

// CRITERION 6: THE CLOCK MUST SURVIVE A RESTART.
//
// This is the sp-20eyn shape — 34,279 restarts that looked like recovery. A watchdog holding its
// clock in memory resets on every bounce, so a restart loop hides the stall completely.
//
// The design answer is that there IS no in-memory clock: LastDeliveryAt comes from the persisted
// task history, so a fresh process computes the SAME duration as one that has been up for hours.
// This asserts that property directly — two identical calls, one standing for a long-lived process
// and one for a process that just started, must agree.
func TestDetectStalls_TheDurationDoesNotDependOnProcessUptime(t *testing.T) {
	lastDelivery := watchNow.Add(-7*time.Hour - 30*time.Minute)
	observed := []MaterialProgress{{Good: "FAB_MATS", Remaining: 165, UnitsDelivered: 0, LastDeliveryAt: lastDelivery}}

	longLived := DetectStalls(watchNow, watchNow.Add(-24*time.Hour), observed, StallThreshold)
	// A process that started ten seconds ago can still see the same persisted row.
	justRestarted := DetectStalls(watchNow, watchNow.Add(-10*time.Second), observed, StallThreshold)

	if len(longLived) != 1 || len(justRestarted) != 1 {
		t.Fatalf("a 7.5-hour stall must be visible to BOTH a long-lived and a freshly-restarted process: longLived=%d justRestarted=%d", len(longLived), len(justRestarted))
	}
	if longLived[0].StalledFor != justRestarted[0].StalledFor {
		t.Fatalf("the reported stall is %s to a long-lived process but %s to a restarted one. A clock that resets on restart means a restart loop hides the stall entirely — the sp-20eyn failure",
			longLived[0].StalledFor, justRestarted[0].StalledFor)
	}
}

// A material that has NEVER received a delivery is judged from the start of the observation window,
// not from the zero time — otherwise a young pipeline is accused of a stall measured in millennia.
func TestDetectStalls_AMaterialThatNeverDeliveredIsJudgedFromTheWindow(t *testing.T) {
	// Window opened 20 minutes ago: inside the threshold, so nothing should fire yet.
	young := DetectStalls(watchNow, watchNow.Add(-20*time.Minute), []MaterialProgress{{
		Good: "FAB_MATS", Remaining: 1600, UnitsDelivered: 0,
	}}, StallThreshold)
	if len(young) != 0 {
		t.Fatalf("stalls = %+v; a pipeline whose window opened 20 minutes ago has not had a chance to deliver, and a zero LastDeliveryAt must not read as a stall since the epoch", young)
	}

	// Same material, window opened well past the threshold: now it is a genuine stall.
	old := DetectStalls(watchNow, watchNow.Add(-3*time.Hour), []MaterialProgress{{
		Good: "FAB_MATS", Remaining: 1600, UnitsDelivered: 0,
	}}, StallThreshold)
	if len(old) != 1 {
		t.Fatal("a material that has delivered nothing for three hours IS stalled, whether or not it ever delivered before")
	}
	if old[0].StalledFor != 3*time.Hour {
		t.Fatalf("StalledFor = %s, want the 3h window the caller can actually speak for", old[0].StalledFor)
	}
}

// ONLY THE STALLED MATERIALS ARE RETURNED, so a mixed pipeline reports the one that is stuck rather
// than burying it among the healthy ones.
func TestDetectStalls_ReportsOnlyTheStalledMaterialInAMixedPipeline(t *testing.T) {
	stalls := DetectStalls(watchNow, windowStart(), []MaterialProgress{
		{Good: "ADVANCED_CIRCUITRY", Remaining: 320, UnitsDelivered: 40, LastDeliveryAt: watchNow.Add(-5 * time.Minute)},
		{Good: "FAB_MATS", Remaining: 165, UnitsDelivered: 0, LastDeliveryAt: watchNow.Add(-8 * time.Hour)},
		{Good: "QUANTUM_STABILIZERS", Remaining: 0, UnitsDelivered: 0},
	}, StallThreshold)

	if len(stalls) != 1 || stalls[0].Good != "FAB_MATS" {
		t.Fatalf("stalls = %+v, want exactly the stalled FAB_MATS. A pipeline is stalled per MATERIAL: the gate sat on FAB_MATS while ADVANCED_CIRCUITRY was fine, and an all-or-nothing check would have missed it", stalls)
	}
}

// The threshold is the sp-c9wuu derivation reused, not a second number answering the same question.
func TestStallThreshold_MatchesTheSupplyTaskDerivation(t *testing.T) {
	const supplyTaskLifetime = 30 * time.Minute
	if StallThreshold != 2*supplyTaskLifetime {
		t.Fatalf("StallThreshold is %s; it is two supply-task lifetimes (2 x %s) — one for a leg that may legitimately be in flight, one for the gap between legs while a market regenerates",
			StallThreshold, supplyTaskLifetime)
	}
	if StallThreshold >= 90*time.Minute {
		t.Fatalf("StallThreshold is %s, which would not have fired inside the shortest of the three incidents (90 minutes)", StallThreshold)
	}
}
