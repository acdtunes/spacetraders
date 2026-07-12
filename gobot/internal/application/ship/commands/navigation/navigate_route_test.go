package navigation

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// TestShouldForceRefreshWaypoints pins the narrow trigger for the sp-g1g5
// auto-sync: force-refresh the system graph exactly once when the cache-first
// load is missing the origin, or is missing an IN-SYSTEM destination. A
// cross-system destination is excluded — a foreign-system waypoint will never
// appear in the origin system's graph, so force-refreshing would be a wasted
// API call (this is the sp-6zgs (f) design error already refused upstream).
func TestShouldForceRefreshWaypoints(t *testing.T) {
	const (
		system = "X1-AB12"
		origin = "X1-AB12-A1" // in-system origin
		inDest = "X1-AB12-B2" // in-system destination
		xsDest = "X1-ZZ99-C3" // cross-system destination
	)

	present := func(symbols ...string) map[string]*shared.Waypoint {
		m := make(map[string]*shared.Waypoint, len(symbols))
		for _, s := range symbols {
			m[s] = &shared.Waypoint{}
		}
		return m
	}

	tests := []struct {
		name        string
		waypoints   map[string]*shared.Waypoint
		destination string
		want        bool
	}{
		{
			name:        "origin missing -> force refresh",
			waypoints:   present(inDest), // origin absent
			destination: inDest,
			want:        true,
		},
		{
			name:        "in-system destination missing -> force refresh",
			waypoints:   present(origin), // in-system dest absent
			destination: inDest,
			want:        true,
		},
		{
			name:        "cross-system destination missing -> DO NOT refresh (excluded)",
			waypoints:   present(origin), // cross-system dest absent, but that's expected
			destination: xsDest,
			want:        false,
		},
		{
			name:        "all present -> no refresh (byte-identical normal nav)",
			waypoints:   present(origin, inDest),
			destination: inDest,
			want:        false,
		},
		{
			name:        "origin missing dominates even with cross-system dest",
			waypoints:   present(), // origin absent
			destination: xsDest,
			want:        true,
		},
		{
			name:        "origin present, cross-system dest present -> no refresh",
			waypoints:   present(origin, xsDest),
			destination: xsDest,
			want:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldForceRefreshWaypoints(tt.waypoints, origin, tt.destination, system)
			if got != tt.want {
				t.Errorf("shouldForceRefreshWaypoints(dest=%q) = %v, want %v", tt.destination, got, tt.want)
			}
		})
	}
}
