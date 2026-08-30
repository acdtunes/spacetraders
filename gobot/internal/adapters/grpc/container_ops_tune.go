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
	"sensing":        string(container.ContainerTypeProbeSensingCoordinator),
	"contract":       string(container.ContainerTypeContractFleetCoordinator),
	"autooutfit":     string(container.ContainerTypeAutoOutfitCoordinator),
	"bootstrap":      string(container.ContainerTypeBootstrapCoordinator),
	"contractscaler": string(container.ContainerTypeContractScaler),
	"growth":         string(container.ContainerTypeFleetGrowth),
	// The trade fleet coordinator owns the tour path's market-freshness floors
	// (sp-k4z5b). "tour" rather than "tradefleet" because the knobs govern what a TOUR
	// will still price off, and "tour" is the word the operator reaches for mid-incident.
	"tour": string(container.ContainerTypeTradeFleetCoordinator),
}

func tunableKnobsByContainerType() map[string]map[string]TuneBound {
	sensing := scoutingCmd.SensingTunableDefaults()
	contract := ContractCoordinatorTunableDefaults()
	autoOutfit := autooutfitCmd.AutoOutfitTunableDefaults()
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
			"contract_fleet_max_hulls": {Type: "int", Min: 0, Max: 16, Default: contractScaler["contract_fleet_max_hulls"], Unit: "hulls", Applies: TuneAppliesLive, Description: "the exclusive contract fleet's live-tunable ceiling (filled delivery-first, behind the 200000 cushion). Default 4 — the cold start's GATE-entry bar; raise it once the gate is built (delivery saturates ~7-8)"},
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
			"expansion_enabled":          {Type: "int", Min: 1, Max: 3, Default: sensing["expansion_enabled"], Unit: "flag", Applies: TuneAppliesLive, Description: "probe SPENDING switch, ONE knob and THREE states: 1=buy probes and dispatch charting seeds (default), 2=neither, 3=buy probes, dispatch no charting seed. NOT 0-based — `tune <key> 0` means revert-to-default fleet-wide, so 0 would make a state unexpressible. 2 BUYS NOTHING: no probe for a coverage placement (operation_type='sensing coverage'), no charting seed; expect `bought 0` on every cycle line, held at the expansion switch. 3 IS THE MARKET-COVERAGE STATE: probes keep being bought and placed, and no hull is put on a jump-burning charting errand — use it when coverage is worth more than exploration. Neither 2 nor 3 strands a seed already in flight: an errand in progress runs to its end and the hull stands down where it finishes, but it is never retargeted onto the next dark system. 2 and 3 do NOT stop the free work — frontier discovery, jump-gate reads, screening, re-tasking idle probes you already own, and gate-crossing footholds all keep running, so coverage keeps growing on the hulls already paid for. It is not the money guard: capex_reserve_credits and the probe cap are, and they apply in every state. Applies next tick"},
			"target_util_pct":            {Type: "int", Min: 50, Max: 95, Default: sensing["target_util_pct"], Unit: "percent", Applies: TuneAppliesLive, Description: "share of the rate-limiter ceiling the WHOLE fleet aims to occupy; sensing takes what is left of it after every other source. The remainder is burst headroom — raising this toward 100 spends the headroom that absorbs traffic spikes. Applies next tick"},
			"min_scan_rate_milli":        {Type: "int", Min: 10, Max: 2_000, Default: sensing["min_scan_rate_milli"], Unit: "milli-req/sec", Applies: TuneAppliesLive, Description: "floor the scan pacer is clamped up to, in thousandths of a request/sec (100 = 0.1 req/s). It is what guarantees planner market data never goes fully dark under API pressure. It governs the PACER ONLY — the residual below which expansion pauses is expansion_min_budget_milli, a separate key, so raising this cannot stop the fleet charting. Raising it also does not raise landed scans on its own: every parked scan is admitted by the fleet-wide market-scan budget ([market_scan] budget_req_per_sec in config.yaml), and the cycle line's declined count is what says which of the two is binding. Applies next tick"},
			"expansion_min_budget_milli": {Type: "int", Min: 1, Max: 2_000, Default: sensing["expansion_min_budget_milli"], Unit: "milli-req/sec", Applies: TuneAppliesLive, Description: "sensing residual BELOW which the expansion pass yields for the tick, in thousandths of a request/sec (20 = 0.02 req/s). It is compared against the residual AFTER the emergency brake — the one number that can express API pressure, since the pacer re-imposes min_scan_rate_milli and gating on the pacer rate would leave expansion charting through a rate-limit storm. HELD APART FROM min_scan_rate_milli ON PURPOSE: they answer different questions, and one key serving both means a scan-rate change silently decides whether the fleet charts. The default is sized against the brake's own floor (0.1x the clamped residual), so a single halving still clears it and only a sustained storm does not. Yielding pauses NEW seeds only — errands already in flight still finish, and the free frontier and gate-read passes still run. Not an off switch: `tune expansion_min_budget_milli 0` reverts to the default. Applies next tick"},
			"value_clamp_r":              {Type: "int", Min: 1, Max: 16, Default: sensing["value_clamp_r"], Unit: "ratio", Applies: TuneAppliesOnRebuild, Description: "ceiling on how much more scan attention the hottest market may earn than the baseline; 1 flattens the weighting entirely. Binds when the scan rotation is built, so it applies at coordinator rebuild (restart/relaunch), not next tick"},
			"inflight_cap":               {Type: "int", Min: 1, Max: 8, Default: sensing["inflight_cap"], Unit: "scans", Applies: TuneAppliesOnRebuild, Description: "concurrent parked scans. It is the backpressure reflex: with every token held the pacer BLOCKS rather than queueing, so a degraded API throttles scan issuance at the source. Binds when the scan rotation is built, so it applies at coordinator rebuild (restart/relaunch), not next tick"},
			"capital_multiplier_k_milli": {Type: "int", Min: 0, Max: 10000, Default: sensing["capital_multiplier_k_milli"], Unit: "milli-hours", Applies: TuneAppliesLive, Description: "MILLI-hours of the TRADING fleet's measured cargo runway the probe buy floor holds back on top of the immutable 50k reserve — so a busier fleet automatically raises the floor. 2000=2h (default), 400=0.4h. Sub-hour matters: at fleet-scale cargo spend a whole hour can exceed the whole treasury. Applies next tick"},
			"capex_reserve_credits":      {Type: "int", Min: 0, Max: 5_000_000, Default: sensing["capex_reserve_credits"], Unit: "credits", Applies: TuneAppliesLive, Description: "credits the probe buy floor holds back for ship capex already committed elsewhere. Adds to the immutable 50k reserve; it never weakens it. Applies next tick"},
			"quartermaster_cadence_secs": {Type: "int", Min: 300, Max: 86_400, Default: sensing["quartermaster_cadence_secs"], Unit: "seconds", Applies: TuneAppliesLive, Description: "a YARD slot's re-read interval. A FLOOR on its scan interval, never a target: hull prices move on their own schedule, so the budget may slow a yard down but never speed it past this. Applies next tick"},
			"surge_inflight_cap":         {Type: "int", Min: 1, Max: 64, Default: sensing["surge_inflight_cap"], Unit: "hulls", Applies: TuneAppliesLive, Description: "SENSING SURGE: how many surplus probes may be FLYING toward charted-but-unpriced systems at once (sp-zvywu). A standing concurrency bound, not a per-tick burst — each tick tops the in-flight population up to this and stops, so the surge's API cost cannot grow tick over tick. Under 10 by default because that is the placement machine's own per-tick action budget, which is what advances these flights. Buying is untouched: the surge only ever dispatches hulls already paid for, and it never takes a probe standing a sensing watch. Not an off switch — 0 reverts to the default. Applies next tick"},
			"coverage_reserve":           {Type: "int", Min: 0, Max: 3, Default: sensing["coverage_reserve"], Unit: "attempts", Applies: TuneAppliesLive, Description: "how many of the fill queue's own per-tick attempts are held back for the single best market in a system the fleet has never entered, once one is outstanding — alongside, never instead of, the existing depth/saturation order. UNLIKE EVERY OTHER KNOB HERE, 0 is the documented default rather than a revert to a positive one: the queue ships fully OFF and the saturate-first order is byte-identical until armed. Exempt from the dark-yard tier, which outranks it unconditionally regardless of this setting. Applies next tick"},

			"procurement_walkaway_mult":        {Type: "int", Min: 1, Max: 20, Default: sensing["procurement_walkaway_mult"], Unit: "multiple", Applies: TuneAppliesLive, Description: "how many times the fleet's CHEAPEST fresh probe ask a counter may charge before the buy queue refuses to transact there at all. A spend guard, and it only ever refuses: a placement whose every counter in reach breaches it is HELD (see buy_walkaway_held on the cycle line) rather than filled at that price, and nothing here can force a purchase. It binds twice — on the stored price when counters are ranked, and again on the LIVE quote at the counter, which is the check a yard whose ask ran away between readings cannot slip past. WHOLE MULTIPLES, not milli-units: the operator vocabulary here is 2, 3, 5. 1 means only the cheapest band is acceptable and will hold placements aggressively. It is INERT when the fleet holds no fresh yard price anywhere — there is nothing to be a multiple of, so the queue falls back to the nearest-first order and says so once (parked_sensing_procurement_unavailable). Applies next tick"},
			"procurement_jump_penalty_credits": {Type: "int", Min: 0, Max: 100_000, Default: sensing["procurement_jump_penalty_credits"], Unit: "credits", Applies: TuneAppliesLive, Description: "charged per gate crossing ON TOP of the measured 5,900 gate fee when the buy queue RANKS counters, so a marginally cheaper distant yard does not win on price noise. It stands for what the gate fee does not cover — the in-system approach legs at both ends, and the turns a ferried hull spends in transit instead of scanning. At the default the indifference threshold is 7,900 credits per hop: a counter one gate out must be that much cheaper before the queue crosses for it. RAISE IT to bias back toward the counter underfoot, LOWER IT to chase the frontier price. NOT a money guard and no guard reads it — the buy floor prices delivery from the gate fee alone. Applies next tick"},

			"chart_hull_cap":  {Type: "int", Min: 1, Max: 15, Default: sensing["chart_hull_cap"], Unit: "hulls", Applies: TuneAppliesLive, Description: "a HARD CEILING on how many probes ONE dark system may be charted by at once, each working a disjoint angular share of its uncharted waypoints. IT DOES NOT SIZE THE CREW — the walk does, measured every tick: a charting tour is serial, so a crew of c clears the outstanding count in 2*outstanding/c turns while the c-th hull first pays two turns per gate hop from the nearest system we hold plus its arrival sweep, and it is worth adding while outstanding/c clears that walk plus a half. A one-hop walk earns nine hulls on a twenty-waypoint system and a thirteen-hop walk earns one, so the answer moves with the map rather than sitting in a constant fitted to a map that has since changed (sp-glyoe). THE MAXIMUM IS DERIVED, NOT PICKED, and that is why it is 15 rather than a round number: hulls arrive one per system per tick, so rank c is granted after the ranks before it have already taken c*(c-1)/2 of the system's 2*outstanding turns, and against the largest chartable system this map holds (57 waypoints) that first exhausts the work at sixteen. RAISE OR LOWER IT to bound what the walk can buy; 1 IS THE OFF SWITCH, one hull per system over the whole list whatever else says. NOT a money guard — a crew hull is a probe like any other and still passes the buy floor and probe_cap. Applies next tick"},
			"chart_hull_2_at": {Type: "int", Min: 1, Max: 500, Default: sensing["chart_hull_2_at"], Unit: "waypoints", Applies: TuneAppliesLive, Description: "a FLOOR on the outstanding uncharted waypoints a system must hold before it earns a SECOND charting hull, applied on top of what the measured walk earns (see chart_hull_cap) and never in place of it — this can only take hulls away. It is the operator's brake on how far the fleet spreads: raise it to hold nearly-finished systems to the hull already on them and to keep crews off the small dark systems a short walk would otherwise crew heavily, lower it to concentrate; set it above any real system's count to hold every system to one hull. THE DEFAULT IMPOSES NOTHING — it is the second hull's own break-even at the shortest walk stored adjacency can report, one gate hop, so at the default the walk decides. Applies next tick"},
			"chart_hull_3_at": {Type: "int", Min: 1, Max: 500, Default: sensing["chart_hull_3_at"], Unit: "waypoints", Applies: TuneAppliesLive, Description: "the same floor for the THIRD charting hull, on the same terms as chart_hull_2_at and bounded by chart_hull_cap regardless. IT ALSO SETS THE STEP BEYOND THE THIRD: every further chart_hull_3_at-minus-chart_hull_2_at waypoints of outstanding work are required before one more hull, so the pair moves the whole brake together instead of leaving a hidden constant to disagree with them. Thresholds set out of order supply no step and the documented one is used. Applies next tick"},

			"pressure_half_life_secs": {Type: "int", Min: 1, Max: 3_600, Default: sensing["pressure_half_life_secs"], Unit: "seconds", Applies: TuneAppliesOnRebuild, Description: "smoothing half-life of the API limiter-pressure EWMA the emergency brake reads. Boot value comes from config.yaml [daemon] limiter_pressure_half_life_seconds; a tuned value persists and is applied whenever the coordinator is rebuilt (restart/relaunch)"},
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
			"market_data_max_age_minutes":      {Type: "int", Min: 1, Max: 43_200, Default: tradeFleet["market_data_max_age_minutes"], Unit: "minutes", Applies: TuneAppliesLive, Description: "FLOOR on how old a cached market_data row may be before the tour stops trusting it. ONE number for both consumers: the freshListings paths (lookback, held-cargo offload, reposition + relocation pre-rank, distress liquidation, candidate shortlist, cross-system sink scan, stocker) AND the FRESH clause of the firm-sink buy gate (sp-tgll8), a fail-closed money guard — never buy into a sink we cannot confirm is live. Effective cap = max(this, rotation bound): raising it above the rotation bound widens the cap, below it is a deliberate no-op, so it can never be tuned tight enough to refuse a row the rotation explains, and never to zero — `tune ... 0` reverts to the documented default, it does NOT disarm the guard (RULINGS #4). Arming the guard remains a boot concern ([trade_fleet].sink_freshness_max_minutes). Applies next tick (the cap is re-read from this column at most every 5s), no restart"},
			"same_market_rebuy_window_minutes": {Type: "int", Min: 1, Max: 1_440, Default: tradeFleet["same_market_rebuy_window_minutes"], Unit: "minutes", Applies: TuneAppliesLive, Description: "how long after a hull SELLS a good at a market that same hull is barred from BUYING that good back there. A sale moves the market against itself, and the ask it leaves behind reads to the next plan as the cheapest source for the good just dumped — so without this a hull re-buys its own dump and pays the spread every rotation without leaving the system. PER HULL, PER MARKET, PER GOOD: another hull may source there, and this hull may source the good elsewhere or other goods there. The SINK is never withdrawn, so a hold discharged in tranches still finishes selling. A PLANNING filter, not a money guard: it fails OPEN (no recorded sale, no filtering) and the record is in-memory, so a daemon restart forgets a window that was already expiring. Raise it where a market recovers slowly; `tune ... 0` reverts to the default. Applies next tick, no restart"},
			"spawn_dispersal_min_other_hulls":  {Type: "int", Min: 1, Max: 100, Default: tradeFleet["spawn_dispersal_min_other_hulls"], Unit: "hulls", Applies: TuneAppliesLive, Description: "how many OTHER active trade hulls must share a hull's system before a no-plan verdict there SKIPS the breathing retries and goes straight to reposition discovery. Hulls of a class are bought at one yard, so they start touring from one system and drain it between them; re-pricing that ground three times re-asks a question the stack has already answered. Only the TIMING of the existing rescue changes — the reposition kill switch, the one-move-per-episode budget, the anti-herd cap and the relocation floor all still decide whether the hull moves, and a rescue that declines leaves the streak running. Counts DEDICATED TRADE hulls only, excluding the hull itself; an unreadable fleet counts as uncrowded. Raise it to make dispersal rarer; `tune ... 0` reverts to the default. Applies next tick, no restart"},
			"explore_min_fresh_listings":       {Type: "int", Min: 1, Max: 500, Default: tradeFleet["explore_min_fresh_listings"], Unit: "rows", Applies: TuneAppliesLive, Description: "how many FRESH cached quotes a ground must show before the reposition pre-flight will spend its one exploration call on it. That pre-flight window is chosen by cached spread alone — a pure function of prices, so it names the same grounds from a given origin forever, and a ground ranking just outside it is never priced, never learnt about and never traded. One extra planner call per episode goes to the ground the fleet has gone longest without pricing; this is the BREADTH half of the evidence that ground must show to earn it. NOT a money guard and no guard reads it: an admitted ground still clears the same pre-flight, the same relocation floor and the same rate ranking as every other candidate, so the slot buys a look and never a verdict. Raise it to spend the call only on broad boards, lower it to sweep the reach faster; `tune ... 0` reverts to the default. Applies next tick, no restart"},
			"explore_min_trade_volume":         {Type: "int", Min: 1, Max: 10_000, Default: tradeFleet["explore_min_trade_volume"], Unit: "units", Applies: TuneAppliesLive, Description: "the deepest per-visit volume a ground must quote somewhere on its board to earn the reposition pre-flight's one exploration call — the ABSORPTION half of the same evidence bar as explore_min_fresh_listings. A market that takes only a token tranche cannot pay for a crossing whatever its spread, so depth rather than price is what makes an unproven ground worth the look. NOT a money guard and no guard reads it. `tune ... 0` reverts to the default. Applies next tick, no restart"},
		},
		// The captain bootstrap coordinator (workflow bootstrap; COLDSTART→GATE→EXPANSION). It is
		// the first CONFIG.YAML-AUTHORITATIVE coordinator in the registry, so its tune key is the SEPARATE
		// BARE family — NOT the prefixed bootstrap_* launch keys, which resolveBootstrapConfig
		// clears+reinjects from config.yaml on every rebuild. A bare tune therefore survives a daemon bounce
		// (the coordinator's per-tick liveconfig reader keeps applying it) instead of being wiped. The
		// cold-start SHAPE is fixed in the coordinator, so the cadence and the contract-start treasury
		// threshold are the runtime levers.
		string(container.ContainerTypeBootstrapCoordinator): {
			"tick_secs":                         {Type: "int", Min: 10, Max: 86_400, Default: bootstrap["tick_secs"], Unit: "seconds", Applies: TuneAppliesLive, Description: "reconcile cadence — kept SHORT because bootstrap runs only at cold start (<0.1 req/s, 20x+ API headroom) and a fast tick cuts poll-latency dead time before the gate (default 45s; sp-lgo3)"},
			"contract_start_treasury_threshold": {Type: "int", Min: 0, Max: 100_000_000, Default: bootstrap["contract_start_treasury_threshold"], Unit: "credits", Applies: TuneAppliesLive, Description: "FLAT treasury at which the CONTRACT OPERATION starts during cold start: below it the command frigate trades under the trade-fleet coordinator and nothing contract-side is launched or bought; at/above it the contract-fleet coordinator comes up and the hauler ramp begins (default 500000). Deliberately NOT netted against the reserve floor — a different reading from the GATE-entry surplus bar. SEQUENCING only, never a spend guard: every buy still passes the untouched 350k working-capital floor, and once contract ops are under way a treasury dip never stands them back down (RULINGS #1/#4)"},
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
