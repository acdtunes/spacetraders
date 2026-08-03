package grpc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// eraWaypointsPath points at the SHARED era fixtures — REAL rows of the daemon's waypoints
// table for the three measured home systems (see the domain package's testdata/README.md).
func eraWaypointsPath(era string) string {
	return filepath.Join("..", "..", "domain", "contractscaler", "testdata", era+"_waypoints.json")
}

// loadEraWaypoints reads one era's REAL charted rows into the *shared.Waypoint records the
// waypoint store hands the resolver — the exact surface production reads. NO market rows are
// supplied alongside: an era template that only resolves after a dock scan is not era-invariant,
// and a cold coordinator must still park its hulls correctly.
func loadEraWaypoints(t *testing.T, era string) []*shared.Waypoint {
	t.Helper()
	raw, err := os.ReadFile(eraWaypointsPath(era))
	require.NoError(t, err, "read %s fixture", era)

	var rows []struct {
		Symbol  string   `json:"symbol"`
		Type    string   `json:"type"`
		X       float64  `json:"x"`
		Y       float64  `json:"y"`
		Traits  []string `json:"traits"`
		HasFuel bool     `json:"has_fuel"`
	}
	require.NoError(t, json.Unmarshal(raw, &rows), "decode %s fixture", era)
	require.NotEmpty(t, rows, "%s fixture is empty — the fixture, not the provider, is broken", era)

	waypoints := make([]*shared.Waypoint, 0, len(rows))
	for _, row := range rows {
		waypoints = append(waypoints, &shared.Waypoint{
			Symbol:       row.Symbol,
			SystemSymbol: shared.ExtractSystemSymbol(row.Symbol),
			Type:         row.Type,
			X:            row.X,
			Y:            row.Y,
			Traits:       row.Traits,
			HasFuel:      row.HasFuel,
		})
	}
	return waypoints
}

func eraPlacementProvider(waypoints []*shared.Waypoint) *contractStandbyPlacementProvider {
	system := ""
	if len(waypoints) > 0 {
		system = waypoints[0].SystemSymbol
	}
	return &contractStandbyPlacementProvider{resolver: &contractScalerRoleResolver{
		home:      &fakeHomeReader{system: system, readable: true},
		waypoints: &fakeWaypointLister{waypoints: waypoints},
		markets:   &fakeMarketReader{byWaypoint: map[string]*market.Market{}},
	}}
}

// THE END-TO-END ACCEPTANCE PROOF (sp-9suun) through the coordinator's driving port: REAL rows
// from three eras with three different home systems and three different numberings all place
// the SAME four physical anchors in the SAME placement order — (1) H-stack, (2) far sink,
// (3) far source base, (4) E-stack — followed by the demand-ranked central fill up to the knee.
//
// This runs the whole production pipeline (waypoint store → durable charted facts → ResolveRoles
// → coord-dedup → TopDeliverySlots), so it is also the guard on the WIRING: drop Type, Traits or
// HasFuel from the adapter's WaypointMarket and every anchor silently vanishes into the central
// fallback, which this test sees.
func TestContractStandbyPlacementProvider_RealEraRowsPlaceTheFourAnchorsFirst(t *testing.T) {
	for _, tc := range []struct {
		era     string
		anchors []string
	}{
		{"era3", []string{"X1-VB74-H51", "X1-VB74-J58", "X1-VB74-B7", "X1-VB74-E44"}},
		{"era4", []string{"X1-UM5-H52", "X1-UM5-J59", "X1-UM5-B7", "X1-UM5-E42"}},
		{"era5", []string{"X1-KP23-H49", "X1-KP23-J56", "X1-KP23-B7", "X1-KP23-E42"}},
	} {
		t.Run(tc.era, func(t *testing.T) {
			waypoints := loadEraWaypoints(t, tc.era)

			got, err := eraPlacementProvider(waypoints).StandbyPlacement(context.Background(), 1)
			require.NoError(t, err)

			require.GreaterOrEqual(t, len(got), len(tc.anchors), "slots = %v", got)
			require.Equal(t, tc.anchors, got[:len(tc.anchors)],
				"the four era-invariant anchors must lead the slot set, got %v", got)

			// One hull per LOCATION: no two slots share a coordinate.
			coordOf := map[string][2]float64{}
			for _, waypoint := range waypoints {
				coordOf[waypoint.Symbol] = [2]float64{waypoint.X, waypoint.Y}
			}
			seen := map[[2]float64]string{}
			for _, slot := range got {
				coord := coordOf[slot]
				require.NotContains(t, seen, coord, "slots %q and %q share location %v", seen[coord], slot, coord)
				seen[coord] = slot
			}

			// Every slot is a fuelled park — a hull that cannot refuel where it stands is stranded.
			fuelled := map[string]bool{}
			for _, waypoint := range waypoints {
				fuelled[waypoint.Symbol] = waypoint.HasFuel
			}
			for _, slot := range got {
				require.True(t, fuelled[slot], "slot %q has no on-site fuel", slot)
			}
		})
	}
}

// Restart-idempotent: the same era rows resolve to the same ordered slot set every pass, so a
// coordinator restart re-homes no hull.
func TestContractStandbyPlacementProvider_EraPlacementIsStableAcrossPasses(t *testing.T) {
	waypoints := loadEraWaypoints(t, "era5")
	provider := eraPlacementProvider(waypoints)

	first, err := provider.StandbyPlacement(context.Background(), 1)
	require.NoError(t, err)
	for pass := 0; pass < 5; pass++ {
		again, err := provider.StandbyPlacement(context.Background(), 1)
		require.NoError(t, err)
		require.Equal(t, first, again, "placement drifted on pass %d", pass)
	}
}

// recordingLogger captures the container log lines a pass emitted.
type recordingLogger struct {
	mu    sync.Mutex
	lines []map[string]interface{}
}

func (l *recordingLogger) Log(level, message string, metadata map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := map[string]interface{}{"level": level, "message": message}
	for key, value := range metadata {
		entry[key] = value
	}
	l.lines = append(l.lines, entry)
}

func (l *recordingLogger) actions(action string) []map[string]interface{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	var hits []map[string]interface{}
	for _, line := range l.lines {
		if line["action"] == action {
			hits = append(hits, line)
		}
	}
	return hits
}

// A slot that fails open does so SILENTLY by design, so the miss must be logged or a changed
// generator template is indistinguishable from a healthy era. An era with the template intact
// logs nothing; strip the far sink's PIRATE_BASE trait and exactly one WARN names it — latched,
// so a persistent break does not spam one line per homing pass.
func TestContractStandbyPlacementProvider_LogsTheAnchorsThisEraDidNotChart(t *testing.T) {
	intact := loadEraWaypoints(t, "era5")
	healthy := &recordingLogger{}
	_, err := eraPlacementProvider(intact).StandbyPlacement(loggerContext(healthy), 1)
	require.NoError(t, err)
	require.Empty(t, healthy.actions("contract_standby_anchor_miss"),
		"a fully charted era must log no anchor miss")

	broken := loadEraWaypoints(t, "era5")
	for _, waypoint := range broken {
		if !waypoint.HasTrait("PIRATE_BASE") {
			continue
		}
		kept := make([]string, 0, len(waypoint.Traits))
		for _, trait := range waypoint.Traits {
			if trait != "PIRATE_BASE" {
				kept = append(kept, trait)
			}
		}
		waypoint.Traits = kept
	}

	logged := &recordingLogger{}
	provider := eraPlacementProvider(broken)
	for pass := 0; pass < 3; pass++ {
		_, err := provider.StandbyPlacement(loggerContext(logged), 1)
		require.NoError(t, err)
	}

	misses := logged.actions("contract_standby_anchor_miss")
	require.Len(t, misses, 1, "the miss must be logged once, not once per pass")
	require.Equal(t, "WARN", misses[0]["level"])
	require.Equal(t, []string{"far_sink"}, misses[0]["missing_anchors"])
}

func loggerContext(logger common.ContainerLogger) context.Context {
	return logging.WithLogger(context.Background(), logger)
}
