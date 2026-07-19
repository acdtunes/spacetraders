package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// idleArbFKServer wires the idle-arb dispatch path against the REAL persistence
// layer, with SQLite foreign keys ENFORCED — the one condition the sp-1z2h
// acceptance sim's port-boundary fakes could never reproduce, and the exact gap
// the sp-1hp9 FK 23503 lived in.
//
// The container repo comes from newRecoveryTestServer (real ContainerRepositoryGORM
// on :memory:), and the ship repo is the real api.ShipRepository sharing the SAME
// db, so ClaimShip's write to ships.container_id is checked against the real
// fk_ships_container -> containers.id constraint. NewTestConnection leaves
// PRAGMA foreign_keys OFF (SQLite's default), so a claim referencing a missing
// container row would silently succeed; this turns it ON to make the FK bite the
// way Postgres's does in production.
func idleArbFKServer(t *testing.T) (*DaemonServer, *gorm.DB, int) {
	t.Helper()
	s, db, playerID := newRecoveryTestServer(t)
	require.NoError(t, db.Exec("PRAGMA foreign_keys = ON").Error,
		"the ordering bug is only observable with FK enforcement on")
	s.shipRepo = api.NewShipRepository(nil, nil, nil, nil, db, shared.NewRealClock())
	return s, db, playerID
}

func insertIdleContractHull(t *testing.T, db *gorm.DB, symbol string, playerID int) {
	t.Helper()
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       symbol,
		PlayerID:         playerID,
		AssignmentStatus: "idle",
		DedicatedFleet:   "contract", // an idle pinned contract hull (the real dispatch target)
	}).Error)
}

// The regression proof for sp-1hp9: an idle-arb dispatch must persist the container
// row BEFORE it claims the hull, so the ships.container_id FK (fk_ships_container)
// has a parent to reference — a claim-first order violates the FK. With FK enforcement
// ON against the real repos, the Add->ClaimShip order is what lets the claim land.
func TestLaunchIdleArb_PersistsContainerRowBeforeClaim_NoFKViolation(t *testing.T) {
	s, db, playerID := idleArbFKServer(t)
	insertIdleContractHull(t, db, "TORWIND-8", playerID)

	containerID, err := s.LaunchIdleArb(context.Background(), appContract.IdleArbSpec{
		ShipSymbol: "TORWIND-8",
		Good:       "FUEL",
		BuyAt:      "X1-HUB-A",
		SellAt:     "X1-HUB-B",
		MaxSpend:   100000,
		MinMargin:  1,
		PlayerID:   playerID,
		Operation:  "contract",
	})
	require.NoError(t, err,
		"idle-arb dispatch must not FK-violate: the container row must be persisted before the hull claim (sp-1hp9)")
	require.NotEmpty(t, containerID)

	// Stop the async runner from lingering on the blocking mediator past the test.
	if runner := s.registeredRunner(containerID); runner != nil {
		defer runner.cancelFunc()
	}

	// The container row exists (the FK parent), persisted synchronously by the dispatch
	// with the recovery-safe arb_run command type.
	var containerModel persistence.ContainerModel
	require.NoError(t, db.First(&containerModel, "id = ?", containerID).Error)
	require.Equal(t, "arb_run", containerModel.CommandType)

	// And the hull is claimed to THAT container — proving the operation-checked claim
	// actually landed against the FK-enforcing DB (not skipped, not violated).
	var shipModel persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-8").First(&shipModel).Error)
	require.Equal(t, "active", shipModel.AssignmentStatus)
	require.NotNil(t, shipModel.ContainerID, "the hull must be claimed after a successful dispatch")
	require.Equal(t, containerID, *shipModel.ContainerID)
}

// The claim-failure exit AFTER the row is persisted: if the hull is taken between the
// dispatcher's read and this claim, LaunchIdleArb must refuse the launch AND clean up
// the container row it already wrote — terminalizing it FAILED (claim_failed) rather
// than leaving a zombie stuck at PENDING with no runner to advance or release it
// (the sp-cr86 failure mode, here at the pre-runner claim boundary). The rival
// holder's claim must be left untouched.
func TestLaunchIdleArb_ClaimRefusedAfterRowPersisted_TerminalizesOrphanRow(t *testing.T) {
	s, db, playerID := idleArbFKServer(t)

	// A rival container already flying this hull. It must exist so the ship row can
	// reference it under the same FK the test enforces.
	insertRunningContainer(t, db, "rival-holder", "trade_route", "TRADING", "{}", playerID, nil)
	rivalID := "rival-holder"
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-8",
		PlayerID:         playerID,
		AssignmentStatus: "active",
		AssignmentOwner:  "container",
		ContainerID:      &rivalID,
		DedicatedFleet:   "contract",
	}).Error)

	containerID, err := s.LaunchIdleArb(context.Background(), appContract.IdleArbSpec{
		ShipSymbol: "TORWIND-8",
		Good:       "FUEL",
		BuyAt:      "X1-HUB-A",
		SellAt:     "X1-HUB-B",
		MaxSpend:   100000,
		MinMargin:  1,
		PlayerID:   playerID,
		Operation:  "contract",
	})
	require.Error(t, err, "a claim on an already-held hull must be refused")
	require.Empty(t, containerID)
	require.Contains(t, err.Error(), "refused")

	// The row the dispatch persisted before the failed claim must be terminalized
	// FAILED with a claim_failed reason — no zombie left at PENDING.
	var orphan persistence.ContainerModel
	require.NoError(t, db.Where("command_type = ? AND player_id = ?", "arb_run", playerID).First(&orphan).Error,
		"the dispatch must have persisted an arb_run container row before the claim failed")
	require.Equal(t, "FAILED", orphan.Status, "the orphan row must be terminalized, not left PENDING")
	require.Contains(t, orphan.ExitReason, "claim_failed")
	require.Nil(t, s.registeredRunner(orphan.ID), "no runner may own a launch that never claimed its hull")

	// The rival holder's claim is untouched.
	var shipModel persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-8").First(&shipModel).Error)
	require.Equal(t, "active", shipModel.AssignmentStatus)
	require.NotNil(t, shipModel.ContainerID)
	require.Equal(t, "rival-holder", *shipModel.ContainerID)
}
