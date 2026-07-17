package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// sp-pvw3 `frontier status` formatter: pure over the wire response, so the one-view rendering is
// unit-tested without a daemon.

func sampleFrontierStatus() *pb.GetFrontierStatusResponse {
	return &pb.GetFrontierStatusResponse{
		ContainerId:       "frontier_expansion_coordinator-player-1-abc",
		DiscoveryShare:    60,
		ScanShare:         40,
		SplitSummary:      "60% discover / 40% scan",
		VirginQueueDepth:  7,
		DarkSystems:       5,
		DarkMarketplaces:  47,
		ProbeFleet:        12,
		ProbeCap:          40,
		ProbesIdle:        3,
		PostsInFlight:     4,
		LastBuyPrice:      25000,
		LastBuyAgeSeconds: 180,
		Blockers:          []string{"purchase cooldown active (3m0s of 10m0s elapsed)"},
	}
}

// The table shows the split, discovery depth, the dark-market backlog (systems + marketplaces), probe
// allocation, the last buy, and blockers — the full one-view the bead asks for.
func TestFormatFrontierStatus_Table(t *testing.T) {
	msg, err := formatFrontierStatus(sampleFrontierStatus(), false)
	require.NoError(t, err)

	require.Contains(t, msg, "60% discover / 40% scan", "the effective split is the headline")
	require.Contains(t, msg, "7 reachable virgin", "discovery frontier depth")
	require.Contains(t, msg, "5 dark-market system(s), 47 unscanned marketplace(s)", "the honest dark-market backlog count")
	require.Contains(t, msg, "12/40 satellites (3 idle), 4 post(s) in flight", "probe allocation + posts in flight")
	require.Contains(t, msg, "25000 cr, 3m0s ago", "last probe buy price + age")
	require.Contains(t, msg, "purchase cooldown active", "blockers are surfaced")
}

// A never-bought coordinator and an unblocked one render honestly, not as empty/garbled fields.
func TestFormatFrontierStatus_NoBuyAndNoBlockers(t *testing.T) {
	resp := sampleFrontierStatus()
	resp.LastBuyAgeSeconds = -1 // never bought
	resp.Blockers = nil

	msg, err := formatFrontierStatus(resp, false)
	require.NoError(t, err)
	require.Contains(t, msg, "Last buy:   none recorded")
	require.Contains(t, msg, "Blockers:   none")
}

// --json emits a machine-readable object carrying every field for scripts.
func TestFormatFrontierStatus_JSON(t *testing.T) {
	msg, err := formatFrontierStatus(sampleFrontierStatus(), true)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(msg), &parsed), "output must be valid JSON")
	// encoding/json on a proto message uses the snake_case `json:` struct tags.
	require.Equal(t, "60% discover / 40% scan", parsed["split_summary"])
	require.EqualValues(t, 5, parsed["dark_systems"])
	require.EqualValues(t, 47, parsed["dark_marketplaces"])
	require.True(t, strings.Contains(msg, "blockers"), "blockers are present in the JSON")
}
