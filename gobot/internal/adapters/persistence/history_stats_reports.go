package persistence

import (
	"context"
	"fmt"
	"time"
)

func (r *HistoryRepository) GoodsStats(ctx context.Context, good string, eraID *int) ([]GoodsEraStat, error) {
	playerIDs, playerToEra, err := r.eraPlayerIDs(ctx, eraID)
	if err != nil {
		return nil, err
	}
	names, err := r.eraNames(ctx)
	if err != nil {
		return nil, err
	}
	if len(playerIDs) == 0 {
		return nil, nil
	}

	var rows []MarketPriceHistoryModel
	if err := r.db.WithContext(ctx).
		Where("good_symbol = ? AND player_id IN ?", good, playerIDs).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to query market price history: %w", err)
	}

	buckets := map[int][]MarketPriceHistoryModel{}
	for _, row := range rows {
		era := playerToEra[row.PlayerID]
		buckets[era] = append(buckets[era], row)
	}

	eraIDs := sortedMapKeys(buckets)
	out := make([]GoodsEraStat, 0, len(eraIDs))
	for _, e := range eraIDs {
		out = append(out, goodsEraStat(e, names[e], buckets[e]))
	}
	return out, nil
}

func goodsEraStat(eraID int, eraName string, bucket []MarketPriceHistoryModel) GoodsEraStat {
	markets := map[string]bool{}
	buys := make([]float64, 0, len(bucket))
	sells := make([]float64, 0, len(bucket))
	supplyDist := map[string]int{}
	totalVolume := 0
	for _, row := range bucket {
		markets[row.WaypointSymbol] = true
		buys = append(buys, float64(row.PurchasePrice))
		sells = append(sells, float64(row.SellPrice))
		if row.Supply != nil {
			supplyDist[*row.Supply]++
		}
		totalVolume += row.TradeVolume
	}
	return GoodsEraStat{
		EraID:               eraID,
		EraName:             eraName,
		MarketCount:         len(markets),
		SampleCount:         len(bucket),
		MedianBuyPrice:      median(buys),
		MedianSellPrice:     median(sells),
		SupplyDistribution:  supplyDist,
		AvgTradeVolume:      avgInt(totalVolume, len(bucket)),
		SellPriceVolatility: stddev(sells),
	}
}

func (r *HistoryRepository) PnL(ctx context.Context, eraID *int, byOperation bool) (*PnLReport, error) {
	playerIDs, _, err := r.eraPlayerIDs(ctx, eraID)
	if err != nil {
		return nil, err
	}
	if len(playerIDs) == 0 {
		return &PnLReport{}, nil
	}

	var rows []TransactionModel
	if err := r.db.WithContext(ctx).Where("player_id IN ?", playerIDs).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to query transactions: %w", err)
	}

	breakdown := map[string]*PnLBucket{}
	daily := map[string]int{}
	total := 0
	for _, row := range rows {
		key := pnlBucketKey(row, byOperation)
		b, ok := breakdown[key]
		if !ok {
			b = &PnLBucket{Key: key}
			breakdown[key] = b
		}
		b.Net += row.Amount
		b.Count++
		total += row.Amount

		if eraID != nil {
			day := row.Timestamp.Format("2006-01-02")
			daily[day] += row.Amount
		}
	}

	report := &PnLReport{NetTotal: total}
	for _, k := range sortedMapKeys(breakdown) {
		report.Breakdown = append(report.Breakdown, *breakdown[k])
	}
	if eraID != nil {
		for _, d := range sortedMapKeys(daily) {
			report.Daily = append(report.Daily, PnLDailyPoint{Date: d, Net: daily[d]})
		}
	}

	return report, nil
}

func pnlBucketKey(row TransactionModel, byOperation bool) string {
	if !byOperation {
		return row.Category
	}
	if row.OperationType == "" {
		return "UNSPECIFIED"
	}
	return row.OperationType
}

func (r *HistoryRepository) ManufacturingStats(ctx context.Context, eraID *int, good *string) ([]ManufacturingGoodStat, error) {
	playerIDs, _, err := r.eraPlayerIDs(ctx, eraID)
	if err != nil {
		return nil, err
	}
	if len(playerIDs) == 0 {
		return nil, nil
	}

	q := r.db.WithContext(ctx).Where("player_id IN ?", playerIDs)
	if good != nil {
		q = q.Where("product_good = ?", *good)
	}
	var rows []ManufacturingPipelineModel
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to query manufacturing pipelines: %w", err)
	}

	buckets := map[string][]ManufacturingPipelineModel{}
	for _, row := range rows {
		buckets[row.ProductGood] = append(buckets[row.ProductGood], row)
	}

	goods := sortedMapKeys(buckets)
	out := make([]ManufacturingGoodStat, 0, len(goods))
	for _, g := range goods {
		out = append(out, manufacturingGoodStat(g, buckets[g]))
	}
	return out, nil
}

func manufacturingGoodStat(good string, bucket []ManufacturingPipelineModel) ManufacturingGoodStat {
	completed := 0
	costs := make([]float64, 0, len(bucket))
	profits := make([]float64, 0, len(bucket))
	for _, row := range bucket {
		if row.Status == "COMPLETED" {
			completed++
		}
		costs = append(costs, float64(row.TotalCost))
		profits = append(profits, float64(row.NetProfit))
	}
	return ManufacturingGoodStat{
		Good:         good,
		Count:        len(bucket),
		SuccessRate:  avgInt(completed, len(bucket)),
		AvgCost:      mean(costs),
		AvgNetProfit: mean(profits),
	}
}

func (r *HistoryRepository) EventStats(ctx context.Context, eraID *int, eventType *string) (*EventReport, error) {
	playerIDs, _, err := r.eraPlayerIDs(ctx, eraID)
	if err != nil {
		return nil, err
	}
	if len(playerIDs) == 0 {
		return &EventReport{}, nil
	}

	q := r.db.WithContext(ctx).Where("player_id IN ?", playerIDs)
	if eventType != nil {
		q = q.Where("type = ?", *eventType)
	}
	var rows []CaptainEventModel
	if err := q.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to query captain events: %w", err)
	}

	byType := map[string]int{}
	weekly := map[string]int{}
	for _, row := range rows {
		byType[row.Type]++
		weekStart := row.CreatedAt.Truncate(7 * 24 * time.Hour)
		weekly[weekStart.Format("2006-01-02")]++
	}

	report := &EventReport{Total: len(rows)}
	for _, t := range sortedMapKeys(byType) {
		report.ByType = append(report.ByType, EventTypeStat{Type: t, Count: byType[t]})
	}
	for _, w := range sortedMapKeys(weekly) {
		report.Weekly = append(report.Weekly, EventWeeklyPoint{WeekStart: w, Count: weekly[w]})
	}

	return report, nil
}
