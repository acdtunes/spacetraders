package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
)

// ledger_positions_test.go — renderer tests for `spacetraders ledger positions` (sp-76r2c).
//
// The report is the surface an analyst actually reads, so the properties that stop the
// seven-false-conclusions failure mode have to hold IN THE RENDERED TEXT, not just in the
// matcher: the realised figure must appear against the CLOSING bucket, open inventory must be
// under its own heading and never inside a realised total, and the defective naive reading must
// be printed and labelled as defective rather than quietly dropped.

type fakePositionsReader struct {
	legs  []ledger.CargoLeg
	stats persistence.LegReadStats
	err   error
}

func (f fakePositionsReader) ReadCargoLegs(context.Context, int) ([]ledger.CargoLeg, persistence.LegReadStats, error) {
	return f.legs, f.stats, f.err
}

func cliAt(h, m int) time.Time { return time.Date(2026, 7, 30, h, m, 0, 0, time.UTC) }

func cliBuy(id, hull, good string, units int, amount int64, at time.Time, op string) ledger.CargoLeg {
	return ledger.CargoLeg{TxID: id, Hull: hull, Good: good, Units: units,
		Amount: -amount, OperationType: op, At: at, IsBuy: true}
}

func cliSell(id, hull, good string, units int, amount int64, at time.Time, op string) ledger.CargoLeg {
	return ledger.CargoLeg{TxID: id, Hull: hull, Good: good, Units: units,
		Amount: amount, OperationType: op, At: at, IsBuy: false}
}

// renderPositions runs the report over the given legs, ending at 12:00 with a 6h window.
func renderPositions(t *testing.T, legs []ledger.CargoLeg, stats persistence.LegReadStats) string {
	t.Helper()
	var out bytes.Buffer
	err := runLedgerPositions(context.Background(),
		fakePositionsReader{legs: legs, stats: stats}, 5,
		positionsReportOptions{Now: cliAt(12, 0), Since: 6 * time.Hour, Bucket: time.Hour},
		&out)
	require.NoError(t, err)
	return out.String()
}

// line returns the rendered row whose first field is the given bucket label.
func line(t *testing.T, report, prefix string) string {
	t.Helper()
	for _, l := range strings.Split(report, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), prefix) {
			return l
		}
	}
	t.Fatalf("no line starting %q in report:\n%s", prefix, report)
	return ""
}

// matchedFields splits the MATCHED side of a bucket row into its columns:
// [label, closes, units, cost, revenue, realised, margin%]. Asserting on the exact realised
// column matters — a substring search over the whole row also matches the naive column and the
// revenue column, and would pass on a report that placed the figure in the wrong one.
func matchedFields(t *testing.T, report, prefix string) []string {
	t.Helper()
	matchedSide := strings.Split(line(t, report, prefix), "|")[0]
	var out []string
	for _, f := range strings.Split(matchedSide, "  ") {
		if trimmed := strings.TrimSpace(f); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// realisedField returns the REALISED column of a bucket row.
func realisedField(t *testing.T, report, prefix string) string {
	t.Helper()
	f := matchedFields(t, report, prefix)
	require.Len(t, f, 7, "expected 7 matched columns in %q", prefix)
	return f[5]
}

// THE REGRESSION TEST at the report surface: a trade bought in hour 06 and sold in hour 07
// realises in hour 07, and the report shows the naive reading's −1,000,000 / +1,200,000
// alongside so the distortion is visible rather than merely fixed.
func TestLedgerPositionsReport_StraddlingTradeRealisesInTheClosingBucket(t *testing.T) {
	report := renderPositions(t, []ledger.CargoLeg{
		cliBuy("b", "TORWIND-27A", "CLOTHING", 100, 1_000_000, cliAt(6, 30), "tour"),
		cliSell("s", "TORWIND-27A", "CLOTHING", 100, 1_200_000, cliAt(7, 15), "tour"),
	}, persistence.LegReadStats{RowsScanned: 2, LegsMatched: 2})

	// The purchase hour appears (the naive column has a figure there) but realises NOTHING:
	// its matched columns are em dashes, not a 1,000,000 loss.
	buyHour := line(t, report, "2026-07-30 06:00")
	assert.Contains(t, buyHour, "-1,000,000",
		"the naive column must still show the purchase hour's defective 1.0M loss, labelled as naive")
	assert.NotContains(t, strings.Split(buyHour, "|")[0], "1,000,000",
		"the MATCHED side of the purchase-hour row must not report the cost as a realised figure")
	assert.Equal(t, "—", realisedField(t, report, "2026-07-30 06:00"),
		"the purchase hour closed nothing, so its REALISED column must be an em dash")

	// The closing hour carries the whole trade, cost included.
	sellHour := line(t, report, "2026-07-30 07:00")
	matchedSide := strings.Split(sellHour, "|")[0]
	assert.Contains(t, matchedSide, "1,000,000", "the closing bucket carries the previous hour's cost")
	assert.Contains(t, matchedSide, "1,200,000", "and the sale revenue")
	assert.Contains(t, matchedSide, "200,000", "and reports +200,000 realised")
	assert.Contains(t, matchedSide, "20.0%", "a plain 20% markup")
	assert.Contains(t, sellHour, "1,200,000",
		"the naive column shows the closing hour as a 1.2M windfall")

	// The naive column is labelled as defective wherever it appears.
	assert.Contains(t, report, "NAIVE (defective)")
	assert.Contains(t, report, "DEFECTIVE — leg placement, not economics; do not quote")

	// The realised total and rate are the matched ones.
	assert.Equal(t, "200,000", realisedField(t, report, "TOTAL"))
}

// Open inventory is reported under its own heading, partitioned by the operation that OPENED it,
// and never inside a realised bucket — the distinction the bead's acceptance criterion requires.
func TestLedgerPositionsReport_OpenInventoryIsSeparateAndPartitionedByOperation(t *testing.T) {
	report := renderPositions(t, []ledger.CargoLeg{
		// A closed trade, so the realised table is non-empty.
		cliBuy("b1", "TORWIND-A", "FOOD", 10, 100_000, cliAt(6, 0), "tour"),
		cliSell("s1", "TORWIND-A", "FOOD", 10, 130_000, cliAt(7, 0), "tour"),
		// A tour purchase still in the hold.
		cliBuy("b2", "TORWIND-A", "URANITE", 180, 629_100, cliAt(8, 0), "tour"),
		// A contract purchase that never closes by construction.
		cliBuy("b3", "TORWIND-B", "DIAMONDS", 77, 7_161, cliAt(9, 0), "contract"),
	}, persistence.LegReadStats{RowsScanned: 4, LegsMatched: 4})

	assert.Contains(t, report, "OPEN INVENTORY AT COST")
	assert.Contains(t, report, "NOT a loss")

	openSection := report[strings.Index(report, "OPEN INVENTORY AT COST"):]
	assert.Contains(t, openSection, "629,100", "the open tour position at cost")
	assert.Contains(t, openSection, "7,161", "the open contract position at cost")
	assert.Contains(t, openSection, "636,261", "and their total basis")
	assert.Contains(t, openSection, "contract",
		"open inventory must name the operation that opened it so structurally-non-closing "+
			"classes can be partitioned out")
	assert.Contains(t, openSection, "NEVER produce a SELL_CARGO",
		"the report must warn that contract/stocker/factory-input purchases accumulate forever")

	// The open basis must NOT appear as a realised figure. It DOES appear in the naive column
	// (that is the defect being displayed), so the assertion is scoped to the matched side.
	assert.Equal(t, "—", realisedField(t, report, "2026-07-30 08:00"),
		"hour 08 bought URANITE and closed nothing: it must realise nothing, not −629,100")
	assert.Equal(t, "—", realisedField(t, report, "2026-07-30 09:00"),
		"hour 09 bought contract cargo and closed nothing")
	assert.Equal(t, "30,000", realisedField(t, report, "TOTAL"),
		"the realised total is the FOOD trade only — open inventory is not netted into it")
	// The naive column, by contrast, reports both purchase hours as losses.
	assert.Contains(t, strings.Split(line(t, report, "2026-07-30 08:00"), "|")[1], "-629,100")
}

// Uncosted sales are reported under their own heading and excluded from realised margin, because
// a zero cost basis is an assertion about provenance rather than a measurement.
func TestLedgerPositionsReport_UncostedSalesAreExcludedFromMarginAndLabelled(t *testing.T) {
	report := renderPositions(t, []ledger.CargoLeg{
		cliBuy("b1", "TORWIND-A", "FOOD", 10, 100_000, cliAt(6, 0), "tour"),
		cliSell("s1", "TORWIND-A", "FOOD", 10, 130_000, cliAt(7, 0), "tour"),
		// Siphoned/transferred-in cargo: sold with no purchase on that hull+good.
		cliSell("s2", "TORWIND-14", "DIAMONDS", 53, 6_201, cliAt(9, 53), "tour"),
	}, persistence.LegReadStats{RowsScanned: 3, LegsMatched: 3})

	assert.Contains(t, report, "UNCOSTED SALES")
	assert.Contains(t, report, "EXCLUDED from realised margin")
	uncostedSection := report[strings.Index(report, "UNCOSTED SALES"):]
	assert.Contains(t, uncostedSection, "6,201")

	// Realised total is the FOOD trade only. The naive column DOES fold the 6,201 in
	// (130,000 − 100,000 + 6,201 = 36,201), which is exactly why it must be kept separate.
	assert.Equal(t, "30,000", realisedField(t, report, "TOTAL"),
		"uncosted revenue must not inflate the realised total")
	assert.Contains(t, strings.Split(line(t, report, "TOTAL"), "|")[1], "36,201",
		"the naive reading DOES absorb uncosted revenue, undetectably")
}

// When the reader could not attribute every ledger row, the report says so instead of presenting
// its total as if it covered the whole ledger.
func TestLedgerPositionsReport_WarnsWhenCoverageIsIncomplete(t *testing.T) {
	legs := []ledger.CargoLeg{
		cliBuy("b1", "TORWIND-A", "FOOD", 10, 100_000, cliAt(6, 0), "tour"),
		cliSell("s1", "TORWIND-A", "FOOD", 10, 130_000, cliAt(7, 0), "tour"),
	}

	covered := renderPositions(t, legs, persistence.LegReadStats{RowsScanned: 2, LegsMatched: 2})
	assert.NotContains(t, covered, "WARNING")

	incomplete := renderPositions(t, legs, persistence.LegReadStats{
		RowsScanned: 5, LegsMatched: 2, UnattributableRows: 2,
		MalformedMetadataRows: 1, UnattributableCredits: 28_812,
	})
	assert.Contains(t, incomplete, "WARNING")
	assert.Contains(t, incomplete, "do not cover the whole ledger")
	assert.Contains(t, incomplete, "28,812",
		"the credits the report cannot explain must be stated, not hidden")
}

// The window scopes CLOSES, and a purchase from before the window still charges its cost against
// the realised margin inside it. Filtering the legs instead would relocate the sp-76r2c artefact
// to the window edge.
func TestLedgerPositionsReport_ChargesCostFromBeforeTheWindow(t *testing.T) {
	report := renderPositions(t, []ledger.CargoLeg{
		// Bought at 01:00 — five hours before the 06:00 window opens.
		cliBuy("b-old", "TORWIND-A", "URANITE", 180, 629_100, cliAt(1, 0), "tour"),
		cliSell("s-in", "TORWIND-A", "URANITE", 180, 698_760, cliAt(8, 5), "tour"),
	}, persistence.LegReadStats{RowsScanned: 2, LegsMatched: 2})

	f := matchedFields(t, report, "2026-07-30 08:00")
	require.Len(t, f, 7)
	assert.Equal(t, "629,100", f[3],
		"the pre-window purchase cost must be charged against the in-window close")
	assert.Equal(t, "698,760", f[4], "revenue is the full sale")
	assert.Equal(t, "69,660", f[5],
		"REALISED is 69,660 — a report showing 698,760 here stranded the purchase outside the window")

	// The naive reading sees only the sale leg and calls the whole 698,760 profit.
	assert.Contains(t, strings.Split(line(t, report, "2026-07-30 08:00"), "|")[1], "698,760")
}

// Degenerate flags fail loudly rather than producing a report with meaningless buckets.
func TestLedgerPositionsReport_RejectsNonPositiveWindowAndBucket(t *testing.T) {
	var out bytes.Buffer
	reader := fakePositionsReader{}

	err := runLedgerPositions(context.Background(), reader, 5,
		positionsReportOptions{Now: cliAt(12, 0), Since: 6 * time.Hour, Bucket: 0}, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--bucket must be positive")

	err = runLedgerPositions(context.Background(), reader, 5,
		positionsReportOptions{Now: cliAt(12, 0), Since: 0, Bucket: time.Hour}, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--since must be positive")
}

// An empty ledger renders a report with no realised buckets rather than a confident zero.
func TestLedgerPositionsReport_EmptyLedger(t *testing.T) {
	report := renderPositions(t, nil, persistence.LegReadStats{})
	assert.Contains(t, report, "POSITION-MATCHED REALISED MARGIN")
	assert.Contains(t, report, "0 cargo rows scanned")
	assert.NotContains(t, report, "WARNING")
	assert.Contains(t, report, "OPEN INVENTORY AT COST")
}

// --good scopes both readings consistently, so the comparison stays honest under a filter.
func TestLedgerPositionsReport_GoodFilterScopesBothReadings(t *testing.T) {
	legs := []ledger.CargoLeg{
		cliBuy("b-cloth", "TORWIND-A", "CLOTHING", 10, 100_000, cliAt(6, 0), "tour"),
		cliSell("s-cloth", "TORWIND-A", "CLOTHING", 10, 130_000, cliAt(7, 0), "tour"),
		cliBuy("b-food", "TORWIND-A", "FOOD", 10, 500_000, cliAt(6, 0), "tour"),
		cliSell("s-food", "TORWIND-A", "FOOD", 10, 900_000, cliAt(7, 0), "tour"),
	}

	var out bytes.Buffer
	require.NoError(t, runLedgerPositions(context.Background(),
		fakePositionsReader{legs: legs, stats: persistence.LegReadStats{RowsScanned: 4, LegsMatched: 4}},
		5,
		positionsReportOptions{Now: cliAt(12, 0), Since: 6 * time.Hour, Bucket: time.Hour, Good: "CLOTHING"},
		&out))
	report := out.String()

	assert.Contains(t, report, "good=CLOTHING", "the report must state its scope")
	assert.Contains(t, line(t, report, "TOTAL"), "30,000", "only the CLOTHING trade is realised")
	assert.NotContains(t, line(t, report, "TOTAL"), "400,000", "the FOOD trade is out of scope")
	// The naive column is scoped identically, so the two columns remain comparable.
	assert.Contains(t, strings.Split(line(t, report, "2026-07-30 06:00"), "|")[1], "-100,000")
}
