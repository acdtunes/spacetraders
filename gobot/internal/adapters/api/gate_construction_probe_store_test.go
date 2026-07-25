package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// probedGateAPI records which gates were read live.
type probedGateAPI struct {
	probed       []string
	underBuild   map[string]bool
	jumpGateCall int
}

func (f *probedGateAPI) GetJumpGate(ctx context.Context, sys, wp, tok string) (*domainPorts.JumpGateData, error) {
	f.jumpGateCall++
	return &domainPorts.JumpGateData{Symbol: wp}, nil
}

func (f *probedGateAPI) GetWaypoint(ctx context.Context, sys, wp, tok string) (*domainPorts.WaypointDetail, error) {
	f.probed = append(f.probed, wp)
	return &domainPorts.WaypointDetail{Symbol: wp, IsUnderConstruction: f.underBuild[wp]}, nil
}

func (f *probedGateAPI) CreateChart(ctx context.Context, shipSymbol, token string) error { return nil }

// The whole point, against the real store: when a system's edge set is re-probed
// because ONE neighbour is still under construction, only that neighbour costs an
// API request. Its already-built siblings — whose verdict cannot have changed — are
// answered from the record.
func TestGateConstructionProbe_OnlyTheUnbuiltNeighbourCostsARequest(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	repo := persistence.NewGormGateEdgeRepository(db)
	ctx := context.Background()

	// A prior refresh recorded four built neighbours and one still building.
	require.NoError(t, repo.Replace(ctx, "X1-KA42", []system.GateEdge{
		{ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51"},
		{ConnectedSystem: "X1-UQ16", GateWaypoint: "X1-UQ16-I12"},
		{ConnectedSystem: "X1-JP61", GateWaypoint: "X1-JP61-I33"},
		{ConnectedSystem: "X1-GQ92", GateWaypoint: "X1-GQ92-I77"},
		{ConnectedSystem: "X1-AF2", GateWaypoint: "X1-AF2-I90", UnderConstruction: true},
	}))

	inner := &probedGateAPI{underBuild: map[string]bool{"X1-AF2-I90": true}}
	probe := api.NewGateConstructionProbe(inner, repo)

	// The next refresh re-reads all five gates, exactly as the gate graph does.
	for _, gate := range []string{"X1-PA3-I51", "X1-UQ16-I12", "X1-JP61-I33", "X1-GQ92-I77", "X1-AF2-I90"} {
		detail, derr := probe.GetWaypoint(ctx, "X1-KA42", gate, "tok")
		require.NoError(t, derr)
		require.Equal(t, gate == "X1-AF2-I90", detail.IsUnderConstruction, "verdict must be preserved for %s", gate)
	}

	require.Equal(t, []string{"X1-AF2-I90"}, inner.probed,
		"only the under-construction gate may cost a live request")
}

// GUARD: once the record ages out, every gate is re-probed live again — the store
// short-circuit is bounded by the same freshness window the routing cache uses, so
// it can never pin a verdict indefinitely.
func TestGateConstructionProbe_ExpiredRecordReprobesEveryGate(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	require.NoError(t, db.Create(&persistence.EraModel{Name: "orion", AgentSymbol: "ORION", PlayerID: 1}).Error)

	ctx := context.Background()
	// A one-nanosecond window makes every stored row instantly past its bound.
	repo := persistence.NewGormGateEdgeRepository(db, persistence.WithFreshWindow(time.Nanosecond))
	require.NoError(t, repo.Replace(ctx, "X1-KA42", []system.GateEdge{
		{ConnectedSystem: "X1-PA3", GateWaypoint: "X1-PA3-I51"},
		{ConnectedSystem: "X1-UQ16", GateWaypoint: "X1-UQ16-I12"},
	}))

	inner := &probedGateAPI{underBuild: map[string]bool{}}
	probe := api.NewGateConstructionProbe(inner, repo)

	for _, gate := range []string{"X1-PA3-I51", "X1-UQ16-I12"} {
		_, derr := probe.GetWaypoint(ctx, "X1-KA42", gate, "tok")
		require.NoError(t, derr)
	}

	require.Equal(t, []string{"X1-PA3-I51", "X1-UQ16-I12"}, inner.probed,
		"an expired record must send every gate back to the live probe")
}
