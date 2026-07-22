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
	"bootstrap":        string(container.ContainerTypeBootstrapCoordinator),
	"contractscaler":   string(container.ContainerTypeContractScaler),
}

func tunableKnobsByContainerType() map[string]map[string]TuneBound {
	sizer := scoutingCmd.SizerTunableDefaults()
	frontier := expansionCmd.FrontierTunableDefaults()
	scoutPost := scoutingCmd.ScoutPostTunableDefaults()
	contract := ContractCoordinatorTunableDefaults()
	autoOutfit := autooutfitCmd.AutoOutfitTunableDefaults()
	shipyardBackfill := scoutingCmd.ShipyardBackfillTunableDefaults()
	bootstrap := bootstrapCmd.BootstrapTunableDefaults()
	contractScaler := contractScalerCmd.ContractScalerTunableDefaults()
	return map[string]map[string]TuneBound{
		string(container.ContainerTypeContractScaler): {
			// The single operator lever on the dedicated contract auto-scaler: the delivery-hull ceiling,
			// hot-reloaded each tick (Pattern-C). Keep LOW (default 2) during a gate sprint — contracts are
			// the funding floor, the gate needs the treasury — and raise to 10-12 post-gate for full
			// contract throughput (delivery hulls saturate ~7-8 = number of distinct central waypoints). Min
			// 0 reverts to the default; the sole money guard (the 200000 cushion) is a const, not tunable.
			"contract_fleet_max_hulls": {Type: "int", Min: 0, Max: 16, Default: contractScaler["contract_fleet_max_hulls"], Unit: "hulls", Description: "the exclusive contract fleet's live-tunable ceiling (delivery hulls; ramps behind the 200000 cushion). Low (2) during the gate sprint, 10-12 post-gate; saturates ~7-8"},
		},
		string(container.ContainerTypeAutoOutfitCoordinator): {
			"min_telemetry_samples":     {Type: "int", Min: 1, Max: 1000, Default: autoOutfit["min_telemetry_samples"], Unit: "legs", Description: "fail-closed thin-telemetry floor — a hull with fewer measured legs is never upgraded"},
			"price_ceiling":             {Type: "int", Min: 0, Max: 5_000_000, Default: autoOutfit["price_ceiling"], Unit: "credits", Description: "max module price the coordinator will pay per install"},
			"max_installs_per_tick":     {Type: "int", Min: 1, Max: 20, Default: autoOutfit["max_installs_per_tick"], Unit: "installs", Description: "per-tick install cap"},
			"payback_horizon_hours":     {Type: "int", Min: 1, Max: 8760, Default: autoOutfit["payback_horizon_hours"], Unit: "hours", Description: "absolute payback gate — cost must be recovered within this horizon (default 0 = off until per-hull throughput is wired)"},
			"treasury_reserve":          {Type: "int", Min: 0, Max: 5_000_000, Default: autoOutfit["treasury_reserve"], Unit: "credits", Description: "working-capital floor an install must never breach"},
			"max_treasury_fraction_pct": {Type: "int", Min: 1, Max: 100, Default: autoOutfit["max_treasury_fraction_pct"], Unit: "percent", Description: "a single module never exceeds this fraction of live treasury"},
		},
		string(container.ContainerTypeMarketFreshnessSizer): {
			"max_spend_per_cycle":         {Type: "int", Min: 0, Max: 5_000_000, Default: sizer["max_spend_per_cycle"], Unit: "credits", Description: "max probe spend within the trailing spend window"},
			"purchase_cooldown_secs":      {Type: "int", Min: 10, Max: 86_400, Default: sizer["purchase_cooldown_secs"], Unit: "seconds", Description: "min wall-clock between probe buys"},
			"spend_window_secs":           {Type: "int", Min: 10, Max: 86_400, Default: sizer["spend_window_secs"], Unit: "seconds", Description: "trailing window the spend cap sums over"},
			"max_probe_fleet":             {Type: "int", Min: 0, Max: 200, Default: sizer["max_probe_fleet"], Unit: "hulls", Description: "total satellite cap"},
			"max_probes_per_system":       {Type: "int", Min: 0, Max: 200, Default: sizer["max_probes_per_system"], Unit: "hulls", Description: "per-system hull cap"},
			"sla_seconds":                 {Type: "int", Min: 10, Max: 86_400, Default: sizer["sla_seconds"], Unit: "seconds", Description: "freshness SLA the sizer sizes against"},
			"target_percentile":           {Type: "int", Min: 1, Max: 100, Default: sizer["target_percentile"], Unit: "percentile", Description: "sp-r57g age percentile the sizer sizes against — a system breaches iff its measured P90 exceeds the SLA, not its max (tail tolerated); 100 = pre-sp-r57g max-age behavior"},
			"value_weighted":              {Type: "int", Min: 1, Max: 2, Default: sizer["value_weighted"], Unit: "mode", Description: "sp-r57g value-weighting mode: 2=on (percentile weighted by per-market trade_volume×price, arb core stays tight), 1=off (plain count percentile)"},
			"worst_cycle_seconds":         {Type: "int", Min: 60, Max: 86_400, Default: sizer["worst_cycle_seconds"], Unit: "seconds", Description: "worst-plausible per-market cycle bounding the market-count clamp ceiling"},
			"cycle_dampening_percent":     {Type: "int", Min: 1, Max: 100, Default: sizer["cycle_dampening_percent"], Unit: "percent", Description: "shrinkage of a system's own noisy per-market cycle toward the fleet median"},
			"breach_response_percent":     {Type: "int", Min: 1, Max: 500, Default: sizer["breach_response_percent"], Unit: "percent", Description: "aggressiveness of the circuit-observed breach response (scales the observed age fed to the circuit sizing; 100 = exact measured circuit, >100 buys headroom)"},
			"release_slack_percent":       {Type: "int", Min: 1, Max: 100, Default: sizer["release_slack_percent"], Unit: "percent", Description: "release hysteresis as a percent of the SLA"},
			"release_stable_window_secs":  {Type: "int", Min: 10, Max: 86_400, Default: sizer["release_stable_window_secs"], Unit: "seconds", Description: "how long a warm surplus must hold before a probe is shed"},
			"reserved_frontier_floor":     {Type: "int", Min: 0, Max: 200, Default: sizer["reserved_frontier_floor"], Unit: "hulls", Description: "sp-iopd MVP: probes reserved for the frontier — the sizer holds its aggregate against (supply − this) and releases the surplus; 0 = off (pre-sp-iopd)"},
			"hold_unscanned_market_posts": {Type: "int", Min: 0, Max: 1, Default: sizer["hold_unscanned_market_posts"], Unit: "flag", Description: "sp-u8jc/sp-gucu bootstrap-catch-22 fix: 1 ⇒ a CHARTED system whose waypoints carry the MARKETPLACE trait but that has NO scanned market_data yet is HELD (its standing post is not retired 'markets gone') and counted as one-probe initial-scan demand, so the coordinator mans it (in-system idle, sp-u8jc cross-system relay, or a probe buy) and it gets its first scan; 0 (default) ⇒ retire-as-gone, byte-identical. Requires the daemon to have wired the charted-marketplace reader"},
			"size_by_charted_markets":     {Type: "int", Min: 0, Max: 1, Default: sizer["size_by_charted_markets"], Unit: "flag", Description: "sp-7pdo cold-start scale-up: 1 ⇒ a market-bearing system whose CHARTED marketplace-waypoint count exceeds its SCANNED census count is SIZED against the charted count, so a home post part-way through its first circuit reaches its true RequiredHulls immediately (the coordinator then mans + VRP-partitions the idle probes) instead of crawling on one hull; the age/percentile telemetry stays on the scanned reality (only the market-count numerator changes); 0 (default) ⇒ size on the scanned count only, byte-identical. Requires the daemon to have wired the charted-marketplace reader"},
		},
		// sp-tlekc frontier overhaul: EXACTLY four operator knobs (22 → 4). max_probe_fleet is the sole
		// throttle; reach_mode + discover_scan_balance compose the reach + discover/scan split from
		// coherent presets; reserved_freshness_floor is the one retained expert guard (retired P4).
		// Everything else — the price ceiling (immutable 100k), the 50k working-capital floor, the
		// hop-penalty/sibling-margin sourcing terms, the 25% rule — is an internal const, not a knob.
		string(container.ContainerTypeFrontierExpansion): {
			"max_probe_fleet":          {Type: "int", Min: 0, Max: 200, Default: frontier["max_probe_fleet"], Unit: "hulls", Description: "total satellite cap — the SOLE frontier throttle. Set the fleet size; it fills one probe per tick as fast as the API + 25% rule + 50k floor safely allow, then stops. ~30 trickle / ~50 normal / 80 aggressive / ~150 flood"},
			"reach_mode":               {Type: "int", Min: 1, Max: 3, Default: frontier["reach_mode"], Unit: "mode", Description: "sp-tlekc frontier reach preset: 1=shallow (pure-BFS near ring, reuse off), 2=balanced (the live snapshot: 65/35 breadth/depth, reuse+snowball+reap armed), 3=deep (deeper hops, more pathfinders, wider reuse). Composes depth/breadth + reuse relay + off-gate + reap timeout from ONE coherent preset (couplings honored by construction)"},
			"discover_scan_balance":    {Type: "int", Min: 0, Max: 100, Default: frontier["discover_scan_balance"], Unit: "percent", Description: "sp-tlekc percent of each cycle's post-declaration capacity spent CHARTING VIRGIN systems; the rest (100-this) concurrently drains the dark-market backlog, with graceful degradation (a dry side yields its budget to the other). 100 = pure discovery, 40 = the live snapshot. Tuning 0 reverts to the default like every knob"},
			"reserved_freshness_floor": {Type: "int", Min: 0, Max: 200, Default: frontier["reserved_freshness_floor"], Unit: "hulls", Description: "sp-iopd MVP: idle probes the frontier reserves for freshness — discounted from the supply covering its demand (buys rather than cannibalize scanning); 0 = off. The one retained expert guard (retired when probe allocation unifies, sp-tlekc Phase 4)"},
		},
		string(container.ContainerTypeScoutPostCoordinator): {
			"manning_stall_cycles":             {Type: "int", Min: 1, Max: 1440, Default: scoutPost["manning_stall_cycles"], Unit: "cycles", Description: "consecutive stale reconcile cycles before a silent fully-manned post is re-manned"},
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
		// sp-r6yq: the captain bootstrap coordinator (workflow bootstrap; DATA→INCOME→GATE). It is the
		// first CONFIG.YAML-AUTHORITATIVE coordinator in the registry, so its tune keys are the SEPARATE
		// BARE family (probe_target, coverage_bar_percent, …) — NOT the prefixed bootstrap_* launch keys,
		// which resolveBootstrapConfig clears+reinjects from config.yaml on every rebuild. A bare tune
		// therefore survives a daemon bounce (the coordinator's per-tick liveconfig reader keeps applying
		// it) instead of being wiped. The int-only mechanism carries the two float knobs as integer
		// percents (coverage_bar_percent, reserve_margin_percent) and income_bar as whole credits; the
		// two ship-type knobs stay launch-only (no coordinator makes a string asset live-tunable).
		string(container.ContainerTypeBootstrapCoordinator): {
			"probe_target":           {Type: "int", Min: 1, Max: 50, Default: bootstrap["probe_target"], Unit: "hulls", Description: "DATA target: probes scouting the home system before the coverage bar can clear"},
			"coverage_bar_percent":   {Type: "int", Min: 1, Max: 100, Default: bootstrap["coverage_bar_percent"], Unit: "percent", Description: "home-system scan-coverage target, integer percent (the float coverage_bar; 90 = 0.90) — continuous background (freshness sizer), no longer gates phase progression"},
			"reserve_margin_percent": {Type: "int", Min: 1, Max: 100, Default: bootstrap["reserve_margin_percent"], Unit: "percent", Description: "capital gate: max percent of live treasury a single staged buy may spend (the float reserve_margin as an integer percent — 50 = 0.50)"},
			"hauler_target":          {Type: "int", Min: 1, Max: 50, Default: bootstrap["hauler_target"], Unit: "hulls", Description: "INCOME hull cap: at most one contract hauler per viable hub, up to this"},
			"income_bar":             {Type: "int", Min: 1, Max: 5_000_000, Default: bootstrap["income_bar"], Unit: "credits", Description: "INCOME→GATE exit: realized net credits/hour the contract fleet must clear (whole credits; the float income_bar carries no fractional part)"},
			"min_contract_earners":   {Type: "int", Min: 1, Max: 50, Default: bootstrap["min_contract_earners"], Unit: "hulls", Description: "haulers kept on contracts through GATE to keep funding gate-material acquisition"},
			"gate_worker_target":     {Type: "int", Min: 1, Max: 50, Default: bootstrap["gate_worker_target"], Unit: "hulls", Description: "GATE worker cap: ~one per active gate-material chain + a delivery hauler, up to this (mostly repurposed idle haulers, rarely a buy)"},
			"tick_secs":              {Type: "int", Min: 10, Max: 86_400, Default: bootstrap["tick_secs"], Unit: "seconds", Description: "reconcile cadence — kept SHORT because bootstrap runs only at cold start (<0.1 req/s, 20x+ API headroom) and a fast tick cuts poll-latency dead time before the gate (default 45s; sp-lgo3)"},
			// sp-tsn2 single-buyer arbitration flag: 1 ⇒ bootstrap DEFERS its DATA probe buy to the
			// freshsizer once coverage>0 and a freshsizer coordinator is running (so exactly one buyer grows
			// the shared fleet — the era-3 multi-buyer lesson); 0 (default) ⇒ today's behavior, both buy
			// behind their own guards. Bootstrap never defers into a vacuum (freshsizer must be running).
			"defer_probe_to_freshsizer": {Type: "int", Min: 0, Max: 1, Default: bootstrap["defer_probe_to_freshsizer"], Unit: "flag", Description: "sp-tsn2: 1 ⇒ bootstrap hands DATA probe acquisition to the freshsizer once the first market is covered and a freshsizer coordinator runs (single-buyer arbitration); 0 (default) ⇒ both buy independently (byte-identical)"},
			// sp-fp3y scaled-GATE-entry gate + sp-sjvv early-autosizer are DEFAULT-ON (sp-5nd2), armed via
			// config.yaml. The positive knobs below are FORCE-ON overrides (re-arm a config-disabled run);
			// the *_disabled knobs are the no-restart live kill-switches (disable wins). The two bars are
			// phase thresholds like income_bar, NOT money-floors (RULINGS #5).
			"scaled_gate_entry":                {Type: "int", Min: 0, Max: 1, Default: bootstrap["scaled_gate_entry"], Unit: "flag", Description: "sp-fp3y FORCE-ON override (feature is DEFAULT-ON via config.yaml): 1 ⇒ force the scaled GATE-entry gate armed even if config disabled it; 0 (default) ⇒ defer to the config default. To turn the gate OFF live, use scaled_gate_entry_disabled"},
			"scaled_gate_entry_disabled":       {Type: "int", Min: 0, Max: 1, Default: bootstrap["scaled_gate_entry_disabled"], Unit: "flag", Description: "sp-5nd2 live kill-switch: 1 ⇒ stand DOWN the (default-on) scaled GATE-entry gate next tick with no restart — GATE re-enters on the bare income_bar; 0 (default) ⇒ armed. Wins over scaled_gate_entry"},
			"gate_income_bar":                  {Type: "int", Min: 1, Max: 5_000_000, Default: bootstrap["gate_income_bar"], Unit: "credits", Description: "sp-fp3y armed GATE-entry bar: SUSTAINED (rolling-window mean) net credits/hour the contract fleet must clear to enter GATE (whole credits; default 50000, well above income_bar so a single contract payout cannot trip it). Inert while the gate is disabled"},
			"gate_min_haulers":                 {Type: "int", Min: 1, Max: 50, Default: bootstrap["gate_min_haulers"], Unit: "hulls", Description: "sp-fp3y armed GATE-entry hauler floor: contract-dedicated haulers required to enter GATE, proving a multi-hull op (the ktio deadlock entered GATE with ZERO). Default 2, below hauler_target so few-hub universes still gate. Inert while the gate is disabled"},
			"autosizer_early_scaling":          {Type: "int", Min: 0, Max: 1, Default: bootstrap["autosizer_early_scaling"], Unit: "flag", Description: "sp-sjvv FORCE-ON override (feature is DEFAULT-ON via config.yaml): 1 ⇒ force the early autosizer launch + single-buyer arbitration armed even if config disabled it; 0 (default) ⇒ defer to the config default. To turn it OFF live, use autosizer_early_scaling_disabled"},
			"autosizer_early_scaling_disabled": {Type: "int", Min: 0, Max: 1, Default: bootstrap["autosizer_early_scaling_disabled"], Unit: "flag", Description: "sp-5nd2 live kill-switch: 1 ⇒ stand DOWN the (default-on) early autosizer launch + single-buyer arbitration next tick with no restart — the autosizer stays off and bootstrap buys its own haulers; 0 (default) ⇒ armed. Wins over autosizer_early_scaling"},
			// DEFAULT-OFF contract-scaler arm — the shipwright's single arm lever, live-tunable.
			"contract_scaler_early_scaling": {Type: "int", Min: 0, Max: 1, Default: bootstrap["contract_scaler_early_scaling"], Unit: "flag", Description: "1 ⇒ bootstrap launches the standing dedicated contract auto-scaler EARLY (DATA/INCOME) so it ramps the exclusive contract fleet behind the 200000 cushion; 0 (default) ⇒ OFF, byte-identical. Armed by the shipwright after validation"},
			// Scaled-gate hardening — DEFAULT-OFF (0). Extends the scaled GATE gate so a cold start never
			// LATCHES GATE on a 2-hauler income blip with no war chest and then cannibalizes the contract op
			// below the depot-staging floor (the death spiral). Gate entry becomes STRICTER (RULINGS #4); the
			// two floors are phase thresholds, not money-guards (RULINGS #5).
			"gate_surplus_hardening":        {Type: "int", Min: 0, Max: 1, Default: bootstrap["gate_surplus_hardening"], Unit: "flag", Description: "master switch: 1 ⇒ arm scaled-gate hardening (raised GATE-entry hauler floor + treasury-surplus war chest + sustained $/hr; keep a higher contract-earner floor through GATE; re-derive INCOME from an under-scaled sticky GATE with hysteresis); 0 (default) ⇒ off, byte-identical"},
			"gate_hauler_floor":             {Type: "int", Min: 1, Max: 50, Default: bootstrap["gate_hauler_floor"], Unit: "hulls", Description: "armed GATE-entry hauler floor — a genuinely scaled op, not the 2-hull minimum; clamped ≥ gate_min_haulers so hardening is never looser than the plain scaled gate. Default 4 (= hauler_target). Inert while gate_surplus_hardening=0"},
			"gate_surplus_floor":            {Type: "int", Min: 0, Max: 50_000_000, Default: bootstrap["gate_surplus_floor"], Unit: "credits", Description: "armed GATE-entry treasury SURPLUS (over the immutable 50k reserve) required to enter GATE — the gate-material war chest so GATE is earned from surplus, never raced on a thin treasury. Default 500000 (⇒ treasury ≥ 550k). A phase threshold, not a spend guard. Inert while gate_surplus_hardening=0"},
			"gate_contract_floor":           {Type: "int", Min: 0, Max: 50, Default: bootstrap["gate_contract_floor"], Unit: "hulls", Description: "contract-earner floor held through GATE while hardening is armed — GATE repurposes only the surplus ABOVE this to construction, never below (the reconciler withholds the staging depot below a 2-hauler pool). Default 2, supersedes min_contract_earners while armed. Inert while gate_surplus_hardening=0"},
			"gate_reentry_construction_pct": {Type: "int", Min: 0, Max: 100, Default: bootstrap["gate_reentry_construction_pct"], Unit: "percent", Description: "escape hatch: construction-progress ceiling below which an under-scaled sticky GATE may re-derive INCOME (past it, real materials are flowing and GATE is permanent). Default 5. Inert while gate_surplus_hardening=0"},
			"gate_reentry_streak_ticks":     {Type: "int", Min: 1, Max: 1000, Default: bootstrap["gate_reentry_streak_ticks"], Unit: "ticks", Description: "escape-hatch anti-thrash hysteresis: consecutive under-scaled + low-progress ticks before the GATE→INCOME re-derive fires (a single dip never flips the phase; in-memory, fails safe on restart). Default 3 (≈2.25 min at 45s). Inert while gate_surplus_hardening=0"},
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
