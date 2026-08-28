package grpc

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// countingSyncShipRepo records how many full-fleet API syncs the guard actually spent.
type countingSyncShipRepo struct {
	navigation.ShipRepository
	hulls int
	calls int
	err   error
}

func (r *countingSyncShipRepo) SyncAllFromAPI(_ context.Context, _ shared.PlayerID) (int, error) {
	r.calls++
	if r.err != nil {
		return 0, r.err
	}
	return r.hulls, nil
}

// newTestThrottle puts the throttle on a controllable clock so spacing is asserted, not waited out.
func newTestThrottle(now *time.Time) *fleetRefreshThrottle {
	t := newFleetRefreshThrottle()
	t.now = func() time.Time { return *now }
	return t
}

// TestFleetRefreshThrottle_PricesSweepByPageCount pins the cost model: spacing scales with the
// pages a fleet read is actually charged for, leaving a one-page fleet effectively unthrottled.
func TestFleetRefreshThrottle_PricesSweepByPageCount(t *testing.T) {
	th := newFleetRefreshThrottle()

	require.Equal(t, time.Duration(0), th.spacing(0),
		"an unpriced sweep must not be held back — the first read after boot has no cost to amortise")

	// A cold start's fleet is one page, at or under the tick, so the guard still runs every
	// tick in the regime it was designed for.
	onePage := th.spacing(7)
	require.InDelta(t, 50.0, onePage.Seconds(), 1.0,
		"a 1-page fleet must stay ~tick-rate: the guard is cheap and valuable at cold start")

	// A 95-page fleet must buy 95x as much quiet.
	full := th.spacing(1896)
	require.Equal(t, 95, int(math.Ceil(1896.0/float64(api.FleetPageLimit))), "1896 hulls is 95 pages")
	require.InDelta(t, 95*onePage.Seconds(), full.Seconds(), 1.0,
		"spacing must be linear in pages — the cost of the sweep, not a fixed interval")
}

// TestFleetRefreshThrottle_HoldsFleetReadsToItsAPIAllowance states the guarantee in the unit the
// API budget is scarce in. A sweep-per-tick on a large fleet can exceed the account's entire
// rate ceiling; whatever the tick rate, the guard must stay inside its allowance.
func TestFleetRefreshThrottle_HoldsFleetReadsToItsAPIAllowance(t *testing.T) {
	const (
		hulls    = 1896
		tick     = 45 * time.Second
		oneHour  = time.Hour
		pageCost = 95 // ceil(1896/20)
	)

	now := time.Now()
	th := newTestThrottle(&now)
	repo := &countingSyncShipRepo{hulls: hulls}
	refresher := &bootstrapRefresher{shipRepo: repo, throttle: th}

	// A full hour of ticks at the real bootstrap cadence.
	deadline := now.Add(oneHour)
	for !now.After(deadline) {
		require.NoError(t, refresher.RefreshFleet(context.Background(), 9))
		now = now.Add(tick)
	}

	ticks := int(oneHour / tick)
	callsSpent := repo.calls * pageCost
	require.Equal(t, 80, ticks, "sanity: a 45s tick is 80 ticks/hour")

	require.LessOrEqual(t, callsSpent, int(bootstrapFleetRefreshBudgetPerHour)+pageCost,
		"the guard spent %d API calls in an hour (%d sweeps x %d pages); its allowance is %.0f/hour",
		callsSpent, repo.calls, pageCost, bootstrapFleetRefreshBudgetPerHour)

	// And an order of magnitude under the sweep-per-tick it replaces.
	require.Less(t, callsSpent, ticks*pageCost/10,
		"must be an order of magnitude under the per-tick sweep this replaces (%d calls/hour)",
		ticks*pageCost)
}

// TestFleetRefreshThrottle_SkipIsSuccessNotFailure is what makes the throttle safe behind a
// fail-closed guard: reconcileOnce aborts the whole tick on error, so reporting "not due yet"
// that way would silently stall the coordinator instead of skipping a read.
func TestFleetRefreshThrottle_SkipIsSuccessNotFailure(t *testing.T) {
	now := time.Now()
	repo := &countingSyncShipRepo{hulls: 1896}
	refresher := &bootstrapRefresher{shipRepo: repo, throttle: newTestThrottle(&now)}

	require.NoError(t, refresher.RefreshFleet(context.Background(), 9))
	require.Equal(t, 1, repo.calls, "the first tick after boot must read: nothing is priced yet")

	now = now.Add(45 * time.Second)
	require.NoError(t, refresher.RefreshFleet(context.Background(), 9),
		"a throttled tick must report success — an error would abort the whole reconcile")
	require.Equal(t, 1, repo.calls, "and must NOT have spent a second 95-call sweep")
}

// TestFleetRefreshThrottle_ResumesOnceTheSweepIsPaidFor: the guard is deferred, never disabled.
func TestFleetRefreshThrottle_ResumesOnceTheSweepIsPaidFor(t *testing.T) {
	now := time.Now()
	th := newTestThrottle(&now)
	repo := &countingSyncShipRepo{hulls: 1896}
	refresher := &bootstrapRefresher{shipRepo: repo, throttle: th}

	require.NoError(t, refresher.RefreshFleet(context.Background(), 9))
	require.Equal(t, 1, repo.calls)

	spacing := th.spacing(1896)
	now = now.Add(spacing - time.Second)
	require.NoError(t, refresher.RefreshFleet(context.Background(), 9))
	require.Equal(t, 1, repo.calls, "one second short of the spacing is still too soon")

	now = now.Add(2 * time.Second)
	require.NoError(t, refresher.RefreshFleet(context.Background(), 9))
	require.Equal(t, 2, repo.calls, "past the spacing the guard must read again — deferred, not disabled")
}

// TestFleetRefreshThrottle_FailedSweepDoesNotBuyQuiet: only a successful read is priced, or one
// transient API error suppresses the guard for a whole spacing.
func TestFleetRefreshThrottle_FailedSweepDoesNotBuyQuiet(t *testing.T) {
	now := time.Now()
	repo := &countingSyncShipRepo{hulls: 1896, err: errors.New("API refused the fleet read")}
	refresher := &bootstrapRefresher{shipRepo: repo, throttle: newTestThrottle(&now)}

	require.Error(t, refresher.RefreshFleet(context.Background(), 9),
		"a genuine read failure must still fail the tick closed")
	require.Equal(t, 1, repo.calls)

	// No time passed at all: the guard must still be willing to retry.
	repo.err = nil
	require.NoError(t, refresher.RefreshFleet(context.Background(), 9))
	require.Equal(t, 2, repo.calls, "a failed sweep must not have counted as one that was paid for")
}

// TestFleetRefreshThrottle_IsPerPlayer: the refresher is a singleton shared across players, so
// one player's spacing must not silence another's guard.
func TestFleetRefreshThrottle_IsPerPlayer(t *testing.T) {
	now := time.Now()
	repo := &countingSyncShipRepo{hulls: 1896}
	refresher := &bootstrapRefresher{shipRepo: repo, throttle: newTestThrottle(&now)}

	require.NoError(t, refresher.RefreshFleet(context.Background(), 9))
	require.NoError(t, refresher.RefreshFleet(context.Background(), 12))
	require.Equal(t, 2, repo.calls, "a second player's first sweep must not be charged to the first's spacing")
}

// TestFleetRefreshThrottle_AbsentThrottleStillRefreshes: a nil throttle must read every tick,
// never silently never read.
func TestFleetRefreshThrottle_AbsentThrottleStillRefreshes(t *testing.T) {
	repo := &countingSyncShipRepo{hulls: 3}
	refresher := &bootstrapRefresher{shipRepo: repo}

	require.NoError(t, refresher.RefreshFleet(context.Background(), 9))
	require.NoError(t, refresher.RefreshFleet(context.Background(), 9))
	require.Equal(t, 2, repo.calls, "no throttle means no deferral, never no refresh")
}
