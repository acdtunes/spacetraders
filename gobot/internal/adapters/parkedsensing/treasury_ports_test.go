package parkedsensing

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeTxnRepo serves amounts PER TRANSACTION TYPE, so a test can prove the two sides of the cargo
// window are read through two correctly scoped queries rather than one query answering for both. It
// KEEPS every query in the order it was handed them, because the ORDER is itself a guard property:
// recovery must be read before spend, so a row landing between the two reads can only raise the
// measured position. `rows` is the fallback for a type with no entry, which keeps the pre-existing
// single-type tests reading exactly as they did.
type fakeTxnRepo struct {
	rows     []int
	byType   map[ledger.TransactionType][]int
	calls    int
	queries  []ledger.QueryOptions
	lastOpts ledger.QueryOptions
}

func (r *fakeTxnRepo) Create(context.Context, *ledger.Transaction) error { return nil }

func (r *fakeTxnRepo) FindByID(
	context.Context, ledger.TransactionID, shared.PlayerID,
) (*ledger.Transaction, error) {
	return nil, nil
}

func (r *fakeTxnRepo) FindByPlayer(
	_ context.Context, playerID shared.PlayerID, opts ledger.QueryOptions,
) ([]*ledger.Transaction, error) {
	r.calls++
	r.lastOpts = opts
	r.queries = append(r.queries, opts)

	txnType := ledger.TransactionTypePurchaseCargo
	if opts.TransactionType != nil {
		txnType = *opts.TransactionType
	}
	amounts := r.rows
	if r.byType != nil {
		amounts = r.byType[txnType]
	}

	rows := make([]*ledger.Transaction, 0, len(amounts))
	for _, amount := range amounts {
		rows = append(rows, ledger.ReconstructTransaction(
			ledger.NewTransactionID(), playerID, time.Now(),
			txnType, amount, 0, amount, "", nil, "", "", "",
		))
	}
	return rows, nil
}

func (r *fakeTxnRepo) CountByPlayer(
	context.Context, shared.PlayerID, ledger.QueryOptions,
) (int, error) {
	return len(r.rows), nil
}

// queryFor returns the query the port issued for one transaction type, and whether it issued one.
func (r *fakeTxnRepo) queryFor(txnType ledger.TransactionType) (ledger.QueryOptions, bool) {
	for _, q := range r.queries {
		if q.TransactionType != nil && *q.TransactionType == txnType {
			return q, true
		}
	}
	return ledger.QueryOptions{}, false
}

// assertCargoScope pins the three fields that decide WHICH rows a spend figure is summed from,
// none of which is stricter-only. The scan is timestamp-ordered and bounded, so a lost type filter
// does not merely dilute the figure — non-cargo rows crowd real cargo purchases out of the bound
// and the guard reads LOW. A lost window sums the fleet's whole history into a per-hour rate. A
// bound below the port's own under-reads outflow, the permissive direction again; a bound that
// never reaches the driver leaves the read unbounded.
func assertCargoScope(t *testing.T, opts ledger.QueryOptions, txnType ledger.TransactionType, since time.Time) {
	t.Helper()
	if opts.TransactionType == nil || *opts.TransactionType != txnType {
		t.Fatalf("the read must be scoped to %s, got type filter %v", txnType, opts.TransactionType)
	}
	if opts.StartDate == nil || !opts.StartDate.Equal(since) {
		t.Fatalf("the window must reach the driver: got %v, want %v", opts.StartDate, since)
	}
	if opts.Limit != cargoSpendScan {
		t.Fatalf("the scan bound must reach the driver: got %d, want %d", opts.Limit, cargoSpendScan)
	}
}

// The scoping is an input to a money guard, so it is asserted at the driver rather than inferred
// from a figure a fake would have returned whatever the query said. BOTH sides are pinned: the two
// halves of a netted position must be measured over the SAME window with the SAME bound, or the
// difference between them is a difference between two windows.
func TestCargoOutflowSince_ScopesBothSidesToTheSameCargoWindow(t *testing.T) {
	repo := &fakeTxnRepo{rows: []int{-40_000}}
	port := NewCargoSpendPort(repo)
	since := time.Now().Add(-time.Hour)

	if _, err := port.CargoOutflowSince(context.Background(), 1, since); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	buys, ok := repo.queryFor(ledger.TransactionTypePurchaseCargo)
	if !ok {
		t.Fatal("the port never asked for cargo purchases")
	}
	assertCargoScope(t, buys, ledger.TransactionTypePurchaseCargo, since)

	sells, ok := repo.queryFor(ledger.TransactionTypeSellCargo)
	if !ok {
		t.Fatal("the port never asked for cargo sales — the position cannot be netted from spend alone")
	}
	assertCargoScope(t, sells, ledger.TransactionTypeSellCargo, since)
}

// RECOVERY IS READ FIRST, SPEND SECOND, and the order is a guard property rather than a style
// choice. Two reads cannot be atomic, so a row WILL sometimes land between them: with sales read
// first, a sale arriving in the gap is missed (recovery under-counted) and a purchase arriving in
// the gap is counted (spend over-counted). Both leave the measured position HIGHER than the truth,
// which is the only direction a money guard may be wrong in. Reversing these two lines silently
// inverts that.
func TestCargoOutflowSince_ReadsRecoveryBeforeSpend(t *testing.T) {
	repo := &fakeTxnRepo{byType: map[ledger.TransactionType][]int{
		ledger.TransactionTypePurchaseCargo: {-40_000},
		ledger.TransactionTypeSellCargo:     {50_000},
	}}
	port := NewCargoSpendPort(repo)

	if _, err := port.CargoOutflowSince(context.Background(), 1, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.queries) != 2 {
		t.Fatalf("expected exactly two reads, got %d", len(repo.queries))
	}
	if first := repo.queries[0].TransactionType; first == nil || *first != ledger.TransactionTypeSellCargo {
		t.Fatalf("recovery must be read FIRST so a row in the gap can only raise the position: got %v", first)
	}
}

// The sibling read feeds the probe-buy floor from the same rows and must be scoped identically:
// two money guards measuring one fleet's outflow through two different queries would reserve
// against two different measurements of one quantity.
func TestAbsCargoBuySpendSince_ScopesTheQueryToRecentCargoPurchases(t *testing.T) {
	repo := &fakeTxnRepo{rows: []int{-40_000, -80_000}}
	port := NewCargoSpendPort(repo)
	since := time.Now().Add(-time.Hour)

	total, err := port.AbsCargoBuySpendSince(context.Background(), 1, since)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 120_000 {
		t.Fatalf("expected the absolute sum 120000, got %d", total)
	}
	assertCargoScope(t, repo.lastOpts, ledger.TransactionTypePurchaseCargo, since)
}

// The spend side's two statistics come from ONE pass over the same rows: the sum the runway term
// needs and the largest single row the hold-fill term needs. Two purchase queries would be two
// chances for the two terms to see different windows.
func TestCargoOutflowSince_ReturnsSumAndLargestFromOnePurchasePass(t *testing.T) {
	repo := &fakeTxnRepo{byType: map[ledger.TransactionType][]int{
		ledger.TransactionTypePurchaseCargo: {-40_000, -80_000, -10_000},
		ledger.TransactionTypeSellCargo:     {70_000, 30_000},
	}}
	port := NewCargoSpendPort(repo)

	out, err := port.CargoOutflowSince(context.Background(), 1, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Spent != 130_000 {
		t.Fatalf("expected the absolute spend 130000, got %d", out.Spent)
	}
	if out.Largest != 80_000 {
		t.Fatalf("expected the largest single row 80000, got %d", out.Largest)
	}
	if out.Recovered != 100_000 {
		t.Fatalf("expected the recovered 100000, got %d", out.Recovered)
	}
	if !out.Complete {
		t.Fatal("a window well inside the scan bound was seen whole")
	}
	if repo.calls != 2 {
		t.Fatalf("one query per side and no more, got %d", repo.calls)
	}
}

// A stray POSITIVE row (a refund, a correction) must ADD to measured outflow rather than
// cancel real spend out of it, and must be able to raise the largest-single figure — a
// malformed row may only ever make the guard stricter.
func TestCargoOutflowSince_StrayPositiveRowMakesTheGuardStricter(t *testing.T) {
	repo := &fakeTxnRepo{byType: map[ledger.TransactionType][]int{
		ledger.TransactionTypePurchaseCargo: {-40_000, 90_000},
	}}
	port := NewCargoSpendPort(repo)

	out, err := port.CargoOutflowSince(context.Background(), 1, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Spent != 130_000 {
		t.Fatalf("a positive row must add, not cancel: got %d", out.Spent)
	}
	if out.Largest != 90_000 {
		t.Fatalf("a positive row must be able to raise the largest single figure: got %d", out.Largest)
	}
}

// THE TWO SIDES TAKE OPPOSITE RULES ON A MALFORMED ROW, AND BOTH RULES POINT AT THE GUARD. Spend
// takes each row's ABSOLUTE value, so a stray sign still adds; recovery DROPS any row that is not
// genuine income, because an absolute value there would subtract a phantom recovery and lower the
// position. Copying the spend loop onto the sell side is the natural mistake and it inverts the
// direction, so the case is pinned rather than left to symmetry.
func TestCargoOutflowSince_MalformedRecoveryRowCannotLowerThePosition(t *testing.T) {
	repo := &fakeTxnRepo{byType: map[ledger.TransactionType][]int{
		ledger.TransactionTypePurchaseCargo: {-100_000},
		ledger.TransactionTypeSellCargo:     {30_000, -50_000},
	}}
	port := NewCargoSpendPort(repo)

	out, err := port.CargoOutflowSince(context.Background(), 1, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Recovered != 30_000 {
		t.Fatalf("a negative sale row must be dropped, not absolute-valued: got %d, want 30000", out.Recovered)
	}
}

// A READ THAT HIT ITS ROW BOUND DID NOT SEE THE WINDOW, and a position netted across a window only
// half seen is understated by whatever fell outside the bound. The port reports the truncation
// rather than deciding what to do about it; the money math falls back to the gross measure.
//
// EITHER SIDE TRUNCATING IS ENOUGH. A truncated purchase read against a whole sale read is the
// weakening case outright; a truncated sale read is merely stricter, and is reported the same way
// because "the window was not fully seen" is one fact with one honest answer.
func TestCargoOutflowSince_TruncatedReadIsNotACompleteWindow(t *testing.T) {
	saturated := make([]int, cargoSpendScan)
	for i := range saturated {
		saturated[i] = -1_000
	}

	for _, tc := range []struct {
		name    string
		byType  map[ledger.TransactionType][]int
		wantErr bool
	}{
		{name: "purchases truncated", byType: map[ledger.TransactionType][]int{
			ledger.TransactionTypePurchaseCargo: saturated,
			ledger.TransactionTypeSellCargo:     {50_000},
		}},
		{name: "sales truncated", byType: map[ledger.TransactionType][]int{
			ledger.TransactionTypePurchaseCargo: {-50_000},
			ledger.TransactionTypeSellCargo:     saturated,
		}},
	} {
		out, err := NewCargoSpendPort(&fakeTxnRepo{byType: tc.byType}).
			CargoOutflowSince(context.Background(), 1, time.Now().Add(-time.Hour))
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.name, err)
		}
		if out.Complete {
			t.Fatalf("%s: a read at the scan bound must not claim a complete window", tc.name)
		}
	}
}

// No rows in the window is a genuine zero, not an error: a fleet that has bought no cargo
// reserves nothing above the immutable floor. The window is COMPLETE — nothing was hidden by a
// bound, so netting the (empty) recovery is honest.
func TestCargoOutflowSince_NoRowsIsZero(t *testing.T) {
	port := NewCargoSpendPort(&fakeTxnRepo{})
	out, err := port.CargoOutflowSince(context.Background(), 1, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Spent != 0 || out.Largest != 0 || out.Recovered != 0 {
		t.Fatalf("expected an empty window, got %+v", out)
	}
	if !out.Complete {
		t.Fatal("an empty window inside the bound was seen whole")
	}
}
