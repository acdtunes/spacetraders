package persistence_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// jumpTx is one recorded jump as the gate-fee aggregate reads it: whose it is, when it
// landed, the system it departed, and what the gate charged (stored negative, as an expense).
type jumpTx struct {
	playerID shared.PlayerID
	at       time.Time
	origin   string
	fee      int
}

func seedJumps(t *testing.T, rows ...jumpTx) *persistence.GormTransactionRepository {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	for _, pid := range []shared.PlayerID{player, otherPlayer} {
		require.NoError(t, db.Create(&persistence.PlayerModel{
			ID:          pid.Value(),
			AgentSymbol: fmt.Sprintf("GATE-FEE-%d", pid.Value()),
			CreatedAt:   time.Now(),
		}).Error)
	}

	for i, row := range rows {
		metadata := fmt.Sprintf(`{"origin_system":%q,"destination_system":"X1-FA75"}`, row.origin)
		require.NoError(t, db.Create(&persistence.TransactionModel{
			ID:              fmt.Sprintf("tx-jump-%d", i),
			PlayerID:        row.playerID.Value(),
			Timestamp:       row.at,
			CreatedAt:       row.at,
			TransactionType: string(ledger.TransactionTypeJump),
			Category:        string(ledger.TypeToCategoryMap[ledger.TransactionTypeJump]),
			Amount:          -row.fee,
			Metadata:        metadata,
		}).Error)
	}

	return persistence.NewGormTransactionRepository(db)
}

// The measured incident (sp-htzl1.5): a gate's fee CLIMBS with cumulative use — 5k, then
// 100k, then 300k on the same origin within hours — so the mean (135k here) prices the next
// jump at a fraction of what it will cost. Only the latest observation is the price.
func TestPerOriginGateFees_LatestNotMean(t *testing.T) {
	repo := seedJumps(t,
		jumpTx{player, hoursAgo(6), "X1-VT70", 5_000},
		jumpTx{player, hoursAgo(3), "X1-VT70", 100_000},
		jumpTx{player, hoursAgo(1), "X1-VT70", 300_000},
	)

	fees, err := repo.PerOriginGateFees(ctx(), player, hoursAgo(24))

	require.NoError(t, err)
	require.Equal(t, map[string]int64{"X1-VT70": 300_000}, fees)
}

// The window still bounds the scan, so a gate nobody has crossed lately keeps whatever price
// it last showed rather than falling back to the flat charge — but a jump older than `since`
// is not in the table at all.
func TestPerOriginGateFees_ExcludesJumpsOlderThanTheWindow(t *testing.T) {
	repo := seedJumps(t,
		jumpTx{player, hoursAgo(9), "X1-OLD", 90_000},
		jumpTx{player, hoursAgo(2), "X1-VT70", 40_000},
	)

	fees, err := repo.PerOriginGateFees(ctx(), player, hoursAgo(4))

	require.NoError(t, err)
	require.Equal(t, map[string]int64{"X1-VT70": 40_000}, fees)
}

// Each origin carries its own latest, and another player's jumps are not our prices.
func TestPerOriginGateFees_OriginsAndPlayersAreIndependent(t *testing.T) {
	repo := seedJumps(t,
		jumpTx{player, hoursAgo(2), "X1-VT70", 300_000},
		jumpTx{player, hoursAgo(3), "X1-VT70", 5_000},
		jumpTx{player, hoursAgo(2), "X1-FA75", 7_000},
		jumpTx{player, hoursAgo(1), "X1-FA75", 9_000},
		jumpTx{otherPlayer, hoursAgo(0.5), "X1-VT70", 1},
	)

	fees, err := repo.PerOriginGateFees(ctx(), player, hoursAgo(24))

	require.NoError(t, err)
	require.Equal(t, map[string]int64{"X1-VT70": 300_000, "X1-FA75": 9_000}, fees)
}
