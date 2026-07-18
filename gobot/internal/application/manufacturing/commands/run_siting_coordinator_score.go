package commands

import (
	"context"
	"math"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// score computes each candidate's ranking score (SCORE step, sp-vdld M3):
//
//	alignmentFactor  = 1 + WeightTourAlignment × tourSignal        (>= 1; tour-pull boost)
//	overlapFraction  = share of the candidate's feed markets contended by other candidates ∈ [0,1]
//	ageFraction      = min(1, DataAge / FreshnessMax) ∈ [0,1]
//	unreachFraction  = 1 − reachabilitySignal(system) ∈ [0,1]    (1 = no worker can reach it)
//	Score            = ProjectedPL × alignmentFactor
//	                   − WeightInputCompetition   × ProjectedPL × overlapFraction
//	                   − WeightStaleness          × ProjectedPL × ageFraction
//	                   − WeightWorkerReachability × ProjectedPL × unreachFraction
//
// The additive penalties are scaled by ProjectedPL so they rank on the same credit scale as
// the projection (a weight ~1 then means "a fully-contended / fully-stale / fully-unstaffable
// site loses ~its whole projected value"). The worker-reachability penalty (sp-3vg8) is the
// manning-feasibility check that complements the launch guard's margin check: a far-cluster
// chain that clears margin but has no in-system idle worker AND no ferry path in (C81/GS93) is
// deprioritized instead of launched workerless. The Analyst owns every weight (RULINGS #5); this is only the
// structure. A candidate the launch guard vetoes (Proceed=false) — or one that cannot be
// priced — is dropped at ZERO cost (the sp-2dv4 veto: never launched, never retried this
// tick). Alignment read errors do NOT drop a candidate (alignment is an enhancement, not a
// gate) — the signal falls back to 0 (neutral).
func (h *RunSitingCoordinatorHandler) score(ctx context.Context, cmd *RunSitingCoordinatorCommand, cfg sitingRunConfig, candidates []SitingCandidate) []ScoredCandidate {
	type staged struct {
		cand   SitingCandidate
		pl     int
		signal float64
	}

	// Phase 1: project each candidate through the launch guard + read its tour-pull signal.
	// Vetoed / unpriceable candidates are excluded here (zero cost).
	stagedList := make([]staged, 0, len(candidates))
	for _, c := range candidates {
		proj, err := h.projector.Project(ctx, c.Good, c.System, cmd.PlayerID)
		if err != nil || !proj.Proceed {
			continue
		}
		stagedList = append(stagedList, staged{cand: c, pl: proj.ProjectedPL, signal: h.tourSignal(ctx, cmd.PlayerID, c)})
	}

	// Phase 2: input-market contention across the surviving set (chains sharing a feed source
	// starve each other), computed once over all staged candidates.
	contention := make(map[string]int)
	for _, s := range stagedList {
		for _, m := range dedupeStrings(s.cand.InputMarkets) {
			contention[m]++
		}
	}

	// Phase 3: assemble scores.
	scored := make([]ScoredCandidate, 0, len(stagedList))
	for _, s := range stagedList {
		alignmentFactor := 1.0 + cfg.WeightAlignment*s.signal
		overlap := overlapFraction(s.cand.InputMarkets, contention)
		ageFrac := ageFraction(s.cand.DataAgeSecs, cfg.FreshnessMax.Seconds())
		unreachFrac := 1.0 - h.reachabilitySignal(ctx, cmd.PlayerID, s.cand.System)
		competition := cfg.WeightCompetition * float64(s.pl) * overlap
		staleness := cfg.WeightStaleness * float64(s.pl) * ageFrac
		unreachability := cfg.WeightReachability * float64(s.pl) * unreachFrac
		scored = append(scored, ScoredCandidate{
			SitingCandidate: s.cand,
			ProjectedPL:     s.pl,
			TourAlignment:   alignmentFactor,
			Competition:     competition,
			Staleness:       staleness,
			Unreachability:  unreachability,
			Score:           float64(s.pl)*alignmentFactor - competition - staleness - unreachability,
			Proceed:         true,
		})
	}

	// Rank: highest score first, Key ascending as a deterministic tie-break.
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		return scored[i].Key() < scored[j].Key()
	})
	return scored
}

// tourSignal reads the candidate's tour-pull signal, treating a nil provider or a read error
// as 0 (neutral) — alignment boosts a site but never gates it.
func (h *RunSitingCoordinatorHandler) tourSignal(ctx context.Context, playerID int, c SitingCandidate) float64 {
	if h.alignment == nil {
		return 0
	}
	signal, err := h.alignment.Alignment(ctx, playerID, c.Good, c.System)
	if err != nil || signal < 0 {
		return 0
	}
	return signal
}

// reachabilitySignal reads the candidate system's worker-staffing reachability signal, clamped
// to [0,1] (1 = fully staffable, 0 = no worker can reach it; sp-3vg8). A nil provider, a read
// error, or an out-of-range value falls back to 1.0 (fully reachable / neutral) so the term
// never drops or wrongly penalizes a candidate — reachability deprioritizes an unmannable site
// but a transient gate-graph read must not nuke the portfolio (the alignment fail-open idiom).
func (h *RunSitingCoordinatorHandler) reachabilitySignal(ctx context.Context, playerID int, system string) float64 {
	if h.reachability == nil {
		return 1
	}
	signal, err := h.reachability.Reachability(ctx, playerID, system)
	if err != nil {
		return 1
	}
	if signal < 0 {
		return 0
	}
	if signal > 1 {
		return 1
	}
	return signal
}

// overlapFraction is the share of a candidate's DISTINCT feed markets that at least one other
// candidate also draws from (contention >= 2). 0 = all feeds private; 1 = every feed contended.
func overlapFraction(inputMarkets []string, contention map[string]int) float64 {
	distinct := dedupeStrings(inputMarkets)
	if len(distinct) == 0 {
		return 0
	}
	shared := 0
	for _, m := range distinct {
		if contention[m] >= 2 {
			shared++
		}
	}
	return float64(shared) / float64(len(distinct))
}

// ageFraction maps data age to a [0,1] staleness fraction relative to the freshness ceiling.
func ageFraction(ageSecs, freshnessMaxSecs float64) float64 {
	if freshnessMaxSecs <= 0 {
		return 0
	}
	f := ageSecs / freshnessMaxSecs
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// dedupeStrings returns the distinct values of s, preserving first-seen order.
func dedupeStrings(s []string) []string {
	if len(s) < 2 {
		return s
	}
	seen := make(map[string]struct{}, len(s))
	out := make([]string, 0, len(s))
	for _, v := range s {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// resolveK sizes the target portfolio (MAINTAIN step, sp-vdld M4). Precedence:
//   - TopK config override (> 0) wins directly.
//   - else floor(workers / WorkersPerChain) from the WorkerCounter (C3 rotation math). K may
//     legitimately be 0 for a tiny fleet — capacity-without-workers is the era-1 lesson.
//
// It returns ok=false only when K cannot be determined at all (no override AND no readable
// worker count); the caller then leaves the portfolio untouched this tick rather than churning
// it on a transient read failure.
func (h *RunSitingCoordinatorHandler) resolveK(ctx context.Context, cmd *RunSitingCoordinatorCommand, cfg sitingRunConfig) (int, bool) {
	if cfg.TopK > 0 {
		return cfg.TopK, true
	}
	if h.workers == nil {
		return 0, false
	}
	n, err := h.workers.CountWorkers(ctx, cmd.PlayerID)
	if err != nil {
		return 0, false
	}
	if n <= 0 {
		return 0, true // no manufacturing hulls → no chains (a valid answer)
	}
	perChain := cfg.WorkersPerChain
	if perChain <= 0 {
		perChain = defaultSitingWorkersPerChain
	}
	return int(math.Floor(float64(n) / perChain)), true
}

// maintain selects the target portfolio: the highest-scored candidates, up to K, subject to
// the per-system and per-input-market concentration caps. Candidates are consumed in score
// order; one that would breach a cap is SKIPPED (it does not consume a K slot — a lower-scored
// candidate that fits takes it), so the caps shape the mix without shrinking the portfolio.
func (h *RunSitingCoordinatorHandler) maintain(cfg sitingRunConfig, scored []ScoredCandidate, k int) []ScoredCandidate {
	if k <= 0 {
		return nil
	}
	perSystem := make(map[string]int)
	perInputMarket := make(map[string]int)
	desired := make([]ScoredCandidate, 0, k)

	for _, c := range scored {
		if len(desired) >= k {
			break
		}
		if perSystem[c.System] >= cfg.MaxChainsPerSystem {
			continue
		}
		if breachesInputCap(c.InputMarkets, perInputMarket, cfg.MaxChainsPerInputMarket) {
			continue
		}
		desired = append(desired, c)
		perSystem[c.System]++
		for _, m := range dedupeStrings(c.InputMarkets) {
			perInputMarket[m]++
		}
	}
	return desired
}

// breachesInputCap reports whether adding a chain drawing these feed markets would push any
// one market's chain count past the per-input-market concentration cap.
func breachesInputCap(inputMarkets []string, perInputMarket map[string]int, maxChains int) bool {
	for _, m := range dedupeStrings(inputMarkets) {
		if perInputMarket[m] >= maxChains {
			return true
		}
	}
	return false
}

// warnUnsized emits the "cannot size portfolio" WARN once per episode (edge-triggered), so a
// persistent worker-count failure does not spam the log every tick.
func (h *RunSitingCoordinatorHandler) warnUnsized(ctx context.Context, cmd *RunSitingCoordinatorCommand) {
	st := h.coordinatorState(cmd.ContainerID)
	h.mu.Lock()
	already := st.unsizedWarned
	st.unsizedWarned = true
	h.mu.Unlock()
	if already {
		return
	}
	common.LoggerFromContext(ctx).Log("WARNING", "siting cannot size the portfolio: no top_k override and worker count unavailable — leaving the running chains untouched this tick (sp-vdld)", map[string]interface{}{
		"action":       "siting_unsized",
		"container_id": cmd.ContainerID,
		"bead":         "sp-vdld",
	})
}

// noteSized clears the unsized-warn latch when sizing recovers, re-arming the one-shot WARN.
func (h *RunSitingCoordinatorHandler) noteSized(containerID string) {
	st := h.coordinatorState(containerID)
	h.mu.Lock()
	st.unsizedWarned = false
	h.mu.Unlock()
}
