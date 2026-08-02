package parkedsensing

import (
	"context"
	"errors"
	"fmt"
)

// reaper.go is the sensing ledger's bookkeeping pass: it hands back placement
// claims that the map moved out from under.
//
// THE LEAK IT CLOSES. The buy queue's candidate read covers MARKET and YARD
// slots in WANTED **or** QUEUED, but only in systems whose verdict is IN_SCOPE.
// So a placement claimed for purchase — its purchase yard recorded, its state
// flipped to QUEUED — whose system then LOSES IN_SCOPE is never drained again
// and never released. Correctly never bought (a placement we have not justified
// is the one purchase the queue must never make), but leaked three ways: the row
// squats on its waypoint's primary key, the buy_queued gauge reads permanently
// inflated, and the already-overloaded QUEUED state picks up a third meaning
// ("claimed, then orphaned") on top of the two it carries.
//
// WHY THIS IS BOOKKEEPING AND NOT A RESCUE. The recovery is asymmetric: a system
// that REGAINS IN_SCOPE resumes draining its QUEUED rows all by itself, because
// the drain reads WANTED and QUEUED together. Nothing here needs to help that
// case, and nothing here tries to. This pass exists only for systems that are
// currently out of scope, where the honest state of a claim nobody will act on is
// WANTED with no yard behind it.
//
// WHY IT IS SAFE TO TRANSITION A ROW THE SCAN PACER MAY BE WRITING. Nothing keeps
// the pacer's scan columns and this pass's state columns off the same row: the
// pacer runs concurrently with the reconcile and can commit at any instant, and
// this pass is not the only writer that transitions a placement under it. The
// protection is structural rather than an ordering invariant: ownership of
// sensing_slots is per-COLUMN, so a transition emits state, updated_at and
// whichever of assigned_ship / purchase_yard it actually changed, and cannot reach
// last_scan_at or spread_ewma however the two writers interleave (see the
// ownership block in sensing_ledger_repository.go, which names this pass as the
// reason). That is why the reaper needs no special-casing and uses the SlotFields
// DTO exactly as the six other transition call sites do.

// DefaultMaxReaps bounds how many claims one pass may release. A plain constant,
// deliberately not a knob: like maxDrainAttempts and DefaultMaxPlacementActions
// it paces a burst of writes rather than expressing an economic preference, and
// nothing downstream benefits from tuning it. A backlog is not lost — the rows
// left over are still QUEUED, still stranded, and still first in line next tick.
const DefaultMaxReaps = 20

// ReapLedger is the reaper's slice of the sensing ledger, and it is the
// narrowest in the engine: read the verdicts, read the claims, hand one back.
//
// Kept disjoint from the buy queue's BuyLedger on purpose, as every other engine
// slice is. This pass runs immediately before the drain and touches the same
// rows, so the one thing it must be structurally unable to do is buy: it can
// neither price a yard, nor count the probe fleet, nor read the treasury, so no
// change here can turn a bookkeeping sweep into a spending path.
type ReapLedger interface {
	// Systems returns every system row the player holds — the verdict map this
	// pass filters on.
	Systems(ctx context.Context, playerID int) ([]ExpandSystem, error)
	// SlotsByState returns the player's slots in any of the given states.
	SlotsByState(ctx context.Context, playerID int, states ...string) ([]QueuedSlot, error)
	// TransitionSlot advances one slot's state, guarded on it still being in
	// fromState so a concurrent drain and this pass cannot both move one row.
	TransitionSlot(ctx context.Context, playerID int, waypoint, kind, fromState, toState string, set SlotFields) error
}

// ReapPorts is everything ReapStrandedClaims needs from the outside world.
type ReapPorts struct {
	Ledger ReapLedger
}

// ReapReport is one pass's outcome, for the heartbeat.
type ReapReport struct {
	// Reaped counts stranded claims handed back to WANTED.
	Reaped int
	// Skipped counts rows another writer moved first. Routine rather than
	// alarming — see the loop for why a lost race is legitimate.
	Skipped int
}

// ReapStrandedClaims reverts MARKET and YARD placements left QUEUED in systems
// that are no longer IN_SCOPE, up to maxReaps per pass. A non-positive maxReaps
// falls back to DefaultMaxReaps.
//
// SPARE claims are deliberately out of scope. A SPARE is a charting-seed request,
// and expansion consumes those TARGET-AWARE: a QUEUED spare answers "is a request
// for that frontier already outstanding?" with yes, and suppresses a duplicate.
// Reverting it to WANTED would release nothing — the row goes on suppressing
// either way — while putting this pass and the expansion engine in a tug-of-war
// over the same row every tick.
func ReapStrandedClaims(ctx context.Context, p ReapPorts, playerID int, maxReaps int) (ReapReport, error) {
	var rep ReapReport
	if maxReaps <= 0 {
		maxReaps = DefaultMaxReaps
	}

	claimed, err := p.Ledger.SlotsByState(ctx, playerID, SlotStateQueued)
	if err != nil {
		return rep, fmt.Errorf("failed to list claimed sensing placements: %w", err)
	}
	// The claim read is the cheap gate in front of the verdict scan. A tick with
	// nothing claimed — the overwhelmingly common one, since the drain clears its
	// claims within the same tick it makes them — must not pay for a scan of the
	// whole system table to discover it has nothing to do.
	candidates := placementClaims(claimed)
	if len(candidates) == 0 {
		return rep, nil
	}

	systems, err := p.Ledger.Systems(ctx, playerID)
	if err != nil {
		// An unreadable verdict map is NOT an empty one, and the difference is the
		// whole pass: empty means "no system is in scope", which is this pass's own
		// reap condition, so a permissive read would revert every claim in the
		// ledger at once — including the ones the drain is actively working.
		return rep, fmt.Errorf("failed to read the sensing verdict map, releasing no claims this tick: %w", err)
	}
	inScope := make(map[string]bool, len(systems))
	for _, s := range systems {
		inScope[s.System] = s.Verdict == VerdictInScope
	}

	// The yard recorded on the claim was chosen for a system we no longer want
	// watched, so it goes with the claim. Leaving it would have the drain treat a
	// stale preference as this placement's provenance if the system ever returns.
	cleared := ""
	for _, slot := range candidates {
		// Every trip through the write path that CONTINUES the pass lands in
		// exactly one counter, so their sum is the writes this budget has to
		// bound. The fatal branch below is the one write that lands in neither,
		// and it is harmless precisely because it RETURNS — the budget is never
		// consulted again, so it cannot be overspent by the write it missed. A
		// slot the verdict filter passes over costs nothing either, and that is
		// what stops a run of in-scope claims crowding out the stranded ones
		// behind them.
		if rep.Reaped+rep.Skipped >= maxReaps {
			break
		}
		// A system with NO row is read as out of scope, not as an unknown to leave
		// alone: the drain's own IN_SCOPE lookup misses an absent row exactly as it
		// misses a rejected one, so the claim is stranded either way.
		if inScope[slot.System] {
			continue
		}

		err := p.Ledger.TransitionSlot(ctx, playerID, slot.Waypoint, slot.Kind, SlotStateQueued, SlotStateWanted,
			SlotFields{PurchaseYard: &cleared})
		switch {
		case errors.Is(err, ErrSlotClaimed):
			// Another writer moved the row first. Legitimate only if the verdict
			// flipped back mid-tick and the drain took it — and if it was not, the
			// row is still QUEUED and this pass sees it again next tick. Either way
			// nothing here should stop for it.
			rep.Skipped++
		case err != nil:
			// Not contention: the ledger is refusing writes. Retrying into that
			// would cost a write per stranded row per tick and release nothing, so
			// the pass stops and reports what it did manage.
			return rep, fmt.Errorf("failed to release the stranded sensing claim on %s: %w", slot.Waypoint, err)
		default:
			rep.Reaped++
		}
	}
	return rep, nil
}

// placementClaims narrows the claimed rows to the placements this pass owns.
//
// An ALLOWLIST rather than "not SPARE": the two read the same today and diverge
// the moment a fourth slot kind is added, and the safe direction for a pass that
// rewrites state is to ignore a kind it has never heard of rather than to
// transition it on the strength of not recognising it.
func placementClaims(slots []QueuedSlot) []QueuedSlot {
	out := make([]QueuedSlot, 0, len(slots))
	for _, slot := range slots {
		if slot.Kind == SlotKindMarket || slot.Kind == SlotKindYard {
			out = append(out, slot)
		}
	}
	return out
}
