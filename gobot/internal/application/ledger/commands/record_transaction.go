package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RecordTransactionCommand represents a command to record a financial transaction
type RecordTransactionCommand struct {
	PlayerID          int
	TransactionType   string
	Amount            int                    // Positive for income, negative for expenses
	BalanceBefore     int
	BalanceAfter      int
	Description       string
	Metadata          map[string]interface{}
	RelatedEntityType string
	RelatedEntityID   string
	Timestamp         *time.Time // Optional: if provided, use this timestamp; otherwise use current time
}

// RecordTransactionResponse represents the result of recording a transaction
type RecordTransactionResponse struct {
	TransactionID string
	Timestamp     time.Time
}

// RecordTransactionHandler handles the RecordTransaction command
type RecordTransactionHandler struct {
	transactionRepo ledger.TransactionRepository
	clock           shared.Clock
}

// NewRecordTransactionHandler creates a new RecordTransactionHandler
func NewRecordTransactionHandler(
	transactionRepo ledger.TransactionRepository,
	clock shared.Clock,
) *RecordTransactionHandler {
	// Default to real clock if not provided
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &RecordTransactionHandler{
		transactionRepo: transactionRepo,
		clock:           clock,
	}
}

// Handle executes the RecordTransaction command
func (h *RecordTransactionHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RecordTransactionCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *RecordTransactionCommand")
	}

	// Parse and validate transaction type
	transactionType, err := ledger.ParseTransactionType(cmd.TransactionType)
	if err != nil {
		return nil, fmt.Errorf("invalid transaction type: %w", err)
	}

	// Create player ID
	playerID, err := shared.NewPlayerID(cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("invalid player ID: %w", err)
	}

	// Determine timestamp
	timestamp := h.clock.Now()
	if cmd.Timestamp != nil {
		timestamp = *cmd.Timestamp
	}

	// Create transaction entity
	transaction, err := ledger.NewTransaction(
		playerID,
		timestamp,
		transactionType,
		cmd.Amount,
		cmd.BalanceBefore,
		cmd.BalanceAfter,
		cmd.Description,
		cmd.Metadata,
		cmd.RelatedEntityType,
		cmd.RelatedEntityID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create transaction: %w", err)
	}

	// Persist transaction
	if err := h.transactionRepo.Create(ctx, transaction); err != nil {
		return nil, fmt.Errorf("failed to persist transaction: %w", err)
	}

	// Record transaction metrics
	// Extract category from transaction metadata (if available)
	category := ""
	if transaction.Category() != "" {
		category = string(transaction.Category())
	}

	// Extract agent symbol from metadata (if available)
	agentSymbol := ""
	if cmd.Metadata != nil {
		if agent, ok := cmd.Metadata["agent"].(string); ok {
			agentSymbol = agent
		}
	}
	// Fallback to a default agent if not provided
	if agentSymbol == "" {
		agentSymbol = "UNKNOWN"
	}

	// Record the transaction metrics
	metrics.RecordTransaction(
		cmd.PlayerID,
		agentSymbol,
		cmd.TransactionType,
		category,
		cmd.Amount,
		cmd.BalanceAfter,
	)

	return &RecordTransactionResponse{
		TransactionID: transaction.ID().String(),
		Timestamp:     transaction.Timestamp(),
	}, nil
}
