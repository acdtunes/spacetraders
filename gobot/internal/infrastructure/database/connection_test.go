package database

import (
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
