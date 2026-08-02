package persistence

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

const summaryHighlightLimit = 5

func (r *HistoryRepository) Summary(ctx context.Context, eraID *int) (*SummaryReport, error) {
	resolvedEra, err := r.resolveSummaryEra(ctx, eraID)
	if err != nil {
		return nil, err
	}
	if resolvedEra == nil {
		return &SummaryReport{}, nil
	}

	var era EraModel
	if err := r.db.WithContext(ctx).Where("era_id = ?", *resolvedEra).First(&era).Error; err != nil {
		return nil, fmt.Errorf("failed to load era: %w", err)
	}

	report := &SummaryReport{EraID: *resolvedEra, EraName: era.Name}

	eraOverview, err := r.findEraOverview(ctx, *resolvedEra)
	if err != nil {
		return nil, err
	}
	if eraOverview != nil {
		report.DurationDays = eraOverview.DurationDays
		report.FinalCredits = eraOverview.FinalCredits
	}

	pnl, err := r.PnL(ctx, resolvedEra, false)
	if err != nil {
		return nil, err
	}
	report.IncomeMixPct = incomeMixPct(pnl.Breakdown)

	report.TopGoodsByTradingProfit, err = r.topGoodsByTradingProfit(ctx, era.PlayerID, summaryHighlightLimit)
	if err != nil {
		return nil, err
	}

	contractStats, err := r.ContractsStats(ctx, resolvedEra, nil)
	if err != nil {
		return nil, err
	}
	if len(contractStats) > 0 {
		report.ContractCount = contractStats[0].TotalCount
		report.ContractFulfillmentRate = contractStats[0].FulfillmentRate
	}

	thin, fuelMin, fuelMax, err := r.thinGoodsAndFuelRange(ctx, era.PlayerID)
	if err != nil {
		return nil, err
	}
	report.ThinGoods = thin
	if fuelMin >= 0 {
		report.FuelPriceMin = fuelMin
		report.FuelPriceMax = fuelMax
	}

	events, err := r.EventStats(ctx, resolvedEra, nil)
	if err != nil {
		return nil, err
	}
	report.EventHighlights = topEventTypes(events.ByType, summaryHighlightLimit)

	return report, nil
}

func (r *HistoryRepository) resolveSummaryEra(ctx context.Context, eraID *int) (*int, error) {
	if eraID != nil {
		return eraID, nil
	}
	return r.LatestClosedEraID(ctx)
}

func (r *HistoryRepository) findEraOverview(ctx context.Context, eraID int) (*EraOverview, error) {
	overview, err := r.ListEras(ctx)
	if err != nil {
		return nil, err
	}
	for i := range overview {
		if overview[i].EraID == eraID {
			return &overview[i], nil
		}
	}
	return nil, nil
}

// Always non-nil so the report marshals an object, not null.
func incomeMixPct(breakdown []PnLBucket) map[string]float64 {
	mix := map[string]float64{}
	incomeTotal := 0
	for _, b := range breakdown {
		if b.Net > 0 {
			incomeTotal += b.Net
		}
	}
	for _, b := range breakdown {
		if b.Net > 0 && incomeTotal > 0 {
			mix[b.Key] = (float64(b.Net) / float64(incomeTotal)) * 100
		}
	}
	return mix
}

func (r *HistoryRepository) topGoodsByTradingProfit(ctx context.Context, playerID int, limit int) ([]GoodProfit, error) {
	var txRows []TransactionModel
	if err := r.db.WithContext(ctx).
		Where("player_id = ? AND category IN ?", playerID, []string{"TRADING_REVENUE", "TRADING_COSTS"}).
		Find(&txRows).Error; err != nil {
		return nil, fmt.Errorf("failed to query trading transactions: %w", err)
	}

	goodProfit := map[string]int{}
	for _, row := range txRows {
		good, ok := tradedGoodSymbol(row)
		if !ok {
			continue
		}
		goodProfit[good] += row.Amount
	}

	goods := make([]string, 0, len(goodProfit))
	for g := range goodProfit {
		goods = append(goods, g)
	}
	sort.Slice(goods, func(i, j int) bool {
		return goodProfit[goods[i]] > goodProfit[goods[j]]
	})
	if len(goods) > limit {
		goods = goods[:limit]
	}

	var out []GoodProfit
	for _, g := range goods {
		out = append(out, GoodProfit{Good: g, NetProfit: goodProfit[g]})
	}
	return out, nil
}

func tradedGoodSymbol(row TransactionModel) (string, bool) {
	if row.Metadata == "" {
		return "", false
	}
	var meta map[string]interface{}
	if err := json.Unmarshal([]byte(row.Metadata), &meta); err != nil {
		return "", false
	}
	good, ok := meta["good_symbol"].(string)
	if !ok || good == "" {
		return "", false
	}
	return good, true
}

// fuelMin is -1 when the era saw no FUEL samples.
func (r *HistoryRepository) thinGoodsAndFuelRange(ctx context.Context, playerID int) ([]string, int, int, error) {
	var mphRows []MarketPriceHistoryModel
	if err := r.db.WithContext(ctx).Where("player_id = ?", playerID).Find(&mphRows).Error; err != nil {
		return nil, 0, 0, fmt.Errorf("failed to query market price history: %w", err)
	}

	goodSamples := map[string][]MarketPriceHistoryModel{}
	for _, row := range mphRows {
		goodSamples[row.GoodSymbol] = append(goodSamples[row.GoodSymbol], row)
	}

	var thin []string
	fuelMin, fuelMax := -1, -1
	for good, samples := range goodSamples {
		scarce := 0
		volume := 0
		for _, s := range samples {
			if s.Supply != nil && (*s.Supply == "SCARCE" || *s.Supply == "LIMITED") {
				scarce++
			}
			volume += s.TradeVolume
			if good == "FUEL" {
				if fuelMin == -1 || s.SellPrice < fuelMin {
					fuelMin = s.SellPrice
				}
				if s.SellPrice > fuelMax {
					fuelMax = s.SellPrice
				}
			}
		}
		if isThinGood(scarce, volume, len(samples)) {
			thin = append(thin, good)
		}
	}
	sort.Strings(thin)
	return thin, fuelMin, fuelMax, nil
}

func isThinGood(scarceSamples, totalVolume, sampleCount int) bool {
	if sampleCount == 0 {
		return false
	}
	mostlyScarce := float64(scarceSamples)/float64(sampleCount) >= 0.5
	return mostlyScarce && avgInt(totalVolume, sampleCount) < 20
}

func topEventTypes(byType []EventTypeStat, limit int) []EventTypeStat {
	sort.Slice(byType, func(i, j int) bool {
		return byType[i].Count > byType[j].Count
	})
	if len(byType) > limit {
		return byType[:limit]
	}
	return byType
}
