package contract

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// IdleArbDispatcher harvests the contract fleet's idle wall-time with
// hub-local, one-shot arb legs, dispatched through the CONTRACT coordinator's
// own claim identity so ship-dedication exclusivity stays intact (the arb
// containers claim through the same atomic operation-checked ClaimShip every
// contract worker uses).
//
// WHY NO RECALLABLE LEASE: the dispatcher never claims a hull while claimable
// idle hulls ≤ ReserveHulls, and it RECOUNTS from the repository before EVERY
// claim. Contract claims are serial (one worker at a time) and every
// completion releases a hull back to idle, so a contract claim always finds
// ≥ReserveHulls unclaimed, homed hulls — added claim latency from arb is
// ZERO. A recall primitive would add persisted lease state, a recall
// protocol, and mid-leg cargo cleanup to improve on a bound that is already
// zero.
//
// HUB-LOCAL is physical, not advisory: the leg's BuyAt is the hull's CURRENT
// waypoint (its hub) — the arb run's location guard refuses to buy anywhere
// else — and SellAt must sit within HubRadius in the same system, so a hull is
// never more than one short hop from home. Guards are inherited wholesale from
// the arb run: min-margin (live re-read, fail-closed), max-spend, the
// non-tunable working-capital floor, stranded-cargo=failure. Nothing here
// weakens them (RULINGS #4); the dispatcher only DECIDES lanes, it never spends.
//
// The dispatcher itself holds no persisted state: every tick recomputes from
// live discovery, and the launched containers are ordinary recovery-safe
// arb_run rows (rebuilt or cleanly released on daemon restart — RULINGS #2/#3).

// IdleArbDispatcher runs the idle-gap harvest for one coordinator's dedicated
// fleet.
type IdleArbDispatcher struct {
	shipRepo        navigation.ShipRepository
	marketRepo      market.MarketRepository
	graphProvider   system.ISystemGraphProvider
	launcher        IdleArbLauncher
	homer           ShipHomer // post-leg re-homing (nil → re-home off)
	contractGoods   ContractGoodsProvider
	treasury        TreasuryReader // live-treasury source for the reserve gate (nil → gate inert)
	clock           shared.Clock
	playerID        shared.PlayerID
	fleet           string
	cfg             IdleArbConfig
	blacklist       map[string]struct{}            // upper-cased cfg.Blacklist, built once
	launchStandby   []string                       // the launch standby set — the fallback when no live resolver is wired
	standbyResolver func(context.Context) []string // resolves the LIVE standby set each pass (nil → launchStandby)
	// placementProvider auto-resolves the standby set from the ≤6 FIXED placement slots when
	// the live `fleet hub` set is EMPTY (the auto hub-placement) — the SAME
	// resolution (ResolveStandbyForHoming) the coordinator's between-legs homing uses, so the
	// standing re-home sweep and the between-legs hook place hulls on ONE slot set (RULINGS #7).
	// Nil-safe: without it rehomeDriftedHulls keeps the raw fleet-hub/launch set, so a sweep with
	// no hubs pinned is a no-op.
	placementProvider StandbyPlacementProvider
	lanes             *laneMutex // one hull per (good, sink) per recovery window

	// The cross-engine absorption ledger. nil → integration inert (the same
	// optional-port contract the other guards use). When wired, the dispatcher
	// CONSULTS it once per pass (skip:reserved) and RECORDS each launched leg's sell
	// side so tours and other dispatchers see this leg's in-flight absorption — the
	// lane mutex + flat hold above stay armed in parallel as a second layer.
	ledger          absorption.Ledger
	plannedTTLSlack time.Duration
	skipReserved    int // legs skipped: sink reserved/recovering in the absorption ledger

	// Observability counters. In-memory and reset on restart by design: they
	// measure THIS process's harvest rate, not operational state — a restart
	// legitimately restarts the window. The operational state (claims,
	// reservations, container rows) is persisted by the existing mechanisms
	// (RULINGS #2), untouched here. DispatchOnce is called serially (Run's
	// single goroutine), so these need no locking.
	startTime        time.Time
	attempts         int // legs launch-attempted
	launched         int // legs successfully launched
	skipBlacklist    int // legs skipped: good on the blacklist
	skipContractGood int // legs skipped: good under an open contract
	skipLeash        int // legs skipped: only profit was beyond the leash/leg-time
	skipLaneHeld     int // legs skipped: best lane held by a live/recovering leg
	skipUnprofitable int // legs skipped: live net_per_u below the profitability floor
	rehomed          int // hulls re-homed post-leg (cumulative)
	heldReserveFloor int // passes cut short: one more leg would breach the working-capital reserve
}

// NewIdleArbDispatcher wires a dispatcher for the given dedicated fleet. A nil
// contractGoods provider leaves the contract-good exclusion inert —
// the same optional-port contract the other guards use for missing wiring.
func NewIdleArbDispatcher(
	shipRepo navigation.ShipRepository,
	marketRepo market.MarketRepository,
	graphProvider system.ISystemGraphProvider,
	launcher IdleArbLauncher,
	homer ShipHomer,
	contractGoods ContractGoodsProvider,
	clock shared.Clock,
	playerID shared.PlayerID,
	fleet string,
	cfg IdleArbConfig,
) *IdleArbDispatcher {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	cfg = cfg.WithDefaults()
	// Pre-build the blacklist lookup once, upper-cased so a config typo in case
	// still matches the API's upper-case good symbols.
	blacklist := make(map[string]struct{}, len(cfg.Blacklist))
	for _, g := range cfg.Blacklist {
		blacklist[strings.ToUpper(strings.TrimSpace(g))] = struct{}{}
	}
	// The launch standby set is the fallback the at-home filter uses when no LIVE
	// resolver is wired; trimmed of blank entries here so the per-pass lookup need
	// not. When a resolver IS wired (production), the coordinator resolves the
	// CURRENT hub set from its container config each pass so a `fleet hub` change
	// is honored with no restart.
	return &IdleArbDispatcher{
		shipRepo:      shipRepo,
		marketRepo:    marketRepo,
		graphProvider: graphProvider,
		launcher:      launcher,
		homer:         homer,
		contractGoods: contractGoods,
		clock:         clock,
		playerID:      playerID,
		fleet:         fleet,
		cfg:           cfg,
		blacklist:     blacklist,
		launchStandby: trimmedStandby(cfg.StandbyStations),
		lanes:         newLaneMutex(clock, cfg.RecoveryHold),
		startTime:     clock.Now(),
	}
}

// trimmedStandby drops blank entries from a standby-station set. Waypoint symbols
// are case-exact, so no case-folding.
func trimmedStandby(stations []string) []string {
	out := make([]string, 0, len(stations))
	for _, s := range stations {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// SetStandbyResolver wires the LIVE standby-station resolver: the dispatcher
// calls it once per pass to get the CURRENT hub set instead of the frozen
// launch set, so a `fleet hub add|remove` on the running coordinator is
// honored with no restart. Nil (unset) keeps the launch set.
func (d *IdleArbDispatcher) SetStandbyResolver(resolver func(context.Context) []string) {
	d.standbyResolver = resolver
}

// resolveStandby returns the standby-station set for THIS pass: the live resolver
// when wired, else the launch set.
func (d *IdleArbDispatcher) resolveStandby(ctx context.Context) []string {
	if d.standbyResolver != nil {
		return d.standbyResolver(ctx)
	}
	return d.launchStandby
}

// SetStandbyPlacementProvider wires the fixed-placement reader the standing re-home sweep
// (rehomeDriftedHulls) auto-resolves its standby set from when the live `fleet hub` set is empty,
// so a SITTING idle pool homes to the ≤6 fixed placement slots with NO manual hub pins — the
// sp-bu6ma / sp-mtgje auto hub-placement the coordinator's between-legs homing already uses (via
// ResolveStandbyForHoming). Optional and nil-safe: without it the sweep stays on the raw
// fleet-hub/launch set (byte-identical), matching the SetStandbyResolver optional-port idiom.
func (d *IdleArbDispatcher) SetStandbyPlacementProvider(provider StandbyPlacementProvider) {
	d.placementProvider = provider
}

// SetAbsorptionLedger wires the cross-engine absorption ledger, the
// optional-port idiom the other dispatcher dependencies use. A nil ledger leaves the
// consult and the launch-record inert. plannedTTLSlack pads a recorded leg's
// projected round-trip TTL (0 → the package default).
func (d *IdleArbDispatcher) SetAbsorptionLedger(ledger absorption.Ledger, plannedTTLSlack time.Duration) {
	d.ledger = ledger
	if plannedTTLSlack <= 0 {
		plannedTTLSlack = defaultAbsorptionPlannedTTLSlack
	}
	d.plannedTTLSlack = plannedTTLSlack
}

// SetTreasuryReader wires the live-treasury source for the working-capital reserve gate
// (sp-zq635 §4a). Nil (unset) leaves the gate inert — byte-identical to the pre-gate
// behavior, so tests that never wire it are unaffected; production wires it so the pass's
// cumulative leg-spend can never breach the immutable reserve floor.
func (d *IdleArbDispatcher) SetTreasuryReader(reader TreasuryReader) {
	d.treasury = reader
}

// Run ticks DispatchOnce every Interval until ctx is cancelled. Started as a
// goroutine by the fleet coordinator's Handle; the coordinator's own context
// bounds its life, so a stopped coordinator stops the harvest with it.
func (d *IdleArbDispatcher) Run(ctx context.Context) {
	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", fmt.Sprintf(
		"Idle-gap arb dispatcher running: fleet %q, reserve %d hull(s), hub radius %.0f, leash radius %.0f, max leg %s, max spend %d/leg, min margin %d/unit, tick %s",
		d.fleet, d.cfg.ReserveHulls, d.cfg.HubRadius, d.cfg.LeashRadius, d.cfg.MaxLegDuration, d.cfg.MaxSpendPerLeg, d.cfg.MinMarginPerUnit, d.cfg.Interval,
	), nil)

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.cfg.Interval):
		}
		d.DispatchOnce(ctx)
	}
}

// DispatchOnce runs one dispatch pass and returns how many legs it launched.
// Exported so the zero-missed-claims simulation can drive it deterministically.
func (d *IdleArbDispatcher) DispatchOnce(ctx context.Context) int {
	logger := common.LoggerFromContext(ctx)

	launched := 0
	passSkips := 0 // dispatch-time guard skips THIS pass (drives the harvest-summary trigger)

	// POST-LEG RE-HOMING: send every idle dedicated hull that finished
	// off-station back to its balanced standby station FIRST — before the arb
	// loop, and before the contract-goods read below (which is arb-only
	// and fail-closed) so a contract-read failure never skips the re-home. This
	// returns the hulls homed THIS pass; the arb loop excludes them so a hull is
	// never re-arbed from a drift position the same tick it is being sent home
	// (chained legs would drift past the leash, which is measured from the hull's
	// CURRENT waypoint). Inert when re-homing is off (nil homer / no stations).
	homingThisPass := d.rehomeDriftedHulls(ctx)
	rehomedThisPass := len(homingThisPass)

	// LANE MUTEX reconcile: observe which of the legs this dispatcher
	// launched have terminated, so their (good, sink) lanes can begin the recovery
	// hold and eventually free. A terminated leg is one whose hull no longer
	// carries the leg's container id (released to idle, or re-claimed by a
	// contract) — the same live fleet state the reserve accounting reads. A read
	// failure skips reconcile (lanes stay held — the safe direction: never free a
	// lane we cannot confirm terminated), and terminations are picked up next pass.
	if shipContainerIDs, ok := d.fleetShipContainerIDs(ctx); ok {
		d.lanes.reconcile(shipContainerIDs)
	}

	// Emit the harvest summary on every return path of a pass that did
	// something, so the attempt rate, the per-reason skip pressure, and the
	// re-home count are readable from message text.
	defer func() { d.logHarvestSummary(ctx, launched, passSkips, rehomedThisPass) }()

	openGoods, ok := d.readOpenContractGoods(ctx)
	if !ok {
		return launched
	}

	// One batched absorption-ledger read per pass. The consult skips candidates
	// whose (good, sink) another engine has reserved in flight or just crushed
	// (a recovering shadow above its floor) — the cross-engine generalization
	// the in-memory lane mutex cannot see. Fail-closed: an unreadable ledger
	// declines every candidate this pass rather than dispatch blind.
	consult := d.readAbsorption(ctx)

	// One live-treasury read per pass for the working-capital reserve gate
	// (sp-zq635 §4a). The dispatcher launches several concurrent legs per pass, each
	// capped at MaxSpendPerLeg but with no shared spend ledger, so the gate accounts
	// for THIS pass's CUMULATIVE committed spend (committedSpend, below) and holds the
	// rest once one more leg would drop treasury under the reserve. Inert when no reader
	// is wired; fail-closed when the read fails.
	floorGate := d.readReserveFloorGate(ctx)
	var committedSpend int64

	// tried tracks hulls already handled this pass (launched, or skipped for
	// want of a lane) so the recount loop below always terminates. A skipped
	// hull stays idle and keeps padding the reserve — conservative.
	tried := map[string]bool{}

	for {
		// RECOUNT before every claim: the reserve check must see the
		// repository's current truth, not this pass's opening snapshot —
		// this is what shrinks the race window with the coordinator's own
		// claims to the recount→claim gap.
		idleShips, _, err := FindIdleShipsByFleet(ctx, d.playerID, d.shipRepo, d.fleet)
		if err != nil {
			logger.Log("WARNING", fmt.Sprintf("Idle-arb dispatch: idle discovery failed, skipping pass: %v", err), nil)
			return launched
		}

		// A hull sent home this pass is off-limits: its in-transit mark can lag the homer's
		// fire-and-forget return, so exclude it rather than trust the recount.
		candidates := dispatchableHulls(idleShips, tried, homingThisPass)

		// The reserve is judged against ALL idle hulls (tried-but-skipped ones
		// still count — they are genuinely claimable by a contract), but only
		// untried hulls are dispatchable. Hulls launched earlier this pass are
		// already claimed by their containers, so the recount above has
		// excluded them — no separate subtraction needed.
		if len(idleShips) <= d.cfg.ReserveHulls || len(candidates) == 0 {
			return launched
		}

		hull := candidates[0]
		tried[hull.ShipSymbol()] = true

		lane, skip := d.pickHubLocalLane(ctx, hull, openGoods, consult)
		if lane == nil {
			// A guard refused this hull's only profitable lane → attribute the
			// skip by cause. skipNone means there simply was no
			// profitable local lane, i.e. idle-for-lack-of-opportunity, not a
			// guard skip.
			if d.recordSkip(skip) {
				passSkips++
			}
			continue
		}

		// WORKING-CAPITAL RESERVE GATE (sp-zq635 §4a): a GLOBAL treasury bound, not a
		// lane skip. Once launching one more MaxSpendPerLeg leg would drop live treasury
		// below the reserve — counting the legs this pass has already committed — no later
		// candidate can pass either, so HOLD the rest of the pass (fail-closed on an
		// unreadable treasury). Checked AFTER a lane is chosen so a hull with no lane is
		// still attributed to its lane reason, not the floor. RULINGS #1: park, never crash.
		if floorGate.Holds(committedSpend, int64(d.cfg.MaxSpendPerLeg)) {
			d.heldReserveFloor++
			passSkips++
			logReserveFloorHold(ctx, floorGate, d.cfg.MaxSpendPerLeg, committedSpend)
			return launched
		}

		// Hand the arb run's live-verify gate the RELATIVE floor
		// ceil(fraction × quoted margin), not the flat absolute floor. The run
		// re-reads live prices and fails closed, so a leg whose live margin
		// has collapsed below that fraction of its quote aborts pre-buy (zero
		// spend) instead of buying on a razor cushion.
		spec := IdleArbSpec{
			ShipSymbol: hull.ShipSymbol(),
			Good:       lane.Good,
			BuyAt:      hull.CurrentLocation().Symbol,
			SellAt:     lane.SellAt,
			MaxSpend:   d.cfg.MaxSpendPerLeg,
			MinMargin:  idleArbMinMargin(d.cfg, lane.MarginPerUnit),
			PlayerID:   d.playerID.Value(),
			Operation:  d.fleet,
			// Arm the arb run's per-tranche sell floor with the SAME
			// 80%-of-quote knob the buy-side live-verify uses (cfg.MarginVerifyFraction).
			SellFloorFraction: d.cfg.MarginVerifyFraction,
		}
		d.attempts++
		containerID, err := d.launcher.LaunchIdleArb(ctx, spec)
		if err != nil {
			// Losing the claim race (the coordinator took this hull for a
			// contract between recount and claim) is the system WORKING —
			// contract claims outrank arb. Log and move on.
			logger.Log("INFO", fmt.Sprintf(
				"Idle-arb dispatch: launch for %s declined (%v) - hull skipped this pass", hull.ShipSymbol(), err), nil)
			continue
		}
		launched++
		d.launched++
		// Commit this leg's worst-case spend to the pass's reserve-gate tally so a
		// later leg THIS pass is judged against the treasury the earlier legs will draw
		// (the concurrent-breach the per-leg arb-run floor alone cannot see).
		committedSpend += int64(d.cfg.MaxSpendPerLeg)
		// LANE MUTEX: mark this (good, sink) held the instant the leg
		// launches, so a later candidate THIS pass that would pick the same sink is
		// skipped:lane-held (within-pass dedupe), and the next pass holds it until
		// the leg terminates + the recovery window elapses (cross-pass).
		d.lanes.noteLaunch(laneKey{good: lane.Good, sink: lane.SellAt}, hull.ShipSymbol(), containerID)
		// Publish this leg's sell-side absorption to the cross-engine ledger at
		// the same seam the mutex is marked, so a tour or another dispatcher
		// consults it. Fail-open record (the leg has committed) — see recordAbsorption.
		d.recordAbsorption(ctx, hull, lane, containerID)

		logIdleArbLaunched(logger, hull.ShipSymbol(), lane, spec, containerID)
	}
}

// readOpenContractGoods reads the OPEN-contract goods once per pass. Fails CLOSED —
// a read error skips the tick; a nil provider leaves the exclusion inert.
func (d *IdleArbDispatcher) readOpenContractGoods(ctx context.Context) (map[string]struct{}, bool) {
	if d.contractGoods == nil {
		return map[string]struct{}{}, true
	}
	goods, err := d.contractGoods.OpenContractGoods(ctx, d.playerID.Value())
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Idle-arb dispatch: could not read open-contract goods, skipping pass (fail-closed): %v", err), nil)
		return nil, false
	}
	return goods, true
}

func dispatchableHulls(idleShips []*navigation.Ship, tried, homingThisPass map[string]bool) []*navigation.Ship {
	var candidates []*navigation.Ship
	for _, s := range idleShips {
		if !tried[s.ShipSymbol()] && !homingThisPass[s.ShipSymbol()] {
			candidates = append(candidates, s)
		}
	}
	return candidates
}

func logIdleArbLaunched(logger common.ContainerLogger, shipSymbol string, lane *IdleArbLane, spec IdleArbSpec, containerID string) {
	logger.Log("INFO", fmt.Sprintf(
		"Idle-gap arb leg launched: %s flies %s %s->%s (quoted margin %d/unit = bid %d - ask %d, live-verify floor %d/unit, distance %.0f, max spend %d) in container %s",
		shipSymbol, lane.Good, spec.BuyAt, lane.SellAt,
		lane.MarginPerUnit, lane.DestBid, lane.SourceAsk, spec.MinMargin, lane.Distance, spec.MaxSpend, containerID,
	), map[string]interface{}{
		"action":       "idle_arb_launched",
		"ship_symbol":  shipSymbol,
		"good":         lane.Good,
		"buy_at":       spec.BuyAt,
		"sell_at":      lane.SellAt,
		"margin":       lane.MarginPerUnit,
		"distance":     lane.Distance,
		"container_id": containerID,
	})
}

// fleetShipContainerIDs returns the dedicated fleet's live ship→container map
// (symbol → current container id, "" when idle/unassigned) — the input the lane
// mutex reconciles its launched legs against. It reads the same
// repository the reserve recount does. ok is false on a read failure, so the
// caller skips reconcile and leaves lane holds untouched rather than free a lane
// it cannot confirm terminated.
func (d *IdleArbDispatcher) fleetShipContainerIDs(ctx context.Context) (map[string]string, bool) {
	ships, err := d.shipRepo.FindAllByPlayer(ctx, d.playerID)
	if err != nil {
		return nil, false
	}
	out := make(map[string]string, len(ships))
	for _, s := range ships {
		if s.DedicatedFleet() != d.fleet {
			continue
		}
		out[s.ShipSymbol()] = s.ContainerID()
	}
	return out, true
}

// rehomeDriftedHulls sends every idle dedicated hull that is NOT sitting at one
// of the standby stations back to its balanced standby station via the EXISTING
// HomeShipCommand (the ShipHomer port), and returns the set of hulls homed this
// pass so the caller keeps them out of the arb loop. This is the STANDING sweep
// that clears the SITTING idle pool (contracts finished long ago, hulls held
// ready): it runs at the top of every pass on ALL idle off-station hulls,
// regardless of whether they ever fly an arb leg — so the reserve-floor buffer
// (never arb'd) is homed too.
//
// STANDBY SET: resolved the SAME way the coordinator's between-legs homing
// resolves it — the `fleet hub`/launch set, then ResolveStandbyForHoming
// AUTO-FILLS it from the role-classified central parks when the pinned set is
// EMPTY (the auto hub-placement). Without the auto-fill the sweep bailed
// whenever the operator relied on auto-placement, leaving the pool to pile where
// it last finished (the live J59 pile).
//
// Re-homing off-station hulls between legs also keeps the hub-local leash
// honest: the leash is measured from the hull's CURRENT waypoint, so a hull
// left at a drift position could chain legs arbitrarily far from home.
//
// WHY ONLY OFF-SLOT HULLS: a hull already parked at ITS OWN assigned slot is left alone. Under fixed
// placement each delivery hull permanently owns one slot (the symbol-zip of the roster onto the ≤6
// placement slots, domainContract.AssignedSlot); re-firing HomeShipCommand on a hull already at its
// slot would be a no-op churn, and a hull sitting at a PEER's slot must move to its OWN — treating
// "at ANY standby station" as home leaves it there forever. Claimed and in-transit hulls never appear
// here — FindIdleShipsByFleet already excludes them — so an active contract claim or an in-flight leg
// is never disturbed. The command frigate is skipped explicitly (RULINGS #7). A surplus hull (beyond
// the delivery knee) owns no slot and is left for the scaler to re-role into a warehouse.
//
// Best-effort and inert when re-homing is off (nil homer, or an empty EFFECTIVE standby set — no
// `fleet hub` pins AND no fixed placement resolved), matching HomeShipCommand's own "empty stations
// disables relocation" contract.
func (d *IdleArbDispatcher) rehomeDriftedHulls(ctx context.Context) map[string]bool {
	homed := map[string]bool{}
	if d.homer == nil {
		return homed
	}

	logger := common.LoggerFromContext(ctx)

	// LIVE standby set for this pass, resolved the SAME way the coordinator's between-legs homing
	// resolves it: the `fleet hub`/launch set (resolveStandby), then ResolveStandbyForHoming
	// AUTO-FILLS it from the ≤6 FIXED placement slots when EMPTY (the sp-bu6ma / sp-mtgje auto
	// hub-placement) so a SITTING idle pool homes with no manual hub pins. Nil placement provider →
	// the raw set is kept (byte-identical). An empty EFFECTIVE set disables re-homing.
	standbyStations := ResolveStandbyForHoming(ctx, logger, d.placementProvider, d.playerID.Value(), d.resolveStandby(ctx))
	if len(standbyStations) == 0 {
		return homed
	}

	// The FULL delivery roster (idle + busy) — the SAME set HomeShipHandler zips against (both via
	// FindFleetMemberSymbols), so this pre-filter's per-hull slot matches the handler's exactly. A read
	// error skips the pre-filter's slot check for this pass (falls through to dispatch, which the
	// handler no-ops if already home) rather than skipping the whole sweep.
	roster, err := FindFleetMemberSymbols(ctx, d.playerID, d.shipRepo, d.fleet)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Idle-arb re-home: fleet roster read failed, homing every idle hull this pass (handler will no-op the settled ones): %v", err), nil)
		roster = nil
	}

	idleShips, _, err := FindIdleShipsByFleet(ctx, d.playerID, d.shipRepo, d.fleet)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("Idle-arb re-home: idle discovery failed, skipping re-home this pass: %v", err), nil)
		return homed
	}

	for _, hull := range idleShips {
		// The command frigate is never swept by the standing re-home: it hauls only as a last resort
		// and its positioning is managed by that draft, not the idle harvest (RULINGS #7).
		if isCommandHull(hull) {
			continue
		}
		loc := hull.CurrentLocation()
		if loc == nil {
			continue
		}
		// FIXED PLACEMENT: skip a hull already at ITS OWN assigned slot (no thrash), and skip a surplus
		// hull that owns no slot (left for the scaler's re-role). A hull off its slot is homed to it.
		slot, owns := domainContract.AssignedSlot(hull.ShipSymbol(), roster, standbyStations)
		if !owns {
			continue // surplus over the knee — the scaler re-roles it to a warehouse
		}
		if loc.Symbol == slot {
			continue // already at MY slot — re-firing would be a no-op churn
		}
		if err := d.homer.HomeShip(ctx, hull.ShipSymbol(), standbyStations); err != nil {
			logger.Log("WARNING", fmt.Sprintf(
				"Idle-arb re-home: could not dispatch homing for %s from %s: %v", hull.ShipSymbol(), loc.Symbol, err), nil)
			continue
		}
		homed[hull.ShipSymbol()] = true
		d.rehomed++
		logger.Log("INFO", fmt.Sprintf(
			"Idle-arb re-home: %s idle off-slot at %s - homing to its fixed slot %s", hull.ShipSymbol(), loc.Symbol, slot),
			map[string]interface{}{
				"action":      "idle_arb_rehome",
				"ship_symbol": hull.ShipSymbol(),
				"from":        loc.Symbol,
				"slot":        slot,
			})
	}

	return homed
}
