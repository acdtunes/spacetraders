package parkedsensing

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	domainSensing "github.com/andrescamacho/spacetraders-go/internal/domain/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// drainState is the running position one tick mutates as it buys: the treasury
// left, the floor it must stay above, and how many hulls are now owned. It is
// threaded by pointer so every pop sees the effect of the pop before it.
type drainState struct {
	credits  int64
	floor    int64
	owned    int64
	probeCap int64
}

// DrainBuyQueue fills as many wanted placements as the treasury and the probe
// cap allow, in priority order.
//
// Order: FILLS first — placements in systems already judged IN_SCOPE, deepest
// system first — then SEEDS, the spare hulls sent to frontier systems nobody has
// screened. A fill watches a market we know we want; a seed is speculative, so a
// tick that can afford one probe spends it on the known-good placement.
//
// Both ceilings are re-checked on EVERY pop, not once at the top: each purchase
// moves the treasury and grows the fleet, so a batch affordable when the tick
// started may not be by its third buy.
//
// THE TICK IS IN TWO HALVES: free work that fills a placement from a hull we
// already own, and work that pays for a new one. Either the operator's
// BuyKnobs.SpendEnabled switch or a HEAVY wave runs the first half and none of
// the second. A pass that can read a live price, claim a placement for purchase,
// or pay a counter belongs below that gate.
func DrainBuyQueue(
	ctx context.Context,
	p BuyPorts,
	playerID int,
	k BuyKnobs,
	clock shared.Clock,
) (BuyReport, error) {
	var rep BuyReport
	if clock == nil {
		clock = shared.NewRealClock()
	}

	// The WAVE is read FIRST, ahead of every gate, so rep.Wave is populated on EVERY return path —
	// including the ones that stop before a floor is built. It is published beside the growth
	// coordinator's own per-tick gauge, and the two must not disagree merely because a tick took a
	// short path. Every read behind this port is a LOCAL DB query, so it costs no API call.
	wave, waveReason, heavyTarget, spendHold, err := readWave(ctx, p, playerID)
	if err != nil {
		return rep, err
	}
	rep.Wave, rep.WaveProbeReason, rep.HeavyReserveTarget = wave, waveReason, heavyTarget
	rep.ProbeSpendHold = spendHold

	// Cheapest-first gate order: the ledger reads are local, the treasury and
	// price reads are network. A tick with nothing to buy must not cost an API call.
	candidates, yards, surface, covered, err := drainCandidates(ctx, p, playerID)
	// PUBLISHED BEFORE THE EMPTY-QUEUE RETURN, so an empty queue reports a zero it
	// measured rather than leaving the last tick's value standing. The error path
	// publishes nothing: an unread surface is not a small one.
	if err == nil {
		rep.DarkMarkets, rep.DarkMarketsHeld = surface.inReach, surface.held
		rep.DarkMarketsUnreached, rep.DarkMarketsReadable = surface.unreached, surface.readable
	}
	if err != nil || len(candidates) == 0 {
		return rep, err
	}
	// Published even on the paused path below: "the queue is ordered and the switch
	// is off" and "the ordering never sees a shipyard" must read differently.
	rep.YardsQueued, rep.YardsAtHead = yards.queued, yards.atHead

	// THE TWO PURCHASE GATES, resolved into ONE local so a later edit cannot update three of the
	// four sites that consult it. Both sit AHEAD of every money read because a tick that may not
	// buy must not price anything either: LiveCredits is an API call, and a ceiling evaluated
	// against a purchase that cannot happen yields a CapHeld/FloorHeld the operator has to
	// discount. The candidate list above is still built, because the free passes below need to
	// know which placements are open.
	//
	// WHY THE WAVE IS A BINARY GATE AND NOT A PRIORITY ORDERING. A heavy is a lump and probes are
	// a trickle; interleaving the two spenders means the treasury never reaches the lump, so a
	// reader who "simplifies" the wave into a per-class priority restores the exact condition this
	// design exists to remove.
	//
	// IT PAUSES PURCHASES ONLY. The free half below still re-tasks an idle spare and still flies a
	// surplus hull to a foothold at zero credits, so a pause keeps coverage growing on hulls already
	// paid for. The third gate can only SUBTRACT a buy: a conjunct in an AND (DeriveProbeSpendHold).
	mayBuy := k.SpendEnabled && wave != common.WaveHeavy && spendHold == common.ProbeSpendHoldNone
	rep.SpendingPaused = !mayBuy

	// st stays zero-valued while paused, and every consumer of it below is behind
	// the same gate: a zero cap beside a zero owned count would otherwise read as
	// "at the probe cap" and report a ceiling nobody consulted.
	var st drainState
	if mayBuy {
		var capHeld bool
		st, capHeld, err = openDrainBudget(ctx, p, playerID, k, clock.Now())
		if err != nil {
			return rep, err
		}
		if capHeld {
			rep.CapHeld = true
			return rep, nil
		}
	}

	t := &drainTick{
		p: p, playerID: playerID, k: k, mayBuy: mayBuy, st: &st, rep: &rep,
		// One memo per TICK, never longer: a refusal is re-learned from scratch next
		// tick, so a counter merely having a bad minute is retried, not blacklisted.
		memo: newRefusalMemo(&rep),
		// Per-TICK: it holds the surplus pool the tick allocates from, so two
		// placements cannot both be handed the same hull.
		footholds: &footholdBroker{},
		// Per-TICK: it holds where our hulls stand and the gate walker deciding which
		// of those places can reach a placement, so a burst of placements reads the
		// ledger once and each source's walk once.
		ferry: &ferryBroker{},
		// Per-TICK: the yard-price snapshot placements are ranked against and the ceiling
		// derived from it — one bulk price read and one topology read for the whole tick.
		procurement: newProcurementBroker(k),
	}

	// The attempts the FILLS may spend before standing aside for queued seeds — the
	// whole budget when none is outstanding. It SPLITS maxDrainAttempts, never adds
	// to it. See seedshare.go.
	fillBudget := fillAttemptBudget(candidates)

	// The share of the fill budget an already-held placement may spend before
	// standing aside for BuyKnobs.CoverageReserve. Computed only when armed, so
	// an unarmed tick never even evaluates coverageReserveActive. See coverageshare.go.
	coverageBudget := fillBudget
	if k.CoverageReserve > 0 {
		coverageBudget = coverageFillBudget(fillBudget, k.CoverageReserve, coverageReserveActive(candidates, covered, yards))
	}

	for _, slot := range candidates {
		if rep.Attempts >= maxDrainAttempts {
			break
		}
		if mayBuy && st.owned >= st.probeCap {
			rep.CapHeld = true
			break
		}
		// Checked BEFORE any read, so a fill standing aside costs nothing: the
		// loop runs past the remaining fills to reach the seeds behind them.
		if yieldsToSeeds(slot, rep.Attempts, fillBudget) {
			continue
		}
		if k.CoverageReserve > 0 && yieldsToCoverageReserve(slot, covered, yards, rep.Attempts, coverageBudget) {
			continue
		}

		// Whether THIS placement stands on a shipyard the fleet cannot price, taken
		// once because every fill path below reports against it, and at the moment of
		// funding — rep.Bought afterwards cannot say which slot the hull went to.
		darkYard := yards.wants(slot.Waypoint)

		funded, halt, err := t.fundPlacement(ctx, slot, clock.Now())
		if err != nil {
			return rep, err
		}
		t.countYardFill(darkYard && funded)
		if halt {
			break
		}
	}
	return rep, nil
}

// fundPlacement fills one placement by the cheapest means available, and reports
// whether it was funded and whether the whole drain must stop.
func (t *drainTick) fundPlacement(ctx context.Context, slot QueuedSlot, now time.Time) (bool, bool, error) {
	// The system's slots serve both the spare-reuse scan and the purchasing-hull
	// lookup below, so they are read once per pop.
	inSystem, err := t.p.Ledger.SlotsBySystem(ctx, t.playerID, slot.System)
	if err != nil {
		return false, false, fmt.Errorf("sensing slots in %q unreadable: %w", slot.System, err)
	}

	// A spare hull already standing in this system fills the placement for free.
	// Always preferred over buying — it spends nothing and consumes no cap headroom
	// (the hull merely changes which slot claims it).
	reused, err := t.reuseSpareHull(ctx, slot, inSystem)
	if err != nil {
		return false, false, err
	}
	if reused {
		t.rep.Reused++
		t.rep.Attempts++
		return true, false, nil
	}

	// PAUSED — by the operator's switch, a HEAVY wave or the no-consumer hold, which
	// stop purchases identically. The free half is done for this placement, and everything
	// past this point either prices a hull or pays for one. The foothold is still
	// attempted: it fills a placement by flying a hull we ALREADY OWN across a gate
	// — no credit, no API call — so switching it off with the purchases would starve
	// the placement machine of destinations while saving nothing (see
	// ExpandKnobs.SeedsEnabled). A placement it cannot fill is left WANTED for the
	// tick the pause lifts, and is NOT counted as SkippedNoYard, which means
	// something specific and would be a lie here.
	if !t.mayBuy {
		paused, err := t.footholds.fill(ctx, t.p, t.playerID, slot, t.rep)
		return paused, false, err
	}

	buys, walkedAway, err := t.resolvePurchaseCandidates(ctx, slot, inSystem, now)
	if err != nil {
		return false, false, err
	}
	if walkedAway {
		// Every counter in reach asked above the ceiling. The placement is left WANTED —
		// not claimed, and not counted as SkippedNoYard, which means something specific
		// and would be a lie here — and retried next tick. It costs NO ATTEMPT: the
		// refusal was decided from stored rows and touched no API.
		t.rep.WalkAwayHeld++
		return false, false, nil
	}
	if len(buys) == 0 {
		// No yard in this system has a hull of ours we can buy through. For a SPARE
		// placement this IS the buying deadlock: expansion asked for a foothold in a
		// system we have judged but never occupied, and buying there needs a hull
		// there already. The foothold path breaks it — it flies a surplus hull in
		// from within gate reach, after which the system funds itself. No money and
		// no API call, so it costs no attempt.
		foothold, err := t.footholds.fill(ctx, t.p, t.playerID, slot, t.rep)
		if err != nil {
			return false, false, err
		}
		if !foothold {
			// Everything else waits until expansion puts a usable probe within
			// reach — never a blind cross-map buy. Costs no attempt because it
			// touched no API.
			t.rep.SkippedNoYard++
		}
		return foothold, false, nil
	}

	claimed, err := t.claimForPurchase(ctx, slot, buys[0].yard)
	if err != nil {
		return false, false, err
	}
	if !claimed {
		return false, false, nil
	}

	boughtBefore := t.rep.Bought
	halt, err := t.fillSlot(ctx, slot, buys)
	if err != nil {
		return false, false, err
	}
	return t.rep.Bought > boughtBefore, halt, nil
}

// readWave resolves this tick's regime. AN UNWIRED READER IS THE PROBE WAVE, never HEAVY: a nil
// seam means no heavy buyer is deployed, and failing the other way would let a wiring omission
// pause probe buying forever. AN ERRORING READER FAILS THE TICK CLOSED, because a silently-PROBE
// tick derived from an unreadable signal is the blind spend RULINGS #4 forbids. It holds nothing.
func readWave(ctx context.Context, p BuyPorts, playerID int) (common.Wave, common.WaveProbeReason, common.HeavyReserveTarget, common.ProbeSpendHold, error) {
	if p.Wave == nil {
		return common.WaveProbe, common.WaveProbeReasonGrowthDisabled, 0, common.ProbeSpendHoldNone, nil
	}
	wave, reason, target, hold, err := p.Wave.Wave(ctx, playerID)
	if err != nil {
		return "", "", 0, common.ProbeSpendHoldNone, fmt.Errorf("wave unreadable, buying nothing this tick: %w", err)
	}
	if target <= 0 {
		target = 0
	}
	return wave, reason, target, hold, nil
}

// openDrainBudget prices the tick. The second return reports the probe cap
// already met, which stops the drain before a single money read.
func openDrainBudget(
	ctx context.Context,
	p BuyPorts,
	playerID int,
	k BuyKnobs,
	now time.Time,
) (drainState, bool, error) {
	owned, err := p.Ledger.CountOwnedProbes(ctx, playerID)
	if err != nil {
		return drainState{}, false, fmt.Errorf("probe cap unreadable, buying nothing this tick: %w", err)
	}
	if owned >= int64(k.ProbeCap) {
		return drainState{}, true, nil
	}

	credits, err := p.Treasury.LiveCredits(ctx, playerID)
	if err != nil {
		return drainState{}, false, fmt.Errorf("treasury unreadable, buying nothing this tick: %w", err)
	}
	spend, err := p.CargoSpend.AbsCargoBuySpendSince(ctx, playerID, now.Add(-cargoSpendLookback))
	if err != nil {
		// An unknowable cargo outflow is NOT a zero one. Reading it as zero collapses
		// the runway term and hands back the cheapest floor available exactly when we
		// understand the least.
		return drainState{}, false, fmt.Errorf("cargo spend unreadable, buying nothing this tick: %w", err)
	}
	// THE WAVE OWNS THE HEAVY HOLD-BACK NOW, so no heavy term reaches this floor. A ramp and a
	// binary gate are two mechanisms doing one job, and the ramp was the one that could only ever
	// half-stop the trickle. NO GUARD IS RELAXED (RULINGS #4): ProbeBuyFloor floors its result at
	// the immutable reserve, so dropping a non-negative addend can lower the floor only as far as
	// that guard and never past it, and the capex reserve, runway term and probe cap all still bind.
	return drainState{
		credits:  credits,
		owned:    owned,
		probeCap: int64(k.ProbeCap),
		floor: domainSensing.ProbeBuyFloor(
			common.ImmutableReserveFloor,
			k.CapexReserve,
			domainSensing.CargoSpendPerHour(spend),
			k.KMilli,
		),
	}, false, nil
}

// fillSlot buys one hull for a claimed placement, trying each yard where we have
// a usable purchasing hull until one sells us a probe. It reports whether the
// whole drain should stop.
//
// Working down the candidate yards matters because a refusal is usually LOCAL to
// the counter — out of stock, a hull that moved between selection and purchase —
// while the placement is still fillable at the yard next door. Abandoning the
// slot on the first refusal leaves it claimed and unfilled every tick whenever
// its nearest yard is the unreliable one.
//
// Every yard tried costs an attempt, including the ones that fail. See
// maxDrainAttempts for why failure must not be the cheap path.
func (t *drainTick) fillSlot(ctx context.Context, slot QueuedSlot, buys []purchaseCandidate) (bool, error) {
	for _, candidate := range buys {
		// A counter that already refused THIS TICK is not asked again: the refusal
		// belongs to the counter, not to the placement that met it first, so
		// re-asking spends an attempt a working yard would have used to re-learn a
		// recorded fact. The skip costs no attempt — it touches no API.
		if t.memo.blocks(candidate.yard, candidate.buyer) {
			continue
		}
		if t.rep.Attempts >= maxDrainAttempts {
			return true, nil
		}
		t.rep.Attempts++

		quote, err := t.p.Purchaser.Quote(ctx, t.playerID, candidate.yard)
		if err != nil {
			// Unpriceable counter: no floor check possible, try the next. No buyer is
			// named because no hull was engaged in it.
			t.memo.record(BuyStepQuote, candidate.yard, "", err.Error())
			continue
		}
		// THE WALK-AWAY, ON THE BILL RATHER THAN ON A READING. The ranking judged this
		// counter's STORED ask; this is what it actually charges, and the two differ
		// exactly when it matters — a yard whose price ran away between readings looks
		// cheap in the snapshot and expensive here. BEFORE the floor, because the two say
		// different things: the floor is "the fleet cannot afford this", this is "the
		// fleet will not pay this". It only ever removes a counter (RULINGS #4).
		if t.procurement.overCeiling(quote) {
			t.memo.record(BuyStepWalkAway, candidate.yard, "", t.procurement.walkAwayReason(quote))
			continue
		}
		// THE FLOOR BINDS ON LANDED COST, NOT STICKER. A probe bought at a counter in
		// another system still has to be flown to its post, and every gate it crosses
		// charges a fee the quote alone does not see. Landed cost can only ever make
		// the guard STRICTER: ferry is non-negative by construction and
		// LandedProbeCost is floored at the quote, so a placement bought at its own
		// destination prices at the quote alone.
		ferry := candidate.ferryCost()
		landed := domainSensing.LandedProbeCost(quote, ferry)
		if t.st.credits-landed < t.st.floor {
			// At the floor. Stop rather than shop for a cheaper yard: the floor
			// protects working capital, and a cheaper probe erodes it just the same.
			t.rep.FloorHeld = true
			return true, nil
		}

		probe, err := t.p.Purchaser.Buy(ctx, t.playerID, candidate.buyer, candidate.yard, t.p.ClaimOwnerContainerID)
		if err != nil || probe.ShipSymbol == "" {
			// This counter refused; the placement is still fillable elsewhere. The
			// buyer IS named here: the hull is what tells "the yard is out of stock"
			// apart from "this hull cannot buy".
			t.memo.record(BuyStepBuy, candidate.yard, candidate.buyer, buyRefusalReason(err))
			continue
		}

		if err := t.recordPurchase(ctx, slot, candidate.yard, probe); err != nil {
			return true, err
		}
		t.rep.Bought++
		if candidate.ferried {
			// A subset of Bought: only this point still knows which counter sold.
			t.rep.Ferried++
		}
		t.st.owned++

		paid := chargedForProbe(quote, probe.Price)
		// The ferry is subtracted AFTER postBuyCredits, never folded into `paid`.
		// postBuyCredits reconciles against probe.CreditsAfter — the authoritative
		// balance the instant the purchase settled — and the ferry has not been flown
		// yet, so it is absent from that balance by definition. Folded into `paid` it
		// would be compared against a balance that predates it and the
		// `CreditsAfter < arithmetic` branch would silently discard it every time.
		// Held back separately, it reserves the delivery against the placements this
		// same tick has yet to pop.
		t.st.credits = postBuyCredits(t.st.credits, paid, probe) - ferry

		logProbeLandedCost(ctx, slot, candidate, probe, paid, ferry)

		if t.haltForPriceDrift(ctx, candidate, probe, quote) {
			return true, nil
		}
		return false, nil
	}
	return false, nil
}

func (c purchaseCandidate) ferryCost() int64 {
	return domainSensing.FerryCost(
		domainSensing.FerryHops(c.ferried, c.ferryHops),
		domainSensing.DefaultGateFeeCredits,
	)
}

// chargedForProbe takes the LARGER of quote and charged: a purchase path reporting
// no price would otherwise read as a free hull, which a money guard must never do.
func chargedForProbe(quote, charged int64) int64 {
	if charged > quote {
		return charged
	}
	return quote
}

// logProbeLandedCost publishes purchase, ferry and landed cost SEPARATELY: read
// on the purchase alone, an expansion costs only what it quoted.
func logProbeLandedCost(
	ctx context.Context,
	slot QueuedSlot,
	candidate purchaseCandidate,
	probe BoughtProbe,
	paid, ferry int64,
) {
	logging.LoggerFromContext(ctx).Log("INFO", "sensing probe bought", map[string]interface{}{
		"action":      "parked_sensing_probe_landed_cost",
		"ship_symbol": probe.ShipSymbol,
		"waypoint":    slot.Waypoint,
		"yard":        candidate.yard,
		"ferried":     candidate.ferried,
		"ferry_hops":  domainSensing.FerryHops(candidate.ferried, candidate.ferryHops),
		"purchase":    paid,
		"ferry":       ferry,
		"landed":      domainSensing.LandedProbeCost(paid, ferry),
	})
}

// haltForPriceDrift stops the tick when the market moved against us mid-purchase.
// The hull stays bought — refusing to record it would orphan a real probe and
// undercount the cap. What CAN be stopped is spending on quotes taken before it.
func (t *drainTick) haltForPriceDrift(ctx context.Context, candidate purchaseCandidate, probe BoughtProbe, quote int64) bool {
	if probe.Price <= quote {
		return false
	}
	t.rep.HaltedPriceDrift = true
	logging.LoggerFromContext(ctx).Log("WARN", "sensing probe cost more than quoted; halting the buy queue for this tick", map[string]interface{}{
		"action":      "parked_sensing_price_drift_halt",
		"ship_symbol": probe.ShipSymbol,
		"waypoint":    candidate.yard,
		"quoted":      quote,
		"paid":        probe.Price,
	})
	return true
}

// postBuyCredits is the treasury the NEXT pop's floor check runs against. The
// shipyard's own post-settlement balance is authoritative when it reports one, but
// is only ACCEPTED when it is the more conservative of the two: a balance higher
// than (before − price) would relax the floor on a value we did not compute.
func postBuyCredits(before, price int64, probe BoughtProbe) int64 {
	arithmetic := before - price
	if probe.CreditsAfter > 0 && probe.CreditsAfter < arithmetic {
		return probe.CreditsAfter
	}
	return arithmetic
}

type drainTick struct {
	p        BuyPorts
	playerID int
	k        BuyKnobs
	// mayBuy is the tick's ONE purchase verdict — the operator's switch AND the wave, resolved
	// once at the top. Carried rather than re-derived so the loop's paused branch and the gate
	// that set rep.SpendingPaused cannot answer differently.
	mayBuy      bool
	st          *drainState
	rep         *BuyReport
	memo        *refusalMemo
	footholds   *footholdBroker
	ferry       *ferryBroker
	procurement *procurementBroker
}
