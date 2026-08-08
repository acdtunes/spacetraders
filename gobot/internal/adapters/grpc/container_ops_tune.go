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
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// The daemon side of the GENERIC runtime tune mechanism (sp-vwek), generalizing the
// sp-ev0n per-knob pattern (MutateFactoryWorkerCap + FactoryWorkerCapConfigProvider)
// into one verb over a static bounds registry.

// TuneApplies says WHEN a persisted tune reaches the RUNNING coordinator, per KNOB:
// sensing re-reads most of its knobs each tick, but inflight_cap and value_clamp_r
// bind when the scan rotation is built. The zero value is UNSPECIFIED so an
// unclassified knob reports as undeclared instead of inheriting the confident live
// answer; TestTuneRegistry_EveryKnobDeclaresWhenItApplies fails the build on one.
type TuneApplies int

const (
	// TuneAppliesUnspecified: undeclared. Claim nothing beyond "persisted".
	TuneAppliesUnspecified TuneApplies = iota
	// TuneAppliesLive: a live-config reader re-reads the knob per tick, no restart.
	TuneAppliesLive
	// TuneAppliesOnRebuild: bound at construction; the loop keeps its launch value.
	TuneAppliesOnRebuild
)

// TuneBound is one knob's bounds-registry entry: the validity range a tune must fall
// in, the documented default that applies when the key is unset, when a tune of it
// reaches the running loop, and the operator metadata --show prints.
type TuneBound struct {
	Type        string
	Min         int
	Max         int
	Default     int
	Unit        string
	Applies     TuneApplies
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
	Applies       TuneApplies
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
	"growth":           string(container.ContainerTypeFleetGrowth),
	// The trade fleet coordinator owns the tour path's market-freshness floors
	// (sp-k4z5b). "tour" rather than "tradefleet" because the knobs govern what a TOUR
	// will still price off, and "tour" is the word the operator reaches for mid-incident.
	"tour": string(container.ContainerTypeTradeFleetCoordinator),
}

func tunableKnobsByContainerType() map[string]map[string]TuneBound {
	sensing := scoutingCmd.SensingTunableDefaults()
	scoutPost := scoutingCmd.ScoutPostTunableDefaults()
	contract := ContractCoordinatorTunableDefaults()
	autoOutfit := autooutfitCmd.AutoOutfitTunableDefaults()
	shipyardBackfill := scoutingCmd.ShipyardBackfillTunableDefaults()
	bootstrap := bootstrapCmd.BootstrapTunableDefaults()
	contractScaler := contractScalerCmd.ContractScalerTunableDefaults()
	fleetGrowth := fleetCmd.FleetGrowthTunableDefaults()
	tradeFleet := tradingCmd.TradeFleetTunableDefaults()
	return map[string]map[string]TuneBound{
		// The fleet-growth coordinator: the fleet's only heavy buyer. Three live levers — the
		// master switch, the ceiling on owned heavy hulls, and the working-capital runway. The
		// money guards beside them (the immutable 50k reserve floor and the 25%-treasury rule) are
		// compile-time consts and are deliberately NOT tunable.
		//
		// EVERY MIN HERE IS 1, INCLUDING THE TWO COUNT KNOBS. `tune <key> 0` is the revert-to-default
		// VERB fleet-wide, not a value, so 0 reaches none of these knobs — and the bound is
		// machine-readable (it crosses the wire to `tune --show` and any validator built on it), so a
		// Min of 0 would advertise a setting the writer refuses. 1 is the smallest each one accepts.
		string(container.ContainerTypeFleetGrowth): {
			"growth_enabled":            {Type: "int", Min: 1, Max: 2, Default: fleetGrowth["growth_enabled"], Unit: "flag", Applies: TuneAppliesLive, Description: "fleet-growth MASTER SWITCH: 1=on (default), 2=off. NOT 0/1 — `tune <key> 0` means revert-to-default fleet-wide, so 0 would make 'off' unexpressible. OFF STOPS THE READS, not just the buying: no shipyard price walk, no demand read, no pricing errand, no purchase — the walk runs BEFORE the guards can block it, so a blocked decision costs the same request budget as an approved one. OFF ALSO FORCES THE WAVE TO PROBE, so probe buying resumes rather than pausing for a buyer that cannot buy. It is NOT a money guard: the immutable 50k floor and the working-capital term are consts/derived and apply whenever growth is on. Applies next tick, no restart"},
			"heavy_cap":                 {Type: "int", Min: 1, Max: 50, Default: fleetGrowth["heavy_cap"], Unit: "hulls", Applies: TuneAppliesLive, Description: "ceiling on owned HEAVY HULLS (capital exposure), counted FLEET-WIDE regardless of dedicated_fleet tag. The only count-based bound on the class; every other bound is economic. At or over it the wave is PROBE. NOTE: `tune heavy_cap 0` DELETES the key and reverts to the default, so ZERO IS NOT EXPRESSIBLE HERE — to own no heavies use the master switch (`tune growth_enabled 2`), which also stops the reads. Applies next tick"},
			"growth_runway_milli_hours": {Type: "int", Min: 1, Max: 10000, Default: fleetGrowth["growth_runway_milli_hours"], Unit: "milli-hours", Applies: TuneAppliesLive, Description: "MILLI-hours of the TRADING fleet's UNRECOVERED cargo position (cargo bought minus cargo sold back over the same window) a heavy purchase holds back ON TOP OF the immutable 50k reserve. 2000=2h (default), 400=0.4h. It is a POSITION, not gross spend: a fleet recycling its float several times an hour spends the same credits again on every recycle, so the gross measure grows as trading improves and holds back multiples of the capital actually committed. This term is only ever one arm — read growth_working_capital_binding before retuning it, because when the fleet recovers everything it spends the reserve comes from the hold-fill arm instead and this knob moves nothing. It only ever ADDS to the floor and can never lower it. `tune <key> 0` DELETES the key and reverts to the default, so the smallest hold is 1, not 0. Applies next tick"},
		},
		string(container.ContainerTypeContractScaler): {
			// The single operator lever on the dedicated contract auto-scaler: the contract operation's hull
			// ceiling, hot-reloaded each tick (Pattern-C). It doubles as bootstrap's GATE-entry bar, so the
			// shipped default is sized for time-to-gate (a small operation funds the gate sooner) and is
			// raised live once the gate is built — delivery saturates ~7-8. Min 0 reverts to the default; the
			// sole money guard (the 200000 cushion) is a const, not tunable.
			"contract_fleet_max_hulls": {Type: "int", Min: 0, Max: 16, Default: contractScaler["contract_fleet_max_hulls"], Unit: "hulls", Applies: TuneAppliesLive, Description: "the exclusive contract fleet's live-tunable ceiling (filled delivery-first, behind the 200000 cushion). Default 3 — the cold start's GATE-entry bar; raise it once the gate is built (delivery saturates ~7-8)"},
		},
		string(container.ContainerTypeAutoOutfitCoordinator): {
			"min_telemetry_samples":     {Type: "int", Min: 1, Max: 1000, Default: autoOutfit["min_telemetry_samples"], Unit: "legs", Applies: TuneAppliesLive, Description: "fail-closed thin-telemetry floor — a hull with fewer measured legs is never upgraded"},
			"price_ceiling":             {Type: "int", Min: 0, Max: 5_000_000, Default: autoOutfit["price_ceiling"], Unit: "credits", Applies: TuneAppliesLive, Description: "max module price the coordinator will pay per install"},
			"max_installs_per_tick":     {Type: "int", Min: 1, Max: 20, Default: autoOutfit["max_installs_per_tick"], Unit: "installs", Applies: TuneAppliesLive, Description: "per-tick install cap"},
			"payback_horizon_hours":     {Type: "int", Min: 1, Max: 8760, Default: autoOutfit["payback_horizon_hours"], Unit: "hours", Applies: TuneAppliesLive, Description: "absolute payback gate — cost must be recovered within this horizon (default 0 = off until per-hull throughput is wired)"},
			"max_treasury_fraction_pct": {Type: "int", Min: 1, Max: 100, Default: autoOutfit["max_treasury_fraction_pct"], Unit: "percent", Applies: TuneAppliesLive, Description: "a single module never exceeds this fraction of live treasury"},
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
			"tick_secs":    {Type: "int", Min: 10, Max: 86_400, Default: sensing["tick_secs"], Unit: "seconds", Applies: TuneAppliesLive, Description: "reconcile cadence — how often placements advance, the queue drains and the scan rotation is refreshed. Applies next tick"},
			"wait_low_ms":  {Type: "int", Min: 1, Max: 60_000, Default: sensing["wait_low_ms"], Unit: "milliseconds", Applies: TuneAppliesLive, Description: "smoothed limiter wait BELOW which the emergency brake recovers (x1.2/tick toward fully released). Applies next tick"},
			"wait_high_ms": {Type: "int", Min: 1, Max: 600_000, Default: sensing["wait_high_ms"], Unit: "milliseconds", Applies: TuneAppliesLive, Description: "smoothed limiter wait ABOVE which the emergency brake bites (halves per tick, floored at 0.1). Braking is deliberately faster than recovery. Applies next tick"},

			"probe_cap":                  {Type: "int", Min: 1, Max: 10_000, Default: sensing["probe_cap"], Unit: "hulls", Applies: TuneAppliesLive, Description: "hard ceiling on parked probe hulls the engine may own. A BACKSTOP against a runaway placement plan, not the growth dial — the binding constraint on fleet size is the buy floor, i.e. money. Applies next tick"},
			"expansion_enabled":          {Type: "int", Min: 1, Max: 2, Default: sensing["expansion_enabled"], Unit: "flag", Applies: TuneAppliesLive, Description: "probe SPENDING switch: 1=on, 2=off. NOT 0/1 — `tune <key> 0` means revert-to-default fleet-wide, so 0 would make 'off' unexpressible. OFF BUYS NOTHING: no probe for a coverage placement (operation_type='sensing coverage'), no charting seed. Expect `bought 0` on every cycle line, held at the expansion switch. Off does NOT stop the free work — frontier discovery, jump-gate reads, screening, re-tasking idle probes you already own, and gate-crossing footholds all keep running, so coverage keeps growing on the hulls already paid for. It is not the money guard: capex_reserve_credits and the probe cap are, and they apply either way. Applies next tick"},
			"target_util_pct":            {Type: "int", Min: 50, Max: 95, Default: sensing["target_util_pct"], Unit: "percent", Applies: TuneAppliesLive, Description: "share of the rate-limiter ceiling the WHOLE fleet aims to occupy; sensing takes what is left of it after every other source. The remainder is burst headroom — raising this toward 100 spends the headroom that absorbs traffic spikes. Applies next tick"},
			"min_scan_rate_milli":        {Type: "int", Min: 10, Max: 2_000, Default: sensing["min_scan_rate_milli"], Unit: "milli-req/sec", Applies: TuneAppliesLive, Description: "floor the scan pacer is clamped up to, in thousandths of a request/sec (100 = 0.1 req/s). It is what guarantees planner market data never goes fully dark under API pressure, and it doubles as the residual below which expansion pauses. Applies next tick"},
			"value_clamp_r":              {Type: "int", Min: 1, Max: 16, Default: sensing["value_clamp_r"], Unit: "ratio", Applies: TuneAppliesOnRebuild, Description: "ceiling on how much more scan attention the hottest market may earn than the baseline; 1 flattens the weighting entirely. Binds when the scan rotation is built, so it applies at coordinator rebuild (restart/relaunch), not next tick"},
			"inflight_cap":               {Type: "int", Min: 1, Max: 8, Default: sensing["inflight_cap"], Unit: "scans", Applies: TuneAppliesOnRebuild, Description: "concurrent parked scans. It is the backpressure reflex: with every token held the pacer BLOCKS rather than queueing, so a degraded API throttles scan issuance at the source. Binds when the scan rotation is built, so it applies at coordinator rebuild (restart/relaunch), not next tick"},
			"capital_multiplier_k_milli": {Type: "int", Min: 0, Max: 10000, Default: sensing["capital_multiplier_k_milli"], Unit: "milli-hours", Applies: TuneAppliesLive, Description: "MILLI-hours of the TRADING fleet's measured cargo runway the probe buy floor holds back on top of the immutable 50k reserve — so a busier fleet automatically raises the floor. 2000=2h (default), 400=0.4h. Sub-hour matters: at fleet-scale cargo spend a whole hour can exceed the whole treasury. Applies next tick"},
			"capex_reserve_credits":      {Type: "int", Min: 0, Max: 5_000_000, Default: sensing["capex_reserve_credits"], Unit: "credits", Applies: TuneAppliesLive, Description: "credits the probe buy floor holds back for ship capex already committed elsewhere. Adds to the immutable 50k reserve; it never weakens it. Applies next tick"},
			"quartermaster_cadence_secs": {Type: "int", Min: 300, Max: 86_400, Default: sensing["quartermaster_cadence_secs"], Unit: "seconds", Applies: TuneAppliesLive, Description: "a YARD slot's re-read interval. A FLOOR on its scan interval, never a target: hull prices move on their own schedule, so the budget may slow a yard down but never speed it past this. Applies next tick"},
			"surge_inflight_cap":         {Type: "int", Min: 1, Max: 64, Default: sensing["surge_inflight_cap"], Unit: "hulls", Applies: TuneAppliesLive, Description: "SENSING SURGE: how many surplus probes may be FLYING toward charted-but-unpriced systems at once (sp-zvywu). A standing concurrency bound, not a per-tick burst — each tick tops the in-flight population up to this and stops, so the surge's API cost cannot grow tick over tick. Under 10 by default because that is the placement machine's own per-tick action budget, which is what advances these flights. Buying is untouched: the surge only ever dispatches hulls already paid for, and it never takes a probe standing a sensing watch. Not an off switch — 0 reverts to the default. Applies next tick"},

			"pressure_half_life_secs": {Type: "int", Min: 1, Max: 3_600, Default: sensing["pressure_half_life_secs"], Unit: "seconds", Applies: TuneAppliesOnRebuild, Description: "smoothing half-life of the API limiter-pressure EWMA the emergency brake reads. Boot value comes from config.yaml [daemon] limiter_pressure_half_life_seconds; a tuned value persists and is applied whenever the coordinator is rebuilt (restart/relaunch)"},
		},
		string(container.ContainerTypeScoutPostCoordinator): {
			"manning_stall_cycles":             {Type: "int", Min: 1, Max: 1440, Default: scoutPost["manning_stall_cycles"], Unit: "cycles", Applies: TuneAppliesLive, Description: "MINIMUM consecutive stale reconcile cycles before a silent fully-manned post is re-manned; each post's window is raised to its own circuit period (the soonest its worst-case market age could improve), so tuning this only ever lengthens the wait"},
			"manning_stall_correction_cap":     {Type: "int", Min: 1, Max: 100, Default: scoutPost["manning_stall_correction_cap"], Unit: "corrections", Applies: TuneAppliesLive, Description: "re-mans of one silent post before the watchdog backs off to the captain event"},
			"scout_cross_system_relay_enabled": {Type: "int", Min: 0, Max: 1, Default: scoutPost["scout_cross_system_relay_enabled"], Unit: "flag", Applies: TuneAppliesLive, Description: "sp-u8jc cross-system reuse-relay master switch: 1 ⇒ when a declared post has no in-system OR idle probe, borrow ONE surplus probe from an OVER-COVERED source system (manning supply > freshsizer demand) and relay it cross-system to the post (mans the dense unscanned hubs), 0 (default) ⇒ in-system + idle-reposition-only, byte-identical. Requires the daemon to have wired the probe-demand reader"},
			"market_drift_max_age_secs":        {Type: "int", Min: 60, Max: 86_400, Default: scoutPost["market_drift_max_age_secs"], Unit: "seconds", Applies: TuneAppliesLive, Description: "sp-k4z5b AGE trigger on the debounced market-set re-cut: how long a partition drift may stay pending before a partitioned post re-cuts anyway (the threshold trigger fires first when enough markets drift). NOT a market-data freshness cap and deliberately not rotation-derived — it measures how long a DRIFT has been pending, which map growth does not invalidate. Applies next tick"},
			"scout_relay_max_hops":             {Type: "int", Min: 1, Max: 12, Default: scoutPost["scout_relay_max_hops"], Unit: "hops", Applies: TuneAppliesLive, Description: "sp-u8jc max gate-hops the cross-system reuse relay may move a surplus probe (probes are fuel_cap=0 gate-users, so reach is a router bound, not physical). Inert while scout_cross_system_relay_enabled=0"},
		},
		string(container.ContainerTypeContractFleetCoordinator): {
			"min_home_contract_workers": {Type: "int", Min: 0, Max: 200, Default: contract["min_home_contract_workers"], Unit: "hulls", Applies: TuneAppliesLive, Description: "undedicated home general haulers the depot topology never pins as depot-delivery — the contract-worker reserve floor for unbuffered-good sourcing"},
		},
		// sp-k4z5b / sp-ry4r8: the tour path's ONE market-freshness FLOOR. It is not the cap
		// it looks like — the effective cap is max(floor, the live scan rotation's own
		// anti-starvation bound), because the scan budget is a fixed req/s and a market's
		// interval is therefore an OUTPUT of budget / markets known. A flat minute count
		// silently invalidated as the map grew to 4,389 markets and cost ~87% of trade
		// throughput; the derived term now tracks the map on its own, and this knob exists so
		// an operator can widen further mid-incident WITHOUT a daemon bounce (each bounce
		// burns jump cooldowns). Tuning it BELOW the rotation bound is a deliberate no-op.
		//
		// It shipped as two keys (listing_max_age_minutes, sink_freshness_max_minutes) that
		// defaulted identically and took the identical derived term, so they resolved to the
		// same number in every reachable state; sp-ry4r8 collapsed them, because two knobs
		// that must be kept in sync to stay correct are worse than one.
		string(container.ContainerTypeTradeFleetCoordinator): {
			"market_data_max_age_minutes": {Type: "int", Min: 1, Max: 43_200, Default: tradeFleet["market_data_max_age_minutes"], Unit: "minutes", Applies: TuneAppliesLive, Description: "FLOOR on how old a cached market_data row may be before the tour stops trusting it. ONE number for both consumers: the freshListings paths (lookback, held-cargo offload, reposition + relocation pre-rank, distress liquidation, candidate shortlist, cross-system sink scan, stocker) AND the FRESH clause of the firm-sink buy gate (sp-tgll8), a fail-closed money guard — never buy into a sink we cannot confirm is live. Effective cap = max(this, rotation bound): raising it above the rotation bound widens the cap, below it is a deliberate no-op, so it can never be tuned tight enough to refuse a row the rotation explains, and never to zero — `tune ... 0` reverts to the documented default, it does NOT disarm the guard (RULINGS #4). Arming the guard remains a boot concern ([trade_fleet].sink_freshness_max_minutes). Applies next tick (the cap is re-read from this column at most every 5s), no restart"},
		},
		string(container.ContainerTypeShipyardBackfillCoordinator): {
			"max_dispatches_per_cycle": {Type: "int", Min: 1, Max: 100, Default: shipyardBackfill["max_dispatches_per_cycle"], Unit: "posts", Applies: TuneAppliesLive, Description: "per-cycle cap on sweep-once posts the shipyard-backfill sweep declares (bounded further by idle probe supply) so it drains the blind spot over cycles instead of flooding the reconciler"},
			"backfill_max_hops":        {Type: "int", Min: 1, Max: 1000, Default: shipyardBackfill["backfill_max_hops"], Unit: "hops", Applies: TuneAppliesLive, Description: "enumeration REACH — how deep into the gate graph the sweep hunts charted-but-unscanned shipyards; a charted shipyard is in-graph + relay-reachable so the default is the full gate graph (sp-b8lf), tune DOWN only to cap per-cycle enumeration cost"},
		},
		// The captain bootstrap coordinator (workflow bootstrap; COLDSTART→GATE→EXPANSION). It is
		// the first CONFIG.YAML-AUTHORITATIVE coordinator in the registry, so its tune key is the SEPARATE
		// BARE family — NOT the prefixed bootstrap_* launch keys, which resolveBootstrapConfig
		// clears+reinjects from config.yaml on every rebuild. A bare tune therefore survives a daemon bounce
		// (the coordinator's per-tick liveconfig reader keeps applying it) instead of being wiped. The
		// cold-start SHAPE is fixed in the coordinator, so the cadence is the only runtime lever.
		string(container.ContainerTypeBootstrapCoordinator): {
			"tick_secs": {Type: "int", Min: 10, Max: 86_400, Default: bootstrap["tick_secs"], Unit: "seconds", Applies: TuneAppliesLive, Description: "reconcile cadence — kept SHORT because bootstrap runs only at cold start (<0.1 req/s, 20x+ API headroom) and a fast tick cuts poll-latency dead time before the gate (default 45s; sp-lgo3)"},
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
		Applies: bound.Applies,
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

// CoordinatorConfigReader snapshots the persisted config of a player's ACTIVE
// coordinator OF A GIVEN TYPE, rather than of a container the caller names.
//
// It exists for the daemon-global handlers. The tour handler is ONE instance serving
// every TRADING container the trade fleet coordinator launches, so it has no
// container id of its own to read a tune from — but the standing coordinator that
// owns its knobs has exactly one active row per player, and that is the row `tune
// --operation tour` writes to. Resolving by type is therefore reading precisely what
// the operator just wrote, through the same FindActiveCoordinatorByType lookup the
// tune verb itself used to find the target.
//
// No active coordinator is NOT an error: it is a player whose trade fleet is not
// running, which must leave every caller on its boot floor rather than erroring on a
// hot path.
type CoordinatorConfigReader struct {
	containerRepo   *persistence.ContainerRepositoryGORM
	coordinatorType string
}

// NewCoordinatorConfigReader wires a by-type live snapshot source.
func NewCoordinatorConfigReader(containerRepo *persistence.ContainerRepositoryGORM, coordinatorType string) *CoordinatorConfigReader {
	return &CoordinatorConfigReader{containerRepo: containerRepo, coordinatorType: coordinatorType}
}

// Snapshot returns the active coordinator's current persisted config, or an empty
// snapshot when no coordinator of that type is running.
func (r *CoordinatorConfigReader) Snapshot(ctx context.Context, playerID int) (liveconfig.Snapshot, error) {
	model, err := r.containerRepo.FindActiveCoordinatorByType(ctx, r.coordinatorType, playerID)
	if err != nil {
		return nil, fmt.Errorf("find active %s for live config snapshot: %w", r.coordinatorType, err)
	}
	if model == nil || model.Config == "" {
		return liveconfig.Snapshot{}, nil
	}
	config := map[string]interface{}{}
	if err := json.Unmarshal([]byte(model.Config), &config); err != nil {
		return nil, fmt.Errorf("parse %s config for live snapshot: %w", r.coordinatorType, err)
	}
	return liveconfig.Snapshot(config), nil
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
