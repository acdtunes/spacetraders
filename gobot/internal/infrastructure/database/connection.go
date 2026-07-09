package database

import (
	"fmt"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// NewConnection creates a new database connection using the new config system
func NewConnection(cfg *config.DatabaseConfig) (*gorm.DB, error) {
	var dialector gorm.Dialector

	switch cfg.Type {
	case "postgres":
		// Use URL if provided, otherwise build DSN from individual fields
		var dsn string
		if cfg.URL != "" {
			dsn = cfg.URL
		} else {
			dsn = fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
				cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Name, cfg.SSLMode)
		}
		dialector = postgres.Open(dsn)

	case "sqlite":
		// Use Path for SQLite (can be file path or ":memory:")
		path := cfg.Path
		if path == "" {
			path = ":memory:"
		}
		dialector = sqlite.Open(path)

	default:
		return nil, fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure connection pool (only for PostgreSQL)
	if cfg.Type == "postgres" {
		sqlDB, err := db.DB()
		if err != nil {
			return nil, fmt.Errorf("failed to get underlying db: %w", err)
		}

		sqlDB.SetMaxOpenConns(cfg.Pool.MaxOpen)
		sqlDB.SetMaxIdleConns(cfg.Pool.MaxIdle)
		sqlDB.SetConnMaxLifetime(cfg.Pool.MaxLifetime)
	}

	// SQLite has no true concurrent-writer support (it serializes writes at the
	// file-lock level regardless), and a bare ":memory:" DSN gives each physical
	// connection its OWN separate, empty database unless cache=shared is set. Left
	// at Go's default (unbounded) pool, concurrent callers can open more than one
	// physical connection and land on one that never saw AutoMigrate, surfacing as
	// intermittent "no such table" errors. Pinning to a single physical connection
	// makes every caller share the one migrated connection, for :memory: and
	// file-based SQLite alike.
	if cfg.Type == "sqlite" {
		sqlDB, err := db.DB()
		if err != nil {
			return nil, fmt.Errorf("failed to get underlying db: %w", err)
		}

		sqlDB.SetMaxOpenConns(1)
	}

	return db, nil
}

// NewTestConnection creates an in-memory SQLite database for testing
func NewTestConnection() (*gorm.DB, error) {
	cfg := &config.DatabaseConfig{
		Type: "sqlite",
		Path: ":memory:",
	}

	db, err := NewConnection(cfg)
	if err != nil {
		return nil, err
	}

	// Auto-migrate for tests
	if err := AutoMigrate(db); err != nil {
		return nil, fmt.Errorf("failed to auto-migrate test database: %w", err)
	}

	return db, nil
}

// AutoMigrate runs auto-migration for all models (for tests)
func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(persistence.AllModels()...)
}

// Close closes the database connection
func Close(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
