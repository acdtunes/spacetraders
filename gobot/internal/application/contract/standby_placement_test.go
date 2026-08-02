package contract

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakePlacementProvider serves the fixed ≤6 placement slots (or an error), standing in for the
// role-resolver-backed grpc provider the daemon wires.
type fakePlacementProvider struct {
	slots []string
	err   error
}

func (f *fakePlacementProvider) StandbyPlacement(_ context.Context, _ int) ([]string, error) {
	return f.slots, f.err
}

var _ StandbyPlacementProvider = (*fakePlacementProvider)(nil)

// ResolveStandbyForHoming resolves the standby slot set for the pass across its input variations:
//   - a live fleet-hub set present → kept as-is (operator pins win, RULINGS #2 no-thrash);
//   - an EMPTY hub set → auto-driven by the provider's FIXED ≤6 placement slots;
//   - nil provider / read error → fail OPEN: the passed set unchanged (never worse than before);
//   - empty hub AND no placement → stays empty (homing disabled — the safe no-op).
func TestResolveStandbyForHoming(t *testing.T) {
	tests := []struct {
		name     string
		provider StandbyPlacementProvider
		stations []string
		want     []string
	}{
		{
			name:     "operator pins win untouched",
			provider: &fakePlacementProvider{slots: []string{"X1-UM5-A", "X1-UM5-B"}},
			stations: []string{"X1-UM5-K83", "X1-UM5-G49"},
			want:     []string{"X1-UM5-K83", "X1-UM5-G49"},
		},
		{
			name:     "nil provider is byte-identical",
			provider: nil,
			stations: []string{"X1-UM5-K83"},
			want:     []string{"X1-UM5-K83"},
		},
		{
			name:     "provider error fails open (passed set unchanged)",
			provider: &fakePlacementProvider{err: errors.New("market read failed")},
			stations: nil,
			want:     nil,
		},
		{
			name:     "empty hub auto-driven by the fixed placement slots",
			provider: &fakePlacementProvider{slots: []string{"X1-UM5-H52", "X1-UM5-E43", "X1-UM5-K83"}},
			stations: nil,
			want:     []string{"X1-UM5-H52", "X1-UM5-E43", "X1-UM5-K83"},
		},
		{
			name:     "no hub and no placement stays disabled",
			provider: &fakePlacementProvider{slots: nil},
			stations: nil,
			want:     nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveStandbyForHoming(context.Background(), nil, tc.provider, 4, tc.stations)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("stations = %v, want %v", got, tc.want)
			}
		})
	}
}
