package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-pvw3 discover_scan_balance behavior (sp-tlekc: discovery_share promoted 1:1, the sp-jide binary
// scan_only retired): these drive the coordinator through its ReconcileOnce/reconcile driving port and
// assert observable outcomes at the ScoutPostRepository (declared posts) and the ProbePurchaser
// boundary (buys), never internal structure. They cover the CONCURRENT split (both discovery and scan
// posts in one cycle), graceful degradation both directions, and the resolveConfig dial plumbing.

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
			cmd.DiscoverScanBalance = tc.share
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

// GRACEFUL DEGRADATION (backlog dry → discovery). Even with a scan-heavy balance (discover_scan_balance
// low), an EMPTY dark backlog redirects the whole cycle to discovery rather than idling. It declares
// the top virgin frontier system. Mutating out the degradation redirect leaves a scan-heavy split with
// its scan budget stranded → this fails.
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
	cmd.DiscoverScanBalance = 20 // scan-heavy — but the backlog is dry, so it all flows to discovery

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
	cmd.DiscoverScanBalance = 100 // pure-discovery intent

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
// discovery path is byte-identical to before. It declares the top-ranked virgin frontier system.
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
	cmd.DiscoverScanBalance = 100

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	require.Len(t, pr.upserts, 1, "pure discovery runs the normal one-per-cycle expansion declaration")
	require.Equal(t, "X1-HIGH", pr.upserts[0].SystemSymbol, "the expansion ranker governs, not the dark backlog")
	require.Zero(t, dark.calls, "pure discovery never consults the dark-market scanner")
}

// resolveConfig plumbing (sp-tlekc): discover_scan_balance is authoritative (live > launch); the
// legacy discovery_share is the read-through migration alias; an unset dial is the documented default.
// A present live snapshot is authoritative, so a `tune` lands next tick.
func TestResolveFrontierConfig_DiscoverScanBalanceThreading(t *testing.T) {
	require.Equal(t, defaultDiscoveryShare, resolveConfig(testCmd(), nil).DiscoveryShare,
		"no snapshot, no launch value → the documented default (balanced split)")

	launchDial := testCmd()
	launchDial.DiscoverScanBalance = 60
	require.Equal(t, 60, resolveConfig(launchDial, nil).DiscoveryShare, "no snapshot → the launch discover_scan_balance governs")

	// The legacy discovery_share is honored as the rename migration alias when the dial is unset.
	launchLegacy := testCmd()
	launchLegacy.DiscoveryShare = 40
	require.Equal(t, 40, resolveConfig(launchLegacy, nil).DiscoveryShare, "a persisted legacy discovery_share still resolves (rename migration)")

	liveDial := liveconfig.Snapshot{"discover_scan_balance": 80}
	require.Equal(t, 80, resolveConfig(testCmd(), liveDial).DiscoveryShare, "a live discover_scan_balance governs next tick")

	liveLegacy := liveconfig.Snapshot{"discovery_share": 30}
	require.Equal(t, 30, resolveConfig(testCmd(), liveLegacy).DiscoveryShare, "a live legacy discovery_share still resolves (migration)")

	livePrecedence := liveconfig.Snapshot{"discover_scan_balance": 70, "discovery_share": 40}
	require.Equal(t, 70, resolveConfig(testCmd(), livePrecedence).DiscoveryShare, "the dial wins over the legacy alias")

	emptyLive := liveconfig.Snapshot{}
	require.Equal(t, defaultDiscoveryShare, resolveConfig(launchDial, emptyLive).DiscoveryShare,
		"a present-but-empty snapshot is authoritative → the documented default, overriding the launch value")
}
