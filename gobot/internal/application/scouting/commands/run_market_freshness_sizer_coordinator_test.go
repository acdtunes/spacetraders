package commands

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// ---- fakes -----------------------------------------------------------------

type fakeFreshnessReader struct {
	snapshots []domainScouting.SystemFreshnessSnapshot
	err       error
}

func (f *fakeFreshnessReader) SystemsFreshness(_ context.Context, _ int) ([]domainScouting.SystemFreshnessSnapshot, error) {
	return f.snapshots, f.err
}

// fakeChartedMarketplaceReader stands in for the "has a marketplace" signal (sp-u8jc/sp-gucu):
// system → charted marketplace-waypoint count, regardless of whether prices were scanned. The
// coordinator diffs it against the SCANNED freshness census to find charted-but-unscanned hubs.
type fakeChartedMarketplaceReader struct {
	counts map[string]int
	err    error
}

func (f *fakeChartedMarketplaceReader) ChartedMarketSystemCounts(_ context.Context) (map[string]int, error) {
	return f.counts, f.err
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
	lastTarget probebuy.ProbeTarget
}

func (f *fakePurchaser) QuoteProbe(_ context.Context, _ shared.PlayerID, target probebuy.ProbeTarget) (int, string, error) {
	f.lastTarget = target
	return f.quotePrice, "X1-HQ-YARD", nil
}
func (f *fakePurchaser) BuyProbe(_ context.Context, _ shared.PlayerID, _ int, target probebuy.ProbeTarget) (int, string, error) {
	f.buyCalls++
	f.lastTarget = target
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

// fakeTourTelemetryReader stands in for the reused tour_telemetry_repository read (sp-wuksw):
// it returns the player's realized trade legs and RECORDS the player_id + since it was asked
// for, so a test can assert the coordinator scopes the demand read by player.
type fakeTourTelemetryReader struct {
	legs         []trading.TourLegTelemetry
	err          error
	calledPlayer int
	sinceArg     time.Time
	calls        int
}

func (f *fakeTourTelemetryReader) ListByPlayer(_ context.Context, playerID int, since time.Time) ([]trading.TourLegTelemetry, error) {
	f.calledPlayer = playerID
	f.sinceArg = since
	f.calls++
	return f.legs, f.err
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

// mkt builds one market sample (age seconds, value weight) for an sp-r57g percentile fixture.
func mkt(ageSecs, weight float64) domainScouting.MarketFreshnessSample {
	return domainScouting.MarketFreshnessSample{AgeSeconds: ageSecs, Weight: weight}
}

// freshMarkets builds n identical markets at the given age + value weight — the fresh bulk a
// stale straggler is appended to in the reframe fixtures.
func freshMarkets(n int, ageSecs, weight float64) []domainScouting.MarketFreshnessSample {
	out := make([]domainScouting.MarketFreshnessSample, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, mkt(ageSecs, weight))
	}
	return out
}

// activityMarkets builds n identical FRESH markets (age 100s < any SLA so no breach raise fires,
// unit weight) carrying `activity` — a single-activity cohort for the sp-j4kjv per-activity SLA
// fixtures. An empty activity is the unknown/null case.
func activityMarkets(n int, activity string) []domainScouting.MarketFreshnessSample {
	out := make([]domainScouting.MarketFreshnessSample, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, domainScouting.MarketFreshnessSample{AgeSeconds: 100, Weight: 1, Activity: activity})
	}
	return out
}

// snapWithMarkets builds a TRUSTED freshness snapshot from an explicit per-market distribution
// (sp-r57g): MarketCount and OldestAgeSeconds (the max) are DERIVED from the markets so the
// snapshot is self-consistent, while Markets drives the value-weighted percentile the sizer sizes
// against. This is the fixture that exercises the percentile path (vs snap()'s aggregate-only
// fallback to the max).
func snapWithMarkets(system string, cycleSecs float64, samples int, markets []domainScouting.MarketFreshnessSample) domainScouting.SystemFreshnessSnapshot {
	oldest := 0.0
	for _, m := range markets {
		if m.AgeSeconds > oldest {
			oldest = m.AgeSeconds
		}
	}
	return domainScouting.SystemFreshnessSnapshot{
		SystemSymbol:         system,
		MarketCount:          len(markets),
		OldestAgeSeconds:     oldest,
		MeasuredCycleSeconds: cycleSecs,
		CycleSamples:         samples,
		Markets:              markets,
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
	// sp-hej4: the aggregate buy names its NEEDIEST system (largest desired−current gap; X1-A and
	// X1-B both need 2 from 0, first wins) as the demand-proximal target, with the shared default
	// per-hop penalty, so the probe spawns at the yard nearest the shortfall — fail-open otherwise.
	require.Equal(t, "X1-A", pu.lastTarget.System, "the neediest market-bearing system is the buy target")
	require.Equal(t, probebuy.DefaultHopPenaltyCredits, pu.lastTarget.HopPenaltyCredits, "the sizer applies the shared default proximal penalty")
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

// ---- sp-j4kjv: per-activity SLA sizing -------------------------------------

// The freshness SLA is a FUNCTION OF market ACTIVITY. Two systems with the IDENTICAL market count
// (90) and cycle (120s) are sized differently purely by activity: a WEAK cohort against the 360m
// SLA needs 1 probe (ceil(90×120/21600)=1), the SAME markets as a STRONG cohort against the 22m SLA
// need 9 (ceil(90×120/1320)=9). A single global SLA would size both alike — proving the SLA input
// to the required_probes sizing is now per-activity.
func TestSizer_SizesActivityCohortsAgainstPerActivitySLA_StrongNeedsMoreThanWeak(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snapWithMarkets("X1-WEAK", 120, 89, activityMarkets(90, "WEAK")),
		snapWithMarkets("X1-STRONG", 120, 89, activityMarkets(90, "STRONG")),
	}}
	pr := newSizerPostRepo()
	fl := &fakeSizerFleetRepo{all: scouts(t, 40)}
	h := newSizer(fr, pr, fl)
	cmd := sizerCmd()
	cmd.MaxProbesPerSystem = 20 // lift the default-8 cap so the STRONG cohort's true need isn't clipped

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	got := map[string]int{}
	for _, u := range pr.upserts {
		got[u.SystemSymbol] = u.Hulls
	}
	require.Equal(t, 1, got["X1-WEAK"], "a WEAK cohort at the 360m SLA needs 1 probe: ceil(90×120/21600)=1")
	require.Equal(t, 9, got["X1-STRONG"], "the SAME 90 markets at the 22m STRONG SLA need 9 probes: ceil(90×120/1320)=9")
	require.Greater(t, got["X1-STRONG"], got["X1-WEAK"], "a tighter activity SLA sizes more probes for the same market count")
}

// A single system with a MIXED activity set is sized as the SUM of its per-cohort sizing, not one
// blended global SLA. 30 STRONG (ceil(30×120/1320)=3) + 30 WEAK (ceil(30×120/21600)=1) = 4 probes,
// where a single global 60m SLA over all 60 markets would be ceil(60×120/3600)=2.
func TestSizer_MixedActivitySystemSumsPerCohortSizing_NotSingleGlobalSLA(t *testing.T) {
	mixed := append(activityMarkets(30, "STRONG"), activityMarkets(30, "WEAK")...)
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snapWithMarkets("X1-MIX", 120, 89, mixed),
	}}
	pr := newSizerPostRepo()
	fl := &fakeSizerFleetRepo{all: scouts(t, 40)}
	h := newSizer(fr, pr, fl)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Len(t, pr.upserts, 1)
	require.Equal(t, 4, pr.upserts[0].Hulls,
		"mixed-activity demand is the SUM of per-cohort sizing (3 STRONG + 1 WEAK), not the single-global-SLA 2")
}

// Markets whose activity is unknown/null are sized at the RESTRICTED default (135m). A system of 60
// null-activity markets + 30 GROWING markets at a 300s cycle: null → RESTRICTED (8100s):
// ceil(60×300/8100)=3, GROWING (2700s): ceil(30×300/2700)=4, total 7. A single global 60m SLA over
// all 90 markets would be ceil(90×300/3600)=8, and pricing the null cohort as WEAK would be 1
// (total 5) — 7 proves the null cohort is sized at RESTRICTED. (The GROWING market makes the system
// activity-bearing; an ALL-null system falls back to the global SLA — see the guard test below.)
func TestSizer_UnknownActivityMarketsSizedAtRestrictedDefault(t *testing.T) {
	markets := append(activityMarkets(60, ""), activityMarkets(30, "GROWING")...)
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snapWithMarkets("X1-NULLACT", 300, 89, markets),
	}}
	pr := newSizerPostRepo()
	fl := &fakeSizerFleetRepo{all: scouts(t, 40)}
	h := newSizer(fr, pr, fl)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Len(t, pr.upserts, 1)
	require.Equal(t, 7, pr.upserts[0].Hulls,
		"unknown/null activity is sized at the RESTRICTED default (135m): 3 (null@RESTRICTED) + 4 (GROWING) = 7")
}

// BYTE-IDENTICAL GUARD: a system whose markets carry NO activity signal at all (a pre-activity
// census, or an aggregate-only fixture) is sized on the single global sla_seconds — it does NOT
// silently reprice every market at the RESTRICTED default. 90 markets, 120s cycle, global 60m SLA:
// ceil(90×120/3600)=3 (RESTRICTED would be 2). This is the property that keeps every pre-sp-j4kjv
// test green.
func TestSizer_NoActivitySignalFallsBackToGlobalSLA(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snapWithMarkets("X1-NOACT", 120, 89, freshMarkets(90, 100, 1)), // freshMarkets carry Activity ""
	}}
	pr := newSizerPostRepo()
	fl := &fakeSizerFleetRepo{all: scouts(t, 40)}
	h := newSizer(fr, pr, fl)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Len(t, pr.upserts, 1)
	require.Equal(t, 3, pr.upserts[0].Hulls,
		"no activity signal → the global sla_seconds (byte-identical), not the RESTRICTED default")
}

// The per-activity SLAs are TUNE KNOBS defined once (RULINGS #5): resolveSizerConfig resolves each
// from the tick's live snapshot and falls back to its documented default with no snapshot (or a
// `tune 0` revert), and an unknown/null activity resolves to the RESTRICTED default.
func TestResolveSizerConfig_ReadsPerActivitySLAKnobsLiveWithDefaultFallback(t *testing.T) {
	def := resolveSizerConfig(sizerCmd(), nil)
	require.Equal(t, time.Duration(defaultSLAWeakSeconds)*time.Second, def.slaForActivity(shared.ActivityLevelWeak), "no snapshot → WEAK default (360m)")
	require.Equal(t, time.Duration(defaultSLARestrictedSeconds)*time.Second, def.slaForActivity(shared.ActivityLevelRestricted), "RESTRICTED default (135m)")
	require.Equal(t, time.Duration(defaultSLAGrowingSeconds)*time.Second, def.slaForActivity(shared.ActivityLevelGrowing), "GROWING default (45m)")
	require.Equal(t, time.Duration(defaultSLAStrongSeconds)*time.Second, def.slaForActivity(shared.ActivityLevelStrong), "STRONG default (22m)")
	require.Equal(t, time.Duration(defaultSLARestrictedSeconds)*time.Second, def.slaForActivity(shared.ActivityLevel("")), "unknown/null activity resolves to the RESTRICTED default")

	live := liveconfig.Snapshot{"sla_seconds_weak": 9999, "sla_seconds_strong": 111}
	got := resolveSizerConfig(sizerCmd(), live)
	require.Equal(t, 9999*time.Second, got.slaForActivity(shared.ActivityLevelWeak), "live snapshot overrides WEAK next tick")
	require.Equal(t, 111*time.Second, got.slaForActivity(shared.ActivityLevelStrong), "live snapshot overrides STRONG next tick")
	require.Equal(t, time.Duration(defaultSLAGrowingSeconds)*time.Second, got.slaForActivity(shared.ActivityLevelGrowing), "an unset knob under a live snapshot still falls back to its documented default")
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

// sp-tor9 config wiring: the breach-response knob is live-tunable. resolveSizerConfig reads
// breach_response_percent from the tick's live-config snapshot and falls back to its documented
// default with no snapshot — guarding the registry↔overlay drift that would leave the knob
// registered but silently ineffective.
func TestResolveSizerConfig_ReadsBreachResponseKnobLiveWithDefaultFallback(t *testing.T) {
	def := resolveSizerConfig(sizerCmd(), nil)
	require.Equal(t, defaultBreachResponsePercent, def.BreachResponsePercent,
		"no snapshot → documented breach-response default")

	live := liveconfig.Snapshot{"breach_response_percent": 150}
	got := resolveSizerConfig(sizerCmd(), live)
	require.Equal(t, 150, got.BreachResponsePercent, "live snapshot overrides the breach response next tick")
}

// ---- sp-tor9: size from the empirical circuit, respond to breach proportionally ----

// The VB74/DF86 incident, UPDATED OPENLY to sp-r57g percentile semantics (this test formerly fed
// the scalar MAX age; sp-r57g SUPERSEDES that premise — the driving age is now the value-weighted
// P90, and the max tail is tolerated). A high-market system whose P90 market age exceeds the SLA is
// sized UP to the N that brings the P90 under-SLA in ONE resize, not the slow +1 nudge. 26 markets,
// 4 fully-manned probes: 24 markets at exactly the breaching P90 age (5640s = 94min) plus a
// DEEPER stale tail (two markets at 9000s). The measured-cycle model collapses toward 1-2 probes
// here (the pooled inter-scan interval deflates with probe count), so the old closed loop could
// only reach current+1 = 5. Sizing from the P90 through the REUSED sp-tor9 circuit machinery
// (ceil(4×5640/3600)=7) reaches coverage at once — and the 9000s tail is TOLERATED, NOT chased to
// ceil(4×9000/3600)=10. Flattening the response to +1 sizes it to 5 and fails this test.
func TestSizer_ProportionallyRaisesFullyMannedBreachingHighMarketSystem(t *testing.T) {
	markets := append(freshMarkets(24, 5640, 1), mkt(9000, 1), mkt(9000, 1)) // P90 (24th of 26) = 5640
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snapWithMarkets("X1-VB74", 120, 25, markets),
	}}
	pr := newSizerPostRepo(fullyMannedSizerPost("X1-VB74", 4))
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
	h := newSizer(fr, pr, fl)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Equal(t, 7, pr.hullUpdates["X1-VB74"],
		"sized from the breaching P90 via the reused sp-tor9 circuit (ceil(4×5640/3600)=7); the 9000s tail is tolerated, not chased to 10")
	require.Empty(t, pr.upserts, "the raise goes through the manning-preserving UpdateHulls seam, never a clobbering Upsert")
}

// The breach response is PROPORTIONAL to severity, not a flat +1: the same fully-manned 4-probe
// post breaching mildly is raised less than when it breaches severely. Flattening the response to
// current+1 would size BOTH at 5 and fail the severe case (the mutation guard).
func TestSizer_BreachResponseScalesWithSeverity(t *testing.T) {
	cases := []struct {
		name          string
		oldestAgeSecs float64
		wantHulls     int
	}{
		// ceil(4×3960/3600)=ceil(4.4)=5 — a mild 1.1× breach nudges a single probe.
		{"mild breach nudges one probe", 3960, 5},
		// ceil(4×6300/3600)=ceil(7.0)=7 — a 1.75× breach raises proportionally further.
		{"severe breach raises proportionally further", 6300, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
				snap("X1-VB74", 26, tc.oldestAgeSecs, 120, 25),
			}}
			pr := newSizerPostRepo(fullyMannedSizerPost("X1-VB74", 4))
			fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
			h := newSizer(fr, pr, fl)

			require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

			require.Equal(t, tc.wantHulls, pr.hullUpdates["X1-VB74"],
				"the breach response scales with the age/SLA ratio, never a flat +1")
		})
	}
}

// DISJOINTNESS (sp-iupr issue 1 preserved): the circuit raise is for TRUSTED telemetry ONLY. The
// SAME 26-market post, fully manned and breaching at 94min, but TELEMETRY-STARVED (its probes are
// not producing scan intervals) is NOT circuit-raised — its age is a MANNING signal, not a
// capacity shortfall, so it stays on the static market-count model (ceil(26×180/3600)=2) and
// steps DOWN toward it, never up to 7. Same markets, same breaching age, opposite outcome from the
// trusted case above — selected purely by trusted-vs-starved.
func TestSizer_StarvedBreachingHighMarketSystemStaysOnMarketCountModel(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-VB74", 26, 5640, 0 /*no cycle*/, 0 /*starved*/),
	}}
	pr := newSizerPostRepo(fullyMannedSizerPost("X1-VB74", 4))
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
	h := newSizer(fr, pr, fl)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Equal(t, 3, pr.hullUpdates["X1-VB74"],
		"a starved breaching post is NOT circuit-raised — it steps down toward the market-count floor (ceil(26×180/3600)=2)")
}

// NO RELEASE-FLAP: once VB74 is correctly sized to 7 probes its worst-case age settles under the
// SLA (here 3300s at 7 probes). The static market-count model would read this as a 6-probe
// surplus (26×120/3600=1) and, after the release window, shed toward 1 — re-breaching, then
// re-raising: a flap. The circuit-observed fixpoint (ceil(7×3300/3600)=7) recognizes the post is
// correctly sized and HOLDS it, even across a full release window. A just-raised, correctly-sized
// post is never shed.
func TestSizer_FullyMannedPostAtCircuitFixpointHoldsAcrossReleaseWindow(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-VB74", 26, 3300, 120, 25),
	}}
	pr := newSizerPostRepo(fullyMannedSizerPost("X1-VB74", 7))
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
	h, clock := newSizerWithClock(fr, pr, fl)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))
	clock.Advance(301 * time.Second) // past the default 300s release window
	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	_, resized := pr.hullUpdates["X1-VB74"]
	require.False(t, resized, "a fully-manned post at its circuit fixpoint holds across the release window — no flap")
}

// AGGREGATE DEMAND now reflects true SLA need. Two fully-manned 26-market posts breaching at 94min,
// each currently 4 probes. The old +1 sanity floor sized each to 5 (aggregate 10); the circuit
// response sizes each to 7 (aggregate 14). With a supply of 10 — which COVERED the old demand —
// the sizer now correctly sees the shortfall and buys toward the raised fleet cap.
func TestSizer_AggregateDemandClimbsToTrueSLANeedForBreachingFleet(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-VB74", 26, 5640, 120, 25),
		snap("X1-DF86", 26, 5640, 120, 25),
	}}
	pr := newSizerPostRepo(
		fullyMannedSizerPost("X1-VB74", 4),
		fullyMannedSizerPost("X1-DF86", 4),
	)
	fl := &fakeSizerFleetRepo{all: scouts(t, 10)} // supply 10 covered the OLD demand (5+5), not the new (7+7)
	h := newSizer(fr, pr, fl)
	pu := &fakePurchaser{quotePrice: 5000, buySymbol: "PROBE-NEW"}
	h.SetProbePurchaser(pu)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Equal(t, 7, pr.hullUpdates["X1-VB74"], "each breaching post is sized to its circuit target")
	require.Equal(t, 7, pr.hullUpdates["X1-DF86"], "each breaching post is sized to its circuit target")
	require.Equal(t, 1, pu.buyCalls,
		"aggregate demand (14) now outruns the supply (10) that covered the old +1 demand (10) — the sizer buys")
}

// ---- sp-iopd: reserved FRONTIER floor (freshness holds against pool − N) --------------

// THE sp-iopd MVP (freshness side): with the reserved frontier floor engaged, the sizer HOLDS its
// AGGREGATE footprint against (supply − floor) and RELEASES the surplus, so the frontier keeps its
// reserved probes even when raw freshness demand exceeds the whole pool (the live starvation case).
// Two market-rich systems each seed to the per-system cap (8) → raw aggregate 16 against a 14-probe
// pool. floor 0 is exact pre-sp-iopd behavior (sizes to 16, buys toward it — holding the pool, the
// starvation); floor 6 caps the aggregate at 14−6=8, leaving 6 idle for the frontier and never
// buying into them. The floor-6 row is ALSO the mutation guard: removing the freshness-side
// subtraction (supply−floor → supply) sizes to 16 again, re-consuming the reserved 6 → it fails.
func TestSizer_ReservedFrontierFloorHoldsAggregateAgainstReducedPool(t *testing.T) {
	const pool = 14
	cases := []struct {
		name          string
		floor         int
		wantAggregate int // total hulls declared across the two posts
		wantBuys      int
		wantIdleFree  int // pool − aggregate, the probes left for the frontier
	}{
		{"floor 0 is pre-sp-iopd: sizes to raw demand (16) and buys, holding the pool", 0, 16, 1, pool - 16},
		{"floor 6 holds the aggregate at supply−floor (8), leaving 6 for the frontier, no buy", 6, 8, 0, 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
				snap("X1-A", 160, 28800, 0, 0), // telemetry-starved → seeds to the per-system cap (8)
				snap("X1-B", 160, 28800, 0, 0), // same → raw aggregate 16, EXCEEDING the 14-probe pool
			}}
			pr := newSizerPostRepo()                        // no existing posts → declare path
			fl := &fakeSizerFleetRepo{all: scouts(t, pool)} // pool of 14 < raw demand 16
			h := newSizer(fr, pr, fl)
			pu := &fakePurchaser{quotePrice: 5000, buySymbol: "PROBE-NEW"}
			h.SetProbePurchaser(pu)
			cmd := sizerCmd()
			cmd.ReservedFrontierFloor = tc.floor

			require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

			aggregate := 0
			for _, u := range pr.upserts {
				aggregate += u.Hulls
			}
			require.Equal(t, tc.wantAggregate, aggregate,
				"the sizer holds its aggregate footprint against (supply − reserved_frontier_floor)")
			require.Equal(t, tc.wantBuys, pu.buyCalls,
				"with the floor engaged the sizer never buys into the reserved probes")
			require.Equal(t, tc.wantIdleFree, pool-aggregate,
				"the probes the sizer does NOT hold stay idle for the frontier (its reserved floor)")
		})
	}
}

// sp-iopd config wiring (freshness side): the reserved_frontier_floor knob is live-tunable.
// resolveSizerConfig reads it from the tick's live-config snapshot (live > launch), and with NO
// snapshot falls back to the launch command, else the documented default 0 (floor OFF) — guarding
// the registry↔overlay drift that would leave the knob registered but silently ineffective.
func TestResolveSizerConfig_ReadsReservedFrontierFloorLiveWithDefaultFallback(t *testing.T) {
	def := resolveSizerConfig(sizerCmd(), nil)
	require.Equal(t, defaultReservedFrontierFloor, def.ReservedFrontierFloor,
		"no snapshot, no launch value → the documented default (0, floor OFF)")

	launch := sizerCmd()
	launch.ReservedFrontierFloor = 4
	require.Equal(t, 4, resolveSizerConfig(launch, nil).ReservedFrontierFloor,
		"no snapshot → the launch command value governs")

	live := liveconfig.Snapshot{"reserved_frontier_floor": 6}
	require.Equal(t, 6, resolveSizerConfig(launch, live).ReservedFrontierFloor,
		"a live snapshot overrides the launch value next tick")
}

// sp-iopd release path: when the sizer already HOLDS oversized posts, the reserved frontier floor
// RELEASES the surplus through the manning-preserving resize-DOWN seam (UpdateHulls) — never
// retiring the post or selling the hull — so the freed probes land undedicated in the shared pool
// the frontier claims. Two standing posts at 5 probes each (aggregate 10) held against a 12-probe
// pool with a floor of 6 are resized DOWN to aggregate 6, freeing 6 for the frontier, no re-buy.
func TestSizer_ReservedFrontierFloorReleasesExistingSurplusThroughResizeDown(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-A", 100, 28800, 0, 0), // starved → seeds to ceil(100×180/3600)=5
		snap("X1-B", 100, 28800, 0, 0), // same → raw aggregate 10
	}}
	pr := newSizerPostRepo(
		standingSizerPost("X1-A", 5, "PROBE-A"),
		standingSizerPost("X1-B", 5, "PROBE-B"),
	)
	fl := &fakeSizerFleetRepo{all: scouts(t, 12)}
	h := newSizer(fr, pr, fl)
	pu := &fakePurchaser{quotePrice: 5000, buySymbol: "PROBE-NEW"}
	h.SetProbePurchaser(pu)
	cmd := sizerCmd()
	cmd.ReservedFrontierFloor = 6 // effective pool 12 − 6 = 6

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	released := pr.hullUpdates["X1-A"] + pr.hullUpdates["X1-B"]
	require.Equal(t, 6, released, "the aggregate is released DOWN to supply−floor (6), freeing 6 for the frontier")
	require.Empty(t, pr.removed, "release resizes posts DOWN — it never retires them (freed hulls return to the shared pool)")
	require.Empty(t, pr.upserts, "release goes through the manning-preserving UpdateHulls seam, not a clobbering Upsert")
	require.Zero(t, pu.buyCalls, "no-churn: the sizer does not re-buy a hull it just released")
}

// ---- sp-r57g: PERCENTILE-age target (P90), stale tail explicitly tolerated -------------

// THE CORE REFRAME: a big system whose MAX market age breaches the SLA but whose P90 is comfortably
// under it is NOT over-sized — the stale tail is explicitly TOLERATED (DA78-class: a couple of
// stragglers must not drag the whole system's demand up). 26 markets on a fully-manned single probe:
// 24 fresh (600s) and 2 deeply stale (9000s, 2.5× the SLA). The P90 sits in the fresh bulk (600s) so
// the post HOLDS at 1; reverting the target to the max (target_percentile=100) chases the tail and
// over-sizes it to ceil(1×9000/3600)=3. The P100 row is the MUTATION GUARD — it proves the P90 (not
// the max) is what tolerates the tail: restoring the max metric re-inflates demand and this reframe fails.
func TestSizer_MaxBreachesButP90UnderTargetIsNotOversized(t *testing.T) {
	cases := []struct {
		name          string
		percentile    int
		wantResizedTo int // 0 ⇒ held (no hull update recorded)
	}{
		{"P90 tolerates the stale tail — the post is NOT oversized", 90, 0},
		{"reverting the target to the max (P100) chases the tail and over-sizes (mutation guard)", 100, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			markets := append(freshMarkets(24, 600, 1), mkt(9000, 1), mkt(9000, 1))
			fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
				snapWithMarkets("X1-DA78", 120, 25, markets),
			}}
			pr := newSizerPostRepo(fullyMannedSizerPost("X1-DA78", 1))
			fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
			h := newSizer(fr, pr, fl)
			cmd := sizerCmd()
			cmd.TargetPercentile = tc.percentile

			require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

			if tc.wantResizedTo == 0 {
				_, resized := pr.hullUpdates["X1-DA78"]
				require.False(t, resized, "P90 under the SLA holds the post — the stale tail is tolerated, not chased")
			} else {
				require.Equal(t, tc.wantResizedTo, pr.hullUpdates["X1-DA78"],
					"the max-age premise (P100) over-sizes to chase the tail — exactly the mis-allocation sp-r57g kills")
			}
		})
	}
}

// VALUE-WEIGHTING, both directions at the port (sp-r57g's key extension). Two runs, IDENTICAL
// 26-market counts and an IDENTICAL stale market (5640s), differing ONLY in that stale market's
// throughput weight. HIGH value → the value-weighted P90 is pulled up ONTO the stale market, so it
// breaches and the post is RAISED to 7 (the arb core — VB74/DF86/GP32 — stays tight). LOW value →
// the fresh bulk out-weighs the straggler, the P90 stays fresh, and the post is NOT raised: it
// releases toward the model (the low-traffic periphery lags cheaply). Same inputs, opposite
// outcomes, selected purely by per-market value — the property that keeps the arb core tight.
func TestSizer_ValueWeightedPercentileRaisesHighValueStaleMarketToleratesLowValueStraggler(t *testing.T) {
	cases := []struct {
		name        string
		staleWeight float64
		wantHulls   int
	}{
		{"a HIGH-value stale arb market pulls the P90 up — the post is raised to hold it", 100, 7},
		{"an equal-count LOW-value stale straggler stays in the tolerated tail — the post releases", 1, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 25 fresh arb-core markets (600s, unit weight) + one stale market (5640s) of the case's value.
			markets := append(freshMarkets(25, 600, 1), mkt(5640, tc.staleWeight))
			fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
				snapWithMarkets("X1-GP32", 120, 25, markets),
			}}
			pr := newSizerPostRepo(fullyMannedSizerPost("X1-GP32", 4))
			fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
			h := newSizer(fr, pr, fl)

			require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

			require.Equal(t, tc.wantHulls, pr.hullUpdates["X1-GP32"],
				"per-market value decides whether a stale market drives demand up (raise to 7) or is tolerated (release to 3)")
		})
	}
}

// AGGREGATE DEMAND DROPS vs the max-age baseline, and the freed slug RELEASES through the existing
// sp-iupr hysteresis. Four fully-manned 26-market posts currently oversized at the MAX-age level (3
// probes each = aggregate 12), each a fresh bulk (600s) with a 2-market stale tail (9000s). Under
// the P90 target the tail is tolerated, so each post's demand collapses to the model and it RELEASES
// one probe this tick (→2) through the manning-preserving resize-DOWN seam — never retired. Under
// the max premise (target_percentile=100) the SAME fleet chases the tail and RAISES each to the cap
// (8), the ~68-probe over-provisioning sp-r57g exists to kill. Aggregate 8 vs 32 — the freshness saving.
func TestSizer_PercentileDropsAggregateDemandAndReleasesSlugThroughHysteresis(t *testing.T) {
	cases := []struct {
		name         string
		percentile   int
		wantPerPost  int
		wantReleased bool // true ⇒ resize-DOWN (release), false ⇒ raised
	}{
		{"P90 tolerates the tail: aggregate collapses and each post releases a probe", 90, 2, true},
		{"the max-age baseline (P100) chases the tail and over-provisions to the cap", 100, 8, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			systems := []string{"X1-VB74", "X1-DF86", "X1-GP32", "X1-ZK55"}
			snaps := make([]domainScouting.SystemFreshnessSnapshot, 0, len(systems))
			posts := make([]*domainScouting.ScoutPost, 0, len(systems))
			for _, s := range systems {
				markets := append(freshMarkets(24, 600, 1), mkt(9000, 1), mkt(9000, 1))
				snaps = append(snaps, snapWithMarkets(s, 120, 25, markets))
				posts = append(posts, fullyMannedSizerPost(s, 3))
			}
			fr := &fakeFreshnessReader{snapshots: snaps}
			pr := newSizerPostRepo(posts...)
			fl := &fakeSizerFleetRepo{all: scouts(t, 40)}
			h := newSizer(fr, pr, fl)
			cmd := sizerCmd()
			cmd.TargetPercentile = tc.percentile

			require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

			aggregate := 0
			for _, s := range systems {
				require.Equal(t, tc.wantPerPost, pr.hullUpdates[s], "each post sized to the percentile target")
				aggregate += pr.hullUpdates[s]
			}
			require.Equal(t, tc.wantPerPost*len(systems), aggregate, "aggregate demand tracks the percentile, not the tail")
			require.Empty(t, pr.removed, "release resizes posts DOWN — the freed slug returns to the shared pool, never retired")
			if tc.wantReleased {
				require.Empty(t, pr.upserts, "the release flows through the manning-preserving UpdateHulls seam")
			}
		})
	}
}

// COMPOSES WITH sp-iopd: the reserved frontier floor still caps PERCENTILE-driven demand against
// (supply − floor). Two fully-manned 26-market posts whose breaching P90 (5640s) sizes each to the
// circuit target 7 (raw aggregate 14) are held against a 14-probe pool with a floor of 6 → the
// aggregate is RELEASED down to 14−6=8 through the same resize-DOWN seam, leaving 6 for the frontier
// and buying nothing. Percentile lowers per-system demand; the reserved-floor ceiling still caps the total.
func TestSizer_PercentileDemandStillCappedByReservedFrontierFloor(t *testing.T) {
	const pool = 14
	markets := append(freshMarkets(24, 5640, 1), mkt(9000, 1), mkt(9000, 1)) // P90=5640 breaching → circuit 7
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snapWithMarkets("X1-A", 120, 25, markets),
		snapWithMarkets("X1-B", 120, 25, markets),
	}}
	pr := newSizerPostRepo(
		fullyMannedSizerPost("X1-A", 7),
		fullyMannedSizerPost("X1-B", 7),
	)
	fl := &fakeSizerFleetRepo{all: scouts(t, pool)}
	h := newSizer(fr, pr, fl)
	pu := &fakePurchaser{quotePrice: 5000, buySymbol: "PROBE-NEW"}
	h.SetProbePurchaser(pu)
	cmd := sizerCmd()
	cmd.ReservedFrontierFloor = 6

	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	aggregate := pr.hullUpdates["X1-A"] + pr.hullUpdates["X1-B"]
	require.Equal(t, 8, aggregate, "percentile-driven demand (7+7) is held against supply−floor (14−6=8)")
	require.Empty(t, pr.removed, "the floor releases through resize-DOWN, never retiring a post")
	require.Zero(t, pu.buyCalls, "with the floor engaged the sizer never buys into the reserved probes")
}

// sp-r57g config wiring: the two new knobs resolve live > launch > default. target_percentile is a
// standard positive-int knob (default 90); value_weighted is the int-mode toggle (2=on default,
// 1=off) that stays live-tunable in BOTH directions despite the registry's 0=revert overload — a
// live snapshot can re-enable a launch-disabled weighting, or disable it live if it misbehaves.
func TestResolveSizerConfig_ReadsPercentileKnobsLiveWithDefaultFallback(t *testing.T) {
	def := resolveSizerConfig(sizerCmd(), nil)
	require.Equal(t, defaultTargetPercentile, def.TargetPercentile, "no snapshot, no launch → the documented percentile default (90)")
	require.True(t, def.ValueWeighted, "no snapshot, no launch → value-weighting defaults ON")

	launch := sizerCmd()
	launch.TargetPercentile = 95
	launch.ValueWeightedMode = valueWeightedModeOff
	got := resolveSizerConfig(launch, nil)
	require.Equal(t, 95, got.TargetPercentile, "no snapshot → the launch percentile governs")
	require.False(t, got.ValueWeighted, "no snapshot → the launch value-weighting mode governs (off)")

	live := liveconfig.Snapshot{"target_percentile": 80, "value_weighted": valueWeightedModeOff}
	liveGot := resolveSizerConfig(launch, live)
	require.Equal(t, 80, liveGot.TargetPercentile, "a live snapshot overrides the percentile next tick")
	require.False(t, liveGot.ValueWeighted, "a live snapshot can disable value-weighting next tick")

	reEnabled := resolveSizerConfig(launch, liveconfig.Snapshot{"value_weighted": valueWeightedModeOn})
	require.True(t, reEnabled.ValueWeighted, "a live snapshot can re-enable the value-weighting the launch disabled")
}

// ---- sp-u8jc/sp-gucu: hold charted-but-unscanned market posts (bootstrap-catch-22 fix) ----

// THE ROOT DEPTH-BLOCKER FIX. The freshness census keys "markets" on SCANNED market_data, so a
// CHARTED dense hub (its waypoints carry the MARKETPLACE trait) that has never been scanned reads
// as 0 markets and — pre-fix — its standing post is retired "its markets are gone", so the probe
// never goes and the system stays dark forever. Armed, such a system is HELD for its initial scan
// (NOT retired). A genuinely empty system (no marketplace waypoints charted) still retires. Disabled
// (the default) is byte-identical to today: it retires. This is the retire-CLASSIFICATION matrix; the
// mutation guard is the armed-charted row — revert the hold guard and it retires, failing the test.
func TestSizer_HoldsChartedUnscannedPostWhenArmed_RetiresGenuinelyEmpty(t *testing.T) {
	cases := []struct {
		name         string
		armed        bool
		chartedCount int // marketplace waypoints charted in the hub's system (0 = genuinely empty)
		wantRetired  bool
	}{
		{"armed + charted marketplaces but unscanned → HELD (needs initial scan)", true, 26, false},
		{"armed + no marketplace waypoints (genuinely empty) → retired", true, 0, true},
		{"disabled + charted marketplaces but unscanned → retired (byte-identical to today)", false, 26, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A market-bearing (scanned) system keeps the census non-empty so the fail-safe
			// empty-census guard does not mask the retire decision under test.
			fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
				snap("X1-SCANNED", 10, 100, 120, 59),
			}}
			pr := newSizerPostRepo(
				standingSizerPost("X1-SCANNED", 1, "P-SCANNED"),
				standingSizerPost("X1-DENSEHUB", 1, "P-HUB"), // the charted-unscanned candidate
			)
			fl := &fakeSizerFleetRepo{all: scouts(t, 10)}
			h := newSizer(fr, pr, fl)
			charted := map[string]int{"X1-SCANNED": 10} // the scanned system is charted too (market-bearing → guard moot)
			if tc.chartedCount > 0 {
				charted["X1-DENSEHUB"] = tc.chartedCount
			}
			h.SetChartedMarketplaceReader(&fakeChartedMarketplaceReader{counts: charted})
			cmd := sizerCmd()
			if tc.armed {
				cmd.HoldUnscannedMarketPosts = 1
			}

			require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

			if tc.wantRetired {
				require.Contains(t, pr.removed, "X1-DENSEHUB",
					"an unscanned post with no charted marketplaces (or with the fix disabled) retires as today")
			} else {
				require.NotContains(t, pr.removed, "X1-DENSEHUB",
					"a charted-with-marketplaces-but-unscanned post is HELD for its initial scan, never retired 'markets gone'")
			}
			require.NotContains(t, pr.removed, "X1-SCANNED", "a market-bearing system is never retired")
		})
	}
}

// The held post must also PULL a probe: a charted-but-unscanned system counts as ONE-probe
// initial-scan demand so the aggregate buy provisions capacity for the reconciler/relay to man it
// (in-system idle, the sp-u8jc cross-system relay, or a probe buy) — after which its first scan
// lands market_data and it enters the normal census-sized rotation. The demand is bounded to ONE
// probe, NOT scaled by the 26 charted marketplace waypoints: the supply-1 no-buy row proves the
// bound (a 26-scaled demand would outrun a single probe and buy). Disabled adds no demand.
// Mutation guards: drop the demand-add → the zero-supply armed row stops buying; scale demand by
// the marketplace count → the one-probe-supply armed row starts buying. Either fails the test.
func TestSizer_ChartedUnscannedSystemCountsAsBoundedInitialScanDemand(t *testing.T) {
	cases := []struct {
		name     string
		armed    bool
		supply   int
		wantBuys int
	}{
		{"armed + unscanned hub + zero supply → buy (initial-scan demand outruns supply)", true, 0, 1},
		{"armed + unscanned hub + one-probe supply → no buy (demand is ONE, not marketplace-count-scaled)", true, 1, 0},
		{"disabled + unscanned hub + zero supply → no buy (no initial-scan demand)", false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeFreshnessReader{snapshots: nil}                      // nothing scanned yet — the bootstrap state
			pr := newSizerPostRepo(standingSizerPost("X1-DENSEHUB", 1, "")) // declared, awaiting its first scan
			fl := &fakeSizerFleetRepo{all: scouts(t, tc.supply)}
			h := newSizer(fr, pr, fl)
			pu := &fakePurchaser{quotePrice: 5000, buySymbol: "PROBE-NEW"}
			h.SetProbePurchaser(pu)
			// 26 charted marketplace waypoints, ZERO scanned — the dense-hub bootstrap case.
			h.SetChartedMarketplaceReader(&fakeChartedMarketplaceReader{counts: map[string]int{"X1-DENSEHUB": 26}})
			cmd := sizerCmd()
			if tc.armed {
				cmd.HoldUnscannedMarketPosts = 1
			}

			require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

			require.Equal(t, tc.wantBuys, pu.buyCalls,
				"a charted-unscanned post demands exactly ONE initial-scan probe when armed, and none when disabled")
		})
	}
}

// Config wiring: hold_unscanned_market_posts is a live-tunable int-mode flag. resolveSizerConfig
// reads it from the tick's live-config snapshot (live > launch); with NO snapshot it falls back to
// the launch command, else the documented default (OFF, retire-as-gone) — guarding the registry↔
// overlay drift that would leave the knob registered but silently ineffective.
func TestResolveSizerConfig_ReadsHoldUnscannedMarketPostsLiveWithDefaultFallback(t *testing.T) {
	require.False(t, resolveSizerConfig(sizerCmd(), nil).HoldUnscannedMarketPosts,
		"no snapshot, no launch value → the documented default (OFF, retire-as-gone)")

	launch := sizerCmd()
	launch.HoldUnscannedMarketPosts = 1
	require.True(t, resolveSizerConfig(launch, nil).HoldUnscannedMarketPosts,
		"no snapshot → the launch command value governs")

	require.True(t, resolveSizerConfig(sizerCmd(), liveconfig.Snapshot{"hold_unscanned_market_posts": 1}).HoldUnscannedMarketPosts,
		"a live snapshot arms it next tick")

	require.False(t, resolveSizerConfig(launch, liveconfig.Snapshot{"hold_unscanned_market_posts": 0}).HoldUnscannedMarketPosts,
		"a live 0 reverts even a launch-armed flag (tune 0 = off)")
}

// ---- cold-start scale-up — size a partially-scanned system by its CHARTED market count (default) ----

// THE COLD-START SCALE-UP FIX (the live-gap regression). During the initial circuit the freshness
// census keys "markets" on SCANNED market_data, so a home post part-way through its first circuit
// (10 of 21 charted markets scanned) reads as 10 markets. Two things kept it stuck at one hull:
// the market-count numerator was the scanned 10, AND the measured per-market cycle is taken from that
// same partial subset — a cold-start burst-and-idle artifact (median gap 139s, BELOW the 3600/21≈171s
// a second hull needs), so even sizing off the full charted 21 yields RequiredHulls(21,139,3600)=1.
// Sizing off the full CHARTED count AND the fixed SEED cycle (the scout-post coordinator's undersized-
// warning avgHop) restores RequiredHulls(21,180,3600)=2 — the two coordinators now AGREE. The
// deflated 139s cycle is the anti-theatre teeth: it FAILS a count-only fix (stays 1) AND a
// seed-only fix without the count (RequiredHulls(10,180,3600)=1). Reader unwired ⇒ charted 0 ⇒
// scanned-count fallback (byte-identical). Idempotent once already sized.
func TestSizer_ColdStartSizesOffChartedMarketsAndSeedCycle(t *testing.T) {
	cases := []struct {
		name          string
		readerWired   bool
		existingHulls int // the bootstrap-declared / current post budget
		wantHulls     int // expected UpdateHulls value; 0 ⇒ no resize (fallback / idempotent)
	}{
		{"21 charted over 10 scanned, deflated 139s cycle, 1 hull → resize to 2", true, 1, 2},
		{"charted reader unwired → no resize (fail-closed fallback to the scanned count)", false, 1, 0},
		{"already sized to 2 → no resize (idempotent, no re-partition thrash)", true, 2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 10 of the system's 21 charted markets scanned, their inter-scan gaps a cold-start burst →
			// measured cycle 139s, trusted (9 samples, not starved), fresh (age well under the 1h SLA).
			fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
				snap("X1-VR39", 10 /*scanned*/, 100 /*fresh oldest*/, 139 /*deflated measured cycle*/, 9 /*trusted*/),
			}}
			var post *domainScouting.ScoutPost
			if tc.existingHulls > 1 {
				post = fullyMannedSizerPost("X1-VR39", tc.existingHulls)
			} else {
				post = standingSizerPost("X1-VR39", tc.existingHulls, "P-VR39")
			}
			pr := newSizerPostRepo(post)
			fl := &fakeSizerFleetRepo{all: scouts(t, 10)} // supply ≫ demand → no buy in the way
			h := newSizer(fr, pr, fl)
			if tc.readerWired {
				h.SetChartedMarketplaceReader(&fakeChartedMarketplaceReader{counts: map[string]int{"X1-VR39": 21}})
			}

			require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

			if tc.wantHulls == 0 {
				require.NotContains(t, pr.hullUpdates, "X1-VR39",
					"no resize expected — the post stays at its current budget")
			} else {
				require.Equal(t, tc.wantHulls, pr.hullUpdates["X1-VR39"],
					"sized against 21 CHARTED markets at the seed 180s cycle → RequiredHulls(21,180,3600)=2")
				require.Empty(t, pr.upserts,
					"the resize goes through the manning-preserving UpdateHulls seam, never a clobbering Upsert")
			}
		})
	}
}

// STEADY-STATE GUARD: charted sizing being the default must NOT over-provision. The charted override
// is scoped to the cold-start window (charted > scanned): a FULLY-scanned system sizes on its real
// MEASURED cycle, not the seed (proven by choosing a measured cycle whose RequiredHulls differs from
// the seed's); and a cold-start system with a huge charted count is still bounded by the existing
// per-system cap. Guards against the default silently sizing every system off the seed forever.
func TestSizer_ChartedSizingDefaultDoesNotOverProvision(t *testing.T) {
	t.Run("fully scanned (charted == scanned) sizes on the measured cycle, not the seed", func(t *testing.T) {
		// 30 markets, all scanned, measured 300s cycle (trusted): RequiredHulls(30,300,3600)=3. Were the
		// seed cycle wrongly applied it would be RequiredHulls(30,180,3600)=2 — so 3 proves measured is used.
		fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
			snap("X1-FULL", 30 /*scanned*/, 100 /*fresh*/, 300 /*measured cycle*/, 29 /*trusted*/),
		}}
		pr := newSizerPostRepo(standingSizerPost("X1-FULL", 1, "P-FULL"))
		fl := &fakeSizerFleetRepo{all: scouts(t, 10)}
		h := newSizer(fr, pr, fl)
		h.SetChartedMarketplaceReader(&fakeChartedMarketplaceReader{counts: map[string]int{"X1-FULL": 30}}) // charted == scanned

		require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

		require.Equal(t, 3, pr.hullUpdates["X1-FULL"],
			"fully scanned → the override is inert (charted ≤ scanned), so it sizes on the measured 300s cycle → 3, not the seed's 2")
	})

	t.Run("cold-start charted sizing is bounded by max_probes_per_system", func(t *testing.T) {
		// 200 charted over 5 scanned at the seed cycle → RequiredHulls(200,180,3600)=10, capped to the
		// per-system max of 8 — the default cannot run demand past the existing cap.
		fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
			snap("X1-BIG", 5 /*scanned*/, 100 /*fresh*/, 139 /*measured cycle*/, 4 /*trusted*/),
		}}
		pr := newSizerPostRepo(standingSizerPost("X1-BIG", 1, "P-BIG"))
		fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
		h := newSizer(fr, pr, fl)
		h.SetChartedMarketplaceReader(&fakeChartedMarketplaceReader{counts: map[string]int{"X1-BIG": 200}})

		require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

		require.Equal(t, defaultMaxProbesPerSystem, pr.hullUpdates["X1-BIG"],
			"cold-start charted sizing is clamped to the per-system hull cap (8), never the raw RequiredHulls(200,180,3600)=10")
	})
}

// HOME MANNING FLOOR (sp-2ci9y): the home post carries a permanent MinHulls floor (probe_target)
// so the freshsizer never sizes it below the probes bootstrap bought for the home scan. Same
// fixture (26 markets, telemetry-starved → seed 180s cycle → SLA RequiredHulls=2) both ways: an
// UN-floored post (MinHulls 0, every non-home post) sizes to the SLA minimum 2 — byte-identical to
// pre-sp-2ci9y — while the floored home post is pinned UP to 3. On pre-fix code BOTH size to 2, so
// the floored case is the reproduction of the strand (the 3rd bought probe left idle).
func TestSizer_HomePostFlooredToProbeTarget(t *testing.T) {
	cases := []struct {
		name      string
		minHulls  int
		wantHulls int
	}{
		{"unfloored post (MinHulls 0) sizes to the SLA minimum", 0, 2},
		{"home floor (MinHulls probe_target=3) pins the post up to 3", 3, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
				snap("X1-JC27", 26, 100 /*fresh*/, 0 /*no measured cycle*/, 1 /*starved → seed 180s*/),
			}}
			home := standingSizerPost("X1-JC27", 1, "PROBE-MANNED")
			home.MinHulls = tc.minHulls
			pr := newSizerPostRepo(home)
			fl := &fakeSizerFleetRepo{all: scouts(t, 3)} // supply covers → isolate sizing from the buy
			h := newSizer(fr, pr, fl)

			require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

			require.Equal(t, tc.wantHulls, pr.hullUpdates["X1-JC27"],
				"26 markets @ seed 180s / 3600s SLA = 2; the home floor raises manning to probe_target")
			require.Empty(t, pr.upserts, "resize goes through the narrow UpdateHulls seam, never a full Upsert")
		})
	}
}

// FLOOR IS A FLOOR, NOT A CAP (sp-2ci9y): if the SLA genuinely demands MORE than probe_target the
// home post still sizes UP. A fully-manned home post breaching its SLA 8× is raised to 8 probes —
// the floor of 3 does not cap it. Guards against a min()-instead-of-max() mis-implementation (which
// would clamp the raise back down to 3).
func TestSizer_HomeFloorDoesNotCapAnSLARaise(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-JC27", 26, 28800 /*8h stale vs 1h SLA*/, 120, 25),
	}}
	home := fullyMannedSizerPost("X1-JC27", 1)
	home.MinHulls = 3 // the home floor is below the SLA-driven target
	pr := newSizerPostRepo(home)
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
	h := newSizer(fr, pr, fl)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Equal(t, 8, pr.hullUpdates["X1-JC27"],
		"an 8× SLA breach raises the home post to 8 — the probe_target floor (3) never caps a legitimate SLA raise")
}

// NO EXTRA BUY FROM THE FLOOR (sp-2ci9y, RULINGS #4): the floor is a MANNING floor, not a spend
// change. The home post is floored to 3 (probe_target) while its SLA buy-demand stays 2. With supply
// exactly 2, the un-floored buy-demand (2) is covered — no purchase — even though the post is manned
// to 3. Were the floor leaking into the buy-demand (3 > 2 supply), the guarded buyer would double-buy
// against bootstrap's single-buyer probe purchase; this proves it does not.
func TestSizer_HomeFloorDoesNotInflateBuyDemand(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snap("X1-JC27", 26, 100 /*fresh*/, 0, 1 /*starved → seed 180s → SLA demand 2*/),
	}}
	home := standingSizerPost("X1-JC27", 1, "PROBE-MANNED")
	home.MinHulls = 3
	pr := newSizerPostRepo(home)
	fl := &fakeSizerFleetRepo{all: scouts(t, 2)} // supply 2 == SLA buy-demand 2 (the floor of 3 must NOT tip a buy)
	h := newSizer(fr, pr, fl)
	pu := &fakePurchaser{quotePrice: 5000, buySymbol: "PROBE-NEW"}
	h.SetProbePurchaser(pu)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Equal(t, 0, pu.buyCalls,
		"the manning floor must not inflate buy-demand: SLA demand 2 == supply 2, so no probe is bought")
	require.Equal(t, 3, pr.hullUpdates["X1-JC27"],
		"the post is still MANNED to the floor (3) — the floor drives manning, not the buy")
}

// ---- sp-wuksw: demand-weighted freshness (realized SELL demand supersedes intrinsic) ----

// mktWP is mkt() with the waypoint identity the demand re-weighting keys on.
func mktWP(waypoint string, ageSecs, intrinsicWeight float64) domainScouting.MarketFreshnessSample {
	return domainScouting.MarketFreshnessSample{Waypoint: waypoint, AgeSeconds: ageSecs, Weight: intrinsicWeight}
}

// freshMarketsWP builds n fresh markets (age 600s) each with a distinct waypoint and the given
// intrinsic weight — the arb-core bulk a stale market is appended to in the demand fixtures.
func freshMarketsWP(prefix string, n int, intrinsicWeight float64) []domainScouting.MarketFreshnessSample {
	out := make([]domainScouting.MarketFreshnessSample, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, mktWP(fmt.Sprintf("%s-%d", prefix, i), 600, intrinsicWeight))
	}
	return out
}

// sellLegAt builds a realized SELL leg (a sink hit) at a waypoint, realized now (full weight).
func sellLegAt(waypoint string, units, price int, at time.Time) trading.TourLegTelemetry {
	return trading.TourLegTelemetry{
		Waypoint: waypoint, Good: "PRECIOUS_STONES", IsBuy: false,
		RealizedUnits: units, RealizedUnitPrice: price, RealizedAt: at, PlannedAt: at, PlayerID: 1,
	}
}

// HEADLINE (sp-wuksw test #1): the WEIGHT SOURCE for the value-weighted freshness percentile is
// REALIZED SELL DEMAND, not intrinsic Σ(trade_volume × price). The two cases flip the sizing
// decision on a 25-fresh + 1-stale system (the exact 7-raised / 3-released numbers the intrinsic
// value-weighted test uses), driven purely by where the FLEET earns:
//   - a stale market the fleet SELLS THROUGH (high realized demand, low intrinsic) pulls the P90
//     onto itself and the post is RAISED to hold it (7) — where intrinsic weighting would have
//     tolerated it as a low-value straggler (released to 3);
//   - a stale market with HIGH intrinsic but ZERO realized demand, sitting behind a fresh arb core
//     the fleet DOES earn through, is TOLERATED (released to 3) so it breaches — where intrinsic
//     weighting would have chased it to the circuit target (raised to 7), starving the real sinks.
//
// RED (today, no demand reader): intrinsic weight decides — case 1 releases (3), case 2 raises (7).
// GREEN: realized demand decides — case 1 raises (7), case 2 tolerates (3).
func TestSizer_DemandWeightSupersedesIntrinsicAsPercentileWeightSource(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name      string
		markets   []domainScouting.MarketFreshnessSample
		legs      []trading.TourLegTelemetry
		wantHulls int
	}{
		{
			name: "a stale sink the fleet SELLS THROUGH is raised (demand beats low intrinsic)",
			// 25 fresh low-intrinsic markets + one stale market with the SAME low intrinsic weight
			// but heavy realized sell-demand. Intrinsic weighting tolerates it; demand raises it.
			markets:   append(freshMarketsWP("X1-DW-CORE", 25, 1), mktWP("X1-DW-SINK", 5640, 1)),
			legs:      []trading.TourLegTelemetry{sellLegAt("X1-DW-SINK", 1000, 100, now)}, // 100k realized
			wantHulls: 7,
		},
		{
			name: "a stale HIGH-intrinsic market with ZERO realized demand is tolerated (breaches)",
			// 25 fresh markets — one of which is the fleet's real demand sink — plus a stale market
			// with high intrinsic value but no realized demand. Intrinsic weighting chases the stale
			// straggler; demand keeps the P90 on the fresh sink and lets the straggler breach.
			markets: append(
				append(freshMarketsWP("X1-DW-CORE", 24, 1), mktWP("X1-DW-SINK", 600, 1)),
				mktWP("X1-DW-PERIPHERY", 5640, 100),
			),
			legs:      []trading.TourLegTelemetry{sellLegAt("X1-DW-SINK", 1000, 100, now)}, // 100k realized on a FRESH sink
			wantHulls: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
				snapWithMarkets("X1-DW32", 120, 25, tc.markets),
			}}
			pr := newSizerPostRepo(fullyMannedSizerPost("X1-DW32", 4))
			fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
			h := newSizer(fr, pr, fl)
			h.SetTourTelemetryReader(&fakeTourTelemetryReader{legs: tc.legs})

			require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

			require.Equal(t, tc.wantHulls, pr.hullUpdates["X1-DW32"],
				"realized sell-demand, not intrinsic value, decides which stale market drives the freshness percentile")
		})
	}
}

// BYTE-IDENTICAL FALLBACK (sp-wuksw test #2): with NO realized-demand telemetry (empty read, or
// no reader wired at all) the sizer output is identical to today's intrinsic value-weighting — the
// cold-start prior. The fixture is case 2's high-intrinsic stale straggler, which intrinsic
// weighting RAISES to 7: it must raise to 7 both with an empty telemetry reader and with none.
func TestSizer_ByteIdenticalToIntrinsicWhenNoDemandTelemetry(t *testing.T) {
	markets := append(
		append(freshMarketsWP("X1-CS-CORE", 24, 1), mktWP("X1-CS-SINK", 600, 1)),
		mktWP("X1-CS-PERIPHERY", 5640, 100),
	)
	run := func(wireEmptyReader bool) int {
		fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
			snapWithMarkets("X1-CS32", 120, 25, markets),
		}}
		pr := newSizerPostRepo(fullyMannedSizerPost("X1-CS32", 4))
		fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
		h := newSizer(fr, pr, fl)
		if wireEmptyReader {
			h.SetTourTelemetryReader(&fakeTourTelemetryReader{legs: nil})
		}
		require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))
		return pr.hullUpdates["X1-CS32"]
	}

	require.Equal(t, 7, run(false), "no reader wired → pure intrinsic value-weighting (raises the high-intrinsic straggler to 7)")
	require.Equal(t, 7, run(true), "reader wired but empty → byte-identical to intrinsic value-weighting")
}

// PLAYER SCOPING (sp-wuksw test #4): the coordinator reads demand telemetry scoped to ITS player,
// so another player's realized demand cannot bleed into this player's freshness weighting. The
// coordinator's contract is to pass its own player_id (the repository enforces the row filter).
func TestSizer_DemandReadIsScopedToThePlayer(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		snapWithMarkets("X1-PS32", 120, 25, freshMarketsWP("X1-PS-CORE", 25, 1)),
	}}
	pr := newSizerPostRepo(fullyMannedSizerPost("X1-PS32", 4))
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
	h := newSizer(fr, pr, fl)
	reader := &fakeTourTelemetryReader{legs: nil}
	h.SetTourTelemetryReader(reader)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Equal(t, 1, reader.calls, "the coordinator reads demand telemetry once per tick")
	require.Equal(t, sizerCmd().PlayerID.Value(), reader.calledPlayer,
		"the demand read is scoped to this coordinator's player — another player's demand cannot bleed in")
	require.False(t, reader.sinceArg.IsZero(), "the demand read is bounded to a rolling window, not the full history")
}

// KNOB WIRING (sp-wuksw test #3, config half): the EWMA half-life is a tunable-only knob resolving
// live > default, mirroring the sp-j4kjv per-activity SLA knobs (no launch-command field). The decay
// math itself is proven in TestDemandWeightsBySink_HalfLifeDecaysRealizedValue.
func TestResolveSizerConfig_ReadsDemandHalfLifeLiveWithDefaultFallback(t *testing.T) {
	def := resolveSizerConfig(sizerCmd(), nil)
	require.Equal(t, time.Duration(defaultDemandHalfLifeSeconds)*time.Second, def.DemandHalfLife,
		"no snapshot → the documented demand half-life default")

	live := resolveSizerConfig(sizerCmd(), liveconfig.Snapshot{"demand_ewma_half_life_secs": 7200})
	require.Equal(t, 2*time.Hour, live.DemandHalfLife, "a live snapshot overrides the half-life next tick")

	reverted := resolveSizerConfig(sizerCmd(), liveconfig.Snapshot{"demand_ewma_half_life_secs": 0})
	require.Equal(t, time.Duration(defaultDemandHalfLifeSeconds)*time.Second, reverted.DemandHalfLife,
		"`tune demand_ewma_half_life_secs 0` reverts to the default")
}
