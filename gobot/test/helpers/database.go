package helpers

import (
	"testing"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// NewTestDB creates a new SQLite in-memory database for testing
func NewTestDB(t *testing.T) *gorm.DB {
	db, err := database.NewTestConnection()
	if err != nil {
		t.Fatalf("failed to create test database: %v", err)
	}

	// Cleanup after test
	if t != nil {
		t.Cleanup(func() {
			database.Close(db)
		})
	}

	return db
}
