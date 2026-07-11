package commands

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeFerryRepositioner records the travel delegation (destination + the jump bound
// forwarded) and can inject a failure — the ferry worker must forward exactly one move to
// the shared bounded travel machinery (the trade-route coordinator's
// RepositionToWaypointWithinJumps) and surface its error honestly.
type fakeFerryRepositioner struct {
	calls  []string // "ship->destination"
	bounds []int    // the maxJumps forwarded per call — proves the ferry rides the BOUNDED resolver at the resolved bound
	err    error
}

func (f *fakeFerryRepositioner) RepositionToWaypointWithinJumps(_ context.Context, shipSymbol, destinationWaypoint string, _, maxJumps int) error {
	f.calls = append(f.calls, shipSymbol+"->"+destinationWaypoint)
	f.bounds = append(f.bounds, maxJumps)
	return f.err
}

// The ferry worker delegates the whole cross-system relay to the shared travel
// machinery — it writes no jump logic of its own (sp-f5pr, twin of scout_reposition).
func TestWorkerFerry_DelegatesToTravel(t *testing.T) {
	rep := &fakeFerryRepositioner{}
	handler := NewWorkerFerryHandler(rep)
	cmd := &WorkerFerryCommand{
		PlayerID:            shared.MustNewPlayerID(1),
		ShipSymbol:          "LIGHT-7",
		DestinationWaypoint: "X1-DP51-M1",
		CoordinatorID:       "worker_rebalancer_coordinator-1",
	}

	resp, err := handler.Handle(context.Background(), cmd)

	require.NoError(t, err)
	require.Equal(t, []string{"LIGHT-7->X1-DP51-M1"}, rep.calls, "the ferry delegates exactly one move to shared travel()")
	r, ok := resp.(*WorkerFerryResponse)
	require.True(t, ok, "returns a WorkerFerryResponse")
	require.Equal(t, "LIGHT-7", r.ShipSymbol)
	require.Equal(t, "X1-DP51-M1", r.DestinationWaypoint)
}

// A travel failure is surfaced as an error so the container FAILS honestly (the runner
// releases the claim and the coordinator re-evaluates the hull next tick) rather than
// reporting a false success on a stranded hull (sp-f5pr fail-closed).
func TestWorkerFerry_TravelError_FailsHonestly(t *testing.T) {
	rep := &fakeFerryRepositioner{err: fmt.Errorf("destination jump gate under construction")}
	handler := NewWorkerFerryHandler(rep)
	cmd := &WorkerFerryCommand{
		PlayerID:            shared.MustNewPlayerID(1),
		ShipSymbol:          "LIGHT-7",
		DestinationWaypoint: "X1-DP51-M1",
	}

	_, err := handler.Handle(context.Background(), cmd)

	require.Error(t, err, "a travel failure surfaces — never a false success on a stranded hull")
	require.Contains(t, err.Error(), "destination jump gate under construction", "the underlying cause is preserved verbatim")
}

// An UNSET RepositionJumpBound (the captain left [trade_fleet].reposition_jump_bound at 0, or a
// ferry launched from a config that predates the knob) must forward the resolved DEFAULT (12,
// resolveRepositionJumpBound) to the bounded resolver — never 0, which degrades to the strict
// fetch-through Path that fail-closes a far-cluster launch (the sp-fwxm bug). This is what makes
// the fix safe even where the persist boundary carried no bound.
func TestWorkerFerry_UnsetBound_ForwardsResolvedDefault(t *testing.T) {
	rep := &fakeFerryRepositioner{}
	handler := NewWorkerFerryHandler(rep)
	cmd := &WorkerFerryCommand{
		PlayerID:            shared.MustNewPlayerID(1),
		ShipSymbol:          "LIGHT-7",
		DestinationWaypoint: "X1-DP51-M1",
		// RepositionJumpBound left 0 (unset).
	}

	_, err := handler.Handle(context.Background(), cmd)

	require.NoError(t, err)
	require.Equal(t, []int{repositionJumpBoundDefault}, rep.bounds, "an unset bound must forward the resolved default (12) so the ferry always rides the stored-adjacency RepositionPath, never the strict Path")
}

// A captain's explicit [trade_fleet].reposition_jump_bound override (threaded through the persist
// boundary into RepositionJumpBound) must be forwarded VERBATIM — the ferry honors the configured
// reach, not a hardcoded value.
func TestWorkerFerry_ConfiguredBound_ForwardedVerbatim(t *testing.T) {
	rep := &fakeFerryRepositioner{}
	handler := NewWorkerFerryHandler(rep)
	cmd := &WorkerFerryCommand{
		PlayerID:            shared.MustNewPlayerID(1),
		ShipSymbol:          "LIGHT-7",
		DestinationWaypoint: "X1-DP51-M1",
		RepositionJumpBound: 20, // the captain's override
	}

	_, err := handler.Handle(context.Background(), cmd)

	require.NoError(t, err)
	require.Equal(t, []int{20}, rep.bounds, "a configured bound must be forwarded verbatim (never overridden by the default)")
}

// sp-fwxm THE FERRY PD21 PIN (harness-honest, the kl16 idiom, ferry variant — the vdld
// far-cluster unblock). A worker ferry from a READABLE origin to an unreadable-gate factory
// system — the live C81/GS93 vdld launches, whose destination gate sits in the sp-ikx1
// unreadable-backoff set — must fly via the stored-adjacency RepositionPath, routing PAST the
// unreadable gate, because a ferry is a hull MOVE (no money commitment), exactly like a tour
// reposition (sp-kl16) or a scout reposition (sp-8k9m). This drives the REAL trade-route
// coordinator (the ferry's production movement port) with a gate graph where the strict
// fetch-through Path FAILS (pathErr, the unreadable-gate fail-closed) while RepositionPath
// returns the valid route: a ferry that stayed strict (the sp-fwxm bug) dies on pathErr and
// never jumps, so the far-cluster chain idles workerless; only the bounded resolver flies.
// Asserting the bound reached RepositionPath (== the configured default) proves it is
// RepositionPath, not Path — NOT a fake with a valid strict path, which would HIDE the
// fail-closed (the kl16 harness-honesty finding).
func TestWorkerFerry_UnreadableGateFactory_FliesViaRepositionPath(t *testing.T) {
	// Readable origin: the light-hauler sits ON its home jump gate, ready to jump.
	origin := newTravelShipAtGate(t, "LIGHT-7", "X1-KA42-GATE")
	mediator := &travelMediator{
		gateResp: gateResponseAt(t, "X1-KA42-GATE"),
		jumpResp: &navCmd.JumpShipResponse{Success: true, CooldownSeconds: 60},
	}
	clock := &travelFakeClock{}
	// The trade-route coordinator is the ferry's real movement port (NewWorkerFerryHandler wires
	// it in production, main.go:752). shipRepo serves the origin hull for the initial load and
	// the post-jump reload alike — the flight mechanics are not under test here, only WHICH
	// resolver the ferry rides.
	coordinator := NewRunTradeRouteCoordinatorHandler(mediator, &travelShipRepo{ship: origin}, nil, nil, clock, nil)
	// The unreadable-gate boundary: strict Path fail-closes on the origin gate (pathErr, the
	// verbatim capped-router error the live incident emitted), while the stored adjacency retains
	// the route past it (repositionPath). Modelling the REAL boundary — a valid strict path would
	// hide the fail-closed.
	fake := &fakeGateGraph{
		pathErr:        errors.New("no jump-gate route from X1-KA42 to X1-C81 within 5 jumps"),
		repositionPath: []string{"X1-KA42", "X1-C81"},
	}
	coordinator.SetGateGraph(fake)

	ferry := NewWorkerFerryHandler(coordinator)
	_, err := ferry.Handle(context.Background(), &WorkerFerryCommand{
		PlayerID:            shared.MustNewPlayerID(1),
		ShipSymbol:          "LIGHT-7",
		DestinationWaypoint: "X1-C81-MARKET",
		CoordinatorID:       "worker_rebalancer_coordinator-1",
	})

	require.NoError(t, err, "the ferry must fly PAST the unreadable factory gate via the stored-adjacency RepositionPath — a strict ferry dies on pathErr and the far-cluster chain idles workerless")
	require.Equal(t, repositionJumpBoundDefault, fake.repositionBound, "the ferry must resolve its cross-cluster leg via RepositionPath at the default bound (12), never the strict fetch-through Path (bound 0)")
	require.Len(t, mediator.jumps, 1, "the ferry must actually jump past the unreadable gate, not abort on the strict pathErr")
}
