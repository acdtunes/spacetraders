package commands

// run_trade_route_coordinator_log.go — structured logging and reporting helpers for lane selection and stranded/blocked-cargo exits (sp-wads move-only split).

import (
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// cargoAboardExitLog emits the one structured record every PRE-SELL circuit exit
// shares (sp-149h): once the hull has BOUGHT and is holding cargo it could not
// sell, an operator needs to see WHAT is stranded and WHERE — good/source/dest/
// held/reason — on the log line itself, not buried in a bare {"error": ...} the
// container-log renderer drops (the sp-ynuf/sp-iqyq class the source-dock and
// dest-dock legs already fixed by putting the cause in the message). AbortReason
// still carries the operator-facing prose on the response; this is its structured
// telemetry twin, keyed so a stranded-cargo exit is greppable by action rather
// than by parsing prose. reason names the specific cause (a failed leg's verbatim
// error, or the exhausted-volume detail).
func cargoAboardExitLog(logger common.ContainerLogger, level string, lane trading.ArbitrageLane, held int, reason string) {
	logger.Log(level, "Circuit ending with cargo aboard", map[string]interface{}{
		"action": "cargo_aboard_exit",
		"good":   lane.Good,
		"source": lane.SourceWaypoint,
		"dest":   lane.DestWaypoint,
		"held":   held,
		"reason": reason,
	})
}

// cargoBlockedLog emits the structured pre-flight/pre-buy cargo park (sp-xwa1): the
// hull has too little free hold to buy a tranche, so it parks rather than failing
// mid-buy or buying a useless sliver. The good/needed/free/action/reason fields go in
// the MESSAGE TEXT — `container logs` drops the metadata map (the sp-149h/sp-iqyq
// renderer defect), so an operator reading the CLI must see WHY the hull parked on the
// line itself, not in a discarded map. reason distinguishes this HULL-side stop
// ("no free hold") from a market-side one ("source volume exhausted"), which the two
// causes used to share behind one indistinguishable "volume or hold exhausted" line.
func cargoBlockedLog(logger common.ContainerLogger, good string, needed, free int, reason string) {
	logger.Log("WARNING", fmt.Sprintf(
		"Pre-flight cargo check parked hull: good=%s needed>=%d free=%d action=empty-residual-cargo-before-trading reason=%s",
		good, needed, free, reason),
		map[string]interface{}{
			"action": "cargo_blocked",
			"good":   good,
			"needed": needed,
			"free":   free,
			"reason": reason,
		})
}

// bestSpreadPerUnit returns the highest per-unit spread among ranked lanes, used to
// report how far the best standing lane fell short of the discipline floor when none
// cleared it — so a no-trade run always reports WHY, never a silent zero. Lanes are
// ranked by CAPPED spread, so the deepest per-unit spread is not necessarily lanes[0].
func bestSpreadPerUnit(lanes []trading.ArbitrageLane) int {
	best := 0
	for _, l := range lanes {
		if l.SpreadPerUnit > best {
			best = l.SpreadPerUnit
		}
	}
	return best
}

// derefString flattens an optional supply/activity pointer to its value or "".
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// laneCandidateLogLimit bounds how many top-ranked candidates are attached to the
// lane-selection log line (sp-q1ca): enough to show cross-system candidates were
// actually scanned and ranked even when a penalized home lane wins the selection,
// without flooding the log with the full ranked set on a system with many goods.
const laneCandidateLogLimit = 5

// laneLogCandidates summarizes up to laneCandidateLogLimit top-ranked lanes (in
// their already-rate-ranked order) into loggable payloads, so the lane-selection
// log line makes cross-system scanning VERIFIABLE rather than inferred (sp-q1ca):
// an operator can see the full ranked shortlist — including any cross-system
// candidates that lost to a penalized home lane — not just the one that won.
func laneLogCandidates(lanes []trading.ArbitrageLane) []map[string]interface{} {
	limit := laneCandidateLogLimit
	if len(lanes) < limit {
		limit = len(lanes)
	}
	candidates := make([]map[string]interface{}, 0, limit)
	for _, l := range lanes[:limit] {
		candidates = append(candidates, laneLogPayload(l))
	}
	return candidates
}

// laneLogPayload flattens one lane into the structured fields the captain needs to
// verify lane selection without inferring it from nav destinations (sp-q1ca): the
// good, both endpoints (waypoint + system), the per-unit margin, and whether the
// lane crosses a system boundary (source system != destination system).
func laneLogPayload(l trading.ArbitrageLane) map[string]interface{} {
	sourceSystem := shared.ExtractSystemSymbol(l.SourceWaypoint)
	destSystem := shared.ExtractSystemSymbol(l.DestWaypoint)
	return map[string]interface{}{
		"good":          l.Good,
		"source":        l.SourceWaypoint,
		"source_system": sourceSystem,
		"dest":          l.DestWaypoint,
		"dest_system":   destSystem,
		"cross_system":  sourceSystem != destSystem,
		"spread_per_u":  l.SpreadPerUnit,
		"volume_cap":    l.VolumeCap,
		"capped_spread": l.CappedSpread,
	}
}

// laneSelectionOneLiner renders one lane into a compact
// "GOOD SRC(SRCSYS)->DST(DSTSYS) m=SPREAD <same|cross> rate=R/hr[(x-waived)]" token
// for the selection log message text (sp-149h). m is the per-unit margin
// (SpreadPerUnit); the same/cross tag makes a gate-crossing lane greppable without
// parsing the two system codes.
//
// rate=R/hr is the EXACT score the ranker gave the lane (sp-1wp8:
// laneCircuitRatePerHour — hold-fit-weighted per-circuit value over estimated
// circuit hours, a cross lane paying the jump surcharge in its denominator). This
// is what lets the captain see WHY a lane won or lost autonomous selection
// ("m=1700 cross rate=624490/hr" reads as "raw 1700/u, worth 624k/hr after the
// gate time") instead of inferring the arithmetic. m stays the RAW spread and the
// same/cross tag is unchanged — the rate is appended, not substituted. A directed
// --dest lane ranked at the in-system baseline (laneMatchesTarget waiver) reads
// rate=R/hr(x-waived), matching what ranking actually applied.
func laneSelectionOneLiner(l trading.ArbitrageLane, shipCapacity int, targetDest string, model laneImpactModel) string {
	srcSys := shared.ExtractSystemSymbol(l.SourceWaypoint)
	dstSys := shared.ExtractSystemSymbol(l.DestWaypoint)
	scope := "same"
	if srcSys != dstSys {
		scope = "cross"
	}
	// rate is the EXACT score the ranker used, so it reflects the sp-tl68 effective-spread
	// compression + cooldown debt in production; m stays the raw snapshot spread. An inert
	// model (unit tests) leaves rate at the snapshot value.
	base := fmt.Sprintf("%s %s(%s)->%s(%s) m=%d %s rate=%.0f/hr",
		l.Good, l.SourceWaypoint, srcSys, l.DestWaypoint, dstSys, l.SpreadPerUnit, scope,
		laneCircuitRatePerHour(l, shipCapacity, targetDest, model))
	if srcSys != dstSys && laneMatchesTarget(l, targetDest) {
		return base + "(x-waived)"
	}
	return base
}

// laneSelectionCandidateLimit bounds how many ranked candidates the selection log
// MESSAGE lists (sp-149h) — the captain's ask was "chosen lane + top-3 candidates one-
// liner". Kept smaller than laneCandidateLogLimit (the fuller metadata shortlist) so
// the message line stays a scannable one-liner, not a wall of every ranked lane.
const laneSelectionCandidateLimit = 3

// laneSelectionMessage builds the lane-selection LOG MESSAGE with the chosen lane's
// identity and the top-N candidate shortlist embedded in the TEXT (sp-149h). The
// structured payload is still attached as metadata for structured consumers, but the
// CLI `container logs` view drops the metadata map — so the captain grepping that
// output must see good/source/dest/margin on the line itself, not in a discarded map
// (the same sp-iqyq renderer defect the dock-failure and cargo-aboard legs already
// route around by putting the cause in the message). The stable prefix "Selected top
// disciplined arbitrage lane" is preserved — existing greps/tests that match it keep
// working — with the payload appended after a colon.
func laneSelectionMessage(chosen trading.ArbitrageLane, ranked []trading.ArbitrageLane, shipCapacity int, targetDest string, model laneImpactModel) string {
	limit := laneSelectionCandidateLimit
	if len(ranked) < limit {
		limit = len(ranked)
	}
	tops := make([]string, 0, limit)
	for _, l := range ranked[:limit] {
		tops = append(tops, laneSelectionOneLiner(l, shipCapacity, targetDest, model))
	}
	return fmt.Sprintf("Selected top disciplined arbitrage lane: %s | top%d: %s",
		laneSelectionOneLiner(chosen, shipCapacity, targetDest, model), limit, strings.Join(tops, "; "))
}
