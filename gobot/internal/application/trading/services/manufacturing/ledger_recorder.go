package manufacturing

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCmd "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
)

// LedgerRecorder handles recording manufacturing transactions in the ledger.
// Consolidates the repeated ledger recording pattern found throughout the task worker.
type LedgerRecorder interface {
	RecordPurchase(ctx context.Context, params PurchaseRecordParams) error
	RecordSale(ctx context.Context, params SaleRecordParams) error
	RecordDelivery(ctx context.Context, params DeliveryRecordParams) error
}

// PurchaseRecordParams contains parameters for recording a purchase transaction.
type PurchaseRecordParams struct {
	PlayerID     int
	TaskID       string
	Good         string
	Quantity     int
	PricePerUnit int
	TotalCost    int
	SourceMarket string
	Factory      string
	SupplyLevel  string // Optional: supply level at time of purchase
	Description  string
}

// SaleRecordParams contains parameters for recording a sale transaction.
type SaleRecordParams struct {
	PlayerID     int
	TaskID       string
	Good         string
	Quantity     int
	PricePerUnit int
	TotalRevenue int
	Market       string
	NetProfit    int
	Description  string
}

// DeliveryRecordParams contains parameters for recording a delivery transaction.
type DeliveryRecordParams struct {
	PlayerID     int
	TaskID       string
	Good         string
	Quantity     int
	PricePerUnit int
	TotalRevenue int
	Factory      string
	Description  string
}

// ManufacturingLedgerRecorder implements LedgerRecorder for manufacturing operations.
type ManufacturingLedgerRecorder struct {
	mediator common.Mediator
}

// NewManufacturingLedgerRecorder creates a new ledger recorder service.
func NewManufacturingLedgerRecorder(mediator common.Mediator) *ManufacturingLedgerRecorder {
	return &ManufacturingLedgerRecorder{
		mediator: mediator,
	}
}

// RecordPurchase records a purchase transaction in the ledger.
func (r *ManufacturingLedgerRecorder) RecordPurchase(ctx context.Context, params PurchaseRecordParams) error {
	description := params.Description
	if description == "" {
		description = fmt.Sprintf("Manufacturing: Buy %d %s for delivery to factory", params.Quantity, params.Good)
	}

	metadata := map[string]interface{}{
		"task_id":        params.TaskID,
		"good":           params.Good,
		"quantity":       params.Quantity,
		"price_per_unit": params.PricePerUnit,
	}

	if params.SourceMarket != "" {
		metadata["source_market"] = params.SourceMarket
	}
	if params.Factory != "" {
		metadata["factory"] = params.Factory
	}
	if params.SupplyLevel != "" {
		metadata["supply_level"] = params.SupplyLevel
	}

	_, err := r.mediator.Send(ctx, &ledgerCmd.RecordTransactionCommand{
		PlayerID:          params.PlayerID,
		TransactionType:   string(ledger.TransactionTypePurchaseCargo),
		Amount:            -params.TotalCost, // Negative for expense
		Description:       description,
		RelatedEntityType: "manufacturing_task",
		RelatedEntityID:   params.TaskID,
		OperationType:     "manufacturing",
		Metadata:          metadata,
	})
	return err
}

// RecordSale records a sale transaction in the ledger.
func (r *ManufacturingLedgerRecorder) RecordSale(ctx context.Context, params SaleRecordParams) error {
	description := params.Description
	if description == "" {
		if params.NetProfit != 0 {
			description = fmt.Sprintf("Manufacturing: Sell %d %s (profit: %d)", params.Quantity, params.Good, params.NetProfit)
		} else {
			description = fmt.Sprintf("Manufacturing: Sell %d %s", params.Quantity, params.Good)
		}
	}

	metadata := map[string]interface{}{
		"task_id":        params.TaskID,
		"good":           params.Good,
		"quantity":       params.Quantity,
		"price_per_unit": params.PricePerUnit,
		"market":         params.Market,
	}

	if params.NetProfit != 0 {
		metadata["net_profit"] = params.NetProfit
	}

	_, err := r.mediator.Send(ctx, &ledgerCmd.RecordTransactionCommand{
		PlayerID:          params.PlayerID,
		TransactionType:   string(ledger.TransactionTypeSellCargo),
		Amount:            params.TotalRevenue,
		Description:       description,
		RelatedEntityType: "manufacturing_task",
		RelatedEntityID:   params.TaskID,
		OperationType:     "manufacturing",
		Metadata:          metadata,
	})
	return err
}

// RecordDelivery records a delivery transaction in the ledger (selling to factory).
func (r *ManufacturingLedgerRecorder) RecordDelivery(ctx context.Context, params DeliveryRecordParams) error {
	description := params.Description
	if description == "" {
		description = fmt.Sprintf("Manufacturing: Deliver %d %s to factory", params.Quantity, params.Good)
	}

	metadata := map[string]interface{}{
		"task_id":        params.TaskID,
		"good":           params.Good,
		"quantity":       params.Quantity,
		"price_per_unit": params.PricePerUnit,
		"factory":        params.Factory,
	}

	_, err := r.mediator.Send(ctx, &ledgerCmd.RecordTransactionCommand{
		PlayerID:          params.PlayerID,
		TransactionType:   string(ledger.TransactionTypeSellCargo),
		Amount:            params.TotalRevenue,
		Description:       description,
		RelatedEntityType: "manufacturing_task",
		RelatedEntityID:   params.TaskID,
		OperationType:     "manufacturing",
		Metadata:          metadata,
	})
	return err
}

// RecordLiquidation records a liquidation (recovery sale) transaction in the ledger.
func (r *ManufacturingLedgerRecorder) RecordLiquidation(ctx context.Context, params SaleRecordParams) error {
	description := params.Description
	if description == "" {
		description = fmt.Sprintf("Manufacturing: Liquidate %d %s (recovery)", params.Quantity, params.Good)
	}

	metadata := map[string]interface{}{
		"task_id":        params.TaskID,
		"good":           params.Good,
		"quantity":       params.Quantity,
		"price_per_unit": params.PricePerUnit,
		"waypoint":       params.Market,
	}

	_, err := r.mediator.Send(ctx, &ledgerCmd.RecordTransactionCommand{
		PlayerID:          params.PlayerID,
		TransactionType:   string(ledger.TransactionTypeSellCargo),
		Amount:            params.TotalRevenue,
		Description:       description,
		RelatedEntityType: "manufacturing_task",
		RelatedEntityID:   params.TaskID,
		OperationType:     "manufacturing",
		Metadata:          metadata,
	})
	return err
}
