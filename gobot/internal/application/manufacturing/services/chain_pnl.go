package services

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// Per-good realized-P&L math for the factory kill-switch. Worker-level accounting cannot see a
// hidden loser, because a chain's value flows out through tours rather than through the worker;
// this is the chain-level view the coordinator judges. It mirrors the per-good manufacturing
// dashboard panel Go-side and adds the lift-cost approximation the dashboard omits. The raw ledger
// aggregates it consumes (manufacturing.ChainPnLRaw) live in the domain package so the DB reader
// can produce them without importing this service.
//
// Per output good over a rolling window:
//
//	realized P&L = factory local sells + tour/manual realized net   (realized output value)
//	             + factory input buys                                (input cost, stored NEGATIVE)
//	             − lift cost                                         (chain workers' refuel, approx.)
//
// ATTRIBUTION IS STRICTLY PER-GOOD, NOT PER-CHAIN. transactions.metadata->>'good_symbol' tags the
// good LITERALLY transacted, and the schema carries no task-level input->output linkage, so a
// PURCHASE_CARGO of an input is tagged with the INPUT and never rolled up. A vertically-integrated
// output good therefore reads as a strong earner while its input good carries the input spend.
//
// LIFT COST IS AN APPROXIMATION. Refuel transactions carry no good_symbol and the worker pool is
// SHARED and ROTATING across chains, so exact per-chain lift is unknowable from the ledger. The
// window's manufacturing+factory REFUEL pool is attributed per good in proportion to |factory input
// spend| — a production-activity proxy that lands lift on the good whose PURCHASE drove the haul.
// It is a minor term against realized flows and cannot materially move a kill verdict. A good with
// zero input spend attracts zero lift; the pool is dropped rather than fabricated onto tour-only
// goods.

// ChainPnLResult is one good's computed realized P&L over the window. Every component is exposed so
// the kill verdict can put the numbers in its log TEXT (the container-log renderer drops metadata)
// and the dashboard can reconcile against its own panel.
type ChainPnLResult struct {
	Good             string
	FactorySell      int     // realized factory local sells (+)
	TourNet          int     // realized tour/manual net (signed)
	FactoryInputCost int     // factory input buys (negative)
	LiftCost         int     // attributed refuel/lift (>= 0), the approximation
	Net              int     // FactorySell + TourNet + FactoryInputCost − LiftCost
	WindowHours      float64 // the window the net is spread over
	NetPerHour       float64 // Net / WindowHours (0 when WindowHours <= 0)
	// HasRealization is true iff realized OUTPUT value (FactorySell + max(0, TourNet)) > 0.
	// The kill-switch reads this to fail OPEN on a pre-realization chain: realization lags
	// production, so a chain that has bought inputs but not yet sold anything has no P&L
	// signal to judge — killing it would only churn kill/resume until realization arrives.
	HasRealization bool
}

// ChainPnLReader supplies the raw realized-P&L aggregates for the manufacturing/factory operation
// over a trailing window. Narrow by design: the kill-switch needs only the per-good realized
// cashflow rows plus the operation's refuel pool, not a general ledger API. A nil reader disables
// the kill-switch (fail-OPEN), the optional-port contract the coordinator's fixtures rely on. The
// daemon wires the DB-backed repository via SetChainPnLReader; it satisfies this interface
// structurally against the domain types, so persistence need not import this package.
type ChainPnLReader interface {
	ReadRealizedPnL(ctx context.Context, playerID int, since time.Time) (manufacturing.ChainPnLRaw, error)
}

// ComputeChainPnL turns the raw ledger aggregates into per-good realized P&L over windowHours,
// keyed by good. Pure (no I/O) so the math is pinned from fixture rows independent of the DB
// reader. See the file header for the attribution and lift-cost rules.
func ComputeChainPnL(raw manufacturing.ChainPnLRaw, windowHours float64) map[string]ChainPnLResult {
	// Total input spend is the denominator for the proportional lift split.
	var totalInputSpend int64
	for _, g := range raw.Goods {
		totalInputSpend += int64(absInt(g.FactoryCost))
	}
	liftPool := int64(absInt(raw.RefuelPool))

	out := make(map[string]ChainPnLResult, len(raw.Goods))
	for _, g := range raw.Goods {
		// Proportional lift, integer math with an int64 intermediate to avoid overflow on
		// million-credit spends. Zero total input spend -> zero lift (pool dropped).
		lift := 0
		if totalInputSpend > 0 {
			lift = int(liftPool * int64(absInt(g.FactoryCost)) / totalInputSpend)
		}

		net := g.FactorySell + g.TourNet + g.FactoryCost - lift

		perHour := 0.0
		if windowHours > 0 {
			perHour = float64(net) / windowHours
		}

		realizedOutput := g.FactorySell
		if g.TourNet > 0 {
			realizedOutput += g.TourNet
		}

		out[g.Good] = ChainPnLResult{
			Good:             g.Good,
			FactorySell:      g.FactorySell,
			TourNet:          g.TourNet,
			FactoryInputCost: g.FactoryCost,
			LiftCost:         lift,
			Net:              net,
			WindowHours:      windowHours,
			NetPerHour:       perHour,
			HasRealization:   realizedOutput > 0,
		}
	}
	return out
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
