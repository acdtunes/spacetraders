package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// tagReleaseShipRepo embeds the full ShipRepository and overrides only the two methods the release
// touches, so the compiler keeps the double honest if the interface grows. AssignFleet records what
// was cleared rather than mutating, which is what lets the tests assert the WRITE SET — a fake that
// silently applied the write would make "cleared the right hulls" and "cleared everything"
// indistinguishable.
type tagReleaseShipRepo struct {
	navigation.ShipRepository
	ships   []*navigation.Ship
	err     error
	cleared []string
}

func (r *tagReleaseShipRepo) FindAllByPlayer(_ context.Context, _ shared.PlayerID) ([]*navigation.Ship, error) {
	if r.err != nil {
		// Adversarial: a non-empty fleet alongside the error, so a swallowed error shows up as
		// writes against hulls the caller was never entitled to see.
		return []*navigation.Ship{}, r.err
	}
	return r.ships, nil
}

func (r *tagReleaseShipRepo) AssignFleet(_ context.Context, shipSymbol string, fleet string, _ shared.PlayerID) error {
	if fleet == "" {
		r.cleared = append(r.cleared, shipSymbol)
	}
	return nil
}

// retirementProbe is a satellite hull carrying a given dedicated_fleet tag.
func retirementProbe(t *testing.T, symbol, fleet string) *navigation.Ship {
	t.Helper()
	return shipyardHull(t, symbol, "X1-KP23-A2", fleet, "SATELLITE", navigation.NavStatusDocked)
}

// The probe-buyer retirement (Admiral 2026-07-28), pinned the same three ways the
// frontier-expansion and market-freshness retirements are.
//
// Why it needs pinning at all: this coordinator was BOOT-STANDING, which is exactly what made its
// cost unbounded — nothing had to launch it, so nothing had to notice it, and the first tick after
// bootstrap reached EXPANSION it bought 9 SHIP_PROBE for 245,316 credits in five minutes. A silent
// resurrection (re-adding the type to the boot set, or a builder creeping back into the registry)
// would restore precisely that, and the diff that did it would look innocuous.

// The launch verb answers honestly and persists nothing. The gRPC surface is kept so a residual
// caller — an old CLI, a script, a captain habit — gets a clear answer rather than a missing method.
func TestRetiredProbeBuyerStartVerb_ReturnsRetiredError(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	s := &DaemonServer{containerRepo: repo}

	_, err := s.ProbeBuyerFleetCoordinator(context.Background(), playerID, 0)

	require.Error(t, err)
	require.Contains(t, err.Error(), "retired", "the probe-buyer start verb must say it is retired")
	require.Contains(t, err.Error(), "probe-sensing", "the error must point the operator at the successor")

	var count int64
	require.NoError(t, db.Model(&persistence.ContainerModel{}).Where("player_id = ?", playerID).Count(&count).Error)
	require.Zero(t, count, "a retired verb must persist nothing")
}

// Recovery must treat the persisted row as terminated rather than as an unexplained loss. There IS
// such a row on the live fleet — probe_buyer_coordinator-player-5-b8bedd4f, STOPPED by hand right
// after the burst — so this is a live migration concern, not a hypothetical one.
func TestProbeBuyerCommandTypeIsRetired_SoAStaleRowRecoversCleanly(t *testing.T) {
	require.True(t, retiredCommandTypes["probe_buyer_coordinator"],
		"a persisted probe_buyer_coordinator row must be skipped at recovery, not reported as a loss")
}

// The boot-standing set is the resurrection surface that matters: membership alone is what made
// this coordinator run unattended on every daemon start.
func TestBootStandingSet_ExcludesTheRetiredProbeBuyer(t *testing.T) {
	for _, containerType := range bootStandingCoordinatorTypes {
		require.NotEqual(t, "PROBE_BUYER_COORDINATOR", string(containerType),
			"the probe-buyer coordinator is retired — boot-standing it is what let it spend 245,316 "+
				"credits unattended")
	}
}

// The retirement's DATA half. Retiring a coordinator does not rewrite a ships row, so its recruits
// (TORWIND-2 and TORWIND-E on the live fleet) would keep dedicated_fleet="probe-buyer" — a fleet
// that no longer exists. That tag is not inert: sensing's DockedProbeAt admits only "" and
// sensing_parked, so such a hull is invisible to the buy path, and adoption only retags the hulls
// it actually absorbs.
func TestReleaseRetiredProbeBuyerHulls_ClearsTheTagAndLeavesOtherFleetsAlone(t *testing.T) {
	repo := &tagReleaseShipRepo{ships: []*navigation.Ship{
		retirementProbe(t, "TORWIND-2", "probe-buyer"),
		retirementProbe(t, "TORWIND-E", "probe-buyer"),
		retirementProbe(t, "TORWIND-9", "sensing_parked"), // ours — untouched
		retirementProbe(t, "TORWIND-7", "contract"),       // another live fleet — never poached
		retirementProbe(t, "TORWIND-8", ""),               // already free
	}}
	s := &DaemonServer{shipRepo: repo}

	s.releaseRetiredProbeBuyerHulls(context.Background(), 5)

	require.ElementsMatch(t, []string{"TORWIND-2", "TORWIND-E"}, repo.cleared,
		"exactly the hulls of the deleted fleet are released")
	require.NotContains(t, repo.cleared, "TORWIND-9", "our own sensing hulls keep their tag")
	require.NotContains(t, repo.cleared, "TORWIND-7", "a live foreign fleet is never touched (RULINGS #7)")
}

// Idempotent: a second boot over an already-released fleet costs one read and zero writes.
func TestReleaseRetiredProbeBuyerHulls_IsIdempotent(t *testing.T) {
	repo := &tagReleaseShipRepo{ships: []*navigation.Ship{
		retirementProbe(t, "TORWIND-2", ""),
		retirementProbe(t, "TORWIND-9", "sensing_parked"),
	}}
	s := &DaemonServer{shipRepo: repo}

	s.releaseRetiredProbeBuyerHulls(context.Background(), 5)

	require.Empty(t, repo.cleared, "a settled fleet must cost no writes")
}

// Fail-open, non-fatal: a daemon that cannot read the ships table must still boot.
func TestReleaseRetiredProbeBuyerHulls_UnreadableFleetDoesNotPanic(t *testing.T) {
	s := &DaemonServer{shipRepo: &tagReleaseShipRepo{err: errors.New("ships table unavailable")}}

	require.NotPanics(t, func() { s.releaseRetiredProbeBuyerHulls(context.Background(), 5) })
}

// THE WIRING, not just the function. Mutation probe T2 deleted the boot call and every test stayed
// green — the third time in this branch that a helper was pinned while nothing required anything to
// CALL it. The release only matters if boot actually performs it, and the restart it is meant to fix
// is the only chance it gets.
func TestLaunchBootStandingAfterRecovery_ReleasesRetiredProbeBuyerHulls(t *testing.T) {
	s, db, _ := newRecoveryTestServer(t)
	s.playerRepo = persistence.NewGormPlayerRepository(db)
	repo := &tagReleaseShipRepo{ships: []*navigation.Ship{
		retirementProbe(t, "TORWIND-2", "probe-buyer"),
		retirementProbe(t, "TORWIND-E", "probe-buyer"),
		retirementProbe(t, "TORWIND-7", "contract"),
	}}
	s.shipRepo = repo

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.runCtx = runCtx

	s.launchBootStandingAfterRecovery()

	require.ElementsMatch(t, []string{"TORWIND-2", "TORWIND-E"}, repo.cleared,
		"boot must release the hulls of the deleted probe-buyer fleet, or they stay driven by nobody "+
			"and invisible to sensing's buy path across the very restart this fixes")
}
