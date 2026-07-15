package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// These tests cover the `spacetraders tune` CLI surface (sp-vwek): the generic live
// knob tuner over running containers. By construction the fake below exposes ONLY
// the tune/show RPCs — no container-restart method — so "no restart" is guaranteed
// by the surface this verb can reach, exactly as `fleet hub` and `goods factory
// workers` guarantee it. The daemon is the sole writer of the persisted knob
// (RULINGS #3); bounds/keys are validated daemon-side against the registry.

type tuneCall struct {
	containerID string
	operation   string
	key         string
	value       int64
}

type fakeTuner struct {
	calls    []tuneCall
	resp     *pb.TuneContainerConfigResponse
	respErr  error
	showResp *pb.ShowTunableConfigResponse
}

func (f *fakeTuner) TuneContainerConfig(_ context.Context, containerID, operation, key string, value int64, _ *int32, _ *string) (*pb.TuneContainerConfigResponse, error) {
	f.calls = append(f.calls, tuneCall{containerID: containerID, operation: operation, key: key, value: value})
	if f.respErr != nil {
		return nil, f.respErr
	}
	return f.resp, nil
}

func (f *fakeTuner) ShowTunableConfig(_ context.Context, containerID, operation string, _ *int32, _ *string) (*pb.ShowTunableConfigResponse, error) {
	if f.respErr != nil {
		return nil, f.respErr
	}
	return f.showResp, nil
}

// An effective tune prints old -> new WITH units and the no-restart contract; the
// motivating retune (purchase cooldown 10m -> 1m) is the reference rendering.
func TestRunTune_PrintsOldToNewWithUnits(t *testing.T) {
	client := &fakeTuner{resp: &pb.TuneContainerConfigResponse{
		ContainerId: "market_freshness_sizer_coordinator-player-1-abc", ContainerType: "MARKET_FRESHNESS_SIZER_COORDINATOR",
		Key: "purchase_cooldown_secs", OldEffective: 600, OldSource: "live-config",
		NewEffective: 60, NewSource: "live-config", Unit: "seconds", DefaultValue: 60, Changed: true,
	}}

	msg, err := runTune(context.Background(), client, "market_freshness_sizer_coordinator-player-1-abc", "", "purchase_cooldown_secs", 60, nil, nil)
	require.NoError(t, err)

	require.Len(t, client.calls, 1)
	require.Equal(t, tuneCall{containerID: "market_freshness_sizer_coordinator-player-1-abc", key: "purchase_cooldown_secs", value: 60}, client.calls[0])
	require.Contains(t, msg, "600 -> 60 seconds", "old -> new with units is the verb's core output")
	require.Contains(t, msg, "purchase_cooldown_secs")
	require.Contains(t, strings.ToLower(msg), "next tick")
	require.Contains(t, strings.ToLower(msg), "no restart")

	// A revert (value 0) reports the restored default honestly.
	client.resp = &pb.TuneContainerConfigResponse{
		ContainerId: "c1", ContainerType: "MARKET_FRESHNESS_SIZER_COORDINATOR",
		Key: "purchase_cooldown_secs", OldEffective: 120, OldSource: "live-config",
		NewEffective: 60, NewSource: "default", Unit: "seconds", DefaultValue: 60, Changed: true,
	}
	msg, err = runTune(context.Background(), client, "c1", "", "purchase_cooldown_secs", 0, nil, nil)
	require.NoError(t, err)
	require.Contains(t, msg, "120 -> 60 seconds")
	require.Contains(t, strings.ToLower(msg), "default", "a revert names the default it restored")
}

// An idempotent no-op and a daemon rejection are both reported honestly.
func TestRunTune_NoOpAndErrorsReportedHonestly(t *testing.T) {
	client := &fakeTuner{resp: &pb.TuneContainerConfigResponse{
		ContainerId: "c1", Key: "max_spend_per_cycle", OldEffective: 500000, OldSource: "live-config",
		NewEffective: 500000, NewSource: "live-config", Unit: "credits", Changed: false,
	}}
	msg, err := runTune(context.Background(), client, "c1", "", "max_spend_per_cycle", 500000, nil, nil)
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(msg), "already", "a no-op must not read like a fresh change")

	client.respErr = errors.New("max_spend_per_cycle=9000000 is outside its bounds [0, 5000000] credits — rejected, nothing written")
	_, err = runTune(context.Background(), client, "c1", "", "max_spend_per_cycle", 9000000, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside its bounds")
}

// --show renders every knob with effective value, source, and bounds.
func TestRunTuneShow_ListsKnobsWithSourcesAndBounds(t *testing.T) {
	client := &fakeTuner{showResp: &pb.ShowTunableConfigResponse{
		ContainerId:   "market_freshness_sizer_coordinator-player-1-abc",
		ContainerType: "MARKET_FRESHNESS_SIZER_COORDINATOR",
		Knobs: []*pb.TunableKnobStatus{
			{Key: "max_spend_per_cycle", Effective: 500000, Source: "default", Min: 0, Max: 5000000, Unit: "credits", Description: "max probe spend within the trailing spend window", DefaultValue: 500000},
			{Key: "purchase_cooldown_secs", Effective: 120, Source: "live-config", Min: 10, Max: 86400, Unit: "seconds", Description: "min wall-clock between probe buys", DefaultValue: 60},
		},
	}}

	msg, err := runTuneShow(context.Background(), client, "", "freshsizer", nil, nil)
	require.NoError(t, err)
	require.Contains(t, msg, "MARKET_FRESHNESS_SIZER_COORDINATOR")
	require.Contains(t, msg, "purchase_cooldown_secs")
	require.Contains(t, msg, "120")
	require.Contains(t, msg, "live-config")
	require.Contains(t, msg, "default")
	require.Contains(t, msg, "[10, 86400]", "bounds are part of the listing")
	require.Contains(t, msg, "credits")
}
