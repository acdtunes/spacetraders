package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// sp-pvw3 discovery_share: the pure split + alias-mapping functions the coordinator threads
// config → per-cycle post budget through. These are the mutation-checked core of the knob.

// frontierCapacitySplit divides one cycle's post-declaration capacity between DISCOVERY (chart
// virgin) and SCAN (drain the dark backlog) by the discovery_share ratio, then applies GRACEFUL
// DEGRADATION: a side with no work yields its whole budget to the side that has work, so capacity
// never idles while the other side has work.
func TestFrontierCapacitySplit(t *testing.T) {
	cases := []struct {
		name          string
		share         int
		capacity      int
		discoveryWork bool
		scanWork      bool
		wantDiscovery int
		wantScan      int
	}{
		// --- the ratio, both sides have work (the concurrent split) ---
		{"50/50 of an even capacity", 50, 10, true, true, 5, 5},
		{"60/40", 60, 10, true, true, 6, 4},
		{"80/20", 80, 10, true, true, 8, 2},
		{"100 = pure discovery", 100, 10, true, true, 10, 0},
		{"0 = pure backlog-scan", 0, 10, true, true, 0, 10},
		// rounding: 50% of an ODD capacity rounds to nearest (kills a truncation mutation)
		{"50% of 5 rounds discovery up, scan complements", 50, 5, true, true, 3, 2},
		{"clamp share > 100", 150, 10, true, true, 10, 0},
		{"clamp share < 0", -20, 10, true, true, 0, 10},
		// --- graceful degradation: a dry side yields its budget to the working side ---
		{"backlog empty → scan's share flows to discovery", 50, 10, true, false, 10, 0},
		{"no virgin frontier → discovery's share flows to scan", 50, 10, false, true, 0, 10},
		{"pure discovery but discovery dry → all to scan", 100, 10, false, true, 0, 10},
		{"pure scan but backlog dry → all to discovery", 0, 10, true, false, 10, 0},
		{"neither side has work → nothing declared", 50, 10, false, false, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			discovery, scan := frontierCapacitySplit(tc.share, tc.capacity, tc.discoveryWork, tc.scanWork)
			require.Equal(t, tc.wantDiscovery, discovery, "discovery budget")
			require.Equal(t, tc.wantScan, scan, "scan budget")
			// invariant when at least one side works: the split never invents or drops capacity.
			if tc.discoveryWork || tc.scanWork {
				require.Equal(t, tc.capacity, discovery+scan, "the split conserves total capacity when work exists")
			}
		})
	}
}

// resolveDiscoveryShare folds the discovery_share knob and the DEPRECATED scan_only alias into one
// effective share in [0,100]: discovery_share is authoritative when set (>0), else the deprecated
// scan_only maps its binary (1 → pure backlog-scan share 0), else the documented default.
func TestResolveDiscoveryShare(t *testing.T) {
	cases := []struct {
		name           string
		discoveryShare int
		scanOnly       int
		want           int
	}{
		{"nothing set → documented default", 0, 0, defaultDiscoveryShare},
		{"explicit share governs", 60, 0, 60},
		{"explicit 100 = pure discovery", 100, 0, 100},
		{"share clamps above 100", 150, 0, 100},
		{"deprecated scan_only=1 ↔ share 0 (pure backlog-scan)", 0, 1, 0},
		{"discovery_share wins over the deprecated scan_only", 60, 1, 60},
		{"scan_only=0 is inert (not pure-scan) → default", 0, 0, defaultDiscoveryShare},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, resolveDiscoveryShare(tc.discoveryShare, tc.scanOnly))
		})
	}
}
