package helpers

import (
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// SharedTestDB is the singleton database instance used across all integration tests
var SharedTestDB *gorm.DB

// InitializeSharedTestDB creates and migrates the shared test database
// Called once in TestMain before running any tests
func InitializeSharedTestDB() error {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		return fmt.Errorf("failed to open shared test database: %w", err)
	}

	// Auto-migrate ALL models used across integration tests
	err = db.AutoMigrate(
		// Player models
		&persistence.PlayerModel{},

		// Market models
		&persistence.MarketData{},

		// Waypoint models
		&persistence.WaypointModel{},

		// Contract models
		&persistence.ContractModel{},

		// Container/Daemon models
		&persistence.ContainerModel{},
		&persistence.ContainerLogModel{},
		&persistence.ShipAssignmentModel{},

		// System graph models
		&persistence.SystemGraphModel{},
	)
	if err != nil {
		return fmt.Errorf("failed to migrate shared test database: %w", err)
	}

	SharedTestDB = db
	return nil
}

// TruncateAllTables clears all data from all tables
// Called before each scenario to ensure test isolation
func TruncateAllTables() error {
	if SharedTestDB == nil {
		return fmt.Errorf("shared test database not initialized")
	}

	// Truncate tables in order (respecting foreign key constraints)
	tables := []string{
		"contracts",
		"market_data",
		"waypoints",
		"ship_assignments",
		"container_logs",
		"containers",
		"system_graphs",
		"players",
	}

	for _, table := range tables {
		if err := SharedTestDB.Exec(fmt.Sprintf("DELETE FROM %s", table)).Error; err != nil {
			// Ignore "no such table" errors for optional tables
			continue
		}
	}

	return nil
}

// CloseSharedTestDB closes the shared database connection
// Called in TestMain after all tests complete
func CloseSharedTestDB() error {
	if SharedTestDB == nil {
		return nil
	}

	sqlDB, err := SharedTestDB.DB()
	if err != nil {
		return err
	}

	return sqlDB.Close()
}
