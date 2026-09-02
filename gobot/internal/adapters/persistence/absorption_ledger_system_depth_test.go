package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func TestAbsorptionLedger_SystemDepths_JoinsPricesWithOccupancy(t *testing.T) {
	var _ mvt.SystemDepthReader = (*persistence.AbsorptionLedgerGORM)(nil)
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "DEPTH-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	now := time.Now().UTC()
	exp, imp := "EXPORT", "IMPORT"
	rows := []persistence.MarketData{
		{PlayerID: player.ID, WaypointSymbol: "X1-A-1", GoodSymbol: "IRON", PurchasePrice: 100, SellPrice: 90, TradeVolume: 60, TradeType: &exp, LastUpdated: now},
		{PlayerID: player.ID, WaypointSymbol: "X1-A-2", GoodSymbol: "IRON", PurchasePrice: 160, SellPrice: 150, TradeVolume: 40, TradeType: &imp, LastUpdated: now},
		{PlayerID: player.ID, WaypointSymbol: "X1-B-1", GoodSymbol: "IRON", PurchasePrice: 100, SellPrice: 90, TradeVolume: 10, TradeType: &exp, LastUpdated: now},
		{PlayerID: player.ID, WaypointSymbol: "X1-ZZ-1", GoodSymbol: "IRON", PurchasePrice: 1, SellPrice: 1, TradeVolume: 1, TradeType: &exp, LastUpdated: now},
	}
	for i := range rows {
		require.NoError(t, db.Create(&rows[i]).Error)
	}
	// One PLANNED buy reservation of 10 units on X1-A-1 IRON, unexpired.
	require.NoError(t, db.Create(&persistence.MarketAbsorptionLedgerModel{
		ID: uuid.NewString(), PlayerID: player.ID, Waypoint: "X1-A-1", Good: "IRON", Side: "buy", Units: 10,
		State: "PLANNED", ContainerID: "ctr-1", Engine: "tour", ExpiresAt: now.Add(time.Hour), CreatedAt: now,
	}).Error)

	ledger := persistence.NewAbsorptionLedger(db, "", persistence.AbsorptionLedgerConfig{}, nil)
	got, err := ledger.SystemDepths(context.Background(), player.ID, []string{"X1-A", "X1-B", "X1-NOPE"})
	require.NoError(t, err)
	require.Len(t, got["X1-A"], 2)
	require.Len(t, got["X1-B"], 1)
	require.Nil(t, got["X1-NOPE"])
	require.NotContains(t, got, "X1-ZZ")
	var src mvt.LaneDepth
	for _, l := range got["X1-A"] {
		if l.Listing.Waypoint == "X1-A-1" {
			src = l
		}
	}
	require.Equal(t, "IRON", src.Listing.Good)
	require.Equal(t, 100, src.Listing.Ask)
	require.Equal(t, 90, src.Listing.Bid)
	require.Equal(t, "EXPORT", src.Listing.TradeType)
	require.Equal(t, 60, src.Listing.Volume)
	require.Equal(t, 10, src.BuyPlanned)
	require.Equal(t, 0, src.SellPlanned)
}
