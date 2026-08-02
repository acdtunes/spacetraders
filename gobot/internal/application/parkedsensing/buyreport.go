package parkedsensing

// BuyStep names which half of the purchase path refused. The two are worth
// telling apart because they fail for entirely different reasons and call for
// entirely different operator responses: a QUOTE refusal is the yard (out of
// stock, unpriced listing, an API that will not answer), while a BUY refusal is
// the transaction (a hull that cannot be claimed or docked, a counter that
// declined, credits that moved between pricing and paying). Collapsing them
// into one "it did not work" is what made this queue's failures unreadable.
type BuyStep string

const (
	// BuyStepQuote is a refusal at the live price read, BEFORE any floor check
	// is possible and before any hull is engaged.
	BuyStepQuote BuyStep = "quote"
	// BuyStepBuy is a refusal at the counter, after the price cleared the floor.
	BuyStepBuy BuyStep = "buy"
	// BuyStepMemo is a yard passed over WITHOUT being asked, because a stored
	// listing read already says it sells no probe.
	//
	// It is reported separately from BuyStepQuote precisely because it costs no
	// API call: an operator reading the cycle summary must be able to tell "this
	// counter refused us today" from "we did not ask, and here is why". Conflating
	// them would hide the very saving this step exists to make, and would make the
	// next defect of this shape as invisible as this one was.
	BuyStepMemo BuyStep = "memo"
)

// BuyRefusal is one counter's refusal this tick, with the number of placements
// that met it.
//
// It is AGGREGATED rather than recorded per attempt for a reason that is
// operational, not cosmetic: this drain runs every ~30s forever, so a per-attempt
// record would turn a standing refusal into an unbounded log. One row per
// distinct refusal, carrying how many placements it blocked, says the same thing
// in a line an operator can actually read — and Count is the number that
// distinguishes "one placement is unlucky" from "this counter is blocking the
// whole queue".
type BuyRefusal struct {
	// Step is which half refused. See BuyStep.
	Step BuyStep
	// Yard is the counter that refused.
	Yard string
	// Buyer is the hull the purchase would have gone through. EMPTY on a quote
	// refusal, where no hull was engaged — an empty Buyer is therefore
	// meaningful, not missing data.
	Buyer string
	// Reason is the underlying error, verbatim. This is the field that carries
	// "out of stock" vs "hull not docked" vs "insufficient credits", so it is
	// never summarised or truncated here.
	Reason string
	// Count is how many placements this same refusal blocked this tick.
	Count int
}

// BuyReport is one drain tick's outcome, for the heartbeat.
type BuyReport struct {
	// Bought counts probes purchased; Reused counts placements filled by
	// re-tasking a spare hull instead, which costs nothing.
	Bought, Reused int
	// Queued counts placements claimed for purchase this tick.
	Queued int
	// SkippedNoYard counts placements passed over because no yard in their
	// system had a hull of ours standing at it to buy through. Expected and
	// benign — the placement waits for expansion to establish presence.
	SkippedNoYard int
	// Footholds counts SPARE placements filled by flying a surplus hull across
	// a gate — the path that establishes presence in a system that could not
	// fund one for itself. See foothold.go.
	//
	// It is reported SEPARATELY from Reused, which it otherwise resembles,
	// because the two spend different things: a reuse moves an idle spare and
	// costs nothing, while a foothold takes a hull off a working market and
	// leaves that market unwatched until it is re-bought. An operator must be
	// able to see the second happening without inferring it.
	Footholds int
	// Ferried counts purchases made at a counter in ANOTHER system, with the hull
	// then flown to the placement. See ferry.go.
	//
	// A SUBSET of Bought, reported separately for the same reason Footholds is
	// reported separately from Reused: the two spend the same money but deliver on
	// very different timescales. A local purchase is scanning within a tick or two;
	// a ferried one is several gate steps away, counting against the probe cap the
	// whole time. An operator watching a tick that bought three probes must be able
	// to see which of them will not report a price for a while.
	Ferried int
	// Attempts counts every trip through the buy path, successful or not,
	// against maxDrainAttempts.
	Attempts int
	// Refusals is why the counters that refused this tick refused, one row per
	// distinct refusal. A tick with Attempts > 0 and Bought == 0 and no Refusals
	// is a contradiction — every path that burns an attempt without buying
	// records one.
	Refusals []BuyRefusal
	// HeavyReserve is the credits held back for the next heavy this tick. It is on
	// the report for ONE reason (spec risk 3): while a heavy accumulates, probe
	// buying stops, and on a thin treasury that looks identical to sensing having
	// died. A non-zero value here beside FloorHeld says "saving for a heavy",
	// which is the difference between an operator waiting and an operator paging.
	HeavyReserve int64
	// SpendingPaused reports that the operator's expansion switch is off, so this
	// tick made no purchase at all — see BuyKnobs.SpendEnabled.
	//
	// A SEPARATE FIELD RATHER THAN A FloorHeld VALUE, for the same reason
	// ExpandReport.SpendingPaused is separate from Skipped: a floor or a cap is the
	// fleet declining to afford a probe it still wants, and an operator reading one
	// starts asking about the treasury. This is the operator's own choice, already
	// made, and the cycle line has to say so in those words or the next money hunt
	// begins at the wrong end again.
	//
	// It is mutually exclusive with CapHeld and FloorHeld by construction: a paused
	// tick never reads the treasury or the fleet count, so neither ceiling can be
	// evaluated, and the heartbeat can therefore report exactly one reason.
	SpendingPaused bool
	// CapHeld and FloorHeld report which ceiling stopped the drain.
	CapHeld, FloorHeld bool
	// YardsQueued, YardsAtHead and YardsFilled are the yard-aware ordering's
	// accounting (yardqueue.go). They exist because a coordinator LOSING every one
	// of these decisions would otherwise look identical to one with nothing to
	// decide.
	//
	//   - YardsQueued: candidate placements standing on a shipyard whose price the
	//     fleet cannot see. The rows the ordering was CONSULTED on.
	//   - YardsAtHead: how many of those the ordering delivered into the first
	//     maxDrainAttempts places — the window this tick's budget can reach. High
	//     YardsQueued beside a persistent zero here is the ordering failing: 78 heavy
	//     counters sat in 8,934 rows with a head of six.
	//   - YardsFilled: how many of the placements actually FUNDED this tick — by
	//     reuse, foothold or purchase — stood on one. Presence achieved, and the
	//     only one of the three that costs anything. It reads zero while the
	//     expansion switch is off, which is correct: the queue is ordered and
	//     nothing is bought.
	YardsQueued, YardsAtHead, YardsFilled int
	// HaltedPriceDrift reports that a yard charged MORE than it had just
	// quoted, and the drain stopped for the tick because of it. The hull is
	// still bought and recorded — an overrun cannot un-buy it — but every
	// remaining quote this tick was taken against a market that has since moved,
	// so none of them can be trusted to gate a further purchase. The next tick
	// re-quotes from scratch and proceeds normally.
	HaltedPriceDrift bool
}

// countYardFill records that a funded placement stood on a shipyard the fleet
// cannot price.
//
// One named helper rather than four inline increments, because it is called from
// every path that fills a placement — reuse, the paused foothold, the funded
// foothold and the purchase — and a fill path added later that forgets it would
// not fail anything, it would just quietly under-report the one outcome this
// ordering exists to produce.
func (t *drainTick) countYardFill(filled bool) {
	if filled {
		t.rep.YardsFilled++
	}
}

// refusalMemo remembers which counters have already refused this tick, and
// carries the aggregation behind BuyReport.Refusals.
//
// It exists to answer one question cheaply: has this counter already told us
// no? Before it, the drain re-asked every yard once per placement, which on the
// live fleet meant two counters were asked the same question three times each
// and the entire six-attempt budget was consumed re-confirming two facts.
type refusalMemo struct {
	// idx maps a refusal key to the BuyReport.Refusals row already recorded for
	// it, so a repeat increments a count instead of appending a duplicate row.
	idx map[string]int
	rep *BuyReport
}

func newRefusalMemo(rep *BuyReport) *refusalMemo {
	return &refusalMemo{idx: make(map[string]int), rep: rep}
}

// quoteKey scopes a quote refusal to the YARD ALONE. An unpriceable counter is
// unpriceable no matter which hull we would have bought through, so naming the
// buyer here would let the same dead yard be re-quoted once per hull standing on
// it.
func quoteKey(yard string) string { return "quote\x00" + yard }

// buyKey scopes a buy refusal to the yard AND the hull. The pairing is the
// point: the same yard may well sell through a different hull (the refusal is
// often the hull's — an unclaimable or undocked one), so a buy refusal must not
// condemn the counter itself.
func buyKey(yard, buyer string) string { return "buy\x00" + yard + "\x00" + buyer }

// blocks reports whether this candidate has already refused this tick, counting
// the placement it just blocked.
//
// Both keys are consulted: a yard that failed to QUOTE cannot be bought from
// through any hull, so the quote refusal blocks every candidate on that yard,
// while a buy refusal blocks only the pairing that actually failed.
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

// buyRefusalReason names the nil-error refusal explicitly.
//
// A purchase that returns no error and no hull is the one shape that would
// otherwise record an EMPTY reason — which is precisely the silence this whole
// change exists to remove, so it is given words rather than left blank.
func buyRefusalReason(err error) string {
	if err != nil {
		return err.Error()
	}
	return "the purchase path reported no hull and no error"
}
