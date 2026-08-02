package persistence

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
)

// MarketGoodListing represents one cached market's trade data for a single good,
// including data age so callers can judge staleness before acting on it.
type MarketGoodListing struct {
	WaypointSymbol string
	TradeType      string
	PurchasePrice  int
	SellPrice      int
	Supply         string
	Activity       string
	TradeVolume    int
	LastUpdated    time.Time
}

// marketGoodListingRow is the scan shape both listing finders read. GoodSymbol stays empty for
// the per-good finder, whose SELECT omits the column — the good is the caller's own input.
type marketGoodListingRow struct {
	WaypointSymbol string
	GoodSymbol     string
	TradeType      *string
	PurchasePrice  int
	SellPrice      int
	Supply         *string
	Activity       *string
	TradeVolume    int
	LastUpdated    time.Time
}

func (row marketGoodListingRow) toMarketGoodListing() MarketGoodListing {
	return MarketGoodListing{
		WaypointSymbol: row.WaypointSymbol,
		TradeType:      derefString(row.TradeType),
		PurchasePrice:  row.PurchasePrice,
		SellPrice:      row.SellPrice,
		Supply:         derefString(row.Supply),
		Activity:       derefString(row.Activity),
		TradeVolume:    row.TradeVolume,
		LastUpdated:    row.LastUpdated,
	}
}

func (row marketGoodListingRow) toSystemMarketGoodListing() SystemMarketGoodListing {
	return SystemMarketGoodListing{
		WaypointSymbol: row.WaypointSymbol,
		GoodSymbol:     row.GoodSymbol,
		TradeType:      derefString(row.TradeType),
		PurchasePrice:  row.PurchasePrice,
		SellPrice:      row.SellPrice,
		Supply:         derefString(row.Supply),
		Activity:       derefString(row.Activity),
		TradeVolume:    row.TradeVolume,
		LastUpdated:    row.LastUpdated,
	}
}

// FindMarketsTradingGood returns every cached market known to trade goodSymbol,
// optionally scoped to systemSymbol. Read-only over MarketData; callers sort by
// side (buy/sell) themselves, this finder never hides staleness.
func (r *MarketRepositoryGORM) FindMarketsTradingGood(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) ([]MarketGoodListing, error) {
	query := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, trade_type, purchase_price, sell_price, supply, activity, trade_volume, last_updated").
		Where("player_id = ?", playerID).
		Where("good_symbol = ?", goodSymbol)

	if systemSymbol != "" {
		query = query.Where("waypoint_symbol LIKE ?", systemSymbol+"-%")
	}

	var rows []marketGoodListingRow
	if err := query.Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to find markets trading %s: %w", goodSymbol, err)
	}

	listings := make([]MarketGoodListing, len(rows))
	for i, row := range rows {
		listings[i] = row.toMarketGoodListing()
	}

	return listings, nil
}

// SystemMarketGoodListing is one cached (market, good) row within a system,
// carrying the good symbol so callers can rank cross-market spreads without a
// per-good round trip. Prices are carried straight through from market_data
// (see MarketGoodListing) under the sp-en5h7 convention: purchase_price is the
// ASK (the larger), sell_price the BID (the smaller).
type SystemMarketGoodListing struct {
	WaypointSymbol string
	GoodSymbol     string
	TradeType      string
	PurchasePrice  int // the ASK: what a ship PAYS buying FROM this market (the larger)
	SellPrice      int // the BID: what a ship RECEIVES selling TO this market (the smaller)
	Supply         string
	Activity       string
	TradeVolume    int
	LastUpdated    time.Time
}

// FindAllGoodListingsInSystem returns every cached (market, good) row for a
// system in one read, so the arbitrage scanner can compute cross-market spreads
// from cache without a query per good. Read-only over MarketData; staleness is
// carried per row (LastUpdated) and never hidden by this finder.
func (r *MarketRepositoryGORM) FindAllGoodListingsInSystem(
	ctx context.Context,
	systemSymbol string,
	playerID int,
) ([]SystemMarketGoodListing, error) {
	var rows []marketGoodListingRow
	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol, trade_type, purchase_price, sell_price, supply, activity, trade_volume, last_updated").
		Where("player_id = ?", playerID).
		Where("waypoint_symbol LIKE ?", systemSymbol+"-%").
		Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("failed to find good listings in system %s: %w", systemSymbol, err)
	}

	listings := make([]SystemMarketGoodListing, len(rows))
	for i, row := range rows {
		listings[i] = row.toSystemMarketGoodListing()
	}

	return listings, nil
}

// FindFactoryForGood finds a market that EXPORTS a specific good (i.e., a factory that produces it).
// Only returns markets where trade_type = 'EXPORT', meaning the market produces this good.
// We BUY from a factory, so the price it reports is the ASK — market_data.purchase_price, what
// WE PAY (the larger of the two prices; sp-en5h7).
// Returns nil if no factory exists for this good in the system.
func (r *MarketRepositoryGORM) FindFactoryForGood(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) (*market.FactoryResult, error) {
	var result struct {
		WaypointSymbol string
		GoodSymbol     string
		PurchasePrice  int
		Supply         *string
		Activity       *string
	}

	// Only select markets where trade_type = 'EXPORT' (factories that produce this good)
	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol, purchase_price, supply, activity").
		Where("player_id = ?", playerID).
		Where("waypoint_symbol LIKE ?", systemSymbol+"-%").
		Where("good_symbol = ?", goodSymbol).
		Where("trade_type = ?", "EXPORT").
		Order("purchase_price ASC"). // Prefer cheapest factory (lowest ask)
		Limit(1).
		Scan(&result).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find factory for %s: %w", goodSymbol, err)
	}

	// If no result found, return nil (no factory exists)
	if result.WaypointSymbol == "" {
		return nil, nil
	}

	supply := derefString(result.Supply)
	activity := derefString(result.Activity)

	return &market.FactoryResult{
		WaypointSymbol: result.WaypointSymbol,
		TradeSymbol:    result.GoodSymbol,
		Ask:            result.PurchasePrice,
		Supply:         supply,
		Activity:       activity,
	}, nil
}
