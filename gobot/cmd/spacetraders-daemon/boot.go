package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/daemonlock"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/pidfile"
	"gorm.io/gorm"
)

const advisoryLockAcquireTimeout = 10 * time.Second

func acquirePIDLockOrExit(pf *pidfile.PIDFile, force bool) {
	err := pf.Acquire()
	if err == nil {
		return
	}
	if !force {
		log.Fatalf("Failed to acquire PID file lock: %v\nUse --force to kill the existing daemon", err)
	}

	fmt.Println("Force mode enabled - attempting to kill existing daemon...")
	if killErr := pf.KillExisting(); killErr != nil {
		log.Fatalf("Failed to kill existing daemon: %v", killErr)
	}
	fmt.Println("Existing daemon killed")

	if err := pf.Acquire(); err != nil {
		log.Fatalf("Failed to acquire PID file lock after killing existing daemon: %v", err)
	}
}

func openDatabase(cfg *config.DatabaseConfig) (*gorm.DB, error) {
	fmt.Printf("Connecting to %s database...\n", cfg.Type)
	db, err := database.NewConnection(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}
	fmt.Println("Database connected")
	return db, nil
}

// Reconcile schema on startup: models are the source of truth, and
// AutoMigrate is additive (creates missing tables/columns/indexes, never
// destructive) — closes the gap where a merged model change passes tests
// (which AutoMigrate the in-memory SQLite) but breaks production Postgres
// for lack of a hand-written migration. Non-fatal: a healthy earner must
// not be blocked by a migration quirk — log loudly and continue.
func reconcileSchema(db *gorm.DB) {
	if err := database.AutoMigrate(db); err != nil {
		fmt.Printf("WARNING: schema AutoMigrate failed (continuing on existing schema): %v\n", err)
		return
	}
	fmt.Println("Schema reconciled (AutoMigrate)")
}

func acquirePlayerAdvisoryLock(db *gorm.DB, playerID int) (*daemonlock.PostgresPlayerLock, error) {
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying db for advisory lock: %w", err)
	}
	lockCtx, lockCancel := context.WithTimeout(context.Background(), advisoryLockAcquireTimeout)
	playerLock, err := daemonlock.NewPostgresPlayerLock(lockCtx, sqlDB)
	if err != nil {
		lockCancel()
		return nil, fmt.Errorf("failed to create daemon advisory lock: %w", err)
	}
	if err := daemonlock.AcquireExclusive(lockCtx, playerLock, playerID); err != nil {
		lockCancel()
		_ = playerLock.Close()
		return nil, err
	}
	lockCancel()
	return playerLock, nil
}
