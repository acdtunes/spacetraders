package parkedsensing

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// --- fakes -------------------------------------------------------------------
//
// Both reads fail ADVERSARIALLY — each returns, alongside its error, the value
// that would do the most damage if the reaper read it anyway:
//
//   - Systems returns an EMPTY verdict map. Empty means "no system is IN_SCOPE",
//     which is the reaper's own reap condition — so a swallowed error would
//     revert every claim in the ledger at once.
//   - SlotsByState returns the stranded claims. A swallowed error would reap
//     them off a list the ledger never actually vouched for.
//
// transitionCall and the transitionsTo helper are shared with buyqueue_test.go.

type fakeReapLedger struct {
	systems []ExpandSystem
	slots   []QueuedSlot

	systemsErr, slotsErr error
	// transitionErr is keyed on waypoint: one stranded row can be made to lose
	// the race while its neighbours are reaped normally.
	transitionErr map[string]error

	transitions  []transitionCall
	systemsCalls int
	statesAsked  [][]string
}

func (f *fakeReapLedger) Systems(_ context.Context, _ int) ([]ExpandSystem, error) {
	f.systemsCalls++
	if f.systemsErr != nil {
		return []ExpandSystem{}, f.systemsErr // adversarial: an empty map reads as "nothing in scope"
	}
	return f.systems, nil
}

func (f *fakeReapLedger) SlotsByState(_ context.Context, _ int, states ...string) ([]QueuedSlot, error) {
	f.statesAsked = append(f.statesAsked, states)
	want := make(map[string]bool, len(states))
	for _, s := range states {
		want[s] = true
	}
	var out []QueuedSlot
	for _, s := range f.slots {
		if want[s.State] {
			out = append(out, s)
		}
	}
	if f.slotsErr != nil {
		return out, f.slotsErr // adversarial: the stranded rows are handed over anyway
	}
	return out, nil
}

func (f *fakeReapLedger) TransitionSlot(_ context.Context, _ int, waypoint, kind, from, to string, set SlotFields) error {
	f.transitions = append(f.transitions, transitionCall{waypoint, from, to, set.AssignedShip, set.PurchaseYard})
	if err := f.transitionErr[waypoint]; err != nil {
		return err
	}
	for i := range f.slots {
		if f.slots[i].Waypoint != waypoint || f.slots[i].Kind != kind {
			continue
		}
		if f.slots[i].State != from {
			return fmt.Errorf("%s: %w", waypoint, ErrSlotClaimed)
		}
		f.slots[i].State = to
		if set.AssignedShip != nil {
			f.slots[i].AssignedShip = *set.AssignedShip
		}
		if set.PurchaseYard != nil {
			f.slots[i].PurchaseYard = *set.PurchaseYard
		}
		return nil
	}
	return errors.New("no such slot")
}

func (f *fakeReapLedger) slotAt(waypoint string) QueuedSlot {
	for _, s := range f.slots {
		if s.Waypoint == waypoint {
			return s
		}
	}
	return QueuedSlot{}
}

func reapPortsFor(led *fakeReapLedger) ReapPorts { return ReapPorts{Ledger: led} }

// --- the verdict filter -------------------------------------------------------

// A placement claimed for purchase whose system then LOST its IN_SCOPE verdict is
// stranded: the drain reads WANTED and QUEUED together but only inside IN_SCOPE
// systems, so the row is never drained and never released. The reaper hands the
// claim back, and drops the purchase yard with it — the yard was chosen for a
// system we no longer want watched.
//
// Every non-IN_SCOPE verdict is reaped, not just the rejection: a system knocked
// back to PENDING is equally undrainable, and a reaper keyed on NO_WHITELIST
// would leave those claims squatting forever.
func TestReap_RevertsStrandedClaims(t *testing.T) {
	for _, verdict := range []string{VerdictNoWhitelist, VerdictPending} {
		t.Run(verdict, func(t *testing.T) {
			led := &fakeReapLedger{
				systems: []ExpandSystem{{System: "X1-GONE", Verdict: verdict}},
				slots: []QueuedSlot{{
					Waypoint: "X1-GONE-M1", System: "X1-GONE", Kind: SlotKindMarket,
					State: SlotStateQueued, PurchaseYard: "X1-GONE-Y1",
				}},
			}

			rep, err := ReapStrandedClaims(context.Background(), reapPortsFor(led), testPlayerID, 0)
			if err != nil {
				t.Fatalf("ReapStrandedClaims returned error: %v", err)
			}
			if rep.Reaped != 1 || rep.Skipped != 0 {
				t.Fatalf("report says Reaped=%d Skipped=%d, want 1 and 0", rep.Reaped, rep.Skipped)
			}
			if len(led.transitions) != 1 {
				t.Fatalf("made %d transitions, want 1: %+v", len(led.transitions), led.transitions)
			}

			got := led.transitions[0]
			if got.waypoint != "X1-GONE-M1" || got.from != SlotStateQueued || got.to != SlotStateWanted {
				t.Fatalf("transition was %+v, want X1-GONE-M1 QUEUED→WANTED", got)
			}
			if got.purchaseYard == nil || *got.purchaseYard != "" {
				t.Fatalf("purchase yard was %v, want a pointer to \"\" (the stale yard cleared)", got.purchaseYard)
			}
			if got.assignedShip != nil {
				t.Fatalf("transition named assigned_ship (%v); a QUEUED row is shipless and the column is not the reaper's to write", *got.assignedShip)
			}
			if slot := led.slotAt("X1-GONE-M1"); slot.State != SlotStateWanted || slot.PurchaseYard != "" {
				t.Fatalf("slot is %+v, want WANTED with no purchase yard", slot)
			}
		})
	}
}

// A claim in a system still IN_SCOPE is the buy queue's to work: the drain reads
// QUEUED rows there every tick and retries the purchase. Reaping it would fight
// the drain for the row every tick and never let a placement past the claim.
func TestReap_LeavesInScopeClaimsAlone(t *testing.T) {
	led := &fakeReapLedger{
		systems: []ExpandSystem{{System: "X1-LIVE", Verdict: VerdictInScope}},
		slots: []QueuedSlot{{
			Waypoint: "X1-LIVE-M1", System: "X1-LIVE", Kind: SlotKindMarket,
			State: SlotStateQueued, PurchaseYard: "X1-LIVE-Y1",
		}},
	}

	rep, err := ReapStrandedClaims(context.Background(), reapPortsFor(led), testPlayerID, 0)
	if err != nil {
		t.Fatalf("ReapStrandedClaims returned error: %v", err)
	}
	if rep.Reaped != 0 || rep.Skipped != 0 {
		t.Fatalf("report says Reaped=%d Skipped=%d, want both 0", rep.Reaped, rep.Skipped)
	}
	if len(led.transitions) != 0 {
		t.Fatalf("touched an IN_SCOPE claim: %+v", led.transitions)
	}
}

// A slot whose system carries NO row at all is stranded exactly as hard as one
// that was rejected — the drain's IN_SCOPE lookup misses both — so the absent
// row is read as "not in scope" rather than as an unknown to be left alone.
func TestReap_ReapsClaimWithNoSystemRow(t *testing.T) {
	led := &fakeReapLedger{
		systems: []ExpandSystem{{System: "X1-ELSEWHERE", Verdict: VerdictInScope}},
		slots: []QueuedSlot{{
			Waypoint: "X1-ORPHAN-M1", System: "X1-ORPHAN", Kind: SlotKindMarket, State: SlotStateQueued,
		}},
	}

	rep, err := ReapStrandedClaims(context.Background(), reapPortsFor(led), testPlayerID, 0)
	if err != nil {
		t.Fatalf("ReapStrandedClaims returned error: %v", err)
	}
	if rep.Reaped != 1 {
		t.Fatalf("report says Reaped=%d, want 1 (a system with no verdict row is not in scope)", rep.Reaped)
	}
	if len(led.transitions) != 1 || led.transitions[0].waypoint != "X1-ORPHAN-M1" {
		t.Fatalf("transitions were %+v, want the orphaned claim reverted", led.transitions)
	}
}

// SPARE claims are OUT of scope for the reaper. A SPARE is a charting-seed
// request, and expansion consumes those target-aware: a QUEUED spare is read as
// "a seed for that frontier is already on order" and suppresses a duplicate
// request. Reverting it to WANTED would not release anything — the row still
// suppresses — while fighting expansion for the row every tick.
func TestReap_LeavesSpareClaimsAlone(t *testing.T) {
	led := &fakeReapLedger{
		systems: []ExpandSystem{{System: "X1-GONE", Verdict: VerdictNoWhitelist}},
		slots: []QueuedSlot{
			{Waypoint: "X1-GONE-S1", System: "X1-GONE", Kind: SlotKindSpare, State: SlotStateQueued},
			{Waypoint: "X1-GONE-M1", System: "X1-GONE", Kind: SlotKindMarket, State: SlotStateQueued},
			{Waypoint: "X1-GONE-Y1", System: "X1-GONE", Kind: SlotKindYard, State: SlotStateQueued},
		},
	}

	rep, err := ReapStrandedClaims(context.Background(), reapPortsFor(led), testPlayerID, 0)
	if err != nil {
		t.Fatalf("ReapStrandedClaims returned error: %v", err)
	}
	if rep.Reaped != 2 {
		t.Fatalf("report says Reaped=%d, want 2 (the MARKET and YARD claims only)", rep.Reaped)
	}
	for _, got := range led.transitions {
		if got.waypoint == "X1-GONE-S1" {
			t.Fatalf("reaped the SPARE seed request, which expansion is still counting as supply: %+v", led.transitions)
		}
	}
	if slot := led.slotAt("X1-GONE-S1"); slot.State != SlotStateQueued {
		t.Fatalf("SPARE slot is now %q, want it left QUEUED", slot.State)
	}
}

// --- contention and bounds ----------------------------------------------------

// A row another writer took first is skipped and counted, and the pass carries
// on. The loser of that race is legitimate — a concurrent drain wins the row
// only if the verdict flipped mid-tick — and either way the next tick
// re-evaluates from the ledger. Aborting the pass on it would let one contended
// row hold up every other stranded claim behind it.
func TestReap_SkipsClaimedRowAndContinues(t *testing.T) {
	led := &fakeReapLedger{
		systems: []ExpandSystem{{System: "X1-GONE", Verdict: VerdictNoWhitelist}},
		slots: []QueuedSlot{
			{Waypoint: "X1-GONE-M1", System: "X1-GONE", Kind: SlotKindMarket, State: SlotStateQueued},
			{Waypoint: "X1-GONE-M2", System: "X1-GONE", Kind: SlotKindMarket, State: SlotStateQueued},
			{Waypoint: "X1-GONE-M3", System: "X1-GONE", Kind: SlotKindMarket, State: SlotStateQueued},
		},
		transitionErr: map[string]error{
			"X1-GONE-M2": fmt.Errorf("X1-GONE-M2: %w", ErrSlotClaimed),
		},
	}

	rep, err := ReapStrandedClaims(context.Background(), reapPortsFor(led), testPlayerID, 0)
	if err != nil {
		t.Fatalf("a lost race is not an error: %v", err)
	}
	if rep.Reaped != 2 || rep.Skipped != 1 {
		t.Fatalf("report says Reaped=%d Skipped=%d, want 2 and 1", rep.Reaped, rep.Skipped)
	}
	if len(led.transitions) != 3 {
		t.Fatalf("made %d transitions, want 3 (the contended row does not stop the pass): %+v", len(led.transitions), led.transitions)
	}
	if slot := led.slotAt("X1-GONE-M3"); slot.State != SlotStateWanted {
		t.Fatalf("the claim behind the contended one is %q, want WANTED", slot.State)
	}
}

// A ledger refusing writes is NOT contention, and the two must not be conflated:
// retrying into an outage costs a write per stranded row per tick and reports
// success while nothing is released. The pass stops and says so, with the work
// it did manage still reported.
func TestReap_StopsOnLedgerWriteFailure(t *testing.T) {
	led := &fakeReapLedger{
		systems: []ExpandSystem{{System: "X1-GONE", Verdict: VerdictNoWhitelist}},
		slots: []QueuedSlot{
			{Waypoint: "X1-GONE-M1", System: "X1-GONE", Kind: SlotKindMarket, State: SlotStateQueued},
			{Waypoint: "X1-GONE-M2", System: "X1-GONE", Kind: SlotKindMarket, State: SlotStateQueued},
			{Waypoint: "X1-GONE-M3", System: "X1-GONE", Kind: SlotKindMarket, State: SlotStateQueued},
		},
		transitionErr: map[string]error{"X1-GONE-M2": errors.New("db down")},
	}

	rep, err := ReapStrandedClaims(context.Background(), reapPortsFor(led), testPlayerID, 0)
	if err == nil {
		t.Fatal("a ledger refusing writes did not surface an error")
	}
	if rep.Reaped != 1 {
		t.Fatalf("report says Reaped=%d, want the 1 row released before the failure", rep.Reaped)
	}
	if len(led.transitions) != 2 {
		t.Fatalf("made %d transitions, want 2 (the pass stops at the outage): %+v", len(led.transitions), led.transitions)
	}
}

// The pass is bounded like every other one in this engine: a large backlog is
// worked over more ticks rather than fired in one burst. Nothing is lost — the
// rows left over are still QUEUED and still stranded.
func TestReap_BoundedByMaxReaps(t *testing.T) {
	led := &fakeReapLedger{systems: []ExpandSystem{{System: "X1-GONE", Verdict: VerdictNoWhitelist}}}
	for i := 0; i < DefaultMaxReaps+5; i++ {
		led.slots = append(led.slots, QueuedSlot{
			Waypoint: fmt.Sprintf("X1-GONE-M%02d", i), System: "X1-GONE",
			Kind: SlotKindMarket, State: SlotStateQueued,
		})
	}

	rep, err := ReapStrandedClaims(context.Background(), reapPortsFor(led), testPlayerID, 0)
	if err != nil {
		t.Fatalf("ReapStrandedClaims returned error: %v", err)
	}
	if rep.Reaped != DefaultMaxReaps {
		t.Fatalf("reaped %d rows in one tick, want the cap of %d", rep.Reaped, DefaultMaxReaps)
	}
	if len(led.transitions) != DefaultMaxReaps {
		t.Fatalf("made %d transitions, want %d", len(led.transitions), DefaultMaxReaps)
	}

	// The remainder is picked up by the next tick, from the ledger.
	next, err := ReapStrandedClaims(context.Background(), reapPortsFor(led), testPlayerID, 0)
	if err != nil {
		t.Fatalf("second pass returned error: %v", err)
	}
	if next.Reaped != 5 {
		t.Fatalf("second pass reaped %d, want the 5 left over", next.Reaped)
	}
}

// The budget bounds WRITES, so a claim the verdict filter passes over costs
// nothing. The candidate list is the ledger's own waypoint order, which no rule
// keeps stranded rows near the front of — so a budget spent on rows that were
// never going to be written would let a long run of healthy in-scope claims
// starve the stranded ones behind them on every tick, forever. That is the exact
// leak this pass exists to close, re-opened one line lower down.
func TestReap_InScopeClaimsDoNotConsumeTheBudget(t *testing.T) {
	led := &fakeReapLedger{systems: []ExpandSystem{
		{System: "X1-AAA-LIVE", Verdict: VerdictInScope},
		{System: "X1-ZZZ-GONE", Verdict: VerdictNoWhitelist},
	}}
	// A full budget's worth of healthy claims sorts ahead of the stranded one.
	for i := 0; i < DefaultMaxReaps; i++ {
		led.slots = append(led.slots, QueuedSlot{
			Waypoint: fmt.Sprintf("X1-AAA-LIVE-M%02d", i), System: "X1-AAA-LIVE",
			Kind: SlotKindMarket, State: SlotStateQueued,
		})
	}
	led.slots = append(led.slots, QueuedSlot{
		Waypoint: "X1-ZZZ-GONE-M1", System: "X1-ZZZ-GONE",
		Kind: SlotKindMarket, State: SlotStateQueued,
	})

	rep, err := ReapStrandedClaims(context.Background(), reapPortsFor(led), testPlayerID, 0)
	if err != nil {
		t.Fatalf("ReapStrandedClaims returned error: %v", err)
	}
	if rep.Reaped != 1 {
		t.Fatalf("reaped %d, want the 1 stranded claim behind %d in-scope ones", rep.Reaped, DefaultMaxReaps)
	}
	if len(led.transitions) != 1 || led.transitions[0].waypoint != "X1-ZZZ-GONE-M1" {
		t.Fatalf("transitions were %+v, want only the stranded claim", led.transitions)
	}
}

// An explicit budget overrides the default, which is what lets a caller pace the
// pass without the constant becoming a knob.
func TestReap_HonoursExplicitBudget(t *testing.T) {
	led := &fakeReapLedger{systems: []ExpandSystem{{System: "X1-GONE", Verdict: VerdictNoWhitelist}}}
	for i := 0; i < 4; i++ {
		led.slots = append(led.slots, QueuedSlot{
			Waypoint: fmt.Sprintf("X1-GONE-M%d", i), System: "X1-GONE",
			Kind: SlotKindMarket, State: SlotStateQueued,
		})
	}

	rep, err := ReapStrandedClaims(context.Background(), reapPortsFor(led), testPlayerID, 2)
	if err != nil {
		t.Fatalf("ReapStrandedClaims returned error: %v", err)
	}
	if rep.Reaped != 2 {
		t.Fatalf("reaped %d rows under a budget of 2, want 2", rep.Reaped)
	}
}

// --- fail-closed reads ---------------------------------------------------------

// An unreadable verdict map is not an EMPTY one. Read permissively it would make
// every system look out of scope and revert every claim in the ledger in one
// pass — so the read failing means reaping nothing at all.
func TestReap_FailsClosedOnUnreadableVerdicts(t *testing.T) {
	led := &fakeReapLedger{
		systemsErr: errors.New("db down"),
		slots: []QueuedSlot{
			{Waypoint: "X1-LIVE-M1", System: "X1-LIVE", Kind: SlotKindMarket, State: SlotStateQueued},
		},
	}

	rep, err := ReapStrandedClaims(context.Background(), reapPortsFor(led), testPlayerID, 0)
	if err == nil {
		t.Fatal("an unreadable verdict map did not surface an error")
	}
	if rep.Reaped != 0 || len(led.transitions) != 0 {
		t.Fatalf("reverted %d claim(s) on an unreadable verdict map, want 0: %+v", rep.Reaped, led.transitions)
	}
}

// A slot list we could not read is not a slot list we may act on, even though the
// fake hands the rows over alongside its error.
func TestReap_FailsClosedOnUnreadableSlots(t *testing.T) {
	led := &fakeReapLedger{
		systems:  []ExpandSystem{{System: "X1-GONE", Verdict: VerdictNoWhitelist}},
		slotsErr: errors.New("db down"),
		slots: []QueuedSlot{
			{Waypoint: "X1-GONE-M1", System: "X1-GONE", Kind: SlotKindMarket, State: SlotStateQueued},
		},
	}

	rep, err := ReapStrandedClaims(context.Background(), reapPortsFor(led), testPlayerID, 0)
	if err == nil {
		t.Fatal("an unreadable slot list did not surface an error")
	}
	if rep.Reaped != 0 || len(led.transitions) != 0 {
		t.Fatalf("reverted %d claim(s) off an unreadable slot list, want 0: %+v", rep.Reaped, led.transitions)
	}
}

// --- cost ---------------------------------------------------------------------

// The overwhelmingly common tick has nothing claimed. It must not pay for a scan
// of the whole verdict map to discover that — the claim read is the cheap gate in
// front of it, and a ledger with no MARKET/YARD claims never reaches the second
// query.
func TestReap_NothingClaimed_ReadsNoVerdicts(t *testing.T) {
	led := &fakeReapLedger{
		systems: []ExpandSystem{{System: "X1-GONE", Verdict: VerdictNoWhitelist}},
		slots: []QueuedSlot{
			{Waypoint: "X1-GONE-M1", System: "X1-GONE", Kind: SlotKindMarket, State: SlotStateWanted},
			{Waypoint: "X1-GONE-S1", System: "X1-GONE", Kind: SlotKindSpare, State: SlotStateQueued},
		},
	}

	rep, err := ReapStrandedClaims(context.Background(), reapPortsFor(led), testPlayerID, 0)
	if err != nil {
		t.Fatalf("ReapStrandedClaims returned error: %v", err)
	}
	if rep.Reaped != 0 {
		t.Fatalf("report says Reaped=%d, want 0", rep.Reaped)
	}
	if led.systemsCalls != 0 {
		t.Fatalf("read the verdict map %d time(s) with nothing claimed, want 0", led.systemsCalls)
	}
	if len(led.statesAsked) != 1 || len(led.statesAsked[0]) != 1 || led.statesAsked[0][0] != SlotStateQueued {
		t.Fatalf("asked the ledger for %v, want exactly [QUEUED]", led.statesAsked)
	}
}

// The verdict map is read ONCE per pass, not once per stranded row.
func TestReap_ReadsVerdictMapOnce(t *testing.T) {
	led := &fakeReapLedger{systems: []ExpandSystem{{System: "X1-GONE", Verdict: VerdictNoWhitelist}}}
	for i := 0; i < 5; i++ {
		led.slots = append(led.slots, QueuedSlot{
			Waypoint: fmt.Sprintf("X1-GONE-M%d", i), System: "X1-GONE",
			Kind: SlotKindMarket, State: SlotStateQueued,
		})
	}

	if _, err := ReapStrandedClaims(context.Background(), reapPortsFor(led), testPlayerID, 0); err != nil {
		t.Fatalf("ReapStrandedClaims returned error: %v", err)
	}
	if led.systemsCalls != 1 {
		t.Fatalf("read the verdict map %d times for 5 stranded rows, want 1", led.systemsCalls)
	}
}
