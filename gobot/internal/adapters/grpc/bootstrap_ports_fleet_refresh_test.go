package grpc

import (
	"context"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// countingSyncShipRepo records what the guard spent: whole-fleet sweeps, and hulls read one at a
// time. Its projection is the fleet the sweep would return, so the plan is the production plan.
type countingSyncShipRepo struct {
	navigation.ShipRepository
	fleet   []*navigation.Ship
	hulls   int // sweep-reported hull count; defaults to len(fleet) when zero
	calls   int // full-fleet sweeps
	perHull []string
	err     error
	findErr error
}

func (r *countingSyncShipRepo) SyncAllFromAPI(_ context.Context, _ shared.PlayerID) (int, error) {
	r.calls++
	if r.err != nil {
		return 0, r.err
	}
	if r.hulls > 0 {
		return r.hulls, nil
	}
	return len(r.fleet), nil
}

func (r *countingSyncShipRepo) SyncShipFromAPI(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	r.perHull = append(r.perHull, symbol)
	if r.err != nil {
		return nil, r.err
	}
	return nil, nil
}

func (r *countingSyncShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	if r.findErr != nil {
		return nil, r.findErr
	}
	return r.fleet, nil
}

// probeFleet is a mature fleet's shape: n idle market sentinels beside one command frigate.
func probeFleet(t *testing.T, n int) []*navigation.Ship {
	t.Helper()
	ships := make([]*navigation.Ship, 0, n+1)
	ships = append(ships, homeReaderShip(t, "TORWIND-1", "X1-HQ-A1", commandRole, tradeFleetTag))
	for i := 0; i < n; i++ {
		ships = append(ships, homeProbe(t, "PROBE-"+string(rune('A'+i%26))+strconv.Itoa(i), "X1-HQ-A1"))
	}
	return ships
}

// newTestThrottle puts the throttle on a controllable clock so spacing is asserted, not waited out.
func newTestThrottle(now *time.Time) *fleetRefreshThrottle {
	t := newFleetRefreshThrottle()
	t.now = func() time.Time { return *now }
	return t
}

// --- WHAT THE GUARD NEEDS FRESH ---
//
// A sync PRESERVES every daemon-owned column, so re-reading moves only nav/location/cargo, and only
// four hull sets decide on those. Narrow this set and a decision reads a phantom; widen it and the
// guard is priced by the fleet again.

func TestBootstrapGuardedHulls_NamesEveryHullABootstrapDecisionReads(t *testing.T) {
	sentinel := homeProbe(t, "SENTINEL-1", "X1-HQ-YARD")
	require.NoError(t, sentinel.ReserveByCaptain(bootstrapCmd.YardSentinelReservationReason, shared.NewRealClock()))

	ships := []*navigation.Ship{
		homeReaderShip(t, "TORWIND-1", "X1-HQ-A1", commandRole, tradeFleetTag),
		homeReaderShip(t, "HAUL-2", "X1-HQ-B2", "HAULER", contractFleetTag),
		homeReaderShip(t, "GATE-3", "X1-HQ-C3", "HAULER", gate.DeliveryFleetTag),
		homeReaderShip(t, "GATE-4", "X1-HQ-C3", "HAULER", gate.FactoryFleetTag),
		homeReaderShip(t, "MFG-5", "X1-HQ-C3", "HAULER", gate.LegacyFleetTag),
		sentinel,
	}

	require.Equal(t,
		[]string{"GATE-3", "GATE-4", "HAUL-2", "MFG-5", "SENTINEL-1", "TORWIND-1"},
		bootstrapGuardedHulls(ships),
		"the command frigate (HomeSystem/cargo/transit), the contract haulers (placement waypoints), every gate tag (idle selection) and the yard sentinel (parked check) are the hulls a decision NAMES")
}

// Market sentinels and trade haulers are COUNTED, never named: their dedication and idle state are
// daemon-owned, their positions drive nothing, and reading them is the whole page bill for nothing.
func TestBootstrapGuardedHulls_ExcludesHullsOnlyEverCounted(t *testing.T) {
	ships := []*navigation.Ship{
		homeProbe(t, "PROBE-2", "X1-HQ-A1"),
		homeReaderShip(t, "TRADE-3", "X1-HQ-B2", "HAULER", tradeFleetTag),
		homeReaderShip(t, "WARE-4", "X1-HQ-B2", "HAULER", warehouseFleetTag),
		homeReaderShip(t, "STOCK-5", "X1-HQ-B2", "HAULER", stockerFleetTag),
		homeReaderShip(t, "IDLE-6", "X1-HQ-B2", "HAULER", ""),
	}

	require.Empty(t, bootstrapGuardedHulls(ships),
		"a hull the observation only counts cannot be made any fresher by reading it — the sync preserves the columns those counts are built from")
}

// An ordinary captain reservation is not the sentinel — the same discriminator observeFleetShape uses.
func TestBootstrapGuardedHulls_OnlyTheSentinelsOwnReservationCounts(t *testing.T) {
	other := homeProbe(t, "PROBE-9", "X1-HQ-A1")
	require.NoError(t, other.ReserveByCaptain("some other errand", shared.NewRealClock()))

	require.Empty(t, bootstrapGuardedHulls([]*navigation.Ship{other}),
		"an operator's manual reservation names no bootstrap decision")
}

// --- WHICH READ THE GUARD BUYS ---

// COLD START: a fleet inside one page makes the sweep both cheaper AND strictly more informative,
// and that is the regime the guard was designed for — behaviour there must not move.
func TestRefreshFleet_ColdStartFleetStillTakesTheWholeSweep(t *testing.T) {
	repo := &countingSyncShipRepo{fleet: []*navigation.Ship{
		homeReaderShip(t, "TORWIND-1", "X1-HQ-A1", commandRole, tradeFleetTag),
		homeProbe(t, "TORWIND-2", "X1-HQ-A1"),
		homeProbe(t, "TORWIND-3", "X1-HQ-A1"),
	}}
	refresher := &bootstrapRefresher{shipRepo: repo}

	require.NoError(t, refresher.RefreshFleet(context.Background(), 9))
	require.Equal(t, 1, repo.calls, "a one-page fleet must still be swept: same price, strictly more information")
	require.Empty(t, repo.perHull, "and must not be read hull-by-hull")
}

// A TIE goes to the sweep for the same reason: equal price, more information.
func TestRefreshFleet_TieGoesToTheSweep(t *testing.T) {
	// A fleet whose page count exactly equals its guarded-hull count.
	ships := []*navigation.Ship{
		homeReaderShip(t, "TORWIND-1", "X1-HQ-A1", commandRole, tradeFleetTag),
		homeReaderShip(t, "HAUL-2", "X1-HQ-B2", "HAULER", contractFleetTag),
		homeReaderShip(t, "HAUL-3", "X1-HQ-B2", "HAULER", contractFleetTag),
		homeReaderShip(t, "GATE-4", "X1-HQ-C3", "HAULER", gate.DeliveryFleetTag),
		homeReaderShip(t, "GATE-5", "X1-HQ-C3", "HAULER", gate.FactoryFleetTag),
	}
	for i := 0; i < 85; i++ {
		ships = append(ships, homeProbe(t, "PROBE-"+strconv.Itoa(i), "X1-HQ-A1"))
	}
	require.Equal(t, 5, fleetPages(len(ships)), "sanity: 90 hulls is 5 pages")

	repo := &countingSyncShipRepo{fleet: ships}
	require.NoError(t, (&bootstrapRefresher{shipRepo: repo}).RefreshFleet(context.Background(), 9))
	require.Equal(t, 1, repo.calls)
	require.Empty(t, repo.perHull, "at equal price the sweep wins — it is strictly more informative")
}

// MATURE FLEET: a full sweep to re-read the one hull a decision names. That read is long enough in
// the priority limiter that a daemon restart cancels it before it can finish.
func TestRefreshFleet_MatureFleetReadsOnlyTheGuardedHulls(t *testing.T) {
	repo := &countingSyncShipRepo{fleet: probeFleet(t, 1895)}
	require.Equal(t, 95, fleetPages(len(repo.fleet)), "sanity: this fleet is 95 pages to enumerate")

	require.NoError(t, (&bootstrapRefresher{shipRepo: repo}).RefreshFleet(context.Background(), 9))

	require.Zero(t, repo.calls, "a 95-page sweep to re-read one named hull must not be bought")
	require.Equal(t, []string{"TORWIND-1"}, repo.perHull,
		"only the command frigate is named by an EXPANSION decision on this fleet")
}

// FAIL-SAFE: a fleet we cannot characterise gets the WHOLE read, never one targeted from nothing.
func TestRefreshFleet_UnreadableProjectionFallsBackToTheSweep(t *testing.T) {
	repo := &countingSyncShipRepo{findErr: errors.New("projection unreadable")}
	require.NoError(t, (&bootstrapRefresher{shipRepo: repo}).RefreshFleet(context.Background(), 9))
	require.Equal(t, 1, repo.calls, "an uncharacterisable fleet must be swept, not guessed at")
	require.Empty(t, repo.perHull)
}

// FAIL CLOSED: every hull here is one a decision this tick reads, so an unreadable one is exactly
// the state the guard exists to stop it acting on.
func TestRefreshFleet_UnreadableGuardedHullFailsTheTickClosed(t *testing.T) {
	repo := &countingSyncShipRepo{fleet: probeFleet(t, 1895), err: errors.New("API refused the hull")}
	refresher := &bootstrapRefresher{shipRepo: repo, throttle: newFleetRefreshThrottle()}

	require.Error(t, refresher.RefreshFleet(context.Background(), 9),
		"a hull a decision names but cannot be read must fail the tick closed, never be acted on stale")

	repo.err = nil
	require.NoError(t, refresher.RefreshFleet(context.Background(), 9),
		"and the failure must not have bought quiet — the guard retries immediately")
}

// --- THE EXIT PATH ---
//
// The guard runs BEFORE Observe and fails the whole tick closed, so the terminal EXPANSION exit is
// gated behind whatever the guard's read costs. A read the fleet has outgrown never completes between
// daemon restarts, and a coordinator whose only remaining job is to observe a built gate never exits.
func TestBootstrapReachesTerminalExitOnAMatureFleetWithoutSweepingIt(t *testing.T) {
	repo := &countingSyncShipRepo{fleet: probeFleet(t, 1895)}

	h := bootstrapCmd.NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&bootstrapRefresher{shipRepo: repo, throttle: newFleetRefreshThrottle()})
	h.SetWorldObserver(&bootstrapTermObserver{obs: matureFleetObservation()})
	h.SetHandoffLauncher(&bootstrapTermHandoff{})

	resp, err := h.Handle(context.Background(), bootstrapRunnerCommand("bootstrap-player-9-exit-test"))
	require.NoError(t, err)

	got, ok := resp.(*bootstrapCmd.RunBootstrapCoordinatorResponse)
	require.True(t, ok)
	require.True(t, got.Done, "a built gate with the hand-off already live must reach the terminal exit")
	require.Equal(t, 1, got.Ticks, "and reach it on the FIRST tick — the guard must not hold the exit")

	require.Zero(t, repo.calls, "the exit must not cost a 95-page sweep of 1896 hulls")
	require.Equal(t, []string{"TORWIND-1"}, repo.perHull,
		"one live read of the one hull an EXPANSION decision names is the whole bill")
}

// --- THE ALLOWANCE ---

// The cost model: spacing scales with the calls a read is charged for, leaving a one-page fleet
// effectively unthrottled.
func TestFleetRefreshThrottle_PricesAReadByItsAPICalls(t *testing.T) {
	th := newFleetRefreshThrottle()

	require.Equal(t, time.Duration(0), th.spacing(0),
		"an unpriced read must not be held back — the first read after boot has no cost to amortise")

	// A cold start's fleet is one page, at or under the tick, so the guard still runs every
	// tick in the regime it was designed for.
	onePage := th.spacing(fleetPages(7))
	require.InDelta(t, 50.0, onePage.Seconds(), 1.0,
		"a 1-page fleet must stay ~tick-rate: the guard is cheap and valuable at cold start")

	// A 95-page sweep must buy 95x as much quiet.
	full := th.spacing(fleetPages(1896))
	require.Equal(t, 95, int(math.Ceil(1896.0/float64(api.FleetPageLimit))), "1896 hulls is 95 pages")
	require.InDelta(t, 95*onePage.Seconds(), full.Seconds(), 1.0,
		"spacing must be linear in calls — the cost of the read, not a fixed interval")

	// And the targeted read is priced at what IT costs, not at the sweep it replaced: one hull
	// buys one page's worth of quiet, so the guard keeps running at ~tick rate on a mature fleet.
	require.InDelta(t, onePage.Seconds(), th.spacing(1).Seconds(), 1.0,
		"a 1-call read must not be charged the 95-call sweep's silence")
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
	// hulls without a projection: FindAllByPlayer returns nothing, so every pass plans the sweep —
	// the worst case the allowance has to bound.
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

// TestFleetRefreshThrottle_ResumesOnceTheReadIsPaidFor: the guard is deferred, never disabled.
func TestFleetRefreshThrottle_ResumesOnceTheReadIsPaidFor(t *testing.T) {
	now := time.Now()
	th := newTestThrottle(&now)
	repo := &countingSyncShipRepo{hulls: 1896}
	refresher := &bootstrapRefresher{shipRepo: repo, throttle: th}

	require.NoError(t, refresher.RefreshFleet(context.Background(), 9))
	require.Equal(t, 1, repo.calls)

	spacing := th.spacing(fleetPages(1896))
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
