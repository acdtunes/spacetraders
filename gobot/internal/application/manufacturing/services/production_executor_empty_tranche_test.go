package services

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/goods"
)

// PART B (sp-q02m feeder crash #4): an input buy that returns "partial failure:
// ... 0 units processed" / API 400 — a market whose supply was drained between the
// scout read and the buy (an empty / zero-volume tranche) — must NOT crash the
// container. It is bounded-retried (covering a transient refill) and then skipped
// (covering a structurally-empty market) so the feeder run survives. A genuine funds
// shortfall also buys zero units but is NOT an empty tranche and must still surface.
//
// These reuse the dock-race harness: the mediator replays a scripted per-attempt
// PurchaseCargoCommand outcome, so we drive the exact crash signature deterministically.

// emptyTrancheError mirrors the production error chain: cargo_transaction.go wraps a
// first-tranche API rejection as "partial failure: ... 0 units processed ...", and the
// underlying cause is the API 400 for an exhausted/empty market.
func emptyTrancheError() error {
	return fmt.Errorf("partial failure: failed to purchase cargo after 0 successful transactions "+
		"(0 units processed, 0 credits): failed to purchase cargo: API error (status 400): "+
		"{\"error\":{\"message\":\"Market purchase failed. Trade good %s is not available in that quantity.\",\"code\":4602}}", dockRaceGood)
}

// insufficientFundsError also buys zero units (so it too carries "0 units processed")
// but is a genuine failure that must surface, never be silently skipped.
func insufficientFundsError() error {
	return fmt.Errorf("partial failure: failed to purchase cargo after 0 successful transactions " +
		"(0 units processed, 0 credits): failed to purchase cargo: API error (status 400): " +
		"{\"error\":{\"message\":\"Purchase failed. Agent has insufficient funds.\",\"code\":4600}}")
}

// Transient empty tranche: the first buy comes back empty, the retry (market refilled)
// succeeds. The feeder must recover, not crash.
func TestBuyGood_EmptyTranche_Transient_RetriesThenSucceeds(t *testing.T) {
	script := []error{emptyTrancheError(), nil}
	executor, repo, mediator := newDockRaceExecutor(t, script)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(context.Background(), repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("an empty tranche that refills must be retried, not crash: got %v", err)
	}
	if result == nil || result.QuantityAcquired <= 0 {
		t.Fatalf("expected a successful buy after the retry, got %+v", result)
	}
	if mediator.purchaseAttempts() != 2 {
		t.Fatalf("expected 2 purchase attempts (1 empty + 1 retry), got %d", mediator.purchaseAttempts())
	}
}

// Persistent empty tranche: every buy comes back empty (structurally empty market).
// The feeder must SKIP the tranche (0-unit result, no error) so the run continues,
// and must terminate after a BOUNDED number of retries — never infinite-loop.
func TestBuyGood_EmptyTranche_Persistent_SkipsAndSurvivesBounded(t *testing.T) {
	// Far more entries than the retry bound, so if it ever infinite-looped it would
	// keep pulling scripted empties rather than hanging on a docked-ship success.
	script := make([]error, 32)
	for i := range script {
		script[i] = emptyTrancheError()
	}
	executor, repo, mediator := newDockRaceExecutor(t, script)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	result, err := executor.ProduceGood(context.Background(), repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err != nil {
		t.Fatalf("a persistently empty tranche must be skipped, not crash the feeder: got %v", err)
	}
	if result == nil || result.QuantityAcquired != 0 {
		t.Fatalf("a skipped empty tranche must yield a 0-unit result, got %+v", result)
	}
	// Bounded: initial attempt + productionEmptyTrancheRetryLimit retries, then skip.
	wantAttempts := productionEmptyTrancheRetryLimit + 1
	if mediator.purchaseAttempts() != wantAttempts {
		t.Fatalf("expected exactly %d bounded purchase attempts, got %d", wantAttempts, mediator.purchaseAttempts())
	}
}

// A genuine funds shortfall buys zero units too, but is NOT an empty tranche: it must
// surface immediately and must NOT be retried or silently skipped.
func TestBuyGood_InsufficientFunds_Surfaces_NotSkipped(t *testing.T) {
	script := []error{insufficientFundsError()}
	executor, repo, mediator := newDockRaceExecutor(t, script)

	node := goods.NewSupplyChainNode(dockRaceGood, goods.AcquisitionBuy)
	_, err := executor.ProduceGood(context.Background(), repo.buildShip(), node, "X1-DR", 1, nil, false)
	if err == nil {
		t.Fatalf("insufficient funds is a genuine failure and must surface, got nil")
	}
	if !strings.Contains(err.Error(), "insufficient funds") {
		t.Fatalf("the genuine funds error must surface verbatim, got: %v", err)
	}
	if mediator.purchaseAttempts() != 1 {
		t.Fatalf("a genuine failure must not be retried, got %d purchase attempts", mediator.purchaseAttempts())
	}
}
