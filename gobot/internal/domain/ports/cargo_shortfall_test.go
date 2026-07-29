package ports

import (
	"fmt"
	"testing"
)

// The 4219 envelope is parsed in exactly one place because several subsystems recover
// from it (the construction drain's phantom-cargo defer, the contract delivery cache
// reconcile, the sp-wbcil sell clamp). These pin the contract every one of them relies
// on: a count is returned ONLY for an unambiguous shortfall naming the good in
// question, and every other error leaves the caller's original verdict standing.

// shortfallBody is the API's real cargo-shortfall response body.
func shortfallBody(ship, good string, requested, onHand int) string {
	return fmt.Sprintf(
		`{"error":{"message":"Ship %s cargo does not contain %d unit(s) of %s. Ship has %d unit(s).","code":4219,`+
			`"data":{"shipSymbol":"%s","tradeSymbol":"%s","cargoUnits":%d,"unitsToRemove":%d}}}`,
		ship, requested, good, onHand, ship, good, onHand, requested)
}

func TestCargoShortfallUnits(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		good      string
		wantUnits int
		wantOK    bool
	}{
		{
			name: "the real incident: typed APIError wrapped the way SellCargo wraps it",
			err: fmt.Errorf("failed to sell cargo: %w",
				&APIError{StatusCode: 400, Body: shortfallBody("TORWIND-D7", "MACHINERY", 71, 11)}),
			good: "MACHINERY", wantUnits: 11, wantOK: true,
		},
		{
			// The shape the older classifiers in this codebase match on: the body
			// reached the caller as a plain fmt.Errorf, not a typed APIError.
			name: "untyped error carrying the same body still parses",
			err:  fmt.Errorf(`API error (status 400): %s`, shortfallBody("HAULER-7", "ADVANCED_CIRCUITRY", 80, 0)),
			good: "ADVANCED_CIRCUITRY", wantUnits: 0, wantOK: true,
		},
		{
			name: "an empty good accepts whichever good the payload names",
			err:  &APIError{StatusCode: 400, Body: shortfallBody("S", "FERTILIZER", 40, 9)},
			good: "", wantUnits: 9, wantOK: true,
		},
		{
			name: "a payload naming a DIFFERENT good says nothing about this one",
			err:  &APIError{StatusCode: 400, Body: shortfallBody("S", "FERTILIZER", 71, 11)},
			good: "MACHINERY", wantOK: false,
		},
		{
			name: "an absent cargoUnits is not a zero hold",
			err: &APIError{StatusCode: 400, Body: `{"error":{"code":4219,"message":"x",` +
				`"data":{"shipSymbol":"S","tradeSymbol":"MACHINERY"}}}`},
			good: "MACHINERY", wantOK: false,
		},
		{
			name: "a negative count is nonsense and is refused",
			err: &APIError{StatusCode: 400, Body: `{"error":{"code":4219,"message":"x",` +
				`"data":{"tradeSymbol":"MACHINERY","cargoUnits":-3}}}`},
			good: "MACHINERY", wantOK: false,
		},
		{
			name: "a different rejection code says nothing about the hold",
			err: &APIError{StatusCode: 400, Body: `{"error":{"code":4602,"message":"Market does not import MACHINERY.",` +
				`"data":{"tradeSymbol":"MACHINERY","cargoUnits":11}}}`},
			good: "MACHINERY", wantOK: false,
		},
		{
			name: "a body that is not JSON at all",
			err:  fmt.Errorf("max retries exceeded: connection reset"),
			good: "MACHINERY", wantOK: false,
		},
		{
			name: "malformed JSON",
			err:  &APIError{StatusCode: 400, Body: `{"error":{"code":4219,`},
			good: "MACHINERY", wantOK: false,
		},
		{
			name: "nil error", err: nil, good: "MACHINERY", wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			units, ok := CargoShortfallUnits(tt.err, tt.good)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (units %d)", ok, tt.wantOK, units)
			}
			if ok && units != tt.wantUnits {
				t.Fatalf("units = %d, want %d", units, tt.wantUnits)
			}
		})
	}
}

// THE VALUE OVER A SUBSTRING MATCH: an error whose text merely CONTAINS the digits
// 4219 — a ship symbol, a credit amount, a waypoint — is not a cargo shortfall. A bare
// strings.Contains(err, "4219") classifier answers yes to every case below, which is
// how a phantom-cargo recovery fires on an unrelated failure.
func TestIsCargoShortfall_DoesNotMatchIncidentalDigits(t *testing.T) {
	notShortfalls := []error{
		fmt.Errorf("failed to sell cargo: %w", &APIError{StatusCode: 400,
			Body: `{"error":{"code":4602,"message":"Ship SHIP-4219 cannot sell here."}}`}),
		fmt.Errorf("failed to purchase cargo: %w", &APIError{StatusCode: 400,
			Body: `{"error":{"code":4216,"message":"Insufficient credits: need 4219."}}`}),
		fmt.Errorf("navigate failed: waypoint X1-BT49-4219 is uncharted"),
	}
	for _, err := range notShortfalls {
		if IsCargoShortfall(err) {
			t.Fatalf("an error that merely contains the digits 4219 is not a cargo shortfall: %v", err)
		}
	}
}

// The boolean classifier accepts a genuine shortfall even when the payload carries no
// usable count — the case a caller that only needs "the cache was wrong, resync" cares
// about, and the one CargoShortfallUnits deliberately refuses.
func TestIsCargoShortfall_MatchesShortfallWithoutUsableCount(t *testing.T) {
	withCount := &APIError{StatusCode: 400, Body: shortfallBody("S", "MACHINERY", 71, 11)}
	withoutCount := &APIError{StatusCode: 400, Body: `{"error":{"code":4219,"message":"Ship has 0 unit(s) of FERTILIZER."}}`}

	if !IsCargoShortfall(withCount) || !IsCargoShortfall(withoutCount) {
		t.Fatal("both shapes are genuine cargo-shortfall rejections")
	}
	if _, ok := CargoShortfallUnits(withoutCount, "FERTILIZER"); ok {
		t.Fatal("a body with no cargoUnits states no count, so the units reader must refuse it")
	}
}
