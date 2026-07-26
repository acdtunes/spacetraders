package types

import (
	"errors"
	"fmt"
	"testing"
)

// Wire form captured verbatim from a live daemon.log 4214 rejection.
const liveInTransitBody = `API error (status 400): {"error":{"code":4214,"message":"Ship is currently in-transit from X1-QR48-CE9A to X1-QR48-B16A and arrives in 201 seconds.","data":{"departureSymbol":"X1-QR48-CE9A","destinationSymbol":"X1-QR48-B16A","arrival":"2026-07-25T22:08:12.312Z","departureTime":"2026-07-25T22:04:45.312Z","secondsToArrival":201}}}`

func TestIsShipInTransitAPIError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"live 4214 wire form", errors.New(liveInTransitBody), true},
		{"wrapped live 4214", fmt.Errorf("failed to navigate ship: %w", errors.New(liveInTransitBody)), true},
		{"message-only in-transit", errors.New("Ship is currently in-transit from A to B and arrives in 5 seconds."), true},
		{"code-only 4214", errors.New(`API error (status 400): {"error":{"code":4214,"message":"Ship SHIP-1 arrival pending."}}`), true},
		{"4204 already at destination", errors.New(`API error (status 400): {"error":{"code":4204,"message":"Ship is currently located at the destination."}}`), false},
		{"4236 not in orbit", errors.New(`API error (status 400): {"error":{"code":4236,"message":"Ship is not currently in orbit."}}`), false},
		{"4244 not docked", errors.New(`API error (status 400): {"error":{"code":4244,"message":"Ship is not currently docked."}}`), false},
		{"unrelated error", errors.New("connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsShipInTransitAPIError(tc.err); got != tc.want {
				t.Fatalf("IsShipInTransitAPIError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The typed error must expose its original API rejection through errors
// unwrapping so existing string-form classifiers deeper in the chain keep
// seeing the raw 4214 body.
func TestErrShipInTransit_UnwrapPreservesCause(t *testing.T) {
	cause := errors.New(liveInTransitBody)
	err := &ErrShipInTransit{ShipSymbol: "SHIP-1", Destination: "X1-QR48-B16A", Cause: cause}

	if !errors.Is(err, cause) {
		t.Fatalf("expected errors.Is to reach the original cause through Unwrap")
	}
	if !IsShipInTransitAPIError(err) {
		t.Fatalf("expected the typed error's own message to still classify as an in-transit rejection")
	}
	var typed *ErrShipInTransit
	if !errors.As(fmt.Errorf("failed to dock for refuel: %w", err), &typed) {
		t.Fatalf("expected errors.As to find ErrShipInTransit through wrapping")
	}
}
