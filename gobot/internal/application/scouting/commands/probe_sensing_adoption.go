package commands

import (
	"context"
	"errors"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// probe_sensing_adoption.go is the standing retry behind the cutover's one-shot
// probe adoption (sp-0gp21).
//
// THE LEAK IT CLOSES. The cutover adopts the touring model's orphaned probes
// exactly once, and it skips any hull it cannot place — correctly, since a hull
// with no location is a hull we must not record. A probe IN TRANSIT at that
// instant therefore slips through, and because markCutoverDone latches, adoption
// never runs again: the hull gets no ledger row, so CountOwnedProbes cannot see
// it, so the probe cap under-reads and authorises buying a replacement for a
// probe we already own (RULINGS #4). It also keeps its `scout` tag and is driven
// by nobody.
//
// WHY A RETRY RATHER THAN A LOUDER CUTOVER. Collecting the skip as a cutover
// failure would hold the whole engine hostage to a probe happening to be in
// flight — the cutover gates every later stage, and "a ship is moving" is the
// most ordinary state in the fleet. The honest fix is to stop making adoption a
// one-shot at all: run it every tick, idempotently, and the hull is picked up the
// moment the ships table can say where it is.
//
// WHY IT LIVES HERE AND NOT IN THE REAPER. It is the reaper's sibling in
// discipline — every tick, bounded, fail-closed, failures collected — but not in
// PORTS, and the ports are what decide. Adoption must enumerate the fleet, and
// internal/application/parkedsensing deliberately exposes no fleet-listing method
// anywhere (see ParkedShipReader): the sensing engines' per-tick cost is meant to
// scale with the placements they are working, not with how many hulls the player
// owns. Adoption also needs the scout-post repository and the hull-bearing slot
// writer, none of which are in ReapLedger — which is the narrowest slice in the
// engine on purpose. Putting this inside the reaper would widen both contracts to
// borrow a tick slot. The coordinator already holds every port this needs, so the
// pass belongs beside the reaper rather than inside it.

// DefaultMaxAdoptions bounds how many hulls one tick may adopt. A plain constant,
// deliberately not a knob, in the same class as the reaper's DefaultMaxReaps and
// the placement machine's DefaultMaxPlacementActions: it paces a burst of ledger
// writes, not an economic choice. A backlog is not lost — the hulls left over are
// still orphaned and still first in line next tick.
const DefaultMaxAdoptions = 10

// legacyProbeBuyerFleetTag is the dedicated_fleet tag the RETIRED probe-buyer coordinator wrote on
// the hulls it recruited. The coordinator is deleted; its tag is not — it is persisted on live
// ships rows and nothing rewrites it, so adoption must still recognise those hulls as orphans.
// Declared here, as a plain string rather than an import, precisely BECAUSE the package that
// defined it is gone: this is the tag's last remaining reader.
const legacyProbeBuyerFleetTag = "probe-buyer"

// adoptableFleetTag reports whether a hull's dedicated_fleet marks it as an ORPHAN this pass may
// absorb, rather than a hull that belongs to somebody.
//
// An ALLOWLIST, not a denylist, and that direction is the guard (RULINGS #7). Adoption used to
// require exactly freshnessScoutFleetTag — a tag no live hull carries, which is why the pass had
// never adopted anything — so the fix is to widen it. But "widen" must not become "take any probe
// you find": a hull dedicated to a live foreign fleet already has a single writer, and absorbing it
// would give one hull two. Listing what is adoptable means a fleet tag invented later is refused by
// default and has to be added deliberately; a denylist would silently absorb it the day it appears.
//
// The four members are the only ways a probe can be nobody's:
//
//   - ""                        never tagged (the ten live hulls the freshness sizer left bare)
//   - freshnessScoutFleetTag    the retired touring model's tag
//   - legacyProbeBuyerFleetTag  the retired buyer coordinator's tag
//   - SensingParkedFleetTag     OUR tag, present on a hull with no row — the other half of a
//     failed write (recorded-then-tag-failed leaves a row, so
//     holds.hulls strikes it off; tagged-then-record-failed leaves
//     none, and used to be unrecoverable because the old filter
//     rejected our own tag forever)
func adoptableFleetTag(fleet string) bool {
	switch fleet {
	case "", freshnessScoutFleetTag, legacyProbeBuyerFleetTag, parkedsensing.SensingParkedFleetTag:
		return true
	default:
		return false
	}
}

// adoptStrandedProbes records every scout-tagged hull the ledger has no row for
// as a parked SPARE where it stands, and reports how many it adopted.
//
// Idempotent by construction: a hull already carrying the sensing tag fails the
// eligibility filter, and a hull already holding a slot row is struck off by the
// ledger read — so a settled fleet costs this pass its three reads and not one
// write, however many ticks run.
func (h *RunProbeSensingCoordinatorHandler) adoptStrandedProbes(
	ctx context.Context,
	cmd *RunProbeSensingCoordinatorCommand,
	ports SensingEnginePorts,
	failures *[]error,
) int {
	logger := common.LoggerFromContext(ctx)
	playerID := cmd.PlayerID.Value()

	// All three reads FAIL CLOSED, and the post list is the sharpest of them: read
	// permissively an unreadable post list is an EMPTY one, which means "no hull is
	// manned" — and this pass would then adopt the probe standing on the surviving
	// home post right out from under the scout coordinator.
	ships, err := h.fleetRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		*failures = append(*failures, fmt.Errorf("failed to list the fleet for probe adoption: %w", err))
		return 0
	}
	posts, err := h.postRepo.ListActive(ctx, playerID)
	if err != nil {
		*failures = append(*failures, fmt.Errorf("failed to list scout posts for probe adoption: %w", err))
		return 0
	}
	holds, err := ledgerHoldings(ctx, ports, playerID)
	if err != nil {
		*failures = append(*failures, err)
		return 0
	}

	manned := make(map[string]bool, len(posts))
	for _, post := range posts {
		if post.AssignedHull != "" {
			manned[post.AssignedHull] = true
		}
	}

	adopted, writes := 0, 0
	for _, ship := range ships {
		// THE BUDGET COUNTS WRITES, NOT CANDIDATES, and the difference is the
		// whole guard. A fleet is mostly hulls this pass will never touch —
		// haulers, manned probes, spares already adopted — and the ships table
		// returns them in a stable order. A budget spent on rows that were never
		// going to be written would therefore let the ineligible majority starve
		// the eligible few on every tick, permanently. The reaper closes the same
		// hole the same way (sp-l3f3d).
		if writes >= DefaultMaxAdoptions {
			break
		}
		if !ship.IsScoutType() || !adoptableFleetTag(ship.DedicatedFleet()) {
			continue // not a probe frame, or dedicated to a fleet that is not ours to take
		}
		if !ship.IsIdle() {
			// DRIVEN BY A LIVE CONTAINER — leave it alone (RULINGS #3, single writer).
			//
			// Three live probes sit `active` on scout_tour containers. Recording one here would let
			// the placement machine re-task a hull mid-flight while its tour still believes it owns
			// it: two writers, one hull, and a moving ship stranded between two intentions. The
			// `manned` map below does NOT cover this — it reads scout POSTS, and a tour is not a
			// post.
			//
			// Nothing is lost by waiting. This pass is a standing per-tick retry (that is the whole
			// reason it exists rather than living in the cutover), so the hull is absorbed on the
			// first tick after its tour releases it. Grabbing it early buys one tick and risks a
			// desynced hull; the trade is not close.
			continue
		}
		if manned[ship.ShipSymbol()] {
			continue // still manning a surviving post — that post's hull, not ours to take
		}
		if holds.hulls[ship.ShipSymbol()] {
			// Recorded but still scout-tagged: the safe half-done shape a failed
			// tag write leaves. The row is what the probe cap counts, and it is
			// already there, so re-writing it would buy nothing and would spend
			// budget a genuinely stranded hull needs.
			continue
		}
		location := ship.CurrentLocation()
		if location == nil || location.Symbol == "" {
			// In transit. Not an error and not a failure to collect — just a hull
			// we cannot place YET. This pass runs again next tick, which is the
			// entire point of it existing.
			continue
		}
		// THE OCCUPANCY GUARD, and it is a money guard. sensing_slots is keyed on
		// the WAYPOINT, and UpsertSpareSlot's conflict set carries slot_kind,
		// state AND assigned_ship — so a second write here does not fail, it
		// silently REWRITES the row that is already there. Three ways that bites,
		// all with every call succeeding and the heartbeat reporting success:
		//
		//   - Two orphans standing at one waypoint: the second overwrites the
		//     first, leaving the first tagged with NO row. It then fails the
		//     scout-tag filter above on every later tick — invisible to
		//     CountOwnedProbes forever, and re-bought (RULINGS #4).
		//   - An orphan standing on a FILLED placement: the incumbent probe is
		//     evicted from its own row, dropping out of the cap, and the row's
		//     kind/state become SPARE/PARKED so a working market placement quietly
		//     leaves the scan rotation.
		//   - An orphan standing on an UNFILLED placement (WANTED, or QUEUED):
		//     same rewrite, same rotation loss, and a QUEUED row additionally
		//     loses the claim its later TransitionSlot(QUEUED→BOUGHT) is guarded
		//     on, which then fails with ErrSlotClaimed.
		//
		// That third case is why this index is keyed on ROW EXISTENCE and not on
		// hull presence: a hull-less row is still a row, and the conflict set does
		// not care that it names no ship. The hull index above answers a different
		// question — "is this HULL already recorded?" — and neither substitutes
		// for the other.
		//
		// A hull skipped here is not lost: it stays untagged and unrecorded, which
		// is the recoverable state, and it is reconsidered on every later tick.
		// See the residual note on ledgerHolds for what it costs meanwhile.
		if row, occupied := holds.rows[location.Symbol]; occupied {
			// A HULL-LESS *WANTED* PLACEMENT THE ORPHAN IS STANDING ON IS NOT A CONFLICT — IT IS
			// THE ANSWER. The screen declared "a probe should stand here"; a probe is standing
			// here. Filling the row in place is strictly better than skipping it, and it is what
			// takes this pass from absorbing one live hull to absorbing the ones that matter:
			// seven idle probes sit on X1-KP23-A2, the yard that sold them, and that waypoint
			// already carried a hull-less WANTED/MARKET row. Under the plain occupancy skip every
			// one of them stayed invisible to the probe cap while the drain tried to buy another.
			//
			// This is NOT the destructive rewrite the occupancy guard exists to prevent, and the
			// difference is exact: UpsertSpareSlot rewrites slot_kind and state through a conflict
			// set, which is how a MARKET placement silently became a SPARE and left the scan
			// rotation. This goes through the GUARDED transition instead — kind is never touched,
			// so a MARKET row stays MARKET and keeps scanning, and the write is conditional on the
			// row still being WANTED, so a racing writer cannot be overwritten.
			//
			// Refused in every other shape:
			//   - AssignedShip != "": the row names an incumbent. Evicting it would drop a working
			//     probe out of the cap and hand its placement away.
			//   - QUEUED: the drain has claimed it for purchase, and its later
			//     TransitionSlot(QUEUED->BOUGHT) is guarded on that state — filling it here breaks
			//     a claim that money is already riding on.
			//   - anything else (BOUGHT / IN_TRANSIT / PARKED): already somebody's.
			if row.State != parkedsensing.SlotStateWanted || row.AssignedShip != "" {
				continue
			}
			hull := ship.ShipSymbol()
			writes++ // counted before the write, as below: a failed write costs the database the same
			terr := ports.Ledger.TransitionSlot(ctx, playerID, location.Symbol,
				parkedsensing.SlotStateWanted, parkedsensing.SlotStateParked,
				parkedsensing.SlotFields{AssignedShip: &hull})
			switch {
			case errors.Is(terr, parkedsensing.ErrSlotClaimed):
				// Another writer took the placement between the read and the write. Routine
				// contention, nothing spent — the hull is still an orphan and still first in line
				// next tick.
				continue
			case terr != nil:
				*failures = append(*failures, fmt.Errorf("failed to fill placement %s with the probe standing on it (%s): %w", location.Symbol, hull, terr))
				continue
			}
			// Same order as the spare path: the row is what the cap counts, so it is written first
			// and the tag is best-effort behind it.
			if tagErr := ports.Fleet.AssignFleet(ctx, playerID, hull, parkedsensing.SensingParkedFleetTag); tagErr != nil {
				logger.Log("WARNING", fmt.Sprintf(
					"Probe %s now fills placement %s but keeps its old fleet tag (the probe cap counts it; the placement machine re-tags it on first use): %v",
					hull, location.Symbol, tagErr), map[string]interface{}{
					"action":      "parked_sensing_adopt_tag_failed",
					"ship_symbol": hull,
				})
			}
			holds.hulls[hull] = true
			adopted++
			continue
		}

		// Counted BEFORE the write, so a failed write still spends budget. That is
		// deliberate: the bound protects the DATABASE from a burst, and a write
		// that fails cost it just as much as one that succeeded. The trade is that
		// a persistently failing ledger burns the whole tick's budget on the same
		// hulls — acceptable, because they are the same hulls it would retry
		// anyway, and the failures are collected and surfaced.
		writes++
		// RECORD BEFORE TAGGING. The two writes are ordered by what a failure
		// between them costs, and the orders are not symmetric.
		// Recorded-but-untagged leaves a hull the probe cap COUNTS and which the
		// filter above then skips forever — safe, and self-healing the first time
		// the placement machine touches the spare. Tagged-but-unrecorded is
		// unrecoverable here: the tag makes the hull fail the scout-tag filter on
		// every future tick, so it would stay invisible to the cap for good and
		// authorise re-buying a probe we own.
		if uerr := ports.Ledger.UpsertSpareSlot(ctx, playerID, parkedsensing.SlotRecord{
			Waypoint:     location.Symbol,
			System:       location.SystemSymbol,
			Kind:         parkedsensing.SlotKindSpare,
			State:        parkedsensing.SlotStateParked,
			AssignedShip: ship.ShipSymbol(),
		}); uerr != nil {
			*failures = append(*failures, fmt.Errorf("failed to adopt stranded probe %s as a spare: %w", ship.ShipSymbol(), uerr))
			continue
		}
		// Best-effort, for the reason above: the cap counts rows, and the row is
		// written. Named rather than silent, because an untagged hull looks idle
		// and undedicated to every other coordinator's ownership sweep.
		if terr := ports.Fleet.AssignFleet(ctx, playerID, ship.ShipSymbol(), parkedsensing.SensingParkedFleetTag); terr != nil {
			logger.Log("WARNING", fmt.Sprintf(
				"Adopted stranded probe %s is recorded as a sensing spare but keeps its old fleet tag (the probe cap counts it; the placement machine re-tags it on first use): %v",
				ship.ShipSymbol(), terr), map[string]interface{}{
				"action":      "parked_sensing_adopt_tag_failed",
				"ship_symbol": ship.ShipSymbol(),
			})
		}
		// The waypoint holds a row now, so a later candidate standing on it is
		// skipped rather than overwriting what this tick just wrote. Recorded as the row this tick
		// actually wrote — a PARKED spare naming this hull — so a second orphan at the same
		// waypoint sees a filled row and is refused by the same test that refuses an incumbent,
		// rather than by a bare "occupied" flag that cannot say why.
		holds.rows[location.Symbol] = parkedsensing.QueuedSlot{
			Waypoint:     location.Symbol,
			System:       location.SystemSymbol,
			Kind:         parkedsensing.SlotKindSpare,
			State:        parkedsensing.SlotStateParked,
			AssignedShip: ship.ShipSymbol(),
		}
		holds.hulls[ship.ShipSymbol()] = true
		adopted++
	}

	if adopted > 0 {
		logger.Log("INFO", fmt.Sprintf(
			"Adopted %d stranded sensing probe(s) the cutover could not place (in transit at the time); they now count against the probe cap",
			adopted), map[string]interface{}{
			"action":   "parked_sensing_adopted_stranded",
			"adopted":  adopted,
			"examined": len(ships),
		})
	}
	return adopted
}

// ledgerHolds is what the sensing ledger already accounts for, in TWO separate
// indexes. They answer different questions and neither substitutes for the
// other, which is exactly the distinction a single "already handled" set gets
// wrong:
//
//	hulls     "is this HULL already recorded anywhere?"  — skip the candidate, no write needed
//	occupied  "does this WAYPOINT hold a row at all?"    — skip the candidate, a write would REWRITE it
//
// occupied counts EVERY row, hull-bearing or not, because UpsertSpareSlot's
// conflict set rewrites slot_kind and state whether or not the row names a ship.
// A WANTED or QUEUED placement is hull-less and would be invisible to a
// hull-keyed index, yet overwriting it is just as destructive.
//
// THE RESIDUAL THIS LEAVES, stated because it is structural rather than a bug to
// be found later. sensing_slots holds ONE row per (player, waypoint), so a hull
// standing where a row already exists can never be recorded while that row does.
// Skipping is the right behaviour — the alternative is the destructive rewrite
// above — but the probe cap under-reads by one for as long as it lasts, and that
// is the money-UNSAFE direction (an under-count authorises a re-buy). How long it
// lasts depends on what is in the way, and neither case resolves on its own:
//
//   - Blocked by ANOTHER ORPHAN'S row (two hulls at one waypoint): the waypoint
//     frees when that first hull is put to work — re-tasked by the buy queue's
//     spare reuse, or claimed as a charting seed. An orphan is driven by nobody,
//     so it does not wander off by itself; something else has to consume it.
//   - Blocked by a PLACEMENT row (WANTED or QUEUED, hull-less): there is no
//     "first hull" to put to work at all, and it gets WORSE rather than better —
//     when the drain eventually fills that placement the row becomes
//     BOUGHT/IN_TRANSIT/PARKED and STAYS, so the waypoint is occupied
//     indefinitely and the orphan is skipped, and uncounted, indefinitely.
//
// Nothing else picks the hull up meanwhile: it keeps the `scout` tag, and the buy
// queue's buyerAt path reads DockedProbeAt, which selects only hulls whose
// dedicated_fleet is empty or already sensing_parked. So a scout-tagged orphan is
// invisible there too.
//
// Closing this needs DIFFERENT ACCOUNTING, not a wider write here: a row-counting
// cap cannot represent two hulls on one waypoint at any width. Tracked as
// sp-dpfp8.
//
// ONE INVARIANT NOW HELD BY CONVENTION RATHER THAN CONSTRUCTION: `hulls` used to
// align with CountOwnedProbes structurally, both reading the same three
// hull-bearing states. This read is now all five, so the alignment holds only
// because WANTED and QUEUED rows never name a hull — true of every writer today,
// but nothing in the schema enforces it. A WANTED row that named a scout-tagged
// hull would make this pass skip a hull the cap does not count, which is the
// money-unsafe direction. No writer produces that combination today.
type ledgerHolds struct {
	hulls map[string]bool
	// rows is the occupancy index, and it carries the ROW rather than a bare bool: presence still
	// answers "does this WAYPOINT hold a row at all?", but the pass now also has to ask WHICH row,
	// because a hull-less WANTED placement the orphan is standing on is filled in place while every
	// other occupied shape is skipped. A bool cannot distinguish "somebody's placement" from "a
	// placement asking for exactly this hull".
	rows map[string]parkedsensing.QueuedSlot
}

// ledgerHoldings builds both indexes from the single query the pass makes.
//
// EVERY state is read, not just the hull-bearing ones. The hull index only ever
// gains entries from rows that name a ship (WANTED and QUEUED are intents with
// nothing behind them), but the occupancy index needs the unfilled rows too —
// they are the ones a naive hull-keyed guard would leave exposed.
func ledgerHoldings(ctx context.Context, ports SensingEnginePorts, playerID int) (ledgerHolds, error) {
	slots, err := ports.Ledger.SlotsByState(ctx, playerID,
		parkedsensing.SlotStateWanted, parkedsensing.SlotStateQueued, parkedsensing.SlotStateBought,
		parkedsensing.SlotStateInTransit, parkedsensing.SlotStateParked)
	if err != nil {
		return ledgerHolds{}, fmt.Errorf("failed to read what the sensing ledger already holds: %w", err)
	}
	held := ledgerHolds{
		hulls: make(map[string]bool, len(slots)),
		rows:  make(map[string]parkedsensing.QueuedSlot, len(slots)),
	}
	for _, slot := range slots {
		held.rows[slot.Waypoint] = slot
		if slot.AssignedShip != "" {
			held.hulls[slot.AssignedShip] = true
		}
	}
	return held, nil
}
