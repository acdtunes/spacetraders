package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
)

// ledger_positions.go — `spacetraders ledger positions`, the analyst-facing surface for
// position-matched realised accounting (sp-76r2c).
//
// It exists so that asking "what did the fleet realise per hour" is one command rather than a
// forty-line FIFO CTE retyped (and re-derived, and mis-derived) per question. It prints the
// naive time-bucketed leg sum RIGHT NEXT TO the position-matched figure, because the naive
// reading is plausible — it never looks broken, it just inverts — and the only durable defence
// against trusting it again is to show the two side by side every time.

// positionsReader is the read this command needs, kept narrow so the renderer is testable
// without a database.
type positionsReader interface {
	ReadCargoLegs(ctx context.Context, playerID int) ([]ledger.CargoLeg, persistence.LegReadStats, error)
}

func newLedgerPositionsCommand() *cobra.Command {
	var (
		since   time.Duration
		bucket  time.Duration
		good    string
		hull    string
		operatn string
	)

	cmd := &cobra.Command{
		Use:   "positions",
		Short: "Position-matched realised margin per time bucket, with open inventory at cost (sp-76r2c)",
		Long: `Report realised trade margin with each purchase matched to the sale that CLOSED it,
attributed to the CLOSING instant — so an hourly figure means "positions closed this
hour", which is an answerable question.

WHY THIS EXISTS. transactions stores each trade leg as its own signed row
(PURCHASE_CARGO negative, SELL_CARGO positive). A hull buys in one clock hour and sells
in another, so any time-bucketed SUM(amount) measures where the hour boundary fell
rather than economics — and it INVERTS signs rather than merely blurring them. Live, the
same goods in adjacent hours read −2,132,071 then +1,931,781. That substrate produced
seven separate false conclusions in one analysis session (sp-76r2c).

Every report prints both readings. The NAIVE column is the defective one, shown only so
the distortion is visible; never quote it.

Matching rule: chronological FIFO on (hull, good), NOT scoped by operation_type — a
position opened on a tour is routinely closed by a liquidation. Purchases still in a hold
are reported separately as inventory at cost and never as a loss. Sales with no purchase
to close (siphoned or transferred-in cargo) are reported separately as uncosted revenue
and never folded into margin.

The full ledger history is always matched; --since filters the RESULT. Narrowing the read
instead would strand purchases outside the window and just relocate the defect to the
window edge.

Examples:
  spacetraders ledger positions --since 6h
  spacetraders ledger positions --since 24h --bucket 3h
  spacetraders ledger positions --since 6h --good CLOTHING
  spacetraders ledger positions --since 12h --hull TORWIND-27A`,
		RunE: func(cmd *cobra.Command, args []string) error {
			playerIdent, err := resolvePlayerIdentifier()
			if err != nil {
				return err
			}
			db, err := openDatabase()
			if err != nil {
				return err
			}
			// Resolve --agent to a numeric id: matching is scoped by player_id, and a
			// silent 0 would read an empty ledger and report a confident zero margin.
			resolvedPlayerID, err := resolveNumericPlayerID(cmd.Context(), db, playerIdent)
			if err != nil {
				return err
			}
			opts := positionsReportOptions{
				Now: time.Now().UTC(), Since: since, Bucket: bucket,
				Good: good, Hull: hull, OperationType: operatn,
			}
			return runLedgerPositions(cmd.Context(),
				persistence.NewGormTransactionRepository(db), resolvedPlayerID, opts, os.Stdout)
		},
	}

	cmd.Flags().DurationVar(&since, "since", 6*time.Hour, "Trailing window to report closes in")
	cmd.Flags().DurationVar(&bucket, "bucket", time.Hour, "Time bucket width (1h, 15m, 3h, ...)")
	cmd.Flags().StringVar(&good, "good", "", "Restrict to one good symbol")
	cmd.Flags().StringVar(&hull, "hull", "", "Restrict to one hull symbol")
	cmd.Flags().StringVar(&operatn, "operation", "",
		"Restrict to positions CLOSED under one operation_type (buys are never filtered — "+
			"a position may open under a different operation than it closes under)")

	return cmd
}

type positionsReportOptions struct {
	Now           time.Time
	Since         time.Duration
	Bucket        time.Duration
	Good          string
	Hull          string
	OperationType string
}

// reportWindow is the half-open [start, end) the OUTPUT is scoped to. Matching always runs
// over the full history first, so this never bounds what is read.
type reportWindow struct {
	start time.Time
	end   time.Time
}

func (o positionsReportOptions) window() reportWindow {
	return reportWindow{start: o.Now.Add(-o.Since).Truncate(o.Bucket), end: o.Now}
}

func (w reportWindow) contains(t time.Time) bool {
	return !t.Before(w.start) && t.Before(w.end)
}

// includesGoodHull applies the --good/--hull scope. Inventory is filtered by this alone:
// --operation scopes CLOSES, and an open position has not closed.
func (o positionsReportOptions) includesGoodHull(good, hull string) bool {
	if o.Good != "" && good != o.Good {
		return false
	}
	return o.Hull == "" || hull == o.Hull
}

func (o positionsReportOptions) includes(good, hull, operationType string) bool {
	if !o.includesGoodHull(good, hull) {
		return false
	}
	return o.OperationType == "" || operationType == o.OperationType
}

// runLedgerPositions matches the player's full cargo history, scopes the OUTPUT to the
// requested window, and renders the matched figures beside the naive ones.
func runLedgerPositions(ctx context.Context, reader positionsReader, playerID int, opts positionsReportOptions, out io.Writer) error {
	if opts.Bucket <= 0 {
		return fmt.Errorf("--bucket must be positive, got %s", opts.Bucket)
	}
	if opts.Since <= 0 {
		return fmt.Errorf("--since must be positive, got %s", opts.Since)
	}

	legs, stats, err := reader.ReadCargoLegs(ctx, playerID)
	if err != nil {
		return err
	}

	// Match the FULL history, then filter. Filtering the legs first would strand every
	// purchase made before the window and turn real trades into uncosted sales.
	matched := ledger.MatchPositions(legs)
	reconciles := matched.ReconcilesTo(legs)

	win := opts.window()

	closed := ledger.FilterClosedByWindow(matched.Closed, win.start, win.end)
	closed = filterClosed(closed, opts)
	uncosted := filterUncosted(matched.Uncosted, win, opts)
	open := filterOpen(matched.Open, opts)

	// The naive comparison is computed over the legs whose OWN instant falls in the window —
	// exactly what `SELECT date_trunc(...), SUM(amount) ... WHERE created_at >= ?` does.
	naiveLegs := filterLegsForNaive(legs, win, opts)

	buckets := ledger.BucketRealized(closed, opts.Bucket)
	naiveBuckets := ledger.BucketNaiveLegs(naiveLegs, opts.Bucket)

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	writePositionsHeader(w, playerID, win, opts, stats, reconciles)
	writeBucketComparison(w, buckets, naiveBuckets)
	writeBucketTotals(w, buckets, naiveBuckets, opts.Since)
	writeUncostedSales(w, uncosted)
	writeOpenInventory(w, open, win.end)
	return w.Flush()
}

func writePositionsHeader(w io.Writer, playerID int, win reportWindow,
	opts positionsReportOptions, stats persistence.LegReadStats, reconciles bool) {
	fmt.Fprintf(w, "POSITION-MATCHED REALISED MARGIN  (player %d)\n", playerID)
	fmt.Fprintf(w, "window\t%s → %s  (%s, %s buckets)\n",
		win.start.Format("2006-01-02 15:04"), win.end.Format("2006-01-02 15:04"),
		opts.Since, opts.Bucket)
	if scope := describeScope(opts); scope != "" {
		fmt.Fprintf(w, "scope\t%s\n", scope)
	}
	fmt.Fprintf(w, "ledger\t%d cargo rows scanned, %d matched, %d unattributable (%s credits)\n",
		stats.RowsScanned, stats.LegsMatched, stats.UnattributableRows+stats.MalformedMetadataRows,
		formatCredits(int(stats.UnattributableCredits)))
	if !stats.Covered() {
		fmt.Fprintf(w, "\tWARNING: some rows carry metadata this reader cannot attribute to a\n")
		fmt.Fprintf(w, "\tposition; the figures below do not cover the whole ledger.\n")
	}
	if !reconciles {
		fmt.Fprintf(w, "\tWARNING: matched credits do NOT reconcile to the ledger — treat every\n")
		fmt.Fprintf(w, "\tfigure below as unsafe and file a bug.\n")
	}
	fmt.Fprintln(w)
}

func writeBucketComparison(w io.Writer, buckets []ledger.RealizedBucket, naiveBuckets []ledger.NaiveBucket) {
	naiveByStart := make(map[time.Time]ledger.NaiveBucket, len(naiveBuckets))
	for _, b := range naiveBuckets {
		naiveByStart[b.Start] = b
	}

	fmt.Fprintln(w, "bucket\tcloses\tunits\tcost\trevenue\tREALISED\tmargin%\t| NAIVE (defective)\tlegs")
	for _, start := range unionBucketStarts(buckets, naiveBuckets) {
		matchedBucket, hasMatched := findRealized(buckets, start)
		naiveBucket, hasNaive := naiveByStart[start]

		realised, marginPct := "—", "—"
		closes, units, cost, revenue := "—", "—", "—", "—"
		if hasMatched {
			realised = formatCredits(int(matchedBucket.RealizedMargin()))
			closes = fmt.Sprintf("%d", matchedBucket.Closes)
			units = fmt.Sprintf("%d", matchedBucket.Units)
			cost = formatCredits(int(matchedBucket.Cost))
			revenue = formatCredits(int(matchedBucket.Revenue))
			if ratio, ok := matchedBucket.MarginRatio(); ok {
				marginPct = fmt.Sprintf("%.1f%%", ratio*100)
			}
		}
		naive, legCount := "—", "—"
		if hasNaive {
			naive = formatCredits(int(naiveBucket.NetCredits))
			legCount = fmt.Sprintf("%d/%d", naiveBucket.Buys, naiveBucket.Sells)
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t| %s\t%s\n",
			start.Format("2006-01-02 15:04"), closes, units, cost, revenue, realised, marginPct,
			naive, legCount)
	}
}

func writeBucketTotals(w io.Writer, buckets []ledger.RealizedBucket, naiveBuckets []ledger.NaiveBucket, since time.Duration) {
	var totalCost, totalRevenue int64
	var totalCloses, totalUnits int
	for _, b := range buckets {
		totalCost += b.Cost
		totalRevenue += b.Revenue
		totalCloses += b.Closes
		totalUnits += b.Units
	}
	var naiveTotal int64
	var naiveBuys, naiveSells int
	for _, b := range naiveBuckets {
		naiveTotal += b.NetCredits
		naiveBuys += b.Buys
		naiveSells += b.Sells
	}
	fmt.Fprintf(w, "TOTAL\t%d\t%d\t%s\t%s\t%s\t%s\t| %s\t%d/%d\n",
		totalCloses, totalUnits,
		formatCredits(int(totalCost)), formatCredits(int(totalRevenue)),
		formatCredits(int(totalRevenue-totalCost)),
		formatMarginPct(totalCost, totalRevenue),
		formatCredits(int(naiveTotal)), naiveBuys, naiveSells)
	fmt.Fprintln(w)

	hours := since.Hours()
	if hours > 0 {
		fmt.Fprintf(w, "realised $/hr\t%s\t(position-matched, closes attributed to the closing instant)\n",
			formatCredits(int(float64(totalRevenue-totalCost)/hours)))
		fmt.Fprintf(w, "naive $/hr\t%s\t(DEFECTIVE — leg placement, not economics; do not quote)\n",
			formatCredits(int(float64(naiveTotal)/hours)))
	}
	fmt.Fprintln(w)
}

func writeUncostedSales(w io.Writer, uncosted []ledger.UncostedSale) {
	if len(uncosted) == 0 {
		return
	}
	var uncostedRevenue int64
	var uncostedUnits int
	for _, u := range uncosted {
		uncostedRevenue += u.Revenue
		uncostedUnits += u.Units
	}
	fmt.Fprintf(w, "UNCOSTED SALES (in window, EXCLUDED from realised margin above)\n")
	fmt.Fprintf(w, "\t%d sales, %d units, %s credits — cargo with no purchase on that hull+good\n",
		len(uncosted), uncostedUnits, formatCredits(int(uncostedRevenue)))
	fmt.Fprintf(w, "\t(siphoned or transferred-in cargo; a large figure here means matched\n")
	fmt.Fprintf(w, "\thistory was truncated, not that the fleet found free cargo)\n")
	fmt.Fprintln(w)
}

// writeOpenInventory partitions by the operation that OPENED each position, so the classes
// that never close (contract, stocker, factory inputs) are visible rather than lumped in.
func writeOpenInventory(w io.Writer, open []ledger.OpenPosition, asOf time.Time) {
	fmt.Fprintf(w, "OPEN INVENTORY AT COST (as of %s — NOT a loss)\n", asOf.Format("2006-01-02 15:04"))
	byOperation := make(map[string]*openInventoryAggregate)
	for _, o := range open {
		agg, ok := byOperation[o.BuyOperationType]
		if !ok {
			agg = &openInventoryAggregate{oldest: o.OpenedAt}
			byOperation[o.BuyOperationType] = agg
		}
		agg.positions++
		agg.units += o.Units
		agg.basis += o.CostBasis
		if o.OpenedAt.Before(agg.oldest) {
			agg.oldest = o.OpenedAt
		}
	}
	operations := make([]string, 0, len(byOperation))
	for op := range byOperation {
		operations = append(operations, op)
	}
	sort.Slice(operations, func(i, j int) bool {
		return byOperation[operations[i]].basis > byOperation[operations[j]].basis
	})
	fmt.Fprintln(w, "buy operation\tpositions\tunits\tcost basis\toldest open")
	var openBasis int64
	for _, op := range operations {
		agg := byOperation[op]
		openBasis += agg.basis
		fmt.Fprintf(w, "%s\t%d\t%d\t%s\t%s\n",
			orDash(op), agg.positions, agg.units,
			formatCredits(int(agg.basis)), agg.oldest.Format("2006-01-02 15:04"))
	}
	fmt.Fprintf(w, "TOTAL\t%d\t\t%s\n", len(open), formatCredits(int(openBasis)))
	fmt.Fprintln(w)
	fmt.Fprintf(w, "NOTE\tcontract / stocker purchases and factory inputs NEVER produce a SELL_CARGO\n")
	fmt.Fprintf(w, "\t(contract revenue arrives as CONTRACT_FULFILLED with no hull attribution),\n")
	fmt.Fprintf(w, "\tso they accumulate here permanently. Partition on buy operation before\n")
	fmt.Fprintf(w, "\treading this total as cargo still in flight.\n")
}

type openInventoryAggregate struct {
	positions, units int
	basis            int64
	oldest           time.Time
}

func filterClosed(closed []ledger.ClosedPosition, opts positionsReportOptions) []ledger.ClosedPosition {
	out := make([]ledger.ClosedPosition, 0, len(closed))
	for _, c := range closed {
		if opts.includes(c.Good, c.Hull, c.SellOperationType) {
			out = append(out, c)
		}
	}
	return out
}

func filterUncosted(uncosted []ledger.UncostedSale, win reportWindow, opts positionsReportOptions) []ledger.UncostedSale {
	out := make([]ledger.UncostedSale, 0, len(uncosted))
	for _, u := range uncosted {
		if win.contains(u.ClosedAt) && opts.includes(u.Good, u.Hull, u.SellOperationType) {
			out = append(out, u)
		}
	}
	return out
}

// filterOpen is deliberately NOT window-filtered: inventory is a stock, not a flow, and a
// position opened before the window is still in the hold.
func filterOpen(open []ledger.OpenPosition, opts positionsReportOptions) []ledger.OpenPosition {
	out := make([]ledger.OpenPosition, 0, len(open))
	for _, o := range open {
		if opts.includesGoodHull(o.Good, o.Hull) {
			out = append(out, o)
		}
	}
	return out
}

// filterLegsForNaive selects the legs a naive windowed SQL sum would have seen: those whose OWN
// instant falls inside the window, regardless of whether their counterpart leg does. That
// asymmetry IS the defect being displayed.
func filterLegsForNaive(legs []ledger.CargoLeg, win reportWindow, opts positionsReportOptions) []ledger.CargoLeg {
	out := make([]ledger.CargoLeg, 0, len(legs))
	for _, leg := range legs {
		if win.contains(leg.At) && opts.includes(leg.Good, leg.Hull, leg.OperationType) {
			out = append(out, leg)
		}
	}
	return out
}

func findRealized(buckets []ledger.RealizedBucket, start time.Time) (ledger.RealizedBucket, bool) {
	for _, b := range buckets {
		if b.Start.Equal(start) {
			return b, true
		}
	}
	return ledger.RealizedBucket{}, false
}

// unionBucketStarts returns every bucket instant either reading produced, ascending. The union
// matters: a bucket present in one column and absent in the other is the clearest possible
// display of the misplacement.
func unionBucketStarts(realized []ledger.RealizedBucket, naive []ledger.NaiveBucket) []time.Time {
	seen := make(map[int64]time.Time)
	for _, b := range realized {
		seen[b.Start.UnixNano()] = b.Start
	}
	for _, b := range naive {
		seen[b.Start.UnixNano()] = b.Start
	}
	out := make([]time.Time, 0, len(seen))
	for _, t := range seen {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

func describeScope(opts positionsReportOptions) string {
	var parts []string
	if opts.Good != "" {
		parts = append(parts, "good="+opts.Good)
	}
	if opts.Hull != "" {
		parts = append(parts, "hull="+opts.Hull)
	}
	if opts.OperationType != "" {
		parts = append(parts, "closed under operation="+opts.OperationType)
	}
	if len(parts) == 0 {
		return ""
	}
	scope := parts[0]
	for _, p := range parts[1:] {
		scope += ", " + p
	}
	return scope
}

// formatMarginPct renders realised margin as a percentage of cost, or an em dash on a zero
// basis rather than a fabricated 0% (RULINGS #4).
func formatMarginPct(cost, revenue int64) string {
	if cost <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", float64(revenue-cost)/float64(cost)*100)
}
