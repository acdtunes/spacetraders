package ship

import (
	"fmt"
	"testing"
)

// sp-423c (gate hardening): isRetryableRefuelError decides whether a refuel failure
// gets retried-with-backoff-then-rerouted (sp-vsfn's fix for the
// goods_factory-SHIP_PARTS-c7e2ecb2 crash) or surfaces immediately as unrecoverable.
// It had zero test coverage before this change despite guarding the exact defect
// class sp-vsfn was filed for.
//
// Fixture provenance: these are not raw SpaceTraders API bodies - they are this
// codebase's OWN client-layer wrapper strings (internal/adapters/api/retry_policy.go
// classifyResponse/classifyNetworkError/doWithRetry), which is what
// isRetryableRefuelError actually parses (its doc comment says as much: detection is
// "via substring match against the same messages retry_policy.go's final wrap
// produces"). Verified directly against retry_policy.go source, not guessed:
//   - "server error (%d)" for 5xx
//   - "rate limited (429)"
//   - "service unavailable (503)"
//   - "network error: %w"
//   - "max retries exceeded: %w" (wrapping any of the above)
//
// The sp-vsfn incident's own captured signature - "failed to refuel: ... max retries
// exceeded: server error (500)" - is used verbatim as the primary case.
func TestIsRetryableRefuelError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "sp-vsfn incident signature: max retries exceeded wrapping a server error",
			err:  fmt.Errorf("failed to refuel: %w", fmt.Errorf("max retries exceeded: server error (500)")),
			want: true,
		},
		{
			name: "bare server error 5xx",
			err:  fmt.Errorf("failed to refuel: %w", fmt.Errorf("server error (502)")),
			want: true,
		},
		{
			name: "rate limited 429",
			err:  fmt.Errorf("failed to refuel: %w", fmt.Errorf("rate limited (429)")),
			want: true,
		},
		{
			name: "service unavailable 503",
			err:  fmt.Errorf("failed to refuel: %w", fmt.Errorf("service unavailable (503)")),
			want: true,
		},
		{
			name: "network error",
			err:  fmt.Errorf("failed to refuel: %w", fmt.Errorf("network error: connection reset by peer")),
			want: true,
		},
		{
			name: "timeout",
			err:  fmt.Errorf("failed to refuel: context deadline exceeded: timeout"),
			want: true,
		},
		{
			// Reuses the exact 4600 "insufficient funds" shape already established as
			// a real fixture elsewhere in this codebase
			// (production_executor_empty_tranche_test.go's insufficientFundsError),
			// applied here to the refuel call site: a permanent/logic error must NOT
			// be retried, only rerouted-or-surfaced.
			name: "genuine permanent error (insufficient funds) must not be retried",
			err: fmt.Errorf("failed to refuel: %w", fmt.Errorf(
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
			if got := isRetryableRefuelError(tt.err); got != tt.want {
				t.Fatalf("isRetryableRefuelError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
