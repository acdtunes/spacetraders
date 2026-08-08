package commands

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// Handle is the mediator's entry point, so a request of the wrong type must be REFUSED rather than
// coerced: a coordinator that silently ran on a sibling's command would spend against the wrong
// player's config.
func TestGrowthHandle_RejectsTheWrongRequestType(t *testing.T) {
	h := NewRunFleetGrowthCoordinatorHandler(nil)
	if _, err := h.Handle(context.Background(), &RunFleetGrowthCoordinatorResponse{}); err == nil {
		t.Fatal("Handle must refuse a request that is not the growth command")
	}
}

// THE ANTI-THRASH STREAK IS PER-CONTAINER. The handler is a registered singleton serving every
// player's ticks, so a streak shared across containers would let one coordinator's persistent
// shortfall authorise another's seven-figure purchase on its first tick.
func TestGrowthState_IsPerContainer(t *testing.T) {
	h := NewRunFleetGrowthCoordinatorHandler(nil)

	h.coordinatorState("growth-1").heavyShortfallStreak = 3
	if got := h.coordinatorState("growth-2").heavyShortfallStreak; got != 0 {
		t.Fatalf("a second container inherited a streak of %d — the state is not per-container", got)
	}
	if got := h.coordinatorState("growth-1").heavyShortfallStreak; got != 3 {
		t.Fatalf("the first container's streak was disturbed, got %d", got)
	}
}

// AN UNWIRED LIVE-CONFIG READER MEANS ON, not off. A coordinator that is simply not wired for live
// config must keep buying on its launch values; reading "unwired" as "paused" would silently stop
// heavy buying on a deployment that never tuned anything.
func TestGrowthLiveKnobs_UnwiredReaderKeepsTheLaunchValuesAndStaysOn(t *testing.T) {
	h := NewRunFleetGrowthCoordinatorHandler(nil)

	cap, runway, on := h.liveKnobs(context.Background(), growthCmd(), 7, 1500)
	if cap != 7 || runway != 1500 || !on {
		t.Fatalf("unwired live config = (%d,%d,%v), want the launch values and ON", cap, runway, on)
	}
}

// A CONFIG READ FAILURE FALLS BACK TO THE LAUNCH BEHAVIOUR, never to a state derived from the
// failure: a DB blip must not silently stop heavy buying, and it must not silently disable a
// coordinator for an operator who never tuned it.
func TestGrowthLiveKnobs_SnapshotErrorFallsBackToLaunchAndStaysOn(t *testing.T) {
	h := NewRunFleetGrowthCoordinatorHandler(nil)
	h.SetGrowthConfigReader(erroringGrowthConfig{})

	cap, runway, on := h.liveKnobs(context.Background(), growthCmd(), 7, 1500)
	if cap != 7 || runway != 1500 || !on {
		t.Fatalf("a config blip resolved to (%d,%d,%v), want the launch values and ON", cap, runway, on)
	}
}

// The live tune overrides the launch value for both numeric knobs, on ONE snapshot per tick.
func TestGrowthLiveKnobs_TunedValuesOverrideTheLaunchValues(t *testing.T) {
	h := NewRunFleetGrowthCoordinatorHandler(nil)
	h.SetGrowthConfigReader(stubGrowthConfig{heavyCapKey: 9, growthRunwayKey: 400})

	cap, runway, on := h.liveKnobs(context.Background(), growthCmd(), 7, 1500)
	if cap != 9 || runway != 400 || !on {
		t.Fatalf("tuned knobs resolved to (%d,%d,%v), want (9,400,true)", cap, runway, on)
	}
}

// 1=ON / 2=OFF, deliberately NOT 0/1: `tune <key> 0` DELETES the key and means revert-to-default,
// so a 0/1 encoding would make "off" unexpressible. An absent key is therefore ON.
func TestGrowthLiveKnobs_OnlyTheOffSentinelSwitchesGrowthOff(t *testing.T) {
	cases := []struct {
		name string
		cfg  stubGrowthConfig
		want bool
	}{
		{"absent key is ON — absence is the revert", stubGrowthConfig{}, true},
		{"explicit 1 is ON", stubGrowthConfig{growthEnabledKey: defaultGrowthEnabled}, true},
		{"the OFF sentinel switches growth off", stubGrowthConfig{growthEnabledKey: growthDisabled}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewRunFleetGrowthCoordinatorHandler(nil)
			h.SetGrowthConfigReader(tc.cfg)
			if _, _, on := h.liveKnobs(context.Background(), growthCmd(), 5, 2000); on != tc.want {
				t.Fatalf("growth on = %v, want %v", on, tc.want)
			}
		})
	}
}

// THE LIVE CAP REACHES THE WAVE, not just the buy. Tuning the cap down to the owned count leaves
// nothing to save toward, so the regime must flip to PROBE on the very next tick — with no restart
// and no container rebuild.
func TestGrowthReconcile_TunedCapAtTheOwnedCountFlipsTheWaveToProbe(t *testing.T) {
	sink := &recordingWaveSink{}
	h := newGrowthHandlerWith(t, growthFixture{
		lanes: &fakeLanes{count: 9, readable: true}, treasury: 12_000_000, yardAsk: 1_000_000,
		heaviesOwned: 2,
	})
	h.SetMetricsSink(sink)
	h.SetGrowthConfigReader(stubGrowthConfig{heavyCapKey: 2})

	if _, err := h.reconcileOnce(context.Background(), growthCmd()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sink.waves[0] != common.WaveProbe || sink.reasons[0] != common.WaveProbeReasonUnreachable {
		t.Fatalf("a live cap at the owned count must flip the wave to PROBE, got %q/%q", sink.waves[0], sink.reasons[0])
	}
}

// erroringGrowthConfig is a live-config reader whose snapshot always fails — the transient DB blip.
type erroringGrowthConfig struct{}

func (erroringGrowthConfig) Snapshot(context.Context, string, int) (liveconfig.Snapshot, error) {
	return nil, context.DeadlineExceeded
}
