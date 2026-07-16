package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-jide scan_only knob: decouple SCANNING the discovered-market backlog from EXPANDING to
// virgin. These tests drive the coordinator through its ReconcileOnce driving port and assert
// observable outcomes at the ScoutPostRepository (declared posts) and the ProbePurchaser
// boundary (buys), never internal structure.

// fakeDarkScanner is the DarkMarketScanner port double: it returns a fixed FULL charted-unscanned
// market backlog (the discovered "dark" set — NOT the hop-bounded expansion frontier), so the
// scan_only tests pin exactly which systems the coordinator sweeps.
type fakeDarkScanner struct {
	candidates []ScanCandidate
	err        error
	calls      int
}

func (f *fakeDarkScanner) ChartedUnscannedMarketSystems(_ context.Context, _ int) ([]ScanCandidate, error) {
	f.calls++
	return f.candidates, f.err
}

// scan_only=1 core behavior: the coordinator declares a breadth sweep-once post for EVERY
// uncovered charted-unscanned MARKET system (ranked by market count, an already-covered dark
// system excluded), while declaring NO depth pathfinder and buying NO probe — even though this
// exact fixture (empty posts + zero idle probes + treasury & purchaser wired) BUYS in the normal
// expansion path (see TestFrontier_NoScanner_ServesSlotDemandOnly / DryRun_ActsOnNothing). It
// pins requirements (i) no depth posts, (ii) no probe buy, (iii) sweep posts for the full dark set.
func TestFrontierScanOnly_DeclaresSweepForEveryDarkSystem_DeclaresNoDepth_BuysNoProbe(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{
		standingPost("X1-COVERED", "P9"), // a dark system already covered → must NOT be re-declared
	}}
	fr := &fakeFleetRepo{} // zero idle probes → the normal path would look short and BUY
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 20000, quoteYard: "X1-HOME-SY", buySymbol: "PROBE-NEW", buyPrice: 20000}
	h.SetProbePurchaser(buyer)
	// The NORMAL expansion scanner offers a depth-eligible deep virgin AND a market-rich breadth
	// head — scan_only must consult NEITHER (no expansion, no depth).
	normal := &fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-VIRGIN-DEEP", Hops: 4, KnownMarkets: 0, Charted: false}, // depth pathfinder target
		{SystemSymbol: "X1-BREADTH", Hops: 1, KnownMarkets: 9, Charted: true},      // breadth ranker head
	}}
	h.SetExpansionScanner(normal)
	// The FULL discovered dark backlog (ranked by market count; X1-COVERED already has a post).
	dark := &fakeDarkScanner{candidates: []ScanCandidate{
		{SystemSymbol: "X1-DARK-A", MarketCount: 3},
		{SystemSymbol: "X1-DARK-B", MarketCount: 14}, // highest count → chosen head
		{SystemSymbol: "X1-COVERED", MarketCount: 5}, // excluded: already posted
	}}
	h.SetDarkMarketScanner(dark)

	cmd := testCmd()
	cmd.ScanOnly = 1

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	declared := map[string]*domainScouting.ScoutPost{}
	for _, post := range pr.upserts {
		declared[post.SystemSymbol] = post
	}
	// (iii) one sweep-once post per UNCOVERED dark system — the full backlog, covered one excluded.
	require.Len(t, pr.upserts, 2, "a sweep-once post per uncovered dark-market system (X1-COVERED excluded)")
	require.Contains(t, declared, "X1-DARK-A", "the full dark backlog is swept, not just the top one")
	require.Contains(t, declared, "X1-DARK-B")
	for _, post := range pr.upserts {
		require.Equal(t, domainScouting.PostKindSweepOnce, post.Kind, "dark-backlog posts are breadth sweep-once posts")
		require.Equal(t, 1, post.Hulls, "sweep-once is single-hull")
	}
	// (i) NO depth pathfinder and NO expansion-frontier declaration — the expansion scanner is
	// never even consulted in scan_only.
	require.NotContains(t, declared, "X1-VIRGIN-DEEP", "scan_only declares no depth pathfinder to virgin")
	require.NotContains(t, declared, "X1-BREADTH", "scan_only does not run the expansion-frontier ranker")
	require.Zero(t, normal.calls, "scan_only never consults the hop-bounded expansion scanner")
	// (ii) NO probe purchase — this exact fixture buys in the normal path; scan_only spends nothing.
	require.Zero(t, buyer.buyCalls, "scan_only buys no probes")
	require.Zero(t, buyer.quoteCalls, "scan_only does not even price a probe")
}

// scan_only=1 requirement (iv): once the discovered backlog is fully scanned (the dark scanner
// returns nothing) the coordinator IDLES — it declares nothing and buys nothing — rather than
// reaching for virgin. The normal scanner still offers a rich candidate (which the normal path
// would declare and buy for) to prove scan_only ignores it and does not fall back to expansion.
func TestFrontierScanOnly_IdlesWhenBacklogFullyScanned(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}
	fr := &fakeFleetRepo{} // zero idle probes → the normal path would buy
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 20000, quoteYard: "X1-HOME-SY", buySymbol: "PROBE-NEW", buyPrice: 20000}
	h.SetProbePurchaser(buyer)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-HIGH", Hops: 1, KnownMarkets: 5, Charted: true}, // would be declared+bought-for normally
	}})
	h.SetDarkMarketScanner(&fakeDarkScanner{candidates: nil}) // backlog empty — fully scanned

	cmd := testCmd()
	cmd.ScanOnly = 1

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	require.Empty(t, pr.upserts, "empty backlog → declares nothing (idle), never reaches for virgin")
	require.Zero(t, buyer.buyCalls, "an idle scan_only cycle buys nothing")
}

// DEFAULT-SAFETY (the load-bearing proof): scan_only=0 (the default) is byte-identical to today.
// With a dark scanner WIRED but scan_only unset, the coordinator ignores it entirely and runs the
// normal expansion path — declaring the top-ranked NORMAL frontier system exactly as
// TestFrontier_DeclaresTopRankedFrontierPost does. The dark scanner is never consulted.
func TestFrontierScanOnly_Zero_RunsNormalExpansionPath_DarkScannerInert(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}
	fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}} // supply covers → isolate declaration
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	normal := &fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-LOW", Hops: 1, KnownMarkets: 1, Charted: true},
		{SystemSymbol: "X1-HIGH", Hops: 1, KnownMarkets: 5, Charted: true}, // highest score
	}}
	h.SetExpansionScanner(normal)
	dark := &fakeDarkScanner{candidates: []ScanCandidate{{SystemSymbol: "X1-DARK", MarketCount: 99}}}
	h.SetDarkMarketScanner(dark)

	cmd := testCmd() // ScanOnly defaults to 0

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	require.Len(t, pr.upserts, 1, "scan_only=0 runs the normal one-per-cycle expansion declaration")
	require.Equal(t, "X1-HIGH", pr.upserts[0].SystemSymbol, "the normal expansion ranker governs, not the dark backlog")
	require.Zero(t, dark.calls, "scan_only=0 never consults the dark-market scanner — the knob is inert by default")
}

// Live-tune plumbing: resolveConfig reads scan_only from the tick's live-config snapshot (live >
// launch), falling back to the launch command when there is no snapshot, else the documented
// default 0 (OFF). Crucially `tune scan_only 0` reverts to OFF even over a launch value that set
// it — 0 is the default here, so there is no <=0 fallback that would strand it ON.
func TestResolveFrontierConfig_ReadsScanOnlyLiveWithDefaultFallback(t *testing.T) {
	def := resolveConfig(testCmd(), nil)
	require.Equal(t, defaultScanOnly, def.ScanOnly, "no snapshot, no launch value → the documented default (0, OFF)")
	require.Equal(t, 0, def.ScanOnly)

	launch := testCmd()
	launch.ScanOnly = 1
	require.Equal(t, 1, resolveConfig(launch, nil).ScanOnly, "no snapshot → the launch command value governs")

	live := liveconfig.Snapshot{"scan_only": 1}
	require.Equal(t, 1, resolveConfig(testCmd(), live).ScanOnly, "a live snapshot enables scan_only next tick")

	revert := liveconfig.Snapshot{"scan_only": 0}
	require.Equal(t, 0, resolveConfig(launch, revert).ScanOnly, "tune scan_only 0 reverts to OFF even over a launch value")
}
