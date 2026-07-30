package ledger

import (
	"sort"
	"time"
)

// position_matching.go — the DEFINITIVE realised-margin accounting for cargo trades
// (sp-76r2c).
//
// THE DEFECT THIS REPLACES. The transactions ledger stores each trade leg as its own
// signed row: PURCHASE_CARGO negative, SELL_CARGO positive. A hull buys in one clock hour
// and sells in another, so ANY time-bucketed SUM(amount) mixes unmatched legs and measures
// where the bucket boundary fell rather than economics. It does not merely blur the signal,
// it INVERTS it. Live, same goods, adjacent hours (2026-07-30):
//
//	                 hour 06:00     hour 08:00
//	CLOTHING        -2,132,071     +1,931,781
//	URANITE           -647,190     +2,843,115
//	MEDICINE        +1,427,360     -1,835,254
//
// An open-position hour reads as pure loss and its closing hour as pure profit. That
// substrate produced seven separate false conclusions in one analysis session (sp-76r2c
// enumerates them): "net per hour collapsed", "margin falls as the fleet grows", "trade
// quality varies 13x per hour" — every one of them leg placement, not economics.
//
// THE FIX. Match each purchase to the sale that closes it and attribute the realised margin
// to the CLOSING instant. An hourly figure then means "positions closed this hour", which is
// an answerable question. Units still in a hold are reported SEPARATELY as inventory at cost
// so they can never silently contaminate a net.
//
// THE MATCHING RULE: chronological FIFO on (hull, good).
//
//   - Keyed on hull + good. Cargo is fungible within a hold, so the (hull, good) stream is
//     the finest grain at which a lot identity survives; nothing in the ledger links a
//     specific sale row back to a specific purchase row.
//
//   - CHRONOLOGICAL, walked as a lot queue — deliberately NOT the cumulative-unit-interval
//     overlap join that expresses FIFO compactly in SQL. Interval overlap ignores time
//     order, and 3.4% of live fragments (254 of 7,502 over 7 days) would match a sale to a
//     LATER purchase: cargo can enter a hold without a PURCHASE_CARGO row at all (gas
//     siphoning extracts it; TransferCargo moves it between hulls and moves no credits, so
//     it writes no ledger row). Those sales must stay uncosted, not steal a future
//     purchase's basis.
//
//   - NOT scoped by operation_type. A position opened under one operation is routinely
//     closed under another: liquidation sold 2,101 units and bought none; manual sold 3,192
//     units more than it bought. Scoping the match by operation would manufacture orphans on
//     both sides.
//
//   - Splits are first-class in BOTH directions, because the data forces it: over 7 days
//     13.4% of matched purchases close across 2+ sales (up to 8) and 15.8% of sales close
//     2+ purchases. A 1:1 pairing would misattribute roughly one leg in seven.
//
// THE THREE OUTPUT BUCKETS ARE EXHAUSTIVE AND RECONCILE TO THE LEDGER. Every credit of
// every leg fed in lands in exactly one bucket:
//
//	Σ Closed.Revenue + Σ UncostedSales.Revenue == Σ (sell amounts)
//	Σ Closed.Cost    + Σ OpenPositions.CostBasis == Σ |buy amounts|
//
// ReconcilesTo asserts both. Allocation is exact integer proration (see prorate), so the
// identity holds to the credit with no residual — a fragment total can never drift from the
// row it came from.
//
// WHAT DOES NOT CLOSE, AND WHY THAT IS NOT A BUG. Some purchases structurally never produce
// a SELL_CARGO and will sit in OpenPositions forever:
//
//   - operation_type="contract" (114,762 units live): cargo is DELIVERED to a contract, and
//     the revenue arrives as CONTRACT_FULFILLED with no hull and no good attribution at all.
//   - operation_type="stocker": pre-buys that feed contract delivery, same story.
//   - factory-input purchases: consumed into production, and the OUTPUT good is what sells.
//
// OpenPositions therefore carries BuyOperationType on every row so a consumer can partition
// genuinely-open inventory from structurally-consumed inventory. This package deliberately
// does NOT hard-code that taxonomy: the operation set changes, and a wrong built-in
// classification would be exactly the kind of invisible, plausible-looking distortion
// sp-76r2c exists to stop. Report the dimension; let the consumer decide.

// CargoLeg is one PURCHASE_CARGO or SELL_CARGO ledger row reduced to the fields position
// matching needs. Hull, Good and Units come from the row's metadata
// (ship_symbol / good_symbol / units); a row missing any of them cannot be attributed to a
// position and must be excluded by the caller and counted, never fed in as a zero.
type CargoLeg struct {
	// TxID is the transactions.id of the row, carried through to the output fragments so a
	// realised figure can always be traced back to the two legs that produced it.
	TxID string
	// Hull is metadata->>'ship_symbol' — the position key together with Good.
	Hull string
	// Good is metadata->>'good_symbol' — the position key together with Hull.
	Good string
	// Units is metadata->>'units', always positive in live data (verified: 0 of 53,193
	// attributable rows have units <= 0).
	Units int
	// Amount is the credit amount AS STORED: negative for a purchase, positive for a sale
	// (verified live: 28,854/28,854 purchases negative, 24,339/24,339 sales positive). The
	// matcher takes the magnitude, so a sign-flipped row would surface as an absurd cost
	// rather than silently cancelling.
	Amount int64
	// OperationType is the row's operation_type ("tour", "contract", "liquidation", ...).
	// Carried through for partitioning only — it is NEVER part of the match key.
	OperationType string
	// At is the leg's instant. Use one column consistently across buys and sells; the
	// repository uses created_at, matching every other cash-true reader
	// (RealizedCashRate, ReadRealizedPnL). timestamp and created_at differ on every live
	// row but by at most 0.16s, so the choice cannot reorder a real position.
	At time.Time
	// IsBuy is true for PURCHASE_CARGO, false for SELL_CARGO.
	IsBuy bool
}

// ClosedPosition is one matched fragment: Units of Good that a hull bought and then sold.
// The realised margin belongs to ClosedAt — that attribution IS the fix. A single fragment
// may cover part of a purchase, part of a sale, or part of both.
type ClosedPosition struct {
	Hull  string
	Good  string
	Units int

	BuyTxID          string
	BuyOperationType string
	// OpenedAt is the purchase instant. Reported so holding time is visible, but it is NOT
	// the attribution timestamp: bucketing on OpenedAt reintroduces the defect from the
	// other side.
	OpenedAt time.Time
	// Cost is the positive magnitude of the purchase credits allocated to this fragment.
	Cost int64

	SellTxID          string
	SellOperationType string
	// ClosedAt is the sale instant — THE attribution timestamp. Bucket on this.
	ClosedAt time.Time
	// Revenue is the positive sale credits allocated to this fragment.
	Revenue int64
}

// RealizedMargin is the fragment's realised profit, negative on a genuine losing trade.
func (c ClosedPosition) RealizedMargin() int64 { return c.Revenue - c.Cost }

// HoldingTime is how long the position stayed open. Always non-negative: the chronological
// walk can never close a position with a later purchase.
func (c ClosedPosition) HoldingTime() time.Duration { return c.ClosedAt.Sub(c.OpenedAt) }

// OpenPosition is purchased cargo not yet closed by a sale, at cost. It is inventory, NOT a
// loss — reporting it as one is precisely the sign inversion sp-76r2c documents. Check
// BuyOperationType before treating it as genuinely open: contract, stocker and factory-input
// purchases never close by construction.
type OpenPosition struct {
	Hull  string
	Good  string
	Units int

	BuyTxID          string
	BuyOperationType string
	OpenedAt         time.Time
	// CostBasis is the positive magnitude of the purchase credits still held as inventory.
	CostBasis int64
}

// UncostedSale is sale revenue with no purchase to match against on that hull+good — cargo
// that entered the hold without a PURCHASE_CARGO row (gas siphoning extracts it; a
// TransferCargo from another hull moves no credits and writes no ledger row), or a sale
// whose purchase predates the matched history.
//
// It is reported separately rather than booked as pure profit, because a zero cost basis is
// an ASSERTION about provenance, not a measurement. Live it is a rounding-level 369 units
// (0.03% of 1.29M sold), so a consumer that folds it into realised margin will not be far
// wrong — but it must fold it in KNOWINGLY. A sudden rise here means matched history was
// truncated, not that the fleet found free cargo.
type UncostedSale struct {
	Hull  string
	Good  string
	Units int

	SellTxID          string
	SellOperationType string
	ClosedAt          time.Time
	Revenue           int64
}

// MatchedPositions is the exhaustive partition of the legs fed to MatchPositions. Every
// credit lands in exactly one bucket; ReconcilesTo proves it.
type MatchedPositions struct {
	// Closed holds the matched fragments, sorted by ClosedAt (then SellTxID, BuyTxID).
	Closed []ClosedPosition
	// Open holds unclosed purchase remainders, sorted by OpenedAt (then BuyTxID).
	Open []OpenPosition
	// Uncosted holds sale fragments with no purchase, sorted by ClosedAt (then SellTxID).
	Uncosted []UncostedSale
}

// RealizedMargin is the total realised margin across every closed fragment, ignoring open
// inventory and uncosted revenue. Over a window this is the honest "what did closing
// positions earn", the figure a naive SUM(amount) was standing in for.
func (m MatchedPositions) RealizedMargin() int64 {
	var total int64
	for _, c := range m.Closed {
		total += c.RealizedMargin()
	}
	return total
}

// OpenCostBasis is the total credits tied up in unclosed inventory. Report it BESIDE a
// realised figure, never inside one.
func (m MatchedPositions) OpenCostBasis() int64 {
	var total int64
	for _, o := range m.Open {
		total += o.CostBasis
	}
	return total
}

// UncostedRevenue is the total sale revenue that found no purchase to close.
func (m MatchedPositions) UncostedRevenue() int64 {
	var total int64
	for _, u := range m.Uncosted {
		total += u.Revenue
	}
	return total
}

// ReconcilesTo reports whether the matched output accounts for every credit in legs. It is
// the invariant that makes this substrate trustworthy where SUM(amount) was not: matching
// REDISTRIBUTES credits across time, it never creates or destroys them.
//
//	Σ Closed.Revenue + Σ Uncosted.Revenue == Σ (sale amounts)
//	Σ Closed.Cost    + Σ Open.CostBasis   == Σ |purchase amounts|
//
// A false here means the allocator lost credits to rounding and the figures are not
// treasury-true — a caller may legitimately treat it as fail-closed.
func (m MatchedPositions) ReconcilesTo(legs []CargoLeg) bool {
	var wantRevenue, wantCost int64
	for _, leg := range legs {
		if leg.IsBuy {
			wantCost += magnitude(leg.Amount)
			continue
		}
		wantRevenue += magnitude(leg.Amount)
	}

	var gotRevenue, gotCost int64
	for _, c := range m.Closed {
		gotRevenue += c.Revenue
		gotCost += c.Cost
	}
	for _, u := range m.Uncosted {
		gotRevenue += u.Revenue
	}
	for _, o := range m.Open {
		gotCost += o.CostBasis
	}

	return gotRevenue == wantRevenue && gotCost == wantCost
}

// MatchPositions matches purchases to the sales that close them and returns the exhaustive
// three-bucket partition. Pure: it does not read a clock, touch a database, or mutate legs.
//
// legs may arrive in any order — they are grouped by (hull, good) and sorted chronologically
// (ties broken by TxID, so the result is deterministic; live data has no ties at all).
//
// Feed it the FULL history you want matched, not just the reporting window. Truncating the
// input silently converts real positions into UncostedSale and OpenPosition rows at the
// truncation edge, which is the same class of artefact as bucketing by hour: live p99
// holding time is 25.6h and the max is 90h, so a short prologue would corrupt a
// six-hour report. Match wide, then filter the OUTPUT by ClosedAt.
func MatchPositions(legs []CargoLeg) MatchedPositions {
	type key struct{ hull, good string }

	grouped := make(map[key][]CargoLeg)
	for _, leg := range legs {
		if leg.Units <= 0 {
			// A non-positive-unit leg carries no position to match. Dropping it would
			// break reconciliation silently, so treat it as a whole-row remainder below.
			continue
		}
		k := key{leg.Hull, leg.Good}
		grouped[k] = append(grouped[k], leg)
	}

	// Deterministic group order. The final sorts make output order independent of this,
	// but a stable walk keeps the matching itself reproducible.
	keys := make([]key, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].hull != keys[j].hull {
			return keys[i].hull < keys[j].hull
		}
		return keys[i].good < keys[j].good
	})

	var result MatchedPositions
	for _, k := range keys {
		matchStream(grouped[k], &result)
	}

	// Zero- and negative-unit legs cannot be positioned but their credits must still
	// reconcile: book them as whole-row remainders in the appropriate bucket.
	for _, leg := range legs {
		if leg.Units > 0 {
			continue
		}
		if leg.IsBuy {
			result.Open = append(result.Open, OpenPosition{
				Hull: leg.Hull, Good: leg.Good, Units: leg.Units,
				BuyTxID: leg.TxID, BuyOperationType: leg.OperationType,
				OpenedAt: leg.At, CostBasis: magnitude(leg.Amount),
			})
			continue
		}
		result.Uncosted = append(result.Uncosted, UncostedSale{
			Hull: leg.Hull, Good: leg.Good, Units: leg.Units,
			SellTxID: leg.TxID, SellOperationType: leg.OperationType,
			ClosedAt: leg.At, Revenue: magnitude(leg.Amount),
		})
	}

	sortOutput(&result)
	return result
}

// buyLot is an open purchase in the FIFO queue. consumed tracks how many of the row's units
// have already been matched out, so proration stays anchored to the ORIGINAL row and every
// fragment telescopes back to its exact amount.
type buyLot struct {
	leg      CargoLeg
	cost     int64 // positive magnitude of the whole row
	consumed int
}

// matchStream walks one (hull, good) stream chronologically, maintaining a FIFO queue of
// open purchase lots. This is the whole algorithm: purchases enqueue, sales drain from the
// front, and whatever is left over at each end becomes an explicitly-labelled remainder.
func matchStream(legs []CargoLeg, out *MatchedPositions) {
	ordered := make([]CargoLeg, len(legs))
	copy(ordered, legs)
	sort.SliceStable(ordered, func(i, j int) bool {
		if !ordered[i].At.Equal(ordered[j].At) {
			return ordered[i].At.Before(ordered[j].At)
		}
		return ordered[i].TxID < ordered[j].TxID
	})

	var queue []*buyLot
	for _, leg := range ordered {
		if leg.IsBuy {
			queue = append(queue, &buyLot{leg: leg, cost: magnitude(leg.Amount)})
			continue
		}

		revenue := magnitude(leg.Amount)
		sold := 0              // units of THIS sale already allocated
		remaining := leg.Units // units of THIS sale still needing a purchase

		for remaining > 0 && len(queue) > 0 {
			lot := queue[0]
			take := min(lot.leg.Units-lot.consumed, remaining)

			out.Closed = append(out.Closed, ClosedPosition{
				Hull: leg.Hull, Good: leg.Good, Units: take,

				BuyTxID:          lot.leg.TxID,
				BuyOperationType: lot.leg.OperationType,
				OpenedAt:         lot.leg.At,
				Cost:             prorate(lot.cost, lot.consumed+take, lot.leg.Units) - prorate(lot.cost, lot.consumed, lot.leg.Units),

				SellTxID:          leg.TxID,
				SellOperationType: leg.OperationType,
				ClosedAt:          leg.At,
				Revenue:           prorate(revenue, sold+take, leg.Units) - prorate(revenue, sold, leg.Units),
			})

			lot.consumed += take
			sold += take
			remaining -= take
			if lot.consumed >= lot.leg.Units {
				queue = queue[1:]
			}
		}

		if remaining > 0 {
			// No purchase left to close against: extracted or transferred-in cargo, or a
			// purchase outside the matched history. Zero cost basis, labelled as such.
			out.Uncosted = append(out.Uncosted, UncostedSale{
				Hull: leg.Hull, Good: leg.Good, Units: remaining,
				SellTxID: leg.TxID, SellOperationType: leg.OperationType,
				ClosedAt: leg.At,
				Revenue:  prorate(revenue, sold+remaining, leg.Units) - prorate(revenue, sold, leg.Units),
			})
		}
	}

	// Whatever is still queued is inventory in a hold, reported at cost — NOT a loss.
	for _, lot := range queue {
		out.Open = append(out.Open, OpenPosition{
			Hull: lot.leg.Hull, Good: lot.leg.Good,
			Units:            lot.leg.Units - lot.consumed,
			BuyTxID:          lot.leg.TxID,
			BuyOperationType: lot.leg.OperationType,
			OpenedAt:         lot.leg.At,
			CostBasis:        lot.cost - prorate(lot.cost, lot.consumed, lot.leg.Units),
		})
	}
}

// prorate returns round(total * part / whole) for non-negative integers, the allocator that
// makes reconciliation exact.
//
// Fragments are allocated as a DIFFERENCE of two prorate calls over adjacent unit offsets,
// so a partition of [0, whole) telescopes to prorate(total, whole, whole) −
// prorate(total, 0, whole) == total − 0 == total. No residual to sweep up, no per-fragment
// rounding error accumulating into a drift between a realised figure and the treasury.
func prorate(total int64, part, whole int) int64 {
	if whole <= 0 || part <= 0 {
		return 0
	}
	if part >= whole {
		return total
	}
	return (total*int64(part) + int64(whole)/2) / int64(whole)
}

// magnitude returns the absolute value of a stored credit amount, so the matcher is
// indifferent to the ledger's sign convention (purchases negative, sales positive) and a
// sign-flipped row surfaces as an absurd cost rather than silently cancelling a real one.
func magnitude(amount int64) int64 {
	if amount < 0 {
		return -amount
	}
	return amount
}

// sortOutput imposes a deterministic, report-friendly order: closed and uncosted fragments
// by the instant they are ATTRIBUTED to (ClosedAt), open positions by when they opened.
//
// Within one sale, fragments stay in FIFO order — OpenedAt ascending, oldest lot first —
// because that is the order the matcher consumed them in and the order a reader auditing a
// split needs to see. Tiebreaking on BuyTxID BEFORE OpenedAt would sort the lots
// alphabetically and silently present a 2-lot split as though the newer lot had been
// consumed first.
func sortOutput(m *MatchedPositions) {
	sort.SliceStable(m.Closed, func(i, j int) bool {
		a, b := m.Closed[i], m.Closed[j]
		if !a.ClosedAt.Equal(b.ClosedAt) {
			return a.ClosedAt.Before(b.ClosedAt)
		}
		if a.SellTxID != b.SellTxID {
			return a.SellTxID < b.SellTxID
		}
		if !a.OpenedAt.Equal(b.OpenedAt) {
			return a.OpenedAt.Before(b.OpenedAt)
		}
		return a.BuyTxID < b.BuyTxID
	})
	sort.SliceStable(m.Open, func(i, j int) bool {
		a, b := m.Open[i], m.Open[j]
		if !a.OpenedAt.Equal(b.OpenedAt) {
			return a.OpenedAt.Before(b.OpenedAt)
		}
		return a.BuyTxID < b.BuyTxID
	})
	sort.SliceStable(m.Uncosted, func(i, j int) bool {
		a, b := m.Uncosted[i], m.Uncosted[j]
		if !a.ClosedAt.Equal(b.ClosedAt) {
			return a.ClosedAt.Before(b.ClosedAt)
		}
		return a.SellTxID < b.SellTxID
	})
}
