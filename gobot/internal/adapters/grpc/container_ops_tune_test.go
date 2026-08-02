package grpc

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
	"gorm.io/gorm"
)

// These tests cover the generic runtime tune mechanism (sp-vwek). The mechanism
// generalizes the sp-ev0n worker-cap pattern: a `tune` verb read-modify-writes
// ONE knob in a RUNNING container's persisted config column (the daemon as
// single writer, RULINGS #3), a static bounds registry rejects
// out-of-bounds/unknown tunes BEFORE any write, and the config column is the
// restart-recovery source (RULINGS #2). The probe-sensing coordinator is the
// mechanism vehicle here; the bootstrap tests at the bottom prove the LIVE
// (liveconfig, next-tick) consumption path end-to-end.

// ---- fixtures ---------------------------------------------------------------

func tuneTestDB(t *testing.T) (*gorm.DB, *persistence.ContainerRepositoryGORM, int) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	player := persistence.PlayerModel{AgentSymbol: "TUNE-AGENT", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&player).Error)
	return db, persistence.NewContainerRepository(db), player.ID
}

func seedTuneContainer(t *testing.T, db *gorm.DB, playerID int, id, containerType, commandType, status string, config map[string]interface{}) {
	t.Helper()
	raw, err := json.Marshal(config)
	require.NoError(t, err)
	now := time.Now()
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID: id, PlayerID: playerID,
		ContainerType: containerType, CommandType: commandType,
		Status: status, Config: string(raw),
		StartedAt: &now, HeartbeatAt: &now,
	}).Error)
}

func containerConfigJSON(t *testing.T, repo *persistence.ContainerRepositoryGORM, id string, playerID int) string {
	t.Helper()
	model, err := repo.Get(context.Background(), id, playerID)
	require.NoError(t, err)
	require.NotNil(t, model)
	return model.Config
}

type tuneFakeRecorder struct{ events []*captain.Event }

func (f *tuneFakeRecorder) Record(_ context.Context, e *captain.Event) error {
	f.events = append(f.events, e)
	return nil
}

const (
	tuneSensingContainerID        = "probe_sensing_coordinator-player-tune-test"
	sensingContainerType          = "PROBE_SENSING_COORDINATOR"
	scoutPostContainerType        = "SCOUT_POST_COORDINATOR"
	shipyardBackfillContainerType = "SHIPYARD_BACKFILL_COORDINATOR"
	contractCoordinatorType       = "CONTRACT_FLEET_COORDINATOR"
	bootstrapContainerType        = "BOOTSTRAP_COORDINATOR"
	tuneBootstrapContainerID      = "bootstrap-player-tune-test"
)

// ---- rejection: bounds + unknown keys, no write ------------------------------

// A tune outside its registry bounds, with a negative value, or naming a key the
// engine does not expose is REJECTED before any write: the config column must be
// byte-identical afterwards (no silent partial state).
func TestTune_RejectsInvalidTunes_NoWrite(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, tuneSensingContainerID, sensingContainerType, "probe_sensing_coordinator", "RUNNING", map[string]interface{}{
		"container_id": tuneSensingContainerID, "tick_secs": 600,
	})
	s := &DaemonServer{containerRepo: repo}
	before := containerConfigJSON(t, repo, tuneSensingContainerID, playerID)

	cases := []struct {
		name  string
		key   string
		value int
	}{
		{"tick below min (10s floor)", "tick_secs", 5},
		{"tick above max (86400s ceiling)", "tick_secs", 90000},
		{"capex reserve above max (5M ceiling)", "capex_reserve_credits", 6_000_000},
		{"negative value", "capex_reserve_credits", -1},
		{"unknown key for this engine", "warp_speed", 42},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.MutateContainerConfigKey(context.Background(), tuneSensingContainerID, "", tc.key, tc.value, playerID)
			require.Error(t, err)
			require.Equal(t, before, containerConfigJSON(t, repo, tuneSensingContainerID, playerID),
				"a rejected tune must leave the config column byte-identical")
		})
	}
}

// Target resolution failures are clear operator errors: an unknown operation alias,
// a missing container, a STOPPED container, and an engine with no tunable registry.
// The retired freshsizer/frontier aliases are unknown operations now — the same
// clear error, not a silent no-op.
func TestTune_RejectsUnresolvableTargets(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, "stopped-sensing", sensingContainerType, "probe_sensing_coordinator", "STOPPED", map[string]interface{}{"container_id": "stopped-sensing"})
	seedTuneContainer(t, db, playerID, "gas-coord-1", "GAS_COORDINATOR", "gas_coordinator", "RUNNING", map[string]interface{}{"container_id": "gas-coord-1"})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	_, err := s.MutateContainerConfigKey(ctx, "", "bogus-operation", "capex_reserve_credits", 1000, playerID)
	require.Error(t, err, "an unknown operation alias must be rejected")

	for _, retired := range []string{"freshsizer", "frontier"} {
		_, err = s.MutateContainerConfigKey(ctx, "", retired, "capex_reserve_credits", 1000, playerID)
		require.Error(t, err, "the retired %q operation alias must be rejected like any unknown alias", retired)
		require.Contains(t, err.Error(), "no tunable coordinator")
	}

	_, err = s.MutateContainerConfigKey(ctx, "no-such-container", "", "capex_reserve_credits", 1000, playerID)
	require.Error(t, err, "a missing container must be rejected")

	_, err = s.MutateContainerConfigKey(ctx, "stopped-sensing", "", "capex_reserve_credits", 1000, playerID)
	require.Error(t, err, "a STOPPED container must be rejected — tune targets RUNNING/PENDING work")

	_, err = s.MutateContainerConfigKey(ctx, "gas-coord-1", "", "capex_reserve_credits", 1000, playerID)
	require.Error(t, err, "an engine with no tunable-knob registry must be rejected")

	_, err = s.MutateContainerConfigKey(ctx, "", "", "capex_reserve_credits", 1000, playerID)
	require.Error(t, err, "one of container id or operation is required")
}

// ---- revert: 0 restores the documented default -------------------------------

// `tune <key> 0` reverts the knob: the key is removed from the config column, so
// the coordinator's default chain applies — the NEXT rebuild (and any restart)
// runs on the documented default const, reported honestly in the outcome.
func TestTune_ZeroRevertsKnobToDocumentedDefault(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, tuneSensingContainerID, sensingContainerType, "probe_sensing_coordinator", "RUNNING", map[string]interface{}{
		"container_id": tuneSensingContainerID, "tick_secs": 600,
	})
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	out, err := s.MutateContainerConfigKey(ctx, tuneSensingContainerID, "", "tick_secs", 0, playerID)
	require.NoError(t, err)
	require.True(t, out.Changed, "reverting a set knob is an effective change")
	require.Equal(t, 600, out.OldEffective)
	require.Equal(t, "live-config", out.OldSource)
	require.Equal(t, 30, out.NewEffective, "the documented default (30s reconcile cadence) applies after revert")
	require.Equal(t, "default", out.NewSource)

	// The column no longer carries a positive value for the key — the recovery
	// rebuild (and any live reader) sees "no live value → default".
	snap, err := NewContainerConfigReader(repo).Snapshot(ctx, tuneSensingContainerID, playerID)
	require.NoError(t, err)
	_, set := snap.PositiveInt("tick_secs")
	require.False(t, set, "revert must clear the key from the config column")

	// Re-reverting an already-default knob is an honest no-op.
	out, err = s.MutateContainerConfigKey(ctx, tuneSensingContainerID, "", "tick_secs", 0, playerID)
	require.NoError(t, err)
	require.False(t, out.Changed)
}

// ---- restart-recovery: the config column is the recovery source --------------

// A tuned value SURVIVES restart recovery: recovery rebuilds the launch command from
// the config column (buildCommandForType), which now carries the tuned value — while
// an untuned knob still resolves to its default. This is the RULINGS #2 guarantee
// the worker cap proved, applied to the sensing coordinator's money knobs.
func TestTune_TunedValueSurvivesRestartRecovery(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, tuneSensingContainerID, sensingContainerType, "probe_sensing_coordinator", "RUNNING", map[string]interface{}{
		"container_id": tuneSensingContainerID, "probe_cap": 0,
	})
	s := &DaemonServer{containerRepo: repo, containerSpecs: map[string]ContainerSpec{}}
	s.registerContainerSpecs()
	ctx := context.Background()

	_, err := s.MutateContainerConfigKey(ctx, tuneSensingContainerID, "", "capex_reserve_credits", 350000, playerID)
	require.NoError(t, err)

	// RESTART: reload the persisted config through the JSON round-trip recovery does
	// (numbers come back as float64) and rebuild through the SAME factory recovery uses.
	var reloaded map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(containerConfigJSON(t, repo, tuneSensingContainerID, playerID)), &reloaded))
	rebuilt, err := s.buildCommandForType("probe_sensing_coordinator", reloaded, playerID, tuneSensingContainerID)
	require.NoError(t, err)
	cmd, ok := rebuilt.(*scoutingCmd.RunProbeSensingCoordinatorCommand)
	require.True(t, ok)

	require.Equal(t, 350000, cmd.CapexReserveCredits, "recovery must read the TUNED value from the config column, not the default")
	require.Zero(t, cmd.ProbeCap, "an untuned knob stays unset — the coordinator default chain applies")
}

// ---- idempotency + audit ------------------------------------------------------

// Setting a knob to its current value is an honest no-op (no DB write), and every
// EFFECTIVE tune — and ONLY an effective tune — emits the config.tuned audit event:
// these knobs move real credits, so a change is never a silent DB write.
func TestTune_IdempotentNoOp_AndAuditOnEffectiveTunesOnly(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, tuneSensingContainerID, sensingContainerType, "probe_sensing_coordinator", "RUNNING", map[string]interface{}{
		"container_id": tuneSensingContainerID,
	})
	rec := &tuneFakeRecorder{}
	SetCaptainEventRecorder(rec)
	t.Cleanup(func() { SetCaptainEventRecorder(nil) })
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	out, err := s.MutateContainerConfigKey(ctx, tuneSensingContainerID, "", "tick_secs", 120, playerID)
	require.NoError(t, err)
	require.True(t, out.Changed)
	require.Len(t, rec.events, 1, "an effective tune must emit exactly one audit event")
	require.Equal(t, captain.EventConfigTuned, rec.events[0].Type)
	require.Equal(t, playerID, rec.events[0].PlayerID)
	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(rec.events[0].Payload), &payload))
	require.Equal(t, tuneSensingContainerID, payload["container_id"])
	require.Equal(t, "tick_secs", payload["key"])
	require.EqualValues(t, 30, payload["old_effective"], "the pre-tune effective value (default 30s) is audited")
	require.EqualValues(t, 120, payload["new_effective"])

	// Idempotent re-tune: same value → no write, no audit.
	out, err = s.MutateContainerConfigKey(ctx, tuneSensingContainerID, "", "tick_secs", 120, playerID)
	require.NoError(t, err)
	require.False(t, out.Changed, "re-tuning to the current value must be a no-op")
	require.Len(t, rec.events, 1, "a no-op must not emit an audit event")

	// Rejected tune: no write happened, so nothing to audit.
	_, err = s.MutateContainerConfigKey(ctx, tuneSensingContainerID, "", "tick_secs", 5, playerID)
	require.Error(t, err)
	require.Len(t, rec.events, 1, "a rejected tune must not emit an audit event")
}

// ---- registry invariants --------------------------------------------------------

// The bounds registry is the single documented source of truth for what is tunable:
// its key set and defaults must MATCH the coordinator-exported defaults maps (drift
// here would make a tune silently ineffective), every entry must carry sane bounds
// and metadata, and NO *_treasury_pct knob may ever exceed the compile-time 25%
// treasury guard — the guard is never weakened, made tunable, or bypassable. The
// retired freshsizer/frontier engines must be GONE from the registry: a tune surface
// over an unbuildable container would be a lie.
func TestTuneRegistry_MatchesCoordinatorDefaults_AndNeverWeakensTreasuryGuard(t *testing.T) {
	registry := tunableKnobsByContainerType()

	engines := []struct {
		containerType string
		defaults      map[string]int
	}{
		{sensingContainerType, scoutingCmd.SensingTunableDefaults()},
		{scoutPostContainerType, scoutingCmd.ScoutPostTunableDefaults()},
		{shipyardBackfillContainerType, scoutingCmd.ShipyardBackfillTunableDefaults()},
		{contractCoordinatorType, ContractCoordinatorTunableDefaults()},
		{bootstrapContainerType, bootstrapCmd.BootstrapTunableDefaults()},
		{string(container.ContainerTypeContractScaler), contractScalerCmd.ContractScalerTunableDefaults()},
		{string(container.ContainerTypeFleetAutosizer), fleetCmd.FleetAutosizerTunableDefaults()},
		{string(container.ContainerTypeTradeFleetCoordinator), tradingCmd.TradeFleetTunableDefaults()},
	}
	for _, engine := range engines {
		knobs, ok := registry[engine.containerType]
		require.True(t, ok, "engine %s must be registered", engine.containerType)
		require.Len(t, knobs, len(engine.defaults), "registry keys must exactly match the coordinator's tunable set for %s", engine.containerType)
		for key, def := range engine.defaults {
			bound, ok := knobs[key]
			require.True(t, ok, "knob %s.%s must be registered", engine.containerType, key)
			require.Equal(t, def, bound.Default, "registry default for %s must equal the coordinator's documented default", key)
			require.Equal(t, "int", bound.Type)
			require.Greater(t, bound.Max, 0, "%s must carry a positive Max", key)
			require.GreaterOrEqual(t, bound.Max, bound.Min, "%s bounds must be ordered", key)
			require.NotEmpty(t, bound.Unit, "%s must carry a unit", key)
			require.NotEmpty(t, bound.Description, "%s must carry a description", key)
		}
	}

	// The retired engines' tune surfaces are unwired with their registry entries.
	_, sizerStillTunable := registry[string(container.ContainerTypeMarketFreshnessSizer)]
	require.False(t, sizerStillTunable, "the retired market-freshness sizer must have no tune surface")
	_, frontierStillTunable := registry[string(container.ContainerTypeFrontierExpansion)]
	require.False(t, frontierStillTunable, "the retired frontier expansion coordinator must have no tune surface")

	// The treasury-fraction rule: any *_treasury_pct knob ever registered is capped at
	// the compile-time 25 guard. (Vacuously true today — no registered engine
	// exposes one — but the rule is pinned for every future registry entry.)
	for containerType, knobs := range registry {
		for key, bound := range knobs {
			if strings.Contains(key, "treasury_pct") {
				require.LessOrEqual(t, bound.Max, 25, "%s.%s: a treasury-pct knob may never exceed the 25%% hard guard", containerType, key)
				require.GreaterOrEqual(t, bound.Min, 1, "%s.%s: a treasury-pct knob floor is 1", containerType, key)
			}
		}
	}
}

// ---- show: effective knobs + sources + bounds -----------------------------------

// The minimal `tune --show` for a migrated engine: every registered knob with its
// EFFECTIVE value, its source (live-config when the column carries a positive value —
// launch values share that store — else default), and its bounds, sorted by key.
func TestShowTunableConfig_ListsEffectiveValuesSourcesAndBounds(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	seedTuneContainer(t, db, playerID, tuneSensingContainerID, sensingContainerType, "probe_sensing_coordinator", "RUNNING", map[string]interface{}{
		"container_id": tuneSensingContainerID, "tick_secs": 120,
	})
	s := &DaemonServer{containerRepo: repo}

	out, err := s.ShowTunableConfig(context.Background(), tuneSensingContainerID, "", playerID)
	require.NoError(t, err)
	require.Equal(t, tuneSensingContainerID, out.ContainerID)
	require.Equal(t, sensingContainerType, out.ContainerType)
	require.Len(t, out.Knobs, len(scoutingCmd.SensingTunableDefaults()), "every registered knob is listed")
	require.True(t, sort.SliceIsSorted(out.Knobs, func(i, j int) bool { return out.Knobs[i].Key < out.Knobs[j].Key }), "knobs are listed in stable key order")

	byKey := map[string]TunableKnobStatus{}
	for _, k := range out.Knobs {
		byKey[k.Key] = k
	}
	tick := byKey["tick_secs"]
	require.Equal(t, 120, tick.Effective)
	require.Equal(t, "live-config", tick.Source)
	require.Equal(t, 10, tick.Bound.Min)
	require.Equal(t, 86400, tick.Bound.Max)
	require.Equal(t, "seconds", tick.Bound.Unit)

	spend := byKey["capex_reserve_credits"]
	require.Equal(t, 100000, spend.Effective, "an unset knob shows its documented default")
	require.Equal(t, "default", spend.Source)
	require.Equal(t, 5_000_000, spend.Bound.Max)

	capKnob := byKey["probe_cap"]
	require.Equal(t, 3000, capKnob.Effective, "the probe cap's documented default is listed")
	require.Equal(t, "default", capKnob.Source)
}

// ---- bootstrap: the config.yaml-authoritative coordinator joins the registry (sp-r6yq) ----

// Acceptance (registry side): the captain bootstrap coordinator is a first-class tune target.
// Resolved by `--operation bootstrap` (FindActiveCoordinatorByType), a live write of the BARE tune key
// lands in the persisted column (the coordinator's per-tick liveconfig reader picks it up next tick),
// `--show` lists it with its bounds, an effective tune emits the config.tuned audit, an out-of-bounds
// tune is rejected, and `tune 0` reverts to the documented default. Bootstrap's launch keys are the
// SEPARATE prefixed bootstrap_* family, so an untuned key reads its documented default (source
// "default"), never a launch value.
func TestTune_Bootstrap_TunesLiveViaOperation_ShowRevertAudit(t *testing.T) {
	db, repo, playerID := tuneTestDB(t)
	// Seed with the config.yaml-authoritative PREFIXED launch key (+ identity), mirroring how the
	// daemon launches bootstrap — the bare tune key is a distinct family.
	seedTuneContainer(t, db, playerID, tuneBootstrapContainerID, bootstrapContainerType, "bootstrap", "RUNNING", map[string]interface{}{
		"container_id":        tuneBootstrapContainerID,
		"agent_symbol":        "TUNE-AGENT",
		"bootstrap_tick_secs": 45,
	})
	rec := &tuneFakeRecorder{}
	SetCaptainEventRecorder(rec)
	t.Cleanup(func() { SetCaptainEventRecorder(nil) })
	s := &DaemonServer{containerRepo: repo}
	ctx := context.Background()

	// Live tune via the operation alias: tick_secs 45 (default) → 120. No restart.
	out, err := s.MutateContainerConfigKey(ctx, "", "bootstrap", "tick_secs", 120, playerID)
	require.NoError(t, err)
	require.True(t, out.Changed)
	require.Equal(t, tuneBootstrapContainerID, out.ContainerID, "the operation alias must resolve to the active bootstrap coordinator")
	require.Equal(t, 45, out.OldEffective, "pre-tune effective is the documented default (the prefixed launch key is a separate family)")
	require.Equal(t, 120, out.NewEffective)
	require.Len(t, rec.events, 1, "an effective tune emits exactly one config.tuned audit event")
	require.Equal(t, captain.EventConfigTuned, rec.events[0].Type)

	// The BARE tune key is what the coordinator's per-tick live reader consumes.
	snap, err := NewContainerConfigReader(repo).Snapshot(ctx, tuneBootstrapContainerID, playerID)
	require.NoError(t, err)
	v, set := snap.PositiveInt("tick_secs")
	require.True(t, set)
	require.Equal(t, 120, v)

	// --show lists every registered knob with its bounds; the tuned one reads live-config.
	show, err := s.ShowTunableConfig(ctx, "", "bootstrap", playerID)
	require.NoError(t, err)
	require.Len(t, show.Knobs, len(bootstrapCmd.BootstrapTunableDefaults()))
	byKey := map[string]TunableKnobStatus{}
	for _, k := range show.Knobs {
		byKey[k.Key] = k
	}
	require.Equal(t, 120, byKey["tick_secs"].Effective)
	require.Equal(t, "live-config", byKey["tick_secs"].Source)
	require.Equal(t, 86_400, byKey["tick_secs"].Bound.Max)
	require.Equal(t, "seconds", byKey["tick_secs"].Bound.Unit)

	// Out-of-bounds is rejected before any write (the column stays byte-identical).
	before := containerConfigJSON(t, repo, tuneBootstrapContainerID, playerID)
	_, err = s.MutateContainerConfigKey(ctx, "", "bootstrap", "tick_secs", 86_401, playerID)
	require.Error(t, err, "tick_secs 86401 exceeds the 86400 ceiling")
	require.Equal(t, before, containerConfigJSON(t, repo, tuneBootstrapContainerID, playerID))

	// A knob that is no longer tunable is rejected as an unknown key — nothing is silently armed.
	_, err = s.MutateContainerConfigKey(ctx, "", "bootstrap", "probe_target", 5, playerID)
	require.Error(t, err, "the cold-start shape is fixed in code — probe_target is not a tunable knob")

	// `tune 0` reverts to the documented default; the bare key is cleared from the column.
	out, err = s.MutateContainerConfigKey(ctx, "", "bootstrap", "tick_secs", 0, playerID)
	require.NoError(t, err)
	require.True(t, out.Changed)
	require.Equal(t, 45, out.NewEffective)
	require.Equal(t, "default", out.NewSource)
	snap, err = NewContainerConfigReader(repo).Snapshot(ctx, tuneBootstrapContainerID, playerID)
	require.NoError(t, err)
	_, set = snap.PositiveInt("tick_secs")
	require.False(t, set, "revert clears the bare tune key from the column")
}

// Restart-survival: bootstrap's launch config is config.yaml-authoritative — resolveBootstrapConfig
// CLEARS the prefixed bootstrap_* keys and re-injects them from config.yaml on every build (creation and
// recovery alike). The BARE tune key is a SEPARATE family, never in that clear list, so a tuned value
// persists in the column across a daemon bounce and the coordinator's per-tick live reader keeps applying
// it (RULINGS #2) — while the transient prefixed launch key is refreshed from config.yaml. This is why the
// bootstrap tune key must NOT reuse the prefixed launch key name.
func TestTune_Bootstrap_BareTuneKeySurvivesConfigRebuild(t *testing.T) {
	s := &DaemonServer{} // zero bootstrapConfig → injectBootstrapConfig writes nothing (config.yaml unset)
	config := map[string]interface{}{
		"container_id":        "boot-x",
		"agent_symbol":        "A",
		"bootstrap_tick_secs": 45,  // prefixed launch key — transient, config.yaml-authoritative
		"tick_secs":           120, // bare tune key — must survive the rebuild
	}
	s.resolveBootstrapConfig(config)

	require.EqualValues(t, 120, config["tick_secs"], "a bare tune key must survive the launch-config rebuild")
	_, hasPrefixed := config["bootstrap_tick_secs"]
	require.False(t, hasPrefixed, "the prefixed launch key is transient — cleared and re-injected from config.yaml")
	require.Equal(t, "boot-x", config["container_id"], "identity keys survive the rebuild")
	require.Equal(t, "A", config["agent_symbol"], "identity keys survive the rebuild")
}
