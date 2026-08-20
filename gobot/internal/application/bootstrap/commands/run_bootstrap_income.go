package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// tradeFleetTag is the dedicated-fleet tag the standing trade-fleet coordinator selects on (matches the
// trading package's tradeFleet). The cold-start hull-routing trade-seed buys ONE hull and dedicates
// it to this fleet so acquisition #2 becomes a trade hull, decoupled from the contract op — the trade
// coordinator, contract coordinator, and contract scaler stay phase-BLIND (all the phase logic lives here).
const tradeFleetTag = "trade"

// The STARVED-TRADE CONTRACT FALLBACK's three bounds (sp-bvf20). A cold-start economy exhausts its few
// profitable lanes fast and pre-gate the hull cannot reposition out, so a starved frigate would otherwise
// idle-retry a dead system for hours. Internal classification thresholds, not operational knobs: each is
// pinned to the trade-fleet coordinator's own relaunch ladder, and that interlock is the point.
const (
	// frigateStarvedTourMax is the fast-fail line: a tour shorter than this never really traded. It
	// mirrors minProductiveTourDuration — the same classification driving the coordinator's cooldown
	// escalation, which is exactly the exit this fallback answers.
	frigateStarvedTourMax = 90 * time.Second

	// frigateStarvedDwell is a brief settle buffer before contracts may have the frigate, an order of
	// magnitude UNDER the coordinator's 180s base cooldown: above it, trade wins every first starved exit
	// and the hull idles instead. Whether it is FREE is the release's idle+cargo-empty guard, not this.
	frigateStarvedDwell = 15 * time.Second

	// frigateContractFallbackWindow is how long the released frigate stays untagged and visible to the
	// contract pool, for BOTH shapes. It LAPSES so a fallback nobody took hands the hull back to trade
	// rather than stranding it, closing inside the coordinator's 600s backoff ceiling.
	frigateContractFallbackWindow = 300 * time.Second
)

// actIncome runs the EARNING workstream: the frigate trades from tick 1, and the contract operation
// starts once the treasury clears the contract-start threshold. Independently-guarded, idempotent
// actions on the observed delta, ordered so the fleet earns from tick 1 and never deadlocks:
//
//  1. Keep the command frigate EARNING IN TRADE (its standing home) once probes reach target, and the
//     trade coordinator up regardless.
//  2. Hand the purchasing frigate back to trade once its cold-start buys have landed.
//  3. Run the contract fleet coordinator — from tick 1, because the ENGINE costs nothing and is inert
//     until something offers it a hull (below the threshold there are no haulers, and the frigate is
//     trade-tagged, so its pool is empty by construction).
//  4. Hand the frigate to that engine when trade is locally STARVED — the whole point of running it
//     early. Zero-capital, so it belongs above the gate.
//  5. THE CONTRACT-START GATE, now CAPITAL only: below the threshold nothing contract-side is BOUGHT.
//     The gate defers WHEN the operation spends and never withdraws work already running (RULINGS #1).
//  6. Stop a LEGACY frigate contract loop left by an earlier deploy, at a cargo-empty safe point.
//  7. Hand a STRANDED purchasing frigate back to trade, so it earns and re-pivots later.
//  8. Staged, capital-gated hull acquisition, ROUTED BY ORDER: #1 → the contract fleet, #2 → the TRADE
//     fleet (held until the first contract hull exists), #3… → contract again. The ramp climbs to the
//     fixed Phase-1 hauler target, one hull per tick, each on a distinct delivery slot; the COUNT guard
//     (haulers < haulerTarget) is the double-buy protection. The trade hull does not count toward the
//     scaler's ceiling, and ALL the phase logic lives HERE — the coordinators stay phase-blind.
//
// Each action is guarded "already done / in-flight?" against the FRESH observation, so re-evaluation —
// including the first tick after a restart — never double-acts or double-buys.
func (h *RunBootstrapCoordinatorHandler) actIncome(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult) {
	// ONE yard reading serves the whole tick: the frigate's home (1) and the contract fallback (4) both
	// weigh whether its buy is in reach, and two readings could disagree about the same hull.
	ask := h.buyShipAsk(ctx, cmd, obs)

	// (1) The frigate EARNS IN TRADE once probes reach target, ahead of the graduation stop — trade is not
	// contract income, so a graduated fleet follows the same order.
	h.ensureFrigateTrading(ctx, cmd, obs, res, ask)
	h.ensureTradeCoordinator(ctx, cmd)

	// CONTRACT GRADUATION: a graduated player has DURABLY retired contracts as the funding
	// floor (the operator's manual, era-scoped decision). The rest of this workstream is contract-income
	// — the contract coordinator, the staged hauler buys — so when graduated it does
	// NOTHING: bootstrap never (re)starts or maintains a contract earner, even after a boot-standing
	// relaunch. Fail-OPEN (obs.ContractGraduated defaults false on a fresh era / read miss) ⇒ byte-identical
	// to today. Scanning (actData) and gate construction (actGate) run regardless — this gates ONLY
	// contract income, and never touches trade.
	if obs.ContractGraduated {
		res.Blocker = "contract_graduated"
		common.LoggerFromContext(ctx).Log("INFO", "Bootstrap contract workstream OFF: player is contract-graduated — not starting/maintaining any contract earner (durable, sp-difa.1); scanning + gate construction + trade unaffected", map[string]interface{}{
			"action":       "bootstrap_contract_graduated",
			"container_id": cmd.ContainerID,
		})
		return
	}

	// (2) The tick CONTINUES past the hand-back: freeing a tag conflicts with nothing below it (the
	// acquisition steps that read the purchasing dedication all require ZERO haulers, which this
	// step's own guard excludes), and a hand-back that could not land must never hold the ramp.
	h.releaseFinishedPurchaser(ctx, cmd, obs, res)

	// (3) The contract ENGINE, from tick 1 and ABOVE the capital gate. Launching it buys nothing; it
	// only makes the operation available to pick up idle capacity, and with no haulers owned and the
	// frigate trade-tagged its candidate pool is empty, so an early launch is a genuine no-op until
	// step (4) offers it the starved frigate. Idempotent via the running observation.
	if !obs.BatchContractRunning {
		h.ensureBatchContract(ctx, cmd, res)
	}

	// (4) STARVED TRADE (or a CAPITAL-GATED buy) → CONTRACT WORK. Also zero-capital (it writes one
	// dedication), so it sits above the gate: a cold-start fleet is exactly the one that needs it.
	h.releaseStarvedFrigateToContract(ctx, cmd, cfg, obs, res, ask)

	// (5) THE CONTRACT-START GATE — NEW SPEND only: once the operation is under way, a treasury dip
	// (including the one its own hull buys cause) never stands it back down.
	if !contractOpsWarranted(obs, cfg.ContractStartTreasury) {
		res.Blocker = "contract_start_deferred"
		common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Bootstrap contract CAPITAL deferred: treasury=%d below the contract-start threshold=%d (flat, not netted) — the contract engine runs (it spends nothing) and the frigate falls back to it when trade starves, but no hull is bought or scaled this tick", obs.Treasury, cfg.ContractStartTreasury), map[string]interface{}{
			"action":       "bootstrap_contract_start_deferred",
			"container_id": cmd.ContainerID,
			"treasury":     obs.Treasury,
			"threshold":    cfg.ContractStartTreasury,
		})
		return
	}

	// (6) A frigate contract loop from an earlier deploy still holds the hull; stop it once so it
	// returns to trade (never mid-delivery — that would abandon accepted contract cargo).
	if obs.FrigateContractLoopRunning && obs.CommandFrigateID != "" && obs.FrigateCargoEmpty {
		h.stopLegacyFrigateLoop(ctx, cmd, obs, res)
	}

	// (7) Hand a STRANDED purchasing frigate back to trade, so it earns again and re-pivots later.
	if h.releaseStrandedPurchaser(ctx, cmd, obs, res) {
		return
	}

	// (8) Staged hull acquisition. The target is the FIXED Phase-1 hauler count — a constant of the plan,
	// never a per-tick reading of markets or of whatever contract is live, so the ramp climbs to one size
	// and stays there instead of resizing under it. The count guard is the double-buy protection; the
	// era's fixed delivery slots decide only WHERE each hull lands.
	res.PlacementSlots = len(obs.ContractPlacementSlots)
	desired := haulerTarget
	contractHaulers := len(obs.Haulers)

	// (8a) HULL-ROUTING: route cold-start light-hull acquisitions by order — #1 →
	// contract, #2 → TRADE, #3… → contract. Once the FIRST contract hull exists and no trade hull does yet,
	// seed ONE trade-dedicated hull + ensure the trade coordinator, then RETURN so THIS tick's acquisition
	// is the trade seed, not a 2nd contract hauler. The trade hull EXISTING (obs.TradeHullCount) is the
	// durable, observable "seeded" signal — idempotent by construction, re-derived each tick, restart-safe
	// (no stored flag). The trade hull is decoupled from the contract op: it does NOT count toward the
	// contract scaler's ceiling, and the trade/contract coordinators + scaler stay phase-BLIND.
	if contractHaulers >= 1 && obs.TradeHullCount == 0 {
		h.maybeSeedTradeHull(ctx, cmd, obs, res)
		return
	}

	// (8b) Staged contract-hauler acquisition — HELD at 1 contract hull until the trade hull is seeded
	// (the (contractHaulers == 0 || TradeHullCount >= 1) guard), so acquisition #2 is the trade seed above:
	// contractHaulers==0 still buys contract #1 unchanged; once a trade hull exists contract buying resumes
	// for #3…, capped at the fixed hauler target.
	if contractHaulers < desired && (contractHaulers == 0 || obs.TradeHullCount >= 1) {
		h.maybeBuyHauler(ctx, cmd, obs, res)
	}
}

// contractOpsUnderway reports whether the contract operation has already SPENT, read entirely from the
// live world: it owns a hauler, or the frigate is committed mid-purchase for it. This LATCHES the
// threshold — without it the pivot's own hull buy, which legitimately spends the treasury back under the
// bar, would read as "not started yet" next tick and stand the operation down mid-flight (RULINGS #1).
// Nothing is stored, so a restart re-derives the same answer (RULINGS #2).
//
// A RUNNING coordinator is deliberately NOT evidence. It was, while the launch itself sat behind the
// threshold; now the engine is boot-standing (actIncome step 3), so "running" says nothing about capital
// and counting it would hold the gate open from tick 1 at zero treasury. Both remaining terms are real
// spend that already happened, which is the only thing the latch was ever protecting (RULINGS #4).
func contractOpsUnderway(obs Observation) bool {
	return len(obs.Haulers) > 0 || obs.CommandFrigatePurchasing
}

// frigateTradeTourStarved reports whether the frigate's LAST run came back starved — too short to have
// traded — using the same fast-fail line the trade-fleet coordinator scores its own relaunch backoff on.
// Derived per tick from the two persisted assignment timestamps, so a restart re-derives it (RULINGS #2).
// No run on record is no evidence, and no evidence is not starvation.
func frigateTradeTourStarved(obs Observation) bool {
	if obs.CommandFrigateLastRunStart.IsZero() || obs.CommandFrigateLastRunEnd.IsZero() {
		return false
	}
	return obs.CommandFrigateLastRunEnd.Sub(obs.CommandFrigateLastRunStart) < frigateStarvedTourMax
}

// frigateContractFallbackOpen reports whether the starved frigate's contract-fallback window is open
// right now: it came back from a starved run AND has settled past the dwell. Both bootstrap steps that
// touch the tag read this ONE predicate, which is what makes them incapable of fighting each other —
// the release fires only while it is open, the trade re-dedication holds off only while it is open —
// and it is why the dwell can be short without the two steps racing to swap the tag.
func frigateContractFallbackOpen(obs Observation, now time.Time) bool {
	if !frigateTradeTourStarved(obs) {
		return false
	}
	parked := now.Sub(obs.CommandFrigateLastRunEnd)
	return parked >= frigateStarvedDwell && parked < frigateStarvedDwell+frigateContractFallbackWindow
}

// frigateBuyShipWanted reports whether bootstrap still needs the frigate STANDING BY as its exclusive
// buy ship: exactly one acquisition names it by symbol and has no other buyer — the trade seed (step
// 8a). Read off the live fleet (RULINGS #2), and OFF for a graduated player, who buys no cold-start
// hull at all and whose frigate must never be pinned to a buy that will not happen.
func frigateBuyShipWanted(obs Observation) bool {
	return obs.CommandFrigateID != "" && !obs.ContractGraduated && len(obs.Haulers) > 0 && obs.TradeHullCount == 0
}

// frigateBuyShipStalled reports the PURCHASING half of the contract fallback: a buy ship is wanted, its
// buy is out of reach, and the hull is free and empty past the same dwell the trade half opens on (a
// frigate with no run on record has been off the roster since the era began, so it is past by default).
//
// This is the SHAPE, and it does not lapse: a hull pinned to a buy it cannot make is stalled at every park
// age, so the release below is never gated by the clock and no tag nothing can use becomes permanent. How
// long it then WAITS untagged is frigateBuyShipFallbackOpen's bounded question. The capital test is READ,
// never moved: pivotWouldHold is the seed's own cushion≥floor gate, so this decides only WHERE it waits.
func frigateBuyShipStalled(obs Observation, ask int64, now time.Time) bool {
	if !frigateBuyShipWanted(obs) || !obs.CommandFrigateIdle || !obs.FrigateCargoEmpty {
		return false
	}
	if !pivotWouldHold(obs.Treasury, ask) {
		return false
	}
	return now.Sub(obs.CommandFrigateLastRunEnd) >= frigateStarvedDwell
}

// frigateBuyShipFallbackOpen is the purchasing half's frigateContractFallbackOpen — the stalled buy ship's
// offer to the contract pool, on the SAME two bounds. Closing on capital alone deadlocks when the contract
// engine's own work is blocked by the same empty treasury: nothing earns, so capital never recovers and no
// leg ever claims the hull. So it LAPSES — to TRADE (ensureFrigateTrading's default), income gated on
// neither the buy nor that contract, never back to the yard, which is the state that stalled.
func frigateBuyShipFallbackOpen(obs Observation, ask int64, now time.Time) bool {
	if !frigateBuyShipStalled(obs, ask, now) {
		return false
	}
	return now.Sub(obs.CommandFrigateLastRunEnd) < frigateStarvedDwell+frigateContractFallbackWindow
}

// buyShipAsk reads the yard ONCE per tick and only where the answer can change a decision. Read-only:
// it spends nothing and gates nothing. 0 is no reading, which every consumer treats as no evidence.
func (h *RunBootstrapCoordinatorHandler) buyShipAsk(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation) int64 {
	if h.haulAcquirer == nil || !frigateBuyShipWanted(obs) || !obs.CommandFrigateIdle || obs.CommandFrigateOnTrade {
		return 0
	}
	ask, _, _, _ := h.haulAcquirer.PriceCheck(ctx, cmd.PlayerID, haulerShipType)
	return ask
}

// releaseStarvedFrigateToContract hands the frigate to the contract engine when it has nothing to earn
// where it stands, by CLEARING its dedication and nothing else. That one write is the whole mechanism:
// the existing last-resort admission (ship_pool_manager.go, RULINGS #7) takes the command hull only when
// it carries NO dedication and no regular hauler is idle, so an untagged frigate is picked up on the
// engine's own next pass under its own unmodified rules. This is not a second contract path — it only
// makes the hull visible to the one that exists.
//
// TWO starvation shapes funnel through it, and the hull is equally idle in both: trade here is locally
// starved, or the cold-start buy it was dedicated to make is capital-gated.
//
// It fires at an honest free tick only: idle, not flying (never mid-tour, never mid-navigation to the
// yard, PLAYBOOK §9) and cargo-empty. Idle is also what makes an in-flight purchase safe — a purchase
// container CLAIMS its buy ship, so a running transaction never reads free. Writing a dedication
// commands and stops nothing, so no accepted contract, tour or committed buy is abandoned (RULINGS #1).
func (h *RunBootstrapCoordinatorHandler) releaseStarvedFrigateToContract(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, cfg bootstrapRunConfig, obs Observation, res *reconcileResult, ask int64) {
	if obs.CommandFrigateID == "" || !obs.CommandFrigateIdle || !obs.FrigateCargoEmpty {
		return
	}
	now := h.clock.Now()
	starved := obs.CommandFrigateOnTrade && frigateContractFallbackOpen(obs, now)
	stalled := obs.CommandFrigatePurchasing && frigateBuyShipStalled(obs, ask, now)
	if !starved && !stalled {
		return
	}

	// Capital ops outrank one opportunistic leg: while the operation is funded and still hull-less the
	// frigate is the first-hauler pivot's purchaser, so leave it where the pivot expects to find it. Still
	// scoped to a HULL-LESS op on purpose: once hauler #1 exists the pivot is over, and a hull-less stall
	// keeps its own cure (releaseStrandedPurchaser hands the frigate back to TRADE to earn toward the ask).
	if contractOpsWarranted(obs, cfg.ContractStartTreasury) && len(obs.Haulers) == 0 {
		return
	}

	logger := common.LoggerFromContext(ctx)
	if h.retirer == nil {
		res.Blocker = "no_retirer"
		logger.Log("WARN", "Bootstrap needs to release the idle command frigate to contract work but no retirer wired — it would keep standing by with nothing to earn", map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_retirer",
		})
		return
	}
	if err := h.retirer.RetireFromContract(ctx, cmd.PlayerID, obs.CommandFrigateID); err != nil {
		res.Blocker = "frigate_fallback_release_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap could not clear the command frigate %s's dedication — it stays where it is earning nothing; retry next tick: %v", obs.CommandFrigateID, err), map[string]interface{}{
			"action":       "bootstrap_frigate_fallback_release_error",
			"container_id": cmd.ContainerID,
			"ship":         obs.CommandFrigateID,
		})
		return
	}
	res.FrigateContractFallback = true
	parked := now.Sub(obs.CommandFrigateLastRunEnd)
	why := fmt.Sprintf("the cold-start buy it stands by for is out of reach (hauler ask=%d treasury=%d cushion=(treasury−ask)=%d floor=%d) — it returns to being the buy ship the moment that clears, or goes TRADING if nothing takes the offer before it lapses", ask, obs.Treasury, obs.Treasury-ask, contractWorkingCapitalFloor)
	if starved {
		why = fmt.Sprintf("its last tour ran %s (under the %s fast-fail line — it never traded), so trade here is starved — it returns to trade when the leg ends or in %s if nothing takes it",
			obs.CommandFrigateLastRunEnd.Sub(obs.CommandFrigateLastRunStart).Truncate(time.Second),
			frigateStarvedTourMax,
			(frigateStarvedDwell + frigateContractFallbackWindow - parked).Truncate(time.Second))
	}
	logger.Log("INFO", fmt.Sprintf("Bootstrap released the command frigate %s to CONTRACT work after %s off the earning roster: %s. Untagged, it is now visible to the contract coordinator's last-resort admission",
		obs.CommandFrigateID, parked.Truncate(time.Second), why), map[string]interface{}{
		"action":         "bootstrap_frigate_contract_fallback",
		"container_id":   cmd.ContainerID,
		"ship":           obs.CommandFrigateID,
		"reason":         fallbackReason(starved),
		"last_tour_secs": int(obs.CommandFrigateLastRunEnd.Sub(obs.CommandFrigateLastRunStart).Seconds()),
		"parked_secs":    int(parked.Seconds()),
		"hauler_ask":     ask,
		"treasury":       obs.Treasury,
	})
}

// fallbackReason names which starvation shape opened the fallback, for the decision log.
func fallbackReason(starved bool) string {
	if starved {
		return "trade_starved"
	}
	return "buy_ship_capital_gated"
}

// committedBuyShip names the frigate only while it carries the purchasing dedication — the hull an
// earlier tick committed, and so the only one a cold yard may be answered by sending (awaitHaulerPrice's
// own rule). Untagged or trading it is not this decision's to fly.
func committedBuyShip(obs Observation) string {
	if obs.CommandFrigatePurchasing && obs.CommandFrigateID != "" {
		return obs.CommandFrigateID
	}
	return ""
}

// contractOpsWarranted reports whether the contract operation may act this tick. The treasury comparison
// is FLAT — deliberately not netted against any reserve floor, a different reading from the GATE-entry
// surplus bar (gateFunded). SEQUENCING only, never a spend guard: every buy downstream still passes the
// untouched working-capital floor (RULINGS #4).
func contractOpsWarranted(obs Observation, threshold int64) bool {
	return obs.Treasury >= threshold || contractOpsUnderway(obs)
}

// frigateIdleInTrade reports the frigate's honest free tick: in the trade fleet, holding no claim, not
// still flying. It is the same partition the trade-fleet coordinator draws for relaunch candidates, so
// bootstrap takes the hull exactly where that coordinator considers it free — never mid-tour (PLAYBOOK §9).
func frigateIdleInTrade(obs Observation) bool {
	return obs.CommandFrigateOnTrade && obs.CommandFrigateIdle
}

// ensureFrigateTrading gives a free, untagged command frigate a home again — normally the TRADE fleet, so
// it earns under the same coordinator that tours every other trade hull (no second, hand-rolled earner
// loop, and the one write also clears whatever tag it carried). It waits for a genuinely free tick and
// never touches the committed purchaser, so no accepted contract and no in-flight purchase is abandoned
// (RULINGS #1).
//
// It is also where a hull the contract fallback lent out comes BACK to, and the two homes are not
// interchangeable: while the acquisition that names the frigate by symbol is still unbought, the hull
// belongs at the yard as its buy ship, not out on a multi-minute tour a coordinator claims it for.
//
// SEQUENCED BEHIND THE PROBE SEED. The trade coordinator CLAIMS the hull it tours, so a dedicated frigate
// is idle at no tick for the length of a multi-minute leg — and with the one probe claimed by its own tour
// the fleet then has no idle hull at all, the presence-gated yard can never be warmed, and probe buying
// deadlocks for good. Below target the frigate is left alone, so the yard errand actData runs alongside
// this has a hull. Only a NEW dedication is deferred; an already-trading frigate returned above (RULINGS #1).
func (h *RunBootstrapCoordinatorHandler) ensureFrigateTrading(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult, ask int64) {
	if obs.CommandFrigateID == "" || obs.CommandFrigateOnTrade || obs.CommandFrigatePurchasing || !obs.CommandFrigateIdle {
		return
	}
	logger := common.LoggerFromContext(ctx)

	if obs.ProbeCount < probeTarget {
		logger.Log("INFO", fmt.Sprintf("Bootstrap frigate trade dedication DEFERRED: probes=%d/%d — leaving the command frigate %s undedicated and idle so the probe buy can send it to warm the home shipyard; it takes up trade once the seed completes", obs.ProbeCount, probeTarget, obs.CommandFrigateID), map[string]interface{}{
			"action":       "bootstrap_frigate_trade_deferred",
			"container_id": cmd.ContainerID,
			"ship":         obs.CommandFrigateID,
			"probes":       obs.ProbeCount,
			"probe_target": probeTarget,
		})
		return
	}

	// HOLD while EITHER fallback window is open. The frigate was untagged ON PURPOSE — an undedicated
	// command hull is the only shape the last-resort admission accepts — so re-tagging it here would take
	// it back before the contract coordinator's own next pass ran. Each hold is its release's shape plus
	// the window it stays offered for; both LAPSE, so an offer nobody took ends in a home, not in limbo.
	now := h.clock.Now()
	if frigateContractFallbackOpen(obs, now) || frigateBuyShipFallbackOpen(obs, ask, now) {
		logger.Log("INFO", fmt.Sprintf("Bootstrap holding the command frigate %s UNDEDICATED: it has nothing to earn where it stands and is offered to the contract coordinator's last-resort pool; it takes a home again when that leg ends, when the window lapses, or when its buy comes back within reach", obs.CommandFrigateID), map[string]interface{}{
			"action":       "bootstrap_frigate_trade_held_for_contract",
			"container_id": cmd.ContainerID,
			"ship":         obs.CommandFrigateID,
		})
		return
	}

	if h.retirer == nil {
		res.Blocker = "no_retirer"
		logger.Log("WARN", "Bootstrap needs to give the command frigate a fleet but no retirer wired — it would sit idle instead of earning", map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_retirer",
		})
		return
	}

	// BACK TO THE YARD, not out on a tour: the acquisition naming this hull is still unbought and in
	// reach, so it resumes the post the fallback lent it away from. A laden hull is excluded (trade sells
	// its hold), and so is one still OUT of reach — the same capital test the stall opens on, so a lapsed
	// offer can never re-enter it here; it falls through to trade below.
	if frigateBuyShipWanted(obs) && obs.FrigateCargoEmpty && !pivotWouldHold(obs.Treasury, ask) {
		h.restoreFrigateBuyShip(ctx, cmd, obs, res, ask)
		return
	}

	if err := h.retirer.DedicateAsTrade(ctx, cmd.PlayerID, obs.CommandFrigateID); err != nil {
		res.Blocker = "frigate_trade_dedicate_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap could not dedicate the command frigate %s to the trade fleet — retry next tick: %v", obs.CommandFrigateID, err), map[string]interface{}{
			"action":       "bootstrap_frigate_trade_dedicate_error",
			"container_id": cmd.ContainerID,
			"ship":         obs.CommandFrigateID,
		})
		return
	}
	res.FrigateTrading = true
	logger.Log("INFO", fmt.Sprintf("Bootstrap dedicated the command frigate %s to the TRADE fleet — its standing home, now that the probe seed is complete (%d/%d): it tours continuously under the trade-fleet coordinator, and the write also clears any stale contract tag", obs.CommandFrigateID, obs.ProbeCount, probeTarget), map[string]interface{}{
		"action":       "bootstrap_frigate_trading",
		"container_id": cmd.ContainerID,
		"ship":         obs.CommandFrigateID,
	})
}

// restoreFrigateBuyShip puts the exclusive PURCHASING dedication back on a returning untagged frigate —
// the same protected role the pivot writes, through the same single write path. That dedication is what
// keeps the hull reserved at the yard instead of claimed for a tour, which is what makes the next buy
// deterministic (RULINGS #7). It commands nothing and spends nothing.
func (h *RunBootstrapCoordinatorHandler) restoreFrigateBuyShip(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult, ask int64) {
	logger := common.LoggerFromContext(ctx)

	if err := h.retirer.DedicateAsPurchaser(ctx, cmd.PlayerID, obs.CommandFrigateID); err != nil {
		res.Blocker = "frigate_dedicate_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap could not restore the command frigate %s as the exclusive purchasing ship — retry next tick: %v", obs.CommandFrigateID, err), map[string]interface{}{
			"action":       "bootstrap_frigate_buy_ship_restore_error",
			"container_id": cmd.ContainerID,
			"ship":         obs.CommandFrigateID,
		})
		return
	}
	res.FrigateBuyShipRestored = true
	logger.Log("INFO", fmt.Sprintf("Bootstrap restored the command frigate %s as the EXCLUSIVE purchasing ship: the cold-start trade seed is still unbought and its ask is back within reach (ask=%d treasury=%d floor=%d), and this hull is the buy's only named buyer — it stands by at the yard rather than touring, and goes back to trade the moment the seed lands", obs.CommandFrigateID, ask, obs.Treasury, contractWorkingCapitalFloor), map[string]interface{}{
		"action":       "bootstrap_frigate_buy_ship_restored",
		"container_id": cmd.ContainerID,
		"ship":         obs.CommandFrigateID,
		"hauler_ask":   ask,
		"treasury":     obs.Treasury,
	})
}

// ensureTradeCoordinator ensures the standing trade-fleet coordinator during cold start, so the trading
// frigate (and, later, the seeded trade hull) is actually toured rather than left idle with a tag. It runs
// from tick 1, AHEAD of the dedication it serves: it selects strictly on the "trade" tag, so untagged it is
// a zero-API no-op claiming no hull, and being already up is what tours the frigate the moment it lands.
// IDEMPOTENCY lives in the LAUNCHER (it skips a RUNNING/PENDING coordinator), so a per-tick call never
// double-launches. Nil-safe, and a BACKGROUND launch that never claims res.Blocker — a coordinator that
// could not start must not mask why a BUY could not — mirroring ensureContractScalerEarly.
func (h *RunBootstrapCoordinatorHandler) ensureTradeCoordinator(ctx context.Context, cmd *RunBootstrapCoordinatorCommand) {
	logger := common.LoggerFromContext(ctx)

	if h.handoff == nil {
		logger.Log("WARN", "Bootstrap has no hand-off launcher wired — cannot ensure the trade-fleet coordinator, so the trading frigate would idle", map[string]interface{}{
			"action":       "bootstrap_no_handoff_launcher",
			"container_id": cmd.ContainerID,
		})
		return
	}
	if err := h.handoff.LaunchTradeFleetCoordinator(ctx, cmd.PlayerID, cmd.AgentSymbol); err != nil {
		logger.Log("ERROR", fmt.Sprintf("Bootstrap failed to ensure the trade-fleet coordinator (the trading frigate idles until it is up): %v", err), map[string]interface{}{
			"action":       "bootstrap_trade_coordinator_launch_error",
			"container_id": cmd.ContainerID,
		})
	}
}

// releaseFinishedPurchaser hands the command frigate back to the TRADE fleet once the cold-start buys it
// was freed for have landed — a contract hull AND the trade seed both exist, so it has nothing left to
// buy. Without this it stays dedicated "purchasing" forever: idle, off every earning roster, and refused
// by any operation that tries to claim it. A STANDING step read from the live world, not a one-shot tail,
// which is what makes it idempotent, restart-safe, and effective on a fleet that is already stuck.
func (h *RunBootstrapCoordinatorHandler) releaseFinishedPurchaser(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
	if !obs.CommandFrigatePurchasing || obs.CommandFrigateID == "" || len(obs.Haulers) == 0 || obs.TradeHullCount == 0 {
		return
	}
	h.releasePurchaserToTrade(ctx, cmd, obs.CommandFrigateID, res, "its cold-start buys are complete")
}

// releasePurchaserToTrade performs the hand-back and reports whether it happened, so every caller writes
// one tag through one path and reports one tally.
func (h *RunBootstrapCoordinatorHandler) releasePurchaserToTrade(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, frigate string, res *reconcileResult, why string) bool {
	logger := common.LoggerFromContext(ctx)

	if h.retirer == nil {
		res.Blocker = "no_retirer"
		logger.Log("WARN", "Bootstrap needs to release the purchasing frigate back to trade but no retirer wired", map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_retirer",
		})
		return false
	}
	if err := h.retirer.DedicateAsTrade(ctx, cmd.PlayerID, frigate); err != nil {
		res.Blocker = "purchaser_release_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap could not hand the purchasing command frigate %s back to the trade fleet — it stays the buy ship with nothing to buy; retry next tick: %v", frigate, err), map[string]interface{}{
			"action":       "bootstrap_purchaser_release_error",
			"container_id": cmd.ContainerID,
			"ship":         frigate,
		})
		return false
	}
	res.PurchaserReleased = true
	logger.Log("INFO", fmt.Sprintf("Bootstrap RELEASED the purchasing command frigate %s back to the TRADE fleet (%s) — it resumes continuous tours instead of standing by idle-dedicated", frigate, why), map[string]interface{}{
		"action":       "bootstrap_purchaser_released_to_trade",
		"container_id": cmd.ContainerID,
		"ship":         frigate,
		"reason":       why,
	})
	return true
}

// stopLegacyFrigateLoop clears a frigate contract-loop container left running by an earlier deploy: an
// infinite loop never ends on its own, so it would hold the hull's claim forever and the frigate could
// never take up trade. The caller has checked it is running and the frigate is CARGO-EMPTY, so no
// accepted contract's cargo is abandoned (RULINGS #1). Idempotent via the loop-running observation.
func (h *RunBootstrapCoordinatorHandler) stopLegacyFrigateLoop(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if h.frigateLoop == nil {
		logger.Log("WARN", "Bootstrap observed a legacy frigate contract loop but no loop starter wired to stop it — the frigate stays claimed by it", map[string]interface{}{
			"action":       "bootstrap_legacy_frigate_loop_unstoppable",
			"container_id": cmd.ContainerID,
			"ship":         obs.CommandFrigateID,
		})
		return
	}
	if err := h.frigateLoop.StopLoop(ctx, cmd.PlayerID, obs.CommandFrigateID); err != nil {
		res.Blocker = "frigate_loop_stop_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap could not stop the legacy contract loop on the command frigate %s — retry next tick: %v", obs.CommandFrigateID, err), map[string]interface{}{
			"action":       "bootstrap_frigate_loop_stop_error",
			"container_id": cmd.ContainerID,
			"ship":         obs.CommandFrigateID,
		})
		return
	}
	res.FrigateLoopStopped = true
	logger.Log("INFO", fmt.Sprintf("Bootstrap STOPPED the legacy contract loop on the command frigate %s at a cargo-empty safe point — the frigate earns in the trade fleet now, not on a loop of its own", obs.CommandFrigateID), map[string]interface{}{
		"action":       "bootstrap_frigate_loop_stopped",
		"container_id": cmd.ContainerID,
		"ship":         obs.CommandFrigateID,
	})
}

// pivotWouldHold reports whether the first-hauler pivot's capital hold applies to `ask`: committing the
// earner to a buy that would leave the contract op under its working-capital floor strands it, because a
// frigate taken out of trade earns nothing to make up the difference. A 0 ask is the absence of any yard
// reading — no evidence, and no evidence is no reason to hold. It decides only WHEN the earner is
// committed; the floor it reads is the same cushion≥floor test the buy itself gates on, untouched.
func pivotWouldHold(treasury, ask int64) bool {
	return ask > 0 && treasury-ask < contractWorkingCapitalFloor
}

// releaseStrandedPurchaser hands the command frigate back to TRADE when the pivot has left it holding a
// purchase it cannot make, and reports whether it did. The pivot takes the frigate out of the trade fleet
// and dedicates it the exclusive buy ship on the evidence that hauler #1 is within reach; when the ask
// then sits outside the working-capital floor the frigate is neither buying nor earning — the treasury is
// frozen at whatever it held, so the ask can never come within reach. Returning it to the trade fleet is
// the whole action: it tours and earns again, and re-pivots by itself once the treasury clears the floor.
//
// It fires only on that exact shape — no hauler bought yet, the frigate carrying the purchasing
// dedication, and an ask that the SAME hold the pivot weighs rejects. Release and pivot therefore read one
// decision on one treasury and one ask, and cannot ping-pong: the release fires only where the pivot would
// hold, and a frigate freed BY the pivot is by construction one the release leaves alone.
//
// Nav state is deliberately not consulted. Writing a dedication commands nothing and abandons nothing,
// which is why the pivot alone guards on empty cargo. So a frigate still flying to the yard for the price
// read is released on the same terms as one parked at it: that flight is the longest-lived form of the
// strand, and its only purpose is to price a hull this very decision has established the treasury cannot
// cover.
//
// Nothing here spends, and the working-capital floor is read, never moved (RULINGS #4).
func (h *RunBootstrapCoordinatorHandler) releaseStrandedPurchaser(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) bool {
	if len(obs.Haulers) > 0 || !obs.CommandFrigatePurchasing || obs.CommandFrigateID == "" {
		return false
	}
	if h.haulAcquirer == nil || h.retirer == nil {
		return false
	}

	// The ask is live while the yard prices and the last one on record while it is cold, so readability
	// changes how fresh the evidence is, never which decision it supports.
	ask, _, _, _ := h.haulAcquirer.PriceCheck(ctx, cmd.PlayerID, haulerShipType)
	if !pivotWouldHold(obs.Treasury, ask) {
		return false
	}
	return h.releasePurchaserToTrade(ctx, cmd, obs.CommandFrigateID, res,
		fmt.Sprintf("the buy it was freed for is out of reach: hauler ask=%d treasury=%d cushion=(treasury−ask)=%d floor=%d", ask, obs.Treasury, obs.Treasury-ask, contractWorkingCapitalFloor))
}

// ensureBatchContract launches the contract fleet coordinator (workflow batch-contract) so the fleet
// earns. The caller has checked it is not already running, so this is the idempotent launch.
func (h *RunBootstrapCoordinatorHandler) ensureBatchContract(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if h.contractRun == nil {
		res.Blocker = "no_contract_runner"
		logger.Log("WARN", "Bootstrap needs to run batch-contract but no contract runner wired", map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_contract_runner",
		})
		return
	}
	if err := h.contractRun.StartBatchContract(ctx, cmd.PlayerID); err != nil {
		res.Blocker = "batch_contract_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap batch-contract launch failed: %v", err), map[string]interface{}{
			"action":       "bootstrap_batch_contract_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.ContractRun = true
	logger.Log("INFO", "Bootstrap launched batch-contract on the contract fleet — the fleet now earns while the hauler ramp stages", map[string]interface{}{
		"action":       "bootstrap_ran_batch_contract",
		"container_id": cmd.ContainerID,
	})
}

// maybeBuyHauler evaluates and (unless dry-run) executes ONE staged hauler buy behind the readiness
// and capital gates, placing it on the first fixed delivery slot no hauler yet serves. It emits the
// same guardrail arithmetic as the probe buy (RULINGS #4, fail closed). Caller has checked "needed"
// (haulers < haulerTarget).
func (h *RunBootstrapCoordinatorHandler) maybeBuyHauler(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	// Placement: the first fixed slot (within the cap) no hauler already serves. Empty means the era's
	// parks are unresolved, or the home system has fewer distinct parks than the target and all of them
	// are served — either way there is nowhere to spread another hull, so fail closed and retry.
	slot := firstUnservedSlot(obs.ContractPlacementSlots, obs.Haulers, haulerTarget)
	if slot == "" {
		res.Blocker = "no_placement_slot"
		logger.Log("WARN", fmt.Sprintf("Bootstrap hauler needed (%d/%d haulers) but no free delivery slot to place it on (%d slot(s) resolved this era) — no placement target", len(obs.Haulers), haulerTarget, len(obs.ContractPlacementSlots)), map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_placement_slot",
		})
		return
	}

	if h.haulAcquirer == nil {
		res.Blocker = "no_hauler_acquirer"
		logger.Log("WARN", "Bootstrap hauler needed but no hauler acquirer wired — cannot price-check or buy", map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_hauler_acquirer",
		})
		return
	}

	// Readiness / FIRST-HAULER PIVOT decision. Determined BEFORE the price-check so a genuine
	// no-purchaser blocks cheaply without a shipyard read.
	//
	// The FIRST hauler is bought by taking the command frigate out of the trade fleet to serve as THE
	// purchaser (the documented first-hauler pivot) — it happens regardless of whether a stray hull is
	// idle, so the exclusive purchasing ship is always established. The pivot fires ONLY for the
	// cash-flow-critical FIRST hauler and only at a SAFE point:
	//   - len(Haulers)==0 (the first hauler only — a later hauler buys off an incidentally-idle hull),
	//   - the frigate is IDLE IN TRADE (frigateIdleInTrade) and resolved — an honest inter-tour tick, so
	//     no tour is ever interrupted (PLAYBOOK §9), and
	//   - the frigate carries NO cargo (FrigateCargoEmpty) — a hull holding goods sells them on its next
	//     tour first, so a loaded frigate defers the pivot a tick.
	// The actual DEDICATION is deferred until AFTER the capital gate below, so the frigate only ever
	// leaves the trade fleet once the buy is affordable AND warranted. When NOT pivoting, an existing idle
	// hull buys as before; with neither an idle hull nor a pivot available, BLOCK (no_purchaser) and retry.
	pivot := len(obs.Haulers) == 0 && frigateIdleInTrade(obs) && obs.CommandFrigateID != "" && obs.FrigateCargoEmpty
	// committedPurchaser: a prior tick already FREED + DEDICATED the frigate as the exclusive buy ship
	// (fault-2 pivot), so it is THE purchaser even while it is still navigating to the shipyard for
	// the cold-price read — recognise it here so an en-route tick surfaces "positioning", not no_purchaser.
	committedPurchaser := len(obs.Haulers) == 0 && obs.CommandFrigatePurchasing && obs.CommandFrigateID != ""
	if !pivot && !committedPurchaser && !obs.HasIdlePurchaser {
		res.Blocker = "no_purchaser"
		logger.Log("WARN", fmt.Sprintf("Bootstrap hauler needed (%d/%d, slot %s) but BLOCKED: no idle hull to execute the purchase and the first-hauler pivot is unavailable (haulers=%d frigate_on_trade=%v frigate_idle=%v cargo_empty=%v) — retry next tick", len(obs.Haulers), haulerTarget, slot, len(obs.Haulers), obs.CommandFrigateOnTrade, obs.CommandFrigateIdle, obs.FrigateCargoEmpty), map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_purchaser",
		})
		return
	}

	// Price-check (reuse shipyard list — a live, PRESENCE-GATED GetShipyard). Unreadable ⇒ the capital gate
	// cannot be evaluated, so nothing is bought this tick (RULINGS #4 fail-closed).
	price, yard, readable, err := h.haulAcquirer.PriceCheck(ctx, cmd.PlayerID, haulerShipType)
	if err != nil || !readable {
		// On cold start nothing is standing at the home shipyard — the frigate is out on a tour, the probes
		// are scouting — so the presence-gated read prices nothing. `price` is then the last ask the yard gave,
		// the only evidence available for deciding whether to commit the earner to a trip to the yard. The
		// working-capital floor stays HERE and is evaluated on the real price once it reads.
		h.awaitHaulerPrice(ctx, cmd, obs, res, pivot, price, err)
		return
	}

	// Capital gate: buy as soon as the treasury AFTER the buy still clears the ABSOLUTE
	// contract working-capital floor — affordable ⇔ cushion=(treasury−price) ≥ contract_working_capital_floor.
	// The hauler exists to SCALE cash flow, so it is bought as soon as the buy leaves a safe goods+fuel
	// operating cushion (PLAYBOOK §3) — an absolute floor, not a fraction of a growing balance. The probe
	// buy gates on the same shape against common.ImmutableReserveFloor (see run_bootstrap_reconcile.go).
	// RULINGS #4 fail-closed: an unreadable price already returned above, and a cushion below the floor
	// does NOT buy — so after a permitted buy treasury ≥ floor by construction (the working-capital safety).
	cushion := obs.Treasury - price
	affordable := cushion >= contractWorkingCapitalFloor
	floorNote := "clears the working-capital floor"
	if !affordable {
		floorNote = "BLOCKED by the working-capital floor (treasury−price below the contract working-capital floor)"
	}
	logger.Log("INFO", fmt.Sprintf("Bootstrap hauler buy decision: price=%d treasury=%d floor=%d cushion=(treasury−price)=%d affordable=(cushion≥floor)=%v slot=%s yard=%s — %s", price, obs.Treasury, contractWorkingCapitalFloor, cushion, affordable, slot, yard, floorNote), map[string]interface{}{
		"action":       "bootstrap_hauler_buy_decision",
		"container_id": cmd.ContainerID,
		"price":        price,
		"treasury":     obs.Treasury,
		"floor":        contractWorkingCapitalFloor,
		"cushion":      cushion,
		"affordable":   affordable,
		"slot":         slot,
		"yard":         yard,
	})
	if !affordable {
		res.Blocker = "capital_gate"
		return
	}

	// Execute the pivot BEFORE the buy: dedicate the idle-in-trade frigate the exclusive purchasing ship.
	// Nothing is stopped — an idle-in-trade hull holds no claim, and the trade coordinator only ever
	// relaunches hulls still carrying the trade tag, so the re-tag alone hands the frigate over.
	// Dedicating BEFORE the buy keeps the invariant "the frigate is protected before it is used or left
	// idle": a later buy failure leaves an idle, purchasing-dedicated frigate, which the next tick simply
	// reuses as the purchaser. A dedicate failure returns (retry next tick).
	if pivot {
		if h.retirer == nil {
			res.Blocker = "no_retirer"
			logger.Log("WARN", "Bootstrap first-hauler pivot: no retirer wired to dedicate the purchasing ship", map[string]interface{}{
				"action":       "bootstrap_income_blocked",
				"container_id": cmd.ContainerID,
				"blocker":      "no_retirer",
			})
			return
		}
		if err := h.retirer.DedicateAsPurchaser(ctx, cmd.PlayerID, obs.CommandFrigateID); err != nil {
			res.Blocker = "frigate_dedicate_error"
			logger.Log("ERROR", fmt.Sprintf("Bootstrap first-hauler pivot: dedicating the command frigate %s as the exclusive purchasing ship failed — retry next tick: %v", obs.CommandFrigateID, err), map[string]interface{}{
				"action":       "bootstrap_frigate_pivot_dedicate_error",
				"container_id": cmd.ContainerID,
				"ship":         obs.CommandFrigateID,
			})
			return
		}
		res.FrigatePivoted = true
		logger.Log("INFO", fmt.Sprintf("Bootstrap first-hauler PIVOT: took the idle-in-trade command frigate %s out of the trade fleet and dedicated it the EXCLUSIVE purchasing ship (protected, never re-drafted) — it buys hauler #1 and the trade seed, then goes straight back to trading", obs.CommandFrigateID), map[string]interface{}{
			"action":       "bootstrap_frigate_pivot",
			"container_id": cmd.ContainerID,
			"ship":         obs.CommandFrigateID,
		})
	}

	// The purchaser is the command frigate whenever the pivot owns it — freed THIS tick (pivot) or already
	// freed+dedicated on a prior tick and now positioned at the yard (committedPurchaser). Otherwise "" lets
	// the acquirer use any idle hull (the direct-buy behavior for a subsequent hauler bootstrap buys itself).
	purchaser := ""
	if pivot || committedPurchaser {
		purchaser = obs.CommandFrigateID
	}
	bought, err := h.haulAcquirer.BuyAndPlace(ctx, cmd.PlayerID, haulerShipType, yard, slot, purchaser)
	if err != nil {
		res.Blocker = "purchase_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap hauler purchase failed: %v", err), map[string]interface{}{
			"action":       "bootstrap_hauler_buy_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.HaulersBought++
	if h.metrics != nil {
		h.metrics.RecordHaulerPurchased()
	}
	logger.Log("INFO", fmt.Sprintf("Bootstrap bought contract hauler %s at %s for %d, dedicated + placed on delivery slot %s (%d/%d haulers, %d slots)", bought.ShipSymbol, yard, bought.Price, slot, len(obs.Haulers)+1, haulerTarget, res.PlacementSlots), map[string]interface{}{
		"action":       "bootstrap_bought_hauler",
		"container_id": cmd.ContainerID,
		"ship":         bought.ShipSymbol,
		"price":        bought.Price,
		"slot":         slot,
	})
}

// maybeSeedTradeHull seeds acquisition #2 to the TRADE fleet (hull-routing). The caller has
// checked the routing gate (one contract hull exists, no trade hull yet — obs.TradeHullCount==0, the durable
// observable "seeded" signal). It buys ONE hull, dedicates it to the trade fleet (NO hub — a trade hull runs
// continuous tours the coordinator assigns, not a fixed contract hub), then ensures the standing trade-fleet
// coordinator so the hull is put on a tour.
//
// MONEY GUARD (RULINGS #4): it carries the SAME price-check + working-capital gate as maybeBuyHauler because
// the trade hull occupies acquisition-slot #2 — the slot that as a contract hauler is capital-gated at
// contract_working_capital_floor — so re-routing #2 to trade must NOT weaken the guard on that spend (an
// unreadable price or a short cushion buys nothing this tick). No first-hauler pivot / cold-price dance is
// needed: by this point the command frigate is already the exclusive purchasing ship (established freeing it
// for contract #1) and idle at the yard, so it is the deterministic purchaser. Both collaborators are required
// UP FRONT (never buy a trade hull we cannot then manage) — a nil acquirer/launcher is a logged skip surfaced
// as a blocker (like the maybeBuyHauler nil guards), never a panic.
func (h *RunBootstrapCoordinatorHandler) maybeSeedTradeHull(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult) {
	logger := common.LoggerFromContext(ctx)

	if h.haulAcquirer == nil {
		res.Blocker = "no_hauler_acquirer"
		logger.Log("WARN", "Bootstrap trade-seed needed but no hauler acquirer wired — cannot price-check or buy", map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_hauler_acquirer",
		})
		return
	}
	// Require the launcher BEFORE buying — never strand a purchased trade hull with no coordinator to run it.
	if h.handoff == nil {
		res.Blocker = "no_handoff_launcher"
		logger.Log("WARN", "Bootstrap trade-seed needed but no hand-off launcher wired — cannot ensure the trade coordinator, so not buying a trade hull we could not then manage", map[string]interface{}{
			"action":       "bootstrap_income_blocked",
			"container_id": cmd.ContainerID,
			"blocker":      "no_handoff_launcher",
		})
		return
	}

	// Price-check (reuse shipyard list — a live, PRESENCE-GATED GetShipyard). Unreadable ⇒ the capital gate
	// cannot be evaluated, so nothing is bought this tick (RULINGS #4 fail-closed). The buy ship normally
	// stands at the yard from the contract-#1 buy, but the contract fallback can lend it out and a leg
	// leaves it wherever it ended, so a cold yard is answered as the hauler buy answers one: SEND it.
	price, yard, readable, err := h.haulAcquirer.PriceCheck(ctx, cmd.PlayerID, haulerShipType)
	if err != nil || !readable {
		h.awaitReadablePrice(ctx, cmd, obs, res, committedBuyShip(obs), "", "trade-seed", err)
		return
	}

	// Capital gate: affordable ⇔ cushion=(treasury−price) ≥ contract_working_capital_floor — the SAME
	// floor the contract-hauler buy this slot displaces uses, so re-routing #2 to trade never weakens the money
	// guard (RULINGS #4/#5). A cushion below the floor does NOT buy.
	cushion := obs.Treasury - price
	affordable := cushion >= contractWorkingCapitalFloor
	floorNote := "clears the working-capital floor"
	if !affordable {
		floorNote = "BLOCKED by the working-capital floor (treasury−price below the contract working-capital floor)"
	}
	logger.Log("INFO", fmt.Sprintf("Bootstrap trade-seed buy decision: price=%d treasury=%d floor=%d cushion=(treasury−price)=%d affordable=(cushion≥floor)=%v yard=%s — %s", price, obs.Treasury, contractWorkingCapitalFloor, cushion, affordable, yard, floorNote), map[string]interface{}{
		"action":       "bootstrap_trade_seed_buy_decision",
		"container_id": cmd.ContainerID,
		"price":        price,
		"treasury":     obs.Treasury,
		"floor":        contractWorkingCapitalFloor,
		"cushion":      cushion,
		"affordable":   affordable,
		"yard":         yard,
	})
	if !affordable {
		res.Blocker = "capital_gate"
		return
	}

	// Buy + dedicate to the trade fleet (no hub). The purchaser is the command frigate — the exclusive
	// purchasing ship established at the first-hauler pivot — so the buy is deterministic, not dependent on an
	// incidentally-idle hull.
	bought, err := h.haulAcquirer.BuyAndDedicate(ctx, cmd.PlayerID, haulerShipType, yard, tradeFleetTag, obs.CommandFrigateID)
	if err != nil {
		res.Blocker = "trade_seed_purchase_error"
		logger.Log("ERROR", fmt.Sprintf("Bootstrap trade-seed purchase failed: %v", err), map[string]interface{}{
			"action":       "bootstrap_trade_seed_buy_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	res.TradeHullSeeded = true
	logger.Log("INFO", fmt.Sprintf("Bootstrap seeding the 2nd cold-start hull to the TRADE fleet (hull-routing) + ensuring the trade coordinator: bought %s at %s for %d, dedicated %q (does NOT count toward the contract scaler ceiling)", bought.ShipSymbol, yard, bought.Price, tradeFleetTag), map[string]interface{}{
		"action":       "bootstrap_seeded_trade_hull",
		"container_id": cmd.ContainerID,
		"ship":         bought.ShipSymbol,
		"price":        bought.Price,
	})

	// Ensure the standing trade-fleet coordinator so the seeded hull is put on a continuous tour. Idempotent
	// (skips a running coordinator). Best-effort BACKGROUND launch: it surfaces a failure on its own ERROR line
	// and never claims res.Blocker (the seed itself succeeded), mirroring the early autosizer/contract-scaler
	// launches.
	if err := h.handoff.LaunchTradeFleetCoordinator(ctx, cmd.PlayerID, cmd.AgentSymbol); err != nil {
		logger.Log("ERROR", fmt.Sprintf("Bootstrap trade-seed: launching the trade-fleet coordinator failed (the seeded hull will idle until it is launched): %v", err), map[string]interface{}{
			"action":       "bootstrap_trade_coordinator_launch_error",
			"container_id": cmd.ContainerID,
		})
		return
	}
	logger.Log("INFO", "Bootstrap ensured the trade-fleet coordinator (trade-seed) — the seeded trade hull is now managed on continuous tours", map[string]interface{}{
		"action":       "bootstrap_trade_coordinator_ensured",
		"container_id": cmd.ContainerID,
	})

	// The seed was the frigate's LAST cold-start buy, so hand it straight back to trade. The standing
	// release step covers the same ground next tick; doing it here just saves the hull a tick idle.
	if obs.CommandFrigatePurchasing && obs.CommandFrigateID != "" {
		h.releasePurchaserToTrade(ctx, cmd, obs.CommandFrigateID, res, "its cold-start buys are complete (contract hauler #1 + the trade seed)")
	}
}

// awaitHaulerPrice answers a cold yard on the first-hauler buy. The buy needs a live LIGHT_SHUTTLE
// price, but a yard prices its hulls only while a ship is standing at it and on cold start nothing is
// — the frigate is out on a tour, the probes are scouting. The only hull that can warm the yard is the
// earner, so this decides whether to commit it: TAKE it at an idle-in-trade SAFE POINT
// (DedicateAsPurchaser — command duty per RULINGS #7; cargo-empty so nothing it is carrying is lost) and
// send it, or leave it trading. Either way the tick buys nothing (RULINGS #4): warming the yard makes
// bootstrap's own pre-buy floor guard evaluable on a real price, it does not bypass it.
//
// lastAsk is the last price the yard gave for a hauler, 0 when it has never given one.
func (h *RunBootstrapCoordinatorHandler) awaitHaulerPrice(ctx context.Context, cmd *RunBootstrapCoordinatorCommand, obs Observation, res *reconcileResult, pivot bool, lastAsk int64, priceErr error) {
	logger := common.LoggerFromContext(ctx)

	// Freeing the frigate is only worth it if something can then send it. With no scanner wired, fail
	// closed without stopping it — an earning loop is never halted for a trip that cannot be made.
	if h.scanner == nil {
		h.awaitReadablePrice(ctx, cmd, obs, res, "", "", "hauler", priceErr)
		return
	}

	// The committed purchaser: a frigate an earlier tick already freed and dedicated, so it goes to the
	// yard on later unreadable ticks without being freed again.
	purchaser := ""
	if obs.CommandFrigatePurchasing && obs.CommandFrigateID != "" {
		purchaser = obs.CommandFrigateID
	}

	if pivot {
		// NEVER free the earner while the hauler is out of reach. Stopping the only ship that earns to
		// go buy something the treasury cannot cover leaves nothing earning, so the treasury never
		// reaches the price and the frigate waits at the yard forever. The yard is cold, so weigh the
		// last ask it gave against the SAME cushion≥floor test the readable path applies (RULINGS #4/#5)
		// — the money guard is untouched, this only decides WHEN the frigate is freed. No ask means no
		// evidence, and no evidence is no reason to hold.
		if pivotWouldHold(obs.Treasury, lastAsk) {
			res.Blocker = "capital_gate"
			logger.Log("INFO", fmt.Sprintf("Bootstrap first-hauler pivot HELD (not taking the earner): last hauler ask=%d treasury=%d cushion=(treasury−price)=%d floor=%d — keeping the command frigate %s TRADING until the treasury clears the working-capital floor", lastAsk, obs.Treasury, obs.Treasury-lastAsk, contractWorkingCapitalFloor, obs.CommandFrigateID), map[string]interface{}{
				"action":       "bootstrap_pivot_held_unaffordable",
				"container_id": cmd.ContainerID,
				"blocker":      "capital_gate",
				"last_ask":     lastAsk,
				"treasury":     obs.Treasury,
				"floor":        contractWorkingCapitalFloor,
				"ship":         obs.CommandFrigateID,
			})
			return
		}
		if h.retirer == nil {
			res.Blocker = "price_unreadable"
			logger.Log("WARN", "Bootstrap hauler price unreadable and no retirer wired to free the command frigate — failing closed (no buy)", map[string]interface{}{
				"action":       "bootstrap_price_blocked",
				"container_id": cmd.ContainerID,
				"blocker":      "price_unreadable",
			})
			return
		}
		if err := h.retirer.DedicateAsPurchaser(ctx, cmd.PlayerID, obs.CommandFrigateID); err != nil {
			res.Blocker = "frigate_dedicate_error"
			logger.Log("ERROR", fmt.Sprintf("Bootstrap first-hauler pivot: dedicating the command frigate %s as the exclusive purchasing ship failed — retry next tick: %v", obs.CommandFrigateID, err), map[string]interface{}{
				"action":       "bootstrap_frigate_pivot_dedicate_error",
				"container_id": cmd.ContainerID,
				"ship":         obs.CommandFrigateID,
			})
			return
		}
		res.FrigatePivoted = true
		purchaser = obs.CommandFrigateID
		logger.Log("INFO", fmt.Sprintf("Bootstrap first-hauler PIVOT: took the command frigate %s at an idle-in-trade tick (dedicated the exclusive purchasing ship) — sending it to the home shipyard so the hauler price reads next tick", obs.CommandFrigateID), map[string]interface{}{
			"action":       "bootstrap_frigate_pivot",
			"container_id": cmd.ContainerID,
			"ship":         obs.CommandFrigateID,
		})
	}

	// With no purchaser to name — a subsequent buy resting on an incidentally-idle probe that is not at
	// the yard — the scanner picks a free hull, and is lent nothing: committing the earner is the PIVOT's
	// decision above, taken against the capital test, never a side effect here.
	h.awaitReadablePrice(ctx, cmd, obs, res, purchaser, "", "hauler", priceErr)
}

// firstUnservedSlot returns the first fixed delivery slot (within the ramp's cap) that no existing
// hauler is placed on, or "" when every capped slot is served. A slot is "served" when some hauler's
// Waypoint is on it (idle at, or heading to) — so a hauler bought last tick and still en route keeps
// its slot from being re-selected, which is what spreads the ramp's hulls one per park. The count
// guard caps total buys regardless, so a placement can never overshoot the hauler target.
func firstUnservedSlot(slots []string, haulers []HaulerSnapshot, slotCap int) string {
	served := make(map[string]struct{}, len(haulers))
	for _, hl := range haulers {
		if hl.Waypoint != "" {
			served[hl.Waypoint] = struct{}{}
		}
	}
	limit := len(slots)
	if limit > slotCap {
		limit = slotCap
	}
	for i := 0; i < limit; i++ {
		if _, ok := served[slots[i]]; !ok {
			return slots[i]
		}
	}
	return ""
}
