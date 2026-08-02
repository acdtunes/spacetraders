package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// releaseReasonCrossSystemReuseRelay stamps a hull freed from a manning tour to be relayed
// cross-system to a starved post, so the release ledger distinguishes a surplus donation
// from a dead-tour reclaim or a sweep-once retirement.
const releaseReasonCrossSystemReuseRelay = "scout_cross_system_reuse_relay"

// surplusProbeCandidate is one over-covered source post's donatable manning probe: the source
// system, the ship + its tour + the slot it mans, that system's manning supply (mannedCount) and
// freshsizer demand, and the gate-hops from the source to the target post. mannedCount + demand ride
// on the candidate so pickSurplusProbe's over-covered filter (the "never strip below demand" guard)
// is PURE over its inputs — unit-testable with no repo.
type surplusProbeCandidate struct {
	sourceSystem string
	shipSymbol   string
	tourID       string
	slotIndex    int
	mannedCount  int
	demand       int
	hops         int
}

// maybeRelaySurplusProbe is the CROSS-SYSTEM reuse relay: when a declared post has no
// in-system satellite AND the idle pool is spent, it borrows ONE surplus probe from an
// OVER-COVERED source system (manning supply > freshsizer demand) and relays it
// cross-system onto the post — reusing the SAME idle-reposition dispatch primitives
// (discoverMarkets -> pickRepositionDestination -> spawnReposition) and the
// per-slot backoff. It returns true when it OWNS the slot this tick (a relay dispatched, or an active
// backoff), false to fall through to the honest park. FAIL-SAFE by construction: disabled (flag off
// or no demand reader), no over-covered surplus within reach, an unreadable demand, or a dispatch
// error all park honest, never strip a system below its need, and never move a probe blind.
func (h *RunScoutPostCoordinatorHandler) maybeRelaySurplusProbe(
	ctx context.Context,
	cmd *RunScoutPostCoordinatorCommand,
	post *domainScouting.ScoutPost,
	slot domainScouting.ScoutSlotRef,
	key string,
	sourcePosts []*domainScouting.ScoutPost,
	relayCfg scoutRelayConfig,
) bool {
	// Disabled (the default) or no demand reader wired -> return false BEFORE any side
	// effect, so the caller parks honest. This is the whole default-OFF gate.
	if !relayCfg.enabled || h.probeDemandReader == nil {
		return false
	}
	logger := common.LoggerFromContext(ctx)

	// Share the idle-reposition per-slot dispatch backoff: a recent relay for this slot
	// (idle OR surplus) backs off both paths, so a torn-down source probe is never
	// re-torn-down every tick. Announce the skip once per cooldown episode. Backed off
	// => we OWN the slot (do NOT park).
	if h.repositionBackedOff(key) {
		if h.noteRepositionBackoffLogged(key) {
			logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: cross-system reuse relay backing off after a recent relay — retrying shortly", post.SystemSymbol), map[string]interface{}{
				"action":        "scout_cross_system_relay_backoff",
				"system_symbol": post.SystemSymbol,
			})
		}
		return true
	}

	// Resolve a destination waypoint in the TARGET system (any market; the relay just lands the
	// hull there and the next in-system tick's tour starts from wherever it sits) — identical to
	// the idle-reposition path, including the virgin discovery fallback.
	markets, err := h.discoverMarkets(ctx, post.SystemSymbol)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to discover markets for cross-system relay target %s: %v", post.SystemSymbol, err), nil)
		return false
	}
	if len(markets) == 0 {
		markets = h.discoverVirginMarkets(ctx, cmd, post, key)
		if len(markets) == 0 {
			return true // parked honest by discoverVirginMarkets (owns the slot this tick)
		}
	}
	destWaypoint := pickRepositionDestination(markets)

	candidate, ok := pickSurplusProbe(h.gatherSurplusCandidates(ctx, cmd, sourcePosts, post.SystemSymbol, relayCfg.maxHops), relayCfg.maxHops)
	if !ok {
		logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: no surplus probe in an over-covered system within %d hops — parked (fail-closed)", post.SystemSymbol, relayCfg.maxHops), map[string]interface{}{
			"action":        "scout_cross_system_relay_no_surplus",
			"system_symbol": post.SystemSymbol,
			"max_hops":      relayCfg.maxHops,
		})
		return false // fall through to the honest park
	}

	// Tear down the chosen probe's source tour so its hull is reclaimable (the shared
	// teardown primitive, per slot), then relay it cross-system onto the target. The
	// source is over-covered, so losing one probe still leaves it at (or above) its
	// freshsizer demand.
	h.tearDownSurplusSource(ctx, cmd, sourcePosts, candidate)

	relayID, err := h.spawnReposition(ctx, cmd, candidate.shipSymbol, destWaypoint, relayCfg.maxHops, false)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Failed to dispatch cross-system reuse relay of %s from %s to post %s: %v", candidate.shipSymbol, candidate.sourceSystem, post.SystemSymbol, err), nil)
		return false
	}

	// Arm the backoff BEFORE persisting the relay reference: if the Upsert below fails, the backoff
	// still prevents an immediate second teardown+relay to this slot next tick.
	h.noteRepositionDispatch(key)
	slot.SetRepositionContainerID(relayID)
	if err := h.postRepo.Upsert(ctx, post); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Dispatched cross-system relay for post %s but failed to persist relay reference: %v", post.SystemSymbol, err), nil)
	}

	logger.Log("INFO", fmt.Sprintf("Scout post %s unmanned: cross-system reuse relay repositioning %s from over-covered %s (%d probe(s) manning > %d demand, %d jump(s), ≤%d bound, relay %s) → %s", post.SystemSymbol, candidate.shipSymbol, candidate.sourceSystem, candidate.mannedCount, candidate.demand, candidate.hops, relayCfg.maxHops, relayID, destWaypoint), map[string]interface{}{
		"action":        "scout_cross_system_reuse_relay",
		"system_symbol": post.SystemSymbol,
		"ship_symbol":   candidate.shipSymbol,
		"source_system": candidate.sourceSystem,
		"manned_count":  candidate.mannedCount,
		"demand":        candidate.demand,
		"jumps":         candidate.hops,
		"max_hops":      relayCfg.maxHops,
		"destination":   destWaypoint,
		"relay":         relayID,
	})
	return true
}

// gatherSurplusCandidates resolves every OVER-COVERED source post's donatable probe: for
// each loaded post that is NOT the target, it reads the source system's freshsizer demand (cached
// per system so a re-read is free), picks a donatable manning slot, and measures the gate-hops to
// the target. A post at/under its demand, with unreadable/zero demand (cannot assess), with no
// manning slot to give, or out of reach is dropped — never raid a system blind or below its need.
func (h *RunScoutPostCoordinatorHandler) gatherSurplusCandidates(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, sourcePosts []*domainScouting.ScoutPost, targetSystem string, maxHops int) []surplusProbeCandidate {
	demandCache := make(map[string]int)
	out := make([]surplusProbeCandidate, 0, len(sourcePosts))
	for _, src := range sourcePosts {
		if src.SystemSymbol == targetSystem {
			continue // never borrow from the post we are trying to man
		}
		mannedCount := src.MannedCount()
		if mannedCount == 0 {
			continue // nothing manning here to donate
		}
		demand, ok := h.probeDemandCached(ctx, cmd, src.SystemSymbol, demandCache)
		if !ok || demand <= 0 {
			continue // demand unreadable / cannot assess ⇒ never raid blind
		}
		slotIndex, shipSymbol, tourID, has := firstDonatableSlot(src)
		if !has {
			continue
		}
		hops := h.hopsBetween(ctx, src.SystemSymbol, targetSystem, maxHops)
		if hops < 1 {
			continue // unreachable within the relay reach
		}
		out = append(out, surplusProbeCandidate{
			sourceSystem: src.SystemSymbol,
			shipSymbol:   shipSymbol,
			tourID:       tourID,
			slotIndex:    slotIndex,
			mannedCount:  mannedCount,
			demand:       demand,
			hops:         hops,
		})
	}
	return out
}

// pickSurplusProbe selects the probe to relay: the FEWEST-hop candidate that is within maxHops AND
// sits in an OVER-COVERED system (mannedCount strictly greater than demand — the "never strip a
// system below its freshsizer need" guard; a system at exactly its demand is left alone). Ties break
// on the lowest source system, then the lowest ship symbol, for determinism. Pure over its inputs
// (the demand + hops are pre-resolved onto each candidate), so the over-covered and reach guards are
// unit-testable with no store, census, or repo.
func pickSurplusProbe(candidates []surplusProbeCandidate, maxHops int) (surplusProbeCandidate, bool) {
	best := surplusProbeCandidate{}
	found := false
	for _, candidate := range candidates {
		if candidate.hops < 1 || candidate.hops > maxHops {
			continue // out of reach
		}
		if candidate.mannedCount <= candidate.demand {
			continue // NOT over-covered — taking one would strip the system to/below its demand
		}
		if !found ||
			candidate.hops < best.hops ||
			(candidate.hops == best.hops && candidate.sourceSystem < best.sourceSystem) ||
			(candidate.hops == best.hops && candidate.sourceSystem == best.sourceSystem && candidate.shipSymbol < best.shipSymbol) {
			best, found = candidate, true
		}
	}
	return best, found
}

// tearDownSurplusSource stops the donated probe's source tour and reclaims its hull (the
// shared teardown primitive, applied to ONE slot), then clears that slot and persists the source post so
// the donation is durable and the source post's next tick sees the slot honestly unmanned. Reuses
// reclaimHullFromContainer (the shared reclaim path) so a hull the stop races is still freed on a
// later tick. Best-effort: a persist failure is logged, not fatal (pass 1 reconciles it).
func (h *RunScoutPostCoordinatorHandler) tearDownSurplusSource(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, sourcePosts []*domainScouting.ScoutPost, candidate surplusProbeCandidate) {
	logger := common.LoggerFromContext(ctx)
	src := findPostBySystem(sourcePosts, candidate.sourceSystem)
	if src == nil {
		return // defensive: the source post vanished between selection and teardown
	}
	for _, slot := range src.Slots() {
		if slot.Index() != candidate.slotIndex {
			continue
		}
		if tourID := slot.TourContainerID(); tourID != "" {
			_ = h.daemonClient.StopContainer(ctx, tourID)
			h.reclaimHullFromContainer(ctx, cmd, tourID, releaseReasonCrossSystemReuseRelay)
		} else {
			h.releaseHull(ctx, cmd, slot.AssignedHull(), releaseReasonCrossSystemReuseRelay)
		}
		slot.SetAssignedHull("")
		slot.SetTourContainerID("")
		break
	}
	if err := h.postRepo.Upsert(ctx, src); err != nil {
		logger.Log("WARNING", fmt.Sprintf("Cross-system relay freed a probe from %s but failed to persist the donation: %v", candidate.sourceSystem, err), nil)
	}
}

// hopsBetween measures the gate-hop distance from fromSystem to toSystem over the stored adjacency
// bounded to maxJumps (the expendable-probe resolver), returning -1 when unroutable within the
// bound. Mirrors selectNearestSatelliteByHops' reach math.
func (h *RunScoutPostCoordinatorHandler) hopsBetween(ctx context.Context, fromSystem, toSystem string, maxJumps int) int {
	path, err := h.gateGraph.RepositionPath(ctx, fromSystem, toSystem, maxJumps)
	if err != nil || len(path) == 0 {
		return -1
	}
	return len(path) - 1
}

// probeDemandCached reads a system's freshsizer demand once per gather pass (cache keyed by system),
// so a cluster of source posts costs one demand read each. An unreadable demand returns ok=false so
// the caller drops the candidate rather than raiding a system whose need it cannot judge.
func (h *RunScoutPostCoordinatorHandler) probeDemandCached(ctx context.Context, cmd *RunScoutPostCoordinatorCommand, systemSymbol string, cache map[string]int) (int, bool) {
	if v, seen := cache[systemSymbol]; seen {
		return v, v >= 0
	}
	demand, err := h.probeDemandReader.ProbeDemand(ctx, cmd.PlayerID.Value(), systemSymbol)
	if err != nil {
		cache[systemSymbol] = -1 // memoize the failure so a re-read is free within the pass
		return 0, false
	}
	cache[systemSymbol] = demand
	return demand, true
}

// firstDonatableSlot picks the manning slot a source post will donate: the HIGHEST-index
// manned slot (an extra slot before the primary), so a multi-hull post keeps its primary slot +
// partition intact and gives up an extra. Returns the slot index, its hull, its tour container, and
// whether one was found. A post with no manned slot yields has=false.
func firstDonatableSlot(post *domainScouting.ScoutPost) (slotIndex int, shipSymbol, tourID string, has bool) {
	for _, slot := range post.Slots() {
		if hull := slot.AssignedHull(); hull != "" {
			slotIndex, shipSymbol, tourID, has = slot.Index(), hull, slot.TourContainerID(), true
		}
	}
	return slotIndex, shipSymbol, tourID, has
}

// findPostBySystem returns the loaded post for a system, or nil.
func findPostBySystem(posts []*domainScouting.ScoutPost, systemSymbol string) *domainScouting.ScoutPost {
	for _, p := range posts {
		if p.SystemSymbol == systemSymbol {
			return p
		}
	}
	return nil
}

// CensusProbeDemandReader implements SystemProbeDemandReader as the freshsizer's per-system demand
// derived from the SAME SystemsFreshness census the manning-stall watchdog reads. Demand is
// FreshnessRequiredHulls(marketCount, cycle, sla, oldestAge): the freshsizer's own closed-loop model,
// so a system BREACHING its SLA reads a RAISED demand (and is never raided by the relay), while a
// comfortably-fresh over-provisioned core system reads a low demand (and can donate its surplus). A
// system ABSENT from the census reads demand 0 — "cannot assess" — which the coordinator treats as
// "do not raid", so a missing/stale census never strips a probe blind. cycle and sla are config
// (RULINGS #5); the daemon seeds them from the freshness sizer's defaults so the two agree.
type CensusProbeDemandReader struct {
	census domainScouting.SystemFreshnessReader
	cycle  time.Duration
	sla    time.Duration
}

// NewCensusProbeDemandReader wires the census-backed freshsizer-demand source. cycle is the seeded
// per-market scan cadence and sla the freshness target the demand is sized against; a non-positive
// value falls back to the freshness sizer's documented defaults so the reader is never degenerate.
func NewCensusProbeDemandReader(census domainScouting.SystemFreshnessReader, cycle, sla time.Duration) *CensusProbeDemandReader {
	if cycle <= 0 {
		cycle = defaultSeedCycleSeconds * time.Second
	}
	if sla <= 0 {
		sla = defaultSLASeconds * time.Second
	}
	return &CensusProbeDemandReader{census: census, cycle: cycle, sla: sla}
}

var _ SystemProbeDemandReader = (*CensusProbeDemandReader)(nil)

// ProbeDemand returns systemSymbol's freshsizer demand from the current census. A system with no
// census row (or no markets) reads 0 — the caller's "cannot assess ⇒ do not raid" signal.
func (r *CensusProbeDemandReader) ProbeDemand(ctx context.Context, playerID int, systemSymbol string) (int, error) {
	snapshots, err := r.census.SystemsFreshness(ctx, playerID)
	if err != nil {
		return 0, fmt.Errorf("freshness census unreadable for probe demand: %w", err)
	}
	for _, snap := range snapshots {
		if snap.SystemSymbol != systemSymbol {
			continue
		}
		age := time.Duration(snap.OldestAgeSeconds * float64(time.Second))
		return domainScouting.FreshnessRequiredHulls(snap.MarketCount, r.cycle, r.sla, age), nil
	}
	return 0, nil // no census row ⇒ cannot assess (do not raid)
}
