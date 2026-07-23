package contract

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakeDemandProvider serves a fixed per-park demand map (or an error), standing in for
// the role-resolver-backed grpc provider the daemon wires.
type fakeDemandProvider struct {
	demand map[string]float64
	err    error
}

func (f *fakeDemandProvider) StandbyDemand(_ context.Context, _ int) (map[string]float64, error) {
	return f.demand, f.err
}

var _ StandbyDemandProvider = (*fakeDemandProvider)(nil)

// ResolveStandbyForHoming attaches the per-park demand map to an already-resolved station
// set and applies the empty-set auto-fallback. The behavior across its input variations:
//   - nil provider / read error → fail OPEN: stations untouched, nil demand (uniform homing,
//     byte-identical to the pre-fix behavior — a positioning read, never a spend; RULINGS #4);
//   - a live fleet-hub set present → kept as-is (operator pins win, RULINGS #2 no-thrash) and
//     ranked by the demand map;
//   - an EMPTY fleet-hub set → auto-driven by the role central parks (the demand map's keys,
//     sorted) — the sp-bu6ma auto hub-placement that fixes the J59 pile-up with no manual pins;
//   - empty hub AND no demand → the set stays empty (homing disabled — the safe no-op).
func TestResolveStandbyForHoming(t *testing.T) {
	tests := []struct {
		name         string
		provider     StandbyDemandProvider
		stations     []string
		wantStations []string
		wantDemand   map[string]float64
	}{
		{
			name:         "nil provider is byte-identical",
			provider:     nil,
			stations:     []string{"X1-UM5-K83", "X1-UM5-G49"},
			wantStations: []string{"X1-UM5-K83", "X1-UM5-G49"},
			wantDemand:   nil,
		},
		{
			name:         "provider error fails open",
			provider:     &fakeDemandProvider{err: errors.New("market read failed")},
			stations:     []string{"X1-UM5-K83"},
			wantStations: []string{"X1-UM5-K83"},
			wantDemand:   nil,
		},
		{
			name:         "non-empty fleet hub kept and ranked",
			provider:     &fakeDemandProvider{demand: map[string]float64{"X1-UM5-G49": 340, "X1-UM5-K83": 260}},
			stations:     []string{"X1-UM5-K83", "X1-UM5-G49"},
			wantStations: []string{"X1-UM5-K83", "X1-UM5-G49"},
			wantDemand:   map[string]float64{"X1-UM5-G49": 340, "X1-UM5-K83": 260},
		},
		{
			name:         "empty fleet hub auto-driven by role parks (sorted)",
			provider:     &fakeDemandProvider{demand: map[string]float64{"X1-UM5-G49": 340, "X1-UM5-K83": 260, "X1-UM5-E43": 240}},
			stations:     nil,
			wantStations: []string{"X1-UM5-E43", "X1-UM5-G49", "X1-UM5-K83"},
			wantDemand:   map[string]float64{"X1-UM5-G49": 340, "X1-UM5-K83": 260, "X1-UM5-E43": 240},
		},
		{
			name:         "no hub and no demand stays disabled",
			provider:     &fakeDemandProvider{demand: map[string]float64{}},
			stations:     nil,
			wantStations: nil,
			wantDemand:   map[string]float64{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotStations, gotDemand := ResolveStandbyForHoming(context.Background(), nil, tc.provider, 4, tc.stations)

			if !reflect.DeepEqual(gotStations, tc.wantStations) {
				t.Fatalf("stations = %v, want %v", gotStations, tc.wantStations)
			}
			if !reflect.DeepEqual(gotDemand, tc.wantDemand) {
				t.Fatalf("demand = %v, want %v", gotDemand, tc.wantDemand)
			}
		})
	}
}
