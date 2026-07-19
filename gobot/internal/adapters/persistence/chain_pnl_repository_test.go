package persistence_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// sp-461l (epic sp-g9td): ReadRealizedPnL's per-good tour realized net now comes from the
// TRANSACTIONS ledger (operation_type="tour", metadata->>'good_symbol'), NOT tour_leg_telemetry
// netting. sp-rd21 proved telemetry netting read ~2x inflated (dropped buy legs), so the factory
// chain kill-switch (and the light-autosizer worker-rate guard) were judging an inflated per-good
// tour net. This pins the SOURCE: telemetry here nets to a very different (higher) figure, but the
// TourNet must reconcile to the ledger, which is treasury-true and consistent with the factory
// flows the same function already reads from transactions.
func TestReadRealizedPnL_TourNetFromTransactionsNotTelemetry(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewGormChainPnLRepository(db)
	ctx := context.Background()

	player := persistence.PlayerModel{AgentSymbol: "CHAIN-PNL", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	pid := player.ID

	base := time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)
	since := base

	tx := func(id, txType, opType, good string, amount int, at time.Time) {
		meta := "{}" // valid JSON with no good_symbol key (refuel/no-good rows)
		if good != "" {
			meta = `{"good_symbol":"` + good + `"}`
		}
		require.NoError(t, db.Create(&persistence.TransactionModel{
			ID: id, PlayerID: pid, Timestamp: at, CreatedAt: at,
			TransactionType: txType, Category: "test", Amount: amount,
			OperationType: opType, Metadata: meta,
		}).Error)
	}
	telLeg := func(good string, isBuy bool, units, price int, at time.Time) {
		require.NoError(t, db.Create(&persistence.TourLegTelemetryModel{
			TourID: "ctr-x", ShipSymbol: "SHIP-1", LegIndex: 0, Waypoint: "W", Good: good,
			IsBuy: isBuy, RealizedUnits: units, RealizedUnitPrice: price,
			PlannedAt: at, RealizedAt: at, PlayerID: pid,
		}).Error)
	}

	// TOUR cargo transactions for IRON: +30k sell, −10k buy ⇒ ledger tour net = +20,000.
	tx("t-iron-sell", "SELL_CARGO", "tour", "IRON", 30_000, base.Add(time.Hour))
	tx("t-iron-buy", "PURCHASE_CARGO", "tour", "IRON", -10_000, base.Add(90*time.Minute))
	// A tour REFUEL row (no good_symbol) must NOT enter tour_net (cargo-only, matching the old CTE).
	tx("t-iron-refuel", "REFUEL", "tour", "", -500, base.Add(2*time.Hour))
	// Out-of-window tour sell — excluded by created_at >= since.
	tx("t-iron-old", "SELL_CARGO", "tour", "IRON", 999_999, base.Add(-time.Hour))

	// TELEMETRY for IRON with a DIFFERENT net: sell 100@900 (+90k), buy 100@100 (−10k) ⇒ +80,000.
	// If TourNet were still telemetry-netted it would read +80,000, not the ledger's +20,000.
	telLeg("IRON", false, 100, 900, base.Add(time.Hour))
	telLeg("IRON", true, 100, 100, base.Add(90*time.Minute))

	// FACTORY flows (unchanged path) for FOOD: −5k input buy, +2k local sell; and a manufacturing
	// REFUEL of −300 into the refuel pool.
	tx("f-food-buy", "PURCHASE_CARGO", "manufacturing", "FOOD", -5_000, base.Add(time.Hour))
	tx("f-food-sell", "SELL_CARGO", "factory_workflow", "FOOD", 2_000, base.Add(time.Hour))
	tx("f-refuel", "REFUEL", "manufacturing", "", -300, base.Add(time.Hour))

	raw, err := repo.ReadRealizedPnL(ctx, pid, since)
	require.NoError(t, err)

	byGood := map[string]int{}
	tourNetByGood := map[string]int{}
	for _, g := range raw.Goods {
		byGood[g.Good] = g.FactoryCost + g.FactorySell
		tourNetByGood[g.Good] = g.TourNet
	}

	require.Equal(t, 20_000, tourNetByGood["IRON"],
		"IRON tour net must be the ledger's +20k (sell 30k − buy 10k), NOT the telemetry net of +80k")
	require.Equal(t, -3_000, byGood["FOOD"], "FOOD factory flows unchanged: −5k buy + 2k sell")
	require.Equal(t, -300, raw.RefuelPool, "manufacturing refuel pool unchanged")
}

// The tour_net is scoped to operation_type="tour": a non-tour cargo trade carrying a good_symbol
// (e.g. an arbitrage sell) must NOT leak into the per-good tour net.
func TestReadRealizedPnL_TourNetExcludesNonTourOps(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewGormChainPnLRepository(db)
	ctx := context.Background()

	player := persistence.PlayerModel{AgentSymbol: "CHAIN-PNL-2", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	pid := player.ID
	base := time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)

	mk := func(id, txType, opType, good string, amount int) {
		require.NoError(t, db.Create(&persistence.TransactionModel{
			ID: id, PlayerID: pid, Timestamp: base.Add(time.Hour), CreatedAt: base.Add(time.Hour),
			TransactionType: txType, Category: "test", Amount: amount,
			OperationType: opType, Metadata: `{"good_symbol":"` + good + `"}`,
		}).Error)
	}
	mk("tour-sell", "SELL_CARGO", "tour", "COPPER", 15_000)
	mk("arb-sell", "SELL_CARGO", "arbitrage", "COPPER", 99_000) // must NOT count toward tour net

	raw, err := repo.ReadRealizedPnL(ctx, pid, base)
	require.NoError(t, err)
	var copper int
	for _, g := range raw.Goods {
		if g.Good == "COPPER" {
			copper = g.TourNet
		}
	}
	require.Equal(t, 15_000, copper, "only the operation_type='tour' sell counts, not the arbitrage sell")
}
