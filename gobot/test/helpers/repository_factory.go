package helpers

import (
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/graph"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// TestRepositories holds all real repository instances for integration tests
type TestRepositories struct {
	DB                   *gorm.DB
	PlayerRepo           player.PlayerRepository
	WaypointRepo         system.WaypointRepository
	SystemGraphRepo      system.SystemGraphRepository
	ShipAssignmentRepo   *persistence.ShipAssignmentRepositoryGORM
	MarketRepo           *persistence.MarketRepositoryGORM
	ContractRepo         *persistence.GormContractRepository
	ContainerRepo        *persistence.ContainerRepositoryGORM
	ContainerLogRepo     *persistence.GormContainerLogRepository
	GraphBuilder         system.IGraphBuilder
	GraphService         *graph.GraphService
	ShipRepo             navigation.ShipRepository
	MiningOperationRepo  *persistence.MiningOperationRepository
}

// NewTestRepositories creates all real repository instances using shared test DB
// apiClient should be a MockAPIClient instance for testing
// clock is used for time-sensitive operations (usually a MockClock in tests)
func NewTestRepositories(apiClient ports.APIClient, clock shared.Clock) *TestRepositories {
	db := SharedTestDB

	// Create GORM repositories
	playerRepo := persistence.NewGormPlayerRepository(db)
	waypointRepo := persistence.NewGormWaypointRepository(db)
	systemGraphRepo := persistence.NewGormSystemGraphRepository(db)
	shipAssignmentRepo := persistence.NewShipAssignmentRepository(db)
	marketRepo := persistence.NewMarketRepository(db)
	contractRepo := persistence.NewGormContractRepository(db)
	containerRepo := persistence.NewContainerRepository(db)
	containerLogRepo := persistence.NewGormContainerLogRepository(db, clock)
	miningOperationRepo := persistence.NewMiningOperationRepository(db)

	// Create graph builder and service
	graphBuilder := api.NewGraphBuilder(apiClient, playerRepo, waypointRepo)
	graphService := graph.NewGraphService(systemGraphRepo, waypointRepo, graphBuilder)

	// Create API-based ship repository
	shipRepo := api.NewShipRepository(apiClient, playerRepo, waypointRepo, graphService)

	return &TestRepositories{
		DB:                  db,
		PlayerRepo:          playerRepo,
		WaypointRepo:        waypointRepo,
		SystemGraphRepo:     systemGraphRepo,
		ShipAssignmentRepo:  shipAssignmentRepo,
		MarketRepo:          marketRepo,
		ContractRepo:        contractRepo,
		ContainerRepo:       containerRepo,
		ContainerLogRepo:    containerLogRepo,
		GraphBuilder:        graphBuilder,
		GraphService:        graphService,
		ShipRepo:            shipRepo,
		MiningOperationRepo: miningOperationRepo,
	}
}
