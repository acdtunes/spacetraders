package market

import (
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-pf60: MarketPriceHistory has carried supply/activity fields since its
// introduction (commit 6ea64647, 2025-11-24) and migration 016 has had the
// columns since the table's creation - the tier-at-capture data was never
// actually missing. What WAS missing is dedicated coverage: this package had
// zero *_test.go files, so nothing locked in that tier fields survive
// construction/validation/round-trip. That matters because time-consistent
// tier tagging (vs. sp-hqrb's bug of tagging historical ladders with
// CURRENT market_data tiers) depends entirely on these fields staying intact
// on this entity.
func TestNewMarketPriceHistory_CapturesTierAtObservationTime(t *testing.T) {
	playerID := shared.MustNewPlayerID(1)
	supply := "LIMITED"
	activity := "WEAK"

	history, err := NewMarketPriceHistory(
		"X1-NK36-D39", "MEDICINE", playerID, 1900, 1950, &supply, &activity, 20,
	)
	if err != nil {
		t.Fatalf("NewMarketPriceHistory: %v", err)
	}

	if got := history.Supply(); got == nil || *got != "LIMITED" {
		t.Fatalf("Supply() = %v, want LIMITED", got)
	}
	if got := history.Activity(); got == nil || *got != "WEAK" {
		t.Fatalf("Activity() = %v, want WEAK", got)
	}
}

func TestNewMarketPriceHistory_NilTierAllowed(t *testing.T) {
	playerID := shared.MustNewPlayerID(1)

	history, err := NewMarketPriceHistory(
		"X1-NK36-D39", "MEDICINE", playerID, 1900, 1950, nil, nil, 20,
	)
	if err != nil {
		t.Fatalf("NewMarketPriceHistory with nil tier: %v", err)
	}
	if history.Supply() != nil {
		t.Fatalf("Supply() = %v, want nil (unknown tier - e.g. a pre-sp-pf60-style row)", history.Supply())
	}
	if history.Activity() != nil {
		t.Fatalf("Activity() = %v, want nil", history.Activity())
	}
}

func TestNewMarketPriceHistory_RejectsInvalidSupply(t *testing.T) {
	playerID := shared.MustNewPlayerID(1)
	bogus := "NOT_A_REAL_TIER"

	_, err := NewMarketPriceHistory(
		"X1-NK36-D39", "MEDICINE", playerID, 1900, 1950, &bogus, nil, 20,
	)
	if err != ErrInvalidSupply {
		t.Fatalf("err = %v, want ErrInvalidSupply", err)
	}
}

func TestNewMarketPriceHistory_RejectsInvalidActivity(t *testing.T) {
	playerID := shared.MustNewPlayerID(1)
	bogus := "NOT_A_REAL_ACTIVITY"

	_, err := NewMarketPriceHistory(
		"X1-NK36-D39", "MEDICINE", playerID, 1900, 1950, nil, &bogus, 20,
	)
	if err != ErrInvalidActivity {
		t.Fatalf("err = %v, want ErrInvalidActivity", err)
	}
}

// TestNewMarketPriceHistoryWithID_RoundTripsTierFieldsFromStorage exercises
// the exact constructor the persistence layer uses to rehydrate a row
// (GormMarketPriceHistoryRepository.modelToHistory), proving tier fields
// survive the load-from-database path alongside id/recordedAt.
func TestNewMarketPriceHistoryWithID_RoundTripsTierFieldsFromStorage(t *testing.T) {
	playerID := shared.MustNewPlayerID(1)
	supply := "SCARCE"
	activity := "RESTRICTED"
	recordedAt := time.Date(2026, 7, 9, 21, 28, 0, 0, time.UTC)

	history, err := NewMarketPriceHistoryWithID(
		42, "X1-NK36-D39", "MEDICINE", playerID, 1900, 1950, &supply, &activity, 20, recordedAt,
	)
	if err != nil {
		t.Fatalf("NewMarketPriceHistoryWithID: %v", err)
	}

	if history.ID() != 42 {
		t.Fatalf("ID() = %d, want 42", history.ID())
	}
	if got := history.Supply(); got == nil || *got != "SCARCE" {
		t.Fatalf("Supply() = %v, want SCARCE", got)
	}
	if got := history.Activity(); got == nil || *got != "RESTRICTED" {
		t.Fatalf("Activity() = %v, want RESTRICTED", got)
	}
	if !history.RecordedAt().Equal(recordedAt) {
		t.Fatalf("RecordedAt() = %v, want %v", history.RecordedAt(), recordedAt)
	}
}
