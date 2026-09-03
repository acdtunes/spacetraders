package commands

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

// MVT loop transition reasons that are not departure verdicts (those live in package mvt).
const (
	mvtReasonShadow           = "shadow"
	mvtReasonBootstrap        = "bootstrap"
	mvtReasonEmpty            = "empty"
	mvtReasonArrived          = "arrived"
	mvtReasonTravelFailed     = "travel_failed"
	mvtReasonRankerUnreadable = "ranker_unreadable"
	mvtReasonHold             = "hold"
	mvtReasonClaimWriteFailed = "claim_write_failed"
	// Refusals of a CLAIM that had somewhere better to go; each is a stay.
	mvtReasonRepositionDisabled = "reposition_disabled"
	mvtReasonBudgetDenied       = "budget_denied"
	mvtReasonEpisodeSpent       = "episode_spent"
	mvtReasonLaden              = "laden"
	mvtReasonJumpFeeGuard       = "jump_fee_guard"
	// The hull was moved by a path that does not own the claim (disposal, offload, retirement, a tag flip).
	mvtReasonRelocated = "relocated_externally"

	mvtFleetStatsWindow = 24 * time.Hour
	mvtTravelFailureCap = 3
)

type mvtPorts struct {
	claims      mvt.ClaimRegistry
	depth       mvt.SystemDepthReader
	transitions mvt.TransitionRecorder
}

// SetMVTPorts wires the claim registry, the ledger depth reader and the transition
// recorder. Without them the MVT branch stays inert and the shadow logger is silent.
func (h *RunTourCoordinatorHandler) SetMVTPorts(claims mvt.ClaimRegistry, depth mvt.SystemDepthReader, transitions mvt.TransitionRecorder) {
	h.mvt = mvtPorts{claims: claims, depth: depth, transitions: transitions}
}

type mvtLot struct{ units, price int }

// mvtHullState is the loop's in-memory view of one hull. Nothing here is persisted: the
// claim registry holds the durable part, and the yield resets on restart by design.
type mvtHullState struct {
	mu             sync.Mutex
	claimed        string
	yield          *mvt.YieldTracker
	basis          map[string][]mvtLot
	travelFailures int
	holdSells      int
	holdUntil      time.Time
	// leftAt is when this hull last DEPARTED each system, so the ranker can prefer ground it
	// has not just drained. A restart forgets it, which only widens the choice back to today's.
	leftAt map[string]time.Time
}

// holding is the post-failure hold, bounded in both sells and time so a dead system with
// no sells cannot hold forever. The caller holds st.mu.
func (st *mvtHullState) holding(now time.Time) bool {
	return st.holdSells > 0 && now.Before(st.holdUntil)
}

type mvtFleetCache struct {
	mu         sync.Mutex
	stats      mvt.FleetStats
	computedAt time.Time
}

func (h *RunTourCoordinatorHandler) mvtState(cmd *RunTourCoordinatorCommand) *mvtHullState {
	h.mvtMu.Lock()
	defer h.mvtMu.Unlock()
	if h.mvtHulls == nil {
		h.mvtHulls = map[string]*mvtHullState{}
	}
	st := h.mvtHulls[cmd.ShipSymbol]
	if st == nil {
		st = &mvtHullState{yield: mvt.NewYieldTracker(cmd.YieldWindowSells, cmd.YieldMinSells),
			basis: map[string][]mvtLot{}, leftAt: map[string]time.Time{}}
		floor := cmd.YieldRateSpanFloorMinutes
		if floor <= 0 {
			floor = DefaultYieldRateSpanFloorMinutes
		}
		st.yield.SetRateSpanFloor(time.Duration(floor) * time.Minute)
		h.mvtHulls[cmd.ShipSymbol] = st
	}
	return st
}

func (h *RunTourCoordinatorHandler) mvtCadence(cmd *RunTourCoordinatorCommand) time.Duration {
	if cmd.SpecialistCadenceMinutes <= 0 {
		return time.Duration(DefaultSpecialistCadenceMinutes) * time.Minute
	}
	return time.Duration(cmd.SpecialistCadenceMinutes) * time.Minute
}

// mvtFleetFor is the player's stats entry, created on first use; its own mu serialises the
// recompute so two hulls of one player never both read telemetry.
func (h *RunTourCoordinatorHandler) mvtFleetFor(playerID int) *mvtFleetCache {
	h.mvtMu.Lock()
	defer h.mvtMu.Unlock()
	if h.mvtFleet == nil {
		h.mvtFleet = map[int]*mvtFleetCache{}
	}
	fc := h.mvtFleet[playerID]
	if fc == nil {
		fc = &mvtFleetCache{}
		h.mvtFleet[playerID] = fc
	}
	return fc
}

// mvtFleetStats is the player's fleet-wide draw and rate, recomputed on the specialist cadence.
func (h *RunTourCoordinatorHandler) mvtFleetStats(ctx context.Context, cmd *RunTourCoordinatorCommand) mvt.FleetStats {
	now := h.clock.Now()
	fc := h.mvtFleetFor(cmd.PlayerID)
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if !fc.computedAt.IsZero() && now.Sub(fc.computedAt) < h.mvtCadence(cmd) {
		return fc.stats
	}
	if h.telemetry == nil {
		return fc.stats
	}
	legs, err := h.telemetry.ListByPlayer(ctx, cmd.PlayerID, now.Add(-mvtFleetStatsWindow))
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", "MVT fleet stats unreadable; keeping previous", map[string]interface{}{"error": err.Error()})
		return fc.stats
	}
	fc.stats = mvt.ComputeFleetStats(legs, mvtFleetStatsWindow)
	fc.computedAt = now
	return fc.stats
}

// mvtReach lists the hull's current system and every system within reach hops of it, read
// from the PERSISTED gate adjacency only (GateGraph.StoredSystemsWithinJumps). The shadow runs
// after every productive tour of every trade hull, so its discovery must cost zero API and write
// nothing: the reposition scan's fetch-through walk (repositionNeighborsWithinJumps → Connections)
// issues a live GetJumpGate and a gate_edges Replace for every neighbour whose edge set is missing
// or past its 24h window, and the fresher graph would change the old path's later reposition
// candidate sets — a behaviour change step 1 promises not to make. With no gate graph wired there
// is no stored adjacency to walk and the hull ranks its own system alone (never the live
// jump-gate scan); an unreadable store is an error and the caller stays put. Systems come back
// with current first and the rest in symbol order, so a given graph ranks the same way twice.
func (h *RunTourCoordinatorHandler) mvtReach(ctx context.Context, current string, reach int) ([]string, map[string]int, error) {
	systems := []string{current}
	hops := map[string]int{current: 0}
	if h.legs.gateGraph == nil {
		return systems, hops, nil
	}
	if reach <= 0 {
		reach = DefaultClaimReachHops
	}
	found, err := h.legs.gateGraph.StoredSystemsWithinJumps(ctx, current, reach)
	if err != nil {
		return nil, nil, fmt.Errorf("stored claim reach: %w", err)
	}
	for sys, n := range found {
		if sys == "" || sys == current {
			continue
		}
		systems = append(systems, sys)
		hops[sys] = n
	}
	sort.Strings(systems[1:])
	return systems, hops, nil
}

// mvtSpreadFloor is the credits-per-unit a market pair must clear to count as depth.
func mvtSpreadFloor(cmd *RunTourCoordinatorCommand) int {
	if cmd.RankerMinSpreadPerUnit > 0 {
		return cmd.RankerMinSpreadPerUnit
	}
	return DefaultRankerMinSpreadPerUnit
}

// mvtRank ranks the hull's current system and every priced system within ClaimReachHops.
func (h *RunTourCoordinatorHandler) mvtRank(ctx context.Context, cmd *RunTourCoordinatorCommand, ship *navigation.Ship) ([]mvt.ScoredSystem, error) {
	rk, err := h.mvtRankReach(ctx, cmd, ship, cmd.ClaimReachHops, mvtSpreadFloor(cmd))
	return rk.best(), err
}

// mvtRanking is one reach's scoring under the recently-left preference. systems sinks the
// ground this hull just drained below everything else; plain is the same scoring with the
// preference off, and stands whenever nothing undrained is reachable at all.
type mvtRanking struct {
	systems, plain []mvt.ScoredSystem
	demoted        int
	preferred      int
}

// best is the ranking to act on: a hull with nowhere fresh to go still gets to move.
func (rk mvtRanking) best() []mvt.ScoredSystem {
	if rk.preferred > 0 {
		return rk.systems
	}
	return rk.plain
}

// mvtRecentlyLeftWindow is how long a system the hull departed ranks below undrained ground.
func mvtRecentlyLeftWindow(cmd *RunTourCoordinatorCommand) time.Duration {
	if cmd.MVTRecentlyLeftMinutes > 0 {
		return time.Duration(cmd.MVTRecentlyLeftMinutes) * time.Minute
	}
	return time.Duration(DefaultMVTRecentlyLeftMinutes) * time.Minute
}

// mvtDemoteRecentlyLeft sinks every system this hull departed inside mvt_recently_left_minutes
// below the ground it has not just drained, so it does not re-claim what it emptied the moment
// the neighbour runs dry — the shuttle that drove one gate's fee from 128k to 300k in nine hours.
//
// NEVER REMOVES A CANDIDATE, so it can never idle a hull: a demoted system is still reachable
// behind the rest, which is what lets the fee guard fall back to one it can afford and what
// leaves the escalation something to take at its cap. Current is never demoted — staying put
// crosses no gate.
func (h *RunTourCoordinatorHandler) mvtDemoteRecentlyLeft(cmd *RunTourCoordinatorCommand, ranked []mvt.ScoredSystem, current string, now time.Time) ([]mvt.ScoredSystem, int, int) {
	window := mvtRecentlyLeftWindow(cmd)
	st := h.mvtState(cmd)
	st.mu.Lock()
	fresh := make([]mvt.ScoredSystem, 0, len(ranked))
	var drained []mvt.ScoredSystem
	preferred := 0
	for _, s := range ranked {
		if s.System != current {
			if at, ok := st.leftAt[s.System]; ok && now.Sub(at) < window {
				drained = append(drained, s)
				continue
			}
			preferred++
		}
		fresh = append(fresh, s)
	}
	st.mu.Unlock()
	if len(drained) == 0 {
		return ranked, 0, preferred
	}
	return append(fresh, drained...), len(drained), preferred
}

// mvtRankEscalating widens the claim reach one hop at a time, up to ClaimReachMaxHops,
// until the ranking offers the hull somewhere to go. excludeCurrent is the empty-exit
// case: the solver found nothing here, so only an alternative counts as an offer.
func (h *RunTourCoordinatorHandler) mvtRankEscalating(ctx context.Context, cmd *RunTourCoordinatorCommand, ship *navigation.Ship, excludeCurrent bool) (ranked []mvt.ScoredSystem, hops int, err error) {
	start := cmd.ClaimReachHops
	if start <= 0 {
		start = DefaultClaimReachHops
	}
	limit := cmd.ClaimReachMaxHops
	if limit < start {
		limit = start
	}
	current := ""
	if ship.CurrentLocation() != nil {
		current = ship.CurrentLocation().SystemSymbol
	}
	floor := mvtSpreadFloor(cmd)
	var hasAlt bool
	var rk mvtRanking
	for hops = start; ; hops++ {
		if rk, err = h.mvtRankReach(ctx, cmd, ship, hops, floor); err != nil {
			return nil, hops, err
		}
		ranked = rk.best()
		_, hasAlt = mvt.BestAlternative(ranked, current)
		// A ring offering only ground this hull just drained is no offer: widen first and take
		// the drained neighbour back at the cap, where mvtRanking.best restores it.
		offer := len(ranked) > 0 && (!excludeCurrent || hasAlt) && (rk.demoted == 0 || rk.preferred > 0)
		if hops >= limit || offer {
			break
		}
	}
	if hops > start {
		common.LoggerFromContext(ctx).Log("INFO", "MVT CLAIM: reach escalated", map[string]interface{}{
			"hull": cmd.ShipSymbol, "from_hops": start, "to_hops": hops, "candidates": len(ranked),
			"min_spread": floor, "recently_left_demoted": rk.demoted})
	}
	// The floor steers toward rich ground; with nothing reachable even at the cap, idling
	// the hull is worse than the best thin option, so it gets one relaxed look at floor 0.
	if (len(ranked) == 0 || (excludeCurrent && !hasAlt)) && floor > 0 {
		if rk, err = h.mvtRankReach(ctx, cmd, ship, limit, 0); err != nil {
			return nil, limit, err
		}
		ranked = rk.best()
		common.LoggerFromContext(ctx).Log("INFO", "MVT CLAIM: spread floor relaxed", map[string]interface{}{
			"hull": cmd.ShipSymbol, "hops": limit, "min_spread": floor, "candidates": len(ranked)})
		hops = limit
	}
	return ranked, hops, nil
}

// mvtRankReach ranks the hull's current system and every priced system within hops of it
// (discovered by mvtReach, stored-only). Any unreadable input returns an error and the caller
// stays put.
func (h *RunTourCoordinatorHandler) mvtRankReach(ctx context.Context, cmd *RunTourCoordinatorCommand, ship *navigation.Ship, hops int, floor int) (mvtRanking, error) {
	if h.mvt.depth == nil || h.mvt.claims == nil {
		return mvtRanking{}, errors.New("mvt ports not wired")
	}
	if ship.CurrentLocation() == nil {
		return mvtRanking{}, errors.New("hull has no location")
	}
	if h.jumpTolls == nil || h.gateFees == nil {
		return mvtRanking{}, errors.New("mvt ranker needs jump toll and gate fee readers")
	}
	current := ship.CurrentLocation().SystemSymbol
	systems, hopsOf, err := h.mvtReach(ctx, current, hops)
	if err != nil {
		return mvtRanking{}, err
	}
	depths, err := h.mvt.depth.SystemDepths(ctx, cmd.PlayerID, systems)
	if err != nil {
		return mvtRanking{}, fmt.Errorf("system depths: %w", err)
	}
	inTransit, err := h.mvt.claims.InTransit(ctx, cmd.PlayerID)
	if err != nil {
		return mvtRanking{}, fmt.Errorf("in-transit claims: %w", err)
	}
	if own, ok, _ := h.mvt.claims.Get(ctx, cmd.PlayerID, cmd.ShipSymbol); ok && own.ArrivedAt == nil && inTransit[own.System] > 0 {
		inTransit[own.System]--
	}
	stats := h.mvtFleetStats(ctx, cmd)
	now := h.clock.Now()
	cands := make([]mvt.Candidate, 0, len(systems))
	for _, sys := range systems {
		credits, units, entry := mvt.SystemYield(depths[sys], h.rankerAgeCaps, now, float64(floor))
		if units == 0 {
			continue
		}
		cands = append(cands, mvt.Candidate{System: sys, Hops: hopsOf[sys], YieldCredits: credits, DepthUnits: units,
			InTransit: inTransit[sys], EntryWaypoint: entry})
	}
	costs := mvt.Costs{
		TollSecondsPerHop:  h.jumpTolls.PerHopTollSeconds(ctx, cmd.PlayerID),
		GateFeeFromCurrent: h.gateFees.GateFees(ctx, cmd.PlayerID)[current],
		FleetDrawPerVisit:  stats.MeanMarginPerSystemVisit,
		FleetCreditsPerSec: stats.CreditsPerHullSec,
	}
	st := h.mvtState(cmd)
	st.mu.Lock()
	rate := st.yield.CreditsPerSec(now)
	st.mu.Unlock()
	hull := mvt.Hull{Symbol: cmd.ShipSymbol, System: current, CargoCapacity: ship.CargoCapacity(), CreditsPerSec: rate}
	plain := mvt.Rank(hull, cands, costs)
	undrainedFirst, demoted, preferred := h.mvtDemoteRecentlyLeft(cmd, plain, current, now)
	return mvtRanking{systems: undrainedFirst, plain: plain, demoted: demoted, preferred: preferred}, nil
}

// mvtRecord writes one telemetry line. Recording never fails a hull.
func (h *RunTourCoordinatorHandler) mvtRecord(ctx context.Context, cmd *RunTourCoordinatorCommand, from, to mvt.State, system string, yieldHere, bestAlt, travel float64, reason string) {
	fields := map[string]interface{}{
		"hull": cmd.ShipSymbol, "from_state": string(from), "to_state": string(to), "system": system,
		"yield_here": yieldHere, "best_alternative": bestAlt, "travel_cost": travel, "reason": reason,
	}
	common.LoggerFromContext(ctx).Log("INFO", "MVT transition", fields)
	if h.mvt.transitions == nil {
		return
	}
	if err := h.mvt.transitions.Record(ctx, mvt.Transition{PlayerID: cmd.PlayerID, Hull: cmd.ShipSymbol, From: from, To: to,
		System: system, YieldHere: yieldHere, BestAlternative: bestAlt, TravelCost: travel, Reason: reason, At: h.clock.Now()}); err != nil {
		fields["error"] = err.Error()
		common.LoggerFromContext(ctx).Log("WARNING", "MVT transition not recorded", fields)
	}
}
