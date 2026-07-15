package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// TestReadDerivesCategoryFromTypeIgnoringStoredValue is the R1/sp-bt6r drift
// regression: category is a pure f(type) relabel (TypeToCategoryMap), so a row
// whose stored category disagrees with its type — a rogue writer bypassing
// NewTransaction, or a legacy/out-of-band insert like the briefing_test.go seed
// idiom — must READ BACK with the correct f(type) category, never the divergent
// stored value. The repository re-derives category from transaction_type on
// reconstruction and does not trust (nor even parse) the stored column, so a
// divergent or structurally invalid stored category can never surface through the
// driving read port.
func TestReadDerivesCategoryFromTypeIgnoringStoredValue(t *testing.T) {
	cases := []struct {
		name           string
		txType         string
		storedCategory string // written out-of-band, deliberately disagreeing with f(type)
		want           ledger.Category
	}{
		{"sell_cargo stored as ship_investments", "SELL_CARGO", "SHIP_INVESTMENTS", ledger.CategoryTradingRevenue},
		{"refuel stored as trading_revenue", "REFUEL", "TRADING_REVENUE", ledger.CategoryFuelCosts},
		{"purchase_ship stored as contract_revenue", "PURCHASE_SHIP", "CONTRACT_REVENUE", ledger.CategoryShipInvestments},
		{"contract_fulfilled stored corrupt", "CONTRACT_FULFILLED", "NOT_A_REAL_CATEGORY", ledger.CategoryContractRevenue},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			db, err := database.NewTestConnection()
			require.NoError(t, err)
			require.NoError(t, db.Create(&persistence.PlayerModel{
				ID: 1, AgentSymbol: "AGENT", Token: "tok", CreatedAt: time.Now(),
			}).Error)

			id := ledger.NewTransactionID()
			// Out-of-band write: bypasses NewTransaction, the only way to persist a
			// category that disagrees with type (mirrors briefing_test.go's raw seed).
			require.NoError(t, db.Create(&persistence.TransactionModel{
				ID:              id.String(),
				PlayerID:        1,
				Timestamp:       time.Now(),
				CreatedAt:       time.Now(),
				TransactionType: tc.txType,
				Category:        tc.storedCategory,
				Amount:          100,
				BalanceBefore:   0,
				BalanceAfter:    100,
			}).Error)

			playerID, err := shared.NewPlayerID(1)
			require.NoError(t, err)

			repo := persistence.NewGormTransactionRepository(db)
			got, err := repo.FindByID(context.Background(), id, playerID)
			require.NoError(t, err)
			require.Equal(t, tc.want, got.Category(),
				"read category must be re-derived from type %q, not the stored %q",
				tc.txType, tc.storedCategory)
		})
	}
}
