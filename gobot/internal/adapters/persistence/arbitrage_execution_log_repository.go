package persistence

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"gorm.io/gorm"
)

// GormArbitrageExecutionLogRepository implements ArbitrageExecutionLogRepository using GORM
type GormArbitrageExecutionLogRepository struct {
	db *gorm.DB
}

// NewGormArbitrageExecutionLogRepository creates a new GORM arbitrage execution log repository
func NewGormArbitrageExecutionLogRepository(db *gorm.DB) *GormArbitrageExecutionLogRepository {
	return &GormArbitrageExecutionLogRepository{db: db}
}

// Save persists a new execution log
func (r *GormArbitrageExecutionLogRepository) Save(ctx context.Context, log *trading.ArbitrageExecutionLog) error {
	// Validate log before saving
	if err := r.validateLog(log); err != nil {
		return fmt.Errorf("invalid execution log: %w", err)
	}

	// Convert to GORM model
	model := r.logToModel(log)

	// Save to database
	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return fmt.Errorf("failed to save execution log: %w", result.Error)
	}

	return nil
}

// FindByPlayerID retrieves logs for ML training with pagination
func (r *GormArbitrageExecutionLogRepository) FindByPlayerID(
	ctx context.Context,
	playerID shared.PlayerID,
	limit int,
	offset int,
) ([]*trading.ArbitrageExecutionLog, error) {
	query := r.db.WithContext(ctx).
		Where("player_id = ?", playerID.Value()).
		Order("executed_at DESC")

	// Apply pagination
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}

	var models []ArbitrageExecutionLogModel
	result := query.Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to find execution logs: %w", result.Error)
	}

	// Convert models to domain entities
	logs := make([]*trading.ArbitrageExecutionLog, len(models))
	for i, model := range models {
		log, err := r.modelToLog(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert model: %w", err)
		}
		logs[i] = log
	}

	return logs, nil
}

// FindSuccessfulRuns retrieves only successful executions
func (r *GormArbitrageExecutionLogRepository) FindSuccessfulRuns(
	ctx context.Context,
	playerID shared.PlayerID,
	minExamples int,
) ([]*trading.ArbitrageExecutionLog, error) {
	query := r.db.WithContext(ctx).
		Where("player_id = ? AND success = ?", playerID.Value(), true).
		Order("executed_at DESC")

	// Apply limit if specified
	if minExamples > 0 {
		query = query.Limit(minExamples)
	}

	var models []ArbitrageExecutionLogModel
	result := query.Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to find successful runs: %w", result.Error)
	}

	// Convert models to domain entities
	logs := make([]*trading.ArbitrageExecutionLog, len(models))
	for i, model := range models {
		log, err := r.modelToLog(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert model: %w", err)
		}
		logs[i] = log
	}

	return logs, nil
}

// CountByPlayerID returns total logged executions for a player
func (r *GormArbitrageExecutionLogRepository) CountByPlayerID(ctx context.Context, playerID shared.PlayerID) (int, error) {
	var count int64
	result := r.db.WithContext(ctx).
		Model(&ArbitrageExecutionLogModel{}).
		Where("player_id = ?", playerID.Value()).
		Count(&count)

	if result.Error != nil {
		return 0, fmt.Errorf("failed to count execution logs: %w", result.Error)
	}

	return int(count), nil
}

// ExportToCSV exports logs for ML training (Python consumption)
func (r *GormArbitrageExecutionLogRepository) ExportToCSV(
	ctx context.Context,
	playerID shared.PlayerID,
	outputPath string,
) error {
	// Retrieve successful runs only (used for ML training)
	logs, err := r.FindSuccessfulRuns(ctx, playerID, 0)
	if err != nil {
		return fmt.Errorf("failed to retrieve logs for export: %w", err)
	}

	if len(logs) == 0 {
		return fmt.Errorf("no successful execution logs found for player %d", playerID.Value())
	}

	// Create output file
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create CSV file: %w", err)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	// Write header
	header := []string{
		"id",
		"container_id",
		"ship_symbol",
		"executed_at",
		"good_symbol",
		"buy_market",
		"sell_market",
		"buy_price",
		"sell_price",
		"profit_margin",
		"distance",
		"estimated_profit",
		"buy_supply",
		"sell_activity",
		"current_score",
		"cargo_capacity",
		"cargo_used",
		"fuel_current",
		"fuel_capacity",
		"current_location",
		"actual_net_profit",
		"actual_duration_seconds",
		"fuel_consumed",
		"units_purchased",
		"units_sold",
		"purchase_cost",
		"sale_revenue",
		"buy_price_at_validation",
		"sell_price_at_validation",
		"buy_price_actual",
		"sell_price_actual",
		"profit_per_second",
		"profit_per_unit",
		"margin_accuracy",
	}
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("failed to write CSV header: %w", err)
	}

	// Write data rows
	for _, log := range logs {
		row := []string{
			strconv.Itoa(log.ID()),
			log.ContainerID(),
			log.ShipSymbol(),
			log.ExecutedAt().Format("2006-01-02 15:04:05"),
			log.Good(),
			log.BuyMarket(),
			log.SellMarket(),
			strconv.Itoa(log.BuyPrice()),
			strconv.Itoa(log.SellPrice()),
			fmt.Sprintf("%.2f", log.ProfitMargin()),
			fmt.Sprintf("%.2f", log.Distance()),
			strconv.Itoa(log.EstimatedProfit()),
			log.BuySupply(),
			log.SellActivity(),
			fmt.Sprintf("%.2f", log.CurrentScore()),
			strconv.Itoa(log.CargoCapacity()),
			strconv.Itoa(log.CargoUsed()),
			strconv.Itoa(log.FuelCurrent()),
			strconv.Itoa(log.FuelCapacity()),
			log.CurrentLocation(),
			strconv.Itoa(log.ActualNetProfit()),
			strconv.Itoa(log.ActualDuration()),
			strconv.Itoa(log.FuelConsumed()),
			strconv.Itoa(log.UnitsPurchased()),
			strconv.Itoa(log.UnitsSold()),
			strconv.Itoa(log.PurchaseCost()),
			strconv.Itoa(log.SaleRevenue()),
			strconv.Itoa(log.BuyPriceAtValidation()),
			strconv.Itoa(log.SellPriceAtValidation()),
			strconv.Itoa(log.BuyPriceActual()),
			strconv.Itoa(log.SellPriceActual()),
			fmt.Sprintf("%.4f", log.ProfitPerSecond()),
			fmt.Sprintf("%.2f", log.ProfitPerUnit()),
			fmt.Sprintf("%.2f", log.MarginAccuracy()),
		}

		if err := writer.Write(row); err != nil {
			return fmt.Errorf("failed to write CSV row: %w", err)
		}
	}

	return nil
}

// validateLog validates execution log before saving
func (r *GormArbitrageExecutionLogRepository) validateLog(log *trading.ArbitrageExecutionLog) error {
	if log.ShipSymbol() == "" {
		return fmt.Errorf("ship symbol required")
	}
	if log.Good() == "" {
		return fmt.Errorf("good symbol required")
	}
	if log.Success() && log.ActualDuration() <= 0 {
		return fmt.Errorf("successful run must have positive duration")
	}
	if log.UnitsSold() > log.CargoCapacity() {
		return fmt.Errorf("units sold (%d) exceeds cargo capacity (%d)",
			log.UnitsSold(), log.CargoCapacity())
	}
	return nil
}

// modelToLog converts database model to domain entity
func (r *GormArbitrageExecutionLogRepository) modelToLog(model *ArbitrageExecutionLogModel) (*trading.ArbitrageExecutionLog, error) {
	// Parse player ID
	playerID, err := shared.NewPlayerID(model.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("invalid player ID in database: %w", err)
	}

	// Helper function to dereference pointer or return zero value
	derefString := func(s *string) string {
		if s == nil {
			return ""
		}
		return *s
	}
	derefInt := func(i *int) int {
		if i == nil {
			return 0
		}
		return *i
	}
	derefFloat64 := func(f *float64) float64 {
		if f == nil {
			return 0
		}
		return *f
	}

	// Reconstruct entity
	return trading.ReconstructArbitrageExecutionLog(
		model.ID,
		model.ContainerID,
		model.ShipSymbol,
		playerID,
		model.ExecutedAt,
		model.Success,
		derefString(model.ErrorMessage),
		model.GoodSymbol,
		model.BuyMarket,
		model.SellMarket,
		model.BuyPrice,
		model.SellPrice,
		model.ProfitMargin,
		model.Distance,
		model.EstimatedProfit,
		derefString(model.BuySupply),
		derefString(model.SellActivity),
		derefFloat64(model.CurrentScore),
		model.CargoCapacity,
		model.CargoUsed,
		model.FuelCurrent,
		model.FuelCapacity,
		derefString(model.CurrentLocation),
		derefInt(model.ActualNetProfit),
		derefInt(model.ActualDurationSeconds),
		derefInt(model.FuelConsumed),
		derefInt(model.UnitsPurchased),
		derefInt(model.UnitsSold),
		derefInt(model.PurchaseCost),
		derefInt(model.SaleRevenue),
		derefInt(model.BuyPriceAtValidation),
		derefInt(model.SellPriceAtValidation),
		derefInt(model.BuyPriceActual),
		derefInt(model.SellPriceActual),
		derefFloat64(model.ProfitPerSecond),
		derefFloat64(model.ProfitPerUnit),
		derefFloat64(model.MarginAccuracy),
	), nil
}

// logToModel converts domain entity to database model
func (r *GormArbitrageExecutionLogRepository) logToModel(log *trading.ArbitrageExecutionLog) *ArbitrageExecutionLogModel {
	// Helper function to create pointer
	strPtr := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	intPtr := func(i int) *int {
		if i == 0 {
			return nil
		}
		return &i
	}
	float64Ptr := func(f float64) *float64 {
		if f == 0 {
			return nil
		}
		return &f
	}

	return &ArbitrageExecutionLogModel{
		// Don't set ID - it's auto-incremented
		ContainerID: log.ContainerID(),
		ShipSymbol:  log.ShipSymbol(),
		PlayerID:    log.PlayerID().Value(),
		ExecutedAt:  log.ExecutedAt(),
		Success:     log.Success(),
		ErrorMessage: strPtr(log.ErrorMsg()),

		// Opportunity features
		GoodSymbol:      log.Good(),
		BuyMarket:       log.BuyMarket(),
		SellMarket:      log.SellMarket(),
		BuyPrice:        log.BuyPrice(),
		SellPrice:       log.SellPrice(),
		ProfitMargin:    log.ProfitMargin(),
		Distance:        log.Distance(),
		EstimatedProfit: log.EstimatedProfit(),
		BuySupply:       strPtr(log.BuySupply()),
		SellActivity:    strPtr(log.SellActivity()),
		CurrentScore:    float64Ptr(log.CurrentScore()),

		// Ship state
		CargoCapacity:   log.CargoCapacity(),
		CargoUsed:       log.CargoUsed(),
		FuelCurrent:     log.FuelCurrent(),
		FuelCapacity:    log.FuelCapacity(),
		CurrentLocation: strPtr(log.CurrentLocation()),

		// Execution results
		ActualNetProfit:       intPtr(log.ActualNetProfit()),
		ActualDurationSeconds: intPtr(log.ActualDuration()),
		FuelConsumed:          intPtr(log.FuelConsumed()),
		UnitsPurchased:        intPtr(log.UnitsPurchased()),
		UnitsSold:             intPtr(log.UnitsSold()),
		PurchaseCost:          intPtr(log.PurchaseCost()),
		SaleRevenue:           intPtr(log.SaleRevenue()),

		// Price drift tracking
		BuyPriceAtValidation:  intPtr(log.BuyPriceAtValidation()),
		SellPriceAtValidation: intPtr(log.SellPriceAtValidation()),
		BuyPriceActual:        intPtr(log.BuyPriceActual()),
		SellPriceActual:       intPtr(log.SellPriceActual()),

		// Derived metrics
		ProfitPerSecond: float64Ptr(log.ProfitPerSecond()),
		ProfitPerUnit:   float64Ptr(log.ProfitPerUnit()),
		MarginAccuracy:  float64Ptr(log.MarginAccuracy()),
	}
}
