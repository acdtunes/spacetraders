package commands

// sp-rhju Part 3: the ONE-TIME BACKFILL SWEEP. Shipyard scanning historically rode
// only scout MARKET tours, so it lagged the depth frontier — 55 charted systems held
// a shipyard but only 10 were ever scanned (a 45-system blind spot the heavy-freighter
// yard we hunt may already sit in). This coordinator enumerates the CHARTED-but-UNSCANNED
// shipyard systems and declares a bounded, deeper-first batch of sweep-once posts the
// scout reconciler relays a probe to; the probe's arrival rides the sp-rhju decoupled
// shipyard scan (Part 1) and persists the row — closing the catch-up gap without a
// standing market tour.
//
// Port-to-port: enter through the coordinator's ReconcileOnce driving port, assert at
// the scout-post repository (the dispatch boundary — a declared sweep-once post IS the
// dispatch, exactly like the frontier coordinator). Doubles sit only at the charted
// enumerator, the scanned-set reader, the idle-probe counter, and the post repository.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- doubles at the port boundaries ---------------------------------------

type fakeChartedEnumerator struct {
	systems []ChartedShipyardSystem
	err     error
}

func (f *fakeChartedEnumerator) ChartedShipyardSystems(context.Context, int) ([]ChartedShipyardSystem, error) {
	return f.systems, f.err
}

type fakeScannedReader struct {
	systems []string
	err     error
}

func (f *fakeScannedReader) ScannedSystems(context.Context, int) ([]string, error) {
	return f.systems, f.err
}

type fakeIdleProbeCounter struct {
	count int
	err   error
}

func (f *fakeIdleProbeCounter) IdleProbeCount(context.Context, shared.PlayerID) (int, error) {
	return f.count, f.err
}

// fakeBackfillPostRepo records the declared (Upsert'd) posts — the dispatch boundary —
// and serves pre-existing active posts for the in-flight exclusion.
type fakeBackfillPostRepo struct {
	active   []*domainScouting.ScoutPost
	declared []*domainScouting.ScoutPost
}

func (f *fakeBackfillPostRepo) ListActive(context.Context, int) ([]*domainScouting.ScoutPost, error) {
	return f.active, nil
}

func (f *fakeBackfillPostRepo) Upsert(_ context.Context, post *domainScouting.ScoutPost) error {
	f.declared = append(f.declared, post)
	return nil
}

func (f *fakeBackfillPostRepo) Remove(context.Context, int, string) error { return nil }

func (f *fakeBackfillPostRepo) declaredSystems() []string {
	out := make([]string, 0, len(f.declared))
	for _, p := range f.declared {
		out = append(out, p.SystemSymbol)
	}
	return out
}

// --- fixtures ---------------------------------------------------------------

func chartedSystems(prefix string, n, hops int) []ChartedShipyardSystem {
	out := make([]ChartedShipyardSystem, 0, n)
	for i := 0; i < n; i++ {
		sys := prefix + string(rune('A'+i/26)) + string(rune('A'+i%26))
		out = append(out, ChartedShipyardSystem{
			SystemSymbol:     sys,
			ShipyardWaypoint: sys + "-YARD",
			Hops:             hops,
		})
	}
	return out
}

func backfillCtx() context.Context {
	return common.WithPlayerToken(context.Background(), "test-token")
}

func newBackfillHandler(enum ChartedShipyardEnumerator, scanned ScannedShipyardReader, probes IdleProbeCounter, postRepo domainScouting.ScoutPostRepository) *RunShipyardBackfillCoordinatorHandler {
	return NewRunShipyardBackfillCoordinatorHandler(enum, scanned, probes, postRepo, &shared.MockClock{})
}

// --- scenario 2 + 5: enumerate exactly the unscanned, never re-sweep the scanned ---

// GIVEN 55 charted-shipyard systems, 10 of which are already scanned
// WHEN the backfill reconciles with ample probes and a high per-cycle cap
// THEN it declares a sweep-once post for EXACTLY the 45 unscanned systems, and for
// NONE of the 10 already-scanned ones (the scanned-exclusion is the mutation point).
func TestShipyardBackfill_DeclaresExactlyTheUnscannedShipyards(t *testing.T) {
	charted := append(chartedSystems("X1-SCAN-", 10, 1), chartedSystems("X1-BLIND-", 45, 1)...)
	scannedSystems := make([]string, 0, 10)
	for _, c := range charted[:10] {
		scannedSystems = append(scannedSystems, c.SystemSymbol)
	}

	postRepo := &fakeBackfillPostRepo{}
	h := newBackfillHandler(
		&fakeChartedEnumerator{systems: charted},
		&fakeScannedReader{systems: scannedSystems},
		&fakeIdleProbeCounter{count: 1000},
		postRepo,
	)

	require.NoError(t, h.ReconcileOnce(backfillCtx(), &RunShipyardBackfillCoordinatorCommand{
		PlayerID:              shared.MustNewPlayerID(1),
		MaxDispatchesPerCycle: 1000,
	}))

	declared := postRepo.declaredSystems()
	require.Len(t, declared, 45, "the backfill must dispatch exactly the 45 charted-but-unscanned shipyard systems")
	declaredSet := map[string]bool{}
	for _, s := range declared {
		declaredSet[s] = true
	}
	for _, s := range scannedSystems {
		require.False(t, declaredSet[s], "an already-scanned system %s must never be re-swept", s)
	}
	// Every declared post is a single-hull sweep-once post (the reconciler's relay unit).
	for _, p := range postRepo.declared {
		require.Equal(t, domainScouting.PostKindSweepOnce, p.Kind)
		require.Equal(t, 1, p.HullBudget())
	}
}

// --- scenario 3: bounded / rate-limited, uses idle probes -------------------

// The per-cycle dispatch is min(MaxDispatchesPerCycle, idle probe supply): a catch-up
// sweep never dispatches all 45 at once, and never declares more posts than there are
// idle probes to man them (it must not starve freshness/depth of hulls).
func TestShipyardBackfill_BoundsDispatchByRateAndIdleSupply(t *testing.T) {
	cases := []struct {
		name     string
		rate     int
		idle     int
		expected int
	}{
		{"rate binds below supply", 5, 1000, 5},
		{"idle supply binds below rate", 1000, 3, 3},
		{"neither binds — drain all", 1000, 1000, 45},
		{"no idle probes — dispatch nothing", 10, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			postRepo := &fakeBackfillPostRepo{}
			h := newBackfillHandler(
				&fakeChartedEnumerator{systems: chartedSystems("X1-BLIND-", 45, 1)},
				&fakeScannedReader{systems: nil},
				&fakeIdleProbeCounter{count: tc.idle},
				postRepo,
			)

			require.NoError(t, h.ReconcileOnce(backfillCtx(), &RunShipyardBackfillCoordinatorCommand{
				PlayerID:              shared.MustNewPlayerID(1),
				MaxDispatchesPerCycle: tc.rate,
			}))

			require.Len(t, postRepo.declared, tc.expected,
				"per-cycle dispatch must be bounded by min(rate=%d, idle=%d)", tc.rate, tc.idle)
		})
	}
}

// --- deeper-first prioritization --------------------------------------------

// Deeper systems are more likely to hold a heavy/bulk yard (every shallow system
// scanned so far is light-class), so when the per-cycle cap forces a choice the sweep
// dispatches the DEEPEST-reachable unscanned shipyards first.
func TestShipyardBackfill_PrioritizesDeeperSystemsFirst(t *testing.T) {
	charted := []ChartedShipyardSystem{
		{SystemSymbol: "X1-NEAR", ShipyardWaypoint: "X1-NEAR-Y", Hops: 1},
		{SystemSymbol: "X1-MID", ShipyardWaypoint: "X1-MID-Y", Hops: 4},
		{SystemSymbol: "X1-FAR", ShipyardWaypoint: "X1-FAR-Y", Hops: 8},
	}
	postRepo := &fakeBackfillPostRepo{}
	h := newBackfillHandler(
		&fakeChartedEnumerator{systems: charted},
		&fakeScannedReader{systems: nil},
		&fakeIdleProbeCounter{count: 1000},
		postRepo,
	)

	require.NoError(t, h.ReconcileOnce(backfillCtx(), &RunShipyardBackfillCoordinatorCommand{
		PlayerID:              shared.MustNewPlayerID(1),
		MaxDispatchesPerCycle: 2, // force the deeper-first choice
	}))

	require.Equal(t, []string{"X1-FAR", "X1-MID"}, postRepo.declaredSystems(),
		"the two deepest unscanned shipyards must be dispatched first, deepest before shallower")
}

// --- in-flight exclusion: the sweep progresses, never re-declares -----------

// A charted-unscanned system that ALREADY has an active scout post is already being
// served (a probe is relaying / sweeping it); the backfill must skip it so the bounded
// per-cycle budget advances to NEW blind-spot systems instead of re-declaring in-flight
// work every tick.
func TestShipyardBackfill_SkipsSystemsThatAlreadyHaveAPost(t *testing.T) {
	charted := []ChartedShipyardSystem{
		{SystemSymbol: "X1-INFLIGHT", ShipyardWaypoint: "X1-INFLIGHT-Y", Hops: 2},
		{SystemSymbol: "X1-FRESH", ShipyardWaypoint: "X1-FRESH-Y", Hops: 2},
	}
	postRepo := &fakeBackfillPostRepo{active: []*domainScouting.ScoutPost{
		{PlayerID: 1, SystemSymbol: "X1-INFLIGHT", Kind: domainScouting.PostKindSweepOnce},
	}}
	h := newBackfillHandler(
		&fakeChartedEnumerator{systems: charted},
		&fakeScannedReader{systems: nil},
		&fakeIdleProbeCounter{count: 1000},
		postRepo,
	)

	require.NoError(t, h.ReconcileOnce(backfillCtx(), &RunShipyardBackfillCoordinatorCommand{
		PlayerID:              shared.MustNewPlayerID(1),
		MaxDispatchesPerCycle: 1000,
	}))

	require.Equal(t, []string{"X1-FRESH"}, postRepo.declaredSystems(),
		"a system that already has an active post must not be re-declared; only the fresh blind-spot system is dispatched")
}
