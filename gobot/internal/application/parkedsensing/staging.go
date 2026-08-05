package parkedsensing

import (
	"context"
	"fmt"
	"time"
)

// probeYardsIn lists a system's probe-selling shipyards, memoised for the TICK.
// One map is shared by every consumer in the tick, so a system's yards are read at
// most once however many times they are wanted. The read is a local catalog query,
// never an API call, and the answer cannot change while the tick runs.
func (t *expandTick) probeYardsIn(ctx context.Context, system string) ([]string, error) {
	if yards, cached := t.probeYards[system]; cached {
		return yards, nil
	}
	yards, err := t.p.Yards.ListProbeYards(ctx, system)
	if err != nil {
		return nil, fmt.Errorf("failed to list probe yards in %q: %w", system, err)
	}
	t.probeYards[system] = yards
	return yards, nil
}

// stagingYardFor picks where a seed for target should be bought: a probe-selling
// yard, in any of our own systems FROM WHICH THE TARGET IS ROUTABLE, that carries
// no SPARE placement of its own.
//
// WHERE WE CAN TRANSACT AND WHAT IS NEAR THE TARGET ARE TWO DIFFERENT QUESTIONS,
// AND WELDING THEM IS A STRUCTURAL DEAD ZONE. Require a yard to be staffed AND to
// sit within the placement walk's couple of rings OF THE TARGET, and a target
// whose in-reach systems all lack a shipyard can never be seeded however many
// staffed yards the fleet owns elsewhere; because a system with no shipyard can
// never itself be staffed, the dead zone propagates outward.
//
// So eligibility is routability alone, bounded by SeedFlightUnbounded (the bound
// the seed's own walk resolves under), with candidates ordered NEAREST FIRST
// because a shorter flight is fewer ticks holding probe-cap headroom. The money
// constraint is not dropped: staffedAt still applies on top, so a nearer yard we
// do not hold is skipped for a further one we do.
//
// "OUR OWN" IS ENFORCED, and nothing upstream supplies it: the origins this walks
// are every system carrying a screening VERDICT, and a verdict says "screened and
// worth trading with", never "we have a hull there". Unenforced, a seed is staged
// at a yard in a system we have never visited, the buy queue refuses it on that
// tick and every tick after, and the target never gets eyes.
//
// The enforcement belongs HERE rather than in the origin set. `neighbours` is
// shared with frontier propagation, which needs every system whose gate adjacency
// we have measured precisely BECAUSE it has no verdict yet; narrowing that map to
// occupied systems would collapse the frontier back to one fully-charted ring at a
// time. Occupancy is a requirement of STAGING A PURCHASE, not of believing a gate
// edge, so it is applied at the yard and the map is left whole.
//
// Writing nothing beats writing an unfundable want: nothing retires a WANTED SPARE
// row, and through takeSupplyFor a stale one goes on suppressing the correct
// request that could be made once a bordering system finally is occupied.
//
// The free-waypoint requirement is a money guard in depth: placement rows are
// keyed on the waypoint, so writing a SPARE want over the yard's existing row
// would overwrite whatever state it held — dropping a parked probe out of the cap
// count and authorising the purchase of a replacement standing right there. The
// ledger refuses that write on its own (UpsertSlotMetadata cannot touch state or
// assigned_ship), so this is defence in depth; it still earns its place, because
// picking a free yard is how two seed requests avoid the same waypoint.
func (t *expandTick) stagingYardFor(ctx context.Context, target string) (string, string, error) {
	origins, err := t.reach.originsWithin(ctx, t.reach.origins(), target)
	if err != nil {
		return "", "", err
	}
	// TWO PASSES, EVIDENCE FIRST. Within each pass the origins keep the nearest-first
	// order above, so this adds a condition to the routability selection rather than
	// replacing it. See probeStock.acceptsStaging for what each pass admits.
	for _, wantEvidence := range []bool{true, false} {
		yard, origin, err := t.stagingYardPass(ctx, origins, wantEvidence)
		if err != nil || yard != "" {
			return yard, origin, err
		}
	}
	return "", "", nil
}

// stagingYardPass walks the reachable origins once, admitting only yards whose stored listings match
// this pass — evidenced first, then the never-priced trait guesses.
func (t *expandTick) stagingYardPass(ctx context.Context, origins []string, wantEvidence bool) (string, string, error) {
	for _, origin := range origins {
		yards, err := t.probeYardsIn(ctx, origin)
		if err != nil {
			return "", "", err
		}
		for _, yard := range yards {
			// EVIDENCE THAT THIS COUNTER SELLS THE HULL WE NEED. ListProbeYards falls
			// back to every shipyard-TRAIT waypoint when a system holds no SHIP_PROBE
			// row, so a yard we have already priced and found probe-less comes back
			// looking exactly like one we have never looked at. Staging one of those
			// writes a want the buy queue scans, learns nothing from, and then
			// correctly refuses for the memo's whole TTL.
			stock, err := t.stagedProbeStock(ctx, yard)
			if err != nil {
				return "", "", err
			}
			if !stock.acceptsStaging(wantEvidence) {
				continue
			}
			// A hull of ours must be STANDING at this counter. Without it the buy
			// queue cannot fund the want at all — it only ever buys where one of our
			// hulls is already docked — so staging here would write a row that is
			// refused every tick forever.
			manned, err := t.staffedAt(ctx, yard)
			if err != nil {
				return "", "", err
			}
			if !manned {
				continue
			}
			// Asked of the SPARE half only. A yard that is also a parked market is
			// a perfectly good place to stage a seed — it is, in fact, the normal
			// case, since the yards worth buying at are the ones we already watch.
			if !t.book.occupied(yard, SlotKindSpare) {
				return yard, origin, nil
			}
		}
	}
	return "", "", nil
}

// stagedProbeStock reads what the stored listings say about one yard, memoised for the TICK.
// The same yard is offered for several targets in a neighbourhood and the answer cannot change
// while the tick runs, so the memo keeps this at one local store read per yard rather than one
// per (yard, target) pair.
//
// A READ FAILURE PROPAGATES. Staging onto a yard we could not read is how an unfundable want gets
// written; the tick is idempotent and re-derived, so failing loudly costs only a cycle.
func (t *expandTick) stagedProbeStock(ctx context.Context, yard string) (probeStock, error) {
	if stock, cached := t.listings[yard]; cached {
		return stock, nil
	}
	stock, _, err := readProbeStock(ctx, t.p.ListingMemo, t.playerID, yard, time.Now())
	if err != nil {
		return probeStockUnread, err
	}
	t.listings[yard] = stock
	return stock, nil
}

// acceptsStaging reports whether a yard belongs to this staging pass.
//
// THREE ANSWERS, TWO PASSES, AND ONE YARD ADMITTED BY NEITHER:
//
//   - SELLS — evidence. Taken on the FIRST pass, ahead of any trait guess however near, because a
//     want written here can actually be funded.
//   - UNREAD — never priced. Taken on the SECOND pass only. It is a guess, but it is also how the
//     fleet LEARNS where probes are sold, so ranking it last must not mean removing it.
//   - NONE — priced, and it sells no probe. Admitted by NEITHER pass. This is the standing fact the
//     buy queue's memo already refuses on; staging onto it writes a want that is scanned, learns
//     nothing, and is refused for the whole TTL. (A STALE reading is not this case — readProbeStock
//     degrades it to UNREAD, so a restocked counter is reconsidered.)
func (s probeStock) acceptsStaging(wantEvidence bool) bool {
	if wantEvidence {
		return s == probeStockSells
	}
	return s == probeStockUnread
}
