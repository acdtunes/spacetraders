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
		st = &mvtHullState{yield: mvt.NewYieldTracker(cmd.YieldWindowSells, cmd.YieldMinSells), basis: map[string][]mvtLot{}}
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

// mvtReach lists the hull's current system and every system within ClaimReachHops of it, read
// from the PERSISTED gate adjacency only (GateGraph.StoredSystemsWithinJumps). The shadow runs
// after every productive tour of every trade hull, so its discovery must cost zero API and write
// nothing: the reposition scan's fetch-through walk (repositionNeighborsWithinJumps → Connections)
// issues a live GetJumpGate and a gate_edges Replace for every neighbour whose edge set is missing
// or past its 24h window, and the fresher graph would change the old path's later reposition
// candidate sets — a behaviour change step 1 promises not to make. With no gate graph wired there
// is no stored adjacency to walk and the hull ranks its own system alone (never the live
// jump-gate scan); an unreadable store is an error and the caller stays put. Systems come back
// with current first and the rest in symbol order, so a given graph ranks the same way twice.
func (h *RunTourCoordinatorHandler) mvtReach(ctx context.Context, cmd *RunTourCoordinatorCommand, current string) ([]string, map[string]int, error) {
	systems := []string{current}
	hops := map[string]int{current: 0}
	if h.legs.gateGraph == nil {
		return systems, hops, nil
	}
	reach := cmd.ClaimReachHops
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

// mvtRank ranks the hull's current system and every priced system within ClaimReachHops
// (discovered by mvtReach, stored-only). Any unreadable input returns an error and the caller
// stays put.
func (h *RunTourCoordinatorHandler) mvtRank(ctx context.Context, cmd *RunTourCoordinatorCommand, ship *navigation.Ship) ([]mvt.ScoredSystem, error) {
	if h.mvt.depth == nil || h.mvt.claims == nil {
		return nil, errors.New("mvt ports not wired")
	}
	if ship.CurrentLocation() == nil {
		return nil, errors.New("hull has no location")
	}
	if h.jumpTolls == nil || h.gateFees == nil {
		return nil, errors.New("mvt ranker needs jump toll and gate fee readers")
	}
	current := ship.CurrentLocation().SystemSymbol
	systems, hops, err := h.mvtReach(ctx, cmd, current)
	if err != nil {
		return nil, err
	}
	depths, err := h.mvt.depth.SystemDepths(ctx, cmd.PlayerID, systems)
	if err != nil {
		return nil, fmt.Errorf("system depths: %w", err)
	}
	inTransit, err := h.mvt.claims.InTransit(ctx, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("in-transit claims: %w", err)
	}
	if own, ok, _ := h.mvt.claims.Get(ctx, cmd.PlayerID, cmd.ShipSymbol); ok && own.ArrivedAt == nil && inTransit[own.System] > 0 {
		inTransit[own.System]--
	}
	stats := h.mvtFleetStats(ctx, cmd)
	now := h.clock.Now()
	cands := make([]mvt.Candidate, 0, len(systems))
	for _, sys := range systems {
		credits, units, entry := mvt.SystemYield(depths[sys], h.rankerAgeCaps, now)
		if units == 0 {
			continue
		}
		cands = append(cands, mvt.Candidate{System: sys, Hops: hops[sys], YieldCredits: credits, DepthUnits: units,
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
	return mvt.Rank(hull, cands, costs), nil
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
