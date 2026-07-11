package services

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// sp-br0m: every factory input buy tags its ledger row with the a5j7 selector branch that chose
// the source, so the analyst can grade A1 (supply-first compliance) straight from the
// transactions table and split legal RESCUE buys from violations. buyGood stamps the branch on
// ctx (shared.WithSelectorBranch), and it rides through to the PURCHASE_CARGO recorder. These
// pins prove the RIGHT branch reaches the dispatch for the two branches A1 turns on — an
// ELIGIBLE (healthy supply-first) pick vs a RESCUE (single-source-degraded) buy. The metadata
// round-trip onto a real persisted ledger row is pinned in the cargo package
// (cargo_transaction_selector_branch_test.go). The literal tag strings are asserted directly
// because they are the stable contract the analyst greps — a rename must break loudly here.

// A healthy MODERATE+ source is an ELIGIBLE supply-first pick: the buy tags its ledger row
// selector_branch=eligible_supply_first — the A1-compliant branch.
func TestSelectorBranchTag_EligibleBuyTagsEligible(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-HIGH", supply: supplyHigh, ask: 5000},
	}}
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, nil)
	ctx := common.WithLogger(context.Background(), &dwellCapturingLogger{})

	result := produceBuy(t, executor, shipRepo, ctx)
	if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected a healthy eligible buy, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if got := mediator.purchaseBranchTag(); got != "eligible_supply_first" {
		t.Fatalf("an eligible supply-first buy must tag selector_branch=eligible_supply_first; got %q", got)
	}
}

// With no eligible source, a SCARCE market bought within the rescue cap is the legal
// single-source-degraded exception: the buy tags its ledger row selector_branch=rescue — the
// branch A1 must NOT score as a violation.
func TestSelectorBranchTag_RescueBuyTagsRescue(t *testing.T) {
	repo := &multiSourceMarketRepo{sources: []srcSpec{
		{waypoint: "X1-DR-SCARCE", supply: supplyScarce, ask: 5000},
	}}
	reader := &fakePriceHistoryReader{sellPrices: []int{4800, 4800, 4800}} // median 4800 -> cap 5760, ask 5000 within
	executor, shipRepo, mediator := newMultiSourceExecutor(t, repo, reader)
	ctx := common.WithLogger(context.Background(), &dwellCapturingLogger{})

	result := produceBuy(t, executor, shipRepo, ctx)
	if result == nil || result.QuantityAcquired <= 0 || mediator.purchaseAttempts() != 1 {
		t.Fatalf("expected a rescue buy within the cap, got result=%+v attempts=%d", result, mediator.purchaseAttempts())
	}
	if got := mediator.purchaseBranchTag(); got != "rescue" {
		t.Fatalf("a rescue-clause buy must tag selector_branch=rescue; got %q", got)
	}
}
