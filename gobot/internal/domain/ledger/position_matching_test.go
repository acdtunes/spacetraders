package ledger_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
)

// position_matching_test.go — sp-76r2c.
//
// The headline test is TestBucketRealized_HourStraddlingTradeReportsInTheClosingHourOnly:
// it states the defect AS A TEST. One trade, bought in hour N and sold in hour N+1. Under
// the naive time-bucketed leg sum that shipped before this package, hour N read as a pure
// loss and hour N+1 as pure profit, and nothing in either number hinted that they were two
// halves of one profitable trade. That reading produced seven false conclusions in a single
// analysis session.
//
// The rest of the file pins the matching rule the LIVE DATA forced, not the rule that looked
// tidiest: splits in both directions, sales that precede any purchase, positions that close
// under a different operation_type than they opened under, and exact credit reconciliation.

// hour builds a UTC instant inside a named hour of 2026-07-30 — the day of the sp-76r2c
// demonstration.
func hour(h, m int) time.Time {
	return time.Date(2026, 7, 30, h, m, 0, 0, time.UTC)
}

func buy(id, hull, good string, units int, amount int64, at time.Time, op string) ledger.CargoLeg {
	return ledger.CargoLeg{
		TxID: id, Hull: hull, Good: good, Units: units,
		Amount:        -amount, // stored convention: purchases negative
		OperationType: op, At: at, IsBuy: true,
	}
}

func sell(id, hull, good string, units int, amount int64, at time.Time, op string) ledger.CargoLeg {
	return ledger.CargoLeg{
		TxID: id, Hull: hull, Good: good, Units: units,
		Amount:        amount, // stored convention: sales positive
		OperationType: op, At: at, IsBuy: false,
	}
}

// bucketAt finds the bucket starting at the given instant, or reports absence.
func bucketAt(buckets []ledger.RealizedBucket, start time.Time) (ledger.RealizedBucket, bool) {
	for _, b := range buckets {
		if b.Start.Equal(start) {
			return b, true
		}
	}
	return ledger.RealizedBucket{}, false
}

func naiveAt(buckets []ledger.NaiveBucket, start time.Time) (ledger.NaiveBucket, bool) {
	for _, b := range buckets {
		if b.Start.Equal(start) {
			return b, true
		}
	}
	return ledger.NaiveBucket{}, false
}

// THE REGRESSION TEST (sp-76r2c acceptance criterion).
//
// One CLOTHING trade: 100 units bought at 06:30 for 1,000,000 and sold at 07:15 for
// 1,200,000. Realised margin +200,000, and it belongs ENTIRELY to hour 07:00 — the hour the
// position closed.
//
// The naive leg sum reports hour 06 = −1,000,000 and hour 07 = +1,200,000. Both figures are
// wrong about economics and neither is even the right SIGN for what happened: the fleet did
// not lose 1.0M in hour 06, and it did not earn 1.2M in hour 07. This test asserts BOTH
// readings in one place so the contrast can never quietly disappear.
func TestBucketRealized_HourStraddlingTradeReportsInTheClosingHourOnly(t *testing.T) {
	legs := []ledger.CargoLeg{
		buy("tx-buy", "TORWIND-27A", "CLOTHING", 100, 1_000_000, hour(6, 30), "tour"),
		sell("tx-sell", "TORWIND-27A", "CLOTHING", 100, 1_200_000, hour(7, 15), "tour"),
	}

	matched := ledger.MatchPositions(legs)

	// The trade is one closed position, attributed to the SELL instant.
	require.Len(t, matched.Closed, 1, "one buy fully closed by one sell is exactly one fragment")
	require.Empty(t, matched.Open, "nothing is left in the hold")
	require.Empty(t, matched.Uncosted, "the sale had a purchase to close")
	assert.Equal(t, hour(7, 15), matched.Closed[0].ClosedAt, "attribution instant is the sale")
	assert.Equal(t, int64(200_000), matched.RealizedMargin())

	buckets := ledger.BucketRealized(matched.Closed, time.Hour)

	// HOUR N (06:00) — the hour the cargo was BOUGHT. It must report NOTHING realised.
	// Before sp-76r2c this hour reported −1,000,000, which read as a catastrophic loss and
	// was in fact a hull holding profitable cargo.
	_, present := bucketAt(buckets, hour(6, 0))
	assert.False(t, present,
		"hour 06:00 closed no position, so it must emit no realised bucket at all; "+
			"a bucket here means cost is being attributed to the PURCHASE hour (the sp-76r2c defect)")

	// HOUR N+1 (07:00) — the hour the position CLOSED. It carries the whole trade: the
	// revenue earned here AND the cost incurred in the previous hour.
	closing, present := bucketAt(buckets, hour(7, 0))
	require.True(t, present, "hour 07:00 closed a position and must emit a realised bucket")
	assert.Equal(t, 1, closing.Closes)
	assert.Equal(t, 100, closing.Units)
	assert.Equal(t, int64(1_000_000), closing.Cost,
		"the closing hour must carry the cost from the PREVIOUS hour's purchase; "+
			"a zero cost here means the purchase leg was left stranded in hour 06")
	assert.Equal(t, int64(1_200_000), closing.Revenue)
	assert.Equal(t, int64(200_000), closing.RealizedMargin(),
		"realised margin is +200,000 — not the +1,200,000 the naive sum reported for this hour")

	// Exactly one bucket exists across the whole series: one trade closed once.
	require.Len(t, buckets, 1, "a single trade must not be reported in two different hours")

	// AND the defect, pinned as a fact. If this half ever stops holding, the naive reading
	// has been changed and the comparison above no longer proves anything.
	naive := ledger.BucketNaiveLegs(legs, time.Hour)
	require.Len(t, naive, 2, "the naive reading splits one trade across two hours — that is the bug")

	naiveBuyHour, present := naiveAt(naive, hour(6, 0))
	require.True(t, present)
	assert.Equal(t, int64(-1_000_000), naiveBuyHour.NetCredits,
		"the naive reading calls the purchase hour a 1.0M loss")

	naiveSellHour, present := naiveAt(naive, hour(7, 0))
	require.True(t, present)
	assert.Equal(t, int64(1_200_000), naiveSellHour.NetCredits,
		"the naive reading calls the sale hour a 1.2M profit")

	// The two readings agree on the TOTAL and disagree on its placement in time. That is
	// precisely the failure mode: no aggregate check could ever have caught it.
	assert.Equal(t,
		naiveBuyHour.NetCredits+naiveSellHour.NetCredits,
		matched.RealizedMargin(),
		"naive and matched must agree in total — the defect is placement, not magnitude, "+
			"which is why it survived every reconciliation")
}

// The bead's live demonstration, reproduced as a fixture: adjacent hours where the naive
// reading INVERTS the sign of the same good. Two hulls each run one profitable trade, one
// straddling 06→08 and one straddling 08→09. Naive reports hour 06 deeply negative and hour
// 08 wildly positive; matched reports two ordinary profitable trades.
func TestBucketRealized_AdjacentHourSignInversionIsRemoved(t *testing.T) {
	legs := []ledger.CargoLeg{
		// URANITE: bought in hour 06, sold in hour 08. Naive: 06 = −647,190, 08 = +900,000.
		buy("u-buy", "TORWIND-27A", "URANITE", 180, 647_190, hour(6, 20), "tour"),
		sell("u-sell", "TORWIND-27A", "URANITE", 180, 900_000, hour(8, 5), "tour"),
		// MEDICINE: bought in hour 08, sold in hour 09. Naive: 08 = −450_400 (dragging the
		// URANITE sale down), 09 = +493,000.
		buy("m-buy", "TORWIND-2F1", "MEDICINE", 100, 450_400, hour(8, 40), "tour"),
		sell("m-sell", "TORWIND-2F1", "MEDICINE", 100, 493_000, hour(9, 10), "tour"),
	}

	naive := ledger.BucketNaiveLegs(legs, time.Hour)
	naive06, _ := naiveAt(naive, hour(6, 0))
	naive08, _ := naiveAt(naive, hour(8, 0))
	assert.Negative(t, naive06.NetCredits, "naive hour 06 is a pure-purchase hour: reads as loss")
	assert.Equal(t, int64(-647_190), naive06.NetCredits)
	assert.Equal(t, int64(900_000-450_400), naive08.NetCredits,
		"naive hour 08 mixes one hull's sale with another hull's purchase")

	matched := ledger.MatchPositions(legs)
	require.Len(t, matched.Closed, 2)
	require.Empty(t, matched.Open)
	require.Empty(t, matched.Uncosted)

	buckets := ledger.BucketRealized(matched.Closed, time.Hour)
	require.Len(t, buckets, 2, "two trades closed in two distinct hours")

	_, present := bucketAt(buckets, hour(6, 0))
	assert.False(t, present, "hour 06 closed nothing")

	uranite, present := bucketAt(buckets, hour(8, 0))
	require.True(t, present)
	assert.Equal(t, int64(900_000-647_190), uranite.RealizedMargin(),
		"hour 08 realised the URANITE trade only — the MEDICINE purchase in the same hour "+
			"is inventory, not a loss")
	ratio, ok := uranite.MarginRatio()
	require.True(t, ok)
	assert.InDelta(t, 0.39, ratio, 0.01, "a plain ~39% markup, not the 13x swing the naive reading showed")

	medicine, present := bucketAt(buckets, hour(9, 0))
	require.True(t, present)
	assert.Equal(t, int64(493_000-450_400), medicine.RealizedMargin())

	// Every realised bucket is positive: both trades were profitable, and no hour reports a
	// loss the fleet did not take.
	for _, b := range buckets {
		assert.Positive(t, b.RealizedMargin(), "bucket %s must not invent a loss", b.Start)
	}
}

// ORPHAN CASE 1 (from the brief): an unsold purchase is inventory at cost. It must appear in
// OpenPositions and must NOT appear anywhere in realised margin.
func TestMatchPositions_UnsoldBuyIsInventoryAtCostNotRealizedLoss(t *testing.T) {
	legs := []ledger.CargoLeg{
		buy("tx-open", "TORWIND-27A", "FOOD", 225, 411_525, hour(6, 30), "tour"),
	}

	matched := ledger.MatchPositions(legs)

	require.Empty(t, matched.Closed, "an unsold purchase has realised nothing")
	require.Empty(t, matched.Uncosted)
	require.Len(t, matched.Open, 1)

	open := matched.Open[0]
	assert.Equal(t, "tx-open", open.BuyTxID)
	assert.Equal(t, 225, open.Units)
	assert.Equal(t, int64(411_525), open.CostBasis, "cost basis is positive: it is inventory VALUE")
	assert.Equal(t, "tour", open.BuyOperationType)
	assert.Equal(t, int64(411_525), matched.OpenCostBasis())

	assert.Zero(t, matched.RealizedMargin(), "no position closed, so realised margin is zero")
	assert.Empty(t, ledger.BucketRealized(matched.Closed, time.Hour),
		"an open position must not produce a realised bucket in ANY hour — reporting it as "+
			"a −411,525 hour is exactly the sp-76r2c inversion")
}

// ORPHAN CASE 2: a structurally non-closing purchase (contract delivery) still reports as
// open inventory, carrying its operation_type so a consumer can partition it out. Contract
// cargo is DELIVERED, and the revenue arrives as CONTRACT_FULFILLED with no hull attribution,
// so these purchases never close — 114,762 units of live data sit in this class.
func TestMatchPositions_ContractPurchaseStaysOpenAndNamesItsOperation(t *testing.T) {
	legs := []ledger.CargoLeg{
		buy("tx-contract", "TORWIND-11", "DIAMONDS", 77, 7_161, hour(2, 0), "contract"),
		buy("tx-stocker", "TORWIND-11", "ALUMINUM", 15, 30_000, hour(2, 5), "stocker"),
	}

	matched := ledger.MatchPositions(legs)

	require.Len(t, matched.Open, 2)
	require.Empty(t, matched.Closed)
	ops := []string{matched.Open[0].BuyOperationType, matched.Open[1].BuyOperationType}
	assert.ElementsMatch(t, []string{"contract", "stocker"}, ops,
		"open positions must name their operation so a consumer can separate cargo that "+
			"never closes by design from cargo genuinely still in flight")
	assert.Equal(t, int64(37_161), matched.OpenCostBasis())
	assert.Zero(t, matched.RealizedMargin())
}

// ORPHAN CASE 3: a sale with no purchase on that hull+good. Live this is cargo that entered
// the hold without a PURCHASE_CARGO row — gas siphoning extracts it, and TransferCargo moves
// it between hulls while moving no credits, so no ledger row exists. 369 live units.
//
// It must be reported as UncostedSale, NOT folded into realised margin, because a zero cost
// basis is an assertion about provenance rather than a measurement.
func TestMatchPositions_SaleWithNoPurchaseIsUncostedNotRealizedProfit(t *testing.T) {
	legs := []ledger.CargoLeg{
		sell("tx-siphon", "TORWIND-14", "DIAMONDS", 53, 6_201, hour(9, 53), "tour"),
	}

	matched := ledger.MatchPositions(legs)

	require.Empty(t, matched.Closed)
	require.Empty(t, matched.Open)
	require.Len(t, matched.Uncosted, 1)
	assert.Equal(t, int64(6_201), matched.Uncosted[0].Revenue)
	assert.Equal(t, 53, matched.Uncosted[0].Units)
	assert.Equal(t, int64(6_201), matched.UncostedRevenue())

	assert.Zero(t, matched.RealizedMargin(),
		"uncosted revenue must not enter realised margin implicitly; a consumer that wants "+
			"it must add UncostedRevenue deliberately")
	assert.Empty(t, ledger.BucketRealized(matched.Closed, time.Hour))
}

// A sale that PRECEDES every purchase on its hull+good must stay uncosted and must not steal
// the basis of a LATER purchase.
//
// This is why the matcher walks chronologically instead of using the cumulative-unit-interval
// overlap join that expresses FIFO compactly in SQL: interval overlap ignores time order, and
// 254 of 7,502 live fragments (3.4%) over 7 days would match a sale to a future purchase.
func TestMatchPositions_SaleBeforeAnyPurchaseNeverStealsALaterBuysBasis(t *testing.T) {
	legs := []ledger.CargoLeg{
		// Transferred-in cargo sold at 07:00, with nothing bought yet.
		sell("tx-early-sell", "TORWIND-14", "SILVER", 16, 5_408, hour(7, 0), "tour"),
		// A genuine purchase an hour LATER, still unsold.
		buy("tx-later-buy", "TORWIND-14", "SILVER", 16, 4_000, hour(8, 0), "tour"),
	}

	matched := ledger.MatchPositions(legs)

	require.Empty(t, matched.Closed,
		"a sale can never be closed by a purchase that had not happened yet; a fragment "+
			"here means the matcher is using interval overlap rather than chronological order")
	require.Len(t, matched.Uncosted, 1, "the early sale stays uncosted")
	assert.Equal(t, int64(5_408), matched.Uncosted[0].Revenue)
	require.Len(t, matched.Open, 1, "the later purchase is still inventory")
	assert.Equal(t, int64(4_000), matched.Open[0].CostBasis)

	// If a naive interval-overlap matcher were used it would report a fabricated
	// +1,408 realised margin here, in the hour BEFORE the cargo was bought.
	assert.Zero(t, matched.RealizedMargin())
}

// A single purchase closed across several sales. Live: 13.4% of matched purchases over 7 days
// close across 2+ sales, up to 8. Each fragment carries its own share of the cost, and the
// shares sum back to the purchase exactly.
func TestMatchPositions_OneBuyClosedAcrossSeveralSells(t *testing.T) {
	legs := []ledger.CargoLeg{
		buy("b1", "TORWIND-2F1", "FOOD", 105, 210_000, hour(6, 0), "tour"),
		sell("s1", "TORWIND-2F1", "FOOD", 45, 108_000, hour(7, 0), "tour"),
		sell("s2", "TORWIND-2F1", "FOOD", 60, 150_000, hour(9, 0), "tour"),
	}

	matched := ledger.MatchPositions(legs)

	require.Len(t, matched.Closed, 2, "one purchase split across two sales is two fragments")
	require.Empty(t, matched.Open)
	require.Empty(t, matched.Uncosted)

	first, second := matched.Closed[0], matched.Closed[1]
	assert.Equal(t, "s1", first.SellTxID)
	assert.Equal(t, 45, first.Units)
	assert.Equal(t, int64(90_000), first.Cost, "45/105 of 210,000")
	assert.Equal(t, int64(108_000), first.Revenue)
	assert.Equal(t, hour(7, 0), first.ClosedAt)

	assert.Equal(t, "s2", second.SellTxID)
	assert.Equal(t, 60, second.Units)
	assert.Equal(t, int64(120_000), second.Cost, "60/105 of 210,000")
	assert.Equal(t, int64(150_000), second.Revenue)

	assert.Equal(t, int64(210_000), first.Cost+second.Cost,
		"the split cost must sum back to the purchase exactly")
	assert.Equal(t, int64(48_000), matched.RealizedMargin())

	// The two fragments land in the two hours they CLOSED in, not the one they were bought in.
	buckets := ledger.BucketRealized(matched.Closed, time.Hour)
	require.Len(t, buckets, 2)
	assert.Equal(t, hour(7, 0), buckets[0].Start)
	assert.Equal(t, int64(18_000), buckets[0].RealizedMargin())
	assert.Equal(t, hour(9, 0), buckets[1].Start)
	assert.Equal(t, int64(30_000), buckets[1].RealizedMargin())
	_, present := bucketAt(buckets, hour(6, 0))
	assert.False(t, present, "the purchase hour realised nothing")
}

// Several purchases closed by one sale, FIFO: the OLDEST lot is consumed first. Live: 15.8%
// of sales over 7 days close 2+ purchases.
func TestMatchPositions_SeveralBuysClosedByOneSellOldestLotFirst(t *testing.T) {
	legs := []ledger.CargoLeg{
		buy("b-old", "TORWIND-A", "EQUIPMENT", 40, 100_000, hour(5, 0), "tour"),
		buy("b-new", "TORWIND-A", "EQUIPMENT", 60, 180_000, hour(6, 0), "arb_run"),
		sell("s-all", "TORWIND-A", "EQUIPMENT", 100, 400_000, hour(8, 0), "tour"),
	}

	matched := ledger.MatchPositions(legs)

	require.Len(t, matched.Closed, 2)
	require.Empty(t, matched.Open)
	require.Empty(t, matched.Uncosted)

	assert.Equal(t, "b-old", matched.Closed[0].BuyTxID, "FIFO consumes the oldest lot first")
	assert.Equal(t, 40, matched.Closed[0].Units)
	assert.Equal(t, int64(100_000), matched.Closed[0].Cost)
	assert.Equal(t, int64(160_000), matched.Closed[0].Revenue, "40/100 of 400,000")
	assert.Equal(t, "tour", matched.Closed[0].BuyOperationType)

	assert.Equal(t, "b-new", matched.Closed[1].BuyTxID)
	assert.Equal(t, 60, matched.Closed[1].Units)
	assert.Equal(t, int64(180_000), matched.Closed[1].Cost)
	assert.Equal(t, int64(240_000), matched.Closed[1].Revenue)
	assert.Equal(t, "arb_run", matched.Closed[1].BuyOperationType,
		"a fragment records the operation that OPENED it, which may differ from the closer")

	assert.Equal(t, int64(120_000), matched.RealizedMargin())

	// Both fragments close in hour 08 — one bucket, carrying costs from hours 05 and 06.
	buckets := ledger.BucketRealized(matched.Closed, time.Hour)
	require.Len(t, buckets, 1)
	assert.Equal(t, hour(8, 0), buckets[0].Start)
	assert.Equal(t, int64(280_000), buckets[0].Cost, "costs from TWO earlier hours")
	assert.Equal(t, int64(400_000), buckets[0].Revenue)
}

// A partially-closed purchase splits into a realised fragment AND an open remainder, and the
// two shares sum back to the purchase exactly.
func TestMatchPositions_PartiallyClosedBuySplitsRealizedAndOpen(t *testing.T) {
	legs := []ledger.CargoLeg{
		buy("b1", "TORWIND-2F1", "FOOD", 105, 210_000, hour(6, 0), "tour"),
		sell("s1", "TORWIND-2F1", "FOOD", 45, 108_000, hour(7, 0), "tour"),
	}

	matched := ledger.MatchPositions(legs)

	require.Len(t, matched.Closed, 1)
	require.Len(t, matched.Open, 1)
	assert.Equal(t, int64(90_000), matched.Closed[0].Cost)
	assert.Equal(t, 60, matched.Open[0].Units)
	assert.Equal(t, int64(120_000), matched.Open[0].CostBasis)
	assert.Equal(t, int64(210_000), matched.Closed[0].Cost+matched.Open[0].CostBasis,
		"realised cost plus remaining basis must equal the purchase exactly")
	assert.Equal(t, int64(18_000), matched.RealizedMargin())
	assert.Equal(t, int64(120_000), matched.OpenCostBasis())
	require.True(t, matched.ReconcilesTo(legs))
}

// Matching is NOT scoped by operation_type: a position opened under one operation is
// routinely closed under another. Live, liquidation sold 2,101 units and bought none, and
// manual sold 3,192 units more than it bought — scoping by operation would manufacture a
// phantom orphan on each side.
func TestMatchPositions_DoesNotScopeByOperationType(t *testing.T) {
	legs := []ledger.CargoLeg{
		buy("b-tour", "TORWIND-A", "ANTIMATTER", 50, 500_000, hour(6, 0), "tour"),
		sell("s-liquidation", "TORWIND-A", "ANTIMATTER", 50, 560_000, hour(8, 0), "liquidation"),
	}

	matched := ledger.MatchPositions(legs)

	require.Len(t, matched.Closed, 1,
		"a liquidation sale closes a tour purchase; scoping the match by operation_type "+
			"would report a phantom open position AND a phantom uncosted sale")
	require.Empty(t, matched.Open)
	require.Empty(t, matched.Uncosted)
	assert.Equal(t, "tour", matched.Closed[0].BuyOperationType)
	assert.Equal(t, "liquidation", matched.Closed[0].SellOperationType)
	assert.Equal(t, int64(60_000), matched.RealizedMargin())
}

// Different hulls and different goods are different positions. Cargo is fungible only within
// one hold, and nothing in the ledger links a sale row to a specific purchase row.
func TestMatchPositions_CrossHullAndCrossGoodDoNotMatch(t *testing.T) {
	legs := []ledger.CargoLeg{
		buy("b-hullA", "TORWIND-A", "FOOD", 10, 10_000, hour(6, 0), "tour"),
		sell("s-hullB", "TORWIND-B", "FOOD", 10, 12_000, hour(7, 0), "tour"),
		buy("b-food", "TORWIND-C", "FOOD", 10, 10_000, hour(6, 0), "tour"),
		sell("s-iron", "TORWIND-C", "IRON", 10, 12_000, hour(7, 0), "tour"),
	}

	matched := ledger.MatchPositions(legs)

	assert.Empty(t, matched.Closed, "neither a cross-hull nor a cross-good pair is one position")
	assert.Len(t, matched.Open, 2)
	assert.Len(t, matched.Uncosted, 2)
	assert.Zero(t, matched.RealizedMargin())
	require.True(t, matched.ReconcilesTo(legs))
}

// Credit reconciliation: matching REDISTRIBUTES credits across time, it never creates or
// destroys them. This invariant is what makes the substrate trustworthy where a naive sum was
// not — and it is asserted against an awkward fixture whose amounts do not divide evenly by
// their unit counts, so any per-fragment rounding drift would show up.
func TestMatchPositions_ReconcilesEveryCreditToTheLedger(t *testing.T) {
	legs := []ledger.CargoLeg{
		buy("b1", "TORWIND-A", "CLOTHING", 7, 176_265, hour(6, 12), "tour"),
		sell("s1", "TORWIND-A", "CLOTHING", 3, 100_003, hour(7, 5), "tour"),
		sell("s2", "TORWIND-A", "CLOTHING", 3, 99_997, hour(8, 44), "tour"),
		buy("b2", "TORWIND-A", "CLOTHING", 11, 284_521, hour(9, 1), "arb_run"),
		sell("s3", "TORWIND-A", "CLOTHING", 13, 401_111, hour(10, 30), "liquidation"),
		sell("s4", "TORWIND-B", "URANITE", 5, 7_777, hour(6, 1), "tour"),      // uncosted
		buy("b3", "TORWIND-C", "MEDICINE", 3, 13_337, hour(6, 2), "contract"), // never closes
	}

	matched := ledger.MatchPositions(legs)

	require.True(t, matched.ReconcilesTo(legs),
		"every credit of every leg must land in exactly one bucket")

	// Restate the identity explicitly so a failure says WHICH side drifted.
	var wantRevenue, wantCost int64
	for _, leg := range legs {
		if leg.IsBuy {
			wantCost += -leg.Amount
			continue
		}
		wantRevenue += leg.Amount
	}
	var gotRevenue, gotCost int64
	for _, c := range matched.Closed {
		gotRevenue += c.Revenue
		gotCost += c.Cost
	}
	gotRevenue += matched.UncostedRevenue()
	gotCost += matched.OpenCostBasis()
	assert.Equal(t, wantRevenue, gotRevenue, "sale credits must be conserved exactly")
	assert.Equal(t, wantCost, gotCost, "purchase credits must be conserved exactly")

	// Every fragment has non-negative units and a forward-in-time holding period.
	for _, c := range matched.Closed {
		assert.Positive(t, c.Units)
		assert.GreaterOrEqual(t, c.HoldingTime(), time.Duration(0),
			"a position can never close before it opens")
	}

	// The naive total over the same legs equals realised + uncosted − open basis: the
	// readings agree on the aggregate and differ only in WHERE IN TIME the credits sit.
	naive := ledger.BucketNaiveLegs(legs, time.Hour)
	var naiveTotal int64
	for _, b := range naive {
		naiveTotal += b.NetCredits
	}
	assert.Equal(t,
		naiveTotal,
		matched.RealizedMargin()+matched.UncostedRevenue()-matched.OpenCostBasis(),
		"aggregate agreement is why the hourly defect was invisible")
}

// Proration: an amount indivisible by its unit count must be fully allocated (conservation)
// AND each fragment must carry its proportional unit share (attribution). Conservation alone is
// satisfied by allocators that would still wreck bucket attribution — see the comment on the
// proportionality assertion below.
func TestMatchPositions_ProrationTelescopesExactlyOnIndivisibleAmounts(t *testing.T) {
	cases := []struct {
		name         string
		units        int
		amount       int64
		sellSlices   []int
		sellUnitCost int64
	}{
		{"one credit across many units", 100, 1, []int{33, 33, 34}, 10},
		{"prime amount, prime slices", 97, 999_983, []int{7, 11, 79}, 5},
		{"single unit", 1, 7, []int{1}, 9},
		{"large amount, uneven thirds", 3, 1_000_000, []int{1, 1, 1}, 400_000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			legs := []ledger.CargoLeg{buy("b", "H", "G", tc.units, tc.amount, hour(6, 0), "tour")}
			at := hour(7, 0)
			for i, slice := range tc.sellSlices {
				legs = append(legs, sell(
					"s"+string(rune('a'+i)), "H", "G", slice,
					int64(slice)*tc.sellUnitCost, at.Add(time.Duration(i)*time.Minute), "tour"))
			}

			matched := ledger.MatchPositions(legs)
			require.True(t, matched.ReconcilesTo(legs),
				"allocation must conserve credits exactly even when nothing divides evenly")

			var allocated int64
			for _, c := range matched.Closed {
				allocated += c.Cost
			}
			allocated += matched.OpenCostBasis()
			assert.Equal(t, tc.amount, allocated,
				"the purchase's %d credits must be fully allocated with no residual", tc.amount)

			// Conservation alone is NOT enough, and this is the load-bearing half of the test.
			// Because prorate is applied as a DIFFERENCE of two cumulative calls, the total
			// telescopes to the row amount under ANY monotonic allocator — including a
			// pathological one that dumps a 225-unit purchase's entire cost onto a 1-unit
			// fragment. That would conserve every credit and still shift millions between
			// hourly buckets, which is the whole failure class sp-76r2c exists to prevent.
			// So pin PROPORTIONALITY: each fragment carries its unit share, ±1 credit of
			// integer rounding.
			require.Len(t, matched.Closed, len(tc.sellSlices))
			for i, c := range matched.Closed {
				share := float64(tc.amount) * float64(c.Units) / float64(tc.units)
				assert.InDelta(t, share, float64(c.Cost), 1.0,
					"fragment %d (%d/%d units) must carry its proportional share of the "+
						"purchase cost, not an arbitrary slice of it", i, c.Units, tc.units)
			}
		})
	}
}

// Legs may arrive in any order; the matcher sorts them. A reader that hands over
// DESC-ordered rows (the ledger's default) must get the same answer as one handing over ASC.
func TestMatchPositions_InputOrderDoesNotChangeTheResult(t *testing.T) {
	ascending := []ledger.CargoLeg{
		buy("b-old", "TORWIND-A", "EQUIPMENT", 40, 100_000, hour(5, 0), "tour"),
		buy("b-new", "TORWIND-A", "EQUIPMENT", 60, 180_000, hour(6, 0), "tour"),
		sell("s1", "TORWIND-A", "EQUIPMENT", 50, 200_000, hour(8, 0), "tour"),
		sell("s2", "TORWIND-A", "EQUIPMENT", 50, 210_000, hour(9, 0), "tour"),
	}
	descending := make([]ledger.CargoLeg, len(ascending))
	for i, leg := range ascending {
		descending[len(ascending)-1-i] = leg
	}

	forward := ledger.MatchPositions(ascending)
	reverse := ledger.MatchPositions(descending)

	assert.Equal(t, forward, reverse, "matching must be order-independent and deterministic")
	assert.Equal(t, int64(130_000), forward.RealizedMargin())
	require.Len(t, forward.Closed, 3, "the 60-unit lot spans both sales")
}

// Empty and degenerate inputs return empty results rather than panicking or fabricating rows.
func TestMatchPositions_EmptyAndDegenerateInputs(t *testing.T) {
	empty := ledger.MatchPositions(nil)
	assert.Empty(t, empty.Closed)
	assert.Empty(t, empty.Open)
	assert.Empty(t, empty.Uncosted)
	assert.Zero(t, empty.RealizedMargin())
	assert.True(t, empty.ReconcilesTo(nil))

	assert.Empty(t, ledger.BucketRealized(nil, time.Hour))
	assert.Empty(t, ledger.BucketNaiveLegs(nil, time.Hour))
	assert.Nil(t, ledger.BucketRealized([]ledger.ClosedPosition{{}}, 0),
		"a non-positive bucket width yields nil, never one bucket per fragment")
	assert.Nil(t, ledger.BucketNaiveLegs([]ledger.CargoLeg{{}}, -time.Hour))

	// A zero-unit leg carries no position but its credits must still reconcile.
	zeroUnits := []ledger.CargoLeg{
		buy("b-zero", "H", "G", 0, 500, hour(6, 0), "tour"),
		sell("s-zero", "H", "G", 0, 700, hour(7, 0), "tour"),
	}
	degenerate := ledger.MatchPositions(zeroUnits)
	assert.Empty(t, degenerate.Closed)
	assert.True(t, degenerate.ReconcilesTo(zeroUnits),
		"zero-unit legs must be booked as remainders, not silently dropped")
}

// A realised bucket's margin ratio fails closed on a zero cost basis rather than reporting a
// fabricated break-even (RULINGS #4).
func TestRealizedBucket_MarginRatioFailsClosedOnZeroCost(t *testing.T) {
	_, ok := ledger.RealizedBucket{Revenue: 1_000}.MarginRatio()
	assert.False(t, ok, "a margin ratio on a zero basis is undefined, not 0%")

	ratio, ok := ledger.RealizedBucket{Cost: 1_000, Revenue: 1_250}.MarginRatio()
	require.True(t, ok)
	assert.InDelta(t, 0.25, ratio, 1e-9)

	rate := ledger.RealizedBucket{Cost: 1_000, Revenue: 3_000, Width: 2 * time.Hour}.CreditsPerHour()
	assert.InDelta(t, 1_000.0, rate, 1e-9)
	assert.Zero(t, ledger.RealizedBucket{Revenue: 5}.CreditsPerHour(), "zero width must not divide by zero")
}

// Scoping a report must filter the matched OUTPUT, never the input legs. This test pins the
// difference: matching the full history and filtering to hour 08 reports the true realised
// margin, whereas matching only hour 08's legs loses the purchase entirely and degrades the
// trade into an uncosted sale.
func TestFilterClosedByWindow_ScopesOutputWithoutTruncatingThePurchaseStream(t *testing.T) {
	full := []ledger.CargoLeg{
		buy("b-early", "TORWIND-A", "URANITE", 180, 629_100, hour(6, 22), "tour"),
		sell("s-late", "TORWIND-A", "URANITE", 180, 698_760, hour(8, 0), "tour"),
	}

	// Correct: match wide, filter the output.
	matched := ledger.MatchPositions(full)
	windowed := ledger.FilterClosedByWindow(matched.Closed, hour(8, 0), hour(9, 0))
	require.Len(t, windowed, 1)
	assert.Equal(t, int64(69_660), windowed[0].RealizedMargin())
	assert.Equal(t, int64(629_100), windowed[0].Cost,
		"the cost from OUTSIDE the window is correctly carried into it")

	// Wrong: filter the input. The purchase falls outside hour 08 and the trade degrades
	// into an uncosted sale — a 698,760 phantom windfall instead of a 69,660 profit.
	truncated := ledger.MatchPositions([]ledger.CargoLeg{full[1]})
	require.Empty(t, truncated.Closed)
	require.Len(t, truncated.Uncosted, 1)
	assert.Equal(t, int64(698_760), truncated.Uncosted[0].Revenue,
		"input truncation relocates the sp-76r2c artefact to the window edge — which is why "+
			"the reader matches the full history and filters afterwards")

	// Unbounded on either side.
	assert.Len(t, ledger.FilterClosedByWindow(matched.Closed, time.Time{}, time.Time{}), 1)
	assert.Empty(t, ledger.FilterClosedByWindow(matched.Closed, hour(9, 0), time.Time{}))
	assert.Empty(t, ledger.FilterClosedByWindow(matched.Closed, time.Time{}, hour(8, 0)),
		"the window end is exclusive")
}

// FilterOpenAsOf scopes inventory reporting by purchase instant.
func TestFilterOpenAsOf_ScopesInventoryByPurchaseInstant(t *testing.T) {
	matched := ledger.MatchPositions([]ledger.CargoLeg{
		buy("b-early", "TORWIND-A", "FOOD", 10, 10_000, hour(6, 0), "tour"),
		buy("b-late", "TORWIND-A", "IRON", 10, 20_000, hour(9, 0), "tour"),
	})
	require.Len(t, matched.Open, 2)

	asOfSeven := ledger.FilterOpenAsOf(matched.Open, hour(7, 0))
	require.Len(t, asOfSeven, 1)
	assert.Equal(t, "b-early", asOfSeven[0].BuyTxID)

	assert.Len(t, ledger.FilterOpenAsOf(matched.Open, time.Time{}), 2, "zero time means unbounded")
}

// Bucketing works at widths other than an hour, so a consumer can use the 3-hour blocks the
// sp-76r2c bead identifies as the only defensible naive series — and now get a correct
// figure at ANY width, including the sub-hourly ones that were previously unusable.
func TestBucketRealized_ArbitraryWidths(t *testing.T) {
	matched := ledger.MatchPositions([]ledger.CargoLeg{
		buy("b1", "H", "G", 10, 10_000, hour(6, 0), "tour"),
		sell("s1", "H", "G", 10, 13_000, hour(6, 10), "tour"),
		buy("b2", "H", "G", 10, 10_000, hour(6, 20), "tour"),
		sell("s2", "H", "G", 10, 14_000, hour(8, 40), "tour"),
	})

	quarterHour := ledger.BucketRealized(matched.Closed, 15*time.Minute)
	require.Len(t, quarterHour, 2, "15-minute buckets are now meaningful, not just 3-hour ones")
	assert.Equal(t, hour(6, 0), quarterHour[0].Start)
	assert.Equal(t, int64(3_000), quarterHour[0].RealizedMargin())
	assert.Equal(t, hour(8, 30), quarterHour[1].Start)
	assert.Equal(t, int64(4_000), quarterHour[1].RealizedMargin())

	threeHour := ledger.BucketRealized(matched.Closed, 3*time.Hour)
	require.Len(t, threeHour, 1, "both trades close inside one 3-hour block")
	assert.Equal(t, int64(7_000), threeHour[0].RealizedMargin())
	assert.InDelta(t, 7_000.0/3.0, threeHour[0].CreditsPerHour(), 1e-9)
}
