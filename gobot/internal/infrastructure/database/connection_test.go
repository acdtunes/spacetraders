package database

import (
	"sync"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
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
//
// Before the fix: with enough concurrent queries this reliably surfaces a
// "no such table: manufacturing_pipelines" error on at least one goroutine.
// After the fix (pinning the sqlite pool to a single physical connection):
// every goroutine shares the one migrated connection and none can miss the
// schema.
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
