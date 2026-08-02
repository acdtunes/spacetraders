package ship

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	scoutingQuery "github.com/andrescamacho/spacetraders-go/internal/application/scouting/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// MarketScanner handles automatic market scanning and data persistence
type MarketScanner struct {
	apiClient        domainPorts.APIClient
	marketRepo       scoutingQuery.MarketRepository
	playerRepo       player.PlayerRepository
	priceHistoryRepo market.MarketPriceHistoryRepository

	// budget is the fleet's ONE market-scan allowance, and it is never nil —
	// see NewMarketScanner. Every market read in the daemon that costs an API
	// request reaches ScanAndSaveMarket, so this field is the single place the
	// server's unraisable 2.00 req/s ceiling is defended.
	budget *ScanBudget
}

// NewMarketScanner creates a new market scanner service.
//
// The scan budget is constructed here rather than injected, and that is
// deliberate: it means there is no way to build a scanner that does not enforce
// it. A composition root with configured knobs replaces the default via
// SetScanBudget; a caller that forgets still gets a paced scanner, so the
// failure mode of forgetting is "paced at the default rate", never "unmetered".
func NewMarketScanner(
	apiClient domainPorts.APIClient,
	marketRepo scoutingQuery.MarketRepository,
	playerRepo player.PlayerRepository,
	priceHistoryRepo market.MarketPriceHistoryRepository,
) *MarketScanner {
	s := &MarketScanner{
		apiClient:        apiClient,
		marketRepo:       marketRepo,
		playerRepo:       playerRepo,
		priceHistoryRepo: priceHistoryRepo,
		budget:           NewScanBudget(defaultBudgetReqPerSec, defaultValueClampR),
	}
	// The production market repository can count charted markets, which is the
	// budget's denominator; narrow test fakes cannot, and fall back to counting
	// the markets the budget has been asked about.
	if counter, ok := marketRepo.(ChartedMarketCounter); ok {
		s.budget.SetChartedMarketCounter(counter)
	}
	return s
}

// SetScanBudget replaces the default market-scan allowance with a configured
// one. A nil budget is ignored: there is no supported way to run this scanner
// unpaced.
func (s *MarketScanner) SetScanBudget(b *ScanBudget) {
	if b == nil {
		return
	}
	if counter, ok := s.marketRepo.(ChartedMarketCounter); ok {
		b.SetChartedMarketCounter(counter)
	}
	s.budget = b
}

// ScanBudget exposes the allowance for metrics and the operator report.
func (s *MarketScanner) ScanBudget() *ScanBudget { return s.budget }

// ScanAndSaveMarket scans a market at the given waypoint and saves the data to the database.
// This is a non-fatal operation - errors are logged but do not fail the caller's operation.
//
// IT CANNOT TELL A SCAN FROM A DECLINE, and callers that need to must use
// ScanAndSaveMarketWithOutcome instead. A budget decline returns nil here for a
// good reason (see the Admit branch below), but nil then means two different
// things — "the data is written" and "the data is whatever was already there" —
// and a caller that records freshness off this return records a claim it cannot
// support. That is exactly what made 78.5% of the sensing ledger's freshness
// stamps false.
func (s *MarketScanner) ScanAndSaveMarket(ctx context.Context, playerID uint, waypointSymbol string) error {
	_, err := s.ScanAndSaveMarketWithOutcome(ctx, playerID, waypointSymbol)
	return err
}

// ScanAndSaveMarketWithOutcome is ScanAndSaveMarket plus the one bit an error
// return cannot carry: whether market data was actually written.
//
// scanned is true only when this call reached the API and persisted a fresh
// row. It is false when the fleet's market-scan budget declined the request —
// still a SUCCESS for the caller, whose next act is the store read it would have
// done anyway, but NOT an event that any freshness ledger may record. It is
// false on an error too, for the same reason: nothing was written.
//
// The (bool, error) shape mirrors ScanAndSaveMarketFresh, which already answers
// the same question about its own gate.
func (s *MarketScanner) ScanAndSaveMarketWithOutcome(ctx context.Context, playerID uint, waypointSymbol string) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	// Start timing for metrics
	startTime := time.Now()

	// Get existing market data for price history comparison — and, at no extra
	// query, for the scan budget's freshness test and value weight.
	existingMarket, cacheErr := s.marketRepo.GetMarketData(ctx, waypointSymbol, int(playerID))

	// A CACHE WE COULD NOT READ IS NOT EVIDENCE OF FRESHNESS. The repository can
	// return a non-nil market ALONGSIDE an error, so handing that row to the budget
	// would let a failed read look like a just-scanned market and decline the scan —
	// silently serving from a store we just failed to read. Nil it for the budget
	// decision, which makes the market read as never-scanned and therefore always
	// admitted. This is the same rule ScanAndSaveMarketFresh already applies to its
	// own gate; the price-history diff below keeps whatever it was given, unchanged.
	cached := existingMarket
	if cacheErr != nil {
		cached = nil
	}

	// The fleet's ONE market-scan budget. Declining is a SUCCESS: the
	// caller's next act is to read this waypoint from the store, which is exactly
	// what it would have done after a scan, so it proceeds on prices that are
	// current to within the budget's own rotation interval instead of spending a
	// request the ceiling cannot afford. Returning an error here would instead
	// trip the fail-closed money guards, which is why the four call paths that
	// cannot tolerate a cached price stamp WithLiveScanRequired and are never
	// declined.
	//
	// SUCCESS, BUT NOT A SCAN — hence (false, nil) rather than plain nil. Nothing
	// was written, so a caller keeping a freshness ledger must record nothing.
	if s.budget.Admit(ctx, int(playerID), waypointSymbol, cached, scanClassOf(ctx)) == marketscan.ServeFromStore {
		logger.Log("DEBUG", fmt.Sprintf(
			"[MarketScanner] Serving %s from store - market-scan budget", waypointSymbol), map[string]interface{}{
			"action": "scan_served_from_store", "waypoint": waypointSymbol,
		})
		return false, nil
	}

	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to get player token: %v", err), nil)
		recordMarketScanMetric(playerID, waypointSymbol, startTime, err)
		return false, fmt.Errorf("failed to get player token: %w", err)
	}

	systemSymbol := shared.ExtractSystemSymbol(waypointSymbol)
	logger.Log("INFO", fmt.Sprintf("[MarketScanner] Scanning market at %s", waypointSymbol), nil)

	marketData, err := s.apiClient.GetMarket(ctx, systemSymbol, waypointSymbol, token)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to get market data for %s: %v", waypointSymbol, err), nil)
		recordMarketScanMetric(playerID, waypointSymbol, startTime, err)
		return false, fmt.Errorf("failed to get market data for %s: %w", waypointSymbol, err)
	}

	tradeGoods, err := s.convertAPIGoodsToDomain(marketData.TradeGoods, logger)
	if err != nil {
		recordMarketScanMetric(playerID, waypointSymbol, startTime, err)
		return false, err
	}

	err = s.marketRepo.UpsertMarketData(ctx, playerID, waypointSymbol, tradeGoods, time.Now())
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to persist market data for %s: %v", waypointSymbol, err), nil)
		recordMarketScanMetric(playerID, waypointSymbol, startTime, err)
		return false, fmt.Errorf("failed to persist market data: %w", err)
	}

	// Record price changes in history if repository is available
	if s.priceHistoryRepo != nil && existingMarket != nil {
		s.recordPriceChanges(ctx, existingMarket, waypointSymbol, tradeGoods, int(playerID), logger)
	}

	// Fold what we just paid to learn into the budget's value estimate, so this
	// market's next interval reflects the spread it is quoting NOW.
	s.budget.Observe(waypointSymbol, tradeGoods)

	logger.Log("INFO", fmt.Sprintf("[MarketScanner] Successfully scanned and saved market data for %s (%d goods)", waypointSymbol, len(tradeGoods)), nil)

	recordMarketScanMetric(playerID, waypointSymbol, startTime, nil)

	return true, nil
}

// recordMarketScanMetric records a market-scan outcome to the global market
// collector when one is installed. scanErr is nil on a successful scan and the
// underlying error on a failed one; a nil global collector (e.g. unit tests)
// makes it a no-op. Extracted to remove the five identical record-and-branch
// blocks in ScanAndSaveMarket.
func recordMarketScanMetric(playerID uint, waypointSymbol string, startTime time.Time, scanErr error) {
	if collector := metrics.GetGlobalMarketCollector(); collector != nil {
		collector.RecordScan(int(playerID), waypointSymbol, time.Since(startTime), scanErr)
	}
}

// MarketFreshWithin reports whether the cached market m was last updated within maxAge
// of now — the recent-scan freshness test SHARED by the arrival scan
// (RouteExecutor) and the post-trade impact scan (cargo transactions), so both gates
// agree on one definition of "fresh". A nil market (never scanned) or a zero/unknown
// timestamp is NOT fresh, so the caller scans: a trade must never proceed on prices it
// cannot confirm exist. maxAge<=0 ("gate disabled") is the caller's concern, not this
// pure predicate's.
func MarketFreshWithin(m *market.Market, maxAge time.Duration, now time.Time) bool {
	if m == nil {
		return false
	}
	observed := m.LastUpdated()
	if observed.IsZero() {
		return false
	}
	return now.Sub(observed) <= maxAge
}

// ScanAndSaveMarketFresh is the recent-scan freshness gate over
// ScanAndSaveMarket: when maxAge>0 AND the cached market for waypointSymbol was scanned
// within maxAge, it SKIPS the GetMarket API call and reuses the cache (returns
// scanned=false) — killing the redundant re-scan (the measured "same hull re-scanning a
// market 4s apart"). A stale, never-scanned, or unknown-age market falls through to a
// scan so the trade sees fresh-enough prices, and scanned then reports what THAT scan
// did — true if it wrote data, false if the market-scan budget declined it. maxAge<=0
// disables the gate
// entirely (always scans), so the freshness-scout recovery path
// (which stamps no policy, hence maxAge 0) and every other caller are byte-for-byte
// unaffected. Non-fatal like ScanAndSaveMarket: the returned error is the underlying
// scan error, never the gate.
func (s *MarketScanner) ScanAndSaveMarketFresh(ctx context.Context, playerID uint, waypointSymbol string, maxAge time.Duration) (bool, error) {
	if maxAge > 0 {
		// A cache we could not read is not evidence of freshness: the read error is
		// honored before the returned market is looked at, so a failed read falls
		// through to a scan instead of reusing whatever it happened to hand back.
		existing, err := s.marketRepo.GetMarketData(ctx, waypointSymbol, int(playerID))
		if err == nil && MarketFreshWithin(existing, maxAge, time.Now()) {
			common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
				"[MarketScanner] Skipping scan at %s - cached market fresh (< %s old)", waypointSymbol, maxAge), map[string]interface{}{
				"action": "scan_skipped_fresh", "waypoint": waypointSymbol, "max_age_seconds": int(maxAge.Seconds()),
			})
			return false, nil
		}
	}
	// The outcome is the SCANNER's, not this gate's. Falling through to a scan
	// that the market-scan budget then declines writes no data either, so
	// reporting true here would reintroduce the same collapse this gate's own
	// return value exists to avoid. Every caller today discards the
	// bool, so this is inert now; it stops the next one from inheriting the trap.
	return s.ScanAndSaveMarketWithOutcome(ctx, playerID, waypointSymbol)
}

func (s *MarketScanner) convertAPIGoodsToDomain(apiGoods []domainPorts.TradeGoodData, logger common.ContainerLogger) ([]market.TradeGood, error) {
	tradeGoods := make([]market.TradeGood, 0, len(apiGoods))
	for _, apiGood := range apiGoods {
		// Each price keeps its own name across the boundary: the API's
		// purchasePrice is the domain's purchasePrice. They are two adjacent ints,
		// so passing them in the wrong order compiles silently — which is exactly
		// what happened once: every persisted row, every era, held the two prices
		// transposed, and TradeGoodData happens to DECLARE SellPrice first, which
		// made the wrong order look like the natural one. Keep them named.
		good, err := market.NewTradeGood(
			apiGood.Symbol,
			&apiGood.Supply,
			&apiGood.Activity,
			apiGood.PurchasePrice,
			apiGood.SellPrice,
			apiGood.TradeVolume,
			market.TradeType(apiGood.TradeType),
		)
		if err != nil {
			logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to create trade good: %v", err), nil)
			return nil, fmt.Errorf("failed to create trade good: %w", err)
		}
		tradeGoods = append(tradeGoods, *good)
	}
	return tradeGoods, nil
}

// recordPriceChanges compares old and new market data and records changes to price history
func (s *MarketScanner) recordPriceChanges(
	ctx context.Context,
	existingMarket *market.Market,
	waypointSymbol string,
	newGoods []market.TradeGood,
	playerID int,
	logger common.ContainerLogger,
) {
	// Build map of old goods for quick lookup
	oldGoods := make(map[string]market.TradeGood)
	for _, good := range existingMarket.TradeGoods() {
		oldGoods[good.Symbol()] = good
	}

	// Compare each good in new market data
	for _, newGood := range newGoods {
		oldGood, exists := oldGoods[newGood.Symbol()]

		// Record if good is new or any relevant field changed
		if !exists || s.pricesChanged(&oldGood, &newGood) {
			playerIDObj, err := shared.NewPlayerID(playerID)
			if err != nil {
				logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Invalid player ID: %v", err), nil)
				continue
			}

			history, err := market.NewMarketPriceHistory(
				waypointSymbol,
				newGood.Symbol(),
				playerIDObj,
				newGood.PurchasePrice(),
				newGood.SellPrice(),
				newGood.Supply(),
				newGood.Activity(),
				newGood.TradeVolume(),
			)

			if err != nil {
				logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to create price history: %v", err), nil)
				continue
			}

			if err := s.priceHistoryRepo.RecordPriceChange(ctx, history); err != nil {
				logger.Log("ERROR", fmt.Sprintf("[MarketScanner] Failed to record price change: %v", err), nil)
				// Don't fail the scan if price history recording fails
			}
		}
	}
}

// pricesChanged checks if any relevant field changed between old and new trade goods
func (s *MarketScanner) pricesChanged(oldGood, newGood *market.TradeGood) bool {
	if oldGood.PurchasePrice() != newGood.PurchasePrice() {
		return true
	}
	if oldGood.SellPrice() != newGood.SellPrice() {
		return true
	}
	if oldGood.TradeVolume() != newGood.TradeVolume() {
		return true
	}

	// Compare supply (both could be nil)
	oldSupply := oldGood.Supply()
	newSupply := newGood.Supply()
	if (oldSupply == nil) != (newSupply == nil) {
		return true
	}
	if oldSupply != nil && newSupply != nil && *oldSupply != *newSupply {
		return true
	}

	// Compare activity (both could be nil)
	oldActivity := oldGood.Activity()
	newActivity := newGood.Activity()
	if (oldActivity == nil) != (newActivity == nil) {
		return true
	}
	if oldActivity != nil && newActivity != nil && *oldActivity != *newActivity {
		return true
	}

	return false
}

// scanClassOf reads the market read's budget class off the context.
//
// The mapping is one-way on purpose: only a caller that has explicitly stamped
// WithLiveScanRequired — the four pre-commit money-guard verifications — is
// Earning, and everything else is Discretionary. An unrecognised or unstamped
// caller is therefore paced, which is the direction that keeps the budget the
// honest total as new call sites appear.
func scanClassOf(ctx context.Context) marketscan.Class {
	if shared.LiveScanRequiredFromContext(ctx) {
		return marketscan.Earning
	}
	if shared.PairedScanFromContext(ctx) {
		return marketscan.Paired
	}
	return marketscan.Discretionary
}
