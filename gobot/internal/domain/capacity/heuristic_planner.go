package capacity

import (
	"context"
	"math"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/domain/buffer"
)

// HeuristicPlanner is the v1 deterministic PLAN implementation (spec: PLAN —
// the heuristic planner). Pure logic: it consumes one Signals snapshot plus
// the live Calibration and emits the DesiredTopology — no I/O, no state, no
// clock. Interpretability is a feature: every choice
// traces to a documented ranking or threshold below.
//
// The policy (2026-07-15 design spec, PLAN section):
//   - Hub coverage: rank hubs by frequency × cycle_penalty × payment and walk
//     the ranking under a KEEP-vs-ADD gate split (recorded in CONTRACTS.md —
//     the differ lane builds to it). Hubs ALREADY covered in the actual
//     topology (a live cluster with ≥1 hull in Signals.Topology) are
//     keep-gated at the universal per-hull floor only; hubs NOT yet covered
//     are add-gated at max(floor, live fleet per-hull average). The fleet
//     average is the absorption ceiling and per the spec's north-star it
//     "stops ADDING capacity" — it must never erase a producing covered hub
//     from the desired topology, or the reconciler stops gap-healing the
//     existing machine and presents its capacity to DIFF as surplus. The ADD
//     walk STOPS at the first below-gate hub (spec: "cover the top hubs
//     UNTIL..." — a stop, not a filter); covered hubs ranked behind the stop
//     are still kept. The universal floor is the calibrated
//     AddThresholdPerHullCrHr, or the documented cold-start floor while
//     uncalibrated (0) — a cold fleet's first real tick plans conservatively
//     instead of covering every paying hub. The desired topology SELF-LIMITS
//     unconditionally: it stops wanting new capacity the moment the marginal
//     hull would lower the fleet-wide average.
//   - Buffered goods per hub: select by VALUE DENSITY (the economy-analyst's
//     era-3 model validated over 453 contracts). A buffer's worth is the
//     source-and-haul time it AVOIDS per contract —
//     0.030×source_distance + 0.147×avg_units MINUTES, BOTH terms positive —
//     times the contract frequency, per unit of warehouse budget:
//     value_density = frequency × (0.030×dist + 0.147×avg_units) ÷ avg_units.
//     Value RISES with source distance (a far source is exactly what makes a
//     pre-staged buffer valuable — the avoided leg is long); the prior score
//     DIVIDED by distance and so drove every far-sourced good below a floor,
//     emptying the far-sourced home hub's buffer. Candidates rank by value
//     density and fill the buffered-volume budget greedily; there is NO value
//     floor. Goods with no known source distance cannot be costed and are
//     skipped, and the shared gate still excludes local-production and
//     too-near sources up front.
//   - Caps: per-good cap ≈ avg_units + margin — an uncapped whitelist
//     over-fills the first good and starves the rest.
//   - Counts: workers by work conservation on the observed cycle; warehouses
//     by buffered volume; stockers so restock_throughput ≥ consumption_rate —
//     worker and stocker counts under an absolute per-hub sanity ceiling so
//     pathological telemetry (a wedged hub measuring a 200h cycle) cannot
//     emit an unbounded desired topology.
//
// Empty or insufficient signals produce a conservative plan — possibly empty
// (IsEmpty ⇒ zero actions downstream) — never a guess and never a panic.
type HeuristicPlanner struct{}

// NewHeuristicPlanner assembles the stateless heuristic planner.
func NewHeuristicPlanner() HeuristicPlanner { return HeuristicPlanner{} }

var _ Planner = HeuristicPlanner{}

// DefaultStockerCapacityBudgetUnits is the planner's documented default
// per-hub buffered-volume budget, used when Calibration.StockerCapacityBudget
// is zero (governor.go: "Default 0 = planner's own documented default until
// calibrated"). Sized to two stocker holds' worth with headroom.
const DefaultStockerCapacityBudgetUnits = 240

// Heuristic policy constants — v1 documented values, calibration-pending
// against prod history (854 contracts). Unexported deliberately: tests pin
// literal expected outcomes traced to the policy, not echoes of these.
const (
	// bufferCapMarginFactor puts a 50% margin over avg_units on every cap.
	bufferCapMarginFactor = 1.5
	// sourceDistanceSavingWeightMinutes and averageUnitsSavingWeightMinutes are the
	// economy-analyst's era-3-derived coefficients (validated over 453
	// contracts) of the source-and-haul time a warehouse buffer AVOIDS per contract:
	//
	//	saving_per_contract_minutes = 0.030 × source_distance + 0.147 × avg_units
	//
	// BOTH weights are POSITIVE. A farther source (a longer avoided source-and-haul
	// leg) and a bulkier haul (more avoided load) each make the pre-staged buffer MORE
	// valuable — the exact OPPOSITE of the prior score, which DIVIDED value by
	// distance and drove every far-sourced good below the never-buffer floor, emptying
	// the far-sourced home hub. The FORM (value rises with distance) is universe-agnostic
	// haul physics; the two magnitudes are re-derivable from contract history and belong
	// in calibration once the reconciler is armed.
	sourceDistanceSavingWeightMinutes = 0.030
	averageUnitsSavingWeightMinutes   = 0.147
	// reconcilerMinSourceDistance is the gate-3 source-distance floor on
	// this (reconciler) path: a good whose nearest source sits at/below this many
	// coordinate units is too near to warrant a warehouse slot. A documented
	// constant here (the LIVE depot path carries the live-tunable knob); set well
	// below the near-sourced goods the planner already buffers so it only excludes
	// the co-located/adjacent class, never the genuine far/orphan goods.
	reconcilerMinSourceDistance = 25.0
	// warehouseHoldUnits sizes warehouse counts: one freighter-class hold.
	warehouseHoldUnits = 120.0
	// coldStartFloorPerHullCrHr is the effective universal per-hull-cr/hr
	// floor while AddThresholdPerHullCrHr is uncalibrated (0 — governor.go's
	// documented default). Without it a cold fleet (FleetPerHullCrHr 0) makes
	// the coverage gate vacuous ("marginal > 0") and the first real SENSE
	// tick covers EVERY paying hub. 500 cr/hr filters junk hubs (the fixture
	// QQ7 class: ≈167 cr/hr marginal) while any real hub (thousands) clears
	// it — low enough not to stall a 1–2 ship bootstrap seed. Same
	// zero-resolves-to-planner-default pattern as the stocker budget; an
	// operator who wants near-zero gating calibrates an explicit small value.
	coldStartFloorPerHullCrHr = 500.0
	// maxWorkerCountPerHub / maxStockerCountPerHub are absolute per-hub
	// sanity ceilings on the sized counts — planner-OUTPUT bounds, not sizing
	// models. The sizing trusts measured cycle/frequency, and telemetry can
	// be pathological (a wedged hub measuring a 200h cycle at 2.0/hr would
	// "want" 400 workers; spurious good-frequency explodes stocker cadence
	// the same way): an effectively-unbounded desired topology is pure
	// churn/proposal-spam pressure on DIFF/GOVERN. One fleet's worth of
	// workers on a single hub is already implausible for v1. The coverage
	// gate judges the CLAMPED plan. Warehouses need no ceiling — they are
	// already bounded by the stocker-capacity budget (an operator-set knob).
	maxWorkerCountPerHub  = 12
	maxStockerCountPerHub = 6
	// Stocker round-trip cadence model: cargo per trip, travel seconds per
	// distance unit (each way), and dock/trade overhead per trip.
	stockerCargoUnits               = 80.0
	stockerTravelSecondsPerDistance = 1.0
	stockerTradeOverheadSeconds     = 120.0
	secondsPerHour                  = 3600.0
)

// ComputeDesired recomputes the desired capacity topology from live signals.
// Deterministic: the same Signals and Calibration produce the byte-identical
// DesiredTopology regardless of input slice order. Always returns nil error —
// a pure heuristic cannot fail, only plan conservatively.
func (HeuristicPlanner) ComputeDesired(_ context.Context, signals Signals, cal Calibration) (DesiredTopology, error) {
	walk := newCoverageWalk(cal, signals)
	sourceDistances := sourceDistancesByHub(signals.Economics.SourceDistances)
	localProduction := localProductionByHub(signals.Economics.LocalProduction)
	gate := buffer.Gate{MinExternalSourceDistance: reconcilerMinSourceDistance}
	budgetUnits := stockerBudgetUnits(cal)

	var hubs []DesiredHub
	for _, candidate := range rankHubCandidates(signals.Demand.Hubs, performanceByHub(signals.Performance.Hubs)) {
		hub := planHub(candidate, sourceDistances[candidate.demand.HubSymbol], localProduction[candidate.demand.HubSymbol], gate, budgetUnits)
		if walk.admits(candidate, hub) {
			hubs = append(hubs, hub)
		}
	}
	return DesiredTopology{Hubs: hubs}, nil
}

// ---- hub coverage ------------------------------------------------------------

// hubCandidate is one demand hub joined with its measured performance and its
// coverage-ranking score.
type hubCandidate struct {
	demand      HubDemand
	performance HubPerformance
	score       float64
}

// rankHubCandidates joins demand with performance and orders hubs by coverage
// score, highest first; equal scores order by symbol so the ranking is a
// total order independent of input slice order. Duplicate demand entries for
// one symbol (malformed sense) collapse to the best-ranked one.
func rankHubCandidates(demand []HubDemand, performance map[string]HubPerformance) []hubCandidate {
	candidates := make([]hubCandidate, 0, len(demand))
	for _, hub := range demand {
		candidates = append(candidates, hubCandidate{
			demand:      hub,
			performance: performance[hub.HubSymbol],
			score:       hubCoverageScore(hub, performance[hub.HubSymbol]),
		})
	}
	sortHubCandidates(candidates)
	return dedupeHubCandidates(candidates)
}

// hubCoverageScore ranks hubs by frequency × cycle_penalty × payment (spec:
// PLAN hub coverage). A hub with no observed paying demand scores zero.
func hubCoverageScore(demand HubDemand, performance HubPerformance) float64 {
	if demand.ContractFrequency <= 0 || demand.AvgPaymentCredits <= 0 {
		return 0
	}
	return demand.ContractFrequency * cyclePenalty(performance) * demand.AvgPaymentCredits
}

// cyclePenalty grows with the measured accept→fulfill cycle (1 + cycle-hours):
// a slow hub has the most cycle-time to recover from co-located coverage.
// Unmeasured hubs are neutral (1) — never advantaged, never excluded.
func cyclePenalty(performance HubPerformance) float64 {
	if performance.CycleTimeSeconds <= 0 {
		return 1
	}
	return 1 + performance.CycleTimeSeconds/secondsPerHour
}

func sortHubCandidates(candidates []hubCandidate) {
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].score != candidates[right].score {
			return candidates[left].score > candidates[right].score
		}
		return candidates[left].demand.HubSymbol < candidates[right].demand.HubSymbol
	})
}

func dedupeHubCandidates(ranked []hubCandidate) []hubCandidate {
	seen := make(map[string]bool, len(ranked))
	unique := ranked[:0]
	for _, candidate := range ranked {
		if seen[candidate.demand.HubSymbol] {
			continue
		}
		seen[candidate.demand.HubSymbol] = true
		unique = append(unique, candidate)
	}
	return unique
}

// coverageWalk applies the KEEP-vs-ADD gate split along the hub ranking (spec
// north-star: the absorption ceiling "stops ADDING capacity" — it is an add
// gate, not a keep gate; design recorded in CONTRACTS.md, the differ lane
// builds to it). Hubs already covered in the actual topology are judged
// against the universal floor only, so a fleet-wide average inflated by
// arb/mining hulls never erases the producing machine the reconciler exists
// to keep healing.
type coverageWalk struct {
	keepFloor   float64
	addRequired float64
	covered     map[string]bool
	addStopped  bool
}

func newCoverageWalk(cal Calibration, signals Signals) *coverageWalk {
	keepFloor := keepRequiredPerHullCrHr(cal)
	return &coverageWalk{
		keepFloor:   keepFloor,
		addRequired: math.Max(keepFloor, signals.Economics.FleetPerHullCrHr),
		covered:     coveredHubs(signals.Topology),
	}
}

// admits decides one ranked hub. Covered hubs KEEP their place while the
// marginal hull clears the universal floor — shrink still works: a covered
// hub below the floor drops out. Uncovered hubs face the ADD gate.
func (walk *coverageWalk) admits(candidate hubCandidate, hub DesiredHub) bool {
	if walk.covered[candidate.demand.HubSymbol] {
		return clearsCoverageGate(candidate, hub, walk.keepFloor)
	}
	return walk.admitsAdd(candidate, hub)
}

// admitsAdd walks the ranking's NOT-yet-covered hubs and STOPS at the first
// one whose marginal hull falls below the add requirement — the spec's "cover
// the top hubs UNTIL the marginal hull's projected per-hull-$/hr falls below
// threshold" is a stop, not a filter: a leaner lower-ranked hub behind the
// failure is NOT added even when its own marginal would clear the gate.
// Covered hubs behind the stop still pass through admits' keep branch.
func (walk *coverageWalk) admitsAdd(candidate hubCandidate, hub DesiredHub) bool {
	if walk.addStopped {
		return false
	}
	walk.addStopped = !clearsCoverageGate(candidate, hub, walk.addRequired)
	return !walk.addStopped
}

// clearsCoverageGate is the ROI self-limit: the hub's marginal hull must yield
// something (> 0) and at least the required per-hull-cr/hr.
func clearsCoverageGate(candidate hubCandidate, hub DesiredHub, requiredPerHull float64) bool {
	marginal := marginalPerHullCrHr(candidate.demand, hub)
	return marginal > 0 && marginal >= requiredPerHull
}

// coveredHubs is the set of hubs with live co-located capacity in the ACTUAL
// topology — the existing contract machine. A cluster with zero hulls is NOT
// coverage: planning it is an add in reality and faces the absorption ceiling.
func coveredHubs(topology TopologySignals) map[string]bool {
	covered := make(map[string]bool, len(topology.Clusters))
	for _, cluster := range topology.Clusters {
		if clusterHoldsCapacity(cluster) {
			covered[cluster.HubSymbol] = true
		}
	}
	return covered
}

func clusterHoldsCapacity(cluster ClusterState) bool {
	return len(cluster.Warehouses)+len(cluster.Stockers)+len(cluster.Workers) > 0
}

// marginalPerHullCrHr projects the per-hull-cr/hr of covering this hub: its
// observed contract revenue rate (frequency × payment) spread across every
// hull the plan commits to it (spec: every capacity add is ROI-gated on
// per-hull-$/hr).
func marginalPerHullCrHr(demand HubDemand, hub DesiredHub) float64 {
	hulls := hub.WorkerCount + hub.StockerCount + hub.WarehouseCount
	if hulls <= 0 {
		return 0
	}
	return demand.ContractFrequency * demand.AvgPaymentCredits / float64(hulls)
}

// keepRequiredPerHullCrHr is the universal per-hull-cr/hr floor EVERY hub —
// covered or not — must clear: the calibrated add-threshold, or the planner's
// documented cold-start floor while uncalibrated (zero/absent). For adds the
// requirement additionally rises to the live fleet average (the absorption
// ceiling — a marginal hull yielding below it would lower the fleet-wide
// per-hull-cr/hr the moment it joined; see newCoverageWalk).
func keepRequiredPerHullCrHr(cal Calibration) float64 {
	if cal.AddThresholdPerHullCrHr <= 0 {
		return coldStartFloorPerHullCrHr
	}
	return cal.AddThresholdPerHullCrHr
}

// ---- one hub's plan ------------------------------------------------------------

// planHub assembles one covered hub: buffer whitelist + caps, then counts
// sized to that buffered volume and the hub's observed cycle. Positions stay
// empty — the contract defaults them to the hub itself, and co-location IS
// the cycle-time lever.
func planHub(candidate hubCandidate, sourceDistances map[string]float64, localProduction map[string]bool, gate buffer.Gate, budgetUnits int) DesiredHub {
	selected := selectBufferGoods(candidate.demand.GoodMix, sourceDistances, localProduction, gate, budgetUnits)
	return DesiredHub{
		HubSymbol:      candidate.demand.HubSymbol,
		BufferedGoods:  desiredBufferedGoods(selected),
		WarehouseCount: warehouseCount(selected),
		StockerCount:   stockerCount(selected),
		WorkerCount:    workerCount(candidate.demand, candidate.performance),
	}
}

// workerCount conserves work: frequency (contracts/hr) × cycle (hours) is the
// number of concurrent deliveries in flight; a covered hub always keeps the
// one co-located worker coverage means, even with the cycle unmeasured. The
// sanity ceiling keeps a pathological measured cycle (a wedged 200h hub)
// from manifesting as an unbounded worker count.
func workerCount(demand HubDemand, performance HubPerformance) int {
	if performance.CycleTimeSeconds <= 0 {
		return 1
	}
	concurrent := demand.ContractFrequency * performance.CycleTimeSeconds / secondsPerHour
	return sizeCount(concurrent, maxWorkerCountPerHub)
}

// ---- buffer selection ------------------------------------------------------------

// bufferSelection is one whitelisted good with the numbers count-sizing needs.
type bufferSelection struct {
	good         string
	capUnits     int
	averageUnits float64
	unitsPerHour float64
	distance     float64
}

// bufferCandidate pairs a selection with its ranking score.
type bufferCandidate struct {
	selection bufferSelection
	score     float64
}

// selectBufferGoods picks the hub's buffer whitelist by VALUE DENSITY, best
// first, filling the buffered-volume budget greedily. The budget
// is spent in avg_units — the raw demand volume value_density is measured per —
// so a good is added while the running Σ avg_units stays within budget; a good
// too big for the remaining budget is skipped while smaller higher-density goods
// behind it still make it in. There is NO value floor: the volume budget is the
// only bound, so the far-sourced goods the prior floor deleted are recovered.
func selectBufferGoods(goodMix []GoodDemand, sourceDistances map[string]float64, localProduction map[string]bool, gate buffer.Gate, budgetUnits int) []bufferSelection {
	remaining := float64(budgetUnits)
	var selected []bufferSelection
	for _, candidate := range rankBufferCandidates(goodMix, sourceDistances, localProduction, gate) {
		if candidate.selection.averageUnits > remaining {
			continue
		}
		selected = append(selected, candidate.selection)
		remaining -= candidate.selection.averageUnits
	}
	return selected
}

// rankBufferCandidates scores the eligible goods and orders them best first;
// equal scores order by good so the ranking is a total order independent of
// input slice order. Duplicate good entries collapse to the best-ranked one.
func rankBufferCandidates(goodMix []GoodDemand, sourceDistances map[string]float64, localProduction map[string]bool, gate buffer.Gate) []bufferCandidate {
	candidates := make([]bufferCandidate, 0, len(goodMix))
	for _, good := range goodMix {
		if !admitsBufferGood(good, sourceDistances, localProduction, gate) {
			continue // excluded by the shared candidate gate before it can be ranked
		}
		candidate, eligible := scoreBufferGood(good, sourceDistances)
		if !eligible {
			continue
		}
		candidates = append(candidates, candidate)
	}
	sortBufferCandidates(candidates)
	return dedupeBufferCandidates(candidates)
}

// admitsBufferGood applies the shared candidate gate to one good on the
// reconciler path — the SAME domain/buffer.Gate the LIVE depot selector uses, so
// the three gates cannot drift. Gate 1 is the good's H-scoped contract frequency
// (GoodMix is already the hub's own contract demand), gate 2 is the hub's
// local-production set, gate 3 is the source distance vs the floor.
func admitsBufferGood(good GoodDemand, sourceDistances map[string]float64, localProduction map[string]bool, gate buffer.Gate) bool {
	distance, known := sourceDistances[good.Good]
	return gate.Admits(buffer.Facts{
		Good:                        good.Good,
		HubContractFrequency:        good.Frequency,
		HubProducesLocally:          localProduction[good.Good],
		ExternalSourceDistance:      distance,
		ExternalSourceDistanceKnown: known,
	})
}

// localProductionByHub indexes the per-hub local-production sets by hub then good
// (mirrors sourceDistancesByHub). A hub with no entries maps to a nil set, which
// reads as "produces nothing locally" — gate 2 fails open there.
func localProductionByHub(entries []GoodLocalProduction) map[string]map[string]bool {
	byHub := make(map[string]map[string]bool, len(entries))
	for _, entry := range entries {
		goods := byHub[entry.HubSymbol]
		if goods == nil {
			goods = map[string]bool{}
			byHub[entry.HubSymbol] = goods
		}
		goods[entry.Good] = true
	}
	return byHub
}

// scoreBufferGood applies the era-3 value-density model:
//
//	saving_per_contract = 0.030×source_distance + 0.147×avg_units   (avoided minutes)
//	buffer_value        = frequency × saving_per_contract           (value/hour — REWARDS distance)
//	value_density       = buffer_value ÷ avg_units                  (value per budget unit)
//
// The score MULTIPLIES by distance (a far source makes the buffer more valuable),
// inverting the prior divide-by-distance that emptied far-sourced hubs. There
// is NO value floor — selectBufferGoods bounds the whitelist by the volume budget
// alone. Ineligible ONLY when the good cannot be costed (no known source distance) or
// shows no demand (frequency/avg_units ≤ 0); the distance/frequency/local-production
// gate drops that stay from the pre-fix behaviour (applied up front by admitsBufferGood).
func scoreBufferGood(good GoodDemand, sourceDistances map[string]float64) (bufferCandidate, bool) {
	distance, known := sourceDistances[good.Good]
	if !known || distance <= 0 || good.Frequency <= 0 || good.AvgUnits <= 0 {
		return bufferCandidate{}, false
	}
	savingPerContract := sourceDistanceSavingWeightMinutes*distance + averageUnitsSavingWeightMinutes*good.AvgUnits
	valueDensity := good.Frequency * savingPerContract / good.AvgUnits
	return bufferCandidate{
		selection: bufferSelection{
			good:         good.Good,
			capUnits:     bufferCapUnits(good.AvgUnits),
			averageUnits: good.AvgUnits,
			unitsPerHour: good.Frequency * good.AvgUnits,
			distance:     distance,
		},
		score: valueDensity,
	}, true
}

// bufferCapUnits is avg_units + 50% margin, never below one unit: one
// contract's worth plus headroom, so no single good over-fills the warehouse
// and starves the rest.
func bufferCapUnits(averageUnits float64) int {
	units := int(math.Ceil(averageUnits * bufferCapMarginFactor))
	if units < 1 {
		return 1
	}
	return units
}

func sortBufferCandidates(candidates []bufferCandidate) {
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].score != candidates[right].score {
			return candidates[left].score > candidates[right].score
		}
		return candidates[left].selection.good < candidates[right].selection.good
	})
}

func dedupeBufferCandidates(ranked []bufferCandidate) []bufferCandidate {
	seen := make(map[string]bool, len(ranked))
	unique := ranked[:0]
	for _, candidate := range ranked {
		if seen[candidate.selection.good] {
			continue
		}
		seen[candidate.selection.good] = true
		unique = append(unique, candidate)
	}
	return unique
}

func desiredBufferedGoods(selected []bufferSelection) []DesiredBufferedGood {
	if len(selected) == 0 {
		return nil
	}
	goods := make([]DesiredBufferedGood, 0, len(selected))
	for _, selection := range selected {
		goods = append(goods, DesiredBufferedGood{Good: selection.good, UnitsCap: selection.capUnits})
	}
	return goods
}

// ---- count sizing ------------------------------------------------------------

// warehouseCount fits the buffered volume in freighter-class holds. Nothing
// buffered ⇒ no warehouse: coverage alone is the co-located worker.
func warehouseCount(selected []bufferSelection) int {
	volume := 0
	for _, selection := range selected {
		volume += selection.capUnits
	}
	if volume <= 0 {
		return 0
	}
	return int(math.Ceil(float64(volume) / warehouseHoldUnits))
}

// stockerCount keeps restock_throughput ≥ consumption_rate: the buffered
// goods' consumption (units/hr) against one stocker's round-trip cadence over
// the consumption-weighted mean source distance. Nothing buffered ⇒ no
// stocker. The sanity ceiling keeps spurious good-frequency telemetry from
// manifesting as an unbounded stocker count.
func stockerCount(selected []bufferSelection) int {
	consumptionUnitsPerHour, meanDistance := consumptionProfile(selected)
	if consumptionUnitsPerHour <= 0 {
		return 0
	}
	tripSeconds := 2*meanDistance*stockerTravelSecondsPerDistance + stockerTradeOverheadSeconds
	unitsPerHourPerStocker := stockerCargoUnits * secondsPerHour / tripSeconds
	return sizeCount(consumptionUnitsPerHour/unitsPerHourPerStocker, maxStockerCountPerHub)
}

// sizeCount turns a fractional hull sizing into a count within [1, ceiling].
// The min() runs BEFORE the float→int conversion so pathological telemetry
// cannot overflow the conversion either.
func sizeCount(fractional float64, ceiling int) int {
	count := int(math.Ceil(math.Min(fractional, float64(ceiling))))
	if count < 1 {
		return 1
	}
	return count
}

// consumptionProfile totals the selected goods' consumption rate and its
// consumption-weighted mean source distance.
func consumptionProfile(selected []bufferSelection) (unitsPerHour, meanDistance float64) {
	weightedDistance := 0.0
	for _, selection := range selected {
		unitsPerHour += selection.unitsPerHour
		weightedDistance += selection.unitsPerHour * selection.distance
	}
	if unitsPerHour <= 0 {
		return 0, 0
	}
	return unitsPerHour, weightedDistance / unitsPerHour
}

// ---- signal lookups ------------------------------------------------------------

// stockerBudgetUnits resolves the per-hub buffered-volume budget; a zero
// calibration defers to the planner's documented default (governor.go).
func stockerBudgetUnits(cal Calibration) int {
	if cal.StockerCapacityBudget <= 0 {
		return DefaultStockerCapacityBudgetUnits
	}
	return cal.StockerCapacityBudget
}

// performanceByHub indexes performance by hub symbol. Duplicate entries for
// one hub (malformed sense) keep the slowest cycle — conservative and
// independent of input order.
func performanceByHub(hubs []HubPerformance) map[string]HubPerformance {
	byHub := make(map[string]HubPerformance, len(hubs))
	for _, hub := range hubs {
		existing, seen := byHub[hub.HubSymbol]
		if seen && existing.CycleTimeSeconds >= hub.CycleTimeSeconds {
			continue
		}
		byHub[hub.HubSymbol] = hub
	}
	return byHub
}

// sourceDistancesByHub indexes source distances by hub then good. Duplicate
// entries (malformed sense) keep the longest distance — the conservative
// stocker cost, independent of input order.
func sourceDistancesByHub(distances []GoodSourceDistance) map[string]map[string]float64 {
	byHub := make(map[string]map[string]float64, len(distances))
	for _, entry := range distances {
		goods := byHub[entry.HubSymbol]
		if goods == nil {
			goods = map[string]float64{}
			byHub[entry.HubSymbol] = goods
		}
		existing, seen := goods[entry.Good]
		if seen && existing >= entry.Distance {
			continue
		}
		goods[entry.Good] = entry.Distance
	}
	return byHub
}
