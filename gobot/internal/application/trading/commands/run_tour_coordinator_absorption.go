package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
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
// (the previous shape and every test that does not call SetAbsorptionLedger).

const (
	// defaultTourACapTranches is what trade_fleet.acap_tranches falls back to when unset:
	// the fleet-wide reservation ceiling per (market, good, side) is this many trade_volume
	// tranches. The solver A-caps the tranches a single plan takes; the ledger's Reserve
	// makes that cap FLEET-WIDE (the bead's design goal (a)) by rejecting a plan whose
	// tranches + others' outstanding would exceed it. The knob and tour_solver.py's
	// MAX_PLANNED_TRANCHES_PER_MARKET_GOOD_SIDE MUST be raised TOGETHER: a solver widened
	// past this ceiling breaches its own reservation on every plan it produces.
	defaultTourACapTranches = 2
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
	// tourContendedHolderLogCooldown bounds how often the sp-cddfs enriched
	// contended-holder log may fire PER CONTAINER. A persistently-contended lane
	// retries every plan cycle (bounded only by planner RTT — no backoff on this
	// path), so an ungated log here would flood daemon.log exactly when the fleet
	// is busiest contending for depth. One line per container per window is
	// enough to attribute the contention; it is a log-noise safety valve, not an
	// operational lever an operator would retune from observed economy behavior
	// (RULINGS #5 bounds the parametrize-don't-hardcode rule to values like that).
	tourContendedHolderLogCooldown = 60 * time.Second
)

// aCapTranches is this run's fleet-wide sink cap in trade_volume tranches — the ONE
// resolution seam the reserve ceiling, the live-depth re-read, the sell-floor DEPTH rule and
// the cap-binding metric all read, so they can never disagree about it.
func (cmd *RunTourCoordinatorCommand) aCapTranches() int {
	if cmd == nil {
		return defaultTourACapTranches
	}
	return resolveACapTranches(cmd.ACapTranches)
}

// resolveACapTranches floors the knob to the default: unset (0) and any negative a config
// edit could produce must never shrink the cap below what the solver already plans against.
func resolveACapTranches(knob int) int {
	if knob >= 1 {
		return knob
	}
	return defaultTourACapTranches
}

// SetAbsorptionLedger wires the cross-engine absorption ledger (sp-78ai L3) so the tour
// reserves/nets/converts against fleet-wide market depth. plannedTTLSlack pads
// reservation TTLs (0 → default). Left unwired, the tour plans and flies exactly as
// before. Mirrors the sibling SetAbsorptionLedger injections.
func (h *RunTourCoordinatorHandler) SetAbsorptionLedger(ledger absorption.Ledger, plannedTTLSlack time.Duration) {
	h.absorptionLedger = ledger
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
	budget tourPlanBudget,
) (*routing.TourPlan, map[shadowSinkKey]bool, string, bool, error) {
	shipState := h.tourShipState(ship)
	allowedSystems := h.tourSystems(ctx, ship, cmd)

	var lastPlan *routing.TourPlan
	var lastSnapshot []routing.TourGoodSnapshot
	for attempt := 0; attempt <= tourReserveMaxRetries; attempt++ {
		// The tour graph and its market snapshot consult no absorption state, so they are
		// assembled OUTSIDE the planning gates — and the graph they resolve is exactly what
		// the gates key on, since a plan can only reserve sinks at waypoints inside it.
		req, err := h.buildTourPlanRequest(ctx, shipState, allowedSystems, cmd, budget)
		if err != nil {
			return nil, nil, fmt.Sprintf("tour unavailable: planner error: %v", err), false, nil
		}
		res := h.gatedPlanAttempt(ctx, cmd, req, attempt == 0)
		if res.reason != "" {
			return nil, nil, res.reason, false, nil
		}
		lastPlan, lastSnapshot = res.plan, req.snapshot
		if res.reserved {
			// Q3 (REPORT-ONLY): log the recovery burden this accepted plan projects onto
			// the fleet — it must never steer selection (the analyst's experiment bar
			// accumulates from this log; a live shadow-priced objective is gated on
			// offline replay, not switched on here).
			h.logRecoveryBurden(ctx, cmd, res.plan, req.snapshot)
			// Burn-in: score cap-binding on this accepted plan and hand the
			// execution path the ladder-probe set — both derived from the SAME netted
			// depth already read, both pure observation (never gate a trade, RULINGS #4).
			h.recordCapBinding(ctx, cmd, res.plan, req.snapshot, res.absorptionView)
			return res.plan, shadowSinksFromAbsorption(res.absorptionView), "", true, nil
		}
		// Breach (ok=false) or a ledger-gate error (fail-closed for THIS attempt): re-plan
		// against fresh ledger state — the contested sink now shows occupied to the netting.
	}
	// sp-cddfs (OBSERVABILITY ONLY): every retry exhausted against the same request —
	// attribute the refusal to its actual holders before returning the SAME unattributed
	// reason string as before. This never changes what gets reserved, refused, or
	// selected: it is a best-effort read AFTER the real gate already decided, gated by
	// its own per-container cooldown so a persistently-contended lane logs this once per
	// window rather than once per retry.
	h.logContendedHolders(ctx, cmd, lastPlan, lastSnapshot)
	return nil, nil, "tour unavailable: could not reserve tour depth (sinks contended by other containers)", false, nil
}

// tourPlanAttempt is one pass at release → net → solve → reserve. A non-empty reason is a
// TERMINAL refusal already phrased for the caller; otherwise reserved reports whether the
// plan secured its depth, and a false there is the ordinary "sink now occupied" re-plan.
type tourPlanAttempt struct {
	plan           *routing.TourPlan
	absorptionView []routing.TourMarketAbsorption
	reserved       bool
	reason         string
}

// gatedPlanAttempt runs one attempt inside the planning gates of every contention domain
// the request's tour graph touches. Holding them makes release → net → solve → reserve one
// critical section against every planner that could pick the same sink: without it those
// planners all net the SAME pre-reservation snapshot, rank the same sink best and converge
// there, and the release below briefly shows an incumbent's own held sink free to whoever
// is reading right then. Refusing when the gates cannot be taken is the fail-closed
// direction: unserialized depth is not depth we can honestly plan against.
//
// releaseStale drops this container's leftover in-flight intent — a prior tour's holds, or
// pre-restart rows liveness re-adopted — and so runs on the FIRST attempt only, inside the
// gates. EXECUTED recovery shadows are left untouched (real damage still recovering, which
// the plan must avoid). An unwired ledger or a container-less run reserves nothing and
// therefore never queues.
func (h *RunTourCoordinatorHandler) gatedPlanAttempt(
	ctx context.Context,
	cmd *RunTourCoordinatorCommand,
	req *tourPlanRequest,
	releaseStale bool,
) tourPlanAttempt {
	if h.absorptionLedger != nil && cmd.ContainerID != "" {
		releaseGate, gated := h.acquirePlanGate(ctx, cmd.PlayerID, req.systems)
		if !gated {
			return tourPlanAttempt{reason: "tour unavailable: could not serialize plan-time sink reservation (planning blind would co-dump a sink another hull is taking)"}
		}
		defer releaseGate()
	}
	if releaseStale {
		h.releaseTourReservations(ctx, cmd)
	}
	// Assemble the outstanding cross-container absorption the solver nets out of available
	// depth so it plans AROUND sinks other containers occupy. Empty when the ledger is
	// unwired / the consult is killed / the read fails (fail-OPEN — the conditional Reserve
	// is the hard backstop), leaving the plan against full depth.
	absorptionView := h.assembleAbsorption(ctx, cmd.PlayerID, cmd.ContainerID)
	plan, err := h.solveTourPlan(ctx, req, absorptionView)
	if err != nil {
		return tourPlanAttempt{reason: fmt.Sprintf("tour unavailable: planner error: %v", err)}
	}
	if !plan.Feasible {
		return tourPlanAttempt{reason: fmt.Sprintf("tour unavailable: %s", plan.InfeasibleReason)}
	}
	reserved, rerr := h.reserveTourPlan(ctx, cmd, plan, req.snapshot)
	return tourPlanAttempt{plan: plan, absorptionView: absorptionView, reserved: rerr == nil && reserved}
}

// reserveTourPlan reserves the plan's per-(waypoint, good, side) tranches. It is the
// CONDITIONAL, all-or-nothing Reserve: ok=false means a sink breached the fleet-wide
// cap and the caller re-plans; a DB error fails CLOSED for this attempt (RULINGS #4 —
// the money guard never proceeds on an unrunnable gate). A nil ledger or container-less
// run reserves nothing and proceeds.
func (h *RunTourCoordinatorHandler) reserveTourPlan(ctx context.Context, cmd *RunTourCoordinatorCommand, plan *routing.TourPlan, snapshot []routing.TourGoodSnapshot) (bool, error) {
	if h.absorptionLedger == nil || cmd.ContainerID == "" {
		return true, nil
	}
	entries := h.buildTourReserveEntries(cmd, plan, snapshot)
	// sp-pcxju Part 2: a held-cargo sink this container PRESERVED across the re-plan is
	// already firmly reserved — drop it from the fresh reserve so the plan re-USES it
	// instead of stacking a second row (which the cap check might breach, and whose sale
	// would double-count the crush into two recovery shadows). Inert when nothing was
	// preserved (fresh plans), keeping the reserve byte-identical to pre-Part-2.
	entries = h.dropPreservedSinks(ctx, cmd, entries)
	if len(entries) == 0 {
		return true, nil
	}
	logger := common.LoggerFromContext(ctx)

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

// tourContendedHolderLine is one attributed holder in the sp-cddfs enriched refusal log's
// structured "holders" payload — the JSON tags are for the eventual DB-persisted log row
// (logging.FormatFields marshals metadata to JSON), not consumed by any Go reader.
type tourContendedHolderLine struct {
	Waypoint    string `json:"waypoint"`
	Good        string `json:"good"`
	Side        string `json:"side"`
	ContainerID string `json:"container_id"`
	Engine      string `json:"engine"`
	State       string `json:"state"`
	Units       int    `json:"units"`
	TTLSeconds  int    `json:"ttl_remaining_seconds"`
}

// logContendedHolders enriches a "could not reserve tour depth" refusal (sp-cddfs) with
// WHO holds each contended sink — container id, good, sink waypoint, reserved units, and
// TTL/recovery remaining — turning "sinks contended by other containers" into
// attributable data. It re-derives the LAST attempted plan's reserve entries (the exact
// keys reserveTourPlan just tried and failed on) and lists their CURRENT holder rows.
//
// OBSERVABILITY ONLY: this runs strictly AFTER planAndReserve's retry loop has already
// exhausted and decided to refuse — it changes nothing about what gets reserved, refused,
// or selected. Every exit is a silent skip, never an error the caller must handle: a nil
// ledger, a ledger that does not support the optional HolderLister capability, an empty
// plan, a cooldown still in effect, or a read error all leave the caller's existing
// unattributed reason string as the only signal (byte-identical fail-open). Cooldown-gated
// per container so a persistently-contended lane logs this ONCE per
// tourContendedHolderLogCooldown, not once per retry.
func (h *RunTourCoordinatorHandler) logContendedHolders(ctx context.Context, cmd *RunTourCoordinatorCommand, plan *routing.TourPlan, snapshot []routing.TourGoodSnapshot) {
	if h.absorptionLedger == nil || cmd.ContainerID == "" || plan == nil {
		return
	}
	lister, ok := h.absorptionLedger.(absorption.HolderLister)
	if !ok {
		return
	}
	if !h.allowContendedHolderLog(cmd.ContainerID) {
		return
	}

	// Same derivation reserveTourPlan used on this attempt, dropPreservedSinks included —
	// so the keys queried here are EXACTLY what was submitted to Reserve and breached,
	// never a sink this container preserved (and so never even sent to the failed call).
	entries := h.dropPreservedSinks(ctx, cmd, h.buildTourReserveEntries(cmd, plan, snapshot))
	if len(entries) == 0 {
		return
	}
	keys := make([]absorption.LaneKey, 0, len(entries))
	seen := make(map[absorption.LaneKey]bool, len(entries))
	for _, e := range entries {
		k := absorption.LaneKey{Waypoint: e.Waypoint, Good: e.Good, Side: e.Side}
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}

	logger := common.LoggerFromContext(ctx)
	holdersByKey, err := lister.HoldersForKeys(ctx, cmd.PlayerID, keys)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf(
			"Tour depth refusal for %s: holder attribution read failed, reason stands unattributed: %v", cmd.ContainerID, err), map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol, "container_id": cmd.ContainerID,
		})
		return
	}
	if len(holdersByKey) == 0 {
		return // nothing to attribute (the breach may already have cleared by this read)
	}

	var lines []tourContendedHolderLine
	var summary strings.Builder
	for _, k := range keys {
		holders := holdersByKey[k]
		if len(holders) == 0 {
			continue
		}
		// Deterministic order (map iteration is not) so the log line and the
		// structured payload are stable and testable.
		sort.Slice(holders, func(i, j int) bool { return holders[i].ContainerID < holders[j].ContainerID })
		for _, holder := range holders {
			ttlSeconds := int(holder.TTLRemaining.Seconds())
			lines = append(lines, tourContendedHolderLine{
				Waypoint: k.Waypoint, Good: k.Good, Side: k.Side,
				ContainerID: holder.ContainerID, Engine: holder.Engine, State: holder.State,
				Units: holder.Units, TTLSeconds: ttlSeconds,
			})
			if summary.Len() > 0 {
				summary.WriteString("; ")
			}
			fmt.Fprintf(&summary, "%s holds %d %s/%s@%s (%s, %ds remaining)",
				holder.ContainerID, holder.Units, k.Good, k.Side, k.Waypoint, holder.State, ttlSeconds)
		}
	}
	if len(lines) == 0 {
		return
	}
	// NAME the holders in the message TEXT, not just metadata — this file's own
	// discipline elsewhere (run_tour_coordinator.go's starvation-stop / re-plan logs)
	// inlines the concrete cause into the message string alongside the structured
	// payload, so an operator reading `container logs`/daemon.log sees the attribution
	// without needing to parse JSON.
	logger.Log("WARNING", fmt.Sprintf(
		"Tour depth refusal for %s: %d holder(s) blocking %d contended sink(s) - %s",
		cmd.ContainerID, len(lines), len(holdersByKey), summary.String()), map[string]interface{}{
		"ship_symbol": cmd.ShipSymbol, "container_id": cmd.ContainerID, "holders": lines,
	})
}

// allowContendedHolderLog reports whether containerID's enriched contended-holder log may
// fire now, advancing its cooldown clock when it does (sp-cddfs). Mirrors the
// depositParked/strandedStreak per-container de-dup discipline elsewhere in this handler:
// a shared singleton dispatched concurrently for every touring hull, so the map is
// guarded by its own mutex. Lazily inits the map so a handler built by struct literal
// (several tests bypass NewRunTourCoordinatorHandler) never nil-map-panics here.
func (h *RunTourCoordinatorHandler) allowContendedHolderLog(containerID string) bool {
	now := h.clock.Now()
	h.contendedHolderLogMu.Lock()
	defer h.contendedHolderLogMu.Unlock()
	if h.contendedHolderLogAt == nil {
		h.contendedHolderLogAt = make(map[string]time.Time)
	}
	if last, ok := h.contendedHolderLogAt[containerID]; ok && now.Sub(last) < tourContendedHolderLogCooldown {
		return false
	}
	h.contendedHolderLogAt[containerID] = now
	return true
}

// buildTourReserveEntries aggregates a plan's planned units per (waypoint, good, side) —
// skipping DEPOSIT tranches, whose synthetic warehouse sink has no market depth to reserve
// — and sizes each entry: CapUnits = the resolved acap_tranches × trade_volume, the ceiling on OTHER
// containers' outstanding depth on the lane — the plan's own size is irrelevant to its
// admission and never raises the cap (raising it refused every bulk plan on any shadow,
// sp-6zqza); Tier = the sink's live activity tier (so a converted shadow decays on the
// right curve), QuotedPrice = the side's live quote (telemetry), TTL = 2× projected tour
// seconds + slack. The entry order is deterministic (plan/leg/trade order) so reservation
// IDs line up with the plan.
func (h *RunTourCoordinatorHandler) buildTourReserveEntries(cmd *RunTourCoordinatorCommand, plan *routing.TourPlan, snapshot []routing.TourGoodSnapshot) []absorption.ReserveEntry {
	capTranches := cmd.aCapTranches()
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
		quoted := s.Bid
		if k.side == absorption.SideBuy {
			quoted = s.Ask
		}
		entries = append(entries, absorption.ReserveEntry{
			Waypoint:    k.wp,
			Good:        k.good,
			Side:        k.side,
			Units:       units[k],
			CapUnits:    capTranches * s.TradeVolume,
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

// tourGoodDispositions records, per good the plan trades, how that good LEAVES the hull:
// a market sell (a real market sink whose firm reservation gates the buy) and/or a
// haul-to-storage deposit (the warehouse — a guaranteed sink that exempts the buy from
// the gate). A good the plan buys but never disposes of has neither flag set — the
// firm-sink gate then fail-closes it (buying it would strand the cargo).
type tourGoodDispositions struct {
	marketSold map[string]bool
	deposited  map[string]bool
}

// planDispositions folds a plan's SELL-side trades into per-good dispositions once, so the
// per-buy firm-sink gate reads a map instead of re-scanning the plan each tranche.
func planDispositions(plan *routing.TourPlan) tourGoodDispositions {
	d := tourGoodDispositions{marketSold: map[string]bool{}, deposited: map[string]bool{}}
	for _, leg := range plan.Legs {
		for _, tr := range leg.Trades {
			if tr.IsBuy {
				continue
			}
			if tr.IsDeposit {
				d.deposited[tr.Good] = true
				continue
			}
			d.marketSold[tr.Good] = true
		}
	}
	return d
}

// firmSinkUnits sizes a buy of good to the depth THIS hull's OWN downstream sink
// reservation can still absorb (sp-pcxju) — the achievableUnits min-bound shape ported
// to the tour's plan-time-reserve model. It answers "how many units of this good does a
// GUARANTEED, RESERVED sink still back?":
//
//   - -1 ("not gated"): no ledger wired, a container-less run (the tour reserves nothing
//     there either), or a deposit-bound good (the warehouse is a guaranteed sink). The
//     buy proceeds on its existing bounds, byte-identical to before.
//   - 0 ("no firm sink"): a market-sold good whose sink this container no longer holds
//     (saturated by others / dropped on a re-plan), a buy with no sell disposition at all,
//     or an unreadable ledger. The caller buys nothing on spec (fail-closed, RULINGS #4).
//   - a positive value: the firm reserved sell-depth still held, summed across the good's
//     sinks. A freshly-reserved sink holds the full planned units (the ledger admits a plan
//     on others' depth, never its own size), so the gate is a no-op there.
func (h *RunTourCoordinatorHandler) firmSinkUnits(ctx context.Context, cmd *RunTourCoordinatorCommand, good string, disp tourGoodDispositions) int {
	if h.absorptionLedger == nil || cmd.ContainerID == "" {
		return -1
	}
	if !disp.marketSold[good] {
		if disp.deposited[good] {
			return -1 // warehouse sink is guaranteed — exempt from the market-depth gate
		}
		return 0 // bought with no way to sell it — fail-closed
	}
	held, err := h.absorptionLedger.HeldByContainer(ctx, cmd.ContainerID, cmd.PlayerID)
	if err != nil {
		return 0 // the firm-sink guard could not run — fail-closed (never buy blind)
	}
	total := 0
	for key, units := range held {
		if key.Good == good && key.Side == absorption.SideSell {
			// sp-tgll8 item 2 (the "FRESH" clause): the reservation proves the sink is FIRM,
			// but a buy must also target a FRESH, still-DEEP sink. Re-check each held sink's
			// LIVE market at buy time — a stale sink contributes 0 (fail-closed), a shrunk one
			// contributes only its live depth. Inert (contributes the full held units) until
			// SetSinkFreshness arms it, so this is byte-identical to sp-pcxju by default.
			total += h.freshReachableSinkDepth(ctx, cmd.PlayerID, cmd.aCapTranches(), key.Waypoint, good, units)
		}
	}
	return total
}

// freshReachableSinkDepth bounds one held sell-sink's reserved units by its LIVE market at
// buy time (sp-tgll8 item 2 — the "FRESH" clause of the Admiral principle "a buy must target
// a GUARANTEED, RESERVED, REACHABLE, FRESH, sufficiently-DEEP sink"). sp-pcxju already
// enforces FIRM+DEEP off the reservation; this re-observes the sink waypoint's cached
// market_data (the SAME reader observeGood/foreignMarketFresh use — no new reader) and:
//
//   - returns heldUnits unchanged — INERT, byte-identical to sp-pcxju — when no market repo
//     is wired or the freshness clause is unarmed (SetSinkFreshness never called);
//   - returns 0 (FAIL-CLOSED) when the sink's market is unreadable/gone, no longer trades the
//     good, or is STALE past the freshness threshold — the "fresh" guarantee cannot be
//     confirmed, so the gate never buys on spec (RULINGS #4);
//   - returns min(heldUnits, live absorbable depth) when fresh — shrinking to the live depth
//     (capTranches × live trade_volume, the SAME cap formula buildTourReserveEntries
//     uses) if the sink shrank since planning, a no-op for a still-deep sink.
//
// A zero LastUpdated is "unknown age" and treated as FRESH, matching foreignMarketFresh /
// freshListings / tour_snapshot — the one place the codebase fails OPEN on missing recency.
func (h *RunTourCoordinatorHandler) freshReachableSinkDepth(ctx context.Context, playerID, capTranches int, waypoint, good string, heldUnits int) int {
	if h.marketRepo == nil || h.sinkFreshnessMaxAge <= 0 {
		return heldUnits // freshness clause inert — byte-identical to sp-pcxju
	}
	mkt, err := h.marketRepo.GetMarketData(ctx, waypoint, playerID)
	if err != nil || mkt == nil {
		// The gate logs a generic "no firm sink" at the call site; surface the REAL reason
		// here so a fail-closed refusal is never a silent/misattributed guard.
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Tour firm-sink freshness: sink market at %s unreadable for %s - failing closed (never buy into a sink we cannot confirm is live)", waypoint, good), nil)
		return 0 // cannot confirm the sink is fresh — fail-closed (never buy blind)
	}
	sink := mkt.FindGood(good)
	if sink == nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Tour firm-sink freshness: sink %s no longer trades %s - failing closed", waypoint, good), nil)
		return 0 // the sink no longer trades the good — fail-closed
	}
	// The cap is DERIVED from the live scan rotation, not a minute count written into
	// the source (sp-k4z5b): the scan budget is fixed, so a market's interval is an
	// output of budget ÷ markets known, and a flat threshold silently invalidates as
	// the map grows. Refusing at the rotation bound means this guard fails closed
	// exactly when the SCANNER has failed its own guarantee, never merely because a
	// row is waiting its turn in a bigger map.
	maxAge := h.sinkMaxAge(ctx, playerID)
	if observed := mkt.LastUpdated(); !observed.IsZero() && h.clock.Now().Sub(observed) > maxAge {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Tour firm-sink freshness: sink %s/%s market_data is %s stale (> %s cap, rotation bound %s) - failing closed (the FRESH guarantee failed, sp-tgll8)",
			waypoint, good, h.clock.Now().Sub(observed).Truncate(time.Second), maxAge,
			h.freshness.RotationBound(ctx).Truncate(time.Second)), nil)
		return 0 // stale market_data — the fresh guarantee failed, fail-closed
	}
	liveDepth := resolveACapTranches(capTranches) * sink.TradeVolume()
	if liveDepth < heldUnits {
		return liveDepth // sink depth shrank since planning — shrink to what it can now absorb
	}
	return heldUnits
}

// assembleAbsorption reads the player's outstanding cross-container absorption (PLANNED
// units + EXECUTED shadows already decayed Go-side by the ledger) and shapes it for the
// planner to net. It fails OPEN on a read error (returns nil → plan against full depth):
// the conditional Reserve re-checks the fleet-wide cap in-transaction, so it is the hard
// backstop and a transient netting miss cannot slip an un-capped co-dump. Inert when the
// ledger is unwired.
func (h *RunTourCoordinatorHandler) assembleAbsorption(ctx context.Context, playerID int, containerID string) []routing.TourMarketAbsorption {
	if h.absorptionLedger == nil {
		return nil
	}
	pools, err := h.absorptionLedger.Outstanding(ctx, playerID)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING",
			fmt.Sprintf("Tour absorption consult: ledger read failed, planning against full depth (Reserve remains the hard cap): %v", err), nil)
		return nil
	}
	// Net THIS container's own still-PLANNED depth out of its own plan request (sp-pcxju
	// Part 2): with held-cargo sinks now PRESERVED across a re-plan, the tour must plan
	// INTO its own reserved sink (sell the held cargo there), not treat its own hold as
	// depth to route around — a sink filled to its cap would otherwise self-net to
	// infeasible and strand the cargo. OTHERS' PLANNED depth and every EXECUTED recovery
	// shadow (real damage, even the hull's own) still net in. Byte-identical pre-Part-2:
	// the release-before-replan left this container no own PLANNED rows to subtract.
	own := h.ownPlannedUnits(ctx, containerID, playerID)
	out := make([]routing.TourMarketAbsorption, 0, len(pools))
	for key, occ := range pools {
		planned := occ.PlannedUnits - own[key]
		if planned < 0 {
			planned = 0
		}
		if planned == 0 && occ.RecoveringResidual == 0 {
			continue
		}
		out = append(out, routing.TourMarketAbsorption{
			Waypoint:        key.Waypoint,
			Good:            key.Good,
			Side:            key.Side,
			PlannedUnits:    planned,
			RecoveringUnits: occ.RecoveringResidual,
		})
	}
	return out
}

// ownPlannedUnits reads this container's own still-PLANNED depth per key for the
// assembleAbsorption own-subtraction (sp-pcxju Part 2). Fails OPEN (nil): a container-less
// run or an unreadable read subtracts nothing, so the plan nets against full depth exactly
// as before — the conditional Reserve's cap check remains the hard backstop.
func (h *RunTourCoordinatorHandler) ownPlannedUnits(ctx context.Context, containerID string, playerID int) map[absorption.LaneKey]int {
	if containerID == "" {
		return nil
	}
	own, err := h.absorptionLedger.HeldByContainer(ctx, containerID, playerID)
	if err != nil {
		return nil
	}
	return own
}

// tourMarketFlow accumulates one good's realized units across a leg's price-tiered
// tranches on ONE side of the market, plus the live tier + trade_volume the re-verify
// read (stable across the good's tranches), so the leg-end conversion sizes the shadow
// on the FULL move.
type tourMarketFlow struct {
	units       int
	tier        string
	tradeVolume int
}

// tourFlowKey is a leg's accumulator key. It carries the SIDE as well as the good because
// one leg may both source and sink the same good, and the two occupy different pools.
type tourFlowKey struct {
	good string
	side string
}

// newLegFlows allocates a per-leg accumulator, or nil when no ledger is wired (the
// accumulation and conversion then no-op — the tour flies exactly as before). The
// map is allocated whenever a ledger is present, regardless of the consult switch:
// recording (and therefore converting) still runs in the escape-hatch mode.
func (h *RunTourCoordinatorHandler) newLegFlows() map[tourFlowKey]*tourMarketFlow {
	if h.absorptionLedger == nil {
		return nil
	}
	return map[tourFlowKey]*tourMarketFlow{}
}

// noteMarketFlow folds one executed tranche into its pool's accumulator (units summed,
// tier/trade_volume captured from the live re-verify). No-op when the accumulator is nil
// (no ledger) or nothing moved.
func (h *RunTourCoordinatorHandler) noteMarketFlow(
	legFlows map[tourFlowKey]*tourMarketFlow, side, good string, units int, live *market.TradeGood,
) {
	if legFlows == nil || units <= 0 {
		return
	}
	k := tourFlowKey{good: good, side: side}
	s := legFlows[k]
	if s == nil {
		s = &tourMarketFlow{}
		legFlows[k] = s
	}
	s.units += units
	if live != nil {
		if a := live.Activity(); a != nil {
			s.tier = *a
		}
		s.tradeVolume = live.TradeVolume()
	}
}

// convertLegShadows converts each pool this leg moved through into an EXECUTED recovery
// shadow — ONCE per pool with the full realized units (design §2: "per sink as legs
// complete"), so followers (including this hull's own next plan) see the move and stay
// out until the fitted curve says it regrew. Untagged markets / zero-unit trades leave
// none (the ledger's Q2 rule). Best-effort and fail-open: the trade is done, so a ledger
// miss degrades coordination but never reports a failure (the sell floor, the buy ceiling
// and the live-verify are the hard guards). A failed convert leaves the row PLANNED, which
// still occupies the pool's depth in full — the fail-closed direction.
//
// BOTH SIDES convert. Sell-only left the fleet's purchases no memory past the flight that
// made them, so every hull's next plan read a just-drained source as untouched ground.
func (h *RunTourCoordinatorHandler) convertLegShadows(
	ctx context.Context, cmd *RunTourCoordinatorCommand, waypoint string, legFlows map[tourFlowKey]*tourMarketFlow,
) {
	if h.absorptionLedger == nil || cmd.ContainerID == "" || len(legFlows) == 0 {
		return
	}
	for k, s := range legFlows {
		key := absorption.LaneKey{Waypoint: waypoint, Good: k.good, Side: k.side}
		if err := h.absorptionLedger.ConvertByContainer(ctx, cmd.ContainerID, cmd.PlayerID, key, s.units, s.tier, s.tradeVolume); err != nil {
			common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
				"Tour absorption convert failed for %s at %s/%s %s (trade completed; coordination degraded, guards intact): %v",
				cmd.ContainerID, waypoint, k.good, k.side, err), nil)
		}
	}
}

// releaseTourReservations drops this container's still-PLANNED rows on a (re)plan and on
// exit — but PRESERVES the sink-side rows backing cargo currently in the hold (sp-pcxju
// Part 2), so a laden hull's re-plan cannot drop the sink under already-bought cargo and
// let another engine crush it before the hull sells. Everything else (stale buy-side holds,
// sinks for goods not yet bought) still drops, so the fresh plan nets against OTHERS' depth.
// EXECUTED shadows are left by the ledger. Best-effort and fail-open: a nil ledger /
// container-less run is a no-op; an unreadable hold degrades to release-all (pre-Part-2);
// a release error is logged (the TTL sweep + dead-container reclaim are the backstop).
func (h *RunTourCoordinatorHandler) releaseTourReservations(ctx context.Context, cmd *RunTourCoordinatorCommand) {
	if h.absorptionLedger == nil || cmd.ContainerID == "" {
		return
	}
	keep := h.heldCargoSinkKeys(ctx, cmd)
	if _, err := h.absorptionLedger.ReleaseByContainerExcept(ctx, cmd.ContainerID, cmd.PlayerID, keep); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Tour absorption release failed for %s (TTL sweep + dead-container reclaim will clean up): %v", cmd.ContainerID, err), nil)
	}
}

// heldCargoSinkKeys returns this container's still-PLANNED SELL-side keys whose good is
// currently in the hull's hold — the sinks releaseTourReservations must preserve so
// already-bought cargo keeps its guaranteed sell depth (sp-pcxju). It loads the ship fresh
// (both the re-plan release and the on-exit release run through here, and the on-exit hold
// differs from the start), and fails OPEN: an unreadable ship or ledger returns nil, so the
// caller releases everything exactly as before Part 2 (the safe direction — a hold we cannot
// confirm is left to the fresh plan rather than pinned).
func (h *RunTourCoordinatorHandler) heldCargoSinkKeys(ctx context.Context, cmd *RunTourCoordinatorCommand) []absorption.LaneKey {
	ship, err := h.legs.loadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil
	}
	cargo := ship.Cargo()
	if cargo == nil {
		return nil
	}
	held, err := h.absorptionLedger.HeldByContainer(ctx, cmd.ContainerID, cmd.PlayerID)
	if err != nil {
		return nil
	}
	var keep []absorption.LaneKey
	for key := range held {
		if key.Side == absorption.SideSell && cargo.GetItemUnits(key.Good) > 0 {
			keep = append(keep, key)
		}
	}
	return keep
}

// dropPreservedSinks removes from a plan's reserve entries any SELL-side sink this
// container already holds a PLANNED row for — the preserved held-cargo sinks (sp-pcxju
// Part 2). Re-reserving them would double the row (a breach risk) and double the recovery
// shadow on sale. Fails OPEN: an unreadable ledger keeps every entry (the conditional
// Reserve's cap check is the backstop). A no-preserved-rows container drops nothing, so
// fresh plans reserve exactly as before Part 2.
func (h *RunTourCoordinatorHandler) dropPreservedSinks(ctx context.Context, cmd *RunTourCoordinatorCommand, entries []absorption.ReserveEntry) []absorption.ReserveEntry {
	held, err := h.absorptionLedger.HeldByContainer(ctx, cmd.ContainerID, cmd.PlayerID)
	if err != nil || len(held) == 0 {
		return entries
	}
	kept := make([]absorption.ReserveEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Side == absorption.SideSell {
			if _, preserved := held[absorption.LaneKey{Waypoint: entry.Waypoint, Good: entry.Good, Side: entry.Side}]; preserved {
				continue
			}
		}
		kept = append(kept, entry)
	}
	return kept
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
