package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-pvw3 discovery_share behavior (superseding the sp-jide binary scan_only): these drive the
// coordinator through its ReconcileOnce driving port and assert observable outcomes at the
// ScoutPostRepository (declared posts) and the ProbePurchaser boundary (buys), never internal
// structure. They cover: the deprecated scan_only alias (pure backlog-scan), the CONCURRENT split
// (both discovery and scan posts in one cycle), graceful degradation both directions, and the
// resolveConfig knob/alias plumbing.

// fakeDarkScanner is the DarkMarketScanner port double: it returns a fixed dark-market backlog (the
// discovered charted-but-price-unscanned set), so the tests pin exactly which systems the scan side
// may sweep.
type fakeDarkScanner struct {
	candidates []ScanCandidate
	err        error
	calls      int
}

func (f *fakeDarkScanner) ChartedUnscannedMarketSystems(_ context.Context, _ int) ([]ScanCandidate, error) {
	f.calls++
	return f.candidates, f.err
}

// DEPRECATED-ALIAS: `scan_only=1` still maps to discovery_share 0 (pure backlog-scan). The
// coordinator declares a sweep-once post for EVERY uncovered dark-market system (ranked by market
// count, an already-covered dark system excluded), declares NO depth pathfinder, buys NO probe, and
// never even consults the expansion scanner — even though this exact fixture (empty posts + zero
// idle probes + treasury & purchaser wired) BUYS in the discovery path. Pins the alias mapping +
// pure-scan invariants (no depth, no buy, expansion scanner untouched).
func TestFrontier_DeprecatedScanOnlyAlias_PureBacklogScan(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{
		standingPost("X1-COVERED", "P9"), // a dark system already covered → must NOT be re-declared
	}}
	fr := &fakeFleetRepo{} // zero idle probes → the discovery path would look short and BUY
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	buyer := &fakePurchaser{quotePrice: 20000, quoteYard: "X1-HOME-SY", buySymbol: "PROBE-NEW", buyPrice: 20000}
	h.SetProbePurchaser(buyer)
	// The discovery scanner offers a depth-eligible deep virgin AND a market-rich breadth head — pure
	// backlog-scan (share 0) must consult NEITHER (no discovery declaration, expansion scanner untouched).
	normal := &fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-VIRGIN-DEEP", Hops: 4, KnownMarkets: 0, Charted: false},
		{SystemSymbol: "X1-BREADTH", Hops: 1, KnownMarkets: 9, Charted: true},
	}}
	h.SetExpansionScanner(normal)
	dark := &fakeDarkScanner{candidates: []ScanCandidate{
		{SystemSymbol: "X1-DARK-A", MarketCount: 3},
		{SystemSymbol: "X1-DARK-B", MarketCount: 14}, // highest count → chosen head
		{SystemSymbol: "X1-COVERED", MarketCount: 5}, // excluded: already posted
	}}
	h.SetDarkMarketScanner(dark)

	cmd := testCmd()
	cmd.ScanOnly = 1 // deprecated alias → discovery_share 0

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	declared := map[string]*domainScouting.ScoutPost{}
	for _, post := range pr.upserts {
		declared[post.SystemSymbol] = post
	}
	require.Len(t, pr.upserts, 2, "a sweep-once post per uncovered dark-market system (X1-COVERED excluded)")
	require.Contains(t, declared, "X1-DARK-A", "the full uncovered dark backlog is swept")
	require.Contains(t, declared, "X1-DARK-B")
	for _, post := range pr.upserts {
		require.Equal(t, domainScouting.PostKindSweepOnce, post.Kind, "dark-backlog posts are breadth sweep-once posts")
		require.Equal(t, 1, post.Hulls, "sweep-once is single-hull")
	}
	require.NotContains(t, declared, "X1-VIRGIN-DEEP", "pure backlog-scan declares no depth pathfinder to virgin")
	require.NotContains(t, declared, "X1-BREADTH", "pure backlog-scan does not run the expansion-frontier ranker")
	require.Zero(t, normal.calls, "pure backlog-scan never consults the expansion scanner")
	require.Zero(t, buyer.buyCalls, "pure backlog-scan buys no probes")
	require.Zero(t, buyer.quoteCalls, "pure backlog-scan does not even price a probe")
}

// THE CORE sp-pvw3 CAPABILITY the binary scan_only could never do: with a virgin frontier AND a
// dark-market backlog both present, ONE cycle declares BOTH discovery and scan posts, split by
// discovery_share. The scan side declares exactly its (100-share)% budget of the highest-market dark
// systems; the discovery side declares its breadth head concurrently. Parametrized over shares so the
// scan count SCALES with (100 - share) — mutating the split ratio changes the scan count and fails.
func TestFrontier_DiscoveryShare_DeclaresBothDiscoveryAndScanConcurrently(t *testing.T) {
	cases := []struct {
		name          string
		share         int
		capacity      int
		wantDiscovery int
		wantScan      int
	}{
		{"60/40 → discovery head + 4 dark sweeps", 60, 10, 1, 4},
		{"20/80 → discovery head + 8 dark sweeps", 20, 10, 1, 8},
		{"50/50 → discovery head + 5 dark sweeps", 50, 10, 1, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &shared.MockClock{CurrentTime: time.Now()}
			pr := &fakePostRepo{}
			// Two idle probes cover the single breadth-head slot → isolate DECLARATION (no buy noise).
			fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-Z-1"), newProbe(t, "P2", "X1-Z-2")}}
			lr := &fakeLedgerRepo{}
			h := newHandler(pr, fr, lr, clock)
			// One clean breadth head (charted hop-1, not a depth target → depth declares nothing).
			h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
				{SystemSymbol: "X1-VIRGIN-1", Hops: 1, KnownMarkets: 5, Charted: true},
			}})
			// A rich dark backlog (10 systems) so the scan budget is fully satisfiable.
			darkCandidates := make([]ScanCandidate, 0, 10)
			for i := 0; i < 10; i++ {
				darkCandidates = append(darkCandidates, ScanCandidate{SystemSymbol: "X1-DARK-" + string(rune('A'+i)), MarketCount: 20 - i})
			}
			h.SetDarkMarketScanner(&fakeDarkScanner{candidates: darkCandidates})

			cmd := testCmd()
			cmd.DiscoveryShare = tc.share
			cmd.MaxFrontierPostsInFlight = tc.capacity

			require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

			discovery, scan := 0, 0
			for _, post := range pr.upserts {
				if strings.HasPrefix(post.SystemSymbol, "X1-DARK-") {
					scan++
					continue
				}
				discovery++
			}
			require.Equal(t, tc.wantDiscovery, discovery, "discovery declared its breadth head concurrently")
			require.Equal(t, tc.wantScan, scan, "scan declared exactly its (100-share) budget of dark sweeps")
		})
	}
}

// GRACEFUL DEGRADATION (backlog dry → discovery). Even with pure-scan intent (deprecated scan_only=1
// ↔ share 0), an EMPTY dark backlog redirects the whole cycle to discovery rather than idling — the
// exact behavior the old binary scan_only lacked (it idled while virgin systems went unexplored). It
// declares the top virgin frontier system. Mutating out the degradation redirect leaves share-0 with
// a 0 discovery budget → nothing declared → this fails.
func TestFrontier_BacklogEmpty_ScanShareRedirectsToDiscovery(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}
	fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-Z-1")}} // supply covers → isolate declaration
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
		{SystemSymbol: "X1-HIGH", Hops: 1, KnownMarkets: 5, Charted: true},
	}})
	h.SetDarkMarketScanner(&fakeDarkScanner{candidates: nil}) // backlog empty — fully drained

	cmd := testCmd()
	cmd.ScanOnly = 1 // pure-scan intent (share 0)

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	require.Len(t, pr.upserts, 1, "empty backlog → capacity flows to discovery (never idles)")
	require.Equal(t, "X1-HIGH", pr.upserts[0].SystemSymbol, "the top virgin frontier system is declared")
}

// GRACEFUL DEGRADATION (no virgin → scan). Even with pure-discovery intent (discovery_share 100), an
// empty expansion frontier redirects the whole cycle to draining the dark-market backlog. It declares
// dark sweeps. Mutating out the degradation redirect leaves share-100 with a 0 scan budget → the dark
// scanner is never consulted → nothing declared → this fails.
func TestFrontier_NoVirginFrontier_DiscoveryShareRedirectsToScan(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{}
	fr := &fakeFleetRepo{}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetExpansionScanner(&fakeScanner{candidates: nil}) // no reachable virgin frontier
	dark := &fakeDarkScanner{candidates: []ScanCandidate{
		{SystemSymbol: "X1-DARK-A", MarketCount: 7},
		{SystemSymbol: "X1-DARK-B", MarketCount: 3},
	}}
	h.SetDarkMarketScanner(dark)

	cmd := testCmd()
	cmd.DiscoveryShare = 100 // pure-discovery intent

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	declared := map[string]bool{}
	for _, post := range pr.upserts {
		declared[post.SystemSymbol] = true
	}
	require.Len(t, pr.upserts, 2, "no virgin frontier → capacity flows to scanning the dark backlog (never idles)")
	require.True(t, declared["X1-DARK-A"] && declared["X1-DARK-B"], "both uncovered dark systems are swept")
	require.Positive(t, dark.calls, "the dark scanner is consulted via graceful degradation")
}

// PURE DISCOVERY (share 100) never consults the dark scanner — the extreme stays byte-cheap and the
// discovery path is byte-identical to pre-sp-pvw3. It declares the top-ranked virgin frontier system.
func TestFrontier_PureDiscovery_DarkScannerInert(t *testing.T) {
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

	cmd := testCmd()
	cmd.DiscoveryShare = 100

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	require.Len(t, pr.upserts, 1, "pure discovery runs the normal one-per-cycle expansion declaration")
	require.Equal(t, "X1-HIGH", pr.upserts[0].SystemSymbol, "the expansion ranker governs, not the dark backlog")
	require.Zero(t, dark.calls, "pure discovery never consults the dark-market scanner")
}

// resolveConfig plumbing: discovery_share is authoritative (live > launch); the deprecated scan_only
// maps its binary (1 ↔ share 0); an unset knob is the documented default. A present live snapshot is
// authoritative, so a `tune` of either key lands next tick.
func TestResolveFrontierConfig_DiscoveryShareWithScanOnlyAlias(t *testing.T) {
	require.Equal(t, defaultDiscoveryShare, resolveConfig(testCmd(), nil).DiscoveryShare,
		"no snapshot, no launch value → the documented default (balanced split)")

	launchShare := testCmd()
	launchShare.DiscoveryShare = 60
	require.Equal(t, 60, resolveConfig(launchShare, nil).DiscoveryShare, "no snapshot → the launch discovery_share governs")

	launchScanOnly := testCmd()
	launchScanOnly.ScanOnly = 1
	require.Equal(t, 0, resolveConfig(launchScanOnly, nil).DiscoveryShare, "deprecated scan_only=1 ↔ pure backlog-scan (share 0)")

	liveShare := liveconfig.Snapshot{"discovery_share": 80}
	require.Equal(t, 80, resolveConfig(testCmd(), liveShare).DiscoveryShare, "a live discovery_share governs next tick")

	liveAlias := liveconfig.Snapshot{"scan_only": 1}
	require.Equal(t, 0, resolveConfig(testCmd(), liveAlias).DiscoveryShare, "a live scan_only=1 still resolves to share 0")

	livePrecedence := liveconfig.Snapshot{"discovery_share": 40, "scan_only": 1}
	require.Equal(t, 40, resolveConfig(testCmd(), livePrecedence).DiscoveryShare, "discovery_share wins over the deprecated scan_only")

	emptyLive := liveconfig.Snapshot{}
	require.Equal(t, defaultDiscoveryShare, resolveConfig(launchShare, emptyLive).DiscoveryShare,
		"a present-but-empty snapshot is authoritative → the documented default, overriding the launch value")
}
