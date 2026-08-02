package commands

import (
	"sort"
	"strings"
	"time"

	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// toSinkSales maps realized SELL legs to the slim scouting.SinkSale shape the demand EWMA folds,
// keeping the scouting domain free of the trading package (mirrors autooutfit's toLegSaturation). A
// BUY leg is not a sink and is dropped; the realized sell-value is RealizedUnits × RealizedUnitPrice
// (a skipped leg's RealizedUnits=0 ⇒ zero value ⇒ dropped by the domain fold), and the recency age
// is measured from RealizedAt.
func toSinkSales(legs []trading.TourLegTelemetry, now time.Time) []domainScouting.SinkSale {
	sales := make([]domainScouting.SinkSale, 0, len(legs))
	for _, leg := range legs {
		if leg.IsBuy {
			continue
		}
		sales = append(sales, domainScouting.SinkSale{
			Waypoint:   leg.Waypoint,
			Value:      float64(leg.RealizedUnits) * float64(leg.RealizedUnitPrice),
			AgeSeconds: now.Sub(leg.RealizedAt).Seconds(),
		})
	}
	return sales
}

// buildScanScope derives the tick's sensing scope: the systems the fleet operates in (traded ∪
// occupied) plus the bounded discovery allowance drawn from the richest systems outside it.
//
// A leg's system comes from the census's own waypoint index where the market is still present —
// the authoritative mapping — falling back to parsing the symbol for a market that has since left
// the census. An empty footprint yields an un-narrowed scope, so cold start and any evidence gap
// sense the whole census exactly as before.
//
// tradeEvidence false (no telemetry reader, or its read failed) refuses to narrow at all: the
// occupied set alone is not a footprint, and narrowing on it would release every system the fleet
// trades in but holds no hull in at this instant.
func buildScanScope(snapshots []domainScouting.SystemFreshnessSnapshot, legs []trading.TourLegTelemetry, tradeEvidence bool, occupied map[string]bool, cfg sizerConfig, now time.Time) domainScouting.ScanScope {
	if !tradeEvidence {
		return domainScouting.ScanScope{}
	}
	waypointSystem := make(map[string]string)
	for _, snap := range snapshots {
		for _, market := range snap.Markets {
			if market.Waypoint != "" {
				waypointSystem[market.Waypoint] = snap.SystemSymbol
			}
		}
	}
	visits := make([]domainScouting.TradeVisit, 0, len(legs))
	for _, leg := range legs {
		if leg.RealizedUnits <= 0 {
			continue // a skipped or degraded leg is not a trade
		}
		system, ok := waypointSystem[leg.Waypoint]
		if !ok {
			system = shared.ExtractSystemSymbol(leg.Waypoint)
		}
		visits = append(visits, domainScouting.TradeVisit{System: system, AgeSeconds: now.Sub(leg.RealizedAt).Seconds()})
	}
	traded := domainScouting.TradedFootprint(visits, cfg.FootprintRetention.Seconds())

	candidates := make([]domainScouting.DiscoveryCandidate, 0, len(snapshots))
	for _, snap := range snapshots {
		if snap.MarketCount <= 0 {
			continue
		}
		candidates = append(candidates, domainScouting.DiscoveryCandidate{System: snap.SystemSymbol, Weight: intrinsicSystemWeight(snap)})
	}
	return domainScouting.BuildScanScope(traded, occupied, candidates, cfg.DiscoveryAllowance)
}

// intrinsicSystemWeight totals a system's per-market value weights — the census's own
// Σ(trade_volume × price) prior — used to rank discovery candidates by how rich a market the fleet
// would be watching. A census with no per-market breakdown falls back to the market count so a
// candidate is still ranked above an emptier one rather than collapsing to zero.
func intrinsicSystemWeight(snap domainScouting.SystemFreshnessSnapshot) float64 {
	if len(snap.Markets) == 0 {
		return float64(snap.MarketCount)
	}
	total := 0.0
	for _, market := range snap.Markets {
		total += market.Weight
	}
	if total <= 0 {
		return float64(snap.MarketCount)
	}
	return total
}

// measuredAgeSeconds is the sp-r57g closed-loop ground truth: the (value-weighted) P90 market age
// the sizer sizes and releases against, replacing the tail-dominated max (OldestAgeSeconds). It
// falls back to OldestAgeSeconds when the census carries NO per-market breakdown — a census that
// predates sp-r57g, or an aggregate-only fixture — so the coordinator is byte-identical to
// pre-sp-r57g on the fallback path (percentile 100 also equals the max, the live rollback lever).
func measuredAgeSeconds(snap domainScouting.SystemFreshnessSnapshot, cfg sizerConfig) float64 {
	if len(snap.Markets) == 0 {
		return snap.OldestAgeSeconds
	}
	return domainScouting.WeightedPercentileAgeSeconds(snap.Markets, cfg.ValueWeighted, cfg.TargetPercentile)
}

// systemSizing is one system's input to the hull-target model: its census snapshot, the
// freshness targets it is judged against, and what it is manned with right now.
type systemSizing struct {
	snap        domainScouting.SystemFreshnessSnapshot
	sla         time.Duration
	cycle       time.Duration
	current     int
	fullyManned bool
}

// computeTarget is the per-system SIZE the sizer aims a post at, before release pacing. It runs
// an ordered pipeline: (1) the cycle-driven MODEL, where telemetry noise enters; (2) the
// sp-iupr issue-3b market-count CLAMP that bounds the noise; (3) the sp-tor9 CIRCUIT-OBSERVED
// BREACH RESPONSE that sizes a trusted, fully-manned post from its measured circuit; then the
// floor-of-1 and per-system cap.
//
// The two age-driven branches are deliberately DISJOINT (they must never collide):
//   - a TELEMETRY-STARVED system (its probes have not produced MinCycleSamples scan intervals)
//     has an age that reflects a MANNING failure, NOT a capacity shortfall — raising demand off
//     it only strands more probes (the issue-1 pathology). It stays on the static base (the
//     per-activity cohort sum, or the single-SLA market-count model) and is NEVER raised off the age;
//   - a TRUSTED, FULLY MANNED system is the OPPOSITE case: its P90 market age at the CURRENT hull
//     count is an honest circuit measurement, so the breach response sizes it straight from that
//     circuit. Gated on !starved, so it can never fire for the starved case above.
//
// sp-r57g SUPERSEDES sp-tor9's MAX-AGE premise: measuredAgeSeconds is now the (value-weighted) P90
// market age, NOT the tail-dominated OldestAgeSeconds (the max). The closed-loop measurement + the
// proportional CircuitRequiredHulls response are REUSED verbatim — only the metric feeding them
// changed, so a big system sizes to its ACHIEVABLE P90 (tail tolerated) instead of an unachievable
// max. A target_percentile of 100 makes measuredAgeSeconds == the max, recovering sp-tor9 exactly.
func computeTarget(sz systemSizing, cfg sizerConfig, measuredAgeSeconds float64) (target int, starved bool) {
	starved = sz.snap.CycleSamples < cfg.MinCycleSamples

	// 0. STATIC BASE — the required_probes = ceil(markets × cycle / sla) model. sp-j4kjv: when the
	//    census carries per-market ACTIVITY the base is the per-activity cohort SUM (each activity
	//    sized against its OWN SLA and the per-cohort needs summed); otherwise it is the single-SLA
	//    RequiredHulls over the whole market count. ONLY this base's sla input became per-activity —
	//    the age raise, clamp, circuit response, and release pacing below are UNCHANGED, each still
	//    keyed on the system SLA (`sla`), so a census with no activity signal is byte-identical.
	_, hasOverride := cfg.Overrides[sz.snap.SystemSymbol]
	staticBase, perActivity := activityStaticHulls(sz.snap, sz.cycle, cfg, hasOverride)
	if !perActivity {
		staticBase = domainScouting.RequiredHulls(sz.snap.MarketCount, sz.cycle, sz.sla)
	}

	// 1. MODEL — the static base (starved) or that base raised by the empirical P90 breach (trusted:
	//    sp-orgp/sp-r57g closed loop). The base is per-activity when the census carries activity.
	target = modelTargetFromBase(staticBase, sz.sla, starved, measuredAgeSeconds)

	// 2. MARKET-COUNT CLAMP (sp-iupr issue 3b) — bound the noise-driven model to what this
	//    market count could justify at the worst plausible cycle, capping a small-market system a
	//    noisy-high cycle over-sized (ZY16: 3 markets read as 6). The circuit response below is
	//    ground truth and is applied AFTER, so it may exceed this ceiling.
	target = domainScouting.ClampToMarketCount(target, sz.snap.MarketCount, cfg.WorstCycle, sz.sla)

	// 3. CIRCUIT-OBSERVED BREACH RESPONSE (sp-tor9) — supersedes the issue-3a +1 sanity floor with
	//    one coherent breach path. A TRUSTED, FULLY MANNED post's worst-case age at its CURRENT
	//    hull count directly measures its circuit period; the measured-cycle model cannot, because
	//    the pooled inter-scan interval deflates with probe count, collapsing the static estimate
	//    toward 1 on exactly the high-market systems that need many probes. Size to
	//    ceil(current × age/sla) (scaled by the breach-response knob): PROPORTIONAL to the breach
	//    on the way up (a 158min-at-60min post jumps toward coverage in one resize, not eight),
	//    and — because current × age ≈ markets × perMarketHop is CONSERVED as hulls change — a
	//    STABLE fixpoint at steady state (raising to it drops the age so the next tick re-derives
	//    the same target: no release-flap). It only RAISES here: a non-breaching post's circuit
	//    target is ≤ current, so max() leaves the model target untouched. DISJOINT from the starved
	//    branch by !starved (issue 1: a starved post's age is a manning signal, never a capacity
	//    one); the fullyManned gate keeps the age an HONEST reading — a partially-manned post's age
	//    reflects fewer working probes than its budget, so sizing off it would over-count.
	if !starved && sz.fullyManned {
		// The age fed to the breach-response circuit is the MEASURED P90 (value-weighted),
		// not the max — so the tail beyond the target percentile does not drive the raise.
		effectiveAge := breachResponseAge(measuredAgeSeconds, cfg.BreachResponsePercent)
		if circuitTarget := domainScouting.CircuitRequiredHulls(sz.current, effectiveAge, sz.sla); circuitTarget > target {
			target = circuitTarget
		}
	}

	if target < 1 {
		target = 1
	}
	if target > cfg.MaxProbesPerSystem {
		target = cfg.MaxProbesPerSystem
	}
	return target, starved
}

// breachResponseAge scales the measured age by the breach-response aggressiveness knob (sp-tor9)
// before it is fed to the circuit model — percent > 100 sizes for a proportionally WORSE effective
// age (equivalently, a tighter effective SLA), buying headroom against a circuit that under-measures
// in practice; 100 is the exact measured circuit; the coordinator's default chain guarantees a
// positive percent so this never zeroes the age. ageSeconds is the value-weighted P90, not
// the max — the tail beyond the target percentile does not inflate the breach response.
func breachResponseAge(ageSeconds float64, breachResponsePercent int) time.Duration {
	scaledSeconds := ageSeconds * float64(breachResponsePercent) / 100
	return time.Duration(scaledSeconds * float64(time.Second))
}

// modelTargetFromBase turns the STATIC required-hull base into the cycle-driven model target. A
// telemetry-starved system uses the base as-is and is NOT age-raised (issue 1: its age is a manning
// signal, not a capacity one); a trusted system raises the base by its empirical P90 breach (the
// sp-orgp/sp-r57g closed loop, via RaisedForBreach). The static base is either the per-
// activity cohort sum or the single-SLA RequiredHulls — computed by the caller (computeTarget), which
// is where the per-activity vs global-SLA decision lives; this function only applies the age raise.
func modelTargetFromBase(staticBase int, sla time.Duration, starved bool, measuredAgeSeconds float64) int {
	if starved {
		return staticBase
	}
	age := time.Duration(measuredAgeSeconds * float64(time.Second))
	return domainScouting.RaisedForBreach(staticBase, sla, age)
}

// activityStaticHulls is the per-activity static base: it partitions the system's markets by
// ACTIVITY and SUMS each cohort's RequiredHulls, sizing each cohort against ITS OWN SLA — a WEAK
// cohort tolerates a longer SLA and needs fewer probes than an equal STRONG one. It replaces the
// single-SLA RequiredHulls(MarketCount, cycle, sla) as the model's static base.
//
// It returns (_, false) — "no activity signal, size on the global SLA (byte-identical to
// pre-sp-j4kjv)" — when ANY of:
//   - the system carries a per-system SLA override (an explicit operator decision governs the WHOLE
//     system across activities, so per-activity heuristics must not second-guess it);
//   - the per-market breakdown does not fully account for MarketCount — a cold-start charted override
//     inflated MarketCount beyond the scanned markets, or an aggregate-only / pre-breakdown census;
//   - no market carries a KNOWN activity (a pre-activity census, or a test fixture): an all-unknown
//     system stays on the tuned global SLA rather than repricing every market at the RESTRICTED
//     default, which is what keeps every pre-sp-j4kjv sizing test byte-identical.
//
// Within a partitioned system a market whose activity is unknown/null joins the RESTRICTED cohort
// (the documented unknown default), so a mix of known + null markets sizes the null ones at RESTRICTED.
func activityStaticHulls(snap domainScouting.SystemFreshnessSnapshot, cycle time.Duration, cfg sizerConfig, hasOverride bool) (int, bool) {
	if hasOverride {
		return 0, false
	}
	if len(snap.Markets) == 0 || len(snap.Markets) != snap.MarketCount {
		return 0, false
	}
	counts := make(map[shared.ActivityLevel]int, 4)
	anyKnown := false
	for i := range snap.Markets {
		level, known := canonicalActivity(snap.Markets[i].Activity)
		if known {
			anyKnown = true
		}
		counts[level]++
	}
	if !anyKnown {
		return 0, false
	}
	total := 0
	for level, count := range counts {
		total += domainScouting.RequiredHulls(count, cycle, cfg.slaForActivity(level))
	}
	return total, true
}

// canonicalActivity maps a raw market_data.activity string to its canonical ActivityLevel. An empty
// or unrecognized value returns (ActivityLevelRestricted, false): unknown/null is SIZED at the
// RESTRICTED default, while the false flag lets activityStaticHulls tell an ALL-unknown
// system (fall back to the global SLA) from a mix carrying at least one known activity.
func canonicalActivity(raw string) (shared.ActivityLevel, bool) {
	switch shared.ActivityLevel(strings.ToUpper(strings.TrimSpace(raw))) {
	case shared.ActivityLevelWeak:
		return shared.ActivityLevelWeak, true
	case shared.ActivityLevelGrowing:
		return shared.ActivityLevelGrowing, true
	case shared.ActivityLevelStrong:
		return shared.ActivityLevelStrong, true
	case shared.ActivityLevelRestricted:
		return shared.ActivityLevelRestricted, true
	default:
		return shared.ActivityLevelRestricted, false
	}
}

// stepDownToward sheds exactly one probe, never below the target (the measured requirement).
func stepDownToward(current, target int) int {
	stepDown := current - 1
	if stepDown < target {
		stepDown = target
	}
	return stepDown
}

// releaseAggregateToPool is the sp-iopd reserved-frontier-floor RELEASE: it sheds one probe at a
// time from the LARGEST post (tie-break by system symbol for determinism) until the aggregate
// desired fits effectivePool or every post sits at its floor of 1. Each shed is a resize-DOWN the
// scout reconciler un-mans, returning the hull undedicated to the shared pool the frontier claims
// — never sold or retired. It caps the AGGREGATE only; the per-system computeTarget that produced
// each `desired` is untouched. Largest-first keeps freshness's smallest (cheapest-to-keep) posts
// intact longest, shedding from the systems best able to absorb one fewer probe.
func releaseAggregateToPool(desired map[string]int, effectivePool int) {
	if effectivePool < 0 {
		effectivePool = 0
	}
	for sumDesired(desired) > effectivePool {
		pick := ""
		for system, hulls := range desired {
			if hulls <= 1 {
				continue // never shed a post below one probe — that is retirement, not release
			}
			if pick == "" || hulls > desired[pick] || (hulls == desired[pick] && system < pick) {
				pick = system
			}
		}
		if pick == "" {
			return // every post already at its floor of 1 — the floor is unsatisfiable this tick
		}
		desired[pick]--
	}
}

// sumDesired totals a per-system desired-hulls map — the sizer's aggregate probe footprint.
func sumDesired(desired map[string]int) int {
	total := 0
	for _, hulls := range desired {
		total += hulls
	}
	return total
}

// initialScanDemand (sp-u8jc/sp-gucu) totals the one-probe-per-post initial-scan demand of the
// charted-but-unscanned systems: a system that carries a STANDING post, is charted WITH marketplace
// waypoints (chartedMarketplace[system] > 0), but is ABSENT from the scanned census (not market-
// bearing) needs one probe to make its first scan. It is bounded to ONE probe per post — never
// scaled by the marketplace count — and only for systems that already have a post (a mannable
// target), so it can never over-provision. A nil chartedMarketplace (knob off / reader unwired)
// yields 0, keeping the aggregate demand byte-identical.
func initialScanDemand(posts []*domainScouting.ScoutPost, marketBearing map[string]bool, chartedMarketplace map[string]int, scope domainScouting.ScanScope) int {
	demand := 0
	for _, post := range posts {
		if post.Kind != domainScouting.PostKindStanding {
			continue
		}
		if marketBearing[post.SystemSymbol] {
			continue // already counted by the census demand loop
		}
		if !scope.Includes(post.SystemSymbol) {
			continue // outside the sensing scope — its post is released, so it needs no scan capacity
		}
		if chartedMarketplace[post.SystemSymbol] > 0 {
			demand++ // ONE probe for the initial scan — never scaled by the marketplace count
		}
	}
	return demand
}

// resolveCycleSeconds picks the per-market cycle for a system: its own MEASURED cycle when
// it has cleared the sample floor, else the fleet-wide median of trusted measurements, else
// the seed default. This keeps the cycle EMPIRICAL (never a bare constant) while degrading
// gracefully before telemetry exists. A system's own measurement is DAMPENED toward the fleet-
// wide median (sp-iupr issue 3c): per-system cycle telemetry is noisy, so shrinking each
// reading toward the pooled robust estimate makes equal-market systems converge on the same
// target instead of diverging on noise. A single trusted system (median == own) or a 0%
// dampening is a no-op, so this never perturbs the single-system or launch-frozen paths.
func resolveCycleSeconds(snap domainScouting.SystemFreshnessSnapshot, globalCycleSeconds float64, cfg sizerConfig) time.Duration {
	seconds := cfg.SeedCycle.Seconds()
	switch {
	case snap.CycleSamples >= cfg.MinCycleSamples && snap.MeasuredCycleSeconds > 0:
		seconds = domainScouting.DampenedCycleSeconds(snap.MeasuredCycleSeconds, globalCycleSeconds, cfg.CycleDampeningPercent)
	case globalCycleSeconds > 0:
		seconds = globalCycleSeconds
	}
	return time.Duration(seconds * float64(time.Second))
}

// aggregateMeasuredCycleSeconds is the fleet-wide median of the per-system measured cycles
// that cleared the sample floor — the fallback for a market-bearing system that does not yet
// have enough samples of its own. 0 ⇒ no system has a trusted measurement yet.
func aggregateMeasuredCycleSeconds(snapshots []domainScouting.SystemFreshnessSnapshot, minSamples int) float64 {
	var trusted []float64
	for _, snap := range snapshots {
		if snap.CycleSamples >= minSamples && snap.MeasuredCycleSeconds > 0 {
			trusted = append(trusted, snap.MeasuredCycleSeconds)
		}
	}
	if len(trusted) == 0 {
		return 0
	}
	sort.Float64s(trusted)
	mid := len(trusted) / 2
	if len(trusted)%2 == 1 {
		return trusted[mid]
	}
	return (trusted[mid-1] + trusted[mid]) / 2
}

func indexPostsBySystem(posts []*domainScouting.ScoutPost) map[string]*domainScouting.ScoutPost {
	index := make(map[string]*domainScouting.ScoutPost, len(posts))
	for _, post := range posts {
		index[post.SystemSymbol] = post
	}
	return index
}
