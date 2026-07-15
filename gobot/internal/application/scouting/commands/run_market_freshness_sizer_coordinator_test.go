package commands

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- fakes -----------------------------------------------------------------

type fakeFreshnessReader struct {
	snapshots []domainScouting.SystemFreshnessSnapshot
	err       error
}

func (f *fakeFreshnessReader) SystemsFreshness(_ context.Context, _ int) ([]domainScouting.SystemFreshnessSnapshot, error) {
	return f.snapshots, f.err
}

// fakeSizerPostRepo records every write so a test can assert the exact desired-state the
// coordinator declared/resized/retired. UpdateHulls is the narrow, manning-preserving
// resize seam (the coordinator prefers it over a full Upsert on an existing standing post).
type fakeSizerPostRepo struct {
	posts       []*domainScouting.ScoutPost
	upserts     []*domainScouting.ScoutPost
	removed     []string
	hullUpdates map[string]int
	err         error
}

func newSizerPostRepo(posts ...*domainScouting.ScoutPost) *fakeSizerPostRepo {
	return &fakeSizerPostRepo{posts: posts, hullUpdates: map[string]int{}}
}

func (f *fakeSizerPostRepo) ListActive(_ context.Context, _ int) ([]*domainScouting.ScoutPost, error) {
	return f.posts, f.err
}
func (f *fakeSizerPostRepo) Upsert(_ context.Context, post *domainScouting.ScoutPost) error {
	f.upserts = append(f.upserts, post)
	return nil
}
func (f *fakeSizerPostRepo) Remove(_ context.Context, _ int, systemSymbol string) error {
	f.removed = append(f.removed, systemSymbol)
	return nil
}
func (f *fakeSizerPostRepo) UpdateHulls(_ context.Context, _ int, systemSymbol string, hulls int) error {
	f.hullUpdates[systemSymbol] = hulls
	return nil
}

type fakeSizerFleetRepo struct {
	all []*navigation.Ship
	err error
}

func (f *fakeSizerFleetRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return f.all, f.err
}

type fakeTreasury struct {
	credits int
	err     error
}

func (f *fakeTreasury) LiveCredits(_ context.Context, _ shared.PlayerID) (int, error) {
	return f.credits, f.err
}

type fakePurchaser struct {
	quotePrice int
	buySymbol  string
	buyCalls   int
}

func (f *fakePurchaser) QuoteProbe(_ context.Context, _ shared.PlayerID) (int, string, error) {
	return f.quotePrice, "X1-HQ-YARD", nil
}
func (f *fakePurchaser) BuyProbe(_ context.Context, _ shared.PlayerID, _ int) (int, string, error) {
	f.buyCalls++
	return f.quotePrice, f.buySymbol, nil
}

type fakeLedger struct{ txns []*ledger.Transaction }

func (f *fakeLedger) Create(_ context.Context, _ *ledger.Transaction) error { return nil }
func (f *fakeLedger) FindByID(_ context.Context, _ ledger.TransactionID, _ shared.PlayerID) (*ledger.Transaction, error) {
	return nil, nil
}
func (f *fakeLedger) CountByPlayer(_ context.Context, _ shared.PlayerID, _ ledger.QueryOptions) (int, error) {
	return len(f.txns), nil
}
func (f *fakeLedger) FindByPlayer(_ context.Context, _ shared.PlayerID, opts ledger.QueryOptions) ([]*ledger.Transaction, error) {
	out := make([]*ledger.Transaction, 0, len(f.txns))
	for _, t := range f.txns {
		if opts.StartDate != nil && t.Timestamp().Before(*opts.StartDate) {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp().After(out[j].Timestamp()) })
	return out, nil
}

// ---- helpers ---------------------------------------------------------------

func newScout(t *testing.T, symbol string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint("X1-HOME-A1", 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(0, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 100, 0, cargo, 30, "FRAME_PROBE", "SATELLITE", nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	return ship
}

func scouts(t *testing.T, n int) []*navigation.Ship {
	t.Helper()
	out := make([]*navigation.Ship, n)
	for i := 0; i < n; i++ {
		out[i] = newScout(t, "PROBE-"+string(rune('A'+i)))
	}
	return out
}

func standingSizerPost(system string, hulls int, hull string) *domainScouting.ScoutPost {
	return &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: system, Kind: domainScouting.PostKindStanding,
		Hulls: hulls, AssignedHull: hull, FreshnessTarget: time.Hour,
	}
}

// fullyMannedSizerPost builds a standing post whose EVERY slot (primary + hulls-1 extras)
// carries a hull, so IsFullyManned() is true — the precondition the sp-iupr issue-3 sanity
// floor gates on (a fully-manned, telemetried, breaching post is genuinely under capacity).
func fullyMannedSizerPost(system string, hulls int) *domainScouting.ScoutPost {
	post := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: system, Kind: domainScouting.PostKindStanding,
		Hulls: hulls, AssignedHull: "PROBE-P0", FreshnessTarget: time.Hour,
	}
	for i := 1; i < hulls; i++ {
		post.ExtraSlots = append(post.ExtraSlots, domainScouting.ScoutPostSlot{AssignedHull: "PROBE-P" + string(rune('0'+i))})
	}
	return post
}

func sizerCmd() *RunMarketFreshnessSizerCoordinatorCommand {
	return &RunMarketFreshnessSizerCoordinatorCommand{PlayerID: shared.MustNewPlayerID(1), ContainerID: "freshness-1"}
}

// newSizer wires a handler with a passing buy stack (rich treasury, cheap probe, empty
// ledger) so buy tests exercise the decision; sizing/declare tests give supply >= demand
// to keep the buy out of the way.
func newSizer(fr *fakeFreshnessReader, pr *fakeSizerPostRepo, fl *fakeSizerFleetRepo) *RunMarketFreshnessSizerCoordinatorHandler {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	h := NewRunMarketFreshnessSizerCoordinatorHandler(fr, pr, fl, &fakeLedger{}, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	h.SetProbePurchaser(&fakePurchaser{quotePrice: 5000, buySymbol: "PROBE-NEW"})
	h.SetHullUpdater(pr)
	return h
}

func snap(system string, markets int, oldestAgeSecs, cycleSecs float64, samples int) domainScouting.SystemFreshnessSnapshot {
	return domainScouting.SystemFreshnessSnapshot{
		SystemSymbol: system, MarketCount: markets, OldestAgeSeconds: oldestAgeSecs,
		MeasuredCycleSeconds: cycleSecs, CycleSamples: samples,
	}
}

// newSizerWithClock is newSizer's multi-tick sibling: it hands back the MockClock so a
// test can ADVANCE wall time between ReconcileOnce passes (the stable-window release
// debounce is measured against this clock).
func newSizerWithClock(fr *fakeFreshnessReader, pr *fakeSizerPostRepo, fl *fakeSizerFleetRepo) (*RunMarketFreshnessSizerCoordinatorHandler, *shared.MockClock) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	h := NewRunMarketFreshnessSizerCoordinatorHandler(fr, pr, fl, &fakeLedger{}, clock)
	h.SetTreasuryReader(&fakeTreasury{credits: 1_000_000})
	h.SetProbePurchaser(&fakePurchaser{quotePrice: 5000, buySymbol: "PROBE-NEW"})
	h.SetHullUpdater(pr)
	return h, clock
}

// ---- tests -----------------------------------------------------------------

// A market-rich, fresh system is sized to the static circuit model and DECLARED as a
// standing post: 90 markets × a measured 120s cycle = 10800s circuit / 3600s SLA = 3
// probes. The post is standing (a continuous re-scan tour), single row, sized 3.
func TestSizer_DeclaresStandingPostSizedToModel(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-VB74", 90, 100 /*fresh*/, 120, 89),
	}}
	pr := newSizerPostRepo() // no existing posts
	fl := &fakeSizerFleetRepo{all: scouts(t, 3)}
	h := newSizer(fr, pr, fl)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Len(t, pr.upserts, 1, "one standing post declared")
	got := pr.upserts[0]
	require.Equal(t, "X1-VB74", got.SystemSymbol)
	require.Equal(t, domainScouting.PostKindStanding, got.Kind, "market-bearing systems get standing (continuous) posts")
	require.Equal(t, 3, got.Hulls, "ceil(90×120/3600)=3 probes")
	require.Equal(t, time.Hour, got.FreshnessTarget, "default 1h SLA applied")
}

// per_market_cycle_seconds is MEASURED, not constant: once a system has enough scan
// samples the sizer uses the measured cycle; below the sample floor it falls back to the
// seed default. The two produce DIFFERENT probe counts, proving the measured value drives
// sizing (60 markets: measured 120s → 2 probes; seed 180s → 3 probes).
func TestSizer_UsesMeasuredCycleOverSeedWhenEnoughTelemetry(t *testing.T) {
	cases := []struct {
		name      string
		samples   int
		wantHulls int
	}{
		{"enough samples uses measured 120s → 2", 59, 2},
		{"too few samples falls back to seed 180s → 3", 1, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
				snap("X1-DF86", 60, 100, 120 /*measured*/, tc.samples),
			}}
			pr := newSizerPostRepo()
			fl := &fakeSizerFleetRepo{all: scouts(t, 10)} // supply covers → isolate sizing
			h := newSizer(fr, pr, fl)
			cmd := sizerCmd()
			cmd.SeedCycleSeconds = 180
			cmd.MinCycleSamples = 3

			require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

			require.Len(t, pr.upserts, 1)
			require.Equal(t, tc.wantHulls, pr.upserts[0].Hulls)
		})
	}
}

// Aggregate demand across market-bearing systems exceeds probe supply and every money
// guard passes → the coordinator buys exactly one probe (undedicated; the scout
// reconciler relays it). Two systems needing 2+2 = 4 probes against a supply of 1.
func TestSizer_BuysWhenAggregateDemandExceedsSupply(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-A", 60, 100, 120, 59), // 2 probes
		snap("X1-B", 60, 100, 120, 59), // 2 probes → aggregate demand 4
	}}
	pr := newSizerPostRepo()
	fl := &fakeSizerFleetRepo{all: scouts(t, 1)} // supply 1 < demand 4
	h := newSizer(fr, pr, fl)
	pu := &fakePurchaser{quotePrice: 5000, buySymbol: "PROBE-NEW"}
	h.SetProbePurchaser(pu)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Equal(t, 1, pu.buyCalls, "one probe bought to close the freshness capacity gap")
}

// Supply already covers aggregate demand → no purchase (the sp-njwy over-buy guard: idle +
// in-flight + manning probes all count as supply).
func TestSizer_NoBuyWhenSupplyCoversAggregateDemand(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-A", 60, 100, 120, 59), // 2 probes
	}}
	pr := newSizerPostRepo()
	fl := &fakeSizerFleetRepo{all: scouts(t, 2)} // supply 2 >= demand 2
	h := newSizer(fr, pr, fl)
	pu := &fakePurchaser{quotePrice: 5000, buySymbol: "PROBE-NEW"}
	h.SetProbePurchaser(pu)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Equal(t, 0, pu.buyCalls, "no purchase when supply covers demand")
}

// CLOSED-LOOP FEEDBACK: the static model sizes a 26-market system at 1 probe (26×120 <
// 3600), but its oldest market is 8h stale against a 1h SLA — an 8× breach. The empirical
// age is ground truth, so the sizer RAISES the post to 8 probes, beyond the static model.
// The raise goes through the narrow hull-update seam so live manning is preserved.
func TestSizer_RaisesHullsWhenActualFreshnessBreachesSLA(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-VB74", 26, 28800 /*8h stale*/, 120, 25),
	}}
	pr := newSizerPostRepo(standingSizerPost("X1-VB74", 1, "PROBE-MANNED"))
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
	h := newSizer(fr, pr, fl)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Equal(t, 8, pr.hullUpdates["X1-VB74"], "8× SLA breach raises the post to 8 probes (ground-truth feedback)")
	require.Empty(t, pr.upserts, "resize goes through UpdateHulls, never a manning-clobbering full Upsert")
}

// RELEASE with hysteresis: a system carrying feedback-added probes (3) that is now
// COMFORTABLY under the SLA sheds a probe; one that is under the SLA but not yet
// comfortable HOLDS, so the fleet does not flap at the boundary.
func TestSizer_ReleasesProbesOnlyWhenComfortablyUnderSLA(t *testing.T) {
	cases := []struct {
		name          string
		oldestAgeSecs float64
		wantHulls     int // static floor for 26 markets @120s/3600s is 1
	}{
		{"comfortably fresh sheds one probe", 500 /*<60% of 3600*/, 2},
		{"under SLA but not comfortable holds", 3000 /*between 2160 and 3600*/, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
				snap("X1-VB74", 26, tc.oldestAgeSecs, 120, 25),
			}}
			pr := newSizerPostRepo(standingSizerPost("X1-VB74", 3, "PROBE-MANNED"))
			fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
			h := newSizer(fr, pr, fl)

			require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

			if tc.wantHulls == 3 {
				_, resized := pr.hullUpdates["X1-VB74"]
				require.False(t, resized, "a healthy-but-not-comfortable post holds — no release")
			} else {
				require.Equal(t, tc.wantHulls, pr.hullUpdates["X1-VB74"], "comfortably fresh releases one probe toward the model floor")
			}
		})
	}
}

// AUTO-RESIZE with retirement: a system that lost its markets (no longer in the census)
// but still carries a standing post is RETIRED — the post is removed, freeing its probes
// back to the pool for systems that still need them.
func TestSizer_RetiresStandingPostForMarketlessSystem(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-STILL-HERE", 60, 100, 120, 59),
	}}
	pr := newSizerPostRepo(
		standingSizerPost("X1-STILL-HERE", 2, "P1"),
		standingSizerPost("X1-RETIRED", 3, "P2"), // its system dropped out of the census
	)
	fl := &fakeSizerFleetRepo{all: scouts(t, 10)}
	h := newSizer(fr, pr, fl)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Equal(t, []string{"X1-RETIRED"}, pr.removed, "the marketless system's post is retired, freeing its probes")
}

// FAIL-SAFE: an EMPTY census (a cold start or a transient read that surfaced no
// market-bearing systems) must NEVER mass-retire the standing posts — retiring every post
// on a momentary empty read is a fleet-killer. With no market-bearing systems the
// coordinator declares, resizes, buys, and retires NOTHING.
func TestSizer_EmptyCensusRetiresNothing(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: nil} // census surfaced no systems this tick
	pr := newSizerPostRepo(
		standingSizerPost("X1-A", 2, "P1"),
		standingSizerPost("X1-B", 3, "P2"),
	)
	fl := &fakeSizerFleetRepo{all: scouts(t, 5)}
	h := newSizer(fr, pr, fl)
	pu := &fakePurchaser{quotePrice: 5000, buySymbol: "PROBE-NEW"}
	h.SetProbePurchaser(pu)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Empty(t, pr.removed, "an empty census must not mass-retire standing posts (fleet-killer guard)")
	require.Equal(t, 0, pu.buyCalls, "no demand, no buy")
}

// PROMOTION: a system that a frontier sweep-once post first charted turns out to hold
// markets, so it is promoted to a standing freshness post sized to the model — while its
// already-manning probe is preserved through the promotion.
func TestSizer_PromotesSweepOncePostToStandingWhenMarketsFound(t *testing.T) {
	sweep := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-NEW", Kind: domainScouting.PostKindSweepOnce,
		Hulls: 1, AssignedHull: "PROBE-SWEEPER",
	}
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-NEW", 60, 100, 120, 59), // now known to hold 60 markets → 2 probes
	}}
	pr := newSizerPostRepo(sweep)
	fl := &fakeSizerFleetRepo{all: scouts(t, 10)}
	h := newSizer(fr, pr, fl)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Len(t, pr.upserts, 1, "the sweep-once post is promoted through a full upsert")
	got := pr.upserts[0]
	require.Equal(t, domainScouting.PostKindStanding, got.Kind, "promoted to a standing freshness post")
	require.Equal(t, 2, got.Hulls, "sized to the freshness model")
	require.Equal(t, "PROBE-SWEEPER", got.AssignedHull, "the manning probe is preserved through promotion")
}

// Per-system SLA override: a system flagged for tighter freshness (30min) is sized to that
// stricter target, not the 1h default — 60 markets × 120s / 1800s = 4 probes.
func TestSizer_HonorsPerSystemSLAOverride(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-CRITICAL", 60, 100, 120, 59),
	}}
	pr := newSizerPostRepo()
	fl := &fakeSizerFleetRepo{all: scouts(t, 10)}
	h := newSizer(fr, pr, fl)
	cmd := sizerCmd()
	cmd.SystemSLAOverrides = map[string]int{"X1-CRITICAL": 1800}

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	require.Len(t, pr.upserts, 1)
	require.Equal(t, 4, pr.upserts[0].Hulls, "ceil(60×120/1800)=4 under the 30min override")
	require.Equal(t, 30*time.Minute, pr.upserts[0].FreshnessTarget)
}

// ---- sp-iupr: telemetry-starved over-provisioning + slack release --------------

// BUG 1 (sp-iupr): a system whose probes never complete scan cycles produces NO cycle
// telemetry, so its markets go stale and OldestAgeSeconds grows without bound. The
// closed-loop age raise then pins its post at the per-system cap (8) regardless of how
// FEW markets it has — a 3-market system stuck at 8 forever, higher than a healthy
// 12-market one. A telemetry-starved system's age is a MANNING signal, not a capacity
// one, so the seed/no-telemetry sizing must scale with MARKET COUNT alone (bounded),
// never inflate off the age. Here a 3-market and a 12-market starved+breaching system
// both seed to 1 (3 never higher than 12), and an 80-market one to 4 — seed scales with
// markets, and none is pinned at the cap.
func TestSizer_SeedsTelemetryStarvedSystemByMarketCountNotAge(t *testing.T) {
	cases := []struct {
		name      string
		markets   int
		wantHulls int
	}{
		{"3-market starved seeds small, not the cap", 3, 1},
		{"12-market starved is never below the 3-market one", 12, 1},
		{"80-market starved scales up with market count", 80, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
				// 8h stale (breaching) with ZERO cycle samples: probes are not cycling.
				snap("X1-ZY16", tc.markets, 28800, 0, 0),
			}}
			pr := newSizerPostRepo() // no existing post → declare path
			fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
			h := newSizer(fr, pr, fl)

			require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

			require.Len(t, pr.upserts, 1, "one standing post declared")
			require.Equal(t, tc.wantHulls, pr.upserts[0].Hulls,
				"telemetry-starved sizing is market-count based (ceil(markets×180s/3600s)), never the age-raised cap")
		})
	}
}

// BUG 1 (sp-iupr) release half: a post ALREADY pinned oversized (8) by the old age raise,
// still telemetry-starved, must WALK DOWN to the market-count floor instead of parking at
// the seed/cap forever — its age cannot hold it (age is not a capacity signal when the
// probes aren't cycling). And once it reaches that market-count floor it HOLDS there, never
// released below its own requirement.
func TestSizer_TelemetryStarvedOversizedPostConvergesToMarketCountFloor(t *testing.T) {
	t.Run("pinned-at-cap starved post steps down toward the floor", func(t *testing.T) {
		fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
			snap("X1-ZY16", 3, 28800 /*breaching*/, 0, 0 /*starved*/),
		}}
		pr := newSizerPostRepo(standingSizerPost("X1-ZY16", 8, "PROBE-MANNED"))
		fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
		h := newSizer(fr, pr, fl)

		require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

		require.Equal(t, 7, pr.hullUpdates["X1-ZY16"],
			"a starved oversized post steps down toward the market-count floor, not pinned at the cap")
	})

	t.Run("at the market-count floor a starved post holds", func(t *testing.T) {
		fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
			snap("X1-ZY16", 3, 28800, 0, 0),
		}}
		pr := newSizerPostRepo(standingSizerPost("X1-ZY16", 1, "PROBE-MANNED"))
		fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
		h := newSizer(fr, pr, fl)

		require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

		_, resized := pr.hullUpdates["X1-ZY16"]
		require.False(t, resized, "a starved post at its market-count floor holds — never raised off the stale age, never released below the floor")
	})
}

// BUG 2 (sp-iupr): a post whose measured requirement has fallen below its current budget,
// sitting UNDER its SLA but not yet comfortably fresh (the warm band the old code held
// forever), releases its surplus once the slack has been STABLE for the release window —
// so aggregate supply stops outrunning demand. The release is paced (the freed probe
// returns to the shared pool for the frontier) and floored at the measured requirement.
func TestSizer_ReleasesStableWarmSurplusAfterWindow(t *testing.T) {
	// 26 markets × 120s = 3120s < 3600s SLA → measured requirement 1; the post carries 3
	// (feedback probes). Age 3000s is UNDER the 3600s SLA but past the 60% (2160s) comfort
	// line — the warm band.
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-DF86", 26, 3000, 120, 25),
	}}
	pr := newSizerPostRepo(standingSizerPost("X1-DF86", 3, "PROBE-MANNED"))
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
	h, clock := newSizerWithClock(fr, pr, fl)

	// First observation of the surplus: hold (the window has not elapsed) — proving a
	// warm surplus is not shed on sight.
	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))
	_, resized := pr.hullUpdates["X1-DF86"]
	require.False(t, resized, "warm surplus is not released on the first tick")

	// The slack stays stable across the release window → shed one probe to the pool.
	clock.Advance(301 * time.Second) // default release_stable_window_secs is 300
	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))
	require.Equal(t, 2, pr.hullUpdates["X1-DF86"],
		"a warm surplus stable across the release window sheds one probe toward the measured requirement")
}

// BUG 2 (sp-iupr) hysteresis: a ONE-CYCLE dip in demand (a single tick where desired drops
// below current, then recovers) must NOT release — otherwise the sizer sheds a probe the
// next tick's rebound re-buys, thrashing against the shared pool. Only a slack that stays
// stable across the whole window releases.
func TestSizer_WarmSurplusOneCycleDipDoesNotRelease(t *testing.T) {
	// markets=90 → measured requirement 3 == current 3 (no surplus); markets=26 → requirement
	// 1 < current 3 (surplus). We flip to 26 for a SINGLE tick, then back.
	steady := snap("X1-DF86", 90, 3000, 120, 25) // requirement 3 == current 3
	dip := snap("X1-DF86", 26, 3000, 120, 25)    // requirement 1 < current 3 (one cycle only)
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{steady}}
	pr := newSizerPostRepo(standingSizerPost("X1-DF86", 3, "PROBE-MANNED"))
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
	h, clock := newSizerWithClock(fr, pr, fl)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd())) // t0: no surplus
	clock.Advance(100 * time.Second)
	fr.snapshots = []domainScouting.SystemFreshnessSnapshot{dip}
	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd())) // one cycle of surplus
	clock.Advance(100 * time.Second)
	fr.snapshots = []domainScouting.SystemFreshnessSnapshot{steady}
	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd())) // surplus gone again
	clock.Advance(400 * time.Second)                                      // > a full window since t0
	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Empty(t, pr.hullUpdates, "a one-cycle demand dip never accrues a stable window — no probe is shed")
}

// BUG 2 (sp-iupr) frontier coordination: releasing surplus must return the probe to the
// SHARED idle pool (a resize-DOWN the scout reconciler un-mans, landing the hull undedicated
// where the frontier expansion coordinator (sp-8w89) can claim it), NEVER retire the post or
// sell the hull. No-churn: the post keeps its measured-requirement probes and the sizer does
// not turn around and re-buy the hull it just freed.
func TestSizer_ReleasedSurplusReturnsToSharedPoolNotDestroyed(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-ZY16", 3, 28800, 0, 0), // starved, oversized — an immediate release candidate
	}}
	pr := newSizerPostRepo(standingSizerPost("X1-ZY16", 8, "PROBE-MANNED"))
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)} // supply covers the stepped-down demand → no rebuy
	h := newSizer(fr, pr, fl)
	pu := &fakePurchaser{quotePrice: 5000, buySymbol: "PROBE-NEW"}
	h.SetProbePurchaser(pu)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Equal(t, 7, pr.hullUpdates["X1-ZY16"], "surplus is freed by resizing the post DOWN (returns the hull to the pool)")
	require.Empty(t, pr.removed, "release never RETIRES the post — the frontier can claim the freed hull from the shared pool")
	require.Equal(t, 0, pu.buyCalls, "no-churn: the sizer does not re-buy a hull it just released")
}

// ---- sp-iupr issue 3: bidirectional per-system miscalibration -----------------

// ISSUE 3a — SANITY FLOOR vs the issue-1 STARVED branch (they must never collide). A post
// that is FULLY MANNED and has TRUSTWORTHY telemetry yet whose oldest scan still BREACHES the
// SLA is genuinely under capacity — the closed-loop model, anchored on a noisy-low cycle,
// under-sized it — so its target is BUMPED one probe past its budget (empirical age is ground
// truth). The SAME post, same breaching age, but TELEMETRY-STARVED takes the OPPOSITE path:
// its age is a manning signal, not a capacity one (issue 1), so it stays on the static
// market-count model and is shed toward its floor, never raised off the age. Same inputs,
// opposite outcomes, selected purely by trusted-vs-starved — proving the two branches are
// disjoint (the sanity floor is gated on !starved).
func TestSizer_BumpsFullyMannedBreachingPostAboveModel_DisjointFromStarved(t *testing.T) {
	cases := []struct {
		name      string
		cycleSecs float64
		samples   int
		wantHulls int
	}{
		// Trusted (25 samples): the closed-loop model anchored on the measured 900s cycle only
		// reaches 2 = the current budget (a noisy-low cycle left it STUCK, breaching forever) —
		// so the sanity floor bumps it to 3 (current+1).
		{"trusted fully-manned breaching post is bumped above the stuck model", 900, 25, 3},
		// Starved (0 samples): NOT age-raised (issue 1). Seeded by market count
		// (ceil(4×180/3600)=1) and shed toward that floor — the breach is a manning signal here.
		{"starved fully-manned breaching post is NOT bumped — stays on the market-count model", 0, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
				// NM33: 4 markets, oldest scan 69min (4140s) > the 60min SLA — breaching.
				snap("X1-NM33", 4, 4140, tc.cycleSecs, tc.samples),
			}}
			pr := newSizerPostRepo(fullyMannedSizerPost("X1-NM33", 2)) // sized 2, manned 2/2
			fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
			h := newSizer(fr, pr, fl)

			require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

			require.Equal(t, tc.wantHulls, pr.hullUpdates["X1-NM33"],
				"trusted+manned+breaching bumps above the stuck model; starved holds the market-count model")
		})
	}
}

// ISSUE 3b — MARKET-COUNT CLAMP under the live noise pattern. Feeding the incident's inversion
// (a 3-market system read on a noisy-HIGH cycle, a 26-market system on a noisy-LOW one) the
// sizer must NOT size the 3-market system above the 26-market one. The clamp bounds the small-
// market system to its market-count ceiling (ceil(3×30min/60min)=2) regardless of the noise,
// restoring the monotone-ish order small-market ≤ large-market (the NM33-vs-ZY16 smoking gun).
func TestSizer_ClampsSmallMarketSystemBelowLargeMarketSystem(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-ZY16", 3, 100 /*fresh*/, 6000 /*noisy-high cycle*/, 25),
		snap("X1-VB74", 26, 100 /*fresh*/, 200 /*noisy-low cycle*/, 25),
	}}
	pr := newSizerPostRepo() // no existing posts → declare path
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
	h := newSizer(fr, pr, fl)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	bySystem := map[string]int{}
	for _, u := range pr.upserts {
		bySystem[u.SystemSymbol] = u.Hulls
	}
	require.Equal(t, 2, bySystem["X1-ZY16"],
		"the 3-market system is clamped to its market-count ceiling, not the noise-inflated target")
	require.LessOrEqual(t, bySystem["X1-ZY16"], bySystem["X1-VB74"],
		"a 3-market system is never sized above a 26-market one under the same cycle noise")
}

// ISSUE 3c — CYCLE-NOISE DAMPENING. Two systems with EQUAL market counts and noisy-but-similar
// underlying cycles (600s and 1200s around a ~900s truth) must converge on the SAME probe
// target. Before dampening they diverge (ceil(10×600/3600)=2 vs ceil(10×1200/3600)=4);
// shrinking each toward the fleet median (900s) lands both at ceil(10×900/3600)=3.
func TestSizer_EqualMarketNoisyCyclesConvergeToSameTarget(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-EQ1", 10, 100, 600 /*noisy-low*/, 25),
		snap("X1-EQ2", 10, 100, 1200 /*noisy-high*/, 25),
	}}
	pr := newSizerPostRepo() // declare path
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
	h := newSizer(fr, pr, fl)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	bySystem := map[string]int{}
	for _, u := range pr.upserts {
		bySystem[u.SystemSymbol] = u.Hulls
	}
	require.Equal(t, bySystem["X1-EQ1"], bySystem["X1-EQ2"],
		"equal-market systems with noisy-but-similar cycles converge to the same target")
	require.Equal(t, 3, bySystem["X1-EQ1"],
		"both converge to the fleet-median-anchored target ceil(10×900/3600)=3")
}

// ISSUE 3 config wiring: the two new knobs are live-tunable. resolveSizerConfig reads
// worst_cycle_seconds and cycle_dampening_percent from the tick's live-config snapshot and
// falls back to their documented defaults with no snapshot — guarding against the
// registry↔overlay drift that would leave a registered knob silently ineffective.
func TestResolveSizerConfig_ReadsIssue3KnobsLiveWithDefaultFallback(t *testing.T) {
	def := resolveSizerConfig(sizerCmd(), nil)
	require.Equal(t, time.Duration(defaultWorstCycleSeconds)*time.Second, def.WorstCycle,
		"no snapshot → documented worst-cycle default")
	require.Equal(t, defaultCycleDampeningPercent, def.CycleDampeningPercent,
		"no snapshot → documented dampening default")

	live := liveconfig.Snapshot{"worst_cycle_seconds": 1200, "cycle_dampening_percent": 80}
	got := resolveSizerConfig(sizerCmd(), live)
	require.Equal(t, 1200*time.Second, got.WorstCycle, "live snapshot overrides worst cycle next tick")
	require.Equal(t, 80, got.CycleDampeningPercent, "live snapshot overrides dampening next tick")
}
