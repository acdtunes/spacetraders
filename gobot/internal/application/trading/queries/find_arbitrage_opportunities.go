package queries

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/services"
	"github.com/andrescamacho/spacetraders-go/internal/application/trading/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// FindArbitrageOpportunitiesQuery requests a scan for arbitrage opportunities in a system
type FindArbitrageOpportunitiesQuery struct {
	SystemSymbol  string  // System to scan (e.g., "X1-AU21")
	PlayerID      int     // Player identifier
	MinMargin     float64 // Minimum profit margin threshold (default 10.0%)
	Limit         int     // Maximum opportunities to return (default 20)
	CargoCapacity int     // Ship cargo capacity for profit calculations (default 40)
}

// FindArbitrageOpportunitiesResponse contains the scan results
type FindArbitrageOpportunitiesResponse struct {
	Opportunities []*types.OpportunityDTO
	TotalScanned  int
	SystemSymbol  string
}

// FindArbitrageOpportunitiesHandler handles queries for finding arbitrage opportunities
type FindArbitrageOpportunitiesHandler struct {
	opportunityFinder *services.ArbitrageOpportunityFinder
}

// NewFindArbitrageOpportunitiesHandler creates a new handler
func NewFindArbitrageOpportunitiesHandler(
	opportunityFinder *services.ArbitrageOpportunityFinder,
) *FindArbitrageOpportunitiesHandler {
	return &FindArbitrageOpportunitiesHandler{
		opportunityFinder: opportunityFinder,
	}
}

// Handle executes the query
func (h *FindArbitrageOpportunitiesHandler) Handle(
	ctx context.Context,
	request common.Request,
) (common.Response, error) {
	query, ok := request.(*FindArbitrageOpportunitiesQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	// Apply defaults
	minMargin := query.MinMargin
	if minMargin <= 0 {
		minMargin = 10.0 // Default 10% margin
	}

	limit := query.Limit
	if limit <= 0 {
		limit = 20 // Default top 20
	}

	cargoCapacity := query.CargoCapacity
	if cargoCapacity <= 0 {
		cargoCapacity = 40 // Default light hauler capacity
	}

	// Delegate to service
	opportunities, err := h.opportunityFinder.FindOpportunities(
		ctx,
		query.SystemSymbol,
		query.PlayerID,
		cargoCapacity,
		minMargin,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to find opportunities: %w", err)
	}

	// Convert to DTOs
	dtos := convertOpportunitiesToDTOs(opportunities)

	return &FindArbitrageOpportunitiesResponse{
		Opportunities: dtos,
		TotalScanned:  len(dtos),
		SystemSymbol:  query.SystemSymbol,
	}, nil
}

// convertOpportunitiesToDTOs converts domain opportunities to DTOs
func convertOpportunitiesToDTOs(opportunities []*trading.ArbitrageOpportunity) []*types.OpportunityDTO {
	dtos := make([]*types.OpportunityDTO, len(opportunities))
	for i, opp := range opportunities {
		dtos[i] = &types.OpportunityDTO{
			Good:            opp.Good(),
			BuyMarket:       opp.BuyMarket().Symbol,
			SellMarket:      opp.SellMarket().Symbol,
			BuyPrice:        opp.BuyPrice(),
			SellPrice:       opp.SellPrice(),
			ProfitPerUnit:   opp.ProfitPerUnit(),
			ProfitMargin:    opp.ProfitMargin(),
			EstimatedProfit: opp.EstimatedProfit(),
			Distance:        opp.Distance(),
			BuySupply:       opp.BuySupply(),
			SellActivity:    opp.SellActivity(),
			Score:           opp.Score(),
		}
	}
	return dtos
}
