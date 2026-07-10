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

	// sp-55aa: enforce foreign keys in the test harness by DEFAULT. SQLite leaves
	// PRAGMA foreign_keys OFF unless asked, so every real-DB test silently tolerated
	// FK violations that production Postgres rejects — that gap shipped sp-1hp9 DOA
	// (idle-arb wrote a ships.container_id claim before the container row existed →
	// FK 23503 in prod, green in every test). Enabled AFTER AutoMigrate: enforcement
	// during a migration that rebuilds tables could trip on transient states. The
	// sqlite pool is pinned to one physical connection (see NewConnection), so this
	// one pragma sticks for the connection's lifetime.
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		return nil, fmt.Errorf("failed to enable foreign key enforcement for test database: %w", err)
	}

	return db, nil
}

// AutoMigrate runs auto-migration for all models. It is additive (creates missing
// tables/columns/indexes, never destructive), and also performs one-time cache
// invalidations tied to a schema transition.
func AutoMigrate(db *gorm.DB) error {
	// sp-8qhu: detect whether the gate-graph construction column is being
	// introduced by THIS migration. Pre-sp-8qhu gate_edges rows predate
	// construction tracking and default to under_construction=false (open) — but a
	// gate that was actually still building at their sync time would be routed
	// through until the 24h TTL expired (the exact incident: KA42→AF2 unbuilt).
	// When the column is newly added, invalidate the gate_edges cache (clear
	// synced_at → next read is a miss → the re-fetch re-probes every edge's real
	// build state). Idempotent: fires only on the migration that adds the column,
	// and the gate graph is a pure cache, so this is safe and self-healing.
	gateConstructionColumnExisted := db.Migrator().HasColumn(&persistence.GateEdgeModel{}, "under_construction")

	if err := db.AutoMigrate(persistence.AllModels()...); err != nil {
		return err
	}

	if !gateConstructionColumnExisted {
		if err := db.Exec("UPDATE gate_edges SET synced_at = ''").Error; err != nil {
			return fmt.Errorf("failed to invalidate gate_edges cache for construction re-probe: %w", err)
		}
	}
	return nil
}

// Close closes the database connection
func Close(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
