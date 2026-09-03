package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// trade_fleet.acap_tranches is the fleet-wide absorption sink cap, and it must reach every
// tour container: config.yaml -> the coordinator's launch config -> the launch spec -> each
// tour's own config -> the tour command. These pin every hop, and that an unset knob writes
// nothing at all (byte-identical to the hard-coded 2 it replaced).

// config.yaml -> the coordinator's launch config -> the coordinator command.
func TestTradeFleetConfig_ACapTranchesRoundTrip(t *testing.T) {
	require.Equal(t, 4, buildTradeCmd(t, config.TradeFleetConfig{ACapTranches: 4}).ACapTranches,
		"a configured cap must reach the coordinator command")
	require.Equal(t, 0, buildTradeCmd(t, config.TradeFleetConfig{}).ACapTranches,
		"an unset cap defers to the tour's own default")

	written := map[string]interface{}{}
	(&DaemonServer{tradeFleetConfig: config.TradeFleetConfig{ACapTranches: 4}}).injectTradeFleetConfig(written)
	require.Equal(t, 4, written["trade_fleet_acap_tranches"])

	unset := map[string]interface{}{}
	(&DaemonServer{}).injectTradeFleetConfig(unset)
	_, present := unset["trade_fleet_acap_tranches"]
	require.False(t, present, "an unset cap must write no key, so the launch config is unchanged")
}

// A stale cap persisted at a prior boot must not shadow a config.yaml that has since dropped
// the knob — the tradeFleetConfigKeys clear is what makes the revert (delete the key +
// restart) actually revert.
func TestTradeFleetConfig_ResolveClearsAStaleACapTranches(t *testing.T) {
	s := &DaemonServer{tradeFleetConfig: config.TradeFleetConfig{}}
	persisted := map[string]interface{}{
		"container_id":              "trade-coord-1",
		"agent_symbol":              "TORWIND",
		"trade_fleet_acap_tranches": 4,
	}

	s.resolveTradeFleetConfig(persisted)

	_, present := persisted["trade_fleet_acap_tranches"]
	require.False(t, present, "a stale cap must be cleared so deleting the knob reverts to 2")
}

// The launch spec -> the tour container's own config. Written only when the coordinator
// resolved a cap, so a config-only launch (and the captain CLI path) stays override-free.
func TestTourOverrides_CarryACapTranchesIntoTheTourConfig(t *testing.T) {
	overrides := tourOverridesFor(tradingCmd.TourLaunchSpec{ShipSymbol: "HULL-1", Fleet: "trade", ACapTranches: 4})
	require.NotNil(t, overrides, "a configured cap must ride the launch even on a plain trade hull")
	require.Equal(t, 4, overrides.ACapTranches)

	require.Nil(t, tourOverridesFor(tradingCmd.TourLaunchSpec{ShipSymbol: "HULL-1", Fleet: "trade"}),
		"an unset cap leaves a plain trade hull a config-only launch")

	cfg := map[string]interface{}{"ship_symbol": "HULL-1"}
	applyTourRunOverrides(cfg, &TourRunOverrides{ACapTranches: 4})
	require.Equal(t, 4, cfg["acap_tranches"], "the tour container config carries the cap")

	bare := map[string]interface{}{"ship_symbol": "HULL-1"}
	applyTourRunOverrides(bare, &TourRunOverrides{MVTLoop: true})
	_, present := bare["acap_tranches"]
	require.False(t, present, "an unset cap must write no key into the tour config")
}

// EVERY tour launch carries the cap, not just the coordinator's. addTradeFleetTourKnobs is
// the stamp StartTourRun runs for BOTH entry points — the coordinator's LaunchTour and the
// gRPC/CLI `workflow tour-run`, which passes nil overrides — so a CLI-launched hull can never
// run the default cap while the fleet and the solver run a raised one.
func TestAddTradeFleetTourKnobs_CarriesACapTranchesOnEveryLaunchPath(t *testing.T) {
	set := map[string]interface{}{}
	(&DaemonServer{tradeFleetConfig: config.TradeFleetConfig{ACapTranches: 4}}).addTradeFleetTourKnobs(set)
	require.Equal(t, 4, set["acap_tranches"], "a nil-override CLI launch must still carry the configured cap")

	unset := map[string]interface{}{}
	(&DaemonServer{}).addTradeFleetTourKnobs(unset)
	_, present := unset["acap_tranches"]
	require.False(t, present, "an unset cap writes no key, so the launch config is byte-identical")

	negative := map[string]interface{}{}
	(&DaemonServer{tradeFleetConfig: config.TradeFleetConfig{ACapTranches: -1}}).addTradeFleetTourKnobs(negative)
	_, present = negative["acap_tranches"]
	require.False(t, present, "a negative cap is unset, never persisted for the consumer to floor")
}

// A cap high enough to stop capping is not a cap: 44 would let every hull pile onto the one
// best sink. It is clamped to maxACapTranches with a WARNING naming both values, on the
// coordinator's config and every tour's alike.
func TestACapTranches_IsClampedToTheMaximumWithAWarning(t *testing.T) {
	var logged []string
	restore := acapClampLogf
	acapClampLogf = func(format string, args ...interface{}) { logged = append(logged, fmt.Sprintf(format, args...)) }
	t.Cleanup(func() { acapClampLogf = restore })

	s := &DaemonServer{tradeFleetConfig: config.TradeFleetConfig{ACapTranches: 44}}
	require.Equal(t, 6, maxACapTranches, "the ceiling is one named constant")
	require.Equal(t, maxACapTranches, s.resolvedACapTranches())

	coord := map[string]interface{}{}
	s.injectTradeFleetConfig(coord)
	require.Equal(t, maxACapTranches, coord["trade_fleet_acap_tranches"], "the coordinator runs the clamped cap")

	tour := map[string]interface{}{}
	s.addTradeFleetTourKnobs(tour)
	require.Equal(t, maxACapTranches, tour["acap_tranches"], "so does every tour it launches")

	require.NotEmpty(t, logged, "a clamped cap must be announced, never silently applied")
	require.Contains(t, logged[0], "44", "the WARNING names what was configured")
	require.Contains(t, logged[0], "6", "and what it was clamped to")

	logged = nil
	(&DaemonServer{tradeFleetConfig: config.TradeFleetConfig{ACapTranches: maxACapTranches}}).resolvedACapTranches()
	require.Empty(t, logged, "a cap AT the maximum is honoured, not clamped")
}

// End to end down the gRPC/CLI path itself (`workflow tour-run` -> StartTourRun with NIL
// overrides): the persisted launch config carries the cap and the recovery rebuild reads it
// back onto the command. Unset persists no key and rebuilds to the sentinel 0.
func TestStartTourRun_StampsACapTranchesOnTheNilOverrideCLIPath(t *testing.T) {
	launch := func(t *testing.T, knob int, hull string) *tradingCmd.RunTourCoordinatorCommand {
		t.Helper()
		s, db, playerID := newRecoveryTestServer(t)
		s.tradeFleetConfig.ACapTranches = knob

		ship := newIdleTradeShip(t, hull, playerID)
		ship.SetDedicatedFleet("trade")
		s.shipRepo = &tradeRouteShipRepo{ships: map[string]*navigation.Ship{hull: ship}}

		result, err := s.StartTourRun(context.Background(), hull, 5, int64(100000), 10, 3, int64(0), "AGENT", 1, playerID, nil)
		require.NoError(t, err)
		runner := s.registeredRunner(result.ContainerID)
		require.NotNil(t, runner)
		defer runner.cancelFunc()

		var model persistence.ContainerModel
		require.NoError(t, db.First(&model, "id = ?", result.ContainerID).Error)
		var cfg map[string]interface{}
		require.NoError(t, json.Unmarshal([]byte(model.Config), &cfg))
		if knob > 0 {
			require.Equal(t, float64(knob), cfg["acap_tranches"], "the CLI launch must persist the configured cap")
		} else {
			_, present := cfg["acap_tranches"]
			require.False(t, present, "an unset cap must persist no key")
		}
		rebuilt, err := s.buildCommandForType("tour_run", cfg, playerID, result.ContainerID)
		require.NoError(t, err)
		return rebuilt.(*tradingCmd.RunTourCoordinatorCommand)
	}

	require.Equal(t, 4, launch(t, 4, "TORWIND-71").ACapTranches,
		"a CLI-launched hull must run the fleet's cap, never the default while the solver plans to 4")
	require.Equal(t, 0, launch(t, 0, "TORWIND-72").ACapTranches,
		"unset rebuilds to the sentinel 0, so the tour resolves its own default")
}

// The tour container's config -> the tour command.
func TestBuildTourCoordinatorCommand_ReadsACapTranches(t *testing.T) {
	build := func(cfg map[string]interface{}) *tradingCmd.RunTourCoordinatorCommand {
		t.Helper()
		cmd, ok := buildTourCoordinatorCommand(newConfigReader(cfg), 1, "ctr-1").(*tradingCmd.RunTourCoordinatorCommand)
		require.True(t, ok, "build must return *RunTourCoordinatorCommand")
		return cmd
	}
	require.Equal(t, 4, build(map[string]interface{}{"ship_symbol": "HULL-1", "acap_tranches": 4}).ACapTranches)
	require.Equal(t, 0, build(map[string]interface{}{"ship_symbol": "HULL-1"}).ACapTranches,
		"an absent key leaves the coordinator's own default in force")
}
