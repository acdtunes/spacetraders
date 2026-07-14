package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The element flag parser is the CLI's one piece of real input logic: "WAYPOINT" is an
// uncrewed slot, "WAYPOINT@SHIP" pins a hull, and an empty waypoint is rejected. These
// are the observable outcomes an operator depends on.
func TestParseClusterElementFlag(t *testing.T) {
	cases := []struct {
		raw          string
		wantWaypoint string
		wantShip     string
		wantErr      bool
	}{
		{raw: "X1-A-1", wantWaypoint: "X1-A-1", wantShip: ""},
		{raw: "X1-A-1@WH-1", wantWaypoint: "X1-A-1", wantShip: "WH-1"},
		{raw: " X1-A-1 @ WH-1 ", wantWaypoint: "X1-A-1", wantShip: "WH-1"},
		{raw: "@WH-1", wantErr: true},
		{raw: "", wantErr: true},
	}
	for _, tc := range cases {
		got, err := parseClusterElementFlag(tc.raw)
		if tc.wantErr {
			require.Error(t, err, "raw %q must be rejected", tc.raw)
			continue
		}
		require.NoError(t, err, "raw %q", tc.raw)
		require.Equal(t, tc.wantWaypoint, got.Waypoint)
		require.Equal(t, tc.wantShip, got.ShipSymbol)
	}
}

// buildClusterDTO assembles the four element classes from the repeatable flags — the
// shape the granular `cluster add` sends to the daemon.
func TestBuildClusterDTO(t *testing.T) {
	dto, err := buildClusterDTO("central",
		[]string{"X1-A-1@WH-1"},
		[]string{"X1-SRC-1@ST-1"},
		[]string{"X1-A-1@DH-1"},
		[]string{"X1-HUB-1"},
	)
	require.NoError(t, err)
	require.Equal(t, "central", dto.ID)
	require.Equal(t, []ClusterElementDTO{{Waypoint: "X1-A-1", ShipSymbol: "WH-1"}}, dto.Warehouses)
	require.Equal(t, []ClusterElementDTO{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-1"}}, dto.Stockers)
	require.Equal(t, []ClusterElementDTO{{Waypoint: "X1-A-1", ShipSymbol: "DH-1"}}, dto.DeliveryHulls)
	require.Equal(t, []ClusterElementDTO{{Waypoint: "X1-HUB-1", ShipSymbol: ""}}, dto.SourceHubs)
}

// The apply spec file parses in both accepted shapes — the wrapped {"clusters":[...]}
// object and a bare [...] array — into the same clusters an operator expects to apply.
func TestLoadClusterSpecFile_BothShapes(t *testing.T) {
	dir := t.TempDir()
	wrapped := filepath.Join(dir, "wrapped.json")
	require.NoError(t, os.WriteFile(wrapped, []byte(`{"clusters":[{"id":"central","warehouses":[{"waypoint":"X1-A-1","ship_symbol":"WH-1"}]}]}`), 0o600))
	bare := filepath.Join(dir, "bare.json")
	require.NoError(t, os.WriteFile(bare, []byte(`[{"id":"central","warehouses":[{"waypoint":"X1-A-1","ship_symbol":"WH-1"}]}]`), 0o600))

	for _, path := range []string{wrapped, bare} {
		clusters, err := loadClusterSpecFile(path)
		require.NoError(t, err, "path %s", path)
		require.Len(t, clusters, 1)
		require.Equal(t, "central", clusters[0].ID)
		require.Equal(t, "X1-A-1", clusters[0].Warehouses[0].Waypoint)
		require.Equal(t, "WH-1", clusters[0].Warehouses[0].ShipSymbol)
	}
}

// A mistyped --role is rejected client-side before any RPC.
func TestRequireClusterRole(t *testing.T) {
	for _, role := range []string{"warehouse", "stocker", "delivery-hull", "source-hub"} {
		require.NoError(t, requireClusterRole(role))
	}
	require.Error(t, requireClusterRole("bogus"))
	require.Error(t, requireClusterRole(""))
}
