// Package probebuy holds the guarded probe-purchase decision extracted from the frontier
// expansion coordinator (sp-8w89) so a SECOND standing coordinator — the market-freshness
// auto-sizer (sp-orgp) — reuses the identical money-guard stack instead of re-deriving it.
// The DEMAND signal differs per coordinator (frontier: unmanned coverage slots; freshness:
// aggregate per-system probe sizing) but the SUPPLY-vs-guards decision is the same: buy one
// probe iff demand outruns supply AND every guard passes.
//
// The guards are RULINGS #4/#6 and are NEVER weakened: price <= 25% of LIVE treasury, a
// total-fleet cap, a ledger-derived (restart-safe) purchase cooldown, and a ledger-derived
// per-window spend cap. Every read fails CLOSED — an unreadable treasury, ledger, or price
// means "cannot verify the guard, therefore do not spend". The buyer holds NO mutable state;
// every decision is re-derived from the injected treasury/ledger each call, so a daemon
// restart mid-cooldown re-derives the cooldown from the persisted ledger and never
// double-buys (RULINGS #2). Because the cooldown scopes to ANY probe purchase (from either
// coordinator), the shared ledger also serializes the two coordinators against each other —
// one buying a probe pauses the other for the cooldown, so they cannot collectively over-buy.
package probebuy

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ProbeShipType is the SpaceTraders purchase type for a scout/satellite hull. A purchased
// SHIP_PROBE reports role SATELLITE and satisfies the scout reconciler's manning filter.
const ProbeShipType = "SHIP_PROBE"

// maxTreasuryFractionPercent is the RULINGS #6 hard per-hull ceiling: a probe is bought
// only when its price is at most 25% of LIVE treasury. A deliberate NON-tunable constant.
const maxTreasuryFractionPercent = 25

// TreasuryReader live-reads the player's treasury for the 25% money guard. A nil reader or a
// read error fails the buy CLOSED (no spend). Structurally satisfied by the same
// api-backed reader the frontier coordinator wires (expansion.TreasuryReader).
type TreasuryReader interface {
	LiveCredits(ctx context.Context, playerID shared.PlayerID) (int, error)
}

// ProbePurchaser prices and buys ONE probe through the existing purchase_ship machinery.
// A nil purchaser or any error fails the buy CLOSED. Structurally satisfied by the same
// mediator-backed purchaser the frontier coordinator wires (expansion.ProbePurchaser).
type ProbePurchaser interface {
	QuoteProbe(ctx context.Context, playerID shared.PlayerID) (price int, yard string, err error)
	BuyProbe(ctx context.Context, playerID shared.PlayerID, maxBudget int) (price int, shipSymbol string, err error)
}

// Config carries the four spendable ceilings (RULINGS #5 — every operational value is a
// config key). All are resolved (non-zero) by the caller; the buyer applies them verbatim.
type Config struct {
	MaxProbeFleet    int           // total satellite cap
	MaxSpendPerCycle int           // max probe spend within the trailing spend window
	PurchaseCooldown time.Duration // min wall-clock between probe buys
	SpendWindow      time.Duration // trailing window the spend cap sums over
}

// Outcome is the buyer's decision for the caller's per-cycle summary. Bought is true only
// when a probe was actually purchased (never in dry-run); Reason is a short human string.
type Outcome struct {
	Bought bool
	Reason string
	Price  int
	Symbol string
	Yard   string
}

// GuardedProbeBuyer runs the fail-closed purchase gate stack. It is a stateless value —
// safe to share across players and ticks.
type GuardedProbeBuyer struct {
	treasury  TreasuryReader
	purchaser ProbePurchaser
	ledger    ledger.TransactionRepository
	clock     shared.Clock
	cfg       Config
}

// NewGuardedProbeBuyer wires the buyer. clock defaults to the real clock when nil.
func NewGuardedProbeBuyer(treasury TreasuryReader, purchaser ProbePurchaser, ledgerRepo ledger.TransactionRepository, clock shared.Clock, cfg Config) *GuardedProbeBuyer {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &GuardedProbeBuyer{treasury: treasury, purchaser: purchaser, ledger: ledgerRepo, clock: clock, cfg: cfg}
}

// MaybeBuy buys exactly one probe iff demand outruns supply and every guard passes (or, in
// dryRun, reports what it WOULD do without spending). The gate ORDER is cheapest-first: the
// no-I/O checks (demand, fleet cap) precede the ledger/treasury/API reads, so a no-purchase
// cycle rarely touches the network.
func (b *GuardedProbeBuyer) MaybeBuy(ctx context.Context, playerID shared.PlayerID, demand, supply int, dryRun bool) Outcome {
	// Fleet short? If supply already covers demand, an existing probe serves it — buying
	// would over-provision (the sp-njwy over-buy: supply counts idle + in-flight + manning).
	if supply >= demand {
		return Outcome{Reason: fmt.Sprintf("no purchase: supply covers demand (%d supply >= %d demand)", supply, demand)}
	}

	// Fleet cap (RULINGS #5 ceiling): never grow the satellite fleet past the cap.
	if supply >= b.cfg.MaxProbeFleet {
		return Outcome{Reason: fmt.Sprintf("no purchase: fleet cap reached (%d/%d satellites)", supply, b.cfg.MaxProbeFleet)}
	}

	// Cooldown (ledger-derived, restart-safe): at most one probe buy per cooldown, scoped
	// to probe buys from ANY source so the two coordinators never collectively double-buy.
	last, hadLast, err := b.lastProbePurchase(ctx, playerID)
	if err != nil {
		return Outcome{Reason: fmt.Sprintf("no purchase: purchase ledger unreadable (fail-closed): %v", err)}
	}
	if hadLast {
		if elapsed := b.clock.Now().Sub(last); elapsed < b.cfg.PurchaseCooldown {
			return Outcome{Reason: fmt.Sprintf("no purchase: cooldown active (%s since last probe, need %s)", elapsed.Round(time.Second), b.cfg.PurchaseCooldown)}
		}
	}

	// Treasury (RULINGS #4/#6): cannot read the live balance → do not spend.
	if b.treasury == nil {
		return Outcome{Reason: "no purchase: no treasury reader wired (fail-closed)"}
	}
	credits, err := b.treasury.LiveCredits(ctx, playerID)
	if err != nil {
		return Outcome{Reason: fmt.Sprintf("no purchase: treasury unreadable (fail-closed): %v", err)}
	}

	// Price quote (RULINGS #4): cannot price the hull → do not spend.
	if b.purchaser == nil {
		return Outcome{Reason: "no purchase: no purchaser wired (fail-closed)"}
	}
	price, yard, err := b.purchaser.QuoteProbe(ctx, playerID)
	if err != nil {
		return Outcome{Reason: fmt.Sprintf("no purchase: probe unpriceable (fail-closed): %v", err)}
	}

	// 25% rule (RULINGS #6): integer form price*100 > credits*25 avoids float rounding.
	if price*100 > credits*maxTreasuryFractionPercent {
		return Outcome{Reason: fmt.Sprintf("no purchase: probe price %d exceeds %d%% of treasury %d", price, maxTreasuryFractionPercent, credits)}
	}

	// Per-window spend cap (RULINGS #5 ceiling, ledger-derived).
	windowSpend, err := b.probeSpendSince(ctx, playerID, b.clock.Now().Add(-b.cfg.SpendWindow))
	if err != nil {
		return Outcome{Reason: fmt.Sprintf("no purchase: spend ledger unreadable (fail-closed): %v", err)}
	}
	if windowSpend+price > b.cfg.MaxSpendPerCycle {
		return Outcome{Reason: fmt.Sprintf("no purchase: spend cap (window %d + price %d > %d)", windowSpend, price, b.cfg.MaxSpendPerCycle)}
	}

	// The hard MaxBudget handed to the buy is the 25% treasury ceiling — a slight price
	// move up to (never past) the line still fills (RULINGS #6).
	treasuryCap := credits * maxTreasuryFractionPercent / 100

	if dryRun {
		return Outcome{Reason: fmt.Sprintf("would buy probe at %s for ~%d (dry-run)", yard, price), Price: price, Yard: yard}
	}

	paid, sym, err := b.purchaser.BuyProbe(ctx, playerID, treasuryCap)
	if err != nil {
		return Outcome{Reason: fmt.Sprintf("no purchase: buy failed (fail-closed): %v", err)}
	}
	return Outcome{Bought: true, Reason: fmt.Sprintf("bought probe %s for %d at %s", sym, paid, yard), Price: paid, Symbol: sym, Yard: yard}
}

// lastProbePurchase returns the timestamp of the most recent SHIP_PROBE purchase, derived
// from the persisted transactions ledger (RULINGS #2: the cooldown clock survives a restart
// because it is READ from the ledger, not held in memory).
func (b *GuardedProbeBuyer) lastProbePurchase(ctx context.Context, playerID shared.PlayerID) (time.Time, bool, error) {
	ps := ledger.TransactionTypePurchaseShip
	txns, err := b.ledger.FindByPlayer(ctx, playerID, ledger.QueryOptions{
		TransactionType: &ps,
		OrderBy:         "timestamp DESC",
		Limit:           50,
	})
	if err != nil {
		return time.Time{}, false, err
	}
	for _, t := range txns {
		if isProbePurchase(t) {
			return t.Timestamp(), true, nil
		}
	}
	return time.Time{}, false, nil
}

// probeSpendSince sums probe purchase spend booked since `since`, derived from the ledger.
// Amounts are stored negative (expenses), so spend is the negated sum.
func (b *GuardedProbeBuyer) probeSpendSince(ctx context.Context, playerID shared.PlayerID, since time.Time) (int, error) {
	ps := ledger.TransactionTypePurchaseShip
	txns, err := b.ledger.FindByPlayer(ctx, playerID, ledger.QueryOptions{
		TransactionType: &ps,
		StartDate:       &since,
		Limit:           500,
	})
	if err != nil {
		return 0, err
	}
	sum := 0
	for _, t := range txns {
		if isProbePurchase(t) {
			sum += -t.Amount()
		}
	}
	return sum, nil
}

// isProbePurchase reports whether a PURCHASE_SHIP transaction bought a probe, read from the
// metadata ship_type the purchase machinery stamps.
func isProbePurchase(t *ledger.Transaction) bool {
	st, _ := t.Metadata()["ship_type"].(string)
	return st == ProbeShipType
}
