package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// Cross-engine market-absorption integration for the tour coordinator (sp-78ai L3):
// the tour becomes a ledger WRITER (reserve at plan-accept, convert at sale, release on
// re-plan/exit) and a READER (net outstanding depth into every plan so the solver plans
// AROUND sinks other containers occupy). The L1 substrate (the DB-backed
// absorption.Ledger) does the concurrency-safe, restart-survivable bookkeeping; this
// file wires the tour's lifecycle onto it. All of it is inert when the ledger is unwired
// (the pre-sp-78ai shape and every test that does not call SetAbsorptionLedger).

const (
	// tourACapTranches MUST stay in lockstep with tour_solver.py's
	// MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE: the fleet-wide reservation ceiling per
	// (market, good, side) is this many trade_volume tranches. The solver A-caps the
	// tranches a single plan takes; the ledger's Reserve makes that cap FLEET-WIDE (the
	// bead's design goal (a)) by rejecting a plan whose tranches + others' outstanding
	// would exceed it.
	tourACapTranches = 2
	// defaultTourPlannedTTLSlack pads a plan's projected round-trip so a healthy in-flight
	// reservation never expires mid-tour; minTourPlannedTTL floors it for short tours.
	// The ledger's TTL sweep + dead-container reclaim are the real cleanup — this is the
	// backstop bound so a wedged container cannot hold depth forever (design §1).
	defaultTourPlannedTTLSlack = 15 * time.Minute
	minTourPlannedTTL          = 30 * time.Minute
	// tourReserveMaxRetries bounds the re-plan-on-breach loop inside planAndReserve. A
	// breach is a rare accept-race (another container reserved a sink between our netting
	// snapshot and our Reserve); a couple of re-plans against fresh ledger state clear
	// it, and a persistent contention exits the tour infeasible rather than spinning.
	tourReserveMaxRetries = 2
	// absorptionEngineTour stamps this engine's ledger rows (telemetry + reclaim
	// attribution), matching the "tour" tier the design names.
	absorptionEngineTour = "tour"
)

// SetAbsorptionLedger wires the cross-engine absorption ledger (sp-78ai L3) so the tour
// reserves/nets/converts against fleet-wide market depth. consultDisabled is the operator
// escape hatch (RULINGS #5): it stops NETTING and stops conditional GATING (never
// rejects/re-plans on a breach) while still RECORDING each plan's occupancy so other
// engines keep consulting it — the idle-arb "kill the consult, keep the record" posture.
// plannedTTLSlack pads reservation TTLs (0 → default). Left unwired, the tour plans and
// flies exactly as pre-sp-78ai. Mirrors the sibling SetAbsorptionLedger injections.
func (h *RunTourCoordinatorHandler) SetAbsorptionLedger(ledger absorption.Ledger, consultDisabled bool, plannedTTLSlack time.Duration) {
	h.absorptionLedger = ledger
	h.tourConsultDisabled = consultDisabled
	if plannedTTLSlack <= 0 {
		plannedTTLSlack = defaultTourPlannedTTLSlack
	}
	h.tourPlannedTTLSlack = plannedTTLSlack
}

// planAndReserve plans a depth-netted tour for the given ship state and conditionally
// reserves its tranches all-or-nothing, retrying against fresh ledger state when a
// reservation loses the accept race (a breach is a normal "sink now occupied" re-plan,
// not a failure — design §1/§2). It releases this container's stale PLANNED rows FIRST so
// the plan nets against OTHERS' depth and Reserve cannot double-count the container's own
// prior/pre-restart rows (the restart de-dup). Returns (plan, "", true, nil) on success;
// (nil, reason, false, nil) when no plan/reservation could be secured; a non-nil error
// only on an operational failure the caller should surface.
func (h *RunTourCoordinatorHandler) planAndReserve(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	ship *navigation.Ship,
	maxHops int,
	maxSpend, reserve int64,
	modelVersion string,
) (*routing.TourPlan, map[shadowSinkKey]bool, string, bool, error) {
	// Clear this container's stale in-flight intent before (re)planning: a prior tour's
	// leftover holds, or pre-restart rows that liveness re-adopted. EXECUTED recovery
	// shadows are left untouched (real damage still recovering — the plan must avoid them).
	h.releaseTourReservations(ctx, cmd)

	for attempt := 0; attempt <= tourReserveMaxRetries; attempt++ {
		plan, snapshot, absorptionView, err := h.plan(ctx, ship, maxHops, maxSpend, reserve, cmd, modelVersion)
		if err != nil {
			return nil, nil, fmt.Sprintf("tour unavailable: planner error: %v", err), false, nil
		}
		if !plan.Feasible {
			return nil, nil, fmt.Sprintf("tour unavailable: %s", plan.InfeasibleReason), false, nil
		}
		reserved, rerr := h.reserveTourPlan(ctx, cmd, plan, snapshot)
		if rerr == nil && reserved {
			// Q3 (REPORT-ONLY): log the recovery burden this accepted plan projects onto
			// the fleet — it must never steer selection (the analyst's experiment bar
			// accumulates from this log; a live shadow-priced objective is gated on
			// offline replay, not switched on here).
			h.logRecoveryBurden(ctx, cmd, plan, snapshot)
			// sp-8cz9 burn-in: score cap-binding on this accepted plan and hand the
			// execution path the ladder-probe set — both derived from the SAME netted
			// depth already read, both pure observation (never gate a trade, RULINGS #4).
			h.recordCapBinding(ctx, cmd, plan, snapshot, absorptionView)
			return plan, shadowSinksFromAbsorption(absorptionView), "", true, nil
		}
		// Breach (ok=false) or a ledger-gate error (fail-closed for THIS attempt): re-plan
		// against fresh ledger state — the contested sink now shows occupied to the netting.
	}
	return nil, nil, "tour unavailable: could not reserve tour depth (sinks contended by other containers)", false, nil
}

// reserveTourPlan reserves the plan's per-(waypoint, good, side) tranches. In the default
// (consult-enabled) mode it is the CONDITIONAL, all-or-nothing Reserve: ok=false means a
// sink breached the fleet-wide cap and the caller re-plans; a DB error fails CLOSED for
// this attempt (RULINGS #4 — the money guard never proceeds on an unrunnable gate). In
// the consult-disabled escape-hatch mode it RECORDS each sink unconditionally (never
// gates) so other engines still see the tour's occupancy. A nil ledger or container-less
// run reserves nothing and proceeds.
func (h *RunTourCoordinatorHandler) reserveTourPlan(ctx context.Context, cmd *RunTourCoordinatorCommand, plan *routing.TourPlan, snapshot []routing.TourGoodSnapshot) (bool, error) {
	if h.absorptionLedger == nil || cmd.ContainerID == "" {
		return true, nil
	}
	entries := h.buildTourReserveEntries(plan, snapshot)
	if len(entries) == 0 {
		return true, nil
	}
	logger := common.LoggerFromContext(ctx)

	if h.tourConsultDisabled {
		// Escape hatch: publish occupancy but never gate. RecordPlanned is unconditional
		// (fail-open on write — a launched plan is never stranded by a ledger miss).
		for _, e := range entries {
			if _, err := h.absorptionLedger.RecordPlanned(ctx, cmd.PlayerID, cmd.ContainerID, absorptionEngineTour, e); err != nil {
				logger.Log("WARNING", fmt.Sprintf("Tour absorption record-only: could not record %s/%s (%s) for %s (plan flies; guards intact): %v",
					e.Waypoint, e.Good, e.Side, cmd.ContainerID, err), nil)
			}
		}
		return true, nil
	}

	_, ok, err := h.absorptionLedger.Reserve(ctx, cmd.PlayerID, cmd.ContainerID, absorptionEngineTour, entries)
	if err != nil {
		// The gate itself could not run — fail CLOSED for this attempt (do not fly an
		// un-gated co-dump). planAndReserve re-plans; a persistent error exits infeasible.
		logger.Log("WARNING", fmt.Sprintf("Tour absorption reserve errored for %s - failing closed this attempt (will re-plan): %v", cmd.ContainerID, err), nil)
		return false, err
	}
	if !ok {
		logger.Log("INFO", fmt.Sprintf("Tour absorption reserve breached the fleet-wide sink cap for %s - re-planning against the now-occupied sink", cmd.ContainerID), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "container_id": cmd.ContainerID,
		})
	}
	return ok, nil
}

// buildTourReserveEntries aggregates a plan's planned units per (waypoint, good, side) —
// skipping DEPOSIT tranches, whose synthetic warehouse sink has no market depth to reserve
// — and sizes each entry: CapUnits = tourACapTranches × trade_volume (the fleet-wide
// ceiling), Tier = the sink's live activity tier (so a converted shadow decays on the
// right curve), QuotedPrice = the side's live quote (telemetry), TTL = 2× projected tour
// seconds + slack. The entry order is deterministic (plan/leg/trade order) so reservation
// IDs line up with the plan.
func (h *RunTourCoordinatorHandler) buildTourReserveEntries(plan *routing.TourPlan, snapshot []routing.TourGoodSnapshot) []absorption.ReserveEntry {
	type wg struct{ wp, good string }
	snap := make(map[wg]routing.TourGoodSnapshot, len(snapshot))
	for _, s := range snapshot {
		snap[wg{s.Waypoint, s.Good}] = s
	}

	type lane struct{ wp, good, side string }
	units := map[lane]int{}
	order := make([]lane, 0)
	for _, leg := range plan.Legs {
		for _, tr := range leg.Trades {
			if tr.IsDeposit {
				continue // synthetic haul-to-storage sink: no market depth (design §0/§2)
			}
			side := absorption.SideSell
			if tr.IsBuy {
				side = absorption.SideBuy
			}
			k := lane{leg.Waypoint, tr.Good, side}
			if _, seen := units[k]; !seen {
				order = append(order, k)
			}
			units[k] += tr.Units
		}
	}

	ttl := h.tourReserveTTL(plan)
	entries := make([]absorption.ReserveEntry, 0, len(order))
	for _, k := range order {
		s := snap[wg{k.wp, k.good}]
		capUnits := tourACapTranches * s.TradeVolume
		if capUnits < units[k] {
			// Defensive: a missing snapshot row (tv 0) or a plan somehow above the A-cap
			// must never self-breach the plan's OWN reservation — floor the cap at the
			// planned units, which degrades to binary exclusion against OTHER containers.
			capUnits = units[k]
		}
		quoted := s.Bid
		if k.side == absorption.SideBuy {
			quoted = s.Ask
		}
		entries = append(entries, absorption.ReserveEntry{
			Waypoint:    k.wp,
			Good:        k.good,
			Side:        k.side,
			Units:       units[k],
			CapUnits:    capUnits,
			Tier:        s.Activity,
			QuotedPrice: quoted,
			TTL:         ttl,
		})
	}
	return entries
}

// tourReserveTTL is 2× the plan's projected travel seconds + the configured slack,
// floored at minTourPlannedTTL — the design's per-plan TTL bound so a wedged container
// cannot hold depth past it (the sweep + dead-container reclaim are the real cleanup).
func (h *RunTourCoordinatorHandler) tourReserveTTL(plan *routing.TourPlan) time.Duration {
	var secs int
	for _, leg := range plan.Legs {
		secs += leg.TravelSecondsFromPrev
	}
	ttl := 2*time.Duration(secs)*time.Second + h.tourPlannedTTLSlack
	if ttl < minTourPlannedTTL {
		ttl = minTourPlannedTTL
	}
	return ttl
}

// assembleAbsorption reads the player's outstanding cross-container absorption (PLANNED
// units + EXECUTED shadows already decayed Go-side by the ledger) and shapes it for the
// planner to net. It fails OPEN on a read error (returns nil → plan against full depth):
// the conditional Reserve re-checks the fleet-wide cap in-transaction, so it is the hard
// backstop and a transient netting miss cannot slip an un-capped co-dump. Inert when the
// ledger is unwired or the consult is killed.
func (h *RunTourCoordinatorHandler) assembleAbsorption(ctx context.Context, playerID int) []routing.TourMarketAbsorption {
	if h.absorptionLedger == nil || h.tourConsultDisabled {
		return nil
	}
	pools, err := h.absorptionLedger.Outstanding(ctx, playerID)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING",
			fmt.Sprintf("Tour absorption consult: ledger read failed, planning against full depth (Reserve remains the hard cap): %v", err), nil)
		return nil
	}
	out := make([]routing.TourMarketAbsorption, 0, len(pools))
	for key, occ := range pools {
		if occ.PlannedUnits == 0 && occ.RecoveringResidual == 0 {
			continue
		}
		out = append(out, routing.TourMarketAbsorption{
			Waypoint:        key.Waypoint,
			Good:            key.Good,
			Side:            key.Side,
			PlannedUnits:    occ.PlannedUnits,
			RecoveringUnits: occ.RecoveringResidual,
		})
	}
	return out
}

// tourSinkSale accumulates one sink good's realized sale across a leg's price-tiered
// tranches, plus the live tier + trade_volume the re-verify read (stable across the
// sink's tranches), so the leg-end conversion sizes the shadow on the FULL crush.
type tourSinkSale struct {
	units       int
	tier        string
	tradeVolume int
}

// newLegSells allocates a per-leg sink accumulator, or nil when no ledger is wired (the
// accumulation and conversion then no-op — the tour flies exactly as pre-sp-78ai). The
// map is allocated whenever a ledger is present, regardless of the consult switch:
// recording (and therefore converting) still runs in the escape-hatch mode.
func (h *RunTourCoordinatorHandler) newLegSells() map[string]*tourSinkSale {
	if h.absorptionLedger == nil {
		return nil
	}
	return map[string]*tourSinkSale{}
}

// noteSinkSale folds one executed sell tranche into its sink's accumulator (units summed,
// tier/trade_volume captured from the live re-verify). No-op when the accumulator is nil
// (no ledger) or nothing sold.
func (h *RunTourCoordinatorHandler) noteSinkSale(legSells map[string]*tourSinkSale, good string, units int, live *market.TradeGood) {
	if legSells == nil || units <= 0 {
		return
	}
	s := legSells[good]
	if s == nil {
		s = &tourSinkSale{}
		legSells[good] = s
	}
	s.units += units
	if live != nil {
		if a := live.Activity(); a != nil {
			s.tier = *a
		}
		s.tradeVolume = live.TradeVolume()
	}
}

// convertLegShadows converts each sink good sold at this leg into an EXECUTED recovery
// shadow — ONCE per sink with the full realized units (design §2: "per sink as legs
// complete"), so followers (including this hull's own next plan) see the crush and stay
// out until the fitted half-life says it regrew. Untagged sinks / zero-unit sales leave
// none (the ledger's Q2 rule). Best-effort and fail-open: the sale is done, so a ledger
// miss degrades coordination but never reports a failure (the sell floor + live-verify
// are the hard guards). Mirrors the arb container's convert seam, batched per sink.
func (h *RunTourCoordinatorHandler) convertLegShadows(ctx context.Context, cmd *RunTourCoordinatorCommand, waypoint string, legSells map[string]*tourSinkSale) {
	if h.absorptionLedger == nil || cmd.ContainerID == "" || len(legSells) == 0 {
		return
	}
	for good, s := range legSells {
		key := absorption.LaneKey{Waypoint: waypoint, Good: good, Side: absorption.SideSell}
		if err := h.absorptionLedger.ConvertByContainer(ctx, cmd.ContainerID, cmd.PlayerID, key, s.units, s.tier, s.tradeVolume); err != nil {
			common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
				"Tour absorption convert failed for %s at %s/%s (sale completed; coordination degraded, guards intact): %v",
				cmd.ContainerID, waypoint, good, err), nil)
		}
	}
}

// releaseTourReservations drops all of this container's still-PLANNED rows (the
// release-before-(re)plan invariant and the on-exit cleanup). EXECUTED shadows are left by
// the ledger. Best-effort and fail-open: a nil ledger / container-less run is a no-op, and
// a release error is logged (the TTL sweep + dead-container reclaim are the backstop).
func (h *RunTourCoordinatorHandler) releaseTourReservations(ctx context.Context, cmd *RunTourCoordinatorCommand) {
	if h.absorptionLedger == nil || cmd.ContainerID == "" {
		return
	}
	if _, err := h.absorptionLedger.ReleaseByContainer(ctx, cmd.ContainerID, cmd.PlayerID); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Tour absorption release failed for %s (TTL sweep + dead-container reclaim will clean up): %v", cmd.ContainerID, err), nil)
	}
}

// logRecoveryBurden logs projected_recovery_burden (Q3, REPORT-ONLY): the sum over the
// plan's SELL sinks of realized-plannable-units × the fitted recovery half-life (minutes)
// of the sink's tier — the analyst's crowding-exposure proxy. It NEVER steers selection
// (the live shadow-priced objective is gated on offline replay per RULING Q3). Inert when
// the ledger is unwired (L3 not active).
func (h *RunTourCoordinatorHandler) logRecoveryBurden(ctx context.Context, cmd *RunTourCoordinatorCommand, plan *routing.TourPlan, snapshot []routing.TourGoodSnapshot) {
	if h.absorptionLedger == nil {
		return
	}
	type wg struct{ wp, good string }
	tier := make(map[wg]string, len(snapshot))
	for _, s := range snapshot {
		tier[wg{s.Waypoint, s.Good}] = s.Activity
	}
	var burden float64
	perSink := map[string]float64{}
	for _, leg := range plan.Legs {
		for _, tr := range leg.Trades {
			if tr.IsBuy || tr.IsDeposit {
				continue // recovery is a SELL-side (sink-crush) externality only
			}
			hl := h.recoveryHalfLifeMinutes(tier[wg{leg.Waypoint, tr.Good}])
			b := float64(tr.Units) * hl
			burden += b
			perSink[leg.Waypoint+"/"+tr.Good] += b
		}
	}
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf(
		"Tour projected_recovery_burden: %.0f unit-minutes across %d sink(s) (report-only, does not steer selection)", burden, len(perSink)), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "container_id": cmd.ContainerID,
		"projected_recovery_burden": burden, "per_sink": perSink,
	})
}

// recoveryHalfLifeMinutes returns the fitted recovery half-life (minutes) for a sink tier,
// loaded once from the model artifact (report-only; the ledger owns decision-time decay).
// The handler is shared across concurrent tour runs, so the map is loaded under a Once and
// never mutated per-run. A missing/unreadable artifact or an untagged tier yields 0 — the
// burden metric simply reads 0 there (it never gates anything).
func (h *RunTourCoordinatorHandler) recoveryHalfLifeMinutes(tier string) float64 {
	h.recoveryOnce.Do(func() {
		path := h.modelArtifactPath
		if path == "" {
			path = defaultModelArtifactPath
		}
		h.recoveryHalfLives = readRecoveryHalfLives(path)
	})
	return h.recoveryHalfLives[tier]
}

// readRecoveryHalfLives parses the artifact's recovery section into {tier: half_life_min}.
// Any read/parse miss yields an empty map (report-only fail-soft — never an error path).
func readRecoveryHalfLives(path string) map[string]float64 {
	out := map[string]float64{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var art struct {
		Recovery map[string]struct {
			HalfLifeMinutes float64 `json:"half_life_minutes"`
		} `json:"recovery"`
	}
	if err := json.Unmarshal(data, &art); err != nil {
		return out
	}
	for tier, r := range art.Recovery {
		out[tier] = r.HalfLifeMinutes
	}
	return out
}
