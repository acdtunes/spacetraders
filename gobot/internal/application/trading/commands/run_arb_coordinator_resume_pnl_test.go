package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- A resumed arb run must report honest P&L. The resume skips the completed
// buy, so without carrying the prior attempt's cost the completion line reports NetProfit
// as the full sale revenue (TotalCost=0), silently omitting the basis already paid. ---

// recordingCostPersister captures PersistBuyCost calls so a test can assert a fresh buy
// durably records its cost (and optionally injects a store failure to prove it never
// fails a completed buy).
type recordingCostPersister struct {
	calls []persistedCost
	err   error
}

type persistedCost struct {
	containerID    string
	playerID, cost int
}

func (p *recordingCostPersister) PersistBuyCost(ctx context.Context, containerID string, playerID, cost int) error {
	p.calls = append(p.calls, persistedCost{containerID: containerID, playerID: playerID, cost: cost})
	return p.err
}

// A resumed run (tranche already aboard) whose prior buy cost was persisted and reloaded
// (as buildArbCoordinatorCommand sets PriorAttemptCost from container config) must report
// NetProfit NET of that cost — revenue minus the real basis — not the full revenue.
func TestArbCoordinator_ResumeWithPersistedCost_ReportsHonestNetProfit(t *testing.T) {
	ship := newTradeHauler(t, "ARB-DKJ7-RESUME")
	if err := ship.ReceiveCargo(&shared.CargoItem{Symbol: trGood, Units: 12}); err != nil {
		t.Fatalf("preload cargo: %v", err)
	}
	h, mediator := newArbHandler(ship, nil)

	const priorCost = 24000 // 12u * 2000 basis, as the interrupted attempt actually paid
	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol:       ship.ShipSymbol(),
		Good:             trGood,
		BuyAt:            trSource,
		SellAt:           trDest,
		PlayerID:         1,
		ContainerID:      "arb-run-DKJ7",
		PriorAttemptCost: priorCost, // reloaded from persisted config on the restart rebuild
	})
	if err != nil {
		t.Fatalf("resume must complete, got error: %v", err)
	}
	arb := arbResponse(t, resp)

	// Revenue 12u * 3500 = 42000; honest net = 42000 - 24000 = 18000. The pre-fix bug
	// reported TotalCost=0 → NetProfit=42000 (the full revenue), over-stating by the basis.
	if arb.TotalCost != priorCost {
		t.Fatalf("resumed run must restore the prior attempt's cost: want TotalCost=%d, got %d", priorCost, arb.TotalCost)
	}
	if arb.TotalRevenue != 42000 {
		t.Fatalf("expected revenue 42000 (12u * 3500), got %d", arb.TotalRevenue)
	}
	if arb.NetProfit != 42000-priorCost {
		t.Fatalf("resumed NetProfit must be net of the prior cost: want %d, got %d", 42000-priorCost, arb.NetProfit)
	}
	if len(mediator.sells) != 1 || arb.UnitsTraded != 12 {
		t.Fatalf("the held tranche must be delivered once in full: got %d sells, %d units", len(mediator.sells), arb.UnitsTraded)
	}
}

// A FRESH run must durably record its buy cost the moment the buy succeeds, so a later
// resume can restore it. Asserts both channels persistBuyCostForResume writes: the
// injected store (for a daemon-restart rebuild) and the command's in-memory
// PriorAttemptCost (for an in-process retry).
func TestArbCoordinator_FreshBuy_PersistsCostForResume(t *testing.T) {
	ship := newTradeHauler(t, "ARB-DKJ7-FRESH") // empty 40u hold at trSource
	persister := &recordingCostPersister{}
	h, _ := newArbHandler(ship, nil)
	h.SetCostPersister(persister)

	cmd := &RunArbCoordinatorCommand{
		ShipSymbol:  ship.ShipSymbol(),
		Good:        trGood,
		BuyAt:       trSource,
		SellAt:      trDest,
		PlayerID:    7,
		ContainerID: "arb-run-DKJ7-FRESH",
	}
	resp, err := h.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("fresh run must complete, got error: %v", err)
	}
	arb := arbResponse(t, resp)

	// Full 40u hold at 2000 = 80000 basis.
	if arb.TotalCost != 80000 {
		t.Fatalf("expected fresh buy cost 80000, got %d", arb.TotalCost)
	}
	if len(persister.calls) != 1 {
		t.Fatalf("a fresh buy must persist its cost exactly once, got %d calls", len(persister.calls))
	}
	got := persister.calls[0]
	if got.containerID != "arb-run-DKJ7-FRESH" || got.playerID != 7 || got.cost != 80000 {
		t.Fatalf("persisted the wrong cost tuple: %+v (want container=arb-run-DKJ7-FRESH player=7 cost=80000)", got)
	}
	// In-memory carry for an in-process retry (same command object re-run by the runner).
	if cmd.PriorAttemptCost != 80000 {
		t.Fatalf("fresh buy must record PriorAttemptCost on the command for in-process retry, got %d", cmd.PriorAttemptCost)
	}
}

// The in-process retry path (the container runner re-running the SAME command object on a
// backoff): attempt 1 buys then fails at travel, attempt 2 resumes the held tranche and
// must report P&L net of attempt 1's cost — carried on the command, no store needed.
func TestArbCoordinator_InProcessRetry_ResumedPnLIncludesPriorCost(t *testing.T) {
	ship := newTradeHauler(t, "ARB-DKJ7-RETRY") // empty hold at trSource
	mediator := &arbFaultMediator{ship: ship, navFailsRemaining: 1}
	h := arbHandlerWith(mediator, ship)

	cmd := &RunArbCoordinatorCommand{
		ShipSymbol:  ship.ShipSymbol(),
		Good:        trGood,
		BuyAt:       trSource,
		SellAt:      trDest,
		PlayerID:    1,
		ContainerID: "arb-run-DKJ7-RETRY",
	}

	// Attempt 1: buys the 40u tranche (cost 80000), then the travel leg fails post-buy.
	if _, err := h.Handle(context.Background(), cmd); err == nil {
		t.Fatal("attempt 1 must fail at the post-buy travel leg")
	}
	if cmd.PriorAttemptCost != 80000 {
		t.Fatalf("attempt 1's buy must record the cost on the command for the retry, got %d", cmd.PriorAttemptCost)
	}

	// Attempt 2 (the runner's retry, same command): resumes the held tranche and sells it.
	resp, err := h.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("the retry must resume and complete, got error: %v", err)
	}
	arb := arbResponse(t, resp)
	if !arb.Completed {
		t.Fatalf("the retry must complete, got %+v", arb)
	}
	// Revenue 40u * 3500 = 140000; honest net = 140000 - 80000 = 60000 (NOT the full
	// 140000 the pre-fix TotalCost=0 resume would have claimed).
	if arb.TotalCost != 80000 || arb.NetProfit != 60000 {
		t.Fatalf("resumed retry P&L must include attempt 1's cost: want cost=80000 net=60000, got cost=%d net=%d", arb.TotalCost, arb.NetProfit)
	}
}

// Reporting-only guarantee (the RULINGS #4 check the brief asked to verify): PriorAttemptCost
// must NEVER influence a FRESH buy's sizing or guards. Even with a garbage prior value preset,
// a fresh run (empty hold) buys its normal tranche and reports the REAL buy cost, not the
// stale value — the resume counter is accounting, never a spend input.
func TestArbCoordinator_PriorAttemptCost_NeverAffectsFreshBuy(t *testing.T) {
	ship := newTradeHauler(t, "ARB-DKJ7-GUARD") // empty hold → a fresh buy, not a resume
	h, mediator := newArbHandler(ship, nil)

	resp, err := h.Handle(context.Background(), &RunArbCoordinatorCommand{
		ShipSymbol:       ship.ShipSymbol(),
		Good:             trGood,
		BuyAt:            trSource,
		SellAt:           trDest,
		MaxSpend:         50000, // caps the buy to 25u (50000/2000) — must be honored regardless of PriorAttemptCost
		PlayerID:         1,
		PriorAttemptCost: 999999, // garbage stale value; must not leak into the buy
	})
	if err != nil {
		t.Fatalf("fresh run must complete, got error: %v", err)
	}
	arb := arbResponse(t, resp)

	// The --max-spend cap (25u * 2000 = 50000) is honored exactly; PriorAttemptCost did not
	// resize the tranche nor pollute the reported cost.
	if len(mediator.purchases) != 1 || mediator.purchases[0].Units != 25 {
		t.Fatalf("the spend cap must size the buy to 25u independent of PriorAttemptCost, got %+v", mediator.purchases)
	}
	if arb.TotalCost != 50000 {
		t.Fatalf("fresh buy must report the REAL cost (25u * 2000 = 50000), not the preset 999999, got %d", arb.TotalCost)
	}
}
