package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- sp-6vep: reuse-before-buy, edge-relay reach, snowball, no-timeout abandon -------------

// fakeReuseRelayer records the reuse-relay attempts and returns a scripted outcome. It stands in
// for the real edge-probe relayer at the coordinator's driven-port boundary (port-to-port test).
type fakeReuseRelayer struct {
	ok         bool
	symbol     string
	err        error
	calls      int
	lastTarget ProbeReuseTarget
}

func (f *fakeReuseRelayer) RelayNearestProbe(_ context.Context, _ shared.PlayerID, target ProbeReuseTarget) (string, bool, error) {
	f.calls++
	f.lastTarget = target
	return f.symbol, f.ok, f.err
}

// fakeNeighborReader returns scripted uncharted gate-neighbors per system for the snowball walk.
type fakeNeighborReader struct {
	neighbors map[string][]string
	calls     int
}

func (f *fakeNeighborReader) UnchartedNeighbors(_ context.Context, _ int, systemSymbol string) ([]string, error) {
	f.calls++
	return f.neighbors[systemSymbol], nil
}

// The reuse relay, snowball walk, and reap timeout are no longer per-knob operator settings
// (sp-tlekc): the reach_mode preset composes them (balanced/deep arm reuse+snowball+reap; shallow
// disarms). That preset→config mapping is verified in reach_mode_test.go; the tests below drive the
// ENGINE directly with a built config (cfgWith), so they still pin the reuse/snowball/reap MECHANISM
// exactly, independent of how the values are resolved.

// AC1 (the deadlock fix): with reuse ARMED and an existing edge probe reachable, the coordinator
// staffs the unmanned frontier post by RELAYING that probe — zero purchases — instead of buying at
// an unreachable deep yard. The buyer is wired and every buy guard would pass (short, treasury fat,
// cooldown clear), so the ONLY reason no buy happens is that reuse pre-empted it (mutation guard:
// delete the reuse pre-empt and the buy fires -> buyCalls==1 -> fails).
func TestFrontier_ReuseBeforeBuy_RelaysEdgeProbeInsteadOfBuying(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-BK75", Kind: domainScouting.PostKindStanding},
	}}
	fr := &fakeFleetRepo{} // no idle probes -> short -> would buy
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 10_000_000})
	buyer := &fakePurchaser{quotePrice: 20000, quoteYard: "X1-DEEP-SY", buySymbol: "BOUGHT", buyPrice: 20000}
	h.SetProbePurchaser(buyer)
	relayer := &fakeReuseRelayer{ok: true, symbol: "PROBE-EDGE"}
	h.SetProbeReuseRelayer(relayer)

	cmd := testCmd()
	cfg := cfgWith(func(c *frontierConfig) {
		c.ProbeReuseEnabled = true
		c.EdgeRelayMaxHops = 3
		c.ReuseValueCeiling = 50000
	})

	require.NoError(t, h.reconcile(context.Background(), cmd, cfg))
	require.Equal(t, 1, relayer.calls, "reuse is attempted before buying")
	require.Zero(t, buyer.buyCalls, "a reused edge probe staffs the post — ZERO purchases (the deadlock fix)")
	require.Equal(t, "X1-BK75", relayer.lastTarget.System, "reuse targets the unmanned post's system")
	require.Equal(t, 3, relayer.lastTarget.MaxHops, "relay-reach is bounded by the reach preset's edge-relay hops")
	require.Equal(t, 50000, relayer.lastTarget.ValueCeiling, "the value ceiling is threaded to the relayer")
}

// Reuse is best-effort BEFORE buy: when no existing probe is reusable within reach / under the
// ceiling (relayer ok=false), the coordinator falls back to the unchanged buy path — so arming
// reuse never REMOVES the ability to grow the fleet, it only prefers relay when one is available.
func TestFrontier_ReuseFallsBackToBuyWhenNoReusableProbe(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding},
	}}
	fr := &fakeFleetRepo{}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 10_000_000})
	buyer := &fakePurchaser{quotePrice: 20000, quoteYard: "X1-HOME-SY", buySymbol: "BOUGHT", buyPrice: 20000}
	h.SetProbePurchaser(buyer)
	relayer := &fakeReuseRelayer{ok: false} // no reusable probe
	h.SetProbeReuseRelayer(relayer)

	cmd := testCmd()
	cfg := cfgWith(func(c *frontierConfig) {
		c.ProbeReuseEnabled = true
		c.ReuseValueCeiling = 50000
	})

	require.NoError(t, h.reconcile(context.Background(), cmd, cfg))
	require.Equal(t, 1, relayer.calls, "reuse is attempted first")
	require.Equal(t, 1, buyer.buyCalls, "no reusable probe -> fall back to the unchanged buy path")
}

// reach_mode=shallow DISARMS reuse: a wired relayer is NEVER consulted and the buy path runs as the
// pure buy-only frontier. (Balanced/deep arm reuse — the live default — so this pins the shallow
// preset's reuse-off half via a built config with reuse disarmed.)
func TestFrontier_ReuseDisarmed_BuysNoRelay(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-A", Kind: domainScouting.PostKindStanding},
	}}
	fr := &fakeFleetRepo{}
	lr := &fakeLedgerRepo{}
	h := newHandler(pr, fr, lr, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 10_000_000})
	buyer := &fakePurchaser{quotePrice: 20000, quoteYard: "X1-HOME-SY", buySymbol: "BOUGHT", buyPrice: 20000}
	h.SetProbePurchaser(buyer)
	relayer := &fakeReuseRelayer{ok: true, symbol: "PROBE-EDGE"}
	h.SetProbeReuseRelayer(relayer)

	cfg := cfgWith(func(c *frontierConfig) { c.ProbeReuseEnabled = false }) // shallow's reuse-off
	require.NoError(t, h.reconcile(context.Background(), testCmd(), cfg))

	require.Zero(t, relayer.calls, "reuse disarmed -> the relayer is never consulted")
	require.Equal(t, 1, buyer.buyCalls, "the buy path runs as the pure buy-only frontier")
}

// AC2 (the no-timeout deadlock fix, INDEPENDENT of reuse): today 5 unstaffable posts jam the
// in-flight cap forever because nothing ever reaps them. The abandon timeout removes an unmanned,
// relay-free in-flight sweep-once post once it is older than post_inflight_timeout_secs, freeing its
// slot — so declaration is never permanently jammed. The table pins every guard: DISABLED (default)
// never reaps (byte-identical); armed reaps only the stale + genuinely-unstaffed + sweep-once post
// (a manned post, a post with a relay airborne, a still-fresh post, and a STANDING freshness post
// are all kept). Reuse is DISARMED throughout — the reap applies regardless. Mutation guards: drop
// the age check and the fresh row reaps wrongly; drop the manned/relay guard and a served post reaps.
func TestFrontier_AbandonsStaleInFlightPostIndependentOfReuse(t *testing.T) {
	base := time.Now()
	cases := []struct {
		name        string
		timeoutSecs int
		ageSecs     int
		hull        string
		relayID     string
		kind        domainScouting.PostKind
		wantRemoved bool
	}{
		{"disabled (default 0): a long-stale post is never reaped — byte-identical to today", 0, 36000, "", "", domainScouting.PostKindSweepOnce, false},
		{"armed + older than the timeout: reaped, slot freed (the deadlock fix)", 1800, 3600, "", "", domainScouting.PostKindSweepOnce, true},
		{"armed + fresher than the timeout: kept (give the reconciler time to man it)", 1800, 600, "", "", domainScouting.PostKindSweepOnce, false},
		{"armed + manned: kept (a manned post is not wedged demand)", 1800, 3600, "HULL-1", "", domainScouting.PostKindSweepOnce, false},
		{"armed + relay airborne: kept (already being served)", 1800, 3600, "", "relay-1", domainScouting.PostKindSweepOnce, false},
		{"armed + standing freshness post: kept (only sweep-once frontier posts are reaped)", 1800, 3600, "", "", domainScouting.PostKindStanding, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &shared.MockClock{CurrentTime: base}
			post := &domainScouting.ScoutPost{
				PlayerID:              1,
				SystemSymbol:          "X1-WEDGED",
				Kind:                  tc.kind,
				Hulls:                 1,
				AssignedHull:          tc.hull,
				RepositionContainerID: tc.relayID,
				CreatedAt:             base.Add(-time.Duration(tc.ageSecs) * time.Second),
			}
			pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{post}}
			fr := &fakeFleetRepo{}
			lr := &fakeLedgerRepo{}
			h := newHandler(pr, fr, lr, clock)
			// No reuse relayer — the reap must fire on the timeout ALONE (independent of reuse).

			cmd := testCmd()
			cfg := cfgWith(func(c *frontierConfig) { c.PostInflightTimeout = time.Duration(tc.timeoutSecs) * time.Second })

			require.NoError(t, h.reconcile(context.Background(), cmd, cfg))
			if tc.wantRemoved {
				require.Equal(t, []string{"X1-WEDGED"}, pr.removed, "the wedged post is abandoned, freeing its in-flight slot")
				return
			}
			require.Empty(t, pr.removed, "the post is NOT abandoned")
		})
	}
}

// The reap frees the in-flight cap: with the cap full of STALE unmanned sweep-once posts and a fresh
// discovery target queued, arming the timeout reaps a stale post and the freed slot lets the head be
// declared THIS cycle — the 5/5 permanent jam becomes a rotating queue. Disabled, the jam persists
// (no reap, no declaration). This is the end-to-end "never a permanent 5/5 deadlock" proof.
func TestFrontier_ReapedSlotUnjamsDeclaration(t *testing.T) {
	base := time.Now()
	makePosts := func() []*domainScouting.ScoutPost {
		posts := make([]*domainScouting.ScoutPost, 0, 5)
		for _, s := range []string{"X1-J1", "X1-J2", "X1-J3", "X1-J4", "X1-J5"} {
			posts = append(posts, &domainScouting.ScoutPost{
				PlayerID: 1, SystemSymbol: s, Kind: domainScouting.PostKindSweepOnce, Hulls: 1,
				CreatedAt: base.Add(-1 * time.Hour), // all long-stale, all unmanned -> the 5/5 jam
			})
		}
		return posts
	}
	newScannerHandler := func(pr *fakePostRepo) *RunFrontierExpansionCoordinatorHandler {
		fr := &fakeFleetRepo{idle: []*navigation.Ship{newProbe(t, "P1", "X1-HOME-A1")}} // supply covers -> isolate declaration
		h := newHandler(pr, fr, &fakeLedgerRepo{}, &shared.MockClock{CurrentTime: base})
		h.SetExpansionScanner(&fakeScanner{candidates: []ExpansionCandidate{
			{SystemSymbol: "X1-FRESH", Hops: 1, KnownMarkets: 5, Charted: true},
		}})
		return h
	}

	// Disabled: the cap stays full (5/5), no reap, and the fresh head cannot be declared.
	jammed := &fakePostRepo{posts: makePosts()}
	disabledCfg := cfgWith(func(c *frontierConfig) { c.PostInflightTimeout = 0 })
	require.NoError(t, newScannerHandler(jammed).reconcile(context.Background(), testCmd(), disabledCfg))
	require.Empty(t, jammed.removed, "disabled -> no reap")
	require.Empty(t, jammed.upserts, "cap full (5/5) + no reap -> the fresh head is jammed out")

	// Armed: a stale post is reaped and the freed slot admits the fresh head SAME cycle.
	unjammed := &fakePostRepo{posts: makePosts()}
	armedCfg := cfgWith(func(c *frontierConfig) { c.PostInflightTimeout = 1800 * time.Second })
	require.NoError(t, newScannerHandler(unjammed).reconcile(context.Background(), testCmd(), armedCfg))
	require.NotEmpty(t, unjammed.removed, "armed -> at least one stale post reaped")
	require.Len(t, unjammed.upserts, 1, "the freed slot admits the fresh discovery head — no permanent 5/5 jam")
	require.Equal(t, "X1-FRESH", unjammed.upserts[0].SystemSymbol)
}

// AC4 (snowball): charting frontier system S enqueues S's uncharted gate-neighbors as the next
// targets, so one probe walks the frontier outward instead of the queue backfilling inward. Armed +
// a neighbor reader wired: the coordinator declares sweep-once posts for S's uncharted neighbors.
// Disarmed (the default): the reader is never consulted and nothing is declared (byte-identical).
// The neighbor reader self-gates — a still-virgin S has no gate edges and yields none — so declaring
// off every frontier post is safe. Mutation guard: drop the SnowballNeighbors gate and the disarmed
// row declares neighbors -> fails.
func TestFrontier_Snowball_EnqueuesUnchartedNeighborsWhenArmed(t *testing.T) {
	newSetup := func() (*fakePostRepo, *fakeNeighborReader, *RunFrontierExpansionCoordinatorHandler) {
		clock := &shared.MockClock{CurrentTime: time.Now()}
		pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{
			// A charted frontier post at S — a relayed probe reached it and charted it.
			{PlayerID: 1, SystemSymbol: "X1-S", Kind: domainScouting.PostKindSweepOnce, Hulls: 1, AssignedHull: "PROBE-1"},
		}}
		fr := &fakeFleetRepo{}
		h := newHandler(pr, fr, &fakeLedgerRepo{}, clock)
		nr := &fakeNeighborReader{neighbors: map[string][]string{"X1-S": {"X1-N1", "X1-N2"}}}
		h.SetFrontierNeighborReader(nr)
		return pr, nr, h
	}

	t.Run("armed: S's uncharted neighbors are declared as sweep-once posts", func(t *testing.T) {
		pr, nr, h := newSetup()
		cfg := cfgWith(func(c *frontierConfig) { c.SnowballNeighbors = true })
		require.NoError(t, h.reconcile(context.Background(), testCmd(), cfg))
		require.Positive(t, nr.calls, "the neighbor reader is consulted for charted frontier systems")
		declared := upsertedSystems(pr)
		require.Contains(t, declared, "X1-N1", "S's uncharted neighbor N1 is enqueued")
		require.Contains(t, declared, "X1-N2", "S's uncharted neighbor N2 is enqueued")
		for _, u := range pr.upserts {
			if u.SystemSymbol == "X1-N1" || u.SystemSymbol == "X1-N2" {
				require.Equal(t, domainScouting.PostKindSweepOnce, u.Kind, "snowball posts are sweep-once frontier posts")
			}
		}
	})

	t.Run("disarmed (reach_mode shallow): the reader is never consulted and nothing is declared", func(t *testing.T) {
		pr, nr, h := newSetup()
		cfg := cfgWith(func(c *frontierConfig) { c.SnowballNeighbors = false }) // shallow's snowball-off
		require.NoError(t, h.reconcile(context.Background(), testCmd(), cfg))
		require.Zero(t, nr.calls, "snowball disarmed -> the reader is never consulted")
		require.Empty(t, pr.upserts, "snowball disarmed -> nothing declared")
	})
}

// Snowball is bounded by the in-flight cap and dedups already-covered neighbors: a neighbor that
// already has a post is skipped, and once the cap is reached no further neighbor is enqueued — the
// walk never floods the reconciler past what breadth declaration is allowed.
func TestFrontier_Snowball_RespectsCapAndDedup(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	pr := &fakePostRepo{posts: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-S", Kind: domainScouting.PostKindSweepOnce, Hulls: 1, AssignedHull: "PROBE-1"},
		{PlayerID: 1, SystemSymbol: "X1-N1", Kind: domainScouting.PostKindSweepOnce, Hulls: 1, AssignedHull: "PROBE-2"}, // N1 already covered
	}}
	fr := &fakeFleetRepo{}
	h := newHandler(pr, fr, &fakeLedgerRepo{}, clock)
	nr := &fakeNeighborReader{neighbors: map[string][]string{"X1-S": {"X1-N1", "X1-N2", "X1-N3", "X1-N4"}}}
	h.SetFrontierNeighborReader(nr)

	cfg := cfgWith(func(c *frontierConfig) {
		c.SnowballNeighbors = true
		c.MaxFrontierPostsInFlight = 3 // 2 posts already in flight -> room for exactly ONE more
	})

	require.NoError(t, h.reconcile(context.Background(), testCmd(), cfg))
	declared := upsertedSystems(pr)
	require.NotContains(t, declared, "X1-N1", "an already-covered neighbor is not re-declared (dedup)")
	require.Len(t, pr.upserts, 1, "only ONE new post fits under the in-flight cap (3 - 2 in flight)")
	require.Subset(t, []string{"X1-N2", "X1-N3", "X1-N4"}, declared, "the one declared is an uncharted, uncovered neighbor")
}

func upsertedSystems(pr *fakePostRepo) []string {
	out := make([]string, 0, len(pr.upserts))
	for _, u := range pr.upserts {
		out = append(out, u.SystemSymbol)
	}
	return out
}

var _ = navigation.Ship{}
