package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	autooutfitCmd "github.com/andrescamacho/spacetraders-go/internal/application/autooutfit"
	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	scoutingCmd "github.com/andrescamacho/spacetraders-go/internal/application/scouting/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// The daemon side of the GENERIC runtime tune mechanism (sp-vwek), generalizing the
// sp-ev0n per-knob pattern (MutateFactoryWorkerCap + FactoryWorkerCapConfigProvider)
// into one verb over a static bounds registry.

// TuneBound is one knob's bounds-registry entry: the validity range a tune must fall
// in, the documented default that applies when the key is unset, and the operator
// metadata --show prints.
type TuneBound struct {
	Type        string
	Min         int
	Max         int
	Default     int
	Unit        string
	Description string
}

// TuneOutcome reports one tune's effect.
type TuneOutcome struct {
	ContainerID   string
	ContainerType string
	Key           string
	OldEffective  int
	OldSource     string
	NewEffective  int
	NewSource     string
	Unit          string
	DefaultValue  int
	Changed       bool
}

// TunableKnobStatus is one knob's --show row.
type TunableKnobStatus struct {
	Key       string
	Effective int
	Source    string
	Bound     TuneBound
}

// TuneShowOutcome is the --show listing for one container.
type TuneShowOutcome struct {
	ContainerID   string
	ContainerType string
	Knobs         []TunableKnobStatus
}

var tuneOperationCoordinatorTypes = map[string]string{
	"sensing":          string(container.ContainerTypeProbeSensingCoordinator),
	"scoutpost":        string(container.ContainerTypeScoutPostCoordinator),
	"contract":         string(container.ContainerTypeContractFleetCoordinator),
	"autooutfit":       string(container.ContainerTypeAutoOutfitCoordinator),
	"shipyardbackfill": string(container.ContainerTypeShipyardBackfillCoordinator),
	"bootstrap":        string(container.ContainerTypeBootstrapCoordinator),
	"contractscaler":   string(container.ContainerTypeContractScaler),
	"autosizer":        string(container.ContainerTypeFleetAutosizer),
}

func tunableKnobsByContainerType() map[string]map[string]TuneBound {
	sensing := scoutingCmd.SensingTunableDefaults()
	scoutPost := scoutingCmd.ScoutPostTunableDefaults()
	contract := ContractCoordinatorTunableDefaults()
	autoOutfit := autooutfitCmd.AutoOutfitTunableDefaults()
	shipyardBackfill := scoutingCmd.ShipyardBackfillTunableDefaults()
	bootstrap := bootstrapCmd.BootstrapTunableDefaults()
	contractScaler := contractScalerCmd.ContractScalerTunableDefaults()
	fleetAutosizer := fleetCmd.FleetAutosizerTunableDefaults()
	return map[string]map[string]TuneBound{
		// The fleet capacity autosizer. heavy_cap is its ONLY live-tunable knob — every other
		// autosizer knob stays config.yaml + restart, so this is a deliberate exception rather
		// than a new pattern. Since sp-r7eiu removed the class_ceiling guard it is also the only
		// count-based bound left on any class. The money guards beside it (the immutable 50k
		// floor, the 25%-treasury rule, the per-tick cap) are compile-time consts and are
		// deliberately NOT tunable.
		string(container.ContainerTypeFleetAutosizer): {
			"heavy_cap": {Type: "int", Min: 0, Max: 50, Default: fleetAutosizer["heavy_cap"], Unit: "hulls", Description: "ceiling on owned HEAVY HULLS (capital exposure), counted FLEET-WIDE regardless of dedicated_fleet tag. Since sp-r7eiu removed class_ceiling this is the ONLY count-based bound on any hull class — every other bound is economic (demand, affordability, the per-tick cap). Default 5. NOTE: `tune heavy_cap 0` DELETES the key and reverts to the default — to HOLD at zero (own no heavies) set heavy_cap: 0 in config.yaml and restart. Applies next tick"},
		},
		string(container.ContainerTypeContractScaler): {
			// The single operator lever on the dedicated contract auto-scaler: the contract operation's hull
			// ceiling, hot-reloaded each tick (Pattern-C). It doubles as bootstrap's GATE-entry bar, so the
			// shipped default is sized for time-to-gate (a small operation funds the gate sooner) and is
			// raised live once the gate is built — delivery saturates ~7-8. Min 0 reverts to the default; the
			// sole money guard (the 200000 cushion) is a const, not tunable.
			"contract_fleet_max_hulls": {Type: "int", Min: 0, Max: 16, Default: contractScaler["contract_fleet_max_hulls"], Unit: "hulls", Description: "the exclusive contract fleet's live-tunable ceiling (filled delivery-first, behind the 200000 cushion). Default 3 — the cold start's GATE-entry bar; raise it once the gate is built (delivery saturates ~7-8)"},
		},
		string(container.ContainerTypeAutoOutfitCoordinator): {
			"min_telemetry_samples":     {Type: "int", Min: 1, Max: 1000, Default: autoOutfit["min_telemetry_samples"], Unit: "legs", Description: "fail-closed thin-telemetry floor — a hull with fewer measured legs is never upgraded"},
			"price_ceiling":             {Type: "int", Min: 0, Max: 5_000_000, Default: autoOutfit["price_ceiling"], Unit: "credits", Description: "max module price the coordinator will pay per install"},
			"max_installs_per_tick":     {Type: "int", Min: 1, Max: 20, Default: autoOutfit["max_installs_per_tick"], Unit: "installs", Description: "per-tick install cap"},
			"payback_horizon_hours":     {Type: "int", Min: 1, Max: 8760, Default: autoOutfit["payback_horizon_hours"], Unit: "hours", Description: "absolute payback gate — cost must be recovered within this horizon (default 0 = off until per-hull throughput is wired)"},
			"max_treasury_fraction_pct": {Type: "int", Min: 1, Max: 100, Default: autoOutfit["max_treasury_fraction_pct"], Unit: "percent", Description: "a single module never exceeds this fraction of live treasury"},
		},
		// The parked-probe sensing coordinator (`--operation sensing`): the ONE standing sensing
		// engine. Probes are bought for a WAYPOINT, flown there once, and then stand still
		// scanning forever — so there is no fleet-size dial and no tour pacing knob. What is
		// tunable is the SHAPE OF THE BUDGET (how much of the rate-limiter ceiling sensing may
		// take, and its floor), the shape of the ROTATION (how much more attention a hot market
		// earns, how many scans fly at once), and the money the buy floor holds back. Every buy
		// stays behind the fail-closed floor (immutable 50k reserve + committed capex + K hours
		// of cargo runway) — the 50k is a const, never a knob.
		// goods_whitelist is a string and therefore lives in the [sensing] config.yaml section
		// (the tune mechanism is int-only), injected at container construction.
		// LATENCY: the coordinator holds a liveconfig reader, so a tune applies on the NEXT
		// RECONCILE TICK — except inflight_cap and value_clamp_r, which bind when the scan
		// rotation is constructed and therefore apply at the next rebuild (each says so).
		// RETIRED KEYS: probe_budget, second_probe_threshold, purchase_cooldown_secs,
		// max_spend_per_cycle, spend_window_secs, freshness_target_secs, depth_floor and
		// discovery_declares_per_tick belonged to the touring model and are GONE — tuning one now
		// fails as an unknown key rather than writing a value nothing reads.
		string(container.ContainerTypeProbeSensingCoordinator): {
			"tick_secs":    {Type: "int", Min: 10, Max: 86_400, Default: sensing["tick_secs"], Unit: "seconds", Description: "reconcile cadence — how often placements advance, the queue drains and the scan rotation is refreshed. Applies next tick"},
			"wait_low_ms":  {Type: "int", Min: 1, Max: 60_000, Default: sensing["wait_low_ms"], Unit: "milliseconds", Description: "smoothed limiter wait BELOW which the emergency brake recovers (x1.2/tick toward fully released). Applies next tick"},
			"wait_high_ms": {Type: "int", Min: 1, Max: 600_000, Default: sensing["wait_high_ms"], Unit: "milliseconds", Description: "smoothed limiter wait ABOVE which the emergency brake bites (halves per tick, floored at 0.1). Braking is deliberately faster than recovery. Applies next tick"},

			"probe_cap":                  {Type: "int", Min: 1, Max: 10_000, Default: sensing["probe_cap"], Unit: "hulls", Description: "hard ceiling on parked probe hulls the engine may own. A BACKSTOP against a runaway placement plan, not the growth dial — the binding constraint on fleet size is the buy floor, i.e. money. Applies next tick"},
			"expansion_enabled":          {Type: "int", Min: 1, Max: 2, Default: sensing["expansion_enabled"], Unit: "flag", Description: "expansion SPENDING switch: 1=on, 2=off. NOT 0/1 — `tune <key> 0` means revert-to-default fleet-wide, so 0 would make 'off' unexpressible. Off stops everything that asks another engine to buy: charting-seed requests (the buy queue funds those by buying a probe) and off-gate explorer demand (the autosizer funds that with a ~769k hull). Off does NOT stop coverage growth on hulls you already own — frontier discovery, jump-gate reads, screening, and dispatching idle owned probes to unpriced markets all keep running, and all are free. It is not the money guard: capex_reserve_credits and the probe cap are, and they apply either way. Applies next tick"},
			"target_util_pct":            {Type: "int", Min: 50, Max: 95, Default: sensing["target_util_pct"], Unit: "percent", Description: "share of the rate-limiter ceiling the WHOLE fleet aims to occupy; sensing takes what is left of it after every other source. The remainder is burst headroom — raising this toward 100 spends the headroom that absorbs traffic spikes. Applies next tick"},
			"min_scan_rate_milli":        {Type: "int", Min: 10, Max: 2_000, Default: sensing["min_scan_rate_milli"], Unit: "milli-req/sec", Description: "floor the scan pacer is clamped up to, in thousandths of a request/sec (100 = 0.1 req/s). It is what guarantees planner market data never goes fully dark under API pressure, and it doubles as the residual below which expansion pauses. Applies next tick"},
			"value_clamp_r":              {Type: "int", Min: 1, Max: 16, Default: sensing["value_clamp_r"], Unit: "ratio", Description: "ceiling on how much more scan attention the hottest market may earn than the baseline; 1 flattens the weighting entirely. Binds when the scan rotation is built, so it applies at coordinator rebuild (restart/relaunch), not next tick"},
			"inflight_cap":               {Type: "int", Min: 1, Max: 8, Default: sensing["inflight_cap"], Unit: "scans", Description: "concurrent parked scans. It is the backpressure reflex: with every token held the pacer BLOCKS rather than queueing, so a degraded API throttles scan issuance at the source. Binds when the scan rotation is built, so it applies at coordinator rebuild (restart/relaunch), not next tick"},
			"capital_multiplier_k_milli": {Type: "int", Min: 0, Max: 10000, Default: sensing["capital_multiplier_k_milli"], Unit: "milli-hours", Description: "MILLI-hours of the TRADING fleet's measured cargo runway the probe buy floor holds back on top of the immutable 50k reserve — so a busier fleet automatically raises the floor. 2000=2h (default), 400=0.4h. Sub-hour matters: at fleet-scale cargo spend a whole hour can exceed the whole treasury. Applies next tick"},
			"capex_reserve_credits":      {Type: "int", Min: 0, Max: 5_000_000, Default: sensing["capex_reserve_credits"], Unit: "credits", Description: "credits the probe buy floor holds back for ship capex already committed elsewhere. Adds to the immutable 50k reserve; it never weakens it. Applies next tick"},
			"quartermaster_cadence_secs": {Type: "int", Min: 300, Max: 86_400, Default: sensing["quartermaster_cadence_secs"], Unit: "seconds", Description: "a YARD slot's re-read interval. A FLOOR on its scan interval, never a target: hull prices move on their own schedule, so the budget may slow a yard down but never speed it past this. Applies next tick"},

			"pressure_half_life_secs": {Type: "int", Min: 1, Max: 3_600, Default: sensing["pressure_half_life_secs"], Unit: "seconds", Description: "smoothing half-life of the API limiter-pressure EWMA the emergency brake reads. Boot value comes from config.yaml [daemon] limiter_pressure_half_life_seconds; a tuned value persists and is applied whenever the coordinator is rebuilt (restart/relaunch)"},
		},
		string(container.ContainerTypeScoutPostCoordinator): {
			"manning_stall_cycles":             {Type: "int", Min: 1, Max: 1440, Default: scoutPost["manning_stall_cycles"], Unit: "cycles", Description: "MINIMUM consecutive stale reconcile cycles before a silent fully-manned post is re-manned; each post's window is raised to its own circuit period (the soonest its worst-case market age could improve), so tuning this only ever lengthens the wait"},
			"manning_stall_correction_cap":     {Type: "int", Min: 1, Max: 100, Default: scoutPost["manning_stall_correction_cap"], Unit: "corrections", Description: "re-mans of one silent post before the watchdog backs off to the captain event"},
			"scout_cross_system_relay_enabled": {Type: "int", Min: 0, Max: 1, Default: scoutPost["scout_cross_system_relay_enabled"], Unit: "flag", Description: "sp-u8jc cross-system reuse-relay master switch: 1 ⇒ when a declared post has no in-system OR idle probe, borrow ONE surplus probe from an OVER-COVERED source system (manning supply > freshsizer demand) and relay it cross-system to the post (mans the dense unscanned hubs), 0 (default) ⇒ in-system + idle-reposition-only, byte-identical. Requires the daemon to have wired the probe-demand reader"},
			"scout_relay_max_hops":             {Type: "int", Min: 1, Max: 12, Default: scoutPost["scout_relay_max_hops"], Unit: "hops", Description: "sp-u8jc max gate-hops the cross-system reuse relay may move a surplus probe (probes are fuel_cap=0 gate-users, so reach is a router bound, not physical). Inert while scout_cross_system_relay_enabled=0"},
		},
		string(container.ContainerTypeContractFleetCoordinator): {
			"min_home_contract_workers": {Type: "int", Min: 0, Max: 200, Default: contract["min_home_contract_workers"], Unit: "hulls", Description: "undedicated home general haulers the depot topology never pins as depot-delivery — the contract-worker reserve floor for unbuffered-good sourcing"},
		},
		string(container.ContainerTypeShipyardBackfillCoordinator): {
			"max_dispatches_per_cycle": {Type: "int", Min: 1, Max: 100, Default: shipyardBackfill["max_dispatches_per_cycle"], Unit: "posts", Description: "per-cycle cap on sweep-once posts the shipyard-backfill sweep declares (bounded further by idle probe supply) so it drains the blind spot over cycles instead of flooding the reconciler"},
			"backfill_max_hops":        {Type: "int", Min: 1, Max: 1000, Default: shipyardBackfill["backfill_max_hops"], Unit: "hops", Description: "enumeration REACH — how deep into the gate graph the sweep hunts charted-but-unscanned shipyards; a charted shipyard is in-graph + relay-reachable so the default is the full gate graph (sp-b8lf), tune DOWN only to cap per-cycle enumeration cost"},
		},
		// sp-r6yq: the captain bootstrap coordinator (workflow bootstrap; COLDSTART→GATE→EXPANSION). It is
		// the first CONFIG.YAML-AUTHORITATIVE coordinator in the registry, so its tune key is the SEPARATE
		// BARE family — NOT the prefixed bootstrap_* launch keys, which resolveBootstrapConfig
		// clears+reinjects from config.yaml on every rebuild. A bare tune therefore survives a daemon bounce
		// (the coordinator's per-tick liveconfig reader keeps applying it) instead of being wiped. The
		// cold-start SHAPE is fixed in the coordinator, so the cadence is the only runtime lever.
		string(container.ContainerTypeBootstrapCoordinator): {
			"tick_secs": {Type: "int", Min: 10, Max: 86_400, Default: bootstrap["tick_secs"], Unit: "seconds", Description: "reconcile cadence — kept SHORT because bootstrap runs only at cold start (<0.1 req/s, 20x+ API headroom) and a fast tick cuts poll-latency dead time before the gate (default 45s; sp-lgo3)"},
		},
	}
}

// resolveTunableContainer locates the tune target: by container id (must be an
// active — PENDING/RUNNING — container; a STOPPED container has no running loop to
// retune, and tuning it would silently arm a value for some future restart), or by
// operation alias via FindActiveCoordinatorByType (the same lookup MutateStandbyStation
// uses, freshest-heartbeat row wins).
func (s *DaemonServer) resolveTunableContainer(ctx context.Context, containerID, operation string, playerID int) (*persistence.ContainerModel, error) {
	if containerID != "" {
		model, err := s.containerRepo.Get(ctx, containerID, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to locate container %s: %w", containerID, err)
		}
		if model == nil {
			return nil, fmt.Errorf("no container %s for player %d", containerID, playerID)
		}
		if model.Status != string(container.ContainerStatusPending) && model.Status != string(container.ContainerStatusRunning) {
			return nil, fmt.Errorf("container %s is %s — tune targets a RUNNING/PENDING container's live loop", containerID, model.Status)
		}
		return model, nil
	}
	if operation != "" {
		coordType, ok := tuneOperationCoordinatorTypes[operation]
		if !ok {
			return nil, fmt.Errorf("operation %q has no tunable coordinator (supported: %s)", operation, joinSortedKeys(tuneOperationCoordinatorTypes))
		}
		model, err := s.containerRepo.FindActiveCoordinatorByType(ctx, coordType, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to locate the running %s coordinator: %w", operation, err)
		}
		if model == nil {
			return nil, fmt.Errorf("no running %s coordinator for player %d — start one before tuning it", operation, playerID)
		}
		return model, nil
	}
	return nil, fmt.Errorf("a container id or an --operation alias is required to resolve the tune target")
}

// tuneEffective resolves a knob's effective value + source from a config map: a
// positive value in the config column is the live value (launch values share the
// column, so an untuned launch value reads as live-config too); anything else means
// the documented default applies.
func tuneEffective(config map[string]interface{}, key string, bound TuneBound) (int, string) {
	if v, ok := intValue(config[key]); ok && v > 0 {
		return v, "live-config"
	}
	return bound.Default, "default"
}

// mutateTuneConfigKey applies one tune to a container-config map IN PLACE and
// reports whether it changed. Pure over the map (the MutateStandbyStation /
// MutateFactoryWorkerCap shape): value > 0 sets the key; value == 0 REVERTS it —
// the key is deleted so the coordinator's default chain applies on the next tick
// and on every restart rebuild. changed=false (same value, or revert of an
// already-unset key) lets the caller skip the DB write and report honestly.
func mutateTuneConfigKey(config map[string]interface{}, key string, value int) bool {
	current, hadCurrent := intValue(config[key])
	if value == 0 {
		if !hadCurrent || current <= 0 {
			delete(config, key) // normalize a lingering 0 without calling it a change
			return false
		}
		delete(config, key)
		return true
	}
	config[key] = value
	return !hadCurrent || current != value
}

// MutateContainerConfigKey sets (or, with value 0, reverts) ONE live knob on an
// active container's persisted config column (sp-vwek) and returns the old→new
// effective values. It generalizes MutateFactoryWorkerCap: locate the container (by
// id, or by operation alias), validate the key + value against the static bounds
// registry — an out-of-bounds or unknown-key tune is REJECTED before any write —
// then read-modify-write just the config column via the race-free
// UpdateContainerConfig (the daemon as single writer, RULINGS #3). The running
// coordinator snapshots its config at each tick start (liveconfig.Reader), so the
// change lands on the NEXT tick with no restart; restart recovery rebuilds from the
// same column, so it survives a daemon bounce verbatim (RULINGS #2). Every
// EFFECTIVE tune emits a config.tuned audit event — these knobs move real credits,
// no silent writes.
func (s *DaemonServer) MutateContainerConfigKey(ctx context.Context, containerID, operation, key string, value, playerID int) (*TuneOutcome, error) {
	if value < 0 {
		return nil, fmt.Errorf("tune value must be >= 0 (got %d) — 0 reverts the key to its documented default", value)
	}

	model, err := s.resolveTunableContainer(ctx, containerID, operation, playerID)
	if err != nil {
		return nil, err
	}

	knobs, ok := tunableKnobsByContainerType()[model.ContainerType]
	if !ok {
		return nil, fmt.Errorf("container %s is a %s, which has no live-tunable knobs yet (tunable operations: %s)", model.ID, model.ContainerType, joinSortedKeys(tuneOperationCoordinatorTypes))
	}
	bound, ok := knobs[key]
	if !ok {
		return nil, fmt.Errorf("%q is not a tunable knob of %s — tunable keys: %s", key, model.ContainerType, joinSortedKeys(knobs))
	}
	if value > 0 && (value < bound.Min || value > bound.Max) {
		return nil, fmt.Errorf("%s=%d is outside its bounds [%d, %d] %s — rejected, nothing written", key, value, bound.Min, bound.Max, bound.Unit)
	}

	config := map[string]interface{}{}
	if model.Config != "" {
		if err := json.Unmarshal([]byte(model.Config), &config); err != nil {
			return nil, fmt.Errorf("failed to parse container %s config: %w", model.ID, err)
		}
	}

	oldEffective, oldSource := tuneEffective(config, key, bound)
	changed := mutateTuneConfigKey(config, key, value)
	newEffective, newSource := tuneEffective(config, key, bound)

	out := &TuneOutcome{
		ContainerID: model.ID, ContainerType: model.ContainerType, Key: key,
		OldEffective: oldEffective, OldSource: oldSource,
		NewEffective: newEffective, NewSource: newSource,
		Unit: bound.Unit, DefaultValue: bound.Default, Changed: changed,
	}
	if !changed {
		return out, nil // idempotent verb: no DB write, no audit — nothing happened
	}

	merged, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize container %s config after tune: %w", model.ID, err)
	}
	if err := s.containerRepo.UpdateContainerConfig(ctx, model.ID, playerID, string(merged)); err != nil {
		return nil, fmt.Errorf("failed to persist tune to container %s config: %w", model.ID, err)
	}

	// Audit: a credit-moving knob change is never a silent DB write (sp-vwek §3.5).
	// Deferred captain event — rides the next wake, forces none.
	recordCaptainEvent(captain.EventConfigTuned, "", playerID, map[string]any{
		"container_id":    out.ContainerID,
		"container_type":  out.ContainerType,
		"key":             key,
		"old_effective":   out.OldEffective,
		"old_source":      out.OldSource,
		"new_effective":   out.NewEffective,
		"new_source":      out.NewSource,
		"requested_value": value,
		"unit":            out.Unit,
	})

	return out, nil
}

// ShowTunableConfig lists every registered knob of an active container with its
// EFFECTIVE value, its source, and its bounds — the minimal `tune --show` for the
// migrated engines (full-coverage --show is sp-kv27). Source honesty: launch values
// and tunes share the config column, so a positive column value reads as
// "live-config" whether the launch verb or a tune wrote it; "default" means the
// documented default applies.
func (s *DaemonServer) ShowTunableConfig(ctx context.Context, containerID, operation string, playerID int) (*TuneShowOutcome, error) {
	model, err := s.resolveTunableContainer(ctx, containerID, operation, playerID)
	if err != nil {
		return nil, err
	}
	knobs, ok := tunableKnobsByContainerType()[model.ContainerType]
	if !ok {
		return nil, fmt.Errorf("container %s is a %s, which has no live-tunable knobs yet", model.ID, model.ContainerType)
	}

	config := map[string]interface{}{}
	if model.Config != "" {
		if err := json.Unmarshal([]byte(model.Config), &config); err != nil {
			return nil, fmt.Errorf("failed to parse container %s config: %w", model.ID, err)
		}
	}

	out := &TuneShowOutcome{ContainerID: model.ID, ContainerType: model.ContainerType}
	keys := make([]string, 0, len(knobs))
	for key := range knobs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		bound := knobs[key]
		effective, source := tuneEffective(config, key, bound)
		out.Knobs = append(out.Knobs, TunableKnobStatus{Key: key, Effective: effective, Source: source, Bound: bound})
	}
	return out, nil
}

// joinSortedKeys renders a map's keys as a sorted, comma-separated list for
// operator-facing error messages.
func joinSortedKeys[V any](m map[string]V) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// ContainerConfigReader implements liveconfig.Reader over the container repository —
// the read side of the tune mechanism, mirroring FactoryWorkerCapConfigProvider's
// container-repo backing: the coordinator snapshots its OWN persisted config column
// at each tick start, so a `tune` write is honored on the next tick with no restart.
type ContainerConfigReader struct {
	containerRepo *persistence.ContainerRepositoryGORM
}

// NewContainerConfigReader wires the container-config-backed live snapshot source.
func NewContainerConfigReader(containerRepo *persistence.ContainerRepositoryGORM) *ContainerConfigReader {
	return &ContainerConfigReader{containerRepo: containerRepo}
}

var _ liveconfig.Reader = (*ContainerConfigReader)(nil)

// Snapshot returns the container's current persisted config. A missing row errors
// (so the coordinator falls back to its launch command rather than silently running
// on an empty config — the FactoryWorkerCapConfigProvider discipline); an empty
// config is a valid empty snapshot.
func (r *ContainerConfigReader) Snapshot(ctx context.Context, containerID string, playerID int) (liveconfig.Snapshot, error) {
	model, err := r.containerRepo.Get(ctx, containerID, playerID)
	if err != nil {
		return nil, fmt.Errorf("read container %s for live config snapshot: %w", containerID, err)
	}
	if model == nil {
		return nil, fmt.Errorf("container %s not found for live config snapshot", containerID)
	}
	if model.Config == "" {
		return liveconfig.Snapshot{}, nil
	}
	config := map[string]interface{}{}
	if err := json.Unmarshal([]byte(model.Config), &config); err != nil {
		return nil, fmt.Errorf("parse container %s config for live snapshot: %w", containerID, err)
	}
	return liveconfig.Snapshot(config), nil
}
