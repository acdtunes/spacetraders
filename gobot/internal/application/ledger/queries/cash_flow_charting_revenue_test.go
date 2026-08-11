package queries_test

// The cash-flow statement seeds its bucket map from ledger.AllCategories() and then indexes it by
// each row's own category. A category the domain can produce but that list omits indexes a nil
// bucket, so the report does not mis-count the new revenue — it panics on the first row of it.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func TestCashFlowReportsChartingRewardsAsInflow(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	playerRow := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&playerRow).Error)
	playerID := shared.MustNewPlayerID(playerRow.ID)

	repo := persistence.NewGormTransactionRepository(db)
	recorded := time.Now().UTC()
	transaction, err := ledger.NewTransaction(playerID, recorded, ledger.TransactionTypeChart,
		10000, 1000000, 1010000, "Charting reward", nil, "", "", "manual")
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), transaction))

	response, err := queries.NewGetCashFlowHandler(repo).Handle(context.Background(), &queries.GetCashFlowQuery{
		PlayerID:  playerRow.ID,
		StartDate: recorded.Add(-time.Hour),
		EndDate:   recorded.Add(time.Hour),
	})
	require.NoError(t, err)

	statement := response.(*queries.GetCashFlowResponse)
	var charting *queries.CategoryCashFlow
	for _, category := range statement.Categories {
		if category.Category == string(ledger.CategoryChartingRevenue) {
			charting = category
		}
	}
	require.NotNil(t, charting, "charting revenue must have its own cash-flow bucket")
	require.Equal(t, 10000, charting.TotalInflow)
	require.Equal(t, 10000, charting.NetFlow)
	require.Equal(t, 1, charting.Transactions)
}
