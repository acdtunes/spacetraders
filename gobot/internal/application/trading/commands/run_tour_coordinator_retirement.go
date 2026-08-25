package commands

// run_tour_coordinator_retirement.go — the retirement stand-down: a hull the operator marked
// retiring leaves service from INSIDE the container already flying it, at the first boundary
// its hold is empty.

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// retiringDrained reports the operator's retirement mark read FRESH off the hull, so a mark
// set while this container is mid-plan binds without a restart. Fail OPEN: a hull that cannot
// be read keeps touring exactly as an unmarked one, because retirement is an operator
// convenience and must never become a stall source.
func (h *RunTourCoordinatorHandler) retiringDrained(ctx context.Context, cmd *RunTourCoordinatorCommand) bool {
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil || ship == nil {
		return false
	}
	return ship.RetirementDrained()
}

// standDownRetiring ends the run through the ordinary completion path — the runner releases
// the claim and publishes completion — naming the retirement once so an operator can see the
// mark actually took.
func (h *RunTourCoordinatorHandler) standDownRetiring(cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse, logger common.ContainerLogger) {
	response.RetirementStandDown = true
	response.ExitReason = tourExitRetired
	response.ExitDetail = fmt.Sprintf("retiring hull stands down drained after %d productive tour(s)", response.ToursCompleted)
	logger.Log("INFO", fmt.Sprintf(
		"Retiring hull stands down drained - %s is marked retiring and its hold is empty at this boundary; it is planned no further legs",
		cmd.ShipSymbol), map[string]interface{}{
		"action": "tour_retirement_stand_down", "ship_symbol": cmd.ShipSymbol,
		"container_id": cmd.ContainerID, "tours_completed": response.ToursCompleted,
	})
}
