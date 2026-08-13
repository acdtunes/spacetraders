package ports

import (
	"errors"
	"fmt"
	"testing"
)

// IsNotAtShipyard must key off the PARSED code, so an unrelated rejection that
// merely contains the digits is not mistaken for the shipyard refusal.
func TestIsNotAtShipyard(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "the 4263 refusal, wrapped the way the adapter wraps it",
			err:  fmt.Errorf("failed to scrap ship: %w", &APIError{StatusCode: 400, Body: `{"error":{"message":"Ship is not docked at a waypoint with a shipyard.","code":4263}}`}),
			want: true,
		},
		{
			name: "a different API rejection",
			err:  &APIError{StatusCode: 400, Body: `{"error":{"message":"Ship is not docked.","code":4214}}`},
			want: false,
		},
		{
			name: "prose that merely contains the digits",
			err:  errors.New("hull TORWIND-4263 is unreachable"),
			want: false,
		},
		{
			name: "an unparseable body",
			err:  &APIError{StatusCode: 400, Body: "not json at all"},
			want: false,
		},
		{
			name: "no error",
			err:  nil,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNotAtShipyard(tc.err); got != tc.want {
				t.Fatalf("IsNotAtShipyard(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
