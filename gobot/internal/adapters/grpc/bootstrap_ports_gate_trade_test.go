package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/stretchr/testify/require"
)

// At EXPANSION the gate is built and its construction hulls stop earning, so the hand-off
// re-dedicates them to the TRADE fleet — an ASSIGNMENT, not an un-dedication. Clearing the tag to ""
// would strand them: the trade coordinator only works hulls already tagged "trade", the autosizer only
// tags hulls it buys, and the capacity reconciler that once auto-pinned idle hulls is gone (sp-y2ptq).
//
// It re-guards each hull at write time exactly as the surplus releaser does — still manufacturing-
// dedicated, still idle, not in transit — so a hull that picked up a task since the observation is
// never yanked mid-delivery, plus a cargo-capacity guard because a 0-cargo hull cannot trade.
func TestBootstrapGateReleaser_RedirectsIdleManufacturingHullsToTrade(t *testing.T) {
	// Symbols avoid the "-1" suffix the command-frigate heuristic reserves (IsCommandHull), so these
	// read as ordinary gate workers, not the flagship.
	idle := reclaimHull(t, "MFG-7", 40, "manufacturing", navigation.NavStatusInOrbit)      // → trade
	docked := reclaimHull(t, "MFG-4", 40, "manufacturing", navigation.NavStatusDocked)     // → trade
	midTask := reclaimHull(t, "MFG-8", 40, "manufacturing", navigation.NavStatusInTransit) // mid-delivery → skipped
	retagged := reclaimHull(t, "MFG-9", 40, "contract", navigation.NavStatusInOrbit)       // re-tagged since obs → skipped
	untouched := reclaimHull(t, "CONTRACT-4", 40, "contract", navigation.NavStatusInOrbit) // not requested → untouched
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{idle, docked, midTask, retagged, untouched}}
	r := &bootstrapGateSurplusReleaser{shipRepo: repo}

	redirected, err := r.ReleaseGateWorkersToTrade(context.Background(), 1, []string{"MFG-7", "MFG-4", "MFG-8", "MFG-9"})
	require.NoError(t, err)
	require.Equal(t, 2, redirected)
	require.Equal(t, []assignFleetCall{
		{symbol: "MFG-7", fleet: tradeFleetTag},
		{symbol: "MFG-4", fleet: tradeFleetTag},
	}, repo.assigned)

	// The tag written is the one the trade fleet coordinator actually partitions on — the hull is
	// adopted, not merely un-pinned. (Non-tautological: any other value leaves it invisible to trade.)
	require.Equal(t, "trade", tradeFleetTag)
}

// A 0-cargo hull (a probe wrongly carrying the construction tag) is NEVER handed to trade: it cannot
// haul, so pinning it there would put a useless hull in the trade fleet's pool. Mirrors the contract
// scaler's isReclaimable cargo guard.
func TestBootstrapGateReleaser_NeverRedirectsAZeroCargoHull(t *testing.T) {
	probe := reclaimHull(t, "PROBE-7", 0, "manufacturing", navigation.NavStatusInOrbit)
	hauler := reclaimHull(t, "MFG-7", 40, "manufacturing", navigation.NavStatusInOrbit)
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{probe, hauler}}
	r := &bootstrapGateSurplusReleaser{shipRepo: repo}

	redirected, err := r.ReleaseGateWorkersToTrade(context.Background(), 1, []string{"PROBE-7", "MFG-7"})
	require.NoError(t, err)
	require.Equal(t, 1, redirected)
	require.Equal(t, []assignFleetCall{{symbol: "MFG-7", fleet: tradeFleetTag}}, repo.assigned)
}

// The COMMAND FRIGATE is never re-tagged, even if it somehow carries the construction dedication
// (RULINGS #7 — the flagship is excluded from every re-dedication path). Unreachable through the
// observer today (its command-role branch short-circuits before the manufacturing branch, so the
// frigate never appears in GateWorkerHulls), but every sibling re-tag path carries the guard
// explicitly and this one must not be the exception that a future observer change turns live.
func TestBootstrapGateReleaser_NeverRedirectsTheCommandFrigate(t *testing.T) {
	// The "-1" suffix is what the IsCommandHull heuristic keys on.
	frigate := reclaimHull(t, "AGENT-1", 40, "manufacturing", navigation.NavStatusInOrbit)
	worker := reclaimHull(t, "MFG-7", 40, "manufacturing", navigation.NavStatusInOrbit)
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{frigate, worker}}
	r := &bootstrapGateSurplusReleaser{shipRepo: repo}

	redirected, err := r.ReleaseGateWorkersToTrade(context.Background(), 1, []string{"AGENT-1", "MFG-7"})
	require.NoError(t, err)
	require.Equal(t, 1, redirected)
	require.Equal(t, []assignFleetCall{{symbol: "MFG-7", fleet: tradeFleetTag}}, repo.assigned)
}

// A fleet-read error fails closed: surface it and re-tag nothing (never a blind write on an unknown fleet).
func TestBootstrapGateReleaser_TradeRedirect_FleetReadErrorFailsClosed(t *testing.T) {
	repo := &fakeReclaimShipRepo{findErr: errors.New("db down")}
	r := &bootstrapGateSurplusReleaser{shipRepo: repo}

	redirected, err := r.ReleaseGateWorkersToTrade(context.Background(), 1, []string{"MFG-1"})
	require.Error(t, err)
	require.Equal(t, 0, redirected)
	require.Nil(t, repo.assigned, "a read failure must re-tag nothing")
}

// An empty request is a no-op that never touches the fleet store — the coordinator's "nothing to
// redirect" path costs zero reads.
func TestBootstrapGateReleaser_TradeRedirect_EmptyRequestIsANoOp(t *testing.T) {
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{reclaimHull(t, "MFG-7", 40, "manufacturing", navigation.NavStatusInOrbit)}}
	r := &bootstrapGateSurplusReleaser{shipRepo: repo}

	redirected, err := r.ReleaseGateWorkersToTrade(context.Background(), 1, nil)
	require.NoError(t, err)
	require.Equal(t, 0, redirected)
	require.Nil(t, repo.assigned)
}

// failOnSymbolShipRepo re-tags every hull except one, which fails — the ADVERSARIAL shape for the
// partial-write contract (the shared fake only models an all-or-nothing store fault).
type failOnSymbolShipRepo struct {
	*fakeReclaimShipRepo
	failSymbol string
}

func (r *failOnSymbolShipRepo) AssignFleet(ctx context.Context, shipSymbol, fleet string, playerID shared.PlayerID) error {
	if shipSymbol == r.failSymbol {
		return errors.New("fleet store rejected the write")
	}
	return r.fakeReclaimShipRepo.AssignFleet(ctx, shipSymbol, fleet, playerID)
}

// A write failure mid-way returns the PARTIAL count alongside the error — the hulls already re-tagged
// stay re-tagged, and the rest are retried on a later tick (a hull left construction-tagged is safe).
func TestBootstrapGateReleaser_TradeRedirect_WriteErrorReturnsPartialCount(t *testing.T) {
	first := reclaimHull(t, "MFG-4", 40, "manufacturing", navigation.NavStatusInOrbit)
	second := reclaimHull(t, "MFG-7", 40, "manufacturing", navigation.NavStatusInOrbit)
	inner := &fakeReclaimShipRepo{all: []*navigation.Ship{first, second}}
	r := &bootstrapGateSurplusReleaser{shipRepo: &failOnSymbolShipRepo{fakeReclaimShipRepo: inner, failSymbol: "MFG-7"}}

	redirected, err := r.ReleaseGateWorkersToTrade(context.Background(), 1, []string{"MFG-4", "MFG-7"})
	require.Error(t, err)
	require.Equal(t, 1, redirected, "the count must report what actually landed before the failure")
	require.Equal(t, []assignFleetCall{{symbol: "MFG-4", fleet: tradeFleetTag}}, inner.assigned)
}
