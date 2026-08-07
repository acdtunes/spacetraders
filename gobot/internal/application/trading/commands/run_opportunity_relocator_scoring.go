package commands

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// relocationCandidate is one priced (hull, region) pair eligible to be committed.
type relocationCandidate struct {
	hull      RelocatorHull
	region    RelocatorRegion
	valuation trading.RelocationValuation
}

// scoreCandidates prices every eligible (hull, region) pair and returns the licensed ones, best NPV
// first. Every exclusion is counted by reason for the heartbeat.
func (h *RunOpportunityRelocatorHandler) scoreCandidates(
	ctx context.Context,
	cmd *RunOpportunityRelocatorCommand,
	hulls []RelocatorHull,
	state *relocatorState,
	result *RelocatorTickResult,
) []relocationCandidate {
	telemetry := h.readTelemetry(ctx, cmd)
	remainingEra, eraKnown := h.readEraHorizon(ctx, cmd)
	cooldown := cmd.hullCooldown()

	// SCORE OFFERED HULLS FIRST — not merely commit them first.
	//
	// The candidate sort below already puts offered hulls at the head of the COMMIT order, and that was
	// necessary but not sufficient: it runs after the whole loop, so an offered hull's window was being
	// spent on OTHER hulls' region pre-flights before its own commit could be reached. Each eligible
	// hull costs up to relocationRegionCandidateBudget planner calls, and with the fleet at 52 hulls the
	// scoring phase measured 184s / 304s / 664s from licensing to actuation against a 150s offer window.
	// Every one of the fleet's 98 offers lapsed unclaimed, and zero were ever taken.
	//
	// An offered hull is the ONLY hull under a clock: its tour is stalled, waiting, and will resume at
	// the deadline whatever we do. An un-offered hull is under no such pressure and will still be here
	// next tick. So the scarce thing to protect is not planner budget, it is the offered hull's WINDOW,
	// and the way to protect it is to reach its commit while the offer still stands.
	//
	// Sorted in place: hulls is the observation's own slice and this reorders it for the rest of the
	// tick, which is exactly the intent. Stable, so the observation's order survives within each group
	// and the change is a pure reordering rather than a reshuffle.
	sort.SliceStable(hulls, func(i, j int) bool { return hulls[i].Offered && !hulls[j].Offered })

	var candidates []relocationCandidate
	for _, hull := range hulls {
		if reason, eligible := h.hullEligibility(hull, state, cooldown); !eligible {
			result.skip(reason)
			continue
		}
		currentRate, rateReadable := trading.EwmaHullTourRate(telemetry, hull.ShipSymbol, trading.DefaultHullRateSmoothing)
		if !rateReadable {
			result.skip("hull_rate_unreadable") // fail closed: never move a hull off a guessed rate
			continue
		}
		result.Evaluated++
		candidates = append(candidates, h.scoreHull(ctx, cmd, hull, currentRate, remainingEra, eraKnown, state, result)...)
	}
	// OFFERED FIRST, then by NPV. An offered hull's tour is STALLED and its window is closing; an
	// un-offered idle hull is under no clock and will still be here next tick. Ranking purely by NPV
	// would spend a scarce concurrency budget on the unhurried hull and let the offer lapse — wasting
	// the very window the offer exists to create, and paying the tour's idle time for nothing.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].hull.Offered != candidates[j].hull.Offered {
			return candidates[i].hull.Offered
		}
		return candidates[i].valuation.NPV > candidates[j].valuation.NPV
	})
	return candidates
}

// hullProtected reports whether a hull's OWNERSHIP facts forbid relocating it, and names which one.
//
// It is the ONE definition of "this hull belongs to someone else", read at BOTH gates: at scoring
// (hullEligibility) and again at commit (actuationVerdict, sp-x2jr6 slice 1). Sharing it is the point —
// two separately-written copies of the RULINGS #7 protections would drift the moment a fourth
// protection fact is added, and the copy that got missed would be the one guarding the actual move.
//
// It deliberately covers ONLY ownership. The relocator's own bookkeeping — the per-hull cooldown, an
// already-in-flight move — is not ownership and is checked once, at scoring, where it belongs: applying
// "already_relocating" at commit would refuse every resumed move, which is the one move a restart MUST
// finish.
func hullProtected(hull RelocatorHull) (string, bool) {
	switch {
	case hull.IsCommandFrigate:
		return "command_frigate_protected", true // RULINGS #7
	case hull.Pinned:
		return "pinned_hull_protected", true // RULINGS #7
	case hull.OnTour && !hull.Offered:
		// Only at honest tour release — OR at a boundary its tour has explicitly offered. The offer is
		// the ONE exemption here, and it is deliberately narrow: it lifts the mid-tour rule and nothing
		// else, because a stalled tour is a hull nobody is using, whereas the two cases above are
		// ownership and are never waived.
		return "mid_tour", true
	}
	return "", false
}

// hullEligibility applies every non-economic exclusion, in order of how cheap it is to check. The
// reason string is what the heartbeat counts.
func (h *RunOpportunityRelocatorHandler) hullEligibility(hull RelocatorHull, state *relocatorState, cooldown time.Duration) (string, bool) {
	if reason, protected := hullProtected(hull); protected {
		return reason, false
	}
	if state.movingOrSettling[hull.ShipSymbol] {
		return "already_relocating", false
	}
	if last, seen := state.lastRelocation[hull.ShipSymbol]; seen && h.clock.Now().Sub(last) < cooldown {
		return "within_cooldown", false
	}
	return "", true
}

// scoreHull prices one hull against every region it could reach.
func (h *RunOpportunityRelocatorHandler) scoreHull(
	ctx context.Context,
	cmd *RunOpportunityRelocatorCommand,
	hull RelocatorHull,
	currentRate, remainingEra float64,
	eraKnown bool,
	state *relocatorState,
	result *RelocatorTickResult,
) []relocationCandidate {
	logger := common.LoggerFromContext(ctx)
	regions, err := h.regions.ObserveRegions(ctx, cmd.PlayerID, hull.CurrentSystem, cmd.regionHopRadius())
	if err != nil {
		// Fail closed: an unreadable region set scores NOTHING for this hull.
		result.skip(skipReasonRegionsUnreadable)
		logger.Log("WARN", fmt.Sprintf("Opportunity relocator: regions around %s unreadable for %s, scoring nothing there: %v", hull.CurrentSystem, hull.ShipSymbol, err), map[string]interface{}{
			"ship_symbol": hull.ShipSymbol, "origin_system": hull.CurrentSystem, "reason": "regions_unreadable",
		})
		return nil
	}
	var scored []relocationCandidate
	for _, region := range regions {
		if reason, usable := h.regionUsable(region, hull); !usable {
			result.skip(reason)
			continue
		}
		valuation := trading.ValueRelocation(h.inputsFor(cmd, currentRate, region, remainingEra, eraKnown))
		if !valuation.Licensed() {
			result.skip(string(valuation.Verdict))
			continue
		}
		// Staleness does not veto (see regionUsable), so a LICENSED candidate
		// may be resting on cold ground. Say so at the moment it is admitted, not afterwards — this is
		// the only place that knows both the age and that the economics said yes. A licensed-on-stale
		// candidate is the deliberate trade the Admiral authorised; an unexplained one later is not.
		if h.regionSnapshotStale(region) {
			logger.Log("INFO", fmt.Sprintf(
				"Opportunity relocator: licensing %s -> %s on a STALE snapshot (%s old, cap %s for activity %q) - accepted deliberately, the alternative was leaving the hull in a shared system",
				hull.ShipSymbol, region.AnchorSystem, region.SnapshotAge.Round(time.Minute),
				h.ageCaps.For(region.Activity).Round(time.Minute), region.Activity),
				map[string]interface{}{
					"ship_symbol": hull.ShipSymbol, "origin_system": hull.CurrentSystem,
					"target_system": region.AnchorSystem, "snapshot_age_seconds": int(region.SnapshotAge.Seconds()),
					"activity_cap_seconds": int(h.ageCaps.For(region.Activity).Seconds()),
					"activity":             region.Activity, "trigger": "relocator_stale_region_licensed",
				})
		}
		scored = append(scored, relocationCandidate{hull: hull, region: region, valuation: valuation})
	}
	return scored
}

// regionUsable applies the region-side exclusions: FAIL CLOSED on an unreadable projection.
//
// STALENESS NO LONGER EXCLUDES A REGION (Admiral). The previous rule refused any region
// whose snapshot was older than its activity's freshness cap, and it was the single largest blocker
// of actual spreading: over the measured window the relocator was offered hulls at a healthy rate,
// exempted them from the mid-tour rule correctly, and then refused 27 of ~48 eligible pairings as
// region_snapshot_stale — against 1 relocation. Meanwhile the fleet held market data for 516 systems
// and traded in 39.
//
// The rule was locally correct and globally wrong. Its own reasoning still stands as written: an
// 8-hour-old quote on a WEAK market is rankable, a 40-minute-old one on a STRONG market is not, and
// scoring a stale region at last-seen prices can send a hull to ground that no longer exists. What
// it missed is the alternative. Refusing the move does not hold the hull somewhere known-good; it
// holds it in a system it already shares with ~26 other trade hulls, grinding a contended sink. A
// stale estimate of somewhere else beats a fresh estimate of the crush.
//
// WHAT STILL PROTECTS THE MOVE, because this removes ONE gate and not the economics:
//   - region_rate_unreadable stays. An unreadable projection is still fail-closed: we will act on an
//     OLD number, never on NO number.
//   - ValueRelocation still has to LICENSE the move (no_uplift / below_npv_threshold at :767). The
//     destination must still beat the hull's current rate by the NPV threshold, on whatever data
//     exists.
//   - the anti-herd cap in commitTopCandidates still stops a stampede into one region.
//   - the concurrency cap, the per-hull cooldown, and the actuation-time ownership re-check are all
//     downstream of here and untouched.
//
// So the accepted risk is bounded and RECOVERABLE: a hull can be sent somewhere that turns out no
// better, and is then relocated again from there. That is strictly preferable to the status quo, in
// which it is never sent anywhere at all.
//
// SnapshotAge is deliberately still carried on the region and is reported by the caller, so the cost
// of this decision stays measurable — see the stale-accepted counting at the call site. If
// stale-sourced relocations systematically underperform fresh ones, that shows up as data rather
// than as an argument.
//
// THE ANTI-HERD CAP IS DELIBERATELY NOT CHECKED HERE. It lives at ONE site, in commitTopCandidates,
// because that is where the per-system count actually MUTATES: committing one hull to a region is
// what fills it for the next. A copy of the check here would be indistinguishable from the commit
// check in every outcome — a mutation probe removing it killed no test — which makes it dead surface
// that can silently drift out of step with the real enforcement point.
func (h *RunOpportunityRelocatorHandler) regionUsable(region RelocatorRegion, hull RelocatorHull) (string, bool) {
	if region.AnchorSystem == hull.CurrentSystem {
		return "region_is_current_system", false
	}
	if !region.RateReadable {
		return "region_rate_unreadable", false
	}
	return "", true
}

// regionSnapshotStale reports whether a region's snapshot is older than its activity's freshness cap.
//
// This is the predicate that used to VETO the region. It is retained, and still consulted, purely as
// an OBSERVATION: the relocator now accepts stale ground deliberately, and the fleet needs to be able
// to tell afterwards how much of its spreading was bought on cold data. Deleting it would have made
// that unanswerable, which is the same mistake as shipping the offer path with no refusal counter.
func (h *RunOpportunityRelocatorHandler) regionSnapshotStale(region RelocatorRegion) bool {
	return region.SnapshotAge > h.ageCaps.For(region.Activity)
}

// inputsFor assembles the pure valuation's inputs. travel_h comes from the swappable TravelHopModel;
// the risk margin is one configured tour of THIS hull's own earnings.
func (h *RunOpportunityRelocatorHandler) inputsFor(
	cmd *RunOpportunityRelocatorCommand,
	currentRate float64,
	region RelocatorRegion,
	remainingEra float64,
	eraKnown bool,
) trading.RelocationInputs {
	riskMarginHours := cmd.riskMarginTourHours()
	return trading.RelocationInputs{
		CurrentRate:       currentRate,
		ProjectedRate:     region.ProjectedRate,
		TravelHours:       h.travel.CrossingHours(region.GateHops),
		RemainingEraHours: remainingEra,
		EraHorizonKnown:   eraKnown,
		HorizonHours:      float64(cmd.horizonHours()),
		RiskMargin:        currentRate * riskMarginHours,
		UpliftBar:         float64(cmd.upliftBarPct()) / 100,
		NPVThreshold:      float64(cmd.npvThreshold()),
	}
}

// countHullsBySystem tallies landed trade hulls per system — the anti-herd baseline, taken from the
// SAME observation the candidates come from so there is no second, separately-failing fleet read.
func countHullsBySystem(hulls []RelocatorHull) map[string]int {
	counts := make(map[string]int, len(hulls))
	for _, hull := range hulls {
		counts[hull.CurrentSystem]++
	}
	return counts
}

// hullsBySymbol indexes the observation by ship symbol — the live state the restart contract compares
// an intent against. The whole hull, because it turns on where the hull IS and whether it still flies.
func hullsBySymbol(hulls []RelocatorHull) map[string]RelocatorHull {
	bySymbol := make(map[string]RelocatorHull, len(hulls))
	for _, hull := range hulls {
		bySymbol[hull.ShipSymbol] = hull
	}
	return bySymbol
}

// ── knob resolution (RULINGS #5: the 0/absent -> documented default idiom) ───────────────────────
