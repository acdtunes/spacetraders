package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	autooutfitCmd "github.com/andrescamacho/spacetraders-go/internal/application/autooutfit"
	expansionCmd "github.com/andrescamacho/spacetraders-go/internal/application/expansion/commands"
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
	"freshsizer":       string(container.ContainerTypeMarketFreshnessSizer),
	"frontier":         string(container.ContainerTypeFrontierExpansion),
	"scoutpost":        string(container.ContainerTypeScoutPostCoordinator),
	"contract":         string(container.ContainerTypeContractFleetCoordinator),
	"autooutfit":       string(container.ContainerTypeAutoOutfitCoordinator),
	"shipyardbackfill": string(container.ContainerTypeShipyardBackfillCoordinator),
}

func tunableKnobsByContainerType() map[string]map[string]TuneBound {
	sizer := scoutingCmd.SizerTunableDefaults()
	frontier := expansionCmd.FrontierTunableDefaults()
	scoutPost := scoutingCmd.ScoutPostTunableDefaults()
	contract := ContractCoordinatorTunableDefaults()
	autoOutfit := autooutfitCmd.AutoOutfitTunableDefaults()
	shipyardBackfill := scoutingCmd.ShipyardBackfillTunableDefaults()
	return map[string]map[string]TuneBound{
		string(container.ContainerTypeAutoOutfitCoordinator): {
			"min_telemetry_samples":     {Type: "int", Min: 1, Max: 1000, Default: autoOutfit["min_telemetry_samples"], Unit: "legs", Description: "fail-closed thin-telemetry floor — a hull with fewer measured legs is never upgraded"},
			"price_ceiling":             {Type: "int", Min: 0, Max: 5_000_000, Default: autoOutfit["price_ceiling"], Unit: "credits", Description: "max module price the coordinator will pay per install"},
			"max_installs_per_tick":     {Type: "int", Min: 1, Max: 20, Default: autoOutfit["max_installs_per_tick"], Unit: "installs", Description: "per-tick install cap"},
			"payback_horizon_hours":     {Type: "int", Min: 1, Max: 8760, Default: autoOutfit["payback_horizon_hours"], Unit: "hours", Description: "absolute payback gate — cost must be recovered within this horizon (default 0 = off until per-hull throughput is wired)"},
			"treasury_reserve":          {Type: "int", Min: 0, Max: 5_000_000, Default: autoOutfit["treasury_reserve"], Unit: "credits", Description: "working-capital floor an install must never breach"},
			"max_treasury_fraction_pct": {Type: "int", Min: 1, Max: 100, Default: autoOutfit["max_treasury_fraction_pct"], Unit: "percent", Description: "a single module never exceeds this fraction of live treasury"},
		},
		string(container.ContainerTypeMarketFreshnessSizer): {
			"max_spend_per_cycle":        {Type: "int", Min: 0, Max: 5_000_000, Default: sizer["max_spend_per_cycle"], Unit: "credits", Description: "max probe spend within the trailing spend window"},
			"purchase_cooldown_secs":     {Type: "int", Min: 10, Max: 86_400, Default: sizer["purchase_cooldown_secs"], Unit: "seconds", Description: "min wall-clock between probe buys"},
			"spend_window_secs":          {Type: "int", Min: 10, Max: 86_400, Default: sizer["spend_window_secs"], Unit: "seconds", Description: "trailing window the spend cap sums over"},
			"max_probe_fleet":            {Type: "int", Min: 0, Max: 200, Default: sizer["max_probe_fleet"], Unit: "hulls", Description: "total satellite cap"},
			"max_probes_per_system":      {Type: "int", Min: 0, Max: 200, Default: sizer["max_probes_per_system"], Unit: "hulls", Description: "per-system hull cap"},
			"sla_seconds":                {Type: "int", Min: 10, Max: 86_400, Default: sizer["sla_seconds"], Unit: "seconds", Description: "freshness SLA the sizer sizes against"},
			"target_percentile":          {Type: "int", Min: 1, Max: 100, Default: sizer["target_percentile"], Unit: "percentile", Description: "sp-r57g age percentile the sizer sizes against — a system breaches iff its measured P90 exceeds the SLA, not its max (tail tolerated); 100 = pre-sp-r57g max-age behavior"},
			"value_weighted":             {Type: "int", Min: 1, Max: 2, Default: sizer["value_weighted"], Unit: "mode", Description: "sp-r57g value-weighting mode: 2=on (percentile weighted by per-market trade_volume×price, arb core stays tight), 1=off (plain count percentile)"},
			"worst_cycle_seconds":        {Type: "int", Min: 60, Max: 86_400, Default: sizer["worst_cycle_seconds"], Unit: "seconds", Description: "worst-plausible per-market cycle bounding the market-count clamp ceiling"},
			"cycle_dampening_percent":    {Type: "int", Min: 1, Max: 100, Default: sizer["cycle_dampening_percent"], Unit: "percent", Description: "shrinkage of a system's own noisy per-market cycle toward the fleet median"},
			"breach_response_percent":    {Type: "int", Min: 1, Max: 500, Default: sizer["breach_response_percent"], Unit: "percent", Description: "aggressiveness of the circuit-observed breach response (scales the observed age fed to the circuit sizing; 100 = exact measured circuit, >100 buys headroom)"},
			"release_slack_percent":      {Type: "int", Min: 1, Max: 100, Default: sizer["release_slack_percent"], Unit: "percent", Description: "release hysteresis as a percent of the SLA"},
			"release_stable_window_secs": {Type: "int", Min: 10, Max: 86_400, Default: sizer["release_stable_window_secs"], Unit: "seconds", Description: "how long a warm surplus must hold before a probe is shed"},
			"reserved_frontier_floor":    {Type: "int", Min: 0, Max: 200, Default: sizer["reserved_frontier_floor"], Unit: "hulls", Description: "sp-iopd MVP: probes reserved for the frontier — the sizer holds its aggregate against (supply − this) and releases the surplus; 0 = off (pre-sp-iopd)"},
		},
		string(container.ContainerTypeFrontierExpansion): {
			"max_spend_per_cycle":        {Type: "int", Min: 0, Max: 5_000_000, Default: frontier["max_spend_per_cycle"], Unit: "credits", Description: "max probe spend within the trailing spend window"},
			"purchase_cooldown_secs":     {Type: "int", Min: 10, Max: 86_400, Default: frontier["purchase_cooldown_secs"], Unit: "seconds", Description: "min wall-clock between probe buys"},
			"max_probe_fleet":            {Type: "int", Min: 0, Max: 200, Default: frontier["max_probe_fleet"], Unit: "hulls", Description: "total satellite cap"},
			"proximal_yard_hop_penalty":  {Type: "int", Min: 0, Max: 5_000_000, Default: frontier["proximal_yard_hop_penalty"], Unit: "credits", Description: "price premium accepted per gate-hop closer to the target post when picking the probe yard (sp-hej4); 0 = cheapest reachable yard"},
			"probe_sibling_price_margin": {Type: "int", Min: 0, Max: 5_000_000, Default: frontier["probe_sibling_price_margin"], Unit: "credits", Description: "sp-iqv2 supply-depletion margin: spread the probe buy off a yard once a cheaper reachable sibling undercuts it by more than this (prevents one market spiraling to 4x); 0 = pure proximity (no spread)"},
			// sp-rjgr depth-vs-breadth balance — retune the outward drive live, no restart.
			"breadth_fraction_percent": {Type: "int", Min: 1, Max: 100, Default: frontier["breadth_fraction_percent"], Unit: "percent", Description: "breadth share of frontier capacity (depth = 100 - this; 100 ⇒ pure BFS)"},
			"max_depth_pathfinders":    {Type: "int", Min: 1, Max: 20, Default: frontier["max_depth_pathfinders"], Unit: "hulls", Description: "cap on concurrent depth-pathfinder posts"},
			"max_depth_hops":           {Type: "int", Min: 1, Max: 12, Default: frontier["max_depth_hops"], Unit: "hops", Description: "depth scan horizon + per-pathfinder max target depth (within relay reach)"},
			"objective_bias_percent":   {Type: "int", Min: 1, Max: 100, Default: frontier["objective_bias_percent"], Unit: "percent", Description: "points added to the depth fraction while the heavy-yard objective is unmet"},
			// sp-k645 off-gate explorer demand + target selection (slice B) — signal-only, live-tunable.
			"off_gate_queue_exhaustion_cycles": {Type: "int", Min: 1, Max: 1_000, Default: frontier["off_gate_queue_exhaustion_cycles"], Unit: "cycles", Description: "consecutive empty-queue cycles before off-gate explorer demand fires (trigger a debounce)"},
			"off_gate_warp_range_fuel":         {Type: "int", Min: 1, Max: 100_000, Default: frontier["off_gate_warp_range_fuel"], Unit: "fuel", Description: "max warp fuel a single explorer leg may cost; off-gate systems beyond this are out of range"},
			"off_gate_value_weight":            {Type: "int", Min: 0, Max: 1_000_000, Default: frontier["off_gate_value_weight"], Unit: "weight", Description: "weight on exploration value (promising-type unexplored systems) in off-gate target ranking"},
			"off_gate_fuel_weight":             {Type: "int", Min: 0, Max: 1_000_000, Default: frontier["off_gate_fuel_weight"], Unit: "weight", Description: "weight on warp fuel (distance from the frontier edge) in off-gate target ranking"},
			"reserved_freshness_floor":         {Type: "int", Min: 0, Max: 200, Default: frontier["reserved_freshness_floor"], Unit: "hulls", Description: "sp-iopd MVP: idle probes the frontier reserves for freshness — discounted from the supply covering its demand (buys rather than cannibalize scanning); 0 = off (pre-sp-iopd)"},
		},
		string(container.ContainerTypeScoutPostCoordinator): {
			"manning_stall_cycles":         {Type: "int", Min: 1, Max: 1440, Default: scoutPost["manning_stall_cycles"], Unit: "cycles", Description: "consecutive stale reconcile cycles before a silent fully-manned post is re-manned"},
			"manning_stall_correction_cap": {Type: "int", Min: 1, Max: 100, Default: scoutPost["manning_stall_correction_cap"], Unit: "corrections", Description: "re-mans of one silent post before the watchdog backs off to the captain event"},
		},
		string(container.ContainerTypeContractFleetCoordinator): {
			"min_home_contract_workers": {Type: "int", Min: 0, Max: 200, Default: contract["min_home_contract_workers"], Unit: "hulls", Description: "undedicated home general haulers the depot topology never pins as depot-delivery — the contract-worker reserve floor for unbuffered-good sourcing"},
		},
		string(container.ContainerTypeShipyardBackfillCoordinator): {
			"max_dispatches_per_cycle": {Type: "int", Min: 1, Max: 100, Default: shipyardBackfill["max_dispatches_per_cycle"], Unit: "posts", Description: "per-cycle cap on sweep-once posts the shipyard-backfill sweep declares (bounded further by idle probe supply) so it drains the blind spot over cycles instead of flooding the reconciler"},
			"backfill_max_hops":        {Type: "int", Min: 1, Max: 1000, Default: shipyardBackfill["backfill_max_hops"], Unit: "hops", Description: "enumeration REACH — how deep into the gate graph the sweep hunts charted-but-unscanned shipyards; a charted shipyard is in-graph + relay-reachable so the default is the full gate graph (sp-b8lf), tune DOWN only to cap per-cycle enumeration cost"},
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
		return nil, fmt.Errorf("container %s is a %s, which has no live-tunable knobs yet (tunable engines: freshness sizer, frontier expansion)", model.ID, model.ContainerType)
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
