package grpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// sp-fo0d: the depot receipt caps ranked INVERSELY to contract reward because the demand miner
// ran with eraID=nil — every RUNTIME call site (depot receipt caps, warehouse, stocker, tour)
// passes nil, on the assumption spelled out in the CLI --era help that "the home-system filter
// already confines to the current universe." It does not. SpaceTraders regenerates the universe
// on each weekly reset and REUSES system symbols, so a nil era aggregates contract demand across
// EVERY past universe that happened to deliver to a homonymous "X1-J58". That resurrected a past
// universe's LOW-reward high-recurrence goods (POLYNUCLEOTIDES, count inflated cross-era) and a
// ghost ELECTRONICS with zero current-universe history, while the CURRENT universe's genuine
// HIGH-reward goods (MEDICINE) were buried or dropped from the receipt caps.
//
// These tests drive the REAL miner + real ContractGoodDemand over a two-universe fixture whose
// two universes REUSE the "X1-J58" symbol, and assert the receipt mine sees ONLY the current
// universe.

// scopeTestMarkets returns one cross-system source for every good asked about, so the miner
// keeps each as a candidate (a good with no source anywhere is legitimately dropped). Every
// source is cross-system, so the receipt residual leg is equal across goods — leaving contract
// reward + recurrence + scope as the only differentiators.
type scopeTestMarkets struct{}

func (scopeTestMarkets) FindCheapestMarketsSellingAllSystems(_ context.Context, good string, _ int, _ int) ([]market.CheapestMarketResult, error) {
	return []market.CheapestMarketResult{{WaypointSymbol: "X9-FAR-S1", TradeSymbol: good, SellPrice: 100}}, nil
}

func (scopeTestMarkets) FindCheapestMarketSelling(_ context.Context, _ string, _ string, _ int) (*market.CheapestMarketResult, error) {
	return nil, nil // home ask unknown — irrelevant to receipt-reward scoping
}

// seedReusedSymbolUniverses seeds a PAST universe (closed era, player 1) and the CURRENT universe
// (open era, player 2) that BOTH deliver to a system whose symbol — "X1-J58" — is reused across
// the weekly reset. The past universe recurrently contracted POLYNUCLEOTIDES (low reward, 3
// contracts) and ELECTRONICS (2 contracts); the current universe contracts MEDICINE (high reward,
// 2 contracts). Payments give per-unit reward POLYNUCLEOTIDES=373, MEDICINE=7000 — the live
// inversion in miniature.
func seedReusedSymbolUniverses(t *testing.T, db *gorm.DB) {
	t.Helper()

	pastRegistered := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	pastClosed := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)
	currentRegistered := time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)

	// eras carry a player_id; the contract rows below reference the players under the FK harness.
	require.NoError(t, db.Create(&persistence.PlayerModel{ID: 1, AgentSymbol: "PAST-AGENT", Token: "tok", CreatedAt: time.Now()}).Error)
	require.NoError(t, db.Create(&persistence.PlayerModel{ID: 2, AgentSymbol: "CURRENT-AGENT", Token: "tok", CreatedAt: time.Now()}).Error)

	require.NoError(t, db.Create(&persistence.EraModel{
		Name: "past", AgentSymbol: "PAST-AGENT", PlayerID: 1,
		RegisteredAt: &pastRegistered, ClosedAt: &pastClosed,
	}).Error)
	require.NoError(t, db.Create(&persistence.EraModel{
		Name: "current", AgentSymbol: "CURRENT-AGENT", PlayerID: 2,
		RegisteredAt: &currentRegistered,
	}).Error)

	specs := []struct {
		id       string
		playerID int
		good     string
		units    int
		payment  int
	}{
		{"past-poly-1", 1, "POLYNUCLEOTIDES", 20, 7460},
		{"past-poly-2", 1, "POLYNUCLEOTIDES", 20, 7460},
		{"past-poly-3", 1, "POLYNUCLEOTIDES", 20, 7460},
		{"past-elec-1", 1, "ELECTRONICS", 20, 8000},
		{"past-elec-2", 1, "ELECTRONICS", 20, 8000},
		{"cur-med-1", 2, "MEDICINE", 30, 210000},
		{"cur-med-2", 2, "MEDICINE", 30, 210000},
	}
	for _, s := range specs {
		deliveries := fmt.Sprintf(`[{"TradeSymbol":%q,"DestinationSymbol":"X1-J58-A1","UnitsRequired":%d,"UnitsFulfilled":0}]`, s.good, s.units)
		require.NoError(t, db.Create(&persistence.ContractModel{
			ID: s.id, PlayerID: s.playerID, FactionSymbol: "COSMIC", Type: "PROCUREMENT",
			Accepted: true, Fulfilled: false,
			DeadlineToAccept:   "2026-06-01T00:00:00Z",
			Deadline:           "2026-06-20T00:00:00Z",
			PaymentOnFulfilled: s.payment,
			DeliveriesJSON:     deliveries,
			LastUpdated:        "2026-06-10T00:00:00Z",
		}).Error)
	}
}

// The receipt MINE for the current player surfaces the current universe's HIGH-reward good with
// its contract reward populated, and does NOT surface the past universe's goods reached via the
// reused "X1-J58" symbol — eraID nil, exactly as the runtime call sites pass it.
func TestDepotReceiptMine_ScopesToCurrentUniverse_PopulatesRewardExcludesReusedSymbol(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedReusedSymbolUniverses(t, db)

	miner := persistence.NewDemandMinerWithSources(persistence.NewHistoryRepository(db), scopeTestMarkets{})

	got, err := miner.Mine(context.Background(), "X1-J58", 2 /*current player*/, nil, persistence.DemandMinerOptions{})
	require.NoError(t, err)

	byGood := map[string]persistence.DemandCandidate{}
	for _, c := range got {
		byGood[c.Good] = c
	}

	med, ok := byGood["MEDICINE"]
	require.True(t, ok, "the current universe's high-reward good must surface as a candidate")
	require.InDelta(t, 7000.0, med.ContractRewardPerUnit, 0.001,
		"MEDICINE's per-unit contract reward must be populated from the current universe's contracts")

	_, hasPoly := byGood["POLYNUCLEOTIDES"]
	require.False(t, hasPoly,
		"a past universe's low-reward good reached via a REUSED system symbol must not pollute the current receipt mine")
	_, hasElectronics := byGood["ELECTRONICS"]
	require.False(t, hasElectronics,
		"a good with zero current-universe contract history must never be a candidate (the sp-fo0d ELECTRONICS ghost)")
}

// The depot receipt CAPS — the warehouse's supported_goods whitelist — are computed from the
// current universe only. Driven end to end through the real miner + real ContractGoodDemand and
// the real receipt knapsack: the current universe's high-reward good wins a buffer cap; the past
// universe's reused-symbol goods never enter the caps. Capacity fits every candidate, so any
// stale good present is a scope leak, not a knapsack eviction.
func TestDepotReceiptCaps_ScopedToCurrentUniverse(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	seedReusedSymbolUniverses(t, db)

	miner := persistence.NewDemandMinerWithSources(persistence.NewHistoryRepository(db), scopeTestMarkets{})

	targets := depotWarehouseTargetUnits(
		context.Background(), miner, 200 /*capacity fits every candidate*/, "X1-J58", "X1-J58-WH",
		nil /*coords: fail-open to the coarse residual*/, bufferGateContext{} /*gates fail open: this test pins scope, not gating*/, 2 /*current player*/, nil,
	)

	require.Contains(t, targets, "MEDICINE", "the current universe's high-reward good must win a buffer cap")
	require.NotContains(t, targets, "POLYNUCLEOTIDES", "a past universe's reused-symbol good must not appear in the receipt caps")
	require.NotContains(t, targets, "ELECTRONICS", "a ghost good with no current-universe history must not appear in the receipt caps")
}
