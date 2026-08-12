package parkedsensing

import (
	"context"
	"errors"
	"testing"
	"time"
)

// expansion_probe_yard_test.go pins the third condition on where a seed is staged: EVIDENCE that the
// yard sells the hull we need.
//
// THE DEFECT. ListProbeYards returns yards with a stored SHIP_PROBE listing, and when a system has
// none it falls back to every shipyard-TRAIT waypoint. The fallback fires on "no PROBE row", not on
// "no rows at all" — so a yard we have already priced and found probe-less comes back looking exactly
// like one we have never looked at. Staging then writes a want there, the buy queue scans it, learns
// there is no probe, and the listing memo correctly refuses it for six hours. The want sits on that
// waypoint and nothing is bought.
//
// MEASURED LIVE: of the outstanding SPARE wants, 14 sat at yards NEVER PRICED and 0 at an evidenced
// probe yard — while 8 evidenced probe-selling yards existed elsewhere in the fleet. So the half
// that is biting right now is not "the memo's answer is ignored", it is "evidence is not preferred":
// staging reaches for an unpriced trait yard next door instead of a yard we KNOW sells probes.
//
// THE MEMO IS NOT THE BUG AND IS NOT WEAKENED HERE. It is doing its job. What changes is upstream:
// staging stops choosing yards on a trait guess when evidence is available, and stops choosing ones
// the memo has already answered for.

// The listing fake is buyqueue_test.go's fakeListingMemo, reused rather than reimplemented — the
// engines share one memo contract and a second fake could express a rule the real one does not.
// `scannedAt` holding a key IS "known"; `sells` decides which of the two known answers it gives.

// twoYardWorld: a NEAR system whose only shipyard is a trait guess, and a FAR system holding a yard
// with a stored SHIP_PROBE listing. Both are staffed; the target is routable from both.
//
// Built so evidence and distance DISAGREE — the near yard wins on the hop ordering shipped in
// a7dde668, so a fixture with the evidenced yard nearest could not tell the two rules apart.
//
//	X1-NEAR (trait-only yard) ── X1-TARGET ── X1-FAR (evidenced probe yard)
func twoYardWorld() (*expandHarness, *fakeListingMemo) {
	h := newExpandHarness()
	h.ledger.systems = []ExpandSystem{
		{System: "X1-NEAR", Verdict: VerdictInScope},
		{System: "X1-FAR", Verdict: VerdictInScope},
		{System: "X1-MID", Verdict: VerdictInScope},
		{System: "X1-TARGET", Verdict: VerdictPending, UnchartedCount: 9},
	}
	h.gates.adjacency = map[string][]string{
		"X1-NEAR":   {"X1-TARGET"}, // one hop
		"X1-FAR":    {"X1-MID"},    // two hops
		"X1-MID":    {"X1-TARGET"},
		"X1-TARGET": {},
	}
	h.yards.bySystem = map[string][]string{
		"X1-NEAR": {"X1-NEAR-YARD"},
		"X1-FAR":  {"X1-FAR-YARD"},
	}
	h.ledger.slots = []QueuedSlot{
		staffedYardRow("X1-NEAR", "X1-NEAR-YARD"),
		staffedYardRow("X1-FAR", "X1-FAR-YARD"),
	}
	memo := &fakeListingMemo{
		// X1-NEAR-YARD is absent from scannedAt: never priced, a trait guess. Legitimate as a LAST
		// resort, never as a first choice. X1-FAR-YARD carries evidence: a stored, priced listing.
		sells:     map[string]bool{"X1-FAR-YARD": true},
		scannedAt: map[string]time.Time{"X1-FAR-YARD": time.Now()},
	}
	return h, memo
}

func (h *expandHarness) runWithMemo(t *testing.T, memo *fakeListingMemo) (ExpandReport, error) {
	t.Helper()
	for i := range h.ledger.systems {
		h.ledger.systems[i].CatalogKnown = !h.unswept[h.ledger.systems[i].System]
	}
	p := h.ports()
	p.ListingMemo = memo
	return AdvanceExpansion(context.Background(), p, 1, ExpandKnobs{
		SeedsEnabled: true, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 1.0)
}

// THE TEST THAT MATTERS. An evidenced probe yard two hops out beats a trait-guess yard one hop out.
func TestAdvanceExpansion_StagesAtAnEvidencedProbeYardOverANearerTraitGuess(t *testing.T) {
	h, memo := twoYardWorld()

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1", rep.SeedsRequested)
	}
	if got := h.ledger.upsertedSlots[0].Waypoint; got != "X1-FAR-YARD" {
		t.Fatalf("seed staged at %s, want X1-FAR-YARD — X1-NEAR-YARD is nearer but is only a shipyard "+
			"TRAIT guess, while X1-FAR-YARD carries a stored SHIP_PROBE listing; a want written at a "+
			"yard that sells no probe can never be funded", got)
	}
}

// A YARD THE MEMO HAS ANSWERED PROBE-LESS IS NEVER STAGED ONTO, even when it is nearest and staffed.
//
// This is the standing fact the memo exists to record. Writing a want there produces exactly the
// live failure: the drain scans, learns nothing new, and refuses it for the memo's whole TTL.
func TestAdvanceExpansion_AYardTheMemoKnowsSellsNoProbeIsNeverStagedOnto(t *testing.T) {
	h, memo := twoYardWorld()
	// The near yard has been priced and sells no probe — read just now, so well inside the TTL.
	memo.sells["X1-NEAR-YARD"] = false
	memo.scannedAt["X1-NEAR-YARD"] = time.Now()

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1", rep.SeedsRequested)
	}
	if got := h.ledger.upsertedSlots[0].Waypoint; got != "X1-FAR-YARD" {
		t.Fatalf("seed staged at %s, want X1-FAR-YARD — the memo already recorded that X1-NEAR-YARD "+
			"sells no probe, so a want there is refused for the whole TTL and buys nothing", got)
	}
}

// A STALE probe-less reading is treated as never-read, exactly as the buy queue's own memo rule does.
// A restocked yard must be reconsidered rather than written off for the era.
func TestAdvanceExpansion_AStaleProbelessReadingIsReconsideredNotWrittenOff(t *testing.T) {
	h, _ := twoYardWorld()
	// ONLY the near yard exists, and its probe-less reading is older than the memo's TTL.
	h.yards.bySystem = map[string][]string{"X1-NEAR": {"X1-NEAR-YARD"}}
	h.ledger.slots = []QueuedSlot{staffedYardRow("X1-NEAR", "X1-NEAR-YARD")}
	memo := &fakeListingMemo{
		sells:     map[string]bool{"X1-NEAR-YARD": false},
		scannedAt: map[string]time.Time{"X1-NEAR-YARD": time.Now().Add(-probeListingMemoTTL - time.Minute)},
	}

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — the reading is older than the memo's TTL, so the yard "+
			"is re-checked rather than written off; a restocked counter must be reachable again",
			rep.SeedsRequested)
	}
}

// THE TRAIT FALLBACK SURVIVES as a last resort. A system we have never priced is how the fleet
// LEARNS where probes are sold, so it must still be stageable when no evidenced yard can serve the
// target — just never in preference to one that can.
func TestAdvanceExpansion_AnUnpricedTraitYardIsStillUsedWhenNoEvidencedOneExists(t *testing.T) {
	h, _ := twoYardWorld()
	h.yards.bySystem = map[string][]string{"X1-NEAR": {"X1-NEAR-YARD"}}
	h.ledger.slots = []QueuedSlot{staffedYardRow("X1-NEAR", "X1-NEAR-YARD")}
	memo := &fakeListingMemo{sells: map[string]bool{}, scannedAt: map[string]time.Time{}}

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — an unpriced yard is the only way the fleet learns "+
			"where probes are sold; ranking it last must not mean removing it", rep.SeedsRequested)
	}
	if got := h.ledger.upsertedSlots[0].Waypoint; got != "X1-NEAR-YARD" {
		t.Fatalf("seed staged at %s, want X1-NEAR-YARD", got)
	}
}

// WITHIN the evidenced tier the hop ordering from a7dde668 still decides, so this adds a condition
// rather than replacing the routability selection.
func TestAdvanceExpansion_NearestStillWinsAmongEvidencedYards(t *testing.T) {
	h, memo := twoYardWorld()
	// The NEAR yard is now evidenced too, so both tiers collapse and distance decides again.
	memo.sells["X1-NEAR-YARD"] = true
	memo.scannedAt["X1-NEAR-YARD"] = time.Now()

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1", rep.SeedsRequested)
	}
	if got := h.ledger.upsertedSlots[0].Waypoint; got != "X1-NEAR-YARD" {
		t.Fatalf("seed staged at %s, want X1-NEAR-YARD — both yards are evidenced, so the nearest-first "+
			"routability ordering must still decide", got)
	}
}

// AN UNREADABLE MEMO FAILS THE TICK rather than staging blind.
//
// The fake answers "known, sells a probe" alongside its error, so an engine that swallows it stages
// onto a yard it never actually read — writing exactly the unfundable want this lane removes.
func TestAdvanceExpansion_AnUnreadableListingMemoFailsTheTick(t *testing.T) {
	h, memo := twoYardWorld()
	memo.err = errors.New("shipyard inventory unhappy")

	_, err := h.runWithMemo(t, memo)
	if err == nil {
		t.Fatalf("the tick succeeded on an unreadable listing memo — a yard we could not read must " +
			"never be staged onto as though we had")
	}
	for _, slot := range h.ledger.upsertedSlots {
		if slot.Kind == SlotKindSpare {
			t.Fatalf("staged %v off an unreadable memo read", slot)
		}
	}
}

// AN UNWIRED MEMO leaves staging exactly as it was, so a deployment without the port behaves as it
// did before this change rather than refusing to stage at all.
func TestAdvanceExpansion_AnUnwiredListingMemoLeavesStagingUnchanged(t *testing.T) {
	h, _ := twoYardWorld()

	for i := range h.ledger.systems {
		h.ledger.systems[i].CatalogKnown = !h.unswept[h.ledger.systems[i].System]
	}
	p := h.ports() // ListingMemo left nil
	rep, err := AdvanceExpansion(context.Background(), p, 1, ExpandKnobs{
		SeedsEnabled: true, MinBudgetRate: 0.05, Whitelist: h.whitelist,
	}, 1.0)
	if err != nil {
		t.Fatalf("an unwired memo must not fail the tick: %v", err)
	}
	if rep.SeedsRequested != 1 {
		t.Fatalf("SeedsRequested = %d, want 1 — without the memo the nearest staffed yard is chosen, "+
			"exactly as before this change", rep.SeedsRequested)
	}
	if got := h.ledger.upsertedSlots[0].Waypoint; got != "X1-NEAR-YARD" {
		t.Fatalf("seed staged at %s, want X1-NEAR-YARD (the pre-change choice)", got)
	}
}

// A MEMO-ANSWERED PROBE-LESS YARD IS STAGED ONTO BY NEITHER PASS — not even when it is the ONLY
// yard in reach. Nothing is written, and the tick carries on.
//
// THE FIXTURE HAS NO EVIDENCED YARD ANYWHERE, and that is the whole point of it existing separately
// from AYardTheMemoKnowsSellsNoProbeIsNeverStagedOnto. With an evidenced yard present the FIRST pass
// picks it and the fallback pass is never reached, so that test cannot see what the fallback admits —
// a mutation letting probeStockNone through the fallback survived it. This one fails.
func TestAdvanceExpansion_AProbelessYardIsNotStagedEvenAsALastResort(t *testing.T) {
	h, _ := twoYardWorld()
	h.yards.bySystem = map[string][]string{"X1-NEAR": {"X1-NEAR-YARD"}}
	h.ledger.slots = []QueuedSlot{staffedYardRow("X1-NEAR", "X1-NEAR-YARD")}
	memo := &fakeListingMemo{
		sells:     map[string]bool{"X1-NEAR-YARD": false},
		scannedAt: map[string]time.Time{"X1-NEAR-YARD": time.Now()}, // read just now: inside the TTL
	}

	rep, err := h.runWithMemo(t, memo)
	if err != nil {
		t.Fatalf("a probe-less yard must be skipped quietly, not fail the tick: %v", err)
	}
	if rep.SeedsRequested != 0 {
		t.Fatalf("SeedsRequested = %d, want 0 — the only yard in reach is one the memo has already "+
			"recorded as selling no probe; a want written there is scanned, learns nothing, and is "+
			"refused for the whole TTL, which is the exact live failure this lane removes",
			rep.SeedsRequested)
	}
	for _, slot := range h.ledger.upsertedSlots {
		if slot.Kind == SlotKindSpare {
			t.Fatalf("staged %v onto a yard known to sell no probe", slot)
		}
	}
}
