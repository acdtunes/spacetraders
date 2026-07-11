package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// Type aliases for convenience
type RebalanceContractFleetCommand = contractTypes.RebalanceContractFleetCommand
type RebalanceContractFleetResponse = contractTypes.RebalanceContractFleetResponse

// RebalanceContractFleetHandler implements fleet rebalancing logic
type RebalanceContractFleetHandler struct {
	mediator            common.Mediator
	shipRepo            navigation.ShipRepository
	graphProvider       system.ISystemGraphProvider
	marketRepo          MarketRepository
	converter           system.IWaypointConverter
	distributionChecker *appContract.DistributionChecker

	// Contract pre-position (sp-1ef0): optional collaborators. When both are wired and
	// the feature is enabled, an idle hull is biased toward the predicted next-source
	// market during a delivery leg. Either being nil disables pre-position (the handler
	// falls back to pure distance-based rebalancing).
	contractRepo   domainContract.ContractRepository
	sourceFinder   SourceMarketFinder
	prepositionCfg SourcePrepositionConfig
}

// MarketRepository defines the interface for market data access needed by rebalancing
type MarketRepository interface {
	FindAllMarketsInSystem(ctx context.Context, systemSymbol string, playerID int) ([]string, error)
}

// SourceMarketFinder resolves a contract good to the in-system market that sells it,
// using scanned market availability (sp-1ef0). It must NOT resurrect the persisted
// purchase-history tracking removed in 71aceda — that biased coverage toward
// frequently-used markets over true availability; the cheapest currently-selling market
// is the honest next-source signal.
type SourceMarketFinder interface {
	FindCheapestMarketSelling(ctx context.Context, goodSymbol, systemSymbol string, playerID int) (*market.CheapestMarketResult, error)
}

// DefaultPrepositionConfidenceThreshold gates the same-good-remaining signal when the
// operator leaves the threshold unset. 0.8 admits the near-certain single-good case
// (confidence 1.0) and rejects the ambiguous multi-good case (confidence 0.5).
const DefaultPrepositionConfidenceThreshold = 0.8

// SourcePrepositionConfig is the live-config seam (RULINGS #5) for contract source
// pre-positioning. Disabled is the escape hatch: default OFF means the feature is ON, so
// an absent key reads as enabled and the default-ON intent survives a recovery from a
// config predating the key.
type SourcePrepositionConfig struct {
	Disabled            bool
	ConfidenceThreshold float64
}

// threshold returns the configured confidence gate, or the default when unset.
func (c SourcePrepositionConfig) threshold() float64 {
	if c.ConfidenceThreshold <= 0 {
		return DefaultPrepositionConfidenceThreshold
	}
	return c.ConfidenceThreshold
}

// NewRebalanceContractFleetHandler creates a new rebalance fleet handler.
//
// contractRepo, sourceFinder and prepositionCfg wire the sp-1ef0 contract pre-position
// hint. Passing a nil contractRepo or sourceFinder disables pre-position and preserves
// the original distance-only behavior.
func NewRebalanceContractFleetHandler(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	graphProvider system.ISystemGraphProvider,
	marketRepo MarketRepository,
	converter system.IWaypointConverter,
	contractRepo domainContract.ContractRepository,
	sourceFinder SourceMarketFinder,
	prepositionCfg SourcePrepositionConfig,
) *RebalanceContractFleetHandler {
	return &RebalanceContractFleetHandler{
		mediator:            mediator,
		shipRepo:            shipRepo,
		graphProvider:       graphProvider,
		marketRepo:          marketRepo,
		converter:           converter,
		distributionChecker: appContract.NewDistributionChecker(graphProvider, converter),
		contractRepo:        contractRepo,
		sourceFinder:        sourceFinder,
		prepositionCfg:      prepositionCfg,
	}
}

// Handle executes the fleet rebalancing command
func (h *RebalanceContractFleetHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RebalanceContractFleetCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	result := &RebalanceContractFleetResponse{
		ShipsMoved:         0,
		TargetMarkets:      []string{},
		AverageDistance:    0,
		DistanceThreshold:  500.0, // Default threshold
		RebalancingSkipped: false,
		SkipReason:         "",
		Assignments:        make(map[string]string),
	}

	targetMarkets, skip := h.discoverSystemMarkets(ctx, cmd, result)
	if skip {
		return result, nil
	}

	ships, skip := h.getCoordinatorShips(ctx, cmd, result)
	if skip {
		return result, nil
	}

	needsRebalancing, skip := h.checkIfRebalancingNeeded(ctx, cmd, ships, targetMarkets, result)
	if skip {
		return result, nil
	}

	if !needsRebalancing {
		return result, nil
	}

	if err := h.assignShipsToMarkets(ctx, cmd, ships, targetMarkets, result); err != nil {
		return nil, err
	}

	if err := h.executeParallelRepositioning(ctx, cmd, ships, result); err != nil {
		return nil, err
	}

	return result, nil
}

func (h *RebalanceContractFleetHandler) discoverSystemMarkets(
	ctx context.Context,
	cmd *RebalanceContractFleetCommand,
	result *RebalanceContractFleetResponse,
) ([]string, bool) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Discovering all markets in system", nil)
	targetMarkets, err := h.marketRepo.FindAllMarketsInSystem(ctx, cmd.SystemSymbol, cmd.PlayerID.Value())
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to discover markets: %v", err), nil)
		return nil, true
	}

	result.TargetMarkets = targetMarkets

	if len(targetMarkets) == 0 {
		logger.Log("WARNING", "No markets found in system - skipping rebalancing", nil)
		result.RebalancingSkipped = true
		result.SkipReason = "No markets available in system"
		return nil, true
	}

	logger.Log("INFO", fmt.Sprintf("Found %d markets in system: %v", len(targetMarkets), targetMarkets), nil)
	return targetMarkets, false
}

func (h *RebalanceContractFleetHandler) getCoordinatorShips(
	ctx context.Context,
	cmd *RebalanceContractFleetCommand,
	result *RebalanceContractFleetResponse,
) ([]*navigation.Ship, bool) {
	logger := common.LoggerFromContext(ctx)

	shipSymbols, err := appContract.FindCoordinatorShips(ctx, cmd.CoordinatorID, cmd.PlayerID.Value(), h.shipRepo)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to find coordinator ships: %v", err), nil)
		return nil, true
	}

	logger.Log("INFO", fmt.Sprintf("Found %d ships in coordinator pool", len(shipSymbols)), nil)

	if len(shipSymbols) == 0 {
		logger.Log("INFO", "No ships in coordinator pool - skipping rebalancing", nil)
		result.RebalancingSkipped = true
		result.SkipReason = "No ships in coordinator pool"
		return nil, true
	}

	// OPTIMIZATION: Fetch all ships from cached list (1 API call instead of N)
	// The ship list is cached for 15 seconds in ShipRepository.FindAllByPlayer
	allShips, err := h.shipRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to load ships: %v", err), nil)
		result.RebalancingSkipped = true
		result.SkipReason = "Failed to load ship data"
		return nil, true
	}

	// Build lookup set for efficient filtering
	symbolSet := make(map[string]bool, len(shipSymbols))
	for _, s := range shipSymbols {
		symbolSet[s] = true
	}

	// Filter to only requested ships
	ships := make([]*navigation.Ship, 0, len(shipSymbols))
	for _, ship := range allShips {
		if symbolSet[ship.ShipSymbol()] {
			ships = append(ships, ship)
		}
	}

	if len(ships) == 0 {
		logger.Log("WARNING", "No ships could be loaded - skipping rebalancing", nil)
		result.RebalancingSkipped = true
		result.SkipReason = "Failed to load ship data"
		return nil, true
	}

	return ships, false
}

func (h *RebalanceContractFleetHandler) checkIfRebalancingNeeded(
	ctx context.Context,
	cmd *RebalanceContractFleetCommand,
	ships []*navigation.Ship,
	targetMarkets []string,
	result *RebalanceContractFleetResponse,
) (bool, bool) {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", fmt.Sprintf("Checking distribution (threshold: %.1f distance units)", result.DistanceThreshold), nil)
	needsRebalancing, avgDistance, err := h.distributionChecker.IsRebalancingNeeded(
		ctx,
		ships,
		targetMarkets,
		cmd.SystemSymbol,
		cmd.PlayerID.Value(),
		result.DistanceThreshold,
	)
	if err != nil {
		logger.Log("ERROR", fmt.Sprintf("Failed to check distribution: %v", err), nil)
		return false, true
	}

	result.AverageDistance = avgDistance
	logger.Log("INFO", fmt.Sprintf("Average distance to nearest target: %.1f units", avgDistance), nil)

	if !needsRebalancing {
		logger.Log("INFO", "Fleet already well-distributed - skipping rebalancing", nil)
		result.RebalancingSkipped = true
		result.SkipReason = fmt.Sprintf("Fleet well-distributed (avg distance %.1f < threshold %.1f)", avgDistance, result.DistanceThreshold)
		return false, true
	}

	return true, false
}

func (h *RebalanceContractFleetHandler) assignShipsToMarkets(
	ctx context.Context,
	cmd *RebalanceContractFleetCommand,
	ships []*navigation.Ship,
	targetMarkets []string,
	result *RebalanceContractFleetResponse,
) error {
	logger := common.LoggerFromContext(ctx)

	// sp-1ef0: derive an optional contract pre-position hint. When it clears the
	// confidence guard, one idle hull is biased toward the predicted next source; the
	// predicted market is folded into the target set so the assigner can honor it.
	hint, targetMarkets := h.computePrePositionHint(ctx, cmd, targetMarkets)

	logger.Log("INFO", "Assigning ships to markets...", nil)
	assignments, err := h.distributionChecker.AssignShipsToMarketsWithHint(ctx, ships, targetMarkets, cmd.SystemSymbol, cmd.PlayerID.Value(), hint)
	if err != nil {
		return fmt.Errorf("failed to assign ships to markets: %w", err)
	}

	result.Assignments = assignments
	logger.Log("INFO", fmt.Sprintf("Generated %d ship assignments", len(assignments)), nil)
	return nil
}

// computePrePositionHint derives the same-contract/same-good/multi-delivery-remaining
// pre-position hint (sp-1ef0). It returns an inactive (zero) hint whenever the feature is
// disabled, the collaborators are absent, no active contract yields a near-certain
// signal, or the good is not currently sold anywhere in-system. On a strong signal it
// resolves the good to its cheapest-selling market (live availability, never purchase
// history) and returns that market as the hint plus the target set augmented to include
// it. The confidence guard (Confidence >= threshold) is enforced here and re-checked in
// the domain assigner.
func (h *RebalanceContractFleetHandler) computePrePositionHint(
	ctx context.Context,
	cmd *RebalanceContractFleetCommand,
	targetMarkets []string,
) (domainContract.PrePositionHint, []string) {
	logger := common.LoggerFromContext(ctx)

	if h.prepositionCfg.Disabled || h.contractRepo == nil || h.sourceFinder == nil {
		return domainContract.PrePositionHint{}, targetMarkets
	}

	contracts, err := h.contractRepo.FindActiveContracts(ctx, cmd.PlayerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Pre-position: failed to load active contracts: %v", err), nil)
		return domainContract.PrePositionHint{}, targetMarkets
	}

	threshold := h.prepositionCfg.threshold()

	// Strongest same-good-remaining prediction across active contracts. Each prediction
	// is derived from a SINGLE contract's own deliveries (same-contract only) — selecting
	// the most-certain one is not cross-contract inference.
	best := domainContract.SourcePrediction{}
	for _, c := range contracts {
		if c == nil || !c.Accepted() || c.Fulfilled() {
			continue
		}
		pred := domainContract.PredictNextContractSource(c, 0)
		if pred.HasPrediction && pred.Confidence > best.Confidence {
			best = pred
		}
	}

	if !best.HasPrediction || best.Confidence < threshold {
		// Guard: no signal, or a weak/ambiguous one — no wasted move.
		return domainContract.PrePositionHint{}, targetMarkets
	}

	res, err := h.sourceFinder.FindCheapestMarketSelling(ctx, best.Good, cmd.SystemSymbol, cmd.PlayerID.Value())
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Pre-position: failed to resolve source market for %s: %v", best.Good, err), nil)
		return domainContract.PrePositionHint{}, targetMarkets
	}
	if res == nil || res.WaypointSymbol == "" {
		// No in-system market currently sells the good — nothing to pre-position toward.
		return domainContract.PrePositionHint{}, targetMarkets
	}

	logger.Log("INFO", "Contract pre-position: biasing idle hull toward predicted next source", map[string]interface{}{
		"action":         "contract_preposition",
		"good":           best.Good,
		"predicted_wp":   res.WaypointSymbol,
		"confidence":     best.Confidence,
		"threshold":      threshold,
		"remaining_unit": best.RemainingUnits,
	})

	return domainContract.PrePositionHint{
		TargetWaypoint: res.WaypointSymbol,
		Confidence:     best.Confidence,
		Threshold:      threshold,
	}, ensureContains(targetMarkets, res.WaypointSymbol)
}

// ensureContains returns markets with symbol appended when it is not already present.
func ensureContains(markets []string, symbol string) []string {
	for _, m := range markets {
		if m == symbol {
			return markets
		}
	}
	return append(markets, symbol)
}

func (h *RebalanceContractFleetHandler) executeParallelRepositioning(
	ctx context.Context,
	cmd *RebalanceContractFleetCommand,
	ships []*navigation.Ship,
	result *RebalanceContractFleetResponse,
) error {
	logger := common.LoggerFromContext(ctx)

	logger.Log("INFO", "Starting ship repositioning (parallel)...", nil)

	type navResult struct {
		shipSymbol string
		success    bool
		err        error
	}
	resultsChan := make(chan navResult, len(ships))

	shipsToReposition := 0

	for _, ship := range ships {
		targetMarket, hasAssignment := result.Assignments[ship.ShipSymbol()]
		if !hasAssignment {
			continue
		}

		if ship.CurrentLocation().Symbol == targetMarket {
			logger.Log("INFO", fmt.Sprintf("Ship %s already at target %s - skipping", ship.ShipSymbol(), targetMarket), nil)
			continue
		}

		shipsToReposition++

		go func(shipSymbol, destination string) {
			logger.Log("INFO", fmt.Sprintf("Repositioning %s to %s", shipSymbol, destination), nil)

			navigateCmd := &shipNav.NavigateRouteCommand{
				ShipSymbol:  shipSymbol,
				Destination: destination,
				PlayerID:    cmd.PlayerID,
			}

			_, err := h.mediator.Send(ctx, navigateCmd)
			resultsChan <- navResult{
				shipSymbol: shipSymbol,
				success:    err == nil,
				err:        err,
			}
		}(ship.ShipSymbol(), targetMarket)
	}

	for i := 0; i < shipsToReposition; i++ {
		navRes := <-resultsChan
		if navRes.success {
			result.ShipsMoved++
			logger.Log("INFO", fmt.Sprintf("Successfully repositioned %s", navRes.shipSymbol), nil)
		} else {
			logger.Log("ERROR", fmt.Sprintf("Failed to reposition %s: %v", navRes.shipSymbol, navRes.err), nil)
		}
	}

	logger.Log("INFO", fmt.Sprintf("Rebalancing complete: %d/%d ships repositioned", result.ShipsMoved, shipsToReposition), nil)
	return nil
}
