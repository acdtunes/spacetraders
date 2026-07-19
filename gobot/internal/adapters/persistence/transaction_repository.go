package persistence

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"gorm.io/gorm"
)

// GormTransactionRepository implements TransactionRepository using GORM
type GormTransactionRepository struct {
	db *gorm.DB
}

// NewGormTransactionRepository creates a new GORM transaction repository
func NewGormTransactionRepository(db *gorm.DB) *GormTransactionRepository {
	return &GormTransactionRepository{db: db}
}

// Create persists a new transaction
func (r *GormTransactionRepository) Create(ctx context.Context, transaction *ledger.Transaction) error {
	model, err := r.transactionToModel(transaction)
	if err != nil {
		return fmt.Errorf("failed to convert transaction to model: %w", err)
	}

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return fmt.Errorf("failed to create transaction: %w", result.Error)
	}

	return nil
}

// FindByID retrieves a transaction by its ID
func (r *GormTransactionRepository) FindByID(ctx context.Context, id ledger.TransactionID, playerID shared.PlayerID) (*ledger.Transaction, error) {
	var model TransactionModel
	result := r.db.WithContext(ctx).
		Where("id = ? AND player_id = ?", id.String(), playerID.Value()).
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, &ledger.ErrTransactionNotFound{
				ID:       id.String(),
				PlayerID: playerID.Value(),
			}
		}
		return nil, fmt.Errorf("failed to find transaction: %w", result.Error)
	}

	return r.modelToTransaction(&model)
}

// FindByPlayer retrieves transactions for a player with optional filtering
func (r *GormTransactionRepository) FindByPlayer(ctx context.Context, playerID shared.PlayerID, opts ledger.QueryOptions) ([]*ledger.Transaction, error) {
	query := r.db.WithContext(ctx).Where("player_id = ?", playerID.Value())

	query = r.applyFilters(query, opts)

	orderBy := "timestamp DESC"
	if opts.OrderBy != "" {
		orderBy = opts.OrderBy
	}
	query = query.Order(orderBy)

	if opts.Limit > 0 {
		query = query.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		query = query.Offset(opts.Offset)
	}

	var models []TransactionModel
	result := query.Find(&models)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to find transactions: %w", result.Error)
	}

	transactions := make([]*ledger.Transaction, len(models))
	for i, model := range models {
		tx, err := r.modelToTransaction(&model)
		if err != nil {
			return nil, fmt.Errorf("failed to convert transaction model: %w", err)
		}
		transactions[i] = tx
	}

	return transactions, nil
}

// CountByPlayer returns the count of transactions matching the criteria
func (r *GormTransactionRepository) CountByPlayer(ctx context.Context, playerID shared.PlayerID, opts ledger.QueryOptions) (int, error) {
	query := r.db.WithContext(ctx).Model(&TransactionModel{}).Where("player_id = ?", playerID.Value())

	query = r.applyFilters(query, opts)

	var count int64
	result := query.Count(&count)
	if result.Error != nil {
		return 0, fmt.Errorf("failed to count transactions: %w", result.Error)
	}

	return int(count), nil
}

// applyFilters applies query options to a GORM query
func (r *GormTransactionRepository) applyFilters(query *gorm.DB, opts ledger.QueryOptions) *gorm.DB {
	if opts.StartDate != nil {
		query = query.Where("timestamp >= ?", *opts.StartDate)
	}
	if opts.EndDate != nil {
		query = query.Where("timestamp <= ?", *opts.EndDate)
	}

	if opts.Category != nil {
		query = query.Where("category = ?", opts.Category.String())
	}

	if opts.TransactionType != nil {
		query = query.Where("transaction_type = ?", opts.TransactionType.String())
	}

	if opts.RelatedEntityType != nil {
		query = query.Where("related_entity_type = ?", *opts.RelatedEntityType)
	}
	if opts.RelatedEntityID != nil {
		query = query.Where("related_entity_id = ?", *opts.RelatedEntityID)
	}

	return query
}

// modelToTransaction converts database model to domain entity
func (r *GormTransactionRepository) modelToTransaction(model *TransactionModel) (*ledger.Transaction, error) {
	id, err := ledger.NewTransactionIDFromString(model.ID)
	if err != nil {
		return nil, fmt.Errorf("invalid transaction ID in database: %w", err)
	}

	playerID, err := shared.NewPlayerID(model.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("invalid player ID in database: %w", err)
	}

	transactionType, err := ledger.ParseTransactionType(model.TransactionType)
	if err != nil {
		return nil, fmt.Errorf("invalid transaction type in database: %w", err)
	}

	var metadata map[string]interface{}
	if model.Metadata != "" {
		if err := json.Unmarshal([]byte(model.Metadata), &metadata); err != nil {
			// If unmarshal fails, leave metadata as nil
			metadata = nil
		}
	}

	// Reconstruct transaction entity. category is intentionally NOT read from the
	// model: ReconstructTransaction re-derives it from type, so a
	// divergent or invalid stored category can never surface on read.
	return ledger.ReconstructTransaction(
		id,
		playerID,
		model.Timestamp,
		transactionType,
		model.Amount,
		model.BalanceBefore,
		model.BalanceAfter,
		model.Description,
		metadata,
		model.RelatedEntityType,
		model.RelatedEntityID,
		model.OperationType,
	), nil
}

// transactionToModel converts domain entity to database model
func (r *GormTransactionRepository) transactionToModel(tx *ledger.Transaction) (*TransactionModel, error) {
	var metadataJSON string
	if tx.Metadata() != nil {
		bytes, err := json.Marshal(tx.Metadata())
		if err != nil {
			return nil, fmt.Errorf("failed to marshal metadata: %w", err)
		}
		metadataJSON = string(bytes)
	}

	return &TransactionModel{
		ID:                tx.ID().String(),
		PlayerID:          tx.PlayerID().Value(),
		Timestamp:         tx.Timestamp(),
		TransactionType:   tx.TransactionType().String(),
		Category:          tx.Category().String(),
		Amount:            tx.Amount(),
		BalanceBefore:     tx.BalanceBefore(),
		BalanceAfter:      tx.BalanceAfter(),
		Description:       tx.Description(),
		Metadata:          metadataJSON,
		RelatedEntityType: tx.RelatedEntityType(),
		RelatedEntityID:   tx.RelatedEntityID(),
		OperationType:     tx.OperationType(),
	}, nil
}
