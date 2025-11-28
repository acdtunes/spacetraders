package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands"
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
	mediator       common.Mediator
	shipRepo       navigation.ShipRepository
	ledgerRecorder LedgerRecorder
}

// NewManufacturingSeller creates a new seller service.
func NewManufacturingSeller(
	mediator common.Mediator,
	shipRepo navigation.ShipRepository,
	ledgerRecorder LedgerRecorder,
) *ManufacturingSeller {
	return &ManufacturingSeller{
		mediator:       mediator,
		shipRepo:       shipRepo,
		ledgerRecorder: ledgerRecorder,
	}
}

// SellCargo sells cargo at current market.
func (s *ManufacturingSeller) SellCargo(ctx context.Context, params SellParams) (*SellResult, error) {
	logger := common.LoggerFromContext(ctx)

	// Determine quantity to sell
	quantity := params.Quantity
	if quantity == 0 {
		// Sell all of this good in cargo
		ship, err := s.shipRepo.FindBySymbol(ctx, params.ShipSymbol, params.PlayerID)
		if err != nil {
			return nil, fmt.Errorf("failed to load ship: %w", err)
		}
		quantity = ship.Cargo().GetItemUnits(params.Good)
	}

	if quantity <= 0 {
		return &SellResult{}, nil // Nothing to sell
	}

	// Execute sell
	sellResp, err := s.mediator.Send(ctx, &shipCmd.SellCargoCommand{
		ShipSymbol: params.ShipSymbol,
		GoodSymbol: params.Good,
		Units:      quantity,
		PlayerID:   params.PlayerID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to sell %s: %w", params.Good, err)
	}

	resp := sellResp.(*shipCmd.SellCargoResponse)

	result := &SellResult{
		UnitsSold:    resp.UnitsSold,
		TotalRevenue: resp.TotalRevenue,
	}

	if resp.UnitsSold > 0 {
		result.PricePerUnit = resp.TotalRevenue / resp.UnitsSold
	}

	result.NetProfit = resp.TotalRevenue - params.TotalCost

	logger.Log("INFO", "Sold goods", map[string]interface{}{
		"good":           params.Good,
		"units_sold":     result.UnitsSold,
		"total_revenue":  result.TotalRevenue,
		"price_per_unit": result.PricePerUnit,
		"net_profit":     result.NetProfit,
	})

	// Record ledger transaction
	if s.ledgerRecorder != nil {
		_ = s.ledgerRecorder.RecordSale(ctx, SaleRecordParams{
			PlayerID:     params.PlayerID.Value(),
			TaskID:       params.TaskID,
			Good:         params.Good,
			Quantity:     result.UnitsSold,
			PricePerUnit: result.PricePerUnit,
			TotalRevenue: result.TotalRevenue,
			Market:       params.Market,
			NetProfit:    result.NetProfit,
		})
	}

	return result, nil
}

// DeliverToFactory sells cargo to factory (delivering inputs).
// This is semantically a "delivery" but uses the sell API.
func (s *ManufacturingSeller) DeliverToFactory(ctx context.Context, params SellParams) (*SellResult, error) {
	logger := common.LoggerFromContext(ctx)

	// Determine quantity to deliver
	quantity := params.Quantity
	if quantity == 0 {
		// Deliver all of this good in cargo
		ship, err := s.shipRepo.FindBySymbol(ctx, params.ShipSymbol, params.PlayerID)
		if err != nil {
			return nil, fmt.Errorf("failed to load ship: %w", err)
		}
		quantity = ship.Cargo().GetItemUnits(params.Good)
	}

	if quantity <= 0 {
		return &SellResult{}, nil // Nothing to deliver
	}

	// Execute sell (to factory)
	sellResp, err := s.mediator.Send(ctx, &shipCmd.SellCargoCommand{
		ShipSymbol: params.ShipSymbol,
		GoodSymbol: params.Good,
		Units:      quantity,
		PlayerID:   params.PlayerID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to deliver %s to factory: %w", params.Good, err)
	}

	resp := sellResp.(*shipCmd.SellCargoResponse)

	result := &SellResult{
		UnitsSold:    resp.UnitsSold,
		TotalRevenue: resp.TotalRevenue,
	}

	if resp.UnitsSold > 0 {
		result.PricePerUnit = resp.TotalRevenue / resp.UnitsSold
	}

	result.NetProfit = resp.TotalRevenue - params.TotalCost

	logger.Log("INFO", "Delivered to factory", map[string]interface{}{
		"good":           params.Good,
		"delivered":      result.UnitsSold,
		"revenue":        result.TotalRevenue,
		"cost":           params.TotalCost,
		"net":            result.NetProfit,
		"price_per_unit": result.PricePerUnit,
	})

	// Record ledger transaction
	if s.ledgerRecorder != nil {
		_ = s.ledgerRecorder.RecordDelivery(ctx, DeliveryRecordParams{
			PlayerID:     params.PlayerID.Value(),
			TaskID:       params.TaskID,
			Good:         params.Good,
			Quantity:     result.UnitsSold,
			PricePerUnit: result.PricePerUnit,
			TotalRevenue: result.TotalRevenue,
			Factory:      params.Market, // Market param holds factory symbol for deliveries
		})
	}

	return result, nil
}

// Liquidate sells orphaned cargo to recover investment.
func (s *ManufacturingSeller) Liquidate(ctx context.Context, params SellParams) (*SellResult, error) {
	logger := common.LoggerFromContext(ctx)

	// Determine quantity to sell
	quantity := params.Quantity
	if quantity == 0 {
		// Sell all of this good in cargo
		ship, err := s.shipRepo.FindBySymbol(ctx, params.ShipSymbol, params.PlayerID)
		if err != nil {
			return nil, fmt.Errorf("failed to load ship: %w", err)
		}
		quantity = ship.Cargo().GetItemUnits(params.Good)
	}

	if quantity <= 0 {
		return &SellResult{}, nil // Nothing to liquidate
	}

	// Execute sell
	sellResp, err := s.mediator.Send(ctx, &shipCmd.SellCargoCommand{
		ShipSymbol: params.ShipSymbol,
		GoodSymbol: params.Good,
		Units:      quantity,
		PlayerID:   params.PlayerID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to sell %s: %w", params.Good, err)
	}

	resp := sellResp.(*shipCmd.SellCargoResponse)

	result := &SellResult{
		UnitsSold:    resp.UnitsSold,
		TotalRevenue: resp.TotalRevenue,
	}

	if resp.UnitsSold > 0 {
		result.PricePerUnit = resp.TotalRevenue / resp.UnitsSold
	}

	logger.Log("INFO", "Liquidated goods (recovery)", map[string]interface{}{
		"good":           params.Good,
		"units_sold":     result.UnitsSold,
		"total_revenue":  result.TotalRevenue,
		"price_per_unit": result.PricePerUnit,
	})

	// Record ledger transaction
	if s.ledgerRecorder != nil {
		_ = s.ledgerRecorder.RecordLiquidation(ctx, SaleRecordParams{
			PlayerID:     params.PlayerID.Value(),
			TaskID:       params.TaskID,
			Good:         params.Good,
			Quantity:     result.UnitsSold,
			PricePerUnit: result.PricePerUnit,
			TotalRevenue: result.TotalRevenue,
			Market:       params.Market,
		})
	}

	return result, nil
}
