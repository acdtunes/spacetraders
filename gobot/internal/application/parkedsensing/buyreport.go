package parkedsensing

import "github.com/andrescamacho/spacetraders-go/internal/application/common"

// BuyStep names which half of the purchase path refused. The two are worth telling
// apart because they fail for different reasons and call for different operator
// responses: a QUOTE refusal is the yard (out of stock, unpriced listing, an API
// that will not answer), while a BUY refusal is the transaction (a hull that cannot
// be claimed or docked, a counter that declined, credits that moved between pricing
// and paying).
type BuyStep string

const (
	// BuyStepQuote is a refusal at the live price read, BEFORE any floor check
	// is possible and before any hull is engaged.
	BuyStepQuote BuyStep = "quote"
	// BuyStepBuy is a refusal at the counter, after the price cleared the floor.
	BuyStepBuy BuyStep = "buy"
	// BuyStepMemo is a yard passed over WITHOUT being asked, because a stored listing
	// read already says it sells no probe. It is reported separately from
	// BuyStepQuote precisely because it costs no API call: an operator reading the
	// cycle summary must be able to tell "this counter refused us today" from "we did
	// not ask, and here is why".
	BuyStepMemo BuyStep = "memo"
)

// BuyRefusal is one counter's refusal this tick, with the number of placements that
// met it.
//
// It is AGGREGATED rather than recorded per attempt: the drain runs continuously, so
// a per-attempt record would turn a standing refusal into an unbounded log. One row
// per distinct refusal carrying how many placements it blocked says the same thing in
// a line an operator can read, and Count is what distinguishes "one placement is
// unlucky" from "this counter is blocking the whole queue".
type BuyRefusal struct {
	// Step is which half refused. See BuyStep.
	Step BuyStep
	// Yard is the counter that refused.
	Yard string
	// Buyer is the hull the purchase would have gone through. EMPTY on a quote
	// refusal, where no hull was engaged — an empty Buyer is meaningful, not missing.
	Buyer string
	// Reason is the underlying error, verbatim. It carries "out of stock" vs "hull
	// not docked" vs "insufficient credits", so it is never summarised or truncated.
	Reason string
	// Count is how many placements this same refusal blocked this tick.
	Count int
}

// BuyReport is one drain tick's outcome, for the heartbeat.
type BuyReport struct {
	// Bought counts probes purchased; Reused counts placements filled by re-tasking a
	// spare hull instead, which costs nothing.
	Bought, Reused int
	// Queued counts placements claimed for purchase this tick.
	Queued int
	// SkippedNoYard counts placements passed over because no yard in their system had
	// a hull of ours standing at it to buy through. Expected and benign — the
	// placement waits for expansion to establish presence.
	SkippedNoYard int
	// Footholds counts SPARE placements filled by flying a surplus hull across a gate,
	// establishing presence in a system that could not fund one itself (foothold.go).
	// Reported SEPARATELY from Reused, which it otherwise resembles, because a reuse
	// moves an idle spare and costs nothing while a foothold takes a hull off a
	// working market and leaves that market unwatched until it is re-bought.
	Footholds int
	// Ferried counts purchases made at a counter in ANOTHER system, with the hull then
	// flown to the placement (see ferry.go). A SUBSET of Bought, reported separately
	// because a local purchase is scanning within a tick or two while a ferried one is
	// several gate steps away, counting against the probe cap the whole time.
	Ferried int
	// Attempts counts every trip through the buy path, successful or not, against
	// maxDrainAttempts.
	Attempts int
	// Refusals is why the counters that refused this tick refused, one row per distinct
	// refusal. A tick with Attempts > 0 and Bought == 0 and no Refusals is a
	// contradiction — every path that burns an attempt without buying records one.
	Refusals []BuyRefusal
	// Wave is the regime this tick ran in, and WaveProbeReason names which clause forced
	// PROBE (empty on the HEAVY wave). Both are published on EVERY return path that
	// derived a regime, including those stopping before a floor is built: the growth
	// coordinator publishes its own copy every tick, and two series disagreeing merely
	// because a tick took a short path are indistinguishable from a split-brain.
	//
	// Wave is EMPTY only when no regime could be derived — the reader errored and the
	// tick aborted. A third state, not a PROBE, and it must not be published as one.
	Wave            common.Wave
	WaveProbeReason common.WaveProbeReason
	// HeavyReserveTarget is the ASK the fleet is saving toward for the next heavy —
	// what the target yard charges. Published on EVERY return path, including the ones
	// that stop before a floor is built.
	//
	// IT IS REPORTING ONLY. Since the wave owns the hold-back it reaches no floor and no
	// guard; it is what tells an operator watching a HEAVY wave WHAT the fleet is
	// pausing probe buying for.
	HeavyReserveTarget common.HeavyReserveTarget
	// SpendingPaused reports that this tick made no purchase at all because a purchase
	// gate was shut — the operator's expansion switch (BuyKnobs.SpendEnabled) or a HEAVY
	// wave. WHICH of the two is read off Wave, which is why both are published: an
	// operator told only "paused" cannot tell "you switched this off" from "the fleet is
	// saving for a hull", and would go hunting for a knob nobody touched.
	//
	// A SEPARATE FIELD RATHER THAN A FloorHeld VALUE, for the same reason
	// ExpandReport.SpendingPaused is separate from Skipped: a floor or a cap is the
	// fleet declining to afford a probe it still wants, and an operator reading one
	// starts asking about the treasury. It is mutually exclusive with CapHeld and
	// FloorHeld by construction — a paused tick never reads the treasury or the fleet
	// count, so neither ceiling can be evaluated and the heartbeat reports exactly one
	// reason.
	SpendingPaused bool
	// CapHeld and FloorHeld report which ceiling stopped the drain.
	CapHeld, FloorHeld bool
	// YardsQueued, YardsAtHead and YardsFilled are the yard-aware ordering's accounting
	// (yardqueue.go). They exist because a coordinator LOSING every one of these
	// decisions would otherwise look identical to one with nothing to decide.
	//
	//   - YardsQueued: candidate placements standing on a shipyard whose price the
	//     fleet cannot see — the rows the ordering was CONSULTED on.
	//   - YardsAtHead: how many of those the ordering delivered into the first
	//     maxDrainAttempts places, the window this tick's budget can reach. High
	//     YardsQueued beside a persistent zero here is the ordering failing.
	//   - YardsFilled: how many of the placements FUNDED this tick — by reuse,
	//     foothold or purchase — stood on one. The only one of the three that costs
	//     anything, and correctly zero while the expansion switch is off.
	YardsQueued, YardsAtHead, YardsFilled int
	// The COVERAGE SURFACE — marketplaces wanted with no probe standing in them, in
	// parts on the growth_lane_surface pattern: DarkMarkets is the half a hull can walk
	// to, DarkMarketsHeld the SATURATION BACKLOG within it (systems the fleet ALREADY
	// STANDS IN, what the drain's ordering drives to zero), DarkMarketsUnreached what no
	// hull can walk to, and DarkMarketsReadable false when reachability was unresolvable
	// — a zero Unreached beside a false there is a BLIND read, not a verdict.
	//
	// ALL FOUR ARE PUBLISHED WHILE PAUSED, which is why they are measured ahead of the
	// purchase gates: blocked-by-hold must not read as blocked-by-bug.
	DarkMarkets, DarkMarketsHeld, DarkMarketsUnreached int
	DarkMarketsReadable                                bool
	// HaltedPriceDrift reports that a yard charged MORE than it had just quoted, and
	// the drain stopped for the tick because of it. The hull is still bought and
	// recorded — an overrun cannot un-buy it — but every remaining quote this tick was
	// taken against a market that has since moved, so none can gate a further
	// purchase. The next tick re-quotes from scratch.
	HaltedPriceDrift bool
}

// countYardFill records that a funded placement stood on a shipyard the fleet cannot
// price. One named helper rather than four inline increments, because it is called
// from every path that fills a placement — reuse, the paused foothold, the funded
// foothold and the purchase — and a fill path added later that forgets it fails
// nothing, it just quietly under-reports the one outcome this ordering produces.
func (t *drainTick) countYardFill(filled bool) {
	if filled {
		t.rep.YardsFilled++
	}
}

// refusalMemo remembers which counters have already refused this tick, and carries
// the aggregation behind BuyReport.Refusals. It answers one question cheaply — has
// this counter already told us no? — so the drain does not spend its attempt budget
// re-asking every yard once per placement.
type refusalMemo struct {
	// idx maps a refusal key to the BuyReport.Refusals row already recorded for it,
	// so a repeat increments a count instead of appending a duplicate row.
	idx map[string]int
	rep *BuyReport
}

func newRefusalMemo(rep *BuyReport) *refusalMemo {
	return &refusalMemo{idx: make(map[string]int), rep: rep}
}

// quoteKey scopes a quote refusal to the YARD ALONE. An unpriceable counter is
// unpriceable no matter which hull we would have bought through, so naming the buyer
// would let the same dead yard be re-quoted once per hull standing on it.
func quoteKey(yard string) string { return "quote\x00" + yard }

// buyKey scopes a buy refusal to the yard AND the hull. The pairing is the point: the
// same yard may well sell through a different hull (the refusal is often the hull's —
// an unclaimable or undocked one), so a buy refusal must not condemn the counter.
func buyKey(yard, buyer string) string { return "buy\x00" + yard + "\x00" + buyer }

// blocks reports whether this candidate has already refused this tick, counting the
// placement it just blocked. Both keys are consulted: a yard that failed to QUOTE
// cannot be bought from through any hull, so the quote refusal blocks every candidate
// on that yard, while a buy refusal blocks only the pairing that actually failed.
func (m *refusalMemo) blocks(yard, buyer string) bool {
	for _, key := range []string{quoteKey(yard), buyKey(yard, buyer)} {
		if i, ok := m.idx[key]; ok {
			m.rep.Refusals[i].Count++
			return true
		}
	}
	return false
}

// record files one refusal, folding a repeat into the existing row's count.
func (m *refusalMemo) record(step BuyStep, yard, buyer, reason string) {
	key := quoteKey(yard)
	row := BuyRefusal{Step: step, Yard: yard, Reason: reason, Count: 1}
	if step == BuyStepBuy {
		key = buyKey(yard, buyer)
		row.Buyer = buyer
	}
	if i, ok := m.idx[key]; ok {
		m.rep.Refusals[i].Count++
		return
	}
	m.idx[key] = len(m.rep.Refusals)
	m.rep.Refusals = append(m.rep.Refusals, row)
}

// buyRefusalReason names the nil-error refusal explicitly. A purchase that returns
// no error and no hull is the one shape that would otherwise record an EMPTY reason,
// so it is given words rather than left blank.
func buyRefusalReason(err error) string {
	if err != nil {
		return err.Error()
	}
	return "the purchase path reported no hull and no error"
}
