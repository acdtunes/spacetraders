package expansion

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- price-impact test doubles (sp-4m4ve Phase 3 §2D) ----------------------
//
// probeFakeLedger mimics the GORM transaction repo's read semantics the impact term relies on
// (StartDate filtering, timestamp-DESC ordering, Limit) — the SAME persisted-ledger contract
// recentBuyImpact reads through, so the derivation behaves as it would against the real store.

type probeFakeLedger struct {
	txns []*ledger.Transaction
	err  error
}

func (f *probeFakeLedger) Create(_ context.Context, _ *ledger.Transaction) error { return nil }
func (f *probeFakeLedger) FindByID(_ context.Context, _ ledger.TransactionID, _ shared.PlayerID) (*ledger.Transaction, error) {
	return nil, nil
}
func (f *probeFakeLedger) CountByPlayer(_ context.Context, _ shared.PlayerID, _ ledger.QueryOptions) (int, error) {
	return len(f.txns), nil
}
func (f *probeFakeLedger) FindByPlayer(_ context.Context, _ shared.PlayerID, opts ledger.QueryOptions) ([]*ledger.Transaction, error) {
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

// probeBuyTxn builds a persisted PURCHASE_SHIP(SHIP_PROBE) transaction at waypoint, timestamped
// ts — the exact shape recordShipPurchaseTransaction (purchase_ship.go) writes, so the impact
// term's ledger read matches production metadata (ship_type + waypoint keys).
func probeBuyTxn(t *testing.T, ts time.Time, waypoint string, price int) *ledger.Transaction {
	t.Helper()
	tx, err := ledger.NewTransaction(
		shared.MustNewPlayerID(1), ts, ledger.TransactionTypePurchaseShip,
		-price, price+10, 10, "Purchased SHIP_PROBE ship at "+waypoint,
		map[string]interface{}{"ship_type": probeShipType, "waypoint": waypoint},
		"", "", "fleet expansion",
	)
	require.NoError(t, err)
	return tx
}

// ---- tests -----------------------------------------------------------------
//
// Reproduces the live-observed pathology directly: a "hammered" yard whose scanned price is
// still stale-cheap (the scan hasn't caught its climb yet) but which this player has bought at
// repeatedly per the PERSISTED ledger, against a sibling that is slightly cheaper on scan but one
// hop further out (so, absent the impact term, the hammered yard's proximity still wins).
// SiblingPriceMarginCredits is 0 — the pre-existing sibling-spread override is disabled so only
// the price-impact term under test decides the ranking.
//
// The term is UNCONDITIONAL (sp-4m4ve Phase 3 graduated — no flag, no off path): every
// ProbePurchaser applies it at the fixed rate (estImpactPerBuyCredits per recent buy, within
// impactDecayWindow) by construction. N recent buys at the hammered yard, all within the decay
// window, inflate its effective cost past the (further, but scan-cheaper) sibling's — selection
// rotates PROACTIVELY, before the next re-scan would ever reflect the climb. Buys older than the
// window drop out and the hop-penalty ranking decides alone — parametrized as one behavior
// (Mandate 5): "does a recent buy count toward the (always-on) impact term".
func TestQuoteProbe_RecentBuyImpact_RotatesYardByDefault(t *testing.T) {
	candidates := []shipyardQueries.YardCandidate{
		yard("X1-HAMMERED-YD", "X1-HAMMERED", 0, 21_000), // stale scan — hasn't caught the climb yet
		yard("X1-SIBLING-YD", "X1-SIBLING", 1, 20_900),   // slightly cheaper on scan, but 1 hop further
	}
	now := time.Now()
	cases := []struct {
		name        string
		buyAges     []time.Duration // ages (before now) of recent buys at the hammered yard
		expectYard  string
		expectPrice int
	}{
		{
			name:        "recent buys within the decay window rotate to the cheaper sibling",
			buyAges:     []time.Duration{10 * time.Minute, 20 * time.Minute, 30 * time.Minute},
			expectYard:  "X1-SIBLING-YD",
			expectPrice: 20_900,
		},
		{
			name:        "the same buys, all OLDER than the decay window, do not rotate",
			buyAges:     []time.Duration{3 * time.Hour, 4 * time.Hour, 5 * time.Hour},
			expectYard:  "X1-HAMMERED-YD",
			expectPrice: 21_000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var txns []*ledger.Transaction
			for _, age := range tc.buyAges {
				txns = append(txns, probeBuyTxn(t, now.Add(-age), "X1-HAMMERED-YD", 20_000))
			}
			med := &probeFakeMediator{listings: map[string]int{"X1-HOME-YD": 25_000}}
			ships := &probeFakeShipRepo{idle: []*navigation.Ship{probeShip(t, "BUYER-1", "X1-HOME-YD")}}
			finder := &probeFakeYardFinder{candidates: candidates}
			p := NewProbePurchaser(med, ships, finder, &probeFakeLedger{txns: txns}, &shared.MockClock{CurrentTime: now})

			target := probebuy.ProbeTarget{System: "X1-DEST", HopPenaltyCredits: 500, SiblingPriceMarginCredits: 0}
			price, gotYard, err := p.QuoteProbe(context.Background(), shared.MustNewPlayerID(1), target)

			require.NoError(t, err)
			require.Equal(t, tc.expectYard, gotYard)
			require.Equal(t, tc.expectPrice, price, "the quoted price is always the yard's REAL scanned price, never the inflated ranking number")
		})
	}
}
