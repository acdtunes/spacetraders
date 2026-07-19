package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// sp-difa.1 — the durable per-player ERA-SCOPED contract-graduation flag. These pin the storage
// contract the capacity reconciler + bootstrap read on every boot: a fresh era reads UN-graduated
// (contracts run as the funding floor), the operator's graduate/ungraduate is durable, and one
// player's graduation never bleeds onto another era/player.

// A fresh era row reads UN-graduated (the column default) — so a new era/agent cold-starts with
// contracts running, byte-identical to pre-sp-difa.1.
func TestIsContractGraduated_FreshEraIsUngraduated(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 7}).Error)

	repo := persistence.NewEraRepository(db)
	graduated, err := repo.IsContractGraduated(context.Background(), 7)

	require.NoError(t, err)
	require.False(t, graduated, "a fresh era must read UN-graduated (contracts run as the funding floor)")
}

// An unknown player (no era row) reads UN-graduated — fail-OPEN: a missing row never silently
// suppresses the funding floor.
func TestIsContractGraduated_NoEraRowIsUngraduated(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	repo := persistence.NewEraRepository(db)
	graduated, err := repo.IsContractGraduated(context.Background(), 999)

	require.NoError(t, err)
	require.False(t, graduated)
}

// graduate is durable (persisted) and ungraduate reverses it — the manual decision survives the read
// a restart re-issues.
func TestSetContractGraduated_SetThenClear(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 7}).Error)
	repo := persistence.NewEraRepository(db)
	ctx := context.Background()

	rows, err := repo.SetContractGraduated(ctx, 7, true)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows, "graduate must update the player's era row")

	graduated, err := repo.IsContractGraduated(ctx, 7)
	require.NoError(t, err)
	require.True(t, graduated, "after graduate the player reads GRADUATED")

	rows, err = repo.SetContractGraduated(ctx, 7, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), rows)

	graduated, err = repo.IsContractGraduated(ctx, 7)
	require.NoError(t, err)
	require.False(t, graduated, "ungraduate reverses it")
}

// ERA-SCOPING: graduating one player never affects another player's era, AND a NEW era row for the
// same player (a fresh universe = higher era_id) reads UN-graduated even though the prior era was
// graduated — the read takes the most-recent era, so a fresh era always starts the funding floor.
func TestContractGraduation_IsPerPlayerAndPerEra(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	closed := time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC)
	// Player 7's FIRST (now-closed) era, graduated.
	require.NoError(t, db.Create(&persistence.EraModel{Name: "era-a", AgentSymbol: "A", PlayerID: 7, ClosedAt: &closed, ContractsGraduated: true}).Error)
	// A different player, untouched.
	require.NoError(t, db.Create(&persistence.EraModel{Name: "era-b", AgentSymbol: "B", PlayerID: 8}).Error)
	repo := persistence.NewEraRepository(db)
	ctx := context.Background()

	// Player 8 is unaffected by player 7's graduation.
	grad8, err := repo.IsContractGraduated(ctx, 8)
	require.NoError(t, err)
	require.False(t, grad8, "graduation is per-player: player 8 is untouched")

	// A FRESH era for player 7 (new row, higher era_id) reads UN-graduated despite the closed era
	// being graduated — a new era must start the funding floor.
	require.NoError(t, db.Create(&persistence.EraModel{Name: "era-c", AgentSymbol: "C", PlayerID: 7}).Error)
	grad7, err := repo.IsContractGraduated(ctx, 7)
	require.NoError(t, err)
	require.False(t, grad7, "a fresh era (most recent row) reads UN-graduated even if a prior era was graduated")
}
