package parkedsensing

import (
	"context"
	"errors"
	"fmt"
)

// reaper.go is the sensing ledger's bookkeeping pass: it hands back placement
// claims that the map moved out from under.
//
// The buy queue drains MARKET and YARD slots in WANTED or QUEUED, but only in
// systems whose verdict is IN_SCOPE, so a placement claimed for purchase whose
// system then loses IN_SCOPE is never drained again and never released: the row
// squats on its waypoint's primary key and inflates buy_queued forever. WANTED
// with no yard behind it is the honest state of a claim nobody will act on.
// Recovery is asymmetric — a system that REGAINS IN_SCOPE resumes draining its
// QUEUED rows by itself — so this pass serves only systems currently out of scope.
//
// Transitioning a row the scan pacer may be writing concurrently is safe
// structurally rather than by ordering: ownership of sensing_slots is per-COLUMN,
// so a transition emits state, updated_at and whichever of assigned_ship /
// purchase_yard it changed, and cannot reach last_scan_at or spread_ewma however
// the two writers interleave (see the ownership block in
// sensing_ledger_repository.go).

// DefaultMaxReaps bounds how many claims one pass may release. Like
// maxDrainAttempts it paces a burst of writes rather than expressing an economic
// preference, so it is a constant and not a knob. A backlog is not lost: the rows
// left over are still QUEUED and still first in line next tick.
const DefaultMaxReaps = 20

// ReapLedger is the reaper's slice of the sensing ledger: read the verdicts, read
// the claims, hand one back. Kept disjoint from the buy queue's BuyLedger so this
// pass is structurally unable to buy — it can neither price a yard, nor count the
// probe fleet, nor read the treasury — which matters because it runs immediately
// before the drain and touches the same rows.
type ReapLedger interface {
	// Systems returns every system row the player holds — the verdict map this
	// pass filters on.
	Systems(ctx context.Context, playerID int) ([]ExpandSystem, error)
	// SlotsByState returns the player's slots in any of the given states.
	SlotsByState(ctx context.Context, playerID int, states ...string) ([]QueuedSlot, error)
	// TransitionSlot advances one slot's state, guarded on it still being in
	// From so a concurrent drain and this pass cannot both move one row.
	TransitionSlot(ctx context.Context, playerID int, t SlotTransition, set SlotFields) error
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
// SPARE claims are deliberately out of scope. Expansion consumes charting-seed
// requests TARGET-AWARE, so a QUEUED spare suppresses a duplicate request for that
// frontier either way; reverting it would release nothing and only start a
// tug-of-war over the row every tick.
func ReapStrandedClaims(ctx context.Context, p ReapPorts, playerID int, maxReaps int) (ReapReport, error) {
	var rep ReapReport
	if maxReaps <= 0 {
		maxReaps = DefaultMaxReaps
	}

	claimed, err := p.Ledger.SlotsByState(ctx, playerID, SlotStateQueued)
	if err != nil {
		return rep, fmt.Errorf("failed to list claimed sensing placements: %w", err)
	}
	// The claim read is the cheap gate in front of the verdict scan: a tick with
	// nothing claimed must not pay for a scan of the whole system table.
	candidates := placementClaims(claimed)
	if len(candidates) == 0 {
		return rep, nil
	}

	systems, err := p.Ledger.Systems(ctx, playerID)
	if err != nil {
		// An unreadable verdict map is NOT an empty one: empty means "no system is
		// in scope", which is this pass's own reap condition, so a permissive read
		// would revert every claim in the ledger at once.
		return rep, fmt.Errorf("failed to read the sensing verdict map, releasing no claims this tick: %w", err)
	}
	inScope := make(map[string]bool, len(systems))
	for _, s := range systems {
		inScope[s.System] = s.Verdict == VerdictInScope
	}

	// The yard recorded on the claim was chosen for a system we no longer want
	// watched, so it goes with the claim rather than surviving as a stale
	// preference the drain would read as this placement's provenance.
	cleared := ""
	for _, slot := range candidates {
		// Every trip through the write path that CONTINUES the pass lands in
		// exactly one counter, so their sum is the writes this budget bounds; the
		// fatal branch below lands in neither and is harmless because it RETURNS.
		// A slot the verdict filter passes over costs nothing, which stops a run
		// of in-scope claims crowding out the stranded ones behind them.
		if rep.Reaped+rep.Skipped >= maxReaps {
			break
		}
		// A system with NO row is read as out of scope, not as an unknown to leave
		// alone: the drain's IN_SCOPE lookup misses an absent row exactly as it
		// misses a rejected one, so the claim is stranded either way.
		if inScope[slot.System] {
			continue
		}

		err := p.Ledger.TransitionSlot(ctx, playerID, SlotTransition{
			Waypoint: slot.Waypoint, Kind: slot.Kind, From: SlotStateQueued, To: SlotStateWanted,
		}, SlotFields{PurchaseYard: &cleared})
		switch {
		case errors.Is(err, ErrSlotClaimed):
			// Another writer moved the row first. Either the verdict flipped back
			// mid-tick and the drain took it, or the row is still QUEUED and this
			// pass sees it again next tick.
			rep.Skipped++
		case err != nil:
			// Not contention: the ledger is refusing writes. Retrying into that
			// costs a write per stranded row per tick and releases nothing, so the
			// pass stops and reports what it did manage.
			return rep, fmt.Errorf("failed to release the stranded sensing claim on %s: %w", slot.Waypoint, err)
		default:
			rep.Reaped++
		}
	}
	return rep, nil
}

// placementClaims narrows the claimed rows to the placements this pass owns.
//
// An ALLOWLIST rather than "not SPARE": the two diverge the moment a fourth slot
// kind is added, and the safe direction for a pass that rewrites state is to
// ignore an unrecognised kind rather than transition it.
func placementClaims(slots []QueuedSlot) []QueuedSlot {
	out := make([]QueuedSlot, 0, len(slots))
	for _, slot := range slots {
		if slot.Kind == SlotKindMarket || slot.Kind == SlotKindYard {
			out = append(out, slot)
		}
	}
	return out
}
