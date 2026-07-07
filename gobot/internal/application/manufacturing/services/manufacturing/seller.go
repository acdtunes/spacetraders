package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCargo "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/cargo"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// Seller handles cargo selling operations for manufacturing tasks.
// Consolidates the repeated sell pattern found throughout the task worker.
type Seller interface {
	// SellCargo sells cargo at current market
	SellCargo(ctx context.Context, params SellParams) (*SellResult, error)

	// DeliverToFactory sells cargo to factory (delivering inputs)
	DeliverToFactory(ctx context.Context, params SellParams) (*SellResult, error)
}

// SellParams contains parameters for a sell operation.
type SellParams struct {
	Ship       *navigation.Ship // Optional: use for cargo check (avoids API call when Quantity=0)
	ShipSymbol string
	PlayerID   shared.PlayerID
	Good       string
	Quantity   int    // 0 = sell all of this good in cargo
	TaskID     string // For ledger context
	Market     string // For ledger context (market or factory symbol)
	TotalCost  int    // For net profit calculation (optional)
}

// SellResult contains the results of a sell operation.
type SellResult struct {
	UnitsSold    int
	TotalRevenue int
	PricePerUnit int
	NetProfit    int // Revenue - Cost (if cost provided)
}

// ManufacturingSeller implements Seller for manufacturing operations.
type ManufacturingSeller struct {
	mediator common.Mediator
	shipRepo navigation.ShipRepository
}

// NewManufacturingSeller creates a new seller service.
//
// Ledger recording is NOT a responsibility of the seller: the SellCargoCommand it
// dispatches is the single authoritative recorder (CargoTransactionHandler
// self-records each batch with the in-band agent.credits, sp-sc6u). Recording
// again here double-counted every sell/delivery/liquidation (sp-uytq).
func NewManufacturingSeller(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
) *ManufacturingSeller {
	return &ManufacturingSeller{
		mediator: mediator,
		shipRepo: shipRepo,
	}
}

func (s *ManufacturingSeller) resolveQuantity(ctx context.Context, params SellParams) (int, error) {
	if params.Quantity != 0 {
		return params.Quantity, nil
	}
	ship := params.Ship
	if ship == nil {
		var err error
		ship, err = s.shipRepo.FindBySymbol(ctx, params.ShipSymbol, params.PlayerID)
		if err != nil {
			return 0, fmt.Errorf("failed to load ship: %w", err)
		}
	}
	return ship.Cargo().GetItemUnits(params.Good), nil
}

func (s *ManufacturingSeller) sellViaMediator(ctx context.Context, params SellParams, quantity int) (*shipCargo.SellCargoResponse, error) {
	sellResp, err := s.mediator.Send(shared.WithSkipMarketRefresh(ctx), &shipCargo.SellCargoCommand{
		ShipSymbol: params.ShipSymbol,
		GoodSymbol: params.Good,
		Units:      quantity,
		PlayerID:   params.PlayerID,
	})
	if err != nil {
		return nil, err
	}
	return sellResp.(*shipCargo.SellCargoResponse), nil
}

func newSellResult(resp *shipCargo.SellCargoResponse) *SellResult {
	result := &SellResult{
		UnitsSold:    resp.UnitsSold,
		TotalRevenue: resp.TotalRevenue,
	}
	if resp.UnitsSold > 0 {
		result.PricePerUnit = resp.TotalRevenue / resp.UnitsSold
	}
	return result
}

// SellCargo sells cargo at current market.
func (s *ManufacturingSeller) SellCargo(ctx context.Context, params SellParams) (*SellResult, error) {
	logger := common.LoggerFromContext(ctx)

	quantity, err := s.resolveQuantity(ctx, params)
	if err != nil {
		return nil, err
	}

	if quantity <= 0 {
		return &SellResult{}, nil // Nothing to sell
	}

	resp, err := s.sellViaMediator(ctx, params, quantity)
	if err != nil {
		return nil, fmt.Errorf("failed to sell %s: %w", params.Good, err)
	}

	result := newSellResult(resp)
	result.NetProfit = resp.TotalRevenue - params.TotalCost

	logger.Log("INFO", "Sold goods", map[string]interface{}{
		"good":           params.Good,
		"units_sold":     result.UnitsSold,
		"total_revenue":  result.TotalRevenue,
		"price_per_unit": result.PricePerUnit,
		"net_profit":     result.NetProfit,
	})

	// The dispatched SellCargoCommand already recorded this sale in the ledger
	// (the authoritative single-writer path). Do not record it again.

	return result, nil
}

// DeliverToFactory sells cargo to factory (delivering inputs).
// This is semantically a "delivery" but uses the sell API.
func (s *ManufacturingSeller) DeliverToFactory(ctx context.Context, params SellParams) (*SellResult, error) {
	logger := common.LoggerFromContext(ctx)

	quantity, err := s.resolveQuantity(ctx, params)
	if err != nil {
		return nil, err
	}

	if quantity <= 0 {
		return &SellResult{}, nil // Nothing to deliver
	}

	resp, err := s.sellViaMediator(ctx, params, quantity)
	if err != nil {
		return nil, fmt.Errorf("failed to deliver %s to factory: %w", params.Good, err)
	}

	result := newSellResult(resp)
	result.NetProfit = resp.TotalRevenue - params.TotalCost

	logger.Log("INFO", "Delivered to factory", map[string]interface{}{
		"good":           params.Good,
		"delivered":      result.UnitsSold,
		"revenue":        result.TotalRevenue,
		"cost":           params.TotalCost,
		"net":            result.NetProfit,
		"price_per_unit": result.PricePerUnit,
	})

	// The dispatched SellCargoCommand already recorded this delivery in the
	// ledger (the authoritative single-writer path). Do not record it again.

	return result, nil
}

// Liquidate sells orphaned cargo to recover investment.
func (s *ManufacturingSeller) Liquidate(ctx context.Context, params SellParams) (*SellResult, error) {
	logger := common.LoggerFromContext(ctx)

	quantity, err := s.resolveQuantity(ctx, params)
	if err != nil {
		return nil, err
	}

	if quantity <= 0 {
		return &SellResult{}, nil // Nothing to liquidate
	}

	resp, err := s.sellViaMediator(ctx, params, quantity)
	if err != nil {
		return nil, fmt.Errorf("failed to sell %s: %w", params.Good, err)
	}

	result := newSellResult(resp)

	logger.Log("INFO", "Liquidated goods (recovery)", map[string]interface{}{
		"good":           params.Good,
		"units_sold":     result.UnitsSold,
		"total_revenue":  result.TotalRevenue,
		"price_per_unit": result.PricePerUnit,
	})

	// The dispatched SellCargoCommand already recorded this liquidation in the
	// ledger (the authoritative single-writer path). Do not record it again.

	return result, nil
}
