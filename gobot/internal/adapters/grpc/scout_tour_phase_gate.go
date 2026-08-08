package grpc

import (
	"context"
	"fmt"
	"log"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The bootstrap-phase gate on market tours, and the sweep that enforces it on tours
// already flying (Admiral 2026-08-08: market tours run only during the bootstrap phase).
//
// The standing scout-post coordinator that used to circulate hulls over per-system posts
// is deleted. What survives is an operator-started tour on a named hull — and it survives
// only before the home jump gate is built, because past that edge parked sensing owns
// market freshness and every probe a tour holds is one it cannot place.

// bootstrapPhaseGate is the narrow slice of the shared EXPANSION reader the tour verbs
// need. It is deliberately the SAME reader parked sensing gates its whole tick on, so the
// era in which tours are refused is exactly the era in which parked sensing runs — there
// is no window belonging to both engines, and none belonging to neither.
type bootstrapPhaseGate interface {
	InExpansion(ctx context.Context, playerID shared.PlayerID) (bool, error)
}

// SetBootstrapPhaseGate wires the era gate the manual tour verbs refuse past.
func (s *DaemonServer) SetBootstrapPhaseGate(g bootstrapPhaseGate) { s.phaseGate = g }

// refuseTourOutsideBootstrap is the guard every tour-start verb passes through.
//
// FAIL-CLOSED on an unwired or unreadable phase, and the asymmetry is deliberate. This
// refuses an operator's non-spending action, which is recoverable in seconds; admitting one
// would put a circulating hull into the era whose entire point is that nothing circulates,
// where it would sit undetected because nothing is left to notice it. "I cannot prove we
// are still in bootstrap" is not "we are".
func (s *DaemonServer) refuseTourOutsideBootstrap(ctx context.Context, playerID int) error {
	if s.phaseGate == nil {
		return fmt.Errorf("market tour refused: the bootstrap-phase gate is not wired, so the era cannot be verified — and a tour past the jump gate holds a probe parked sensing needs. This is a daemon wiring fault, not an operator error")
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return fmt.Errorf("market tour refused: %w", err)
	}
	inExpansion, err := s.phaseGate.InExpansion(ctx, pid)
	if err != nil {
		return fmt.Errorf("market tour refused: the bootstrap phase is unreadable, and a tour is never started on an unknown era: %w", err)
	}
	if inExpansion {
		return fmt.Errorf("market tour refused: the home jump gate is BUILT, so the fleet is past the bootstrap phase and market tours are over (Admiral 2026-08-08). Market freshness is the parked-sensing coordinator's — it parks a probe per market rather than circulating one — and a tour here would only hold a hull that engine wants to place")
	}
	return nil
}

// daemonLegacyTourSweeper stops market tours still flying past the graduation edge and
// returns their hulls to the general pool. It is the sensing coordinator's arm into the
// container registry, built over the SAME primitives the deleted reconciler used, so a
// swept hull comes back exactly as it did when an operator stopped one by hand.
type daemonLegacyTourSweeper struct{ server *DaemonServer }

// RunningTours lists the player's RUNNING scout_tour containers.
func (w *daemonLegacyTourSweeper) RunningTours(ctx context.Context, playerID shared.PlayerID) ([]string, error) {
	workers, err := w.server.containerRepo.ListRunningScoutWorkers(ctx, playerID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(workers))
	for _, worker := range workers {
		ids = append(ids, worker.ID)
	}
	return ids, nil
}

// StopTourAndReleaseHull stops the tour, then frees whatever hull it claimed.
//
// ORDER MATTERS: stopping first means nothing is still flying the hull when its claim
// drops. The release is a CAS-retry that refuses to touch a hull which has already moved
// to a new owner, so a probe parked sensing has claimed in the meantime is never yanked.
func (w *daemonLegacyTourSweeper) StopTourAndReleaseHull(ctx context.Context, playerID shared.PlayerID, containerID string) error {
	if err := w.server.StopContainer(containerID); err != nil {
		return fmt.Errorf("stop tour %s: %w", containerID, err)
	}
	ships, err := w.server.shipRepo.FindByContainer(ctx, containerID, playerID)
	if err != nil {
		return fmt.Errorf("read hulls of tour %s: %w", containerID, err)
	}
	for _, ship := range ships {
		if ship == nil || !ship.IsAssigned() {
			continue
		}
		symbol := ship.ShipSymbol()
		if _, _, rerr := w.server.shipRepo.SaveWithRetry(ctx, symbol, playerID,
			func(sh *navigation.Ship) (bool, error) {
				if !sh.IsAssigned() || sh.ContainerID() != containerID {
					return false, nil
				}
				sh.ForceRelease("market_tours_retired", w.server.clock)
				return true, nil
			}); rerr != nil {
			log.Printf("WARNING: market tour %s stopped but hull %s could not be released (retried next sensing tick): %v", containerID, symbol, rerr)
		}
	}
	return nil
}

// NewLegacyTourSweeper builds the sweeper over the daemon the composition already holds,
// so the wiring cannot pass half of it.
func NewLegacyTourSweeper(server *DaemonServer) *daemonLegacyTourSweeper {
	return &daemonLegacyTourSweeper{server: server}
}
