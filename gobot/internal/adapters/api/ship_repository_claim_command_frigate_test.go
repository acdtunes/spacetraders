package api

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-3tsjz (RULINGS #7): the command frigate is NEVER a depot hull. A
// warehouse/stocker ClaimShip of the flagship must be REJECTED with the typed
// command-hull error — recognised by the SAME markers IsCommandHull uses (the
// registration role "COMMAND" OR the conventional "*-1" symbol), for BOTH depot
// operations. The rejection leaves the row untouched, exactly like the
// foreign-fleet dedication rejection: it fails without ever attaching the
// frigate to a depot container. This is the single robust point that closes the
// restart-recovery re-claim (TestClaimShip_OrphanedDepotContainer... below).
func TestClaimShip_RejectsCommandFrigateForDepotOperation(t *testing.T) {
	cases := []struct {
		name      string
		symbol    string
		role      string
		operation string
	}{
		{"warehouse rejects the frigate by the -1 symbol", "TORWIND-1", "HAULER", "warehouse"},
		{"warehouse rejects the frigate by the COMMAND role", "TORWIND-9", "COMMAND", "warehouse"},
		{"stocker rejects the frigate by the -1 symbol", "TORWIND-1", "HAULER", "stocker"},
		{"stocker rejects the frigate by the COMMAND role", "TORWIND-9", "COMMAND", "stocker"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, db, playerID := newDedicationTestRepo(t)

			// The exact live shape: no dedicated_fleet tag, so the sp-l7h2
			// dedication guard cannot catch it — only the command-hull guard can.
			require.NoError(t, db.Create(&persistence.ShipModel{
				ShipSymbol:       tc.symbol,
				PlayerID:         playerID.Value(),
				Role:             tc.role,
				AssignmentStatus: "idle",
			}).Error)
			// Seed the parent so a MISSING guard would claim cleanly (a clean RED),
			// not trip the container FK on the write it must never reach.
			seedContainerParent(t, db, "depot-container-1", playerID.Value())

			err := repo.ClaimShip(context.Background(), tc.symbol, "depot-container-1", playerID, tc.operation)
			require.Error(t, err)

			var commandHull *shared.ShipIsCommandHullError
			require.ErrorAs(t, err, &commandHull, "a depot claim of the command frigate must fail with the typed command-hull error")
			require.Equal(t, tc.operation, commandHull.Operation)

			var model persistence.ShipModel
			require.NoError(t, db.Where("ship_symbol = ?", tc.symbol).First(&model).Error)
			require.Equal(t, "idle", model.AssignmentStatus, "a rejected claim must not mutate the assignment")
			require.Nil(t, model.ContainerID, "a rejected claim must not attach the frigate to a depot container")
		})
	}
}

// Control (byte-identical): a REGULAR dedicated hauler — role HAULER, no "*-1"
// symbol — is claimed by its own depot operation exactly as before. The
// command-frigate guard only rejects the flagship; it never touches a regular
// depot hull, so continuous warehousing/stocking on a proper hauler is
// unaffected.
func TestClaimShip_ClaimsRegularHaulerForDepotOperation(t *testing.T) {
	cases := []struct {
		operation   string
		containerID string
	}{
		{"warehouse", "warehouse-X1-HOME-A1"},
		{"stocker", "stocker-X1-HOME-A1"},
	}

	for _, tc := range cases {
		t.Run(tc.operation+" claims its regular dedicated hull", func(t *testing.T) {
			repo, db, playerID := newDedicationTestRepo(t)

			require.NoError(t, db.Create(&persistence.ShipModel{
				ShipSymbol:       "TORWIND-7",
				PlayerID:         playerID.Value(),
				Role:             "HAULER",
				AssignmentStatus: "idle",
				DedicatedFleet:   tc.operation,
			}).Error)
			seedContainerParent(t, db, tc.containerID, playerID.Value())

			err := repo.ClaimShip(context.Background(), "TORWIND-7", tc.containerID, playerID, tc.operation)
			require.NoError(t, err, "a regular depot hull must be claimed by its own operation")

			var model persistence.ShipModel
			require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-7").First(&model).Error)
			require.Equal(t, "active", model.AssignmentStatus)
			require.NotNil(t, model.ContainerID)
			require.Equal(t, tc.containerID, *model.ContainerID)
		})
	}
}

// The reported bug (sp-3tsjz), reproduced at the single write path: on daemon
// restart ReleaseAllActive frees the frigate to idle with no dedicated_fleet tag,
// the orphaned warehouse-TORWIND-1 container RECOVERS from the container
// registry and re-runs ClaimShip(operation="warehouse"). That recovered re-claim
// — which every sp-gvvph guard (scaler reclaim / launch viability) bypasses —
// must now be REJECTED at ClaimShip, so the flagship can never come back as a
// warehouse. This is the exact call attemptClaimShip's operation-keyed branch
// makes on recovery.
func TestClaimShip_OrphanedDepotContainerCannotReclaimFrigateOnRecovery(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)

	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-1",
		PlayerID:         playerID.Value(),
		Role:             "COMMAND",
		AssignmentStatus: "idle", // freed by ReleaseAllActive at daemon startup
	}).Error)
	// Seed the recovered orphan container so a MISSING guard would re-claim
	// cleanly (a clean RED), not trip the container FK on the forbidden write.
	seedContainerParent(t, db, "warehouse-TORWIND-1-abc123", playerID.Value())

	err := repo.ClaimShip(context.Background(), "TORWIND-1", "warehouse-TORWIND-1-abc123", playerID, "warehouse")
	require.Error(t, err)

	var commandHull *shared.ShipIsCommandHullError
	require.ErrorAs(t, err, &commandHull, "the recovered orphan container must not re-claim the frigate")

	var model persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", "TORWIND-1").First(&model).Error)
	require.Equal(t, "idle", model.AssignmentStatus, "the frigate must stay idle — never re-seated as a warehouse")
	require.Nil(t, model.ContainerID)
}

// EVERY path, including the same-container idempotent re-claim: even if
// ReleaseAllActive did NOT free the frigate first and it is still recorded as
// active on the orphaned depot container, that container's recovery re-claim is
// STILL rejected — the command-hull guard runs BEFORE the idempotent
// same-container success, so a frigate can never be idempotently re-adopted as a
// depot hull. (A regular hull keeps its idempotent recovery; only the flagship
// is refused for a depot role.)
func TestClaimShip_FrigateRejectedEvenOnIdempotentDepotReclaim(t *testing.T) {
	repo, db, playerID := newDedicationTestRepo(t)
	seedContainerParent(t, db, "warehouse-TORWIND-1-abc123", playerID.Value())

	containerID := "warehouse-TORWIND-1-abc123"
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       "TORWIND-1",
		PlayerID:         playerID.Value(),
		Role:             "COMMAND",
		AssignmentStatus: "active",
		ContainerID:      &containerID,
	}).Error)

	err := repo.ClaimShip(context.Background(), "TORWIND-1", containerID, playerID, "warehouse")
	require.Error(t, err)

	var commandHull *shared.ShipIsCommandHullError
	require.ErrorAs(t, err, &commandHull, "a frigate must never be idempotently re-adopted as a depot hull")
}
