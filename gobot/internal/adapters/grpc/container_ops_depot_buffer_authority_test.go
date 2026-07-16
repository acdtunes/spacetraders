package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// sp-j4mc: Buffer-authority HANDOFF — when an ARMED capacity reconciler (epic st-7zk) owns a
// player's depot buffers, the live depot warehouse path STANDS DOWN its buffer re-solve so the
// two never fight over supported_goods. Both write the SAME field via the SAME primitive
// (UpdateSupportedGoods) and rank differently (depot = receipt VALUE, reconciler = FREQUENCY),
// so an armed reconciler + a re-solving depot thrash a budget-tight hub every reload.
//
// "Armed" = a RUNNING CAPACITY_RECONCILER container for the player whose DryRun is FALSE —
// BOTH capacity_dry_run AND capacity_launch_dry_run false (the EXACT predicate
// buildCapacityReconcilerCoordinatorCommand arms on). Anything else — no reconciler, a DryRun
// reconciler (the CURRENT live state), a not-RUNNING reconciler, a nil/unreadable/erroring
// container repo — FAILS SAFE to depot-owns (a query failure must NEVER strand buffers).
//
// These tests drive the REAL launchDepotWarehouse reload against a NON-IDLE warehouse carrying a
// STALE whitelist (reusing nonIdleDepotWarehouseFixture) and, per reconciler state, assert the
// OBSERVABLE outcome on the persisted supported_goods row: the depot OVERWRITES it (owns) or
// LEAVES IT INTACT (stands down to the reconciler). Detection + gate are exercised end to end
// through the driving entry — no internal seam is poked.

// seedReconcilerContainer inserts a CAPACITY_RECONCILER container row for the player with the
// given lifecycle status and dry-run config, so the REAL armed-check (container repo query +
// config parse) classifies it. An empty config ({}) = both dry-run flags absent = ARMED.
func seedReconcilerContainer(t *testing.T, db *gorm.DB, playerID int, status, configJSON string) {
	t.Helper()
	now := time.Now()
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID:            "cap-recon-" + status,
		PlayerID:      playerID,
		ContainerType: string(container.ContainerTypeCapacityReconciler),
		CommandType:   "capacity_reconciler_coordinator",
		Status:        status,
		Config:        configJSON,
		StartedAt:     &now,
		HeartbeatAt:   &now,
	}).Error)
}

func TestDepotBufferAuthority_StandsDownOnlyForArmedReconciler(t *testing.T) {
	const staleGood = "ELECTRONICS" // stamped by the old selector on the running warehouse
	const freshGood = "CLOTHING"    // what the depot re-solve WOULD surface if it owned the buffer
	minerRows := []persistence.DemandCandidate{
		{Good: freshGood, ContractCount: 5, MaxContractUnits: 40,
			ForeignMarket: "X2-SRC", ForeignSystem: "X2", ForeignAsk: 500,
			HomeAsk: 500, HomeAskKnown: true, ContractRewardPerUnit: 5000},
	}

	cases := []struct {
		name string
		// seed mutates the seeded world (reconciler rows) before the reload.
		seed func(t *testing.T, s *DaemonServer, db *gorm.DB, playerID int)
		// standsDown true => the depot must LEAVE supported_goods intact (reconciler owns);
		// false => the depot OWNS and overwrites with the re-solved whitelist (today's behavior).
		standsDown bool
	}{
		{
			name:       "no reconciler -> depot owns (regression, byte-identical to today)",
			standsDown: false,
		},
		{
			name: "DryRun reconciler via launch flag -> depot STILL owns (the current live state)",
			seed: func(t *testing.T, s *DaemonServer, db *gorm.DB, playerID int) {
				seedReconcilerContainer(t, db, playerID, "RUNNING", `{"capacity_launch_dry_run":true}`)
			},
			standsDown: false,
		},
		{
			name: "DryRun reconciler via config flag -> depot STILL owns",
			seed: func(t *testing.T, s *DaemonServer, db *gorm.DB, playerID int) {
				seedReconcilerContainer(t, db, playerID, "RUNNING", `{"capacity_dry_run":true}`)
			},
			standsDown: false,
		},
		{
			name: "armed reconciler (both flags absent) -> depot STANDS DOWN",
			seed: func(t *testing.T, s *DaemonServer, db *gorm.DB, playerID int) {
				seedReconcilerContainer(t, db, playerID, "RUNNING", `{}`)
			},
			standsDown: true,
		},
		{
			name: "armed reconciler (both flags explicitly false) -> depot STANDS DOWN",
			seed: func(t *testing.T, s *DaemonServer, db *gorm.DB, playerID int) {
				seedReconcilerContainer(t, db, playerID, "RUNNING", `{"capacity_dry_run":false,"capacity_launch_dry_run":false}`)
			},
			standsDown: true,
		},
		{
			name: "armed reconciler but NOT running (stopped) -> depot owns (not-running != armed)",
			seed: func(t *testing.T, s *DaemonServer, db *gorm.DB, playerID int) {
				seedReconcilerContainer(t, db, playerID, "STOPPED", `{}`)
			},
			standsDown: false,
		},
		{
			name: "nil container repo -> depot owns (repo unreadable, fail-safe)",
			seed: func(t *testing.T, s *DaemonServer, db *gorm.DB, playerID int) {
				s.containerRepo = nil
			},
			standsDown: false,
		},
		{
			name: "container query errors -> depot owns (fail-safe, never strands buffers)",
			seed: func(t *testing.T, s *DaemonServer, db *gorm.DB, playerID int) {
				// An armed reconciler EXISTS, but the containers table is unreadable: the
				// armed-check must fail toward depot-owns rather than strand the buffer.
				seedReconcilerContainer(t, db, playerID, "RUNNING", `{}`)
				require.NoError(t, db.Migrator().DropTable(&persistence.ContainerModel{}))
			},
			standsDown: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, db, playerID := newRecoveryTestServer(t)
			const shipSymbol = "WH-BUF-AUTH"
			const warehouseWaypoint = "X1-J58-WH"
			opRepo, operationID := nonIdleDepotWarehouseFixture(
				t, s, db, playerID, shipSymbol, warehouseWaypoint, []string{staleGood}, minerRows)

			if tc.seed != nil {
				tc.seed(t, s, db, playerID)
			}

			require.NoError(t, s.launchDepotWarehouse(context.Background(), shipSymbol, warehouseWaypoint, nil, playerID))

			reloaded, err := opRepo.FindByID(context.Background(), operationID)
			require.NoError(t, err)
			require.NotNil(t, reloaded)

			if tc.standsDown {
				require.True(t, reloaded.SupportsGood(staleGood),
					"armed reconciler owns buffers: the depot must NOT overwrite supported_goods — the stale good must survive")
				require.False(t, reloaded.SupportsGood(freshGood),
					"armed reconciler owns buffers: the depot buffer re-solve must NOT run — the re-solved good must be absent")
			} else {
				require.True(t, reloaded.SupportsGood(freshGood),
					"depot owns buffers: the re-solve runs and the fresh high-value good reaches the whitelist")
				require.False(t, reloaded.SupportsGood(staleGood),
					"depot owns buffers: the re-solve REPLACES the stale whitelist")
			}
		})
	}
}
