package database

import (
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// TestAutoMigrateCreatesManufacturingPipeline reproduces the missing-migration bug
// where ManufacturingPipelineModel was excluded from the AutoMigrate list, so the
// live table never gained the sequence_number column and every pipeline persist
// failed with "column sequence_number does not exist" (SQLSTATE 42703).
func TestAutoMigrateCreatesManufacturingPipeline(t *testing.T) {
	db, err := NewTestConnection()
	if err != nil {
		t.Fatalf("failed to create test connection: %v", err)
	}

	migrator := db.Migrator()

	if !migrator.HasTable(&persistence.ManufacturingPipelineModel{}) {
		t.Fatal("expected manufacturing_pipelines table to be created by AutoMigrate")
	}

	if !migrator.HasColumn(&persistence.ManufacturingPipelineModel{}, "sequence_number") {
		t.Fatal("expected manufacturing_pipelines.sequence_number column to be created by AutoMigrate")
	}
}

// TestAutoMigrate_GateConstructionColumnAdded_InvalidatesCacheOnce covers sp-8qhu:
// a pre-feature gate_edges row (no under_construction column) defaults to open, so
// a gate that was actually still building would be wrongly routed through until
// its 24h TTL expired. When AutoMigrate INTRODUCES the column it must invalidate
// the cache once (clear synced_at → next read is a miss → re-probe) — and a
// SECOND migrate must NOT re-clear freshly probed rows (restart-safe, RULING #2).
func TestAutoMigrate_GateConstructionColumnAdded_InvalidatesCacheOnce(t *testing.T) {
	db, err := NewConnection(&config.DatabaseConfig{Type: "sqlite", Path: ":memory:"})
	if err != nil {
		t.Fatalf("failed to create connection: %v", err)
	}

	// Simulate the pre-sp-8qhu schema: a gate_edges table WITHOUT the construction
	// column, carrying a fresh-looking row.
	if err := db.Exec(`CREATE TABLE gate_edges (
		system_symbol TEXT, connected_system TEXT, gate_waypoint TEXT,
		era_id INTEGER, synced_at TEXT,
		PRIMARY KEY (system_symbol, connected_system))`).Error; err != nil {
		t.Fatalf("failed to create legacy gate_edges table: %v", err)
	}
	if err := db.Exec(`INSERT INTO gate_edges (system_symbol, connected_system, gate_waypoint, synced_at)
		VALUES ('X1-KA42','X1-AF2','X1-AF2-I1', ?)`, time.Now().Format(time.RFC3339)).Error; err != nil {
		t.Fatalf("failed to seed legacy row: %v", err)
	}

	// First migrate: adds under_construction AND invalidates the pre-feature row.
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("first AutoMigrate failed: %v", err)
	}
	if !db.Migrator().HasColumn(&persistence.GateEdgeModel{}, "under_construction") {
		t.Fatal("AutoMigrate must add the under_construction column")
	}
	var syncedAt string
	if err := db.Raw(`SELECT synced_at FROM gate_edges WHERE connected_system='X1-AF2'`).Scan(&syncedAt).Error; err != nil {
		t.Fatalf("failed to read synced_at: %v", err)
	}
	if syncedAt != "" {
		t.Fatalf("the pre-feature row must be invalidated (synced_at cleared) for re-probe, got %q", syncedAt)
	}

	// Re-stamp the row as freshly probed, then migrate AGAIN: the column already
	// exists, so the one-time invalidation must NOT fire.
	if err := db.Exec(`UPDATE gate_edges SET synced_at=? WHERE connected_system='X1-AF2'`, time.Now().Format(time.RFC3339)).Error; err != nil {
		t.Fatalf("failed to re-stamp row: %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("second AutoMigrate failed: %v", err)
	}
	if err := db.Raw(`SELECT synced_at FROM gate_edges WHERE connected_system='X1-AF2'`).Scan(&syncedAt).Error; err != nil {
		t.Fatalf("failed to re-read synced_at: %v", err)
	}
	if syncedAt == "" {
		t.Fatal("a second migrate must not re-clear freshly probed rows (restart-safe)")
	}
}

// TestConcurrentQueriesSeeMigratedSchema reproduces a latent flakiness bug in the
// shared ":memory:" SQLite test harness: NewConnection only configures the
// stdlib connection pool for postgres, so a sqlite *gorm.DB is left with Go's
// default (unbounded) pool. Under concurrent access, database/sql opens more
// than one physical connection to serve simultaneous callers - and since the
// DSN is a bare ":memory:" (no cache=shared), each new physical connection is
// its own brand-new, empty SQLite database that never saw AutoMigrate. Callers
// unlucky enough to land on one of those connections see "no such table",
// exactly as observed intermittently in internal/adapters/grpc tests that spawn
// real ContainerRunners (which write and read the containers/container_logs
// tables from concurrent goroutines) alongside the test's own assertions.
func TestConcurrentQueriesSeeMigratedSchema(t *testing.T) {
	db, err := NewTestConnection()
	if err != nil {
		t.Fatalf("failed to create test connection: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("failed to get underlying sql.DB: %v", err)
	}

	const concurrency = 50

	var (
		wg    sync.WaitGroup
		start = make(chan struct{})
		mu    sync.Mutex
		errs  []error
	)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release all goroutines at once to maximize pool contention

			var count int64
			if scanErr := sqlDB.QueryRow("SELECT count(*) FROM manufacturing_pipelines").Scan(&count); scanErr != nil {
				mu.Lock()
				errs = append(errs, scanErr)
				mu.Unlock()
			}
		}()
	}

	close(start)
	wg.Wait()

	if len(errs) > 0 {
		t.Fatalf("expected all %d concurrent queries to see the migrated schema, got %d error(s); first: %v",
			concurrency, len(errs), errs[0])
	}
}
