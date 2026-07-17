package database

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

func TestAutoMigrate_CreatesSystemCoordsTable(t *testing.T) {
	db, err := NewTestConnection()
	if err != nil {
		t.Fatalf("test connection: %v", err)
	}
	if err := AutoMigrate(db); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	if !db.Migrator().HasTable("system_coords") {
		t.Fatal("system_coords table not created")
	}
	for _, col := range []string{"era_id", "symbol", "x", "y", "fetched_at"} {
		if !db.Migrator().HasColumn(&persistence.SystemCoordModel{}, col) {
			t.Errorf("system_coords missing column %s", col)
		}
	}
}
