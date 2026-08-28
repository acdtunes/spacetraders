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

// tradedTx is one ledger row as the recency aggregate reads it: whose it is, when it landed,
// what kind of movement it was, and the waypoint its metadata names.
type tradedTx struct {
	playerID shared.PlayerID
	at       time.Time
	txType   ledger.TransactionType
	waypoint string
}

func seedTradedTransactions(t *testing.T, rows ...tradedTx) *persistence.GormTransactionRepository {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	for _, pid := range []shared.PlayerID{player, otherPlayer} {
		require.NoError(t, db.Create(&persistence.PlayerModel{
			ID:          pid.Value(),
			AgentSymbol: fmt.Sprintf("OWN-TRADE-RECENCY-%d", pid.Value()),
			CreatedAt:   time.Now(),
		}).Error)
	}

	for i, row := range rows {
		// metadata is jsonb in production, so the only two shapes a row can carry are valid
		// JSON and none at all — an empty waypoint seeds the former WITHOUT the key.
		metadata := `{"good_symbol":"CLOTHING","units":10}`
		if row.waypoint != "" {
			metadata = fmt.Sprintf(`{"waypoint":%q,"good_symbol":"CLOTHING","units":10}`, row.waypoint)
		}
		require.NoError(t, db.Create(&persistence.TransactionModel{
			ID:              fmt.Sprintf("tx-own-trade-%d", i),
			PlayerID:        row.playerID.Value(),
			Timestamp:       row.at,
			CreatedAt:       row.at,
			TransactionType: string(row.txType),
			Category:        string(ledger.TypeToCategoryMap[row.txType]),
			Amount:          1,
			Metadata:        metadata,
		}).Error)
	}

	return persistence.NewGormTransactionRepository(db)
}

// The aggregate has to answer with the LATEST trade per waypoint, across hulls and across
// both cargo directions — that recency is fleet-wide is the whole premise of the read.
func TestLastTradeByWaypoint_ReportsTheMostRecentCargoMovement(t *testing.T) {
	recent := hoursAgo(0.1)
	repo := seedTradedTransactions(t,
		tradedTx{player, hoursAgo(2), ledger.TransactionTypePurchaseCargo, "X1-DH68-AB8Z"},
		tradedTx{player, recent, ledger.TransactionTypeSellCargo, "X1-DH68-AB8Z"},
		tradedTx{player, hoursAgo(1.5), ledger.TransactionTypePurchaseCargo, "X1-US72-F23D"},
	)

	last, err := repo.LastTradeByWaypoint(ctx(), player, hoursAgo(4))

	require.NoError(t, err)
	require.Len(t, last, 2)
	require.WithinDuration(t, recent, last["X1-DH68-AB8Z"], time.Second)
	require.WithinDuration(t, hoursAgo(1.5), last["X1-US72-F23D"], time.Second)
}

// Three exclusions, each of which would otherwise report ground as worked that no hull
// traded on: another player's rows, non-cargo movements, and rows outside the scan window.
func TestLastTradeByWaypoint_ExcludesWhatCannotHaveMovedOurPrices(t *testing.T) {
	repo := seedTradedTransactions(t,
		tradedTx{otherPlayer, hoursAgo(0.1), ledger.TransactionTypeSellCargo, "X1-FOREIGN-AA11"},
		tradedTx{player, hoursAgo(0.1), ledger.TransactionTypeRefuel, "X1-FUEL-BB22"},
		tradedTx{player, hoursAgo(0.1), ledger.TransactionTypeJump, "X1-GATE-CC33"},
		tradedTx{player, hoursAgo(9), ledger.TransactionTypeSellCargo, "X1-OLD-DD44"},
		tradedTx{player, hoursAgo(0.2), ledger.TransactionTypeSellCargo, "X1-MINE-EE55"},
	)

	last, err := repo.LastTradeByWaypoint(ctx(), player, hoursAgo(4))

	require.NoError(t, err)
	require.Equal(t, []string{"X1-MINE-EE55"}, keysOf(last))
}

// A row with no waypoint cannot date any ground, and must not become a blank-keyed bucket
// that every unstamped candidate would then match.
func TestLastTradeByWaypoint_DropsRowsWithoutAWaypoint(t *testing.T) {
	repo := seedTradedTransactions(t,
		tradedTx{player, hoursAgo(0.1), ledger.TransactionTypeSellCargo, ""},
		tradedTx{player, hoursAgo(0.2), ledger.TransactionTypeSellCargo, "X1-MINE-EE55"},
	)

	last, err := repo.LastTradeByWaypoint(ctx(), player, hoursAgo(4))

	require.NoError(t, err)
	require.Equal(t, []string{"X1-MINE-EE55"}, keysOf(last))
}

// An empty window is a valid answer, not an error: every caller reads an absent waypoint as
// ground nobody has worked.
func TestLastTradeByWaypoint_EmptyWindowIsNotAnError(t *testing.T) {
	repo := seedTradedTransactions(t)

	last, err := repo.LastTradeByWaypoint(ctx(), player, hoursAgo(4))

	require.NoError(t, err)
	require.Empty(t, last)
}

func keysOf(m map[string]time.Time) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
