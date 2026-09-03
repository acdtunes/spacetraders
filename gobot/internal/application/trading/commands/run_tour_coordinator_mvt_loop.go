package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading/mvt"
)

const mvtReasonClaim = "claim"

// mvtClaimAndTravel runs CLAIM from wherever the hull stands. Every failure resolves to
// "stay where you are". The empty exit drops the current system from the ranking: three
// no-plan tours are the solver's verdict on it, whatever depth the ledger still shows.
// The reach escalates only here — a hull with nothing priced in reach had no move at all.
// A hold after repeated travel failures binds here too, or a relaunch and the empty
// exit would retry the failing route at once.
func (h *RunTourCoordinatorHandler) mvtClaimAndTravel(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse, episode *repositionEpisode, reason string, budget tourPlanBudget) (bool, error) {
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil || ship == nil || ship.CurrentLocation() == nil {
		return false, err
	}
	current := ship.CurrentLocation().SystemSymbol
	st := h.mvtState(cmd)
	st.mu.Lock()
	held := st.holding(h.clock.Now())
	st.mu.Unlock()
	if held {
		h.mvtRecord(ctx, cmd, mvt.StateTrade, mvt.StateTrade, current, 0, 0, 0, mvtReasonHold)
		return false, nil
	}
	ranked, _, rerr := h.mvtRankEscalating(ctx, cmd, ship, reason == mvtReasonEmpty)
	if rerr != nil {
		common.LoggerFromContext(ctx).Log("WARNING", "MVT CLAIM: ranker unreadable, staying", map[string]interface{}{"hull": cmd.ShipSymbol, "error": rerr.Error()})
		st.mu.Lock()
		st.claimed = current
		st.mu.Unlock()
		h.mvtRecord(ctx, cmd, mvt.StateClaim, mvt.StateTrade, current, 0, 0, 0, mvtReasonRankerUnreadable)
		return false, nil
	}
	if reason == mvtReasonEmpty {
		ranked = mvtOthers(ranked, current)
		// A later rescue must never fly back to the ground the previous one just left: that
		// ping-pong burns the cap on a system this episode already gave up on. Skipped once the
		// episode is spent, where the ranking only names the alternative the refusal records.
		if episode != nil && episode.rescues >= 1 && episode.fromSystem != "" && !mvtEpisodeSpent(episode, mvtRescueLimit(cmd)) {
			ranked = mvtOthers(ranked, episode.fromSystem)
			if len(ranked) > 0 {
				common.LoggerFromContext(ctx).Log("INFO", "MVT CLAIM: second rescue", map[string]interface{}{
					"hull": cmd.ShipSymbol, "rescues": episode.rescues, "excluded": episode.fromSystem})
			}
		}
	}
	return h.mvtTravelTo(ctx, cmd, response, episode, ranked, reason, 0, budget)
}

// mvtRescueLimit is how many rescue jumps one margins-death episode may fly.
func mvtRescueLimit(cmd *RunTourCoordinatorCommand) int {
	if cmd.MVTRescueJumpsPerEpisode > 0 {
		return cmd.MVTRescueJumpsPerEpisode
	}
	return DefaultMVTRescueJumpsPerEpisode
}

// mvtEpisodeSpent reports whether this episode may fly no further rescue. A mover that is
// NOT an MVT rescue (the disposal ladder, the offload rung) marks only repositioned, and it
// still spends the whole episode exactly as it does today.
func mvtEpisodeSpent(episode *repositionEpisode, limit int) bool {
	if episode.rescues == 0 {
		return episode.repositioned
	}
	return episode.rescues >= limit
}

// mvtJumpFeeMaxShare is the share of a load's expected credits a jump's gate fee may spend.
func mvtJumpFeeMaxShare(cmd *RunTourCoordinatorCommand) int {
	if cmd.MVTJumpFeeMaxSharePct > 0 {
		return cmd.MVTJumpFeeMaxSharePct
	}
	return DefaultMVTJumpFeeMaxSharePct
}

// mvtJumpFeeGuard drops every candidate whose gate fee eats more than mvt_jump_fee_max_share_pct
// of the credits the ranker expects the hull's next load there to make, and reports whether the
// hull is left with nowhere better than where it stands.
//
// A MONEY GUARD IN SHAPE: it only ever refuses. The ranker prices a crossing per unit against a
// yield estimate, so a gate charging 300k still ranked first behind a rich enough estimate — and
// a load that turns out empty on arrival pays the fee anyway. A candidate the ranker expects to
// earn NOTHING fails the guard rather than dividing by a yield that is not there. The refusal
// runs BEFORE the claim upsert and the persisted reposition, so nothing survives it but the log
// and the transition row. current is kept whatever its fee: standing still crosses no gate.
func (h *RunTourCoordinatorHandler) mvtJumpFeeGuard(ctx context.Context, cmd *RunTourCoordinatorCommand, ranked []mvt.ScoredSystem, current string) ([]mvt.ScoredSystem, bool) {
	share := mvtJumpFeeMaxShare(cmd)
	kept := make([]mvt.ScoredSystem, 0, len(ranked))
	dropped := false
	for _, s := range ranked {
		if s.System == current || (s.ExpectedLoadCredits > 0 && float64(s.GateFee) <= float64(share)/100*s.ExpectedLoadCredits) {
			kept = append(kept, s)
			continue
		}
		dropped = true
		common.LoggerFromContext(ctx).Log("INFO", "MVT CLAIM: jump fee guard refused", map[string]interface{}{
			"hull": cmd.ShipSymbol, "system": s.System, "gate_fee": s.GateFee,
			"expected_load_credits": s.ExpectedLoadCredits, "share_pct": share})
	}
	if !dropped {
		return ranked, false
	}
	if len(kept) == 0 {
		return ranked, true
	}
	if kept[0].System != current {
		return kept, false
	}
	// The head is where the hull stands, but a survivor behind it may still be the better move
	// — the recently-left preference sinks ground the hull drained however it scored, and
	// idling on top of an unaffordable gate is not what the guard is for. Only a survivor the
	// ranker scores ABOVE standing still is taken, so a refusal never widens a jump.
	for _, s := range kept[1:] {
		if s.System != current && s.Score > kept[0].Score {
			return append([]mvt.ScoredSystem{s}, mvtOthers(kept, s.System)...), false
		}
	}
	return ranked, true
}

// mvtOthers is ranked without current.
func mvtOthers(ranked []mvt.ScoredSystem, current string) []mvt.ScoredSystem {
	others := make([]mvt.ScoredSystem, 0, len(ranked))
	for _, s := range ranked {
		if s.System != current {
			others = append(others, s)
		}
	}
	return others
}

// mvtStay pins the hull where it stands and closes the CLAIM on one line.
func (h *RunTourCoordinatorHandler) mvtStay(ctx context.Context, cmd *RunTourCoordinatorCommand, current string, yieldHere float64, alt mvt.ScoredSystem, reason string) (bool, error) {
	h.mvtStampPresence(ctx, cmd, current)
	st := h.mvtState(cmd)
	st.mu.Lock()
	st.claimed = current
	st.mu.Unlock()
	h.mvtRecord(ctx, cmd, mvt.StateClaim, mvt.StateTrade, current, yieldHere, alt.Score, alt.TravelPerUnit, reason)
	return false, nil
}

// mvtTravelTo claims ranked[0] and moves there; ranked[0] == current (or nothing ranked)
// is a stay that re-stamps presence in the registry. Unwired ports make it a no-op.
// yieldHere is the departing hull's realised estimate, carried on the TRADE→CLAIM line.
// A move is refused — as a stay — under the operator kill-switch, with no spend headroom,
// once this episode has spent its rescue jumps (mvt_rescue_jumps_per_episode; nil: no bound), or while
// the hull is laden: the disposal ladder discharges first and the next tour end re-evaluates.
func (h *RunTourCoordinatorHandler) mvtTravelTo(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse, episode *repositionEpisode, ranked []mvt.ScoredSystem, reason string, yieldHere float64, budget tourPlanBudget) (bool, error) {
	if h.mvt.claims == nil {
		return false, nil
	}
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil || ship == nil || ship.CurrentLocation() == nil {
		return false, err
	}
	current := ship.CurrentLocation().SystemSymbol
	now := h.clock.Now()
	st := h.mvtState(cmd)
	logger := common.LoggerFromContext(ctx)

	if len(ranked) == 0 || ranked[0].System == current {
		stayReason := mvt.ReasonStay
		if len(ranked) == 0 {
			stayReason = mvt.ReasonNoAlternative
		}
		return h.mvtStay(ctx, cmd, current, 0, mvt.ScoredSystem{}, stayReason)
	}

	ranked, refused := h.mvtJumpFeeGuard(ctx, cmd, ranked, current)
	if refused {
		return h.mvtStay(ctx, cmd, current, yieldHere, mvt.ScoredSystem{}, mvtReasonJumpFeeGuard)
	}

	target := ranked[0]
	switch {
	case cmd.RepositionDisabled:
		return h.mvtStay(ctx, cmd, current, yieldHere, target, mvtReasonRepositionDisabled)
	case h.budgetDeniesEverySpend(cmd, budget):
		return h.mvtStay(ctx, cmd, current, yieldHere, target, mvtReasonBudgetDenied)
	case episode != nil && mvtEpisodeSpent(episode, mvtRescueLimit(cmd)):
		return h.mvtStay(ctx, cmd, current, yieldHere, target, mvtReasonEpisodeSpent)
	case isLadenForOffload(ship.CargoUnits(), ship.CargoCapacity()):
		return h.mvtStay(ctx, cmd, current, yieldHere, target, mvtReasonLaden)
	}
	h.mvtRecord(ctx, cmd, mvt.StateTrade, mvt.StateClaim, current, yieldHere, target.Score, target.TravelPerUnit, reason)
	h.mvtRecord(ctx, cmd, mvt.StateClaim, mvt.StateTravel, target.System, 0, target.Score, target.TravelPerUnit, mvtReasonClaim)
	if err := h.mvt.claims.Upsert(ctx, cmd.PlayerID, cmd.ShipSymbol, target.System, now); err != nil {
		logger.Log("WARNING", "MVT CLAIM: registry write failed, staying", map[string]interface{}{"hull": cmd.ShipSymbol, "error": err.Error()})
		h.mvtRecord(ctx, cmd, mvt.StateClaim, mvt.StateTrade, current, yieldHere, target.Score, target.TravelPerUnit, mvtReasonClaimWriteFailed)
		return false, nil
	}
	h.persistReposition(ctx, cmd, RepositionEpisode{InProgress: true, TargetSystem: target.System, TargetWaypoint: target.EntryWaypoint})
	jumps := target.Hops
	if jumps < 1 {
		jumps = 1
	}
	if terr := h.legs.RepositionToWaypointWithinJumps(ctx, cmd.ShipSymbol, target.EntryWaypoint, cmd.PlayerID, jumps); terr != nil {
		// A stop mid-flight is not a failed flight: the claim and the persisted destination
		// stay so the restart resumes the jump, as every old-path mover does.
		if ctx.Err() != nil {
			return false, fmt.Errorf("MVT TRAVEL of %s to %s interrupted: %w", cmd.ShipSymbol, target.EntryWaypoint, terr)
		}
		// The flight may have failed after a jump landed, so the scope pins to where the
		// hull physically stands, never to where it started.
		standing := h.mvtStanding(ctx, cmd, current)
		if standing != target.System {
			logger.Log("WARNING", "MVT TRAVEL failed; claim released", map[string]interface{}{"hull": cmd.ShipSymbol, "target": target.System, "standing": standing, "error": terr.Error()})
			_ = h.mvt.claims.Release(ctx, cmd.PlayerID, cmd.ShipSymbol)
			h.persistReposition(ctx, cmd, RepositionEpisode{})
			metrics.RecordTourReposition(cmd.PlayerID, "failed")
			st.mu.Lock()
			st.travelFailures++
			if st.travelFailures >= mvtTravelFailureCap {
				st.travelFailures = 0
				st.holdSells = cmd.YieldWindowSells
				st.holdUntil = now.Add(h.mvtCadence(cmd))
			}
			st.claimed = standing
			st.mu.Unlock()
			h.mvtRecord(ctx, cmd, mvt.StateTravel, mvt.StateTrade, standing, 0, target.Score, target.TravelPerUnit, mvtReasonTravelFailed)
			return false, nil
		}
		// Only the gate->market hop failed: the hull occupies its claim and trades from the gate.
		logger.Log("WARNING", "MVT TRAVEL: arrival hop failed after the jump; trading from the gate", map[string]interface{}{"hull": cmd.ShipSymbol, "target": target.System, "error": terr.Error()})
	}
	_ = h.mvt.claims.MarkArrived(ctx, cmd.PlayerID, cmd.ShipSymbol, h.clock.Now())
	h.persistReposition(ctx, cmd, RepositionEpisode{})
	metrics.RecordTourReposition(cmd.PlayerID, "success")
	response.Repositions++
	if episode != nil {
		episode.repositioned = true
		episode.rescues++
		episode.fromSystem = current
		episode.toSystem = target.System
	}
	st.mu.Lock()
	st.claimed = target.System
	st.yield.Reset()
	st.basis = map[string][]mvtLot{}
	st.travelFailures = 0
	// The ground just departed, so the ranker can prefer somewhere the hull has not drained.
	if st.leftAt == nil {
		st.leftAt = map[string]time.Time{}
	}
	st.leftAt[current] = now
	st.mu.Unlock()
	h.mvtRecord(ctx, cmd, mvt.StateTravel, mvt.StateTrade, target.System, 0, target.Score, target.TravelPerUnit, mvtReasonArrived)
	return true, nil
}

// mvtStanding is the system the hull is physically in; an unreadable hull reports fallback.
func (h *RunTourCoordinatorHandler) mvtStanding(ctx context.Context, cmd *RunTourCoordinatorCommand, fallback string) string {
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil || ship == nil || ship.CurrentLocation() == nil {
		return fallback
	}
	return ship.CurrentLocation().SystemSymbol
}

// mvtStampPresence records "I am here" so other hulls' rankers see the system occupied
// through the ledger's shadows, and recovery finds an arrived row.
func (h *RunTourCoordinatorHandler) mvtStampPresence(ctx context.Context, cmd *RunTourCoordinatorCommand, system string) {
	now := h.clock.Now()
	if err := h.mvt.claims.Upsert(ctx, cmd.PlayerID, cmd.ShipSymbol, system, now); err != nil {
		return
	}
	_ = h.mvt.claims.MarkArrived(ctx, cmd.PlayerID, cmd.ShipSymbol, now)
}

// mvtRecover restores the loop after a container restart from the claim row and the
// in-flight reposition resume. An arrived claim is adopted only where the hull actually
// stands; a hull with no usable claim bootstraps from where it stands.
func (h *RunTourCoordinatorHandler) mvtRecover(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse, episode *repositionEpisode, budget tourPlanBudget) error {
	if h.mvt.claims == nil {
		return nil
	}
	claim, ok, err := h.mvt.claims.Get(ctx, cmd.PlayerID, cmd.ShipSymbol)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", "MVT recover: claim unreadable, bootstrapping", map[string]interface{}{"hull": cmd.ShipSymbol, "error": err.Error()})
		ok = false
	}
	st := h.mvtState(cmd)
	switch {
	case ok && claim.ArrivedAt != nil && h.mvtStanding(ctx, cmd, claim.System) == claim.System:
		st.mu.Lock()
		st.claimed = claim.System
		st.mu.Unlock()
		return nil
	case ok && episode != nil && episode.repositioned:
		if claim.System == episode.toSystem {
			_ = h.mvt.claims.MarkArrived(ctx, cmd.PlayerID, cmd.ShipSymbol, h.clock.Now())
		} else {
			h.mvtStampPresence(ctx, cmd, episode.toSystem)
		}
		st.mu.Lock()
		st.claimed = episode.toSystem
		st.mu.Unlock()
		h.mvtRecord(ctx, cmd, mvt.StateTravel, mvt.StateTrade, episode.toSystem, 0, 0, 0, mvtReasonArrived)
		return nil
	case ok:
		_ = h.mvt.claims.Release(ctx, cmd.PlayerID, cmd.ShipSymbol)
	}
	_, err = h.mvtClaimAndTravel(ctx, cmd, response, episode, mvtReasonBootstrap, budget)
	return err
}

// mvtRetire releases the claim of a hull standing down for retirement (spec §3), so the
// system stops reading as occupied to every other ranker.
func (h *RunTourCoordinatorHandler) mvtRetire(ctx context.Context, cmd *RunTourCoordinatorCommand) {
	if !cmd.MVTLoop || h.mvt.claims == nil {
		return
	}
	if err := h.mvt.claims.Release(ctx, cmd.PlayerID, cmd.ShipSymbol); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", "MVT retirement: claim not released", map[string]interface{}{"hull": cmd.ShipSymbol, "error": err.Error()})
	}
	st := h.mvtState(cmd)
	st.mu.Lock()
	st.claimed = ""
	st.mu.Unlock()
}

// mvtBootBudget prices the bootstrap CLAIM the way the loop prices a tour: an explicit cap
// as given, otherwise the live capital budget. An unreadable treasury leaves the cap at
// zero, which the guard reads as no headroom.
func (h *RunTourCoordinatorHandler) mvtBootBudget(ctx context.Context, cmd *RunTourCoordinatorCommand, budget tourPlanBudget) tourPlanBudget {
	if cmd.MaxSpend != 0 {
		return budget.withMaxSpend(cmd.MaxSpend)
	}
	resolved, _ := h.defaultMaxSpend(ctx, cmd.PlayerID, budget.reserve)
	return budget.withMaxSpend(resolved)
}

// mvtReconcileScope re-pins the claim to home when a path that does not own the claim
// (disposal, offload, retirement, a tag flip) has moved the hull, so the solver is never
// scoped to a system the hull is not in. Returns the system the scope is pinned to.
func (h *RunTourCoordinatorHandler) mvtReconcileScope(ctx context.Context, cmd *RunTourCoordinatorCommand, home string) string {
	st := h.mvtState(cmd)
	st.mu.Lock()
	claimed := st.claimed
	if claimed != "" && claimed != home {
		st.claimed = home
	}
	st.mu.Unlock()
	switch {
	case claimed == "":
		return home
	case claimed == home:
		return claimed
	}
	if h.mvt.claims != nil {
		h.mvtStampPresence(ctx, cmd, home)
	}
	h.mvtRecord(ctx, cmd, mvt.StateTrade, mvt.StateTrade, home, 0, 0, 0, mvtReasonRelocated)
	return home
}

// mvtObserveLeg feeds one realised leg into the hull's yield tracker. Buys open FIFO
// lots; sells consume them and observe margin per unit. Sells with no lot are not evidence.
func (h *RunTourCoordinatorHandler) mvtObserveLeg(cmd *RunTourCoordinatorCommand, leg trading.TourLegTelemetry) {
	if !cmd.MVTLoop || leg.RealizedUnits <= 0 {
		return
	}
	st := h.mvtState(cmd)
	st.mu.Lock()
	defer st.mu.Unlock()
	if leg.IsBuy {
		st.basis[leg.Good] = append(st.basis[leg.Good], mvtLot{units: leg.RealizedUnits, price: leg.RealizedUnitPrice})
		return
	}
	lots := st.basis[leg.Good]
	if len(lots) == 0 {
		return
	}
	need, consumed, margin := leg.RealizedUnits, 0, 0.0
	for need > 0 && len(lots) > 0 {
		take := lots[0].units
		if take > need {
			take = need
		}
		margin += float64(leg.RealizedUnitPrice-lots[0].price) * float64(take)
		consumed += take
		need -= take
		lots[0].units -= take
		if lots[0].units == 0 {
			lots = lots[1:]
		}
	}
	st.basis[leg.Good] = lots
	if consumed == 0 {
		return
	}
	st.yield.Observe(margin/float64(consumed), consumed, leg.RealizedAt)
	if st.holdSells > 0 {
		st.holdSells--
	}
}

// mvtAfterTour applies the departure rule after a productive tour. Deviation from spec
// §1 recorded in the plan: the EWMA updates per sell, the decision is taken at tour end.
// Unwired ports make it a no-op.
func (h *RunTourCoordinatorHandler) mvtAfterTour(ctx context.Context, cmd *RunTourCoordinatorCommand, response *RunTourCoordinatorResponse, budget tourPlanBudget) error {
	if h.mvt.claims == nil {
		return nil
	}
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil || ship == nil || ship.CurrentLocation() == nil {
		return nil
	}
	current := ship.CurrentLocation().SystemSymbol
	st := h.mvtState(cmd)
	st.mu.Lock()
	if st.claimed == "" {
		st.claimed = current
		st.mu.Unlock()
		h.mvtStampPresence(ctx, cmd, current)
		st.mu.Lock()
	}
	hold := st.holding(h.clock.Now())
	st.mu.Unlock()

	ranked, rerr := h.mvtRank(ctx, cmd, ship)
	if rerr != nil {
		h.mvtRecord(ctx, cmd, mvt.StateTrade, mvt.StateTrade, current, 0, 0, 0, mvtReasonRankerUnreadable)
		return nil
	}
	alt, hasAlt := mvt.BestAlternative(ranked, current)
	st.mu.Lock()
	d := mvt.Decide(st.yield, alt.Score, hasAlt)
	st.mu.Unlock()
	if hold {
		h.mvtRecord(ctx, cmd, mvt.StateTrade, mvt.StateTrade, current, d.YieldHere, alt.Score, alt.TravelPerUnit, mvtReasonHold)
		return nil
	}
	if !d.Leave {
		h.mvtRecord(ctx, cmd, mvt.StateTrade, mvt.StateTrade, current, d.YieldHere, alt.Score, alt.TravelPerUnit, d.Reason)
		return nil
	}
	// The verdict weighed realised yield here against the alternatives, so the ledger's own
	// (unrealised) score for this system must not put it back at the head of the list. A
	// productive tour just cleared the episode, so the departure carries no rescue bound.
	_, err = h.mvtTravelTo(ctx, cmd, response, nil, mvtOthers(ranked, current), d.Reason, d.YieldHere, budget)
	return err
}
