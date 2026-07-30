package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// sp-k4z5b: the trade fleet coordinator's market-freshness floor, live-tunable via
// `spacetraders tune --operation tour`. sp-ry4r8 collapsed it to ONE key.
//
// The operational requirement behind the surface: during the incident the only lever was
// a config.yaml edit plus a full daemon bounce, and each bounce burns jump cooldowns —
// the resource the fleet is actually short of. Forty minutes instead of fifteen.

const tuneTradeFleetContainerID = "trade_fleet_coordinator-player-tune-test"

// The operation alias resolves, and --show lists the floor with its bounds, units and
// documented default — exactly ONE market-freshness knob (sp-ry4r8). The old
// listing_max_age_minutes / sink_freshness_max_minutes pair defaulted identically and took
// the identical derived term, so it was two names an operator had to keep in sync.
func TestTourTune_OperationResolvesAndListsTheFreshnessFloor(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, tuneTradeFleetContainerID,
		string(container.ContainerTypeTradeFleetCoordinator), "trade_fleet_coordinator", "RUNNING",
		map[string]interface{}{"container_id": tuneTradeFleetContainerID, "trade_fleet_tick_secs": 120})
	s := &DaemonServer{containerRepo: repo}

	show, err := s.ShowTunableConfig(context.Background(), "", "tour", playerID)
	require.NoError(t, err, "`tune --operation tour` must resolve the active trade fleet coordinator")
	require.Equal(t, tuneTradeFleetContainerID, show.ContainerID)

	byKey := map[string]TunableKnobStatus{}
	for _, k := range show.Knobs {
		byKey[k.Key] = k
	}
	require.Len(t, show.Knobs, len(tradingCmd.TradeFleetTunableDefaults()))
	require.Len(t, show.Knobs, 1,
		"the tour operation must expose ONE market-freshness knob — two that must be kept in sync are worse than one (sp-ry4r8)")

	const key = "market_data_max_age_minutes"
	knob, ok := byKey[key]
	require.True(t, ok, "%s must be reachable via --operation tour", key)
	require.Equal(t, 75, knob.Effective, "untuned, %s reports its documented floor", key)
	require.Equal(t, "default", knob.Source)
	require.Equal(t, "minutes", knob.Bound.Unit)
	require.Equal(t, 1, knob.Bound.Min, "%s may never be tuned to 0 as a VALUE — 0 is the revert verb", key)
	require.Equal(t, 43_200, knob.Bound.Max, "%s ceiling mirrors the scan budget's 30-day arithmetic ceiling", key)
	require.Contains(t, knob.Bound.Description, "rotation bound",
		"%s must tell the operator the floor is not the whole cap", key)
	require.Contains(t, knob.Bound.Description, "next tick",
		"%s must state its latency truthfully — a knob that silently needs a rebuild is worse than no knob", key)
	// One knob, two consumers: the description must name BOTH, or an operator cannot know
	// that widening the listing filter also widens a fail-closed money guard.
	require.Contains(t, knob.Bound.Description, "freshListings",
		"%s must name the listing-filter consumer", key)
	require.Contains(t, knob.Bound.Description, "money guard",
		"%s must name the firm-sink money guard it also floors", key)
	require.Contains(t, knob.Bound.Description, "does NOT disarm",
		"%s must state that 0 reverts rather than disarms (RULINGS #4)", key)

	// The retired names must be GONE from the surface, not silently accepted aliases.
	for _, retired := range []string{"listing_max_age_minutes", "sink_freshness_max_minutes"} {
		_, still := byKey[retired]
		require.False(t, still, "%s was collapsed into %s and must not still be listed", retired, key)
	}
}

// A tuned floor lands in the column the daemon-global tour handler reads BY TYPE, which is
// what makes it live: the tour handler serves every TRADING container and has no container
// id of its own, so a by-id reader could never see the operator's write.
func TestTourTune_TunedFloorIsReadableByTheByTypeReader(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, tuneTradeFleetContainerID,
		string(container.ContainerTypeTradeFleetCoordinator), "trade_fleet_coordinator", "RUNNING",
		map[string]interface{}{"container_id": tuneTradeFleetContainerID})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	out, err := s.MutateContainerConfigKey(ctx, "", "tour", "market_data_max_age_minutes", 1440, playerID)
	require.NoError(t, err)
	require.True(t, out.Changed)
	require.Equal(t, 75, out.OldEffective)
	require.Equal(t, 1440, out.NewEffective)
	require.Equal(t, "live-config", out.NewSource)

	reader := NewCoordinatorConfigReader(repo, string(container.ContainerTypeTradeFleetCoordinator))
	snap, err := reader.Snapshot(ctx, playerID)
	require.NoError(t, err)
	v, set := snap.PositiveInt("market_data_max_age_minutes")
	require.True(t, set, "the by-type reader must see the operator's write")
	require.Equal(t, 1440, v)

	// Revert clears the key so the documented floor applies again — it does NOT write 0,
	// which would be indistinguishable from disarming a fail-closed money guard.
	out, err = s.MutateContainerConfigKey(ctx, "", "tour", "market_data_max_age_minutes", 0, playerID)
	require.NoError(t, err)
	require.Equal(t, 75, out.NewEffective)
	require.Equal(t, "default", out.NewSource)
	snap, err = reader.Snapshot(ctx, playerID)
	require.NoError(t, err)
	_, set = snap.PositiveInt("market_data_max_age_minutes")
	require.False(t, set, "revert clears the key rather than persisting a zero")
}

// Out-of-bounds is rejected before any write, and an unknown key is named as unknown —
// nothing about the freshness surface is silently armed.
func TestTourTune_RejectsOutOfBoundsAndUnknownKeys(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, tuneTradeFleetContainerID,
		string(container.ContainerTypeTradeFleetCoordinator), "trade_fleet_coordinator", "RUNNING",
		map[string]interface{}{"container_id": tuneTradeFleetContainerID})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	before := containerConfigJSON(t, repo, tuneTradeFleetContainerID, playerID)
	for _, tc := range []struct {
		key   string
		value int
	}{
		{"market_data_max_age_minutes", 43_201},
		{"market_data_max_age_mins", 600}, // near-miss on the real key name
		// The collapsed-away names are UNKNOWN keys now, not silent aliases: a stale runbook
		// must error loudly rather than write a column nothing reads (sp-ry4r8).
		{"listing_max_age_minutes", 600},
		{"sink_freshness_max_minutes", 600},
	} {
		_, err := s.MutateContainerConfigKey(ctx, "", "tour", tc.key, tc.value, playerID)
		require.Error(t, err, "%s=%d must be rejected", tc.key, tc.value)
	}
	require.Equal(t, before, containerConfigJSON(t, repo, tuneTradeFleetContainerID, playerID),
		"a rejected tune leaves the column byte-identical")
}

// The by-type reader is fail-safe: no running trade fleet coordinator is an empty
// snapshot, not an error, because it is consulted on the tour's hot path and must leave
// every caller on its boot floor rather than failing a tour.
func TestCoordinatorConfigReader_NoActiveCoordinatorIsEmptyNotAnError(t *testing.T) {
	_, repo, playerID := tuneTestDB(t)

	snap, err := NewCoordinatorConfigReader(repo, string(container.ContainerTypeTradeFleetCoordinator)).
		Snapshot(context.Background(), playerID)
	require.NoError(t, err)
	_, set := snap.PositiveInt("market_data_max_age_minutes")
	require.False(t, set)
}
