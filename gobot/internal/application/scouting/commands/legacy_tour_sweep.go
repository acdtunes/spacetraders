package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The graduation edge for market tours (Admiral 2026-08-08).
//
// Tours are an operator's tool for the bootstrap phase only. A tour that is still flying
// when the home jump gate completes is not merely redundant — it HOLDS A PROBE, and that
// probe is the supply parked sensing is about to place. Refusing to START one past the
// edge is therefore only half the rule; the other half is stopping the ones already up.
//
// It lives on the sensing coordinator because that coordinator IS the graduation edge:
// its whole tick is gated on EXPANSION, so this runs on the first post-gate tick and on
// every tick after. That last part is what makes it durable rather than a one-shot —
// container recovery re-adopts scout_tour workers across a daemon restart, so a boot that
// lands past the edge re-floats a tour that nothing else would ever stop.

// LegacyTourSweeper stops the market tours the touring model left behind and returns their
// hulls to the pool. Satisfied by the daemon's grpc adapter, which owns both the container
// registry and the ship claims.
type LegacyTourSweeper interface {
	// RunningTours lists the player's RUNNING scout_tour containers.
	RunningTours(ctx context.Context, playerID shared.PlayerID) ([]string, error)
	// StopTourAndReleaseHull stops one tour container and force-releases whatever hull it
	// claimed, so the probe returns to the general pool. Best-effort by contract: a
	// failure is reported, never fatal.
	StopTourAndReleaseHull(ctx context.Context, playerID shared.PlayerID, containerID string) error
}

// SetLegacyTourSweeper wires the graduation sweep. nil disables it — and unlike the
// engine ports, that is honest rather than dangerous: an unswept tour is a wasted probe,
// not a wrong decision, and holding the whole sensing tick over one would trade a large
// harm for a small one. It is logged so the omission is visible.
func (h *RunProbeSensingCoordinatorHandler) SetLegacyTourSweeper(s LegacyTourSweeper) {
	h.tourSweeper = s
}

// sweepLegacyTours stops every running market tour. Returns how many it stopped.
//
// It is UNCONDITIONAL within EXPANSION rather than keyed on any "was this ours" mark: past
// the gate there is no legitimate market tour of any provenance, operator-started or
// coordinator-spawned, because the verb that starts one refuses past this same edge. A
// discriminator would only create a class this sweep cannot reach.
func (h *RunProbeSensingCoordinatorHandler) sweepLegacyTours(ctx context.Context, cmd *RunProbeSensingCoordinatorCommand) int {
	if h.tourSweeper == nil {
		return 0
	}
	logger := common.LoggerFromContext(ctx)

	tours, err := h.tourSweeper.RunningTours(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Graduation tour sweep skipped: failed to list running market tours: %v", err), map[string]interface{}{
			"action": "legacy_tour_sweep_list_failed",
		})
		return 0
	}
	if len(tours) == 0 {
		return 0
	}

	stopped := 0
	for _, containerID := range tours {
		if serr := h.tourSweeper.StopTourAndReleaseHull(ctx, cmd.PlayerID, containerID); serr != nil {
			logger.Log("WARNING", fmt.Sprintf("Failed to stop market tour %s at the graduation edge (retried next tick): %v", containerID, serr), map[string]interface{}{
				"action":       "legacy_tour_sweep_failed",
				"container_id": containerID,
			})
			continue
		}
		stopped++
	}
	if stopped > 0 {
		logger.Log("INFO", fmt.Sprintf(
			"Graduation edge: stopped %d market tour(s) and returned their probes to the pool — the home jump gate is built, so market freshness is parked sensing's and a circulating tour only holds a hull this engine needs to place",
			stopped), map[string]interface{}{
			"action":  "legacy_tour_swept",
			"stopped": stopped,
		})
	}
	return stopped
}
