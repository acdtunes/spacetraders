package commands

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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

	require.NoError(t, h.reconcileOnce(context.Background(), sizerCmd()))

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

			require.NoError(t, h.reconcileOnce(context.Background(), cmd))

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

	require.NoError(t, h.reconcileOnce(context.Background(), sizerCmd()))

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

	require.NoError(t, h.reconcileOnce(context.Background(), sizerCmd()))

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

	require.NoError(t, h.reconcileOnce(context.Background(), sizerCmd()))

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

			require.NoError(t, h.reconcileOnce(context.Background(), sizerCmd()))

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

	require.NoError(t, h.reconcileOnce(context.Background(), sizerCmd()))

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

	require.NoError(t, h.reconcileOnce(context.Background(), sizerCmd()))

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

	require.NoError(t, h.reconcileOnce(context.Background(), sizerCmd()))

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

	require.NoError(t, h.reconcileOnce(context.Background(), cmd))

	require.Len(t, pr.upserts, 1)
	require.Equal(t, 4, pr.upserts[0].Hulls, "ceil(60×120/1800)=4 under the 30min override")
	require.Equal(t, 30*time.Minute, pr.upserts[0].FreshnessTarget)
}
