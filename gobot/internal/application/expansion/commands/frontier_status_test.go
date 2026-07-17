package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-pvw3 `frontier status`: the read-only live-state query. Driven through the Status method on the
// coordinator handler with fakes at the ports, asserting the assembled view — split + degradation
// note, HONEST dark-market backlog count, probe allocation, last buy, blockers.

// COVERAGE-GAP test (a): the status backlog count is the HONEST dark set — it counts a
// charted-with-marketplaces-but-unscanned system EVEN WHEN it already carries a post (covered), so it
// reflects the whole dark frontier, not the coverage-excluded internal queue the scan side declares
// from. Here a covered dark system is counted by status (2 systems) but dropped by the internal queue
// (1) — proving the count is broader than the old queue.
func TestFrontierStatus_HonestBacklogCountExceedsCoverageExcludedQueue(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-DARK-COVERED", Kind: domainScouting.PostKindSweepOnce}, // covered but unscanned
	}}
	fr := &fakeFleetRepo{}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetDarkMarketScanner(&fakeDarkScanner{candidates: []ScanCandidate{
		{SystemSymbol: "X1-DARK-COVERED", MarketCount: 5}, // has a post, still dark (unscanned)
		{SystemSymbol: "X1-DARK-NEW", MarketCount: 3},
	}})

	view, err := h.Status(context.Background(), testCmd())
	require.NoError(t, err)

	require.Equal(t, 2, view.DarkSystems, "the honest backlog counts every dark system, including covered-but-unscanned")
	require.Equal(t, 8, view.DarkMarketplaces, "unscanned marketplace count sums across the whole dark set (5+3)")

	// The internal (coverage-excluded) scan queue drops the covered system — the count status must exceed.
	internalQueue := h.buildScanBacklog(context.Background(), testCmd(), pr.posts)
	require.Len(t, internalQueue, 1, "the internal queue excludes the covered system; status must not")
}

// The effective split summary matches the reconcile's actual budgets, including graceful degradation.
func TestFrontierStatus_SplitSummaryReflectsShareAndDegradation(t *testing.T) {
	richVirgin := []ExpansionCandidate{{SystemSymbol: "X1-VIRGIN", Hops: 1, KnownMarkets: 5, Charted: true}}
	richBacklog := []ScanCandidate{{SystemSymbol: "X1-DARK", MarketCount: 4}}

	cases := []struct {
		name    string
		virgin  []ExpansionCandidate
		backlog []ScanCandidate
		want    string
	}{
		{"balanced concurrent split", richVirgin, richBacklog, "60% discover / 40% scan"},
		{"backlog empty → all discovery", richVirgin, nil, "100% discover — dark-market backlog empty (scan share redirected)"},
		{"no virgin → all scan", nil, richBacklog, "100% scan — no reachable virgin frontier (discovery share redirected)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &shared.MockClock{CurrentTime: time.Now()}
			h := newHandler(&fakePostRepo{}, &fakeFleetRepo{}, &fakeLedgerRepo{}, clock)
			h.SetExpansionScanner(&fakeScanner{candidates: tc.virgin})
			h.SetDarkMarketScanner(&fakeDarkScanner{candidates: tc.backlog})

			cmd := testCmd()
			cmd.DiscoveryShare = 60

			view, err := h.Status(context.Background(), cmd)
			require.NoError(t, err)
			require.Equal(t, tc.want, view.SplitSummary)
			require.Equal(t, 60, view.DiscoveryShare)
			require.Equal(t, 40, view.ScanShare)
		})
	}
}

// Status reports probe allocation, posts in flight, the virgin-queue depth, and the read-only blockers.
func TestFrontierStatus_ReportsAllocationDepthAndBlockers(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindSweepOnce},
		{PlayerID: 1, SystemSymbol: "X1-B", Kind: domainScouting.PostKindSweepOnce},
	}}
	idle := []*navigation.Ship{newProbe(t, "P1", "X1-Z-1")}
	all := []*navigation.Ship{newProbe(t, "P1", "X1-Z-1"), newProbe(t, "P2", "X1-Z-2"), newProbe(t, "P3", "X1-Z-3")}
	fr := &fakeFleetRepo{idle: idle, all: all}
	h := newHandler(pr, fr, &fakeLedgerRepo{}, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-V1", Hops: 1, KnownMarkets: 2, Charted: true},
		{SystemSymbol: "X1-V2", Hops: 1, KnownMarkets: 3, Charted: true},
	}})
	// No treasury reader and no purchaser wired → both are read-only fail-closed blockers.

	cmd := testCmd()
	cmd.MaxProbeFleet = 40

	view, err := h.Status(context.Background(), cmd)
	require.NoError(t, err)

	require.Equal(t, 3, view.ProbeFleet, "total satellites owned")
	require.Equal(t, 40, view.ProbeCap)
	require.Equal(t, 1, view.ProbesIdle, "idle relayable probes")
	require.Equal(t, 2, view.PostsInFlight, "outstanding sweep-once posts")
	require.Equal(t, 2, view.VirginQueueDepth, "reachable uncovered virgin frontier systems")
	require.Contains(t, view.Blockers, "no treasury reader wired — buys fail closed")
	require.Contains(t, view.Blockers, "no probe purchaser wired — buys fail closed")
}

// Status reports the last probe buy (price + age) from the persisted ledger, and flags the cooldown.
func TestFrontierStatus_ReportsLastProbeBuyAndCooldownBlocker(t *testing.T) {
	now := time.Now()
	clock := &shared.MockClock{CurrentTime: now}
	pr := &fakePostRepo{}
	fr := &fakeFleetRepo{}
	lr := &fakeLedgerRepo{txns: []*ledger.Transaction{probeTxn(t, now.Add(-2*time.Minute), 25000)}}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	h.SetProbePurchaser(&fakePurchaser{quotePrice: 20000, quoteYard: "X1-HOME-SY", buySymbol: "NEW"})

	cmd := testCmd()
	cmd.PurchaseCooldownSecs = int((10 * time.Minute).Seconds()) // 10m cooldown; last buy 2m ago

	view, err := h.Status(context.Background(), cmd)
	require.NoError(t, err)

	require.Equal(t, 25000, view.LastBuyPrice, "last probe buy price from the ledger")
	require.InDelta(t, 120, view.LastBuyAgeSeconds, 2, "seconds since the last probe buy")
	require.Contains(t, view.Blockers[0], "purchase cooldown active", "the ledger-derived cooldown is a live blocker")
}
