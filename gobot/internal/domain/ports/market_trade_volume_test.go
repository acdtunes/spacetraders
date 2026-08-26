package ports

import (
	"fmt"
	"testing"
)

// The 4604 envelope is parsed in exactly one place because the limit it carries is the only
// authoritative statement about a market's depth in the exchange — a cached trade_volume is
// precisely what the rejection disproved.

// volumeRejection is the API's real rejection, wrapped as production wraps it: a typed
// *APIError inside the adapter's "failed to sell cargo: %w".
func volumeRejection(waypoint, good string, requested, limit int) error {
	body := fmt.Sprintf(
		`{"error":{"code":4604,"message":"Market transaction failed. Trade good %s has a limit of %d units per transaction.",`+
			`"data":{"waypointSymbol":"%s","tradeSymbol":"%s","units":%d,"tradeVolume":%d}}}`,
		good, limit, waypoint, good, requested, limit)
	return fmt.Errorf("failed to sell cargo: %w", &APIError{StatusCode: 400, Body: body})
}

func TestMarketTradeVolumeLimit(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		good      string
		wantLimit int
		wantOK    bool
	}{
		{
			name:      "the live rejection that stranded a 330-unit FOOD hold",
			err:       volumeRejection("X1-UG77-DE3F", "FOOD", 330, 300),
			good:      "FOOD",
			wantLimit: 300,
			wantOK:    true,
		},
		{
			name:      "an empty good accepts whichever good the payload names",
			err:       volumeRejection("X1-VM70-E14D", "FOOD", 350, 332),
			good:      "",
			wantLimit: 332,
			wantOK:    true,
		},
		{
			name:   "a limit stated for a different good says nothing about this one",
			err:    volumeRejection("X1-UG77-DE3F", "FERTILIZER", 330, 300),
			good:   "FOOD",
			wantOK: false,
		},
		{
			name:   "a different rejection code says nothing about the market's depth",
			err:    &APIError{StatusCode: 400, Body: `{"error":{"code":4219,"message":"x","data":{"tradeSymbol":"FOOD","tradeVolume":300}}}`},
			good:   "FOOD",
			wantOK: false,
		},
		{
			name:   "an absent tradeVolume must never read as a zero-depth market",
			err:    &APIError{StatusCode: 400, Body: `{"error":{"code":4604,"message":"x","data":{"tradeSymbol":"FOOD","units":330}}}`},
			good:   "FOOD",
			wantOK: false,
		},
		{
			name:   "a non-positive limit is unusable",
			err:    &APIError{StatusCode: 400, Body: `{"error":{"code":4604,"message":"x","data":{"tradeSymbol":"FOOD","tradeVolume":0}}}`},
			good:   "FOOD",
			wantOK: false,
		},
		{
			name:   "no recoverable body",
			err:    fmt.Errorf("max retries exceeded: connection reset"),
			good:   "FOOD",
			wantOK: false,
		},
		{
			name:   "nil error",
			err:    nil,
			good:   "FOOD",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limit, ok := MarketTradeVolumeLimit(tt.err, tt.good)
			if ok != tt.wantOK {
				t.Fatalf("ok = %t, want %t (limit %d)", ok, tt.wantOK, limit)
			}
			if ok && limit != tt.wantLimit {
				t.Fatalf("limit = %d, want %d", limit, tt.wantLimit)
			}
		})
	}
}

// A bare strings.Contains(err, "4604") classifier answers yes to every error below — a
// waypoint symbol, a credit amount, an unrelated code. Matching the PARSED code keeps
// them apart.
func TestMarketTradeVolumeLimit_DigitsAloneAreNotARejection(t *testing.T) {
	for _, err := range []error{
		&APIError{StatusCode: 400, Body: `{"error":{"code":4602,"message":"Ship cannot sell at X1-BT49-4604."}}`},
		&APIError{StatusCode: 400, Body: `{"error":{"code":4216,"message":"Insufficient credits: need 4604."}}`},
		fmt.Errorf("navigate failed: waypoint X1-BT49-4604 is uncharted"),
	} {
		if _, ok := MarketTradeVolumeLimit(err, "FOOD"); ok {
			t.Fatalf("an error that merely contains the digits 4604 is not a trade-volume rejection: %v", err)
		}
	}
}
