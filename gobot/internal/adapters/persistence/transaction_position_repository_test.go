package persistence_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
	"gorm.io/gorm"
)

// transaction_position_repository_test.go — integration tests (real GORM/sqlite, no mocks) for
// the position-matched realised-margin reader (sp-76r2c).
//
// The headline test is
// TestReadMatchedPositions_HourStraddlingTradeIsRealizedInTheClosingHour: the same
// buy-in-hour-N / sell-in-hour-N+1 regression as the domain suite, but driven end to end
// through the ledger table, so a reader that loses the purchase leg in SQL fails too — not
// just a matcher that misattributes it.

// positionFixture seeds transactions and returns the player id.
type positionFixture struct {
	t    *testing.T
	db   *gorm.DB
	repo *persistence.GormTransactionRepository
	pid  int
}

func newPositionFixture(t *testing.T, agent string) *positionFixture {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	player := persistence.PlayerModel{AgentSymbol: agent, Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)

	return &positionFixture{t: t, db: db, repo: persistence.NewGormTransactionRepository(db), pid: player.ID}
}

// row seeds one raw transaction. metadata is passed verbatim so a test can reproduce the
// LEGACY shapes the live ledger actually contains, not just the happy one.
func (f *positionFixture) row(id, txType, opType, metadata string, amount int, at time.Time) {
	f.t.Helper()
	category := "TRADING_REVENUE"
	if txType != "SELL_CARGO" {
		category = "TRADING_COSTS"
	}
	require.NoError(f.t, f.db.Create(&persistence.TransactionModel{
		ID: id, PlayerID: f.pid, Timestamp: at, CreatedAt: at,
		TransactionType: txType, Category: category, Amount: amount,
		OperationType: opType, Metadata: metadata,
	}).Error)
}

func (f *positionFixture) buy(id, hull, good string, units, amount int, at time.Time, op string) {
	f.row(id, "PURCHASE_CARGO", op, posCargoMeta(hull, good, units), -amount, at)
}

func (f *positionFixture) sell(id, hull, good string, units, amount int, at time.Time, op string) {
	f.row(id, "SELL_CARGO", op, posCargoMeta(hull, good, units), amount, at)
}

// posCargoMeta builds the CURRENT live metadata shape, key order and all, so the reader is
// exercised against the JSON it will actually meet in production.
func posCargoMeta(hull, good string, units int) string {
	return `{"agent":"TORWIND","units":` + strconv.Itoa(units) +
		`,"waypoint":"X1-BA69-BA4E","good_symbol":"` + good + `","ship_symbol":"` + hull + `"}`
}

// posAt is a UTC instant inside a named hour of 2026-07-30 — the day of the sp-76r2c
// demonstration.
func posAt(h, m int) time.Time { return time.Date(2026, 7, 30, h, m, 0, 0, time.UTC) }

// THE REGRESSION TEST, end to end through the ledger table (sp-76r2c acceptance criterion).
//
// A CLOTHING position bought at 06:30 and sold at 07:15. Hour 06 must realise NOTHING; hour 07
// must realise the whole +200,000 margin, carrying the cost from the previous hour. The naive
// reading over the same rows reports hour 06 = −1,000,000 and hour 07 = +1,200,000, and both
// halves are asserted here so the contrast is pinned against the real read path.
func TestReadMatchedPositions_HourStraddlingTradeIsRealizedInTheClosingHour(t *testing.T) {
	f := newPositionFixture(t, "POS-STRADDLE")
	f.buy("tx-buy", "TORWIND-27A", "CLOTHING", 100, 1_000_000, posAt(6, 30), "tour")
	f.sell("tx-sell", "TORWIND-27A", "CLOTHING", 100, 1_200_000, posAt(7, 15), "tour")

	matched, stats, err := f.repo.ReadMatchedPositions(context.Background(), f.pid)
	require.NoError(t, err)
	require.True(t, stats.Covered(), "both rows carry a full position key")
	assert.Equal(t, 2, stats.RowsScanned)
	assert.Equal(t, 2, stats.LegsMatched)

	require.Len(t, matched.Closed, 1)
	require.Empty(t, matched.Open)
	require.Empty(t, matched.Uncosted)
	assert.Equal(t, int64(200_000), matched.RealizedMargin())

	buckets := ledger.BucketRealized(matched.Closed, time.Hour)
	require.Len(t, buckets, 1, "one trade realises in exactly one hour")
	assert.True(t, buckets[0].Start.Equal(posAt(7, 0)),
		"the realised bucket is the CLOSING hour 07:00, got %s", buckets[0].Start)
	assert.Equal(t, int64(1_000_000), buckets[0].Cost,
		"hour 07 must carry hour 06's purchase cost; a zero here means the read lost the buy leg")
	assert.Equal(t, int64(1_200_000), buckets[0].Revenue)
	assert.Equal(t, int64(200_000), buckets[0].RealizedMargin())

	// Hour 06 realised nothing — it held inventory. Under the naive reading it was a 1.0M loss.
	for _, b := range buckets {
		assert.False(t, b.Start.Equal(posAt(6, 0)),
			"hour 06:00 closed no position and must emit no realised bucket")
	}

	// And the defect, over the identical rows.
	legs, _, err := f.repo.ReadCargoLegs(context.Background(), f.pid)
	require.NoError(t, err)
	naive := ledger.BucketNaiveLegs(legs, time.Hour)
	require.Len(t, naive, 2, "the naive reading splits one trade across two hours")
	assert.Equal(t, int64(-1_000_000), naive[0].NetCredits, "naive hour 06 reads as a pure loss")
	assert.Equal(t, int64(1_200_000), naive[1].NetCredits, "naive hour 07 reads as pure profit")
}

// A six-hour report must be scoped by filtering the matched OUTPUT, not by narrowing the read.
// Live p99 holding time is 25.6h, so a window-scoped read would strand purchases outside it and
// relocate the sp-76r2c artefact to the window edge. This pins that the reader returns the full
// history and that filtering afterwards yields the true margin.
func TestReadMatchedPositions_WindowedReportKeepsCostFromBeforeTheWindow(t *testing.T) {
	f := newPositionFixture(t, "POS-WINDOW")
	// Purchase 8 hours before the reporting window opens; sale inside it.
	f.buy("tx-old-buy", "TORWIND-A", "URANITE", 180, 629_100, posAt(0, 22), "tour")
	f.sell("tx-in-sell", "TORWIND-A", "URANITE", 180, 698_760, posAt(8, 5), "tour")

	matched, _, err := f.repo.ReadMatchedPositions(context.Background(), f.pid)
	require.NoError(t, err)

	windowed := ledger.FilterClosedByWindow(matched.Closed, posAt(6, 0), posAt(12, 0))
	require.Len(t, windowed, 1)
	assert.Equal(t, int64(629_100), windowed[0].Cost,
		"the purchase happened 8h before the window and its cost MUST still be charged "+
			"against the realised margin; otherwise the window edge becomes the new hour edge")
	assert.Equal(t, int64(69_660), windowed[0].RealizedMargin())
	assert.Equal(t, 8*time.Hour-17*time.Minute, windowed[0].HoldingTime())
}

// Legacy-metadata rows (the live manufacturing shape: good/quantity, no ship_symbol) cannot be
// attributed to a position. They must be COUNTED and excluded, never coerced into a zero-unit
// leg that would corrupt reconciliation while the read still looked complete.
func TestReadCargoLegs_LegacyMetadataRowsAreCountedNotSilentlyDropped(t *testing.T) {
	f := newPositionFixture(t, "POS-LEGACY")
	f.buy("tx-good", "TORWIND-A", "FOOD", 10, 10_000, posAt(6, 0), "tour")
	// The live legacy manufacturing shape — 226 purchases + 27 sales exist in production.
	f.row("tx-legacy", "PURCHASE_CARGO", "manufacturing",
		`{"good":"FAB_MATS","factory":"X1-KA42-I53","quantity":3,"price_per_unit":9604}`,
		-28_812, posAt(6, 30))
	// Metadata that is not JSON at all.
	f.row("tx-broken", "SELL_CARGO", "tour", `not json`, 5_000, posAt(6, 45))

	legs, stats, err := f.repo.ReadCargoLegs(context.Background(), f.pid)
	require.NoError(t, err)

	require.Len(t, legs, 1, "only the fully-attributable row becomes a leg")
	assert.Equal(t, "tx-good", legs[0].TxID)

	assert.Equal(t, 3, stats.RowsScanned)
	assert.Equal(t, 1, stats.LegsMatched)
	assert.Equal(t, 1, stats.UnattributableRows, "the legacy-shape row")
	assert.Equal(t, 1, stats.MalformedMetadataRows, "the unparseable row")
	assert.Equal(t, int64(28_812+5_000), stats.UnattributableCredits,
		"the credits the matched figures do NOT explain must be reported, not hidden")
	assert.False(t, stats.Covered(),
		"Covered() must be false so a report can say the total is incomplete")
}

// REFUEL rows never enter position matching. Their metadata ship_symbol is the empty string on
// 66,635 of 66,639 live rows and they carry no good or units, so folding them in would attribute
// fuel burn to whichever position happened to close nearby.
func TestReadCargoLegs_ExcludesRefuelAndOtherNonCargoTypes(t *testing.T) {
	f := newPositionFixture(t, "POS-REFUEL")
	f.buy("tx-buy", "TORWIND-A", "FOOD", 10, 10_000, posAt(6, 0), "tour")
	f.sell("tx-sell", "TORWIND-A", "FOOD", 10, 12_000, posAt(7, 0), "tour")
	f.row("tx-refuel", "REFUEL", "tour", `{"ship_symbol":""}`, -500, posAt(6, 30))
	f.row("tx-ship", "PURCHASE_SHIP", "", `{"ship_symbol":"TORWIND-A"}`, -2_000_000, posAt(6, 40))
	f.row("tx-contract", "CONTRACT_FULFILLED", "contract", `{}`, 900_000, posAt(6, 50))

	legs, stats, err := f.repo.ReadCargoLegs(context.Background(), f.pid)
	require.NoError(t, err)

	assert.Equal(t, 2, stats.RowsScanned,
		"only PURCHASE_CARGO and SELL_CARGO are scanned; REFUEL, PURCHASE_SHIP and "+
			"CONTRACT_FULFILLED are filtered in SQL and never counted as unattributable")
	require.Len(t, legs, 2)
	assert.True(t, stats.Covered())

	matched := ledger.MatchPositions(legs)
	assert.Equal(t, int64(2_000), matched.RealizedMargin(),
		"the 500-credit refuel must not be charged against this position's margin")
}

// A contract purchase never produces a SELL_CARGO — the cargo is delivered and the revenue
// arrives as CONTRACT_FULFILLED with no hull attribution. It must report as open inventory at
// cost, naming its operation so a consumer can partition it out, and must NOT read as a loss.
func TestReadMatchedPositions_ContractPurchaseIsOpenInventoryNotRealizedLoss(t *testing.T) {
	f := newPositionFixture(t, "POS-CONTRACT")
	f.buy("tx-contract-buy", "TORWIND-11", "DIAMONDS", 77, 7_161, posAt(2, 0), "contract")
	f.row("tx-contract-paid", "CONTRACT_FULFILLED", "contract", `{}`, 500_000, posAt(3, 0))

	matched, _, err := f.repo.ReadMatchedPositions(context.Background(), f.pid)
	require.NoError(t, err)

	require.Empty(t, matched.Closed)
	require.Len(t, matched.Open, 1)
	assert.Equal(t, "contract", matched.Open[0].BuyOperationType,
		"open inventory must name its operation: contract cargo never closes by construction")
	assert.Equal(t, int64(7_161), matched.OpenCostBasis())
	assert.Empty(t, ledger.BucketRealized(matched.Closed, time.Hour),
		"a contract purchase must never appear as a realised loss in any hour")
}

// Every credit read from the ledger lands in exactly one output bucket, over a fixture that
// exercises splits both ways, a cross-operation close, an uncosted sale and an open remainder.
func TestReadMatchedPositions_ReconcilesEveryCreditRead(t *testing.T) {
	f := newPositionFixture(t, "POS-RECONCILE")
	f.buy("b1", "TORWIND-A", "CLOTHING", 105, 176_265, posAt(5, 12), "tour")
	f.sell("s1", "TORWIND-A", "CLOTHING", 45, 100_003, posAt(6, 5), "tour")
	f.sell("s2", "TORWIND-A", "CLOTHING", 60, 99_997, posAt(8, 44), "liquidation")
	f.buy("b2", "TORWIND-A", "MEDICINE", 11, 284_521, posAt(9, 1), "arb_run")
	f.sell("s3", "TORWIND-A", "MEDICINE", 13, 401_111, posAt(10, 30), "manual") // 2 units uncosted
	f.buy("b3", "TORWIND-B", "FOOD", 225, 411_525, posAt(11, 0), "tour")        // stays open

	matched, stats, err := f.repo.ReadMatchedPositions(context.Background(), f.pid)
	require.NoError(t, err)
	require.True(t, stats.Covered())
	assert.Equal(t, 6, stats.LegsMatched)

	legs, _, err := f.repo.ReadCargoLegs(context.Background(), f.pid)
	require.NoError(t, err)
	require.True(t, matched.ReconcilesTo(legs),
		"matching redistributes credits across time; it must never create or destroy them")

	// The uncosted remainder is the 2 MEDICINE units sold beyond what was bought.
	require.Len(t, matched.Uncosted, 1)
	assert.Equal(t, 2, matched.Uncosted[0].Units)
	// The open remainder is the untouched FOOD purchase.
	require.Len(t, matched.Open, 1)
	assert.Equal(t, int64(411_525), matched.Open[0].CostBasis)
	// A liquidation sale closed a tour purchase: matching is not scoped by operation.
	var sawCrossOperation bool
	for _, c := range matched.Closed {
		if c.BuyOperationType == "tour" && c.SellOperationType == "liquidation" {
			sawCrossOperation = true
		}
	}
	assert.True(t, sawCrossOperation, "a liquidation sale must be allowed to close a tour purchase")
}

// The read is scoped to one player: another agent's legs must never enter this player's
// positions, or one agent's purchase could be matched against another's sale.
func TestReadCargoLegs_IsScopedToOnePlayer(t *testing.T) {
	f := newPositionFixture(t, "POS-PLAYER-A")

	other := persistence.PlayerModel{AgentSymbol: "POS-PLAYER-B", Token: "tok2", CreatedAt: time.Now()}
	require.NoError(t, f.db.Create(&other).Error)

	f.buy("a-buy", "TORWIND-A", "FOOD", 10, 10_000, posAt(6, 0), "tour")
	require.NoError(t, f.db.Create(&persistence.TransactionModel{
		ID: "b-sell", PlayerID: other.ID, Timestamp: posAt(7, 0), CreatedAt: posAt(7, 0),
		TransactionType: "SELL_CARGO", Category: "TRADING_REVENUE", Amount: 99_999,
		OperationType: "tour", Metadata: posCargoMeta("TORWIND-A", "FOOD", 10),
	}).Error)

	matched, stats, err := f.repo.ReadMatchedPositions(context.Background(), f.pid)
	require.NoError(t, err)

	assert.Equal(t, 1, stats.RowsScanned, "the other player's sale must not be read")
	assert.Empty(t, matched.Closed,
		"another agent's sale must never close this agent's purchase, even on the same hull symbol")
	require.Len(t, matched.Open, 1)
}

// An empty ledger yields empty results and a covered read, never a fabricated zero-margin hour.
func TestReadMatchedPositions_EmptyLedger(t *testing.T) {
	f := newPositionFixture(t, "POS-EMPTY")

	matched, stats, err := f.repo.ReadMatchedPositions(context.Background(), f.pid)
	require.NoError(t, err)

	assert.Zero(t, stats.RowsScanned)
	assert.True(t, stats.Covered())
	assert.Empty(t, matched.Closed)
	assert.Empty(t, matched.Open)
	assert.Empty(t, matched.Uncosted)
	assert.Empty(t, ledger.BucketRealized(matched.Closed, time.Hour))
}
