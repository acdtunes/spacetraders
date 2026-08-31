package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// partialSweepShipRepo is the production repository's shape: it satisfies the domain
// port AND the richer fleetSyncReporter, so the guard takes the reporting path exactly
// as it does live.
type partialSweepShipRepo struct {
	navigation.ShipRepository
	fleet      []*navigation.Ship
	unreadable []string
	missing    int
	sweeps     int
	perHull    []string
}

func (r *partialSweepShipRepo) SyncAllFromAPI(ctx context.Context, pid shared.PlayerID) (int, error) {
	result, err := r.SyncAllFromAPIWithReport(ctx, pid)
	return result.Hulls, err
}

func (r *partialSweepShipRepo) SyncAllFromAPIWithReport(_ context.Context, _ shared.PlayerID) (api.FleetSyncResult, error) {
	r.sweeps++
	read := api.FleetReadReport{}
	for i := 0; i < r.missing; i++ {
		read.Unreadable = append(read.Unreadable, api.UnreadableShip{Page: 1, Index: i})
	}
	return api.FleetSyncResult{
		Hulls:           len(r.fleet) - r.missing,
		Read:            read,
		UnreadableHulls: r.unreadable,
	}, nil
}

func (r *partialSweepShipRepo) SyncShipFromAPI(_ context.Context, symbol string, _ shared.PlayerID) (*navigation.Ship, error) {
	r.perHull = append(r.perHull, symbol)
	return nil, nil
}

func (r *partialSweepShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	return r.fleet, nil
}

// The guard reaches the unreadable set through a type assertion, so a repository that
// stopped satisfying it would fall back to the count-only sync and disable the guard
// SILENTLY — no build error, no failing behaviour test, just a phantom decision again.
func TestProductionShipRepositorySatisfiesTheFleetSyncReporter(t *testing.T) {
	var repo interface{} = (*api.ShipRepository)(nil)
	_, ok := repo.(fleetSyncReporter)
	require.True(t, ok, "*api.ShipRepository must report what the fleet sweep could not read")
}

// guardedSweepFleet is the live cold-start shape: a command frigate, a contract hauler
// and a gate worker (all NAMED by a bootstrap decision) beside probes that are only
// COUNTED. Small enough that planRefresh picks the sweep, as it does in production.
func guardedSweepFleet(t *testing.T) []*navigation.Ship {
	t.Helper()
	return []*navigation.Ship{
		homeReaderShip(t, "TORWIND-1", "X1-HQ-A1", commandRole, tradeFleetTag),
		homeReaderShip(t, "TORWIND-2", "X1-HQ-B2", "HAULER", contractFleetTag),
		homeReaderShip(t, "TORWIND-3", "X1-HQ-C3", "HAULER", gate.DeliveryFleetTag),
		homeProbe(t, "TORWIND-8", "X1-HQ-A1"),
		homeProbe(t, "TORWIND-9", "X1-HQ-A1"),
	}
}

// THE hazard a degrading fleet read creates for its callers. Once the enumeration
// survives an unreadable hull, a successful sweep no longer means a COMPLETE fleet —
// and the phantom-cache guard's entire job is to stop a bootstrap decision reading a
// hull's state off a stale row. A hull the sweep could not read still HAS its row,
// preserved verbatim, so a guard that shrugged at a partial sweep would decide on
// precisely the phantom it exists to prevent.
func TestBootstrapRefresh_PartialSweepFailsClosedWhenAGuardedHullWentUnread(t *testing.T) {
	repo := &partialSweepShipRepo{
		fleet:      guardedSweepFleet(t),
		unreadable: []string{"TORWIND-2"}, // a contract hauler: a placement decision names it
		missing:    1,
	}
	refresher := &bootstrapRefresher{shipRepo: repo}

	err := refresher.RefreshFleet(context.Background(), 10)

	require.Error(t, err, "a partial sweep that dropped a GUARDED hull must skip the tick, not decide on its stale row")
	require.Contains(t, err.Error(), "TORWIND-2", "the error must name the hull the tick went blind on")
	require.Equal(t, 1, repo.sweeps, "the sweep still ran; only the DECISION is refused")
}

// The other half of the guard, and what keeps it from being a re-run of the outage it
// fixes: a hull that is merely COUNTED — a probe folded into ProbeCount and named by
// nothing — going unread is exactly the tolerable gap the degrading read was built to
// deliver. Failing the tick on it would put every hull back to blinding the fleet, one
// level up from the enumeration.
func TestBootstrapRefresh_PartialSweepProceedsWhenOnlyACountedHullWentUnread(t *testing.T) {
	repo := &partialSweepShipRepo{
		fleet:      guardedSweepFleet(t),
		unreadable: []string{"TORWIND-8"}, // a probe: counted, never named
		missing:    1,
	}
	refresher := &bootstrapRefresher{shipRepo: repo}

	require.NoError(t, refresher.RefreshFleet(context.Background(), 10),
		"an unreadable hull no decision names must NOT cost the tick — that is the whole point of a fleet read that degrades")
	require.Equal(t, 1, repo.sweeps)
}

// Calibration: the guard must key on the read being PARTIAL, not on the reporting path
// being taken. Without this a guard wired to fail on any reporting sweep would pass the
// hazard test above while freezing bootstrap on every healthy tick.
func TestBootstrapRefresh_CompleteSweepIsUnaffectedByTheGuard(t *testing.T) {
	repo := &partialSweepShipRepo{fleet: guardedSweepFleet(t)}
	refresher := &bootstrapRefresher{shipRepo: repo}

	require.NoError(t, refresher.RefreshFleet(context.Background(), 10))
	require.Equal(t, 1, repo.sweeps)
	require.Empty(t, repo.perHull, "a 5-hull fleet is one page, so the plan is the sweep")
}

// The guard is specified over the hulls a decision NAMES, so the intersection is what
// must be exact — over-firing freezes bootstrap, under-firing is the phantom.
func TestBlindGuardedHulls_IntersectsUnreadableWithTheNamedSet(t *testing.T) {
	plan := refreshPlan{Guarded: []string{"GATE-3", "HAUL-2", "TORWIND-1"}}

	require.Equal(t, []string{"HAUL-2"}, plan.blindGuardedHulls([]string{"HAUL-2", "PROBE-9"}),
		"only the guarded hull counts; the unread probe is a tolerable gap")
	require.Nil(t, plan.blindGuardedHulls([]string{"PROBE-9"}),
		"a fleet read missing only counted hulls leaves no decision blind")
	require.Nil(t, plan.blindGuardedHulls(nil), "a complete read blinds nothing")
	require.Nil(t, refreshPlan{}.blindGuardedHulls([]string{"HAUL-2"}),
		"an unpriced plan names no hull, so nothing can be blind — the projection it would name from is what failed")
	require.Equal(t, []string{"GATE-3", "TORWIND-1"}, plan.blindGuardedHulls([]string{"TORWIND-1", "GATE-3"}),
		"every blind hull is reported, in the guarded set's own order")
}

// The throttle prices a read so the guard re-fires at its allowance. A partial sweep
// stands in for the complete one, so it has to be priced like it: charging only the
// hulls the server SERVED would let a fleet read that keeps coming back short buy less
// quiet each pass and creep up on the API budget the throttle exists to bound.
func TestBootstrapRefresh_PartialSweepIsPricedOnTheHullsTheServerHolds(t *testing.T) {
	fleet := make([]*navigation.Ship, 0, 40)
	for i := 0; i < 40; i++ {
		fleet = append(fleet, homeProbe(t, "PROBE-"+string(rune('A'+i%26))+string(rune('0'+i/26)), "X1-HQ-A1"))
	}
	// 40 hulls = 2 pages, but the sweep only read 19 of them.
	repo := &partialSweepShipRepo{fleet: fleet, unreadable: []string{"PROBE-Z0"}, missing: 21}
	refresher := &bootstrapRefresher{shipRepo: repo}

	spent, err := refresher.executeRefresh(context.Background(),
		shared.MustNewPlayerID(10), refresher.planRefresh(context.Background(), shared.MustNewPlayerID(10)))

	require.NoError(t, err, "no probe is a guarded hull, so a partial sweep of them proceeds")
	require.Equal(t, 2, spent,
		"priced at 2 pages (the 40 hull slots the server holds), not the 1 page the 19 served hulls would suggest")
}
