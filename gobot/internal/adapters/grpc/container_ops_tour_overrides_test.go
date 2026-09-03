package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// sp-nxrt part (a): the trade-fleet coordinator escalates a twice-fast-failed hull to
// MOVEMENT by relaunching it with reposition-reach armed for THAT launch only. The launch
// carries a *TourRunOverrides that applyTourRunOverrides layers onto the freshly-built
// tour config — so the config→command→coordinator path that reads reposition_reach_enabled
// (registry OptionalBool) picks up the per-launch arming without a daemon-global config flip.
func TestApplyTourRunOverrides_ArmsReachForEscalatedLaunch(t *testing.T) {
	// nil overrides = a normal launch: the config's own (global) value is untouched.
	cfg := map[string]interface{}{"reposition_reach_enabled": false}
	applyTourRunOverrides(cfg, nil)
	require.Equal(t, false, cfg["reposition_reach_enabled"], "a nil override is byte-identical to a config-only launch")

	// An escalated launch arms reach even when the global config had it off.
	applyTourRunOverrides(cfg, &TourRunOverrides{RepositionReachEnabled: true})
	require.Equal(t, true, cfg["reposition_reach_enabled"], "the escalation arms reach for this launch")
}

// The override only ever ARMS reach (false -> true); it never downgrades a captain who has
// globally enabled reach. A non-escalated override on a reach-on config leaves it on.
func TestApplyTourRunOverrides_NeverDowngradesGlobalReach(t *testing.T) {
	cfg := map[string]interface{}{"reposition_reach_enabled": true}
	applyTourRunOverrides(cfg, &TourRunOverrides{RepositionReachEnabled: false})
	require.Equal(t, true, cfg["reposition_reach_enabled"],
		"a non-escalated override must never disarm globally-enabled reach")
}

// The tag → mvt_loop link: MVTLoop writes the selector key only when set, so an unselected
// launch (false or nil overrides) leaves the config without it — the legacy path, and the
// rollback lever step 2 relies on.
func TestApplyTourRunOverrides_MVTLoopWritesTheSelectorKeyOnlyWhenSet(t *testing.T) {
	cfg := map[string]interface{}{"ship_symbol": "HULL-1"}
	applyTourRunOverrides(cfg, nil)
	require.Equal(t, map[string]interface{}{"ship_symbol": "HULL-1"}, cfg, "nil overrides leave the config untouched")

	applyTourRunOverrides(cfg, &TourRunOverrides{MVTLoop: false})
	_, present := cfg["mvt_loop"]
	require.False(t, present, "an unselected launch must not write mvt_loop")

	applyTourRunOverrides(cfg, &TourRunOverrides{MVTLoop: true})
	require.Equal(t, true, cfg["mvt_loop"], "a selected launch writes mvt_loop: true")
}

// The mvt_loop → MVTLoop link: the command builder reads the selector back and the four
// loop knobs default to the spec values when absent.
func TestBuildTourCoordinatorCommand_MVTLoopReadsTheSelectorAndKnobDefaults(t *testing.T) {
	build := func(cfg map[string]interface{}) *tradingCmd.RunTourCoordinatorCommand {
		t.Helper()
		cmd, ok := buildTourCoordinatorCommand(newConfigReader(cfg), 1, "ctr-1").(*tradingCmd.RunTourCoordinatorCommand)
		require.True(t, ok, "build must return *RunTourCoordinatorCommand")
		return cmd
	}
	on := build(map[string]interface{}{"ship_symbol": "HULL-1", "mvt_loop": true})
	require.True(t, on.MVTLoop, "mvt_loop: true selects the MVT loop")

	off := build(map[string]interface{}{"ship_symbol": "HULL-1"})
	require.False(t, off.MVTLoop, "an absent mvt_loop is the legacy path")
	require.Equal(t, tradingCmd.DefaultYieldWindowSells, off.YieldWindowSells)
	require.Equal(t, tradingCmd.DefaultYieldMinSells, off.YieldMinSells)
	require.Equal(t, tradingCmd.DefaultClaimReachHops, off.ClaimReachHops)
	require.Equal(t, tradingCmd.DefaultClaimReachMaxHops, off.ClaimReachMaxHops)
	require.Equal(t, 4, off.ClaimReachMaxHops, "an absent claim_reach_max_hops caps escalation at the spec default")
	require.Equal(t, tradingCmd.DefaultRankerMinSpreadPerUnit, off.RankerMinSpreadPerUnit)
	require.Equal(t, tradingCmd.DefaultSpecialistCadenceMinutes, off.SpecialistCadenceMinutes)
	require.Equal(t, tradingCmd.DefaultYieldRateSpanFloorMinutes, off.YieldRateSpanFloorMinutes)
	require.Equal(t, tradingCmd.DefaultMVTRescueJumpsPerEpisode, off.MVTRescueJumpsPerEpisode)
	require.Equal(t, 2, off.MVTRescueJumpsPerEpisode, "an absent mvt_rescue_jumps_per_episode allows the second rescue")
	require.Equal(t, tradingCmd.DefaultMVTJumpFeeMaxSharePct, off.MVTJumpFeeMaxSharePct)
	require.Equal(t, 20, off.MVTJumpFeeMaxSharePct, "the fee guard is ARMED at the shipped share with no config at all")
	require.Equal(t, tradingCmd.DefaultMVTRecentlyLeftMinutes, off.MVTRecentlyLeftMinutes)
	require.Equal(t, 90, off.MVTRecentlyLeftMinutes, "an absent mvt_recently_left_minutes is the shipped window")

	tuned := build(map[string]interface{}{"ship_symbol": "HULL-1", "yield_rate_span_floor_minutes": 5})
	require.Equal(t, 5, tuned.YieldRateSpanFloorMinutes, "a configured floor rides through to the command")

	spread := build(map[string]interface{}{"ship_symbol": "HULL-1", "ranker_min_spread_per_unit": 350})
	require.Equal(t, 350, spread.RankerMinSpreadPerUnit, "a configured spread floor rides through to the command")

	capped := build(map[string]interface{}{"ship_symbol": "HULL-1", "mvt_rescue_jumps_per_episode": 1})
	require.Equal(t, 1, capped.MVTRescueJumpsPerEpisode, "a configured rescue cap rides through to the command")

	share := build(map[string]interface{}{"ship_symbol": "HULL-1", "mvt_jump_fee_max_share_pct": 5})
	require.Equal(t, 5, share.MVTJumpFeeMaxSharePct, "a configured fee share rides through to the command")

	left := build(map[string]interface{}{"ship_symbol": "HULL-1", "mvt_recently_left_minutes": 15})
	require.Equal(t, 15, left.MVTRecentlyLeftMinutes, "a configured recently-left window rides through to the command")
}

// The MVT knobs are stamped into the launch config only when the captain set them, so an
// absent [trade_fleet] key leaves the coordinator's spec default in force instead of pinning
// a 0 that a recovery rebuild would read back as "no floor".
func TestAddTradeFleetTourKnobs_WritesTheMVTKnobsOnlyWhenConfigured(t *testing.T) {
	unset := map[string]interface{}{}
	(&DaemonServer{}).addTradeFleetTourKnobs(unset)
	for _, key := range []string{"yield_window_sells", "yield_min_sells", "claim_reach_hops",
		"specialist_cadence_minutes", "yield_rate_span_floor_minutes", "ranker_min_spread_per_unit",
		"mvt_rescue_jumps_per_episode", "mvt_jump_fee_max_share_pct", "mvt_recently_left_minutes"} {
		_, present := unset[key]
		require.Falsef(t, present, "an unset %s must not be written", key)
	}

	set := map[string]interface{}{}
	(&DaemonServer{tradeFleetConfig: config.TradeFleetConfig{YieldRateSpanFloorMinutes: 45, RankerMinSpreadPerUnit: 350,
		MVTRescueJumpsPerEpisode: 3, MVTJumpFeeMaxSharePct: 5, MVTRecentlyLeftMinutes: 30}}).addTradeFleetTourKnobs(set)
	require.Equal(t, 45, set["yield_rate_span_floor_minutes"])
	require.Equal(t, 350, set["ranker_min_spread_per_unit"])
	require.Equal(t, 3, set["mvt_rescue_jumps_per_episode"])
	require.Equal(t, 5, set["mvt_jump_fee_max_share_pct"])
	require.Equal(t, 30, set["mvt_recently_left_minutes"])
}
