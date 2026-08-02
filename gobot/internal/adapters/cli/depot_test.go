package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// `depot start <name>` resolves the named depot out of the multi-depot spec file: the
// matching depot is returned, and a name the spec does not define is a loud error (never
// a silent no-op that would start nothing).
func TestSelectDepotFromSpec(t *testing.T) {
	spec := []DepotDTO{
		{ID: "central", Warehouses: []DepotElementDTO{{Waypoint: "X1-A-1", ShipSymbol: "WH-1"}}},
		{ID: "outpost", Warehouses: []DepotElementDTO{{Waypoint: "X1-B-2", ShipSymbol: "WH-2"}}},
	}

	got, err := selectDepotFromSpec(spec, "outpost")
	require.NoError(t, err)
	require.Equal(t, "outpost", got.ID, "the named depot is selected out of the spec")
	require.Equal(t, "WH-2", got.Warehouses[0].ShipSymbol, "its full topology comes back")

	_, err = selectDepotFromSpec(spec, "ghost")
	require.Error(t, err, "a depot name absent from the spec must be rejected")
	require.Contains(t, err.Error(), "ghost", "the error names the missing depot")
}

// The element flag parser is the CLI's one piece of real input logic: "WAYPOINT" is an
// uncrewed slot, "WAYPOINT@SHIP" pins a hull, and an empty waypoint is rejected. These
// are the observable outcomes an operator depends on.
func TestParseDepotElementFlag(t *testing.T) {
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
		got, err := parseDepotElementFlag(tc.raw)
		if tc.wantErr {
			require.Error(t, err, "raw %q must be rejected", tc.raw)
			continue
		}
		require.NoError(t, err, "raw %q", tc.raw)
		require.Equal(t, tc.wantWaypoint, got.Waypoint)
		require.Equal(t, tc.wantShip, got.ShipSymbol)
	}
}

// buildDepotDTO assembles the four element classes from the repeatable flags — the
// shape the granular `depot add` sends to the daemon.
func TestBuildDepotDTO(t *testing.T) {
	dto, err := buildDepotDTO("central",
		[]string{"X1-A-1@WH-1"},
		[]string{"X1-SRC-1@ST-1"},
		[]string{"X1-A-1@DH-1"},
		[]string{"X1-HUB-1"},
	)
	require.NoError(t, err)
	require.Equal(t, "central", dto.ID)
	require.Equal(t, []DepotElementDTO{{Waypoint: "X1-A-1", ShipSymbol: "WH-1"}}, dto.Warehouses)
	require.Equal(t, []DepotElementDTO{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-1"}}, dto.Stockers)
	require.Equal(t, []DepotElementDTO{{Waypoint: "X1-A-1", ShipSymbol: "DH-1"}}, dto.DeliveryHulls)
	require.Equal(t, []DepotElementDTO{{Waypoint: "X1-HUB-1", ShipSymbol: ""}}, dto.SourceHubs)
}

// The apply spec file parses in both accepted shapes — the wrapped {"depots":[...]}
// object and a bare [...] array — into the same depots an operator expects to apply.
func TestLoadDepotSpecFile_BothShapes(t *testing.T) {
	dir := t.TempDir()
	wrapped := filepath.Join(dir, "wrapped.json")
	require.NoError(t, os.WriteFile(wrapped, []byte(`{"depots":[{"id":"central","warehouses":[{"waypoint":"X1-A-1","ship_symbol":"WH-1"}]}]}`), 0o600))
	bare := filepath.Join(dir, "bare.json")
	require.NoError(t, os.WriteFile(bare, []byte(`[{"id":"central","warehouses":[{"waypoint":"X1-A-1","ship_symbol":"WH-1"}]}]`), 0o600))

	for _, path := range []string{wrapped, bare} {
		depots, err := loadDepotSpecFile(path)
		require.NoError(t, err, "path %s", path)
		require.Len(t, depots, 1)
		require.Equal(t, "central", depots[0].ID)
		require.Equal(t, "X1-A-1", depots[0].Warehouses[0].Waypoint)
		require.Equal(t, "WH-1", depots[0].Warehouses[0].ShipSymbol)
	}
}

// fakeDepotElementClient captures the element requests without a daemon; the embedded
// interface carries the DaemonServiceClient methods these two calls never touch.
type fakeDepotElementClient struct {
	pb.DaemonServiceClient
	addReq   *pb.AddDepotElementRequest
	placeReq *pb.PlaceDepotElementRequest
}

func (f *fakeDepotElementClient) AddDepotElement(_ context.Context, in *pb.AddDepotElementRequest, _ ...grpc.CallOption) (*pb.DepotElementResponse, error) {
	f.addReq = in
	return &pb.DepotElementResponse{}, nil
}

func (f *fakeDepotElementClient) PlaceDepotElement(_ context.Context, in *pb.PlaceDepotElementRequest, _ ...grpc.CallOption) (*pb.DepotElementResponse, error) {
	f.placeReq = in
	return &pb.DepotElementResponse{}, nil
}

// The Element -> proto mapping is the operator-facing half: --waypoint must reach the
// request's waypoint and --ship its ship_symbol, for BOTH verbs, whose request messages
// declare those two strings in OPPOSITE order. Transposing them compiles, so pin per field.
func TestDepotElementClientVerbs_MapElementOntoTheRequest(t *testing.T) {
	fake := &fakeDepotElementClient{}
	client := &DaemonClient{client: fake}
	ctx := context.Background()

	require.NoError(t, client.AddDepotElement(ctx, 7, "", "central", "stocker",
		depot.Element{Waypoint: "X1-CLI-SRC", ShipSymbol: "CLI-ST"}))
	require.Equal(t, "X1-CLI-SRC", fake.addReq.Waypoint, "--waypoint reaches add_depot_element.waypoint")
	require.Equal(t, "CLI-ST", fake.addReq.ShipSymbol, "--ship reaches add_depot_element.ship_symbol")
	require.Equal(t, "central", fake.addReq.DepotId)
	require.Equal(t, "stocker", fake.addReq.Role)

	require.NoError(t, client.PlaceDepotElement(ctx, 7, "", "central", "delivery-hull",
		depot.Element{Waypoint: "X1-CLI-WH", ShipSymbol: "CLI-DH"}))
	require.Equal(t, "X1-CLI-WH", fake.placeReq.Waypoint, "--waypoint reaches place_depot_element.waypoint")
	require.Equal(t, "CLI-DH", fake.placeReq.ShipSymbol, "--ship reaches place_depot_element.ship_symbol")
	require.Equal(t, "central", fake.placeReq.DepotId)
	require.Equal(t, "delivery-hull", fake.placeReq.Role)
}

// A mistyped --role is rejected client-side before any RPC.
func TestRequireDepotRole(t *testing.T) {
	for _, role := range []string{"warehouse", "stocker", "delivery-hull", "source-hub"} {
		require.NoError(t, requireDepotRole(role))
	}
	require.Error(t, requireDepotRole("bogus"))
	require.Error(t, requireDepotRole(""))
}
