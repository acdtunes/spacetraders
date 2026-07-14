package grpc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// sp-wxf2: the depot receipt path (depotWarehouseTargetUnits -> DemandMiner.Mine ->
// PlanReceiptCaps) used to REUSE the SOURCE-side stocker ranking, which pre-culls candidates by
// buy-leg SAVINGS and truncates to TopN BEFORE the reward knapsack ever runs. A high
// contract-reward good with a small source spread (a MEDICINE/CLOTHING-like good — low savings,
// so ranked last and TopN-culled) was therefore dropped before its reward was ever weighed, while
// a low-reward/high-savings filler survived. The fix ranks the DEPOT candidate selection by
// contract-reward value, so the high-reward good survives the cull and reaches the caps.
//
// This test drives the REAL miner + REAL receipt knapsack over an in-memory demand/market
// fixture (no DB), asserting the observable outcome: the destination warehouse's receipt caps.

// rewardRankDemandSource is a fake persistence.contractDemandSource (its unexported method set)
// returning a fixed contract-demand set. Passed to NewDemandMinerWithSources so the real miner
// runs over it with no database.
type rewardRankDemandSource struct {
	rows []persistence.ContractGoodDemand
}

func (f rewardRankDemandSource) ContractGoodDemand(_ context.Context, _ *int, _ *string) ([]persistence.ContractGoodDemand, error) {
	return f.rows, nil
}

func (f rewardRankDemandSource) CurrentEraID(_ context.Context, _ int) (*int, error) { return nil, nil }

// rewardRankMarkets is a fake persistence.marketAskFinder. Every good gets a CROSS-system source
// (so the receipt residual leg is equal across goods — contract reward and recurrence are the
// only differentiators), and an optional per-good HOME ask drives the source-side buy-leg savings
// the buggy path ranked by. A good absent from homeByGood has no home ask, so its projected
// savings is 0 and it is stock-INELIGIBLE — ranked dead last by the old savings cull.
type rewardRankMarkets struct {
	sourceAsk  int
	homeByGood map[string]int
}

func (f rewardRankMarkets) FindCheapestMarketsSellingAllSystems(_ context.Context, good string, _ int, _ int) ([]market.CheapestMarketResult, error) {
	return []market.CheapestMarketResult{{WaypointSymbol: "X9-FAR-S1", TradeSymbol: good, SellPrice: f.sourceAsk}}, nil
}

func (f rewardRankMarkets) FindCheapestMarketSelling(_ context.Context, good string, _ string, _ int) (*market.CheapestMarketResult, error) {
	if ask, ok := f.homeByGood[good]; ok {
		return &market.CheapestMarketResult{WaypointSymbol: "X1-J58-HOME", TradeSymbol: good, SellPrice: ask}, nil
	}
	return nil, nil
}

// TestDepotReceiptCaps_RanksByContractReward_KeepsHighRewardGoodOverSavingsCull is the sp-wxf2
// acceptance test. EXACTLY DefaultTopN low-reward/high-savings fillers fill the savings-ranked
// TopN window, so the sole high-reward/low-savings good (CLOTHING) lands at position
// DefaultTopN+1 and the source-side savings cull drops it before the reward knapsack. Capacity
// fits every survivor, so a good ABSENT from the caps was culled UPSTREAM of the knapsack — the
// exact defect — not evicted for capacity.
func TestDepotReceiptCaps_RanksByContractReward_KeepsHighRewardGoodOverSavingsCull(t *testing.T) {
	now := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)

	homeByGood := map[string]int{}
	rows := make([]persistence.ContractGoodDemand, 0, persistence.DefaultTopN+1)
	for i := 0; i < persistence.DefaultTopN; i++ {
		good := fmt.Sprintf("FILLER_%02d", i)
		homeByGood[good] = 900 // high home ask >> source ask => large buy-leg savings => ranks first under the savings cull
		rows = append(rows, persistence.ContractGoodDemand{
			Good: good, ContractCount: 2, UnitsRequired: 40, MaxContractUnits: 20,
			RewardPerUnit: 1, // low contract reward
			FirstSeen:     now.Add(-24 * time.Hour), LastSeen: now,
		})
	}
	// CLOTHING: HIGH contract reward, NO home ask => savings 0 => stock-INELIGIBLE => ranked dead
	// last by the savings cull (position DefaultTopN+1) => dropped before the knapsack in the old
	// path. NOT in DefaultColdStartCaps, so its presence can only come from the live reward knapsack.
	rows = append(rows, persistence.ContractGoodDemand{
		Good: "CLOTHING", ContractCount: 2, UnitsRequired: 40, MaxContractUnits: 20,
		RewardPerUnit: 7000, // high contract reward
		FirstSeen:     now.Add(-24 * time.Hour), LastSeen: now,
	})

	miner := persistence.NewDemandMinerWithSources(
		rewardRankDemandSource{rows: rows},
		rewardRankMarkets{sourceAsk: 100, homeByGood: homeByGood},
	)

	// Capacity (1000) fits every TopN survivor (20 goods x 20 units = 400), so any good absent
	// from the caps was culled upstream of the knapsack, not evicted for capacity.
	targets := depotWarehouseTargetUnits(
		context.Background(), miner, 1000, "X1-J58", "X1-J58-WH",
		nil /*coords: fail open to the coarse cross-system residual*/, 2, nil,
	)

	require.Contains(t, targets, "CLOTHING",
		"the high contract-reward good must survive the depot candidate selection and win a receipt-buffer cap; "+
			"the source-side savings cull must no longer drop it before the reward knapsack")
}
