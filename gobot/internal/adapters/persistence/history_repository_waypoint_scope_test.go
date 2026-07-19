package persistence_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// ContractGoodCountsForDeliveryWaypoint must scope to the EXACT delivery WAYPOINT,
// not the whole system — the demand miner already scopes to the system, so the buffer selector needs
// this finer signal to exclude a good contracted elsewhere in the system but never to THIS hub. The
// shared fixture delivers IRON_ORE to X1-HOME-A1 (c-a), X1-HOME-A2 (c-b), and X1-FOREIGN-B1 (c-c);
// a system scope would count IRON_ORE 3 times, but A1 alone saw it ONCE.
func TestContractGoodCountsForDeliveryWaypoint_ScopesToTheExactWaypoint(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedContractDemandFixture(t, db)

	repo := persistence.NewHistoryRepository(db)

	a1, err := repo.ContractGoodCountsForDeliveryWaypoint(context.Background(), nil, "X1-HOME-A1")
	require.NoError(t, err)
	require.Equal(t, map[string]int{"IRON_ORE": 1, "COPPER_ORE": 1, "GOLD": 1}, a1,
		"X1-HOME-A1's contract goods are exactly those DELIVERED there — IRON_ORE counted ONCE (not the system's 3)")

	a2, err := repo.ContractGoodCountsForDeliveryWaypoint(context.Background(), nil, "X1-HOME-A2")
	require.NoError(t, err)
	require.Equal(t, map[string]int{"IRON_ORE": 1}, a2,
		"a different hub in the SAME system has its OWN membership — only IRON_ORE was delivered to A2")

	none, err := repo.ContractGoodCountsForDeliveryWaypoint(context.Background(), nil, "X1-NOWHERE-Z9")
	require.NoError(t, err)
	require.Empty(t, none, "a waypoint no contract delivers to has no membership (buffers nothing under gate 1)")
}
