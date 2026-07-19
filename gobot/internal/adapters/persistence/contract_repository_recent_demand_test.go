package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// RecentContractDemand feeds the contract-hub placement coordinator's demand EWMA.
// The EWMA folds contracts OLDEST→NEWEST, so the read must return that order; it must dedupe
// a good repeated within one contract (recurrence is measured ACROSS contracts, not within),
// and it must skip a single corrupt deliveries blob rather than blind the whole signal (the
// coordinator is fail-safe). This pins all three as observable output of the public method.
func TestRecentContractDemand_OldestToNewest_DedupesGoods_SkipsCorruptRow(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	player := persistence.PlayerModel{AgentSymbol: "HUB-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)

	// Inserted out of chronological order to prove the method sorts by last_updated, not by
	// insertion / primary key. last_updated is an ISO-8601 string → lexicographic == chronological.
	newest := persistence.ContractModel{
		ID: "c-newest", PlayerID: player.ID, FactionSymbol: "COSMIC", Type: "PROCUREMENT",
		Accepted: true, Fulfilled: true, DeadlineToAccept: "2026-07-12T00:00:00Z", Deadline: "2026-07-20T00:00:00Z",
		PaymentOnAccepted: 50, PaymentOnFulfilled: 300,
		DeliveriesJSON: `[{"TradeSymbol":"DRUGS","DestinationSymbol":"X1-AA-1","UnitsRequired":40,"UnitsFulfilled":40},` +
			`{"TradeSymbol":"FUEL","DestinationSymbol":"X1-AA-1","UnitsRequired":10,"UnitsFulfilled":10}]`,
		LastUpdated: "2026-07-12T00:00:00Z",
	}
	corrupt := persistence.ContractModel{
		ID: "c-corrupt", PlayerID: player.ID, FactionSymbol: "COSMIC", Type: "PROCUREMENT",
		Accepted: true, Fulfilled: false, DeadlineToAccept: "2026-07-11T00:00:00Z", Deadline: "2026-07-20T00:00:00Z",
		PaymentOnAccepted: 10, PaymentOnFulfilled: 999,
		DeliveriesJSON: `{not valid json`,
		LastUpdated:    "2026-07-11T00:00:00Z",
	}
	oldest := persistence.ContractModel{
		ID: "c-oldest", PlayerID: player.ID, FactionSymbol: "COSMIC", Type: "PROCUREMENT",
		Accepted: true, Fulfilled: true, DeadlineToAccept: "2026-07-10T00:00:00Z", Deadline: "2026-07-20T00:00:00Z",
		PaymentOnAccepted: 20, PaymentOnFulfilled: 100,
		// FUEL twice in ONE contract → must collapse to a single good (recurrence is cross-contract).
		DeliveriesJSON: `[{"TradeSymbol":"FUEL","DestinationSymbol":"X1-AA-2","UnitsRequired":5,"UnitsFulfilled":5},` +
			`{"TradeSymbol":"FUEL","DestinationSymbol":"X1-AA-3","UnitsRequired":5,"UnitsFulfilled":5}]`,
		LastUpdated: "2026-07-10T00:00:00Z",
	}
	require.NoError(t, db.Create(&newest).Error)
	require.NoError(t, db.Create(&corrupt).Error)
	require.NoError(t, db.Create(&oldest).Error)

	repo := persistence.NewGormContractRepository(db)
	rows, err := repo.RecentContractDemand(context.Background(), player.ID, 0)
	require.NoError(t, err)

	// The corrupt row is skipped; the two readable rows come back oldest→newest.
	require.Len(t, rows, 2, "corrupt deliveries blob must be skipped, not fatal")
	require.Equal(t, []string{"FUEL"}, rows[0].Goods, "oldest contract first; its duplicate FUEL collapses to one good")
	require.Equal(t, 100, rows[0].PaymentOnFulfilled)
	require.Equal(t, []string{"DRUGS", "FUEL"}, rows[1].Goods, "newest contract last; per-delivery order preserved")
	require.Equal(t, 300, rows[1].PaymentOnFulfilled)
}
