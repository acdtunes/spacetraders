package probebuy

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- fakes -----------------------------------------------------------------

type fakeTreasury struct {
	credits int
	err     error
}

func (f *fakeTreasury) LiveCredits(_ context.Context, _ shared.PlayerID) (int, error) {
	return f.credits, f.err
}

type fakePurchaser struct {
	quotePrice int
	quoteYard  string
	quoteErr   error
	buySymbol  string
	buyErr     error

	quoteCalls int
	buyCalls   int
	lastBudget int
	lastTarget ProbeTarget
}

func (f *fakePurchaser) QuoteProbe(_ context.Context, _ shared.PlayerID, target ProbeTarget) (int, string, error) {
	f.quoteCalls++
	f.lastTarget = target
	return f.quotePrice, f.quoteYard, f.quoteErr
}

func (f *fakePurchaser) BuyProbe(_ context.Context, _ shared.PlayerID, maxBudget int, target ProbeTarget) (int, string, error) {
	f.buyCalls++
	f.lastBudget = maxBudget
	f.lastTarget = target
	if f.buyErr != nil {
		return 0, "", f.buyErr
	}
	return f.quotePrice, f.buySymbol, nil
}

// noTarget is the home-yard (no demand-proximal hint) purchase path.
var noTarget = ProbeTarget{}

// fakeLedger mimics the GORM transaction repo's read semantics used by the buyer:
// StartDate filtering, timestamp-DESC ordering, and Limit — so cooldown and
// spend-window derivations behave as they would against the real store.
type fakeLedger struct {
	txns []*ledger.Transaction
	err  error
}

func (f *fakeLedger) Create(_ context.Context, _ *ledger.Transaction) error { return nil }
func (f *fakeLedger) FindByID(_ context.Context, _ ledger.TransactionID, _ shared.PlayerID) (*ledger.Transaction, error) {
	return nil, nil
}
func (f *fakeLedger) CountByPlayer(_ context.Context, _ shared.PlayerID, _ ledger.QueryOptions) (int, error) {
	return len(f.txns), nil
}
func (f *fakeLedger) FindByPlayer(_ context.Context, _ shared.PlayerID, opts ledger.QueryOptions) ([]*ledger.Transaction, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]*ledger.Transaction, 0, len(f.txns))
	for _, t := range f.txns {
		if opts.StartDate != nil && t.Timestamp().Before(*opts.StartDate) {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp().After(out[j].Timestamp()) })
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

func probeTxn(t *testing.T, ts time.Time, price int) *ledger.Transaction {
	t.Helper()
	tx, err := ledger.NewTransaction(
		shared.MustNewPlayerID(1), ts, ledger.TransactionTypePurchaseShip,
		-price, price+10, 10, "Purchased SHIP_PROBE",
		map[string]interface{}{"ship_type": ProbeShipType}, "", "", "freshness sizer",
	)
	require.NoError(t, err)
	return tx
}

func testConfig() Config {
	return Config{
		MaxProbeFleet:    40,
		MaxSpendPerCycle: 100000,
		PurchaseCooldown: 10 * time.Minute,
		SpendWindow:      1 * time.Hour,
	}
}

// newBuyer wires a buyer with all collaborators; individual tests nil out or override
// what they exercise.
func newBuyer(tr TreasuryReader, pu ProbePurchaser, lr ledger.TransactionRepository, clock shared.Clock) *GuardedProbeBuyer {
	return NewGuardedProbeBuyer(tr, pu, lr, clock, testConfig())
}

// ---- tests -----------------------------------------------------------------

// The canonical buy: demand outruns supply and every money guard passes, so exactly one
// probe is purchased with the 25%-of-treasury ceiling as the hard budget.
func TestBuys_WhenDemandExceedsSupplyAndGuardsPass(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	tr := &fakeTreasury{credits: 1000} // 25% ceiling = 250
	pu := &fakePurchaser{quotePrice: 200, quoteYard: "X1-HQ-YARD", buySymbol: "PROBE-NEW"}
	b := newBuyer(tr, pu, &fakeLedger{}, clock)

	// The demand-proximal target rides the guard stack through to the purchaser unchanged
	// (sp-hej4): the buyer guards, then hands the yard hint to the buy — SELECTION only.
	target := ProbeTarget{System: "X1-FRONTIER", HopPenaltyCredits: 50_000}
	out := b.MaybeBuy(context.Background(), shared.MustNewPlayerID(1), 5 /*demand*/, 3 /*supply*/, false, target)

	require.True(t, out.Bought, "a covered-guards buy should happen (%s)", out.Reason)
	require.Equal(t, 1, pu.buyCalls, "exactly one probe bought")
	require.Equal(t, "PROBE-NEW", out.Symbol)
	require.Equal(t, 250, pu.lastBudget, "hard budget is the 25%% treasury ceiling")
	require.Equal(t, target, pu.lastTarget, "the demand-proximal target reaches the buy unchanged")
}

// Supply already covers demand: the reconciler will relay an existing probe, so buying
// would over-provision (the sp-njwy over-buy the coordinator must never make).
func TestNoBuy_WhenSupplyCoversDemand(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pu := &fakePurchaser{quotePrice: 200}
	b := newBuyer(&fakeTreasury{credits: 1000}, pu, &fakeLedger{}, clock)

	out := b.MaybeBuy(context.Background(), shared.MustNewPlayerID(1), 3 /*demand*/, 3 /*supply*/, false, noTarget)

	require.False(t, out.Bought)
	require.Equal(t, 0, pu.buyCalls, "no purchase when supply covers demand")
}

// The money guards each fail the buy CLOSED. One parametrized table because every row is
// the same behavior — "a failing guard blocks the buy" — with a different guard tripped.
// sp-hej4 scenario 5: a demand-proximal target is set on every row, proving target-aware yard
// SELECTION never weakens the guard stack — an over-25% buy (and every other tripped guard) is
// still refused with a target present, exactly as on the home-yard path.
func TestNoBuy_WhenAGuardFails(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name        string
		credits     int
		quotePrice  int
		treasuryErr error
		quoteErr    error
		supply      int
		recentBuy   *time.Time // a probe purchase this long-ago trips the cooldown
		priorSpend  int        // a prior in-window probe spend to trip the spend cap
	}{
		// price 300 > 25% of 1000 (=250) → treasury guard.
		{name: "price exceeds 25pct of treasury", credits: 1000, quotePrice: 300, supply: 0},
		// supply 40 == MaxProbeFleet → fleet-cap guard.
		{name: "fleet cap reached", credits: 100000, quotePrice: 200, supply: 40},
		// a probe bought 1 minute ago, cooldown is 10min → cooldown guard.
		{name: "cooldown active", credits: 100000, quotePrice: 200, supply: 0, recentBuy: ptr(now.Add(-1 * time.Minute))},
		// 99900 already spent in-window + 200 price > 100000 cap → spend-window guard.
		{name: "spend window cap", credits: 100000, quotePrice: 200, supply: 0, priorSpend: 99900},
		// treasury unreadable → fail closed.
		{name: "treasury unreadable", credits: 0, quotePrice: 200, supply: 0, treasuryErr: errors.New("api down")},
		// probe unpriceable → fail closed.
		{name: "probe unpriceable", credits: 100000, quotePrice: 0, supply: 0, quoteErr: errors.New("no yard")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &shared.MockClock{CurrentTime: now}
			pu := &fakePurchaser{quotePrice: tc.quotePrice, quoteYard: "Y", quoteErr: tc.quoteErr, buySymbol: "P"}
			lr := &fakeLedger{}
			if tc.recentBuy != nil {
				lr.txns = append(lr.txns, probeTxn(t, *tc.recentBuy, 200))
			}
			if tc.priorSpend > 0 {
				lr.txns = append(lr.txns, probeTxn(t, now.Add(-2*time.Minute), tc.priorSpend))
			}
			b := newBuyer(&fakeTreasury{credits: tc.credits, err: tc.treasuryErr}, pu, lr, clock)

			target := ProbeTarget{System: "X1-FRONTIER", HopPenaltyCredits: 50_000}
			out := b.MaybeBuy(context.Background(), shared.MustNewPlayerID(1), 10 /*demand*/, tc.supply, false, target)

			require.False(t, out.Bought, "guard should block the buy")
			require.Equal(t, 0, pu.buyCalls, "no purchase when a guard fails (%s)", out.Reason)
		})
	}
}

// Dry-run evaluates every guard and reports the intent but spends nothing (the captain
// watches a cycle before arming it).
func TestDryRun_EvaluatesButDoesNotBuy(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pu := &fakePurchaser{quotePrice: 200, quoteYard: "Y", buySymbol: "P"}
	b := newBuyer(&fakeTreasury{credits: 100000}, pu, &fakeLedger{}, clock)

	out := b.MaybeBuy(context.Background(), shared.MustNewPlayerID(1), 5, 0, true /*dryRun*/, noTarget)

	require.False(t, out.Bought)
	require.Equal(t, 0, pu.buyCalls, "dry-run never buys")
	require.Contains(t, out.Reason, "would buy")
}

// sp-3u5d per-unit price ceiling: the BACKSTOP for the deepest-frontier tail whose only reachable
// yard is a depleted deep one. QuoteProbe has ALREADY run the sibling-spread and returns the FINAL
// chosen quote; the buyer gates THAT price against the ceiling. One parametrized table because every
// row is the same behavior — "buy iff the ceiling is disabled OR the final quote is within it" — with
// treasury 10M and spend cap 5M so ONLY the ceiling can decide the over-priced row (the mutation
// guard: delete the check and the 235k quote passes every other guard and wrongly buys).
func TestDefers_WhenFinalQuoteExceedsPriceCeiling(t *testing.T) {
	highCapConfig := Config{
		MaxProbeFleet:    40,
		MaxSpendPerCycle: 5_000_000, // far above any quote, so the ceiling — not the spend cap — decides
		PurchaseCooldown: 10 * time.Minute,
		SpendWindow:      1 * time.Hour,
	}
	cases := []struct {
		name    string
		ceiling int
		quote   int
		wantBuy bool
	}{
		{name: "ceiling disabled (0) buys at any price — byte-identical to pre-sp-3u5d", ceiling: 0, quote: 235_000, wantBuy: true},
		{name: "quote under ceiling buys", ceiling: 60_000, quote: 23_000, wantBuy: true},
		{name: "quote over ceiling defers", ceiling: 60_000, quote: 235_000, wantBuy: false},
		// After sibling-spread QuoteProbe returns the cheaper reachable sibling (45k), which is under
		// the ceiling → the buy proceeds; only the FINAL best quote is what the ceiling gates.
		{name: "post-sibling-spread sibling under ceiling buys", ceiling: 60_000, quote: 45_000, wantBuy: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &shared.MockClock{CurrentTime: time.Now()}
			tr := &fakeTreasury{credits: 10_000_000} // 25% ceiling = 2.5M, above every quote here
			pu := &fakePurchaser{quotePrice: tc.quote, quoteYard: "X1-DEEP-YARD", buySymbol: "PROBE-NEW"}
			b := NewGuardedProbeBuyer(tr, pu, &fakeLedger{}, clock, highCapConfig)

			target := ProbeTarget{System: "X1-FRONTIER", HopPenaltyCredits: 50_000, SiblingPriceMarginCredits: 30_000, MaxProbePriceCredits: tc.ceiling}
			out := b.MaybeBuy(context.Background(), shared.MustNewPlayerID(1), 5 /*demand*/, 3 /*supply*/, false, target)

			if tc.wantBuy {
				require.True(t, out.Bought, "quote %d within ceiling %d should buy (%s)", tc.quote, tc.ceiling, out.Reason)
				require.Equal(t, 1, pu.buyCalls, "exactly one probe bought")
				return
			}
			require.False(t, out.Bought, "quote %d over ceiling %d must defer (%s)", tc.quote, tc.ceiling, out.Reason)
			require.Zero(t, pu.buyCalls, "an over-ceiling quote never buys")
			// A clear, mirrored "no purchase: ..." defer reason carrying the price, ceiling, and yard.
			require.Contains(t, out.Reason, "235000", "the defer reason states the offending price")
			require.Contains(t, out.Reason, "60000", "the defer reason states the ceiling")
			require.Contains(t, out.Reason, "X1-DEEP-YARD", "the defer reason states the yard")
		})
	}
}

func ptr[T any](v T) *T { return &v }
