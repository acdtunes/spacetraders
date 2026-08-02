package ship

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/yardscan"
)

// defaultYardPresenceReqPerSec is how fast the fleet may START repositioning
// hulls to unpriced yards — the meter behind PresenceRequests.
//
// WHAT IS BEING METERED IS NOT THIS PACKAGE'S OWN TRAFFIC, and that is the whole
// reason it is a rate rather than a per-tick count. A shipyard READ costs one
// request and is charged where it is spent. A REPOSITION costs a Navigate, an
// Orbit and a Dock — and a refuel when the hop is long — all issued later, by the
// placement machine, on a tick this budget never sees. So this allowance paces the
// DECISION that causes that traffic, which is the only point at which it can be
// paced at all: once the claim is written the flying is already owed.
//
// SIZED AGAINST THE HEADROOM, not against the work. The server ceiling is a hard
// 2.00 req/s and the fleet runs at about 1.74, so roughly 0.26 is unspent and the
// shipyard read budget above already claims 0.12 of it. One reposition per fifty
// seconds costs about 0.06 req/s of downstream navigation — under a quarter of
// what is left — and still sweeps the ~100 yards that are actually addressable in
// well under two hours. Going faster would buy a shorter sweep of a set that is
// bounded by how many hulls can be spared, not by how fast requests are issued.
const defaultYardPresenceReqPerSec = 0.02

// yardPresenceBurst is the depth of the reposition bucket.
//
// TWO, and deliberately far shallower than yardBurstRequests. A read bucket wants
// depth so a tour touching several yards in one tick is absorbed rather than
// declined; a reposition bucket wants the opposite, because every token it banks
// is a hull that will be pulled off a working market in one burst. Two allows a
// system whose catalogue has just landed to be worked on the tick it lands
// without letting a quiet hour accumulate an evacuation.
const yardPresenceBurst = 2

// PresenceRequests reports the yards the budget wants priced and cannot see,
// best first, capped at limit.
//
// THIS IS THE BUDGET ADMITTING WHAT IT CANNOT FIX. Its top weight tier is
// "confirmed seller of a hull we want, at a price we have never seen", and the
// rotation drives those yards to the head of the queue exactly as designed — but
// the read it then spends comes back WITHOUT a price, because purchasePrice only
// appears in the response when a hull of ours is standing at the counter. The
// demand is correct and the remedy is not a read. So the same tier is published
// here as a request for PRESENCE, which is the only thing that turns it into a
// price. See yardscan.WantsPresence.
//
// A PULL, NEVER A PUSH, and that is a deliberate defence rather than a style
// choice. A budget that PUBLISHED presence demand into a mover would be a
// latching bridge: the mover would hold the last signal it was sent, and a yard
// that got priced — or a fleet that stopped shopping for the type — would leave a
// stale request standing that keeps pulling hulls at a counter nobody needs. Here
// the set is recomputed from live facts on every call, so a yard leaves it the
// moment it is priced and the whole set empties the moment demand decays. There
// is no state to retract because nothing was ever stored.
//
// A ZERO OR NEGATIVE limit yields nothing rather than everything. The caller's
// bound is the last line of defence against a burst of hull moves, and the reading
// that turns an absent bound into "unbounded" is the one that would hurt.
func (b *YardScanBudget) PresenceRequests(ctx context.Context, playerID int, limit int) []yardscan.PresenceRequest {
	if b == nil || limit <= 0 {
		return nil
	}
	// Refreshed FIRST, and this is the restart case that makes the whole feature
	// work rather than an optimisation. A daemon that has just come up has observed
	// nothing, so facts is empty and this would report no yard needs presence — on a
	// fleet where 81 heavy counters sit unpriced in the DATABASE. The store read is
	// what makes the request set survive a restart.
	b.refreshFacts(ctx, playerID)

	b.mu.Lock()
	defer b.mu.Unlock()

	requests := make([]yardscan.PresenceRequest, 0, len(b.facts))
	for waypoint, f := range b.facts {
		if waypoint == "" || !yardscan.WantsPresence(f) {
			continue
		}
		// Targeted is applied here and not stored, exactly as weightLocked does it,
		// so a yard a money guard is buying at right now sorts above an idle lead
		// of the same class.
		f.Targeted = b.targetedLocked(waypoint)
		requests = append(requests, yardscan.PresenceRequest{
			Waypoint: waypoint,
			// Derived from the symbol rather than carried from the inventory row's
			// own system column, because the CONSUMER keys its search by the sensing
			// ledger's system field, which is derived the same way (see
			// newPurchaseCandidate). Two derivations that could disagree would make
			// the pass silently find no hull to send instead of failing loudly.
			System: shared.ExtractSystemSymbol(waypoint),
			Heavy:  b.heavySeller[waypoint],
			Weight: yardscan.Weight(f, b.policy.ValueClampR),
		})
	}

	ranked := yardscan.RankPresence(requests)
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}

// AdmitPresence consumes one reposition from the allowance, reporting whether
// there was one to consume.
//
// UNLIKE Admit, THERE IS NO OVERDRAFT AND NO FORCED CLASS. A shipyard read can be
// something a money guard must have — RULINGS #4 forbids serving a pre-buy price
// check from store — so that allowance has to be able to go negative and let the
// debt squeeze discretionary traffic instead of refusing a guard. Nothing about a
// reposition is ever that: no purchase waits on one, and a yard left unpriced for
// another minute costs only that the buy loop keeps choosing among the counters it
// can already see. So this refuses cleanly when the bucket is empty, which is what
// keeps a backlog of a hundred addressable yards from being swept in one tick.
func (b *YardScanBudget) AdmitPresence() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.presence.AllowN(b.now(), 1) {
		b.presenceDeclined++
		return false
	}
	b.presenceIssued++
	return true
}
