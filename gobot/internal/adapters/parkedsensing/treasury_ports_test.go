package parkedsensing

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeTxnRepo serves a fixed set of cargo-purchase amounts, COUNTS the reads so a test can prove
// two statistics came from ONE pass rather than merely observing that they agree, and KEEPS the
// query it was handed so a test can prove the scoping reached the driver instead of assuming it.
type fakeTxnRepo struct {
	rows     []int
	calls    int
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
	rows := make([]*ledger.Transaction, 0, len(r.rows))
	for _, amount := range r.rows {
		rows = append(rows, ledger.ReconstructTransaction(
			ledger.NewTransactionID(), playerID, time.Now(),
			ledger.TransactionTypePurchaseCargo, amount, 0, amount, "", nil, "", "", "",
		))
	}
	return rows, nil
}

func (r *fakeTxnRepo) CountByPlayer(
	context.Context, shared.PlayerID, ledger.QueryOptions,
) (int, error) {
	return len(r.rows), nil
}

// assertCargoScope pins the three fields that decide WHICH rows a spend figure is summed from,
// none of which is stricter-only. The scan is timestamp-ordered and bounded, so a lost type filter
// does not merely dilute the figure — non-cargo rows crowd real cargo purchases out of the bound
// and the guard reads LOW. A lost window sums the fleet's whole history into a per-hour rate. A
// bound below the port's own under-reads outflow, the permissive direction again; a bound that
// never reaches the driver leaves the read unbounded.
func assertCargoScope(t *testing.T, opts ledger.QueryOptions, since time.Time) {
	t.Helper()
	if opts.TransactionType == nil || *opts.TransactionType != ledger.TransactionTypePurchaseCargo {
		t.Fatalf("the read must be scoped to cargo purchases, got type filter %v", opts.TransactionType)
	}
	if opts.StartDate == nil || !opts.StartDate.Equal(since) {
		t.Fatalf("the window must reach the driver: got %v, want %v", opts.StartDate, since)
	}
	if opts.Limit != cargoSpendScan {
		t.Fatalf("the scan bound must reach the driver: got %d, want %d", opts.Limit, cargoSpendScan)
	}
}

// The scoping is an input to a money guard, so it is asserted at the driver rather than inferred
// from a figure a fake would have returned whatever the query said.
func TestCargoOutflowSince_ScopesTheQueryToRecentCargoPurchases(t *testing.T) {
	repo := &fakeTxnRepo{rows: []int{-40_000}}
	port := NewCargoSpendPort(repo)
	since := time.Now().Add(-time.Hour)

	if _, _, err := port.CargoOutflowSince(context.Background(), 1, since); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertCargoScope(t, repo.lastOpts, since)
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
	assertCargoScope(t, repo.lastOpts, since)
}

// CargoOutflowSince returns BOTH statistics from ONE pass over the same rows: the sum the
// runway term needs and the largest single row the hold-fill term needs. Two queries would be
// two chances for the two terms to see different windows.
func TestCargoOutflowSince_ReturnsSumAndLargestFromOnePass(t *testing.T) {
	repo := &fakeTxnRepo{rows: []int{-40_000, -80_000, -10_000}}
	port := NewCargoSpendPort(repo)

	total, largest, err := port.CargoOutflowSince(context.Background(), 1, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 130_000 {
		t.Fatalf("expected the absolute sum 130000, got %d", total)
	}
	if largest != 80_000 {
		t.Fatalf("expected the largest single row 80000, got %d", largest)
	}
	if repo.calls != 1 {
		t.Fatalf("both statistics must come from ONE query, got %d", repo.calls)
	}
}

// A stray POSITIVE row (a refund, a correction) must ADD to measured outflow rather than
// cancel real spend out of it, and must be able to raise the largest-single figure — a
// malformed row may only ever make the guard stricter.
func TestCargoOutflowSince_StrayPositiveRowMakesTheGuardStricter(t *testing.T) {
	repo := &fakeTxnRepo{rows: []int{-40_000, 90_000}}
	port := NewCargoSpendPort(repo)

	total, largest, err := port.CargoOutflowSince(context.Background(), 1, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 130_000 {
		t.Fatalf("a positive row must add, not cancel: got %d", total)
	}
	if largest != 90_000 {
		t.Fatalf("a positive row must be able to raise the largest single figure: got %d", largest)
	}
}

// No rows in the window is a genuine zero, not an error: a fleet that has bought no cargo
// reserves nothing above the immutable floor.
func TestCargoOutflowSince_NoRowsIsZero(t *testing.T) {
	port := NewCargoSpendPort(&fakeTxnRepo{})
	total, largest, err := port.CargoOutflowSince(context.Background(), 1, time.Now().Add(-time.Hour))
	if err != nil || total != 0 || largest != 0 {
		t.Fatalf("expected (0,0,nil), got (%d,%d,%v)", total, largest, err)
	}
}
