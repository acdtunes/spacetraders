package setup

import (
	"reflect"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	ledgerQueries "github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// HandlerRegistry holds all application dependencies for handler creation
type HandlerRegistry struct {
	transactionRepo ledger.TransactionRepository
	playerResolver  *common.PlayerResolver
	clock           shared.Clock
}

// NewHandlerRegistry creates a new handler registry with required dependencies
func NewHandlerRegistry(
	transactionRepo ledger.TransactionRepository,
	playerResolver *common.PlayerResolver,
	clock shared.Clock,
) *HandlerRegistry {
	// Default to real clock if not provided
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &HandlerRegistry{
		transactionRepo: transactionRepo,
		playerResolver:  playerResolver,
		clock:           clock,
	}
}

// RegisterLedgerHandlers registers all ledger command and query handlers with the mediator
//
// This method registers:
//   - RecordTransactionCommand → RecordTransactionHandler (for async transaction recording)
//   - GetTransactionsQuery → GetTransactionsHandler (for transaction queries)
//   - GetProfitLossQuery → GetProfitLossHandler (for P&L reports)
//   - GetCashFlowQuery → GetCashFlowHandler (for cash flow reports)
//
// These handlers enable:
//  1. Other command handlers to record financial transactions via mediator
//  2. Query operations for viewing and analyzing transaction history
func (r *HandlerRegistry) RegisterLedgerHandlers(m common.Mediator) error {
	// Register RecordTransactionCommand handler
	recordHandler := ledgerCommands.NewRecordTransactionHandler(r.transactionRepo, r.clock)
	if err := m.Register(
		reflect.TypeOf(&ledgerCommands.RecordTransactionCommand{}),
		recordHandler,
	); err != nil {
		return err
	}

	// Register GetTransactionsQuery handler
	getTransactionsHandler := ledgerQueries.NewGetTransactionsHandler(r.transactionRepo, r.playerResolver)
	if err := m.Register(
		reflect.TypeOf(&ledgerQueries.GetTransactionsQuery{}),
		getTransactionsHandler,
	); err != nil {
		return err
	}

	// Register GetProfitLossQuery handler
	getProfitLossHandler := ledgerQueries.NewGetProfitLossHandler(r.transactionRepo)
	if err := m.Register(
		reflect.TypeOf(&ledgerQueries.GetProfitLossQuery{}),
		getProfitLossHandler,
	); err != nil {
		return err
	}

	// Register GetCashFlowQuery handler
	getCashFlowHandler := ledgerQueries.NewGetCashFlowHandler(r.transactionRepo)
	if err := m.Register(
		reflect.TypeOf(&ledgerQueries.GetCashFlowQuery{}),
		getCashFlowHandler,
	); err != nil {
		return err
	}

	return nil
}

// CreateConfiguredMediator creates a new mediator with all ledger handlers registered
//
// This is a convenience method that creates a mediator and registers all ledger handlers.
// Use this when you need a fully configured mediator for application use.
func (r *HandlerRegistry) CreateConfiguredMediator() (common.Mediator, error) {
	m := mediator.NewMediator()

	if err := r.RegisterLedgerHandlers(m); err != nil {
		return nil, err
	}

	return m, nil
}
