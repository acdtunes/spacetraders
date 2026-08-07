package services

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// MarketLocator finds optimal markets for buying and selling goods.
// It ranks markets by activity and supply levels to guide production decisions.
// For ship-type goods, it also searches shipyards.
type MarketLocator struct {
	marketRepo   market.MarketRepository
	waypointRepo system.WaypointRepository
	playerRepo   player.PlayerRepository
	apiClient    ports.APIClient
	yards        yardSource
}

// NewMarketLocator creates a new market locator service
func NewMarketLocator(
	marketRepo market.MarketRepository,
	waypointRepo system.WaypointRepository,
	playerRepo player.PlayerRepository,
	apiClient ports.APIClient,
) *MarketLocator {
	return &MarketLocator{
		marketRepo:   marketRepo,
		waypointRepo: waypointRepo,
		playerRepo:   playerRepo,
		apiClient:    apiClient,
	}
}

// MarketLocatorResult contains market information for a good
type MarketLocatorResult struct {
	WaypointSymbol string
	Activity       string // WEAK, GROWING, STRONG, RESTRICTED
	Supply         string // SCARCE, LIMITED, MODERATE, HIGH, ABUNDANT
	// Price is the side-appropriate quote (sp-en5h7): the ASK (purchase_price, what WE
	// PAY, the larger) for a market we BUY from, or the BID (sell_price, what the market
	// PAYS us, the smaller) for a market we SELL to.
	Price       int
	TradeVolume int // Maximum units per transaction
}

// FindImportMarket finds a market that wants to buy a good (imports it).
// Returns the market with the highest BID, preferring STRONG activity.
func (l *MarketLocator) FindImportMarket(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	bestMarket, err := l.marketRepo.FindBestMarketBuying(ctx, good, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find import market for %s: %w", good, err)
	}

	if bestMarket == nil {
		return nil, fmt.Errorf("no market found importing %s", good)
	}

	tradeGood, err := l.scannedTradeGood(ctx, bestMarket.WaypointSymbol, good, playerID)
	if err != nil {
		return nil, err
	}

	return &MarketLocatorResult{
		WaypointSymbol: bestMarket.WaypointSymbol,
		Activity:       activityOrEmpty(tradeGood),
		Supply:         bestMarket.Supply,
		Price:          bestMarket.Bid,
		TradeVolume:    tradeGood.TradeVolume(),
	}, nil
}

func (l *MarketLocator) scannedTradeGood(ctx context.Context, waypointSymbol string, good string, playerID int) (*market.TradeGood, error) {
	marketData, err := l.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get market data: %w", err)
	}
	if marketData == nil {
		return nil, fmt.Errorf("no market data found for %s (market may not have been scanned)", waypointSymbol)
	}

	tradeGood := marketData.FindGood(good)
	if tradeGood == nil {
		return nil, fmt.Errorf("good %s not found in market %s", good, waypointSymbol)
	}
	return tradeGood, nil
}

// TradeGoodAt reads ONE known waypoint's listing for ONE good (sp-q9um6).
//
// It is the odd one out in this file and deliberately so: every other locator here SEARCHES a
// system for a market matching a role, and returns whichever waypoint won. This one answers a
// question about a waypoint the CALLER has already resolved — "what does THIS market say about
// THIS good" — which no searching locator can answer, because their whole job is to pick the
// waypoint for you.
//
// That distinction is the reason it exists. A caller holding a resolved factory and wanting its
// IMPORT supply of an input cannot get there through FindImportMarket: that returns the BEST
// importer in the system, which is a different waypoint whenever the resolved factory is not the
// best one — so the answer would silently describe some other market.
//
// It is a thin export of scannedTradeGood rather than new logic, so there is one market read in
// this file and not two that can drift.
func (l *MarketLocator) TradeGoodAt(ctx context.Context, waypointSymbol, good string, playerID int) (*market.TradeGood, error) {
	return l.scannedTradeGood(ctx, waypointSymbol, good, playerID)
}

// FindExportMarket finds a market that SELLS a good to us — a buy SOURCE.
// For actual ship types (not ship components), searches shipyards.
// For regular goods and ship components, returns the cheapest EXPORT or EXCHANGE
// market for the good.
//
// AN IMPORT MARKET IS NEVER A BUY SOURCE. A consumer market lists a purchase_price for the good it
// consumes, so a trade-type-blind "cheapest ask" query returns the consuming factory itself as the
// feed's source whenever no real exporter exists in-system — and the feed is then bought AND
// delivered at that same waypoint, a guaranteed round-trip loss on the ask/bid spread. Only
// EXPORT/EXCHANGE can source a good; EXCHANGE stays eligible as a neutral market. When no such
// market exists the feed is genuinely un-sourceable in-system and the caller skips it.
func (l *MarketLocator) FindExportMarket(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	// Ship components (SHIP_PARTS, SHIP_PLATING) are regular market goods
	if isShipType(good) {
		return l.findShipyardSellingShip(ctx, good, systemSymbol, playerID)
	}

	marketWaypoints, err := l.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find markets for %s: %w", good, err)
	}

	var best *MarketLocatorResult
	for _, waypointSymbol := range marketWaypoints {
		marketData, err := l.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue
		}
		tradeGood := marketData.FindGood(good)
		if tradeGood == nil {
			continue
		}
		// Never source from an IMPORT market (the consumer/factory) — only EXPORT or EXCHANGE can
		// sell a good to us, and buying from the consumer is a same-waypoint round-trip loss.
		if tradeGood.TradeType() == market.TradeTypeImport {
			continue
		}
		price := tradeGood.PurchasePrice()
		if price <= 0 {
			continue // not actually purchasable here
		}
		if best == nil || price < best.Price {
			best = &MarketLocatorResult{
				WaypointSymbol: waypointSymbol,
				Activity:       activityOrEmpty(tradeGood),
				Supply:         supplyOrEmpty(tradeGood),
				Price:          price,
				TradeVolume:    tradeGood.TradeVolume(),
			}
		}
	}

	if best == nil {
		return nil, fmt.Errorf("no export or exchange market found selling %s in %s", good, systemSymbol)
	}
	return best, nil
}

// isModeratePlusSupply reports whether a supply level is MODERATE or better (MODERATE, HIGH or
// ABUNDANT) — the supply-first eligibility floor shared by FindExportMarketBySupplyPriority,
// EligibleSourceMedianAsk and InputSourceEligibility. SCARCE/LIMITED (and an absent/unknown level)
// are excluded. It wraps the exact Order()-vs-LIMITED comparison those call sites used, so the
// shared eligibility test is named in one place without altering the ranking supplyScore arithmetic.
func isModeratePlusSupply(supply string) bool {
	return manufacturing.SupplyLevel(supply).Order()-manufacturing.SupplyLevelLimited.Order() >= 1
}

// FindExportMarketBySupplyPriority finds the best market with acceptable supply level.
// Priority: Supply level (ABUNDANT > HIGH > MODERATE), then Activity (WEAK > GROWING > STRONG).
// SCARCE and LIMITED supply levels are skipped to avoid overpaying.
//
// Activity-based optimization: For EXPORT markets (buying), WEAK activity = lowest prices.
// Data analysis: WEAK + ABUNDANT = avg 43 credits, RESTRICTED + ABUNDANT = 6,863 credits.
//
// This is used for raw material acquisition in manufacturing pipelines.
// Example: LIQUID_NITROGEN at ABUNDANT G52 costs 18-28 credits, but SCARCE C44 costs 650+.
//
// Returns error if no market with MODERATE or better supply exists.
func (l *MarketLocator) FindExportMarketBySupplyPriority(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	// Ship types are handled by shipyards (no supply levels)
	if isShipType(good) {
		return l.findShipyardSellingShip(ctx, good, systemSymbol, playerID)
	}

	marketWaypoints, err := l.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find markets: %w", err)
	}

	type candidateMarket struct {
		waypointSymbol string
		supply         string
		activity       string
		price          int
		tradeVolume    int
		supplyScore    int // ABUNDANT=3, HIGH=2, MODERATE=1
		activityScore  int // WEAK=4, GROWING=3, STRONG=2, RESTRICTED=1
	}
	var candidates []candidateMarket

	for _, waypointSymbol := range marketWaypoints {
		marketData, err := l.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue
		}

		tradeGood := marketData.FindGood(good)
		if tradeGood == nil || tradeGood.TradeType() != market.TradeTypeExport {
			continue
		}

		supply := supplyOrEmpty(tradeGood)

		// Skip SCARCE and LIMITED - only accept MODERATE+
		supplyScore := manufacturing.SupplyLevel(supply).Order() - manufacturing.SupplyLevelLimited.Order()
		if supplyScore < 1 {
			continue
		}

		activity := activityOrEmpty(tradeGood)

		candidates = append(candidates, candidateMarket{
			waypointSymbol: waypointSymbol,
			supply:         supply,
			activity:       activity,
			price:          tradeGood.PurchasePrice(),
			tradeVolume:    tradeGood.TradeVolume(),
			supplyScore:    supplyScore,
			activityScore:  ExportActivityScore(activity),
		})
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no market with MODERATE+ supply for %s (SCARCE/LIMITED markets skipped)", good)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].supplyScore != candidates[j].supplyScore {
			return candidates[i].supplyScore > candidates[j].supplyScore
		}
		if candidates[i].activityScore != candidates[j].activityScore {
			return candidates[i].activityScore > candidates[j].activityScore
		}
		return candidates[i].price < candidates[j].price
	})

	best := candidates[0]
	return &MarketLocatorResult{
		WaypointSymbol: best.waypointSymbol,
		Activity:       best.activity,
		Supply:         best.supply,
		Price:          best.price,
		TradeVolume:    best.tradeVolume,
	}, nil
}

// EligibleSourceMedianAsk returns the median ASK (purchase_price — what WE PAY) across all ELIGIBLE
// (MODERATE+ supply) EXPORT markets for a good in a system, plus how many such sources exist.
//
// It is the POISON-PROOF ceiling baseline. A per-waypoint trailing median drags itself up behind a
// ladder — a laddering source poisons its OWN baseline, so a ceiling built on it chases the ladder
// and never fires. Computed over the SAME eligible source set the supply-first selector picks from,
// this median cannot be poisoned: a source that ladders degrades out of MODERATE+ supply and
// therefore drops out of both the candidate set AND the median.
//
// Eligibility mirrors FindExportMarketBySupplyPriority exactly: EXPORT trade type, supply
// MODERATE or better (SCARCE/LIMITED excluded). count==0 means no eligible source (the
// caller is on the rescue/fallback path and must use a different baseline). Ship types have
// no supply semantics and return count==0.
func (l *MarketLocator) EligibleSourceMedianAsk(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (median int, count int, err error) {
	if isShipType(good) {
		return 0, 0, nil
	}

	marketWaypoints, err := l.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to find markets for %s eligible-median: %w", good, err)
	}

	asks := make([]int, 0, len(marketWaypoints))
	for _, waypointSymbol := range marketWaypoints {
		marketData, err := l.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue
		}
		tradeGood := marketData.FindGood(good)
		if tradeGood == nil || tradeGood.TradeType() != market.TradeTypeExport {
			continue
		}
		// MODERATE+ only — the identical eligibility filter as FindExportMarketBySupplyPriority.
		if !isModeratePlusSupply(supplyOrEmpty(tradeGood)) {
			continue
		}
		price := tradeGood.PurchasePrice()
		if price <= 0 {
			continue
		}
		asks = append(asks, price)
	}

	if len(asks) == 0 {
		return 0, 0, nil
	}
	return medianInt(asks), len(asks), nil
}

// InputSourceEligibility reports whether a good has a healthy (MODERATE+) in-system EXPORT source
// AND whether it has any readable in-system EXPORT source at all. It is the input-poison
// anti-cycle's detector, and the distinction between the two return bools is the whole point:
//
//   - eligible=true: at least one MODERATE+ EXPORT source — the chain can source this input.
//   - eligible=false, hasReadableSource=true: EXPORT source(s) exist and were READ, but every one
//     is SCARCE/LIMITED — POSITIVE evidence the market is depleted and will regenerate. This is the
//     ONLY signal that arms the anti-cycle's recovery pause.
//   - eligible=false, hasReadableSource=false: no EXPORT source for the good was readable in-system
//     — either a cold/partial market cache (a transient read miss) or the good genuinely has no
//     in-system source at all. NEITHER is a depleted-market-that-recovers, so it must NOT arm the
//     recovery pause: a transient miss would idle a healthy chain for hours, and a truly-sourceless
//     input never regenerates (it needs a re-site, not a wait). Both fall through to the selector's
//     ordinary park at production time.
//   - err != nil: the system market LIST read failed — fail toward production (no pause).
//
// Eligibility mirrors FindExportMarketBySupplyPriority / EligibleSourceMedianAsk exactly (EXPORT
// trade type, supply MODERATE or better, priced). Ship types have no supply semantics.
func (l *MarketLocator) InputSourceEligibility(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (eligible bool, hasReadableSource bool, err error) {
	if isShipType(good) {
		// Ship types are shipyard-sourced, not supply-gated — never a depleted-market input.
		return true, true, nil
	}

	marketWaypoints, err := l.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return false, false, fmt.Errorf("failed to find markets for %s input-eligibility: %w", good, err)
	}

	for _, waypointSymbol := range marketWaypoints {
		marketData, err := l.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue // a per-waypoint read miss is NOT counted as a readable source (fail toward production)
		}
		tradeGood := marketData.FindGood(good)
		if tradeGood == nil || tradeGood.TradeType() != market.TradeTypeExport {
			continue
		}
		if tradeGood.PurchasePrice() <= 0 {
			continue // unpriceable listing — not a usable source
		}
		hasReadableSource = true
		// MODERATE+ only — the identical eligibility filter as the sibling locators.
		if isModeratePlusSupply(supplyOrEmpty(tradeGood)) {
			eligible = true
			return eligible, hasReadableSource, nil // one healthy source is enough
		}
	}
	return eligible, hasReadableSource, nil
}

// FindConstructionSource finds a market to BUY a good from for delivery to a
// construction site. It is the construction-scoped source locator used both at
// planning time and by the poll-loop recovery of deferred construction tasks.
//
// Preference order:
//  1. EXPORT market at or above minSupply (cheapest, produced-to-order source) -
//     ranked exactly like FindExportMarketBySupplyPriority.
//  2. FALLBACK: when no EXPORT market clears the floor, an IMPORT or EXCHANGE
//     market holding ABUNDANT/HIGH accumulated stock, which it will sell back at
//     its ask price. A LIMITED export that stalls indefinitely is worse than
//     paying a modest premium at an oversupplied importer.
//
// minSupply is the caller-set EXPORT acceptance floor, on the existing supply-state tolerance
// ladder (Order()). The zero value "" resolves to the MODERATE floor; a lower state lets the caller
// buy down when a pipeline would otherwise defer a material indefinitely.
//
// Returns (nil, nil) when neither exists, so the caller DEFERS the material as a PENDING task that
// recovers when supply regenerates instead of failing the whole pipeline.
func (l *MarketLocator) FindConstructionSource(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
	minSupply manufacturing.SupplyLevel,
) (*MarketLocatorResult, error) {
	// Ship types are sourced from shipyards, which have no supply levels.
	if isShipType(good) {
		return l.findShipyardSellingShip(ctx, good, systemSymbol, playerID)
	}

	floor := minSupply
	if floor == "" {
		floor = manufacturing.SupplyLevelModerate
	}

	marketWaypoints, err := l.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find markets: %w", err)
	}

	exportCandidates, importCandidates := l.collectSourceCandidates(ctx, marketWaypoints, good, playerID, floor)

	// Prefer EXPORT markets when any qualify.
	if len(exportCandidates) > 0 {
		sort.SliceStable(exportCandidates, func(i, j int) bool {
			if exportCandidates[i].supplyScore != exportCandidates[j].supplyScore {
				return exportCandidates[i].supplyScore > exportCandidates[j].supplyScore
			}
			if exportCandidates[i].activityScore != exportCandidates[j].activityScore {
				return exportCandidates[i].activityScore > exportCandidates[j].activityScore
			}
			return exportCandidates[i].price < exportCandidates[j].price
		})
		return exportCandidates[0].result, nil
	}

	// Import/exchange fallback: highest accumulated supply, then cheapest ask.
	if len(importCandidates) > 0 {
		sort.SliceStable(importCandidates, func(i, j int) bool {
			if importCandidates[i].supplyScore != importCandidates[j].supplyScore {
				return importCandidates[i].supplyScore > importCandidates[j].supplyScore
			}
			return importCandidates[i].price < importCandidates[j].price
		})
		return importCandidates[0].result, nil
	}

	// No sourceable market - caller defers the material.
	return nil, nil
}

// sourceCandidate is one market that could supply a construction material, pre-scored so the
// caller only has to sort.
type sourceCandidate struct {
	result        *MarketLocatorResult
	supplyScore   int
	activityScore int
	price         int
}

// collectSourceCandidates scores every market in the system that sells the good, splitting them
// into EXPORT candidates at or above the supply floor and the oversupplied import/exchange
// fallback — only HIGH/ABUNDANT importers hold enough accumulated stock to be a reliable source.
func (l *MarketLocator) collectSourceCandidates(
	ctx context.Context,
	marketWaypoints []string,
	good string,
	playerID int,
	floor manufacturing.SupplyLevel,
) (exportCandidates, importCandidates []sourceCandidate) {
	for _, waypointSymbol := range marketWaypoints {
		marketData, err := l.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue
		}
		tradeGood := marketData.FindGood(good)
		if tradeGood == nil {
			continue
		}

		supply := supplyOrEmpty(tradeGood)
		activity := activityOrEmpty(tradeGood)
		result := &MarketLocatorResult{
			WaypointSymbol: waypointSymbol,
			Activity:       activity,
			Supply:         supply,
			Price:          tradeGood.PurchasePrice(),
			TradeVolume:    tradeGood.TradeVolume(),
		}

		switch tradeGood.TradeType() {
		case market.TradeTypeExport:
			supplyScore := manufacturing.SupplyLevel(supply).Order() - floor.Order() + 1
			if supplyScore < 1 {
				continue
			}
			exportCandidates = append(exportCandidates, sourceCandidate{
				result:        result,
				supplyScore:   supplyScore,
				activityScore: ExportActivityScore(activity),
				price:         tradeGood.PurchasePrice(),
			})
		case market.TradeTypeImport, market.TradeTypeExchange:
			if !isHighOrAbundant(supply) {
				continue
			}
			importCandidates = append(importCandidates, sourceCandidate{
				result:      result,
				supplyScore: manufacturing.SupplyLevel(supply).Order(),
				price:       tradeGood.PurchasePrice(),
			})
		}
	}
	return exportCandidates, importCandidates
}

// FindExportMarketWithGoodSupply finds a market that exports a good with HIGH or ABUNDANT supply.
// This is used for supply-gated acquisitions to ensure we only buy when prices are favorable.
// Returns nil if no market with good supply is available.
//
// Supply levels affect prices:
// - ABUNDANT: -20 to -10% (best prices for buying)
// - HIGH: -10 to 0% (good prices for buying)
// - MODERATE: 0-15% (average prices)
// - LIMITED: +15-30% (above average prices)
// - SCARCE: +30-70% (worst prices - NEVER BUY)
func (l *MarketLocator) FindExportMarketWithGoodSupply(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	// Shipyards don't have supply levels, so they're always available
	if isShipType(good) {
		return l.findShipyardSellingShip(ctx, good, systemSymbol, playerID)
	}

	marketWaypoints, err := l.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find markets in system: %w", err)
	}

	type candidateMarket struct {
		result *MarketLocatorResult
		supply string
		price  int
	}
	var candidates []candidateMarket

	for _, waypointSymbol := range marketWaypoints {
		marketData, err := l.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue
		}

		tradeGood := marketData.FindGood(good)
		if tradeGood == nil {
			continue
		}

		// Only consider EXPORT markets (selling to us)
		if tradeGood.TradeType() != market.TradeTypeExport {
			continue
		}

		supply := supplyOrEmpty(tradeGood)
		if !isHighOrAbundant(supply) {
			continue
		}

		activity := activityOrEmpty(tradeGood)

		candidates = append(candidates, candidateMarket{
			result: &MarketLocatorResult{
				WaypointSymbol: waypointSymbol,
				Activity:       activity,
				Supply:         supply,
				Price:          tradeGood.PurchasePrice(),
				TradeVolume:    tradeGood.TradeVolume(),
			},
			supply: supply,
			price:  tradeGood.PurchasePrice(),
		})
	}

	if len(candidates) == 0 {
		return nil, nil // No market with good supply - not an error, just unavailable
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].supply != candidates[j].supply {
			return candidates[i].supply == supplyAbundant
		}
		return candidates[i].price < candidates[j].price
	})

	return candidates[0].result, nil
}

// FindBestExportMarket finds the best market for selling a good.
// It prefers markets with high activity and abundant supply.
// Ranking: STRONG + ABUNDANT/HIGH > GROWING + MODERATE/HIGH > Any + MODERATE > WEAK/SCARCE
func (l *MarketLocator) FindBestExportMarket(
	ctx context.Context,
	good string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	marketWaypoints, err := l.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find markets in system: %w", err)
	}

	var bestMarket *MarketLocatorResult
	var bestScore int

	for _, waypointSymbol := range marketWaypoints {
		marketData, err := l.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue
		}

		tradeGood := marketData.FindGood(good)
		if tradeGood == nil {
			continue
		}

		activity := activityOrEmpty(tradeGood)
		supply := supplyOrEmpty(tradeGood)

		score := calculateMarketScore(activity, supply)

		if bestMarket == nil || score > bestScore {
			bestScore = score
			bestMarket = &MarketLocatorResult{
				WaypointSymbol: waypointSymbol,
				Activity:       activity,
				Supply:         supply,
				Price:          tradeGood.PurchasePrice(),
				TradeVolume:    tradeGood.TradeVolume(),
			}
		}
	}

	if bestMarket == nil {
		return nil, fmt.Errorf("no market found exporting %s", good)
	}

	return bestMarket, nil
}

// calculateMarketScore assigns a numeric score to a market based on activity and supply.
// Higher scores indicate better markets for selling goods.
// Scoring hierarchy:
// 1. STRONG activity + ABUNDANT/HIGH supply (90-100)
// 2. GROWING activity + MODERATE/HIGH supply (70-80)
// 3. Any activity + MODERATE supply (40-60)
// 4. WEAK activity or SCARCE/LIMITED supply (10-30)
func calculateMarketScore(activity, supply string) int {
	return sellMarketScoringPolicy.Score(activity, supply)
}

// ExportActivityScore returns a score for activity when BUYING from export markets.
// For EXPORT markets (buying), lower activity = lower prices = better for us.
// Data analysis: WEAK + ABUNDANT = avg 43 credits, RESTRICTED + ABUNDANT = 6,863 credits
func ExportActivityScore(activity string) int {
	return market.ActivityLevel(activity).BuyerActivityScore()
}

// ImportActivityScore returns a score for activity when SELLING to import markets.
// For IMPORT markets (selling), higher activity = higher prices = better for us.
// Data analysis: STRONG = avg 7,551 credits, RESTRICTED = 1,480 credits
func ImportActivityScore(activity string) int {
	return market.ActivityLevel(activity).SellerActivityScore()
}

// FindFactoryForProduction finds a waypoint that can produce outputGood
// AND accepts all inputGoods for delivery. This prevents the bug where
// a factory is selected that exports the output but doesn't have a market
// for the required inputs.
//
// Parameters:
//   - outputGood: The good to be produced (factory must EXPORT/SELL this)
//   - inputGoods: Goods that will be delivered (factory must IMPORT/BUY these)
//
// Returns the best factory waypoint that satisfies both conditions.
func (l *MarketLocator) FindFactoryForProduction(
	ctx context.Context,
	outputGood string,
	inputGoods []string,
	systemSymbol string,
	playerID int,
) (*MarketLocatorResult, error) {
	marketWaypoints, err := l.marketRepo.FindAllMarketsInSystem(ctx, systemSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to find markets in system: %w", err)
	}

	var bestFactory *MarketLocatorResult
	var bestScore int

	for _, waypointSymbol := range marketWaypoints {
		marketData, err := l.marketRepo.GetMarketData(ctx, waypointSymbol, playerID)
		if err != nil || marketData == nil {
			continue
		}

		// CRITICAL: Must check trade_type = EXPORT, not just that the good exists!
		// A market can IMPORT a good (consume it) without producing it.
		outputTradeGood := marketData.FindGood(outputGood)
		if outputTradeGood == nil || outputTradeGood.TradeType() != market.TradeTypeExport {
			continue
		}

		// A factory that produces a good should also accept its inputs
		allInputsAccepted := true
		for _, inputGood := range inputGoods {
			inputTradeGood := marketData.FindGood(inputGood)
			if inputTradeGood == nil {
				allInputsAccepted = false
				break
			}
		}

		if !allInputsAccepted {
			continue
		}

		activity := activityOrEmpty(outputTradeGood)
		supply := supplyOrEmpty(outputTradeGood)

		score := calculateMarketScore(activity, supply)

		if bestFactory == nil || score > bestScore {
			bestScore = score
			bestFactory = &MarketLocatorResult{
				WaypointSymbol: waypointSymbol,
				Activity:       activity,
				Supply:         supply,
				Price:          outputTradeGood.PurchasePrice(),
				TradeVolume:    outputTradeGood.TradeVolume(),
			}
		}
	}

	if bestFactory == nil {
		return nil, fmt.Errorf("no factory found that produces %s AND accepts inputs %v", outputGood, inputGoods)
	}

	return bestFactory, nil
}
