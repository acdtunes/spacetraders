package commands

// run_tour_coordinator_relocation_regions.go — the tour coordinator's REGION-OBSERVER seam for the
// opportunity relocator (sp-zvywu Part 2).
//
// WHY IT LIVES HERE. The relocator's RelocatorRegionObserver port asks a question only the tour
// coordinator can answer honestly: "what would THIS ground pay a trade hull?" Answering it means
// discovering gate-reachable systems, reading their cached markets, and asking the SOLVER to price
// the tour a hull would fly there — the exact pre-flight the margins-death rescue
// (run_tour_coordinator_reposition.go) and the rate-floor rescue (run_tour_coordinator_rate_floor.go)
// already run. A second implementation of that pre-flight is how a third relocation trigger starts
// disagreeing with the other two about what a ground is worth, so this file adds NO pricing: it is a
// thin re-shaping of buildRepositionCandidates' proven parts into the relocator's value type.
//
// WHAT IT REUSES, PART BY PART:
//
//   - DISCOVERY: legs.repositionNeighborsWithinJumps — the bounded, DURABLE-adjacency BFS (sp-jeou),
//     walked to the relocator's OWN hopRadius rather than the reposition flight's 12-jump bound,
//     because "system + 2-hop neighbours" is the region radius the bead specifies.
//   - PER-NEIGHBOUR SCREENING + PRE-RANK: scoreRepositionNeighbors — unbuilt-gate rejection, the
//     marketDataAgeFloor fresh-lane filter, the stale-lane counter, and the capped-spread pre-rank score.
//   - HOP-DECAYED ORDERING: repositionDecayedScore over resolveRepositionReachHopDecay, the sp-uf64
//     ranking, so the bounded pre-flight spends its planner calls on the same candidates the reach
//     path would.
//   - PRICING: planAtCandidate — the SYNTHETIC ship state at the candidate's landing waypoint over
//     the candidate-centred tour graph, budgeted exactly as Handle budgets a live tour.
//
// THE PROJECTED RATE IS GENUINE OR IT IS ABSENT. There is no third option in this file. The rate is
// the solver's own projection for the tour it would fly at the candidate, over that tour's own
// wall-clock; when the pre-flight declines, errors, or carries no usable time estimate, the region is
// emitted with RateReadable:false and the relocator excludes it (fail closed). Nothing here
// estimates, interpolates, or substitutes a rate — a fabricated rate would send a hull to ground
// that does not exist.
//
// THE DEADHEAD IS NOT NETTED INTO THE RATE, deliberately, and this is the one place this observer
// differs from repositionCandidateRate. That function amortises the crossing INTO the rate
// (freshProfit / (hops·crossing + replan + plan)) because its consumers rank candidates on a single
// number. The relocator's valuation charges travel SEPARATELY and explicitly —
// trading.RelocationInputs.TravelHours priced through the swappable TravelHopModel, plus
// CurrentRate×TravelHours as opportunity cost, plus a productive window shortened by the crossing —
// so netting it here too would charge the flight twice and refuse moves that genuinely pay.
// ProjectedRate is therefore the STEADY-STATE earning rate at the region, which is exactly what
// trading.RelocationInputs.ProjectedRate documents itself to be.

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// relocationRegionCandidateBudget bounds how many pre-ranked candidates get a real planner
// pre-flight per ObserveRegions call: repositionMaxCandidatesDefault, the SAME bound the
// margins-death rescue applies to its own fan-out (maybeReposition's top-K). The BFS surfaces up to
// repositionBfsMaxSystems (64) systems, so without a bound one tick would fire 64 solver calls PER
// HULL — precisely the unbounded solver fan-out the reposition paths exist to avoid. The cheap
// capped-spread pre-rank orders every candidate; only the top few are priced.
const relocationRegionCandidateBudget = repositionMaxCandidatesDefault

// ObserveRegions implements the relocator's RelocatorRegionObserver port: the candidate regions
// reachable from originSystem within hopRadius gate hops, each carrying the rate the planner projects
// for a trade hull standing at its landing waypoint.
//
// An EMPTY result is an honest verdict, not a failure: it means no gate-reachable system inside the
// radius carries a fresh cached market. An ERROR means the observation itself could not be made (no
// model artifact, an unreadable treasury budget, no hull to price a tour for), which the relocator
// counts as regions_unreadable and scores nothing for the hull — fail closed either way, but the two
// are distinguishable in the heartbeat.
func (h *RunTourCoordinatorHandler) ObserveRegions(ctx context.Context, playerID int, originSystem string, hopRadius int) ([]RelocatorRegion, error) {
	// A SYNTHESISED command carrying only the identity. Every knob left at zero resolves to its
	// documented default (RULINGS #5), which is precisely the intent: the pre-flight must price the
	// tour a DEFAULT-LAUNCHED hull would fly at the candidate, not one shaped by some other
	// container's launch config. The handler-level configuration a real tour also honours — the
	// cargo blocklist, the ranker age caps, the scan policy, the capital-work sensor — lives on the
	// HANDLER, not the command, so it still applies unchanged.
	cmd := &RunTourCoordinatorCommand{PlayerID: playerID}

	budget, err := h.relocationPreflightBudget(ctx, cmd)
	if err != nil {
		return nil, err
	}
	ship, err := h.relocationOriginHull(ctx, playerID, originSystem)
	if err != nil {
		return nil, err
	}

	now := h.clock.Now()
	// Walk the DURABLE gate adjacency to the RELOCATOR's radius. Not resolveRepositionJumpBound (12):
	// that bound describes how far the reposition FLIGHT may route, whereas hopRadius is how far the
	// relocator is willing to consider ground at all. A radius <= 1 degrades inside
	// repositionNeighborsWithinJumps to the plain 1-hop scan, so a trivial radius is safe.
	neighbours, originReason := h.legs.repositionNeighborsWithinJumps(ctx, originSystem, playerID, hopRadius)
	candidates, rejections := h.scoreRepositionNeighbors(ctx, cmd, originSystem, neighbours, now)
	if len(candidates) == 0 {
		// Reuse the reposition path's own self-diagnosing empty log so a relocator that finds no
		// ground names WHY (origin-level no-adjacency vs per-neighbour stale/unbuilt), instead of
		// costing a canary flight to explain (the sp-1ki5 #3 lesson). No ship symbol is in scope
		// here — the region set is per SYSTEM, shared by every hull standing in it.
		logRepositionDiscoveryEmpty(common.LoggerFromContext(ctx), ship.ShipSymbol(), originSystem, neighbours, rejections, originReason)
		return nil, nil
	}
	candidates = rankRelocationRegionCandidates(cmd, candidates)
	// The same exploration slot the margins-death rescue spends, and for the same reason: this IS
	// the reposition ranking, so without it the relocator observes the same few grounds around a
	// given system on every tick and the rest of the region set is never priced at all.
	candidates, priceable := h.admitExplorationCandidate(ctx, cmd, candidates, relocationRegionCandidateBudget)

	regions := make([]RelocatorRegion, 0, priceable)
	for i, candidate := range candidates {
		if i >= priceable {
			// Candidates past the bound are OMITTED, not emitted as RateReadable:false. A region we
			// never priced is not a region whose price is unreadable, and conflating them would
			// inflate the relocator's region_rate_unreadable counter with candidates that were
			// merely out-ranked — turning a healthy bounded pre-flight into a false alarm.
			common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Relocator regions from %s: priced the top %d of %d pre-ranked candidate(s); the rest are out-ranked, not unreadable", originSystem, priceable, len(candidates)), map[string]interface{}{
				"origin_system": originSystem, "priced": priceable, "pre_ranked": len(candidates), "hop_radius": hopRadius,
			})
			break
		}
		regions = append(regions, h.observeOneRegion(ctx, cmd, ship, candidate, budget, now))
	}
	return regions, nil
}

// observeOneRegion prices ONE pre-ranked candidate into a RelocatorRegion.
//
// Every field is an OBSERVATION or it is withheld. GateHops and LandingWaypoint come from the
// discovery that produced the candidate; ProjectedRate comes from the solver or the region is marked
// unreadable; SnapshotAge and Activity come from the very listings the candidate was built from, or
// the region is marked unreadable — because a region whose freshness cannot be established would
// otherwise slip past the relocator's per-activity staleness exclusion at a fabricated age of zero,
// which is the same failure as a fabricated rate wearing a different hat.
func (h *RunTourCoordinatorHandler) observeOneRegion(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	ship *navigation.Ship,
	candidate repositionCandidate,
	budget tourPlanBudget,
	now time.Time,
) RelocatorRegion {
	region := RelocatorRegion{
		AnchorSystem:    candidate.system,
		LandingWaypoint: candidate.waypoint,
		GateHops:        relocationRegionGateHops(candidate),
	}

	age, activity, freshnessKnown := h.relocationRegionFreshness(ctx, cmd, candidate.system, now)
	if !freshnessKnown {
		logRelocationRegionUnreadable(ctx, candidate.system, "snapshot_freshness_unreadable")
		return region // RateReadable stays false — fail closed
	}
	region.SnapshotAge, region.Activity = age, activity

	plan, perr := h.planAtCandidate(ctx, ship, candidate, cmd, budget)
	rate, ok := relocationProjectedRate(plan)
	if !ok {
		// The pre-flight declined, errored, or carried no usable time estimate. Name WHICH — the same
		// three-way disambiguation logRepositionRanking makes (solver-infeasible vs planner-error vs
		// no rate estimate), because "the relocator never moves anyone" reads identically for all
		// three and they demand different fixes.
		logRelocationRegionUnreadable(ctx, candidate.system, relocationRateUnreadableReason(plan, perr))
		return region
	}
	region.ProjectedRate, region.RateReadable = rate, true
	return region
}

// relocationProjectedRate is the region's STEADY-STATE projected credits/hour for the pre-flighted
// hull, or ok=false when the plan cannot support one.
//
// It is fresh profit over the PLAN's OWN wall-clock: fresh profit is
// ProjectedProfit − HeldLiquidation − DepositValue (the honest new-cash earning, the same basis both
// existing relocation triggers gate on, so all three agree on what a ground is worth), and the
// wall-clock is recovered by inverting the solver's cph exactly as repositionCandidateRate does
// (cph = profit/(seconds/3600) => seconds = profit/cph×3600 — pure algebra on the response, no proto
// change). The crossing is NOT in the denominator; see the file header.
//
// ok=false whenever the plan is absent, infeasible, or carries no usable time estimate
// (ProjectedProfit <= 0 or cph <= 0 — a degenerate or mocked planner). That is the divide-by-zero pin
// repositionCandidateRate keeps, and here it is also the fail-closed contract: the relocator must
// never see a rate this file had to guess at.
func relocationProjectedRate(plan *routing.TourPlan) (float64, bool) {
	if plan == nil || !plan.Feasible || plan.ProjectedProfit <= 0 || plan.ProjectedCreditsPerHour <= 0 {
		return 0, false
	}
	planHours := float64(plan.ProjectedProfit) / plan.ProjectedCreditsPerHour
	if planHours <= 0 {
		return 0, false
	}
	freshProfit := plan.ProjectedProfit - plan.HeldLiquidation - plan.DepositValue
	if freshProfit <= 0 {
		// A ground whose whole projected profit is launch-liquidation or synthetic deposit value earns
		// the hull NO new cash. Refusing it here is the same discipline the reposition floor applies to
		// fresh profit — and reporting a non-positive rate as readable would let the relocator's uplift
		// bar compare a real rate against a bookkeeping artefact.
		return 0, false
	}
	return float64(freshProfit) / planHours, true
}

// relocationRegionGateHops is the candidate's gate-hop distance with the SAME defensive floor
// repositionDecayedScore and repositionCandidateRate apply: an unstamped candidate (hops <= 0) from
// some future discovery path is charged one hop, never a free crossing. Zero would price the flight
// as instantaneous and hand the NPV an infinite productive window.
func relocationRegionGateHops(candidate repositionCandidate) int {
	if candidate.hops < 1 {
		return 1
	}
	return candidate.hops
}

// rankRelocationRegionCandidates orders the candidate set the way the armed reach path does
// (repositionReachCandidates): capped-spread score decayed per gate hop, system symbol as the stable
// tie-break so the bounded pre-flight is deterministic. Sharing the ordering is what makes the
// relocator's bounded pre-flight spend its planner calls on the SAME grounds the reach discovery
// would have chosen.
func rankRelocationRegionCandidates(cmd *RunTourCoordinatorCommand, candidates []repositionCandidate) []repositionCandidate {
	decay := resolveRepositionReachHopDecay(cmd.RepositionReachHopDecayPct)
	ranked := make([]repositionCandidate, len(candidates))
	copy(ranked, candidates)
	sort.SliceStable(ranked, func(i, j int) bool {
		di, dj := repositionDecayedScore(ranked[i], decay), repositionDecayedScore(ranked[j], decay)
		if di != dj {
			return di > dj
		}
		return ranked[i].system < ranked[j].system
	})
	return ranked
}

// relocationRegionFreshness reports how old the region's market snapshot is and which activity level
// selects its freshness cap — the two facts the relocator's per-activity staleness exclusion
// (regionUsable against RankerAgeCaps) is judged on.
//
// It reads the SAME cached listings the candidate was pre-ranked from and pairs the HEADLINE LANE's
// two quotes, because that lane IS what makes the region worth flying to: the source waypoint the
// hull lands at and the sink it sells into. Then, deliberately conservative on both axes:
//
//   - AGE is the OLDER of the two quotes. A lane is only as trustworthy as its staler side; taking
//     the fresher one would let a day-old sink hide behind a minutes-old source.
//   - ACTIVITY is whichever of the two yields the TIGHTER cap under the handler's own age-cap table.
//     RankerAgeCaps exists because an 8-hour-old quote on a WEAK market is still rankable while a
//     40-minute-old one on a STRONG market is not; a lane spanning both must be held to the strict end.
//
// ok=false when the system's listings cannot be read or hold no fresh lane at all — the caller then
// marks the region unreadable rather than reporting an age of zero, which would silently defeat the
// exclusion. A system with cached markets but no in-system lane still answers: bestInSystemLane's own
// fallback (its first cached market) is the representative quote, exactly as the pre-rank treats it.
func (h *RunTourCoordinatorHandler) relocationRegionFreshness(ctx context.Context, cmd *RunTourCoordinatorCommand, system string, now time.Time) (time.Duration, string, bool) {
	listings, err := h.legs.collectSystemListings(ctx, system, cmd.PlayerID)
	if err != nil || len(listings) == 0 {
		return 0, "", false
	}
	// The SAME marketDataAgeFloor pre-filter scoreRepositionNeighbors applied when it built the candidate,
	// so the lane read here is the lane the region was ranked on.
	fresh := freshListings(listings, now, h.listingMaxAge(ctx, cmd.PlayerID))
	if len(fresh) == 0 {
		return 0, "", false
	}
	lanes := trading.RankSpreads(fresh)
	if len(lanes) == 0 {
		representative := fresh[0]
		return relocationListingAge(representative, now), representative.Activity, true
	}
	lane := lanes[0]
	source, sourceFound := findRelocationListing(fresh, lane.SourceWaypoint, lane.Good)
	sink, sinkFound := findRelocationListing(fresh, lane.DestWaypoint, lane.Good)
	if !sourceFound || !sinkFound {
		// RankSpreads built the lane FROM these listings, so both sides are present by construction.
		// Failing closed on the impossible branch costs nothing and keeps a future ranking change from
		// silently degrading into a fabricated age.
		return 0, "", false
	}
	age := relocationListingAge(source, now)
	if sinkAge := relocationListingAge(sink, now); sinkAge > age {
		age = sinkAge
	}
	return age, h.harsherStalenessActivity(source.Activity, sink.Activity), true
}

// relocationListingAge is how old a cached quote is. A ZERO ObservedAt means "unknown age" and reads
// as age 0 — the fail-OPEN GoodListing/BuildTourSnapshot convention freshListings already applies, so
// an unstamped row (older tests, a repository that does not populate it) ranks as fresh here for the
// same reason it ranks as fresh there rather than being judged by a second, stricter rule.
func relocationListingAge(listing trading.GoodListing, now time.Time) time.Duration {
	if listing.ObservedAt.IsZero() {
		return 0
	}
	age := now.Sub(listing.ObservedAt)
	if age < 0 {
		return 0 // a clock-skewed future observation is not "negatively stale"
	}
	return age
}

// findRelocationListing locates the (waypoint, good) quote a ranked lane was priced from.
func findRelocationListing(listings []trading.GoodListing, waypoint, good string) (trading.GoodListing, bool) {
	for _, listing := range listings {
		if listing.Waypoint == waypoint && listing.Good == good {
			return listing, true
		}
	}
	return trading.GoodListing{}, false
}

// harsherStalenessActivity returns whichever of a lane's two activity levels is charged MORE for
// the same quote age, so the region is reported under the half of its pricing that ages worst. It
// reads the same StalenessDiscount the ranker charges, so label and haircut cannot drift apart.
func (h *RunTourCoordinatorHandler) harsherStalenessActivity(a, b string) string {
	const probe = time.Hour
	if h.stalenessDiscount.DriftFraction(b, probe) > h.stalenessDiscount.DriftFraction(a, probe) {
		return b
	}
	return a
}

// relocationPreflightBudget resolves the four inputs planAtCandidate needs, EXACTLY as Handle
// resolves them for a live tour, so a region is priced under the same budget and model a real tour
// there would fly under (RULINGS #4: no money guard is read differently, relaxed, or bypassed — the
// pre-flight commits nothing and this only decides what the SOLVER is told it may spend).
//
// Each unreadable input is an ERROR, not a default:
//   - the model artifact bounds the planner's version; Handle exits the tour "unavailable" when it
//     cannot be read, and here the whole region set is unobservable for the same reason;
//   - the dynamic budget fails CLOSED when a wired treasury source cannot be read. Handle pauses and
//     retries; here the equivalent is refusing to observe, because pricing every candidate against a
//     spend cap of 0 would render them all infeasible and report that as the GROUND being poor.
//     An ABSENT treasury source (no apiClient) is not unreadable — it means no explicit cap, exactly
//     as in Handle, with the per-buy working-capital floor still guarding every real spend.
func (h *RunTourCoordinatorHandler) relocationPreflightBudget(ctx context.Context, cmd *RunTourCoordinatorCommand) (tourPlanBudget, error) {
	artifactPath := h.modelArtifactPath
	if artifactPath == "" {
		artifactPath = defaultModelArtifactPath
	}
	modelVersion, err := readTourModelVersion(artifactPath)
	if err != nil {
		return tourPlanBudget{}, fmt.Errorf("relocator regions unobservable: tour model artifact unreadable (%s): %w", artifactPath, err)
	}
	// The synthesised command carries no launch reserve, so this is the coordinator's own documented
	// default — the same value Handle resolves a captain CLI tour to.
	reserve := cmd.WorkingCapitalReserve
	if reserve == 0 {
		reserve = int64(defaultWorkingCapitalReserve)
	}
	maxHops := cmd.MaxHops
	if maxHops <= 0 || maxHops > maxTourHops {
		maxHops = maxTourHops
	}
	maxSpend := cmd.MaxSpend
	if maxSpend == 0 {
		resolved, unreadable := h.defaultMaxSpend(ctx, cmd.PlayerID, reserve)
		if unreadable {
			return tourPlanBudget{}, fmt.Errorf("relocator regions unobservable: live treasury unreadable, so no candidate can be priced against an honest budget")
		}
		maxSpend = resolved
	}
	return tourPlanBudget{maxHops: maxHops, maxSpend: maxSpend, reserve: reserve, modelVersion: modelVersion}, nil
}

// relocationOriginHull picks the hull whose capacity, engine and role the candidate tours are priced
// for: an active TRADE-dedicated hull standing in originSystem.
//
// The port is keyed on SYSTEM, not ship, because a region set is shared by every hull standing in
// that system — which is also why a representative hull is the honest choice rather than a
// compromise. The relocator only ever calls this with the current system of a live trade hull it just
// observed, so one is there by construction; the hulls that share a system share the class facts the
// pre-flight actually consumes (hold size, engine speed, fuel capacity), and planAtCandidate zeroes
// the per-hull cargo and fuel anyway (sp-m9co: a jumping hull arrives with an available hold and full
// tanks), so what remains is a CLASS projection, not one hull's idiosyncratic state.
//
// An unreadable fleet, or no trade hull in the system, is an ERROR: without a hull there is no tour
// to price, and inventing a synthetic hull shape would put a fabricated rate in front of the
// relocator through the back door.
func (h *RunTourCoordinatorHandler) relocationOriginHull(ctx context.Context, playerID int, originSystem string) (*navigation.Ship, error) {
	if h.legs == nil || h.legs.shipRepo == nil {
		return nil, fmt.Errorf("relocator regions unobservable: no ship repository wired to resolve a hull at %s", originSystem)
	}
	ships, err := h.legs.shipRepo.FindAllByPlayer(ctx, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("relocator regions unobservable: reading the fleet to resolve a hull at %s: %w", originSystem, err)
	}
	for _, ship := range ships {
		if ship == nil || ship.DedicatedFleet() != tradeFleet {
			continue
		}
		location := ship.CurrentLocation()
		if location == nil || location.SystemSymbol != originSystem {
			continue
		}
		return ship, nil
	}
	return nil, fmt.Errorf("relocator regions unobservable: no trade-dedicated hull stands in %s to price a candidate tour for", originSystem)
}

// relocationRateUnreadableReason names WHY a candidate carries no usable projection, reusing the
// reposition ranking's own three-way disambiguation (repositionCandidateReason: the solver's verdict,
// a planner-error, or no plan) and adding the fourth case that is specific to a RATE: a feasible plan
// whose cph or profit cannot yield one.
func relocationRateUnreadableReason(plan *routing.TourPlan, perr error) string {
	if reason := repositionCandidateReason(plan, perr); reason != "" {
		return reason
	}
	return "no_rate_estimate"
}

// logRelocationRegionUnreadable records an EXCLUDED region and its reason in the MESSAGE TEXT (which
// `container logs` keeps even though it drops the structured metadata map — the sp-149h/sp-iqyq
// renderer defect). Fail-closed exclusions are the relocator's most likely silent-dormancy cause, so
// each one names itself rather than only incrementing a counter.
func logRelocationRegionUnreadable(ctx context.Context, system, reason string) {
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Relocator region %s excluded: no usable projection (%s) - reported unreadable, never scored at an assumed rate", system, reason), map[string]interface{}{
		"anchor_system": system, "reason": reason, "trigger": "opportunity_relocator",
	})
}
