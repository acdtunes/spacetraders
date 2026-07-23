package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// The regression proof for sp-1hp9, carried onto the long-haul launcher: a LaunchLongHaul
// dispatch MUST persist the container row BEFORE it claims the hull, so the
// ships.container_id FK (fk_ships_container) has a parent to reference — a claim-first order
// FK-violates (23503) exactly the way the idle-arb path did. With FK enforcement ON against
// the real repos, the Add->ClaimShip order is what lets the claim land, AND the persisted
// config carries the operation="long-haul" claim identity so createShipAssignments re-claims
// idempotently on start and restart recovery (RULINGS #2).
func TestLaunchLongHaul_PersistsContainerRowBeforeClaim_NoFKViolation(t *testing.T) {
	s, db, playerID := idleArbFKServer(t)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "HAULER-9",
		PlayerID:         playerID,
		AssignmentStatus: "idle",
		DedicatedFleet:   "long-haul", // an idle long-haul-tagged hull (the real dispatch target)
		CargoCapacity:    120,
	}).Error)

	containerID, err := s.LaunchLongHaul(context.Background(), tradingCmd.LongHaulLaunchSpec{
		ShipSymbol:       "HAULER-9",
		AgentSymbol:      "TORWIND",
		PlayerID:         playerID,
		Iterations:       -1,
		PerHaulCap:       1_000_000,
		TotalExposureCap: 2_000_000,
	})
	require.NoError(t, err,
		"long-haul dispatch must not FK-violate: the container row must be persisted before the hull claim (sp-1hp9)")
	require.NotEmpty(t, containerID)

	// Stop the async runner from lingering on the blocking mediator past the test.
	if runner := s.registeredRunner(containerID); runner != nil {
		defer runner.cancelFunc()
	}

	// The container row exists (the FK parent), persisted synchronously with the
	// recovery-safe longhaul_arb command type, carrying the operation="long-haul" claim
	// identity for restart-recovery re-claim (RULINGS #2).
	var containerModel persistence.ContainerModel
	require.NoError(t, db.First(&containerModel, "id = ?", containerID).Error)
	require.Equal(t, "longhaul_arb", containerModel.CommandType)
	require.Contains(t, containerModel.Config, "long-haul",
		"the persisted config must carry the operation=long-haul claim identity for restart recovery")

	// And the hull is claimed to THAT container — proving the operation-checked claim
	// actually landed against the FK-enforcing DB (not skipped, not violated).
	var shipModel persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "HAULER-9").First(&shipModel).Error)
	require.Equal(t, "active", shipModel.AssignmentStatus)
	require.NotNil(t, shipModel.ContainerID, "the hull must be claimed after a successful dispatch")
	require.Equal(t, containerID, *shipModel.ContainerID)
}

// The claim-failure exit AFTER the row is persisted: if the hull is taken between the
// coordinator's snapshot and this claim, LaunchLongHaul must refuse the launch AND clean up
// the container row it already wrote — terminalizing it FAILED (claim_failed) rather than
// leaving a zombie PENDING with no runner to advance or release it. The rival holder's claim
// is left untouched.
func TestLaunchLongHaul_ClaimRefusedAfterRowPersisted_TerminalizesOrphanRow(t *testing.T) {
	s, db, playerID := idleArbFKServer(t)

	insertRunningContainer(t, db, "rival-holder", "trade_route", "TRADING", "{}", playerID, nil)
	rivalID := "rival-holder"
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "HAULER-9",
		PlayerID:         playerID,
		AssignmentStatus: "active",
		AssignmentOwner:  "container",
		ContainerID:      &rivalID,
		DedicatedFleet:   "long-haul",
		CargoCapacity:    120,
	}).Error)

	containerID, err := s.LaunchLongHaul(context.Background(), tradingCmd.LongHaulLaunchSpec{
		ShipSymbol:       "HAULER-9",
		AgentSymbol:      "TORWIND",
		PlayerID:         playerID,
		Iterations:       -1,
		PerHaulCap:       1_000_000,
		TotalExposureCap: 2_000_000,
	})
	require.Error(t, err, "a claim on an already-held hull must be refused")
	require.Empty(t, containerID)
	require.Contains(t, err.Error(), "refused")

	// The row the dispatch persisted before the failed claim must be terminalized FAILED
	// with a claim_failed reason — no zombie left at PENDING.
	var orphan persistence.ContainerModel
	require.NoError(t, db.Where("command_type = ? AND player_id = ?", "longhaul_arb", playerID).First(&orphan).Error,
		"the dispatch must have persisted a longhaul_arb container row before the claim failed")
	require.Equal(t, "FAILED", orphan.Status, "the orphan row must be terminalized, not left PENDING")
	require.Contains(t, orphan.ExitReason, "claim_failed")
	require.Nil(t, s.registeredRunner(orphan.ID), "no runner may own a launch that never claimed its hull")

	// The rival holder's claim is untouched.
	var shipModel persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "HAULER-9").First(&shipModel).Error)
	require.Equal(t, "active", shipModel.AssignmentStatus)
	require.NotNil(t, shipModel.ContainerID)
	require.Equal(t, "rival-holder", *shipModel.ContainerID)
}

// Arming the standing coordinator persists a recovery-safe container built through the SAME
// factory recovery uses — the wiring smoke that the longhaul_arb_coordinator factory entry is
// registered (a typo'd/unregistered command type fails buildCommandForType here) and that the
// container carries the coordinator type the recovery + observability paths key on. CommandType
// and ContainerType are written synchronously by Add, so the assertion never races the runner.
func TestLongHaulArbCoordinator_FreshArm_PersistsRecoverySafeCoordinator(t *testing.T) {
	s, db, playerID := idleArbFKServer(t)

	id, err := s.LongHaulArbCoordinator(context.Background(), playerID, "TORWIND")
	require.NoError(t, err)
	require.NotEmpty(t, id)
	if runner := s.registeredRunner(id); runner != nil {
		defer runner.cancelFunc()
	}

	var model persistence.ContainerModel
	require.NoError(t, db.First(&model, "id = ?", id).Error)
	require.Equal(t, "longhaul_arb_coordinator", model.CommandType,
		"the coordinator must be built + persisted through the shared factory (recovery-safe)")
	require.Equal(t, string(container.ContainerTypeLongHaulArbCoordinator), model.ContainerType)
}

// A second arm while one coordinator already runs must return the LIVE coordinator's id rather
// than spawning a rival that would double-dispatch the long-haul fleet. Deterministic: the
// pre-inserted RUNNING coordinator makes the arm short-circuit before it builds or persists
// anything, so no runner races the assertion.
func TestLongHaulArbCoordinator_Idempotent_ReturnsExistingWhenAlreadyArmed(t *testing.T) {
	s, db, playerID := idleArbFKServer(t)
	insertRunningContainer(t, db, "longhaul-live", "longhaul_arb_coordinator", "LONGHAUL_ARB_COORDINATOR", "{}", playerID, nil)

	id, err := s.LongHaulArbCoordinator(context.Background(), playerID, "TORWIND")
	require.NoError(t, err)
	require.Equal(t, "longhaul-live", id,
		"arming while one is already running must return the live coordinator, not spawn a rival")

	var count int64
	require.NoError(t, db.Model(&persistence.ContainerModel{}).
		Where("command_type = ? AND player_id = ?", "longhaul_arb_coordinator", playerID).Count(&count).Error)
	require.Equal(t, int64(1), count, "no second coordinator container may be created")
}
