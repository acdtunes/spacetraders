package persistence

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// FindCheapestMarketSelling finds the market with the lowest ASK for a specific good in a
// system — the cheapest place for US to BUY it. The ask is market_data.purchase_price (what
// we PAY, the larger of the two prices); sell_price is the bid the market pays us (sp-en5h7).
// Note: This returns any market with the good - the caller must check supply level at execution time.
// For manufacturing, the COLLECT task checks supply is HIGH/ABUNDANT before buying.
func (r *MarketRepositoryGORM) FindCheapestMarketSelling(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) (*market.CheapestMarketResult, error) {
	var result cheapestMarketRow

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol as trade_symbol, purchase_price, supply").
		Where("player_id = ?", playerID).
		Where("waypoint_symbol LIKE ?", systemSymbol+"-%").
		Where("good_symbol = ?", goodSymbol).
		Order("purchase_price ASC").
		Limit(1).
		Scan(&result).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find cheapest market: %w", err)
	}

	if result.WaypointSymbol == "" {
		return nil, nil
	}

	found := result.toCheapestResult()
	return &found, nil
}

type cheapestMarketRow struct {
	WaypointSymbol string
	TradeSymbol    string
	PurchasePrice  int
	Supply         *string
}

func (row cheapestMarketRow) toCheapestResult() market.CheapestMarketResult {
	return market.CheapestMarketResult{
		WaypointSymbol: row.WaypointSymbol,
		TradeSymbol:    row.TradeSymbol,
		Ask:            row.PurchasePrice,
		Supply:         derefString(row.Supply),
	}
}

// FindCheapestMarketsSellingAllSystems returns up to limit markets selling the
// good across ALL systems with scanned data, cheapest ASK first (purchase_price —
// what we PAY, the larger of the two prices; sp-en5h7). Scouts only scan
// systems the fleet can fly, so "has market data" doubles as the reachability
// filter. Used by the trade engine's demand miner (its local marketAskFinder
// port) to price cross-system SOURCE asks — NOT by contract sourcing, which is
// HOME-system only (RULINGS #14). Deliberately NOT on the MarketRepository
// interface so existing fakes keep compiling.
func (r *MarketRepositoryGORM) FindCheapestMarketsSellingAllSystems(
	ctx context.Context,
	goodSymbol string,
	playerID int,
	limit int,
) ([]market.CheapestMarketResult, error) {
	var rows []cheapestMarketRow

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol as trade_symbol, purchase_price, supply").
		Where("player_id = ?", playerID).
		Where("good_symbol = ?", goodSymbol).
		Order("purchase_price ASC").
		Limit(limit).
		Scan(&rows).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find cheapest markets across systems: %w", err)
	}

	results := make([]market.CheapestMarketResult, 0, len(rows))
	for _, row := range rows {
		results = append(results, row.toCheapestResult())
	}

	return results, nil
}

// FindCheapestMarketSellingWithSupply finds the lowest-ASK market with a specific supply level
// (the ask is purchase_price — what we PAY, the larger price; sp-en5h7).
// This enables supply-priority selection for raw materials: ABUNDANT > HIGH > MODERATE.
// Returns nil if no market exists with the specified supply level.
func (r *MarketRepositoryGORM) FindCheapestMarketSellingWithSupply(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
	supplyLevel string,
) (*market.CheapestMarketResult, error) {
	var result cheapestMarketRow

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol as trade_symbol, purchase_price, supply").
		Where("player_id = ?", playerID).
		Where("waypoint_symbol LIKE ?", systemSymbol+"-%").
		Where("good_symbol = ?", goodSymbol).
		Where("supply = ?", supplyLevel).
		Order("purchase_price ASC").
		Limit(1).
		Scan(&result).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find cheapest market with supply %s: %w", supplyLevel, err)
	}

	if result.WaypointSymbol == "" {
		return nil, nil // No market with this supply level
	}

	found := result.toCheapestResult()
	return &found, nil
}

// FindBestMarketBuying finds the market with the highest BID for a specific good in a system —
// the best market to SELL to (where we get paid the most). The bid is market_data.sell_price
// (what the market PAYS us, the smaller of the two prices); purchase_price is the ask we would
// pay to buy (sp-en5h7).
func (r *MarketRepositoryGORM) FindBestMarketBuying(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) (*market.BestMarketBuyingResult, error) {
	var result struct {
		WaypointSymbol string
		TradeSymbol    string
		SellPrice      int
		Supply         *string
	}

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol as trade_symbol, sell_price, supply").
		Where("player_id = ?", playerID).
		Where("waypoint_symbol LIKE ?", systemSymbol+"-%").
		Where("good_symbol = ?", goodSymbol).
		Order("sell_price DESC").
		Limit(1).
		Scan(&result).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find best market buying: %w", err)
	}

	// If no result found, return nil (not an error)
	if result.WaypointSymbol == "" {
		return nil, nil
	}

	supply := derefString(result.Supply)

	return &market.BestMarketBuyingResult{
		WaypointSymbol: result.WaypointSymbol,
		TradeSymbol:    result.TradeSymbol,
		Bid:            result.SellPrice,
		Supply:         supply,
	}, nil
}

// crossSystemGood is one good's best cross-system market row — the shared result shape both
// global scanners return before mapping it to their typed sink/source result.
type crossSystemGood struct {
	GoodSymbol     string
	WaypointSymbol string
	Price          int
	TradeVolume    int
}

// marketPriceSide is one side of a market_data row — price column, "best" direction, and the
// trade type such a market can never be. Mismatched, the scan silently picks the wrong market.
type marketPriceSide struct {
	priceColumn      string
	priceDir         string
	excludeTradeType string
}

// The two sides are fixed internal column names, never caller input (no injection surface).
// The BID is sell_price — what the market PAYS us; the ASK is purchase_price — what WE PAY.
var (
	bidSide = marketPriceSide{"sell_price", "DESC", string(market.TradeTypeExport)}
	askSide = marketPriceSide{"purchase_price", "ASC", string(market.TradeTypeImport)}
)

// bestAcrossSystems is the ONE parameterized cross-system scan behind BOTH
// BestSinksAcrossSystems (best bid) and BestSourcesAcrossSystems (cheapest ask): DISTINCT ON
// (good_symbol) the best price column per good across ALL SYSTEMS, with the counterpart trade
// type excluded (a sink is never an EXPORT, a source is never an IMPORT), and stale/zero-price
// rows dropped. Discovery shares this one code path rather than a copy-pasted symmetric twin.
func (r *MarketRepositoryGORM) bestAcrossSystems(
	ctx context.Context,
	goods []string,
	playerID int,
	maxAge time.Duration,
	now time.Time,
	side marketPriceSide,
) ([]crossSystemGood, error) {
	if len(goods) == 0 {
		return nil, nil
	}
	var rows []struct {
		GoodSymbol     string
		WaypointSymbol string
		Price          int
		TradeVolume    int
	}
	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select(fmt.Sprintf("DISTINCT ON (good_symbol) good_symbol, waypoint_symbol, %s AS price, trade_volume", side.priceColumn)).
		Where("player_id = ?", playerID).
		Where("good_symbol IN ?", goods).
		Where("last_updated >= ?", now.Add(-maxAge)).
		Where(side.priceColumn+" > 0").
		Where("(trade_type IS NULL OR trade_type <> ?)", side.excludeTradeType).
		Order(fmt.Sprintf("good_symbol, %s %s", side.priceColumn, side.priceDir)).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]crossSystemGood, 0, len(rows))
	for _, row := range rows {
		if row.Price <= 0 {
			continue
		}
		out = append(out, crossSystemGood{
			GoodSymbol:     row.GoodSymbol,
			WaypointSymbol: row.WaypointSymbol,
			Price:          row.Price,
			TradeVolume:    row.TradeVolume,
		})
	}
	return out, nil
}

// BestSinksAcrossSystems returns, for each requested good, the single highest-bid sell
// destination ACROSS ALL SYSTEMS for the player. EXPORT markets are excluded so the result
// mirrors the tour snapshot's sink eligibility — an EXPORT bid is a low sellback the solver
// zeroes, never a real sell destination. A good with no fresh non-EXPORT sink is simply
// absent from the map. It backs the tour coordinator's out-of-horizon lane diagnostic AND the
// long-haul engine's discovery. Read-only and best-effort by contract.
func (r *MarketRepositoryGORM) BestSinksAcrossSystems(
	ctx context.Context,
	goods []string,
	playerID int,
	maxAge time.Duration,
	now time.Time,
) (map[string]market.GlobalSinkResult, error) {
	rows, err := r.bestAcrossSystems(ctx, goods, playerID, maxAge, now, bidSide)
	if err != nil {
		return nil, fmt.Errorf("failed to find best sinks across systems: %w", err)
	}
	out := make(map[string]market.GlobalSinkResult, len(rows))
	for _, row := range rows {
		out[row.GoodSymbol] = market.GlobalSinkResult{
			WaypointSymbol: row.WaypointSymbol,
			SystemSymbol:   shared.ExtractSystemSymbol(row.WaypointSymbol),
			Bid:            row.Price,
			TradeVolume:    row.TradeVolume,
		}
	}
	return out, nil
}

// BestSourcesAcrossSystems returns, for each requested good, the CHEAPEST buy source ACROSS
// ALL SYSTEMS for the player — the source-side mirror of BestSinksAcrossSystems, sharing the
// ONE bestAcrossSystems scan (sp-mepj §2). IMPORT markets are excluded (an importer only BUYS
// a good, never sells one to us), symmetric to the sink scan's EXPORT exclusion. A good with
// no fresh sellable source is simply absent from the map. Paired per-good with the sink scan
// it yields (good, source, sink, ask, bid, depths) spanning any number of gate hops — the
// multi-hop lanes the 1-gate-hop tour/arb horizon structurally cannot see.
func (r *MarketRepositoryGORM) BestSourcesAcrossSystems(
	ctx context.Context,
	goods []string,
	playerID int,
	maxAge time.Duration,
	now time.Time,
) (map[string]market.GlobalSourceResult, error) {
	rows, err := r.bestAcrossSystems(ctx, goods, playerID, maxAge, now, askSide)
	if err != nil {
		return nil, fmt.Errorf("failed to find best sources across systems: %w", err)
	}
	out := make(map[string]market.GlobalSourceResult, len(rows))
	for _, row := range rows {
		out[row.GoodSymbol] = market.GlobalSourceResult{
			WaypointSymbol: row.WaypointSymbol,
			SystemSymbol:   shared.ExtractSystemSymbol(row.WaypointSymbol),
			Ask:            row.Price,
			TradeVolume:    row.TradeVolume,
		}
	}
	return out, nil
}

// FindBestMarketForBuying finds the best market to buy a good from, scoring by trade type, supply, and activity.
// Because this is the BUY side, the price it reports is the ASK — market_data.purchase_price,
// what WE PAY (the larger of the two prices; sp-en5h7).
// Preference order for trade type (best to worst): EXPORT > EXCHANGE > IMPORT > NULL
// Preference order for supply (best to worst): ABUNDANT > HIGH > MODERATE > LIMITED > SCARCE
// Preference order for activity (best to worst): RESTRICTED > WEAK > GROWING > STRONG
// Lower score = better market
func (r *MarketRepositoryGORM) FindBestMarketForBuying(
	ctx context.Context,
	goodSymbol string,
	systemSymbol string,
	playerID int,
) (*market.BestBuyingMarketResult, error) {
	var results []struct {
		WaypointSymbol string
		GoodSymbol     string
		PurchasePrice  int
		Supply         *string
		Activity       *string
		TradeType      *string
	}

	err := r.db.WithContext(ctx).
		Table(marketDataTable).
		Select("waypoint_symbol, good_symbol, purchase_price, supply, activity, trade_type").
		Where("player_id = ?", playerID).
		Where("waypoint_symbol LIKE ?", systemSymbol+"-%").
		Where("good_symbol = ?", goodSymbol).
		Scan(&results).Error

	if err != nil {
		return nil, fmt.Errorf("failed to find markets selling %s: %w", goodSymbol, err)
	}

	if len(results) == 0 {
		return nil, nil // Not available in any market
	}

	var bestResult *market.BestBuyingMarketResult
	bestScore := 100000

	for _, r := range results {
		supply := derefString(r.Supply)
		activity := derefString(r.Activity)
		tradeType := derefString(r.TradeType)

		score := scoreMarketForBuying(tradeType, supply, activity)

		if bestResult == nil || score < bestScore {
			bestScore = score
			bestResult = &market.BestBuyingMarketResult{
				WaypointSymbol: r.WaypointSymbol,
				TradeSymbol:    r.GoodSymbol,
				Ask:            r.PurchasePrice,
				Supply:         supply,
				Activity:       activity,
				TradeType:      market.TradeType(tradeType),
				Score:          score,
			}
		}
	}

	return bestResult, nil
}

// scoreMarketForBuying calculates a score for a market when buying (lower = better)
// Trade Type: EXPORT(0) > EXCHANGE(1) > IMPORT(2) > NULL(3) (weight: 1000)
// Supply: ABUNDANT(0) > HIGH(1) > MODERATE(2) > LIMITED(3) > SCARCE(4) (weight: 10)
// Activity: WEAK(0) > GROWING(1) > STRONG(2) > RESTRICTED(3) (weight: 1, follows BuyerActivityScore)
//
// EXPORT markets are factories that PRODUCE the good - best prices!
// EXCHANGE markets trade goods - moderate prices
// IMPORT markets CONSUME goods - worst prices for buying
//
// Final score = trade_type_score * 1000 + supply_score * 10 + activity_score
func scoreMarketForBuying(tradeType, supply, activity string) int {
	// Trade type is most important: EXPORT markets produce goods = cheap prices
	tradeTypeScore := 3 // Unknown/NULL = worst
	switch tradeType {
	case "EXPORT":
		tradeTypeScore = 0 // Best - factory produces this good
	case "EXCHANGE":
		tradeTypeScore = 1 // OK - trading post
	case "IMPORT":
		tradeTypeScore = 2 // Worst - consumer market (expensive)
	}

	supplyScore := 5 - manufacturing.SupplyLevel(supply).Order()

	activityScore := 4 - market.ActivityLevel(activity).BuyerActivityScore()

	// This ensures EXPORT markets ALWAYS preferred over EXCHANGE over IMPORT
	return tradeTypeScore*1000 + supplyScore*10 + activityScore
}
