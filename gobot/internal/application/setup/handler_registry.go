package setup

import (
	"reflect"

	gasCommands "github.com/andrescamacho/spacetraders-go/internal/application/gas/commands"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	ledgerQueries "github.com/andrescamacho/spacetraders-go/internal/application/ledger/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/application/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/storage"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// HandlerRegistry holds all application dependencies for handler creation
type HandlerRegistry struct {
	transactionRepo    ledger.TransactionRepository
	playerResolver     *player.PlayerResolver
	clock              shared.Clock
	shipRepo           navigation.ShipRepository
	shipAssignmentRepo container.ShipAssignmentRepository
	daemonClient       daemon.DaemonClient
	// Storage and gas operation dependencies
	storageOpRepo      storage.StorageOperationRepository
	storageCoordinator storage.StorageCoordinator
	waypointRepo       system.WaypointRepository
	apiClient          domainPorts.APIClient
}

// NewHandlerRegistry creates a new handler registry with required dependencies
func NewHandlerRegistry(
	transactionRepo ledger.TransactionRepository,
	playerResolver *player.PlayerResolver,
	clock shared.Clock,
	shipRepo navigation.ShipRepository,
	shipAssignmentRepo container.ShipAssignmentRepository,
	daemonClient daemon.DaemonClient,
	storageOpRepo storage.StorageOperationRepository,
	storageCoordinator storage.StorageCoordinator,
	waypointRepo system.WaypointRepository,
	apiClient domainPorts.APIClient,
) *HandlerRegistry {
	// Default to real clock if not provided
	if clock == nil {
		clock = shared.NewRealClock()
	}

	return &HandlerRegistry{
		transactionRepo:    transactionRepo,
		playerResolver:     playerResolver,
		clock:              clock,
		shipRepo:           shipRepo,
		shipAssignmentRepo: shipAssignmentRepo,
		daemonClient:       daemonClient,
		storageOpRepo:      storageOpRepo,
		storageCoordinator: storageCoordinator,
		waypointRepo:       waypointRepo,
		apiClient:          apiClient,
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
func (r *HandlerRegistry) RegisterLedgerHandlers(m mediator.Mediator) error {
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

// RegisterGasHandlers registers all gas extraction command handlers
//
// This method registers:
//   - RunGasCoordinatorCommand → RunGasCoordinatorHandler
//   - RunSiphonWorkerCommand → RunSiphonWorkerHandler
//
// Note: Transport is handled by manufacturing pool via STORAGE_ACQUIRE_DELIVER tasks.
// Storage ships buffer cargo; haulers from the manufacturing pool pick it up.
func (r *HandlerRegistry) RegisterGasHandlers(m mediator.Mediator) error {
	// Register RunGasCoordinatorCommand handler
	coordinatorHandler := gasCommands.NewRunGasCoordinatorHandler(
		m,
		r.shipRepo,
		r.storageOpRepo,
		r.shipAssignmentRepo,
		r.daemonClient,
		r.waypointRepo,
		r.storageCoordinator,
	)
	if err := m.Register(
		reflect.TypeOf(&gasCommands.RunGasCoordinatorCommand{}),
		coordinatorHandler,
	); err != nil {
		return err
	}

	// Register RunSiphonWorkerCommand handler
	siphonHandler := gasCommands.NewRunSiphonWorkerHandler(
		m,
		r.shipRepo,
		r.shipAssignmentRepo,
		r.storageCoordinator,
		r.clock,
	)
	if err := m.Register(
		reflect.TypeOf(&gasCommands.RunSiphonWorkerCommand{}),
		siphonHandler,
	); err != nil {
		return err
	}

	// Register RunStorageShipWorkerCommand handler
	storageShipHandler := gasCommands.NewRunStorageShipWorkerHandler(
		m,
		r.shipRepo,
		r.shipAssignmentRepo,
		r.storageCoordinator,
	)
	if err := m.Register(
		reflect.TypeOf(&gasCommands.RunStorageShipWorkerCommand{}),
		storageShipHandler,
	); err != nil {
		return err
	}

	// Register TransferCargoCommand handler (used by siphon workers to deposit to storage)
	transferHandler := gasCommands.NewTransferCargoHandler(r.shipRepo, r.apiClient)
	if err := m.Register(
		reflect.TypeOf(&gasCommands.TransferCargoCommand{}),
		transferHandler,
	); err != nil {
		return err
	}

	return nil
}

// CreateConfiguredMediator creates a new mediator with all ledger handlers registered
//
// This is a convenience method that creates a mediator and registers all ledger handlers.
// Use this when you need a fully configured mediator for application use.
func (r *HandlerRegistry) CreateConfiguredMediator() (mediator.Mediator, error) {
	m := mediator.NewMediator()

	if err := r.RegisterLedgerHandlers(m); err != nil {
		return nil, err
	}

	// Register gas handlers if dependencies are available
	if r.storageOpRepo != nil && r.storageCoordinator != nil && r.waypointRepo != nil {
		if err := r.RegisterGasHandlers(m); err != nil {
			return nil, err
		}
	}

	return m, nil
}
