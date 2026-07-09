package services

import (
	"fmt"
	"testing"
)

// sp-423c (gate hardening): isTransientDockStateError has two matching branches - the
// local precondition string ("must be docked", already exercised indirectly by
// production_executor_dock_race_test.go's synthesized "ship must be docked to perform
// cargo transactions") and the API's real numeric codes 4214/4244. The numeric-code
// branches - what actually fires when the game API itself, not this codebase's own
// precondition check, is the source of the error - had zero coverage: no test in this
// package ever constructs a real-API-shaped {"error":{"code":4214,...}} envelope. That
// is precisely the gap this bead exists to close (only the real API can exercise it,
// so a wrong substring here would pass the FAKE-backed gate and only break in captain
// LIVE acceptance).
//
// Fixture provenance: 4214/4244 are asserted by this function's own doc comment
// ("the API's 4214/4244 codes"), not independently re-captured raw responses.
func TestIsTransientDockStateError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "real 4214 API shape matches",
			err: fmt.Errorf("failed to purchase cargo: %w", fmt.Errorf(
				`API error (status 400): {"error":{"code":4214,"message":"Failed to update ship cargo. Ship TORWIND-1 must be docked to perform cargo transactions."}}`)),
			want: true,
		},
		{
			name: "real 4244 API shape matches",
			err: fmt.Errorf("failed to sell cargo: %w", fmt.Errorf(
				`API error (status 400): {"error":{"code":4244,"message":"Failed to update ship cargo. Ship TORWIND-1 must be docked to perform cargo transactions."}}`)),
			want: true,
		},
		{
			name: "local precondition string still matches",
			err:  fmt.Errorf("ship must be docked to perform cargo transactions"),
			want: true,
		},
		{
			name: "unrelated real API error (empty tranche 4602) must not false-positive",
			err: fmt.Errorf("failed to purchase cargo: %w", fmt.Errorf(
				`API error (status 400): {"error":{"message":"Market purchase failed. Trade good IRON is not available in that quantity.","code":4602}}`)),
			want: false,
		},
		{
			name: "unrelated real API error (insufficient funds 4600) must not false-positive",
			err: fmt.Errorf("failed to purchase cargo: %w", fmt.Errorf(
				`API error (status 400): {"error":{"message":"Purchase failed. Agent has insufficient funds.","code":4600}}`)),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientDockStateError(tt.err); got != tt.want {
				t.Fatalf("isTransientDockStateError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
