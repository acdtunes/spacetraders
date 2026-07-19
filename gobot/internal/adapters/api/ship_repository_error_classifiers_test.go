package api

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// (gate hardening): isAlreadyDockedError and isAlreadyInOrbitError are the
// idempotency guards for Dock()/Orbit() (ship_repository.go:307,340) — every daemon
// dock/orbit call trusts them to tell a genuine failure apart from "already in the
// requested state, treat as success". Both had zero test coverage before this change,
// so a real-API response shape that didn't match their substring checks would have
// surfaced only in captain LIVE acceptance against the real API, never in the gate.
//
// Fixture provenance: the 4237 code/message is asserted by ship_repository.go's own
// long-standing comment ("API returns error code 4237 when trying to dock an already
// docked ship"), not an independently re-captured raw response like the 4204/4219
// fixtures elsewhere in this codebase (navigate_direct_test.go, jump_ship_test.go) —
// documented here so a future reader knows which fixtures are byte-exact captures and
// which are codebase-asserted. The real envelope shape ({"error":{"code":N,"message":
// "..."}})  and the "failed to X ship: %w" wrapping are independently verified from
// client.go's OrbitShip/DockShip and the request()/requestWithErrorParsing chain.

func TestIsAlreadyDockedError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "real 4237 API shape matches",
			err: fmt.Errorf("failed to dock ship: %w", fmt.Errorf(
				`API error (status 400): {"error":{"code":4237,"message":"Failed to dock ship. Ship TORWIND-1 is already docked."}}`)),
			want: true,
		},
		{
			name: "bare numeric code without the message wording still matches",
			err:  fmt.Errorf("failed to dock ship: %w", fmt.Errorf(`API error (status 400): {"error":{"code":4237,"message":"unexpected wording"}}`)),
			want: true,
		},
		{
			name: "unrelated real API error must not false-positive",
			err: fmt.Errorf("failed to dock ship: %w", fmt.Errorf(
				`API error (status 400): {"error":{"code":4214,"message":"Failed to dock ship. Ship TORWIND-1 must be stationary to dock."}}`)),
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
			require.Equal(t, tt.want, isAlreadyDockedError(tt.err))
		})
	}
}

func TestIsAlreadyInOrbitError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "real already-in-orbit message matches",
			err: fmt.Errorf("failed to orbit ship: %w", fmt.Errorf(
				`API error (status 400): {"error":{"code":4230,"message":"Failed to orbit ship. Ship TORWIND-1 is already in orbit."}}`)),
			want: true,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		// Contract-mismatch regression: before this change the classifier fell back to
		// a bare strings.Contains(msg, "in orbit"), which is a strict superset of
		// "already in orbit" and therefore redundant for the true-positive case AND
		// dangerous for any OTHER real API error that merely mentions orbit as a
		// precondition rather than reporting the idempotent-collision this guard
		// exists for. Dock()/Orbit() (ship_repository.go:340) treats a true result as
		// "not a real error, proceed as success" - swallowing a genuine failure here
		// is a silent-failure contract mismatch of exactly the class this bead exists
		// to catch pre-deploy. No independently-captured real message for this precise
		// wording exists yet, but it is a structurally realistic precondition-style
		// API error (compare the real, captured 4214 "must be docked" family).
		{
			name: "unrelated precondition error mentioning orbit must not false-positive",
			err: fmt.Errorf("failed to jettison cargo: %w", fmt.Errorf(
				`API error (status 400): {"error":{"code":4214,"message":"Failed to jettison cargo. Ship TORWIND-1 must be in orbit to jettison cargo."}}`)),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isAlreadyInOrbitError(tt.err))
		})
	}
}
