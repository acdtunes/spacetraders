// Package commands holds the probe-buyer-fleet coordinator (sp-f082y): a standing daemon
// coordinator that breaks the probe-fleet-growth catch-22. When demand for freshness/scout coverage
// outruns supply, the market-freshness sizer assigns EVERY probe to a post, so the shared probe-buy
// path (which needs an idle UNDEDICATED hull to buy through) never finds a buyer and the fleet can
// never grow — every hull mans a post, none is left to buy MORE probes.
//
// This coordinator maintains a small DEDICATED probe-buyer fleet (dedicated_fleet="probe-buyer"): K
// hulls stationed at probe-selling yards that do movement-free in-place buys, so the fleet grows even
// when demand >> supply. It mirrors the long-haul dedicated-fleet pattern — its hulls are driven
// EXCLUSIVELY by this coordinator and skipped by every other (manning, trade, contract, frontier,
// freshness), because those reconcilers skip any hull dedicated to a fleet that is not their own.
//
// It is THIN orchestration over REUSED machinery, forking nothing:
//   - the buy + every money guard (25% treasury, working-capital floor, fleet cap, price ceiling)
//     is the shared probebuy.GuardedProbeBuyer, whose BuyProbe drives the shared
//     expansion.ProbePurchaser.resolveInPlaceBuy — wired ownFleet="probe-buyer" so it selects the
//     dedicated buyers and claims them under their own fleet operation;
//   - stationing / yard rotation is the shared expansion.ProbeBuyerPositioner (same ownFleet);
//   - recruitment tags the nearest reachable idle undedicated satellite through the single
//     dedicated_fleet write path (ShipRepository.AssignFleet), reusing the ReachableYardFinder.
//
// The loop is idempotent and restart-safe (RULINGS #2): the dedicated tag + fleet cap are DB-backed,
// so every tick re-derives its buyers from persisted ship rows and never holds in-memory-only state.
// It NEVER poaches a hull dedicated to another fleet and NEVER touches the command frigate (RULINGS
// #7); every buy stays behind the unchanged money guards + cap (RULINGS #4), stopping at the cap.
//
// PHASE-GATED (sp-f3mcc, Admiral 2026-07-24): the whole reconcile is inert outside the bootstrap-
// derived EXPANSION phase (the home jump gate built, sp-feiy7) — probes are bought ONLY in the
// steady-state-growth era, never during DATA/INCOME/GATE where they drained the contract working-
// capital band. Fail-closed: an unreadable phase holds every action.
package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/application/probebuy"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

const (
	// ProbeBuyerFleet is the dedicated_fleet tag these buyer hulls carry AND the operation their
	// single-writer claim is taken under (the two must match so ClaimShip's dedication guard accepts
	// the claim). It is the ONE identity the recruiter writes, the purchaser/positioner drive on
	// (ownFleet), and every other reconciler skips.
	ProbeBuyerFleet = "probe-buyer"

	// probeShipType is the SpaceTraders purchase type for a scout/satellite hull; a bought SHIP_PROBE
	// reports role SATELLITE and is counted toward the fleet cap.
	probeShipType = "SHIP_PROBE"

	// defaultProbeBuyerTickSeconds is the reconcile cadence when the launch config leaves it unset.
	defaultProbeBuyerTickSeconds = 60

	// DefaultProbeBuyerCount is K: how many dedicated buyer hulls the fleet maintains. Small on
	// purpose — a couple of stationed buyers keep the fleet growing without cannibalizing manning.
	// Live-tunable via the probe_buyer_count knob.
	DefaultProbeBuyerCount = 2

	// DefaultProbeBuyerMaxFleet is the total satellite cap the coordinator grows toward when the
	// launch config leaves it unset. The cap is the binding demand signal here (freshness demand
	// already outruns it); the coordinator stops buying at the cap. Live-tunable via max_probe_fleet.
	DefaultProbeBuyerMaxFleet = 200

	// probeBuyerMaxProbePrice is the IMMUTABLE per-unit price ceiling handed to every buy — the same
	// anti-overpay backstop the frontier enforces (sp-tlekc §2C). It also drives yard rotation: when a
	// stationed yard's live price climbs past it, the buy defers and the coordinator relocates the
	// buyer to another reachable yard. Immutable const (RULINGS #5) — never tunable OFF into a spiral.
	probeBuyerMaxProbePrice = 100_000
)

// guardedBuyer is the shared fail-closed buy engine (probebuy.GuardedProbeBuyer): it runs the whole
// money-guard stack — demand-vs-supply, fleet cap, 25% treasury, working-capital floor, price
// ceiling, spend window — and, when every guard passes, buys ONE probe through the reused purchaser.
// A driven port of this coordinator; the guard stack has its own tests.
type guardedBuyer interface {
	MaybeBuy(ctx context.Context, playerID shared.PlayerID, demand, supply int, dryRun bool, target probebuy.ProbeTarget) probebuy.Outcome
}

// buyerPositioner stations / rotates an idle dedicated buyer to a reachable probe-yard so its
// in-place price reads next tick (expansion.ProbeBuyerPositioner wired ownFleet="probe-buyer"). It
// NEVER buys and NEVER poaches (RULINGS #4/#7); a no-op when nothing is eligible.
type buyerPositioner interface {
	PositionProbeBuyer(ctx context.Context, playerID shared.PlayerID) (bool, error)
}

// expansionPhaseReader reports whether the bootstrap-derived lifecycle phase is EXPANSION — the
// gate-built steady-state-growth era that probe-buying belongs to (Admiral 2026-07-24, sp-f3mcc:
// "not in the gate, not in data, not in income"). The phase is DERIVED from the live world exactly
// the way the bootstrap coordinator derives it (never a persisted enum, never a running-container
// read — bootstrap EXITS after its hand-off): EXPANSION ⇔ the home-system jump-gate construction is
// COMPLETE (derivePhase's terminal branch, sp-feiy7). An error means the phase could not be read —
// the coordinator treats that FAIL-CLOSED (no buys), because a spender guard never defaults open.
type expansionPhaseReader interface {
	InExpansion(ctx context.Context, playerID shared.PlayerID) (bool, error)
}

// fleetRepository is the ship-roster slice this coordinator needs: read the whole fleet (to count
// satellites + derive the current buyers, restart-safe), and write the dedicated_fleet tag on a
// recruit (the single AssignFleet write path). Satisfied by navigation.ShipRepository.
type fleetRepository interface {
	FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error)
	AssignFleet(ctx context.Context, shipSymbol string, fleet string, playerID shared.PlayerID) error
}

// yardReachability finds the scanned probe-yards reachable from a system (shipyardQueries
// .ReachableYardFinder). Used only to prefer a recruit that can actually reach a yard; nil disables
// the reachability check (the positioner still handles reachability once the hull is tagged).
type yardReachability interface {
	NearestYardsSelling(ctx context.Context, playerID int, shipTypes []string, fromSystems []string) ([]shipyardQueries.YardCandidate, error)
}

// RunProbeBuyerFleetCoordinatorCommand launches/re-adopts the standing coordinator. Zero-value knobs
// resolve to their documented defaults (RULINGS #5).
type RunProbeBuyerFleetCoordinatorCommand struct {
	PlayerID         shared.PlayerID
	ContainerID      string
	TickIntervalSecs int
	ProbeBuyerCount  int  // K dedicated buyers to maintain; <=0 -> default
	MaxProbeFleet    int  // total satellite cap; <=0 -> default
	DryRun           bool // observe-only: evaluate + report but never tag, buy, or move
}

// RunProbeBuyerFleetCoordinatorResponse is the terminal summary when the loop is cancelled.
type RunProbeBuyerFleetCoordinatorResponse struct {
	Ticks int
}

// RunProbeBuyerFleetCoordinatorHandler drives the reconcile loop over the reused collaborators.
type RunProbeBuyerFleetCoordinatorHandler struct {
	fleetRepo  fleetRepository
	buyer      guardedBuyer
	positioner buyerPositioner
	yardFinder yardReachability
	phase      expansionPhaseReader
	clock      shared.Clock
	liveConfig liveconfig.Reader
}

// NewRunProbeBuyerFleetCoordinatorHandler wires the coordinator. yardFinder + liveConfig are optional
// (nil-safe); clock defaults to the real clock. phase is the sp-f3mcc EXPANSION gate — a REQUIRED
// spender guard, deliberately a constructor parameter (not an optional setter): a nil reader holds
// the coordinator fail-closed (no buys, surfaced loudly every tick), never silently open.
func NewRunProbeBuyerFleetCoordinatorHandler(
	fleetRepo fleetRepository,
	buyer guardedBuyer,
	positioner buyerPositioner,
	yardFinder yardReachability,
	phase expansionPhaseReader,
	clock shared.Clock,
) *RunProbeBuyerFleetCoordinatorHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RunProbeBuyerFleetCoordinatorHandler{
		fleetRepo:  fleetRepo,
		buyer:      buyer,
		positioner: positioner,
		yardFinder: yardFinder,
		phase:      phase,
		clock:      clock,
	}
}

// SetLiveConfigReader wires the per-tick live-config snapshot source so probe_buyer_count and
// max_probe_fleet can be tuned without a relaunch. Optional (nil -> launch-config values).
func (h *RunProbeBuyerFleetCoordinatorHandler) SetLiveConfigReader(reader liveconfig.Reader) {
	h.liveConfig = reader
}

// Handle runs the infinite reconcile loop (mirrors the other standing coordinators). Iterations are
// infinite; the container budget is irrelevant.
func (h *RunProbeBuyerFleetCoordinatorHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunProbeBuyerFleetCoordinatorCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *RunProbeBuyerFleetCoordinatorCommand, got %T", request)
	}
	tick := time.Duration(cmd.TickIntervalSecs) * time.Second
	if tick <= 0 {
		tick = defaultProbeBuyerTickSeconds * time.Second
	}
	logger := common.LoggerFromContext(ctx)
	result := &RunProbeBuyerFleetCoordinatorResponse{}
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}
		if err := h.reconcileOnce(ctx, cmd); err != nil {
			logger.Log("WARNING", fmt.Sprintf("probe-buyer-fleet reconcile error: %v", err), map[string]interface{}{
				"action": "probe_buyer_fleet_reconcile_error",
				"error":  err.Error(),
			})
		}
		result.Ticks++
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(tick):
		}
	}
}

// reconcileOnce is one tick: check the Admiral EXPANSION phase gate (outside EXPANSION the tick is
// fully inert), then re-derive the fleet from persisted rows (restart-safe), recruit toward K
// dedicated buyers when short and below cap, then drive up to K in-place buys through the stationed
// buyers behind the reused money-guard stack, and station/rotate a buyer on a shortfall. Every input
// comes from the persisted world each tick; the handler holds no cycle state (RULINGS #2).
func (h *RunProbeBuyerFleetCoordinatorHandler) reconcileOnce(ctx context.Context, cmd *RunProbeBuyerFleetCoordinatorCommand) error {
	// ADMIRAL PHASE GATE (sp-f3mcc, 2026-07-24): "WE SHOULD NOT BE BUYING PROBES IN THE GATE
	// PHASE... not in the gate, not in data, not in income!" Probe-fleet growth belongs to
	// EXPANSION (the gate-built era, sp-feiy7) alone. Outside it — and whenever the phase cannot
	// be read (fail-closed, RULINGS #4) — the WHOLE tick is inert: no buy reaches the guard stack,
	// no satellite is recruited off manning, no buyer is repositioned (zero API churn), not even a
	// fleet read. Checked FIRST so a pre-EXPANSION tick spends nothing anywhere. Guard tightening
	// only: the money-guard stack behind MaybeBuy is untouched and still applies in EXPANSION.
	if !h.expansionReached(ctx, cmd) {
		return nil
	}

	fleetCap, buyerCount := h.resolveConfig(ctx, cmd)

	ships, err := h.fleetRepo.FindAllByPlayer(ctx, cmd.PlayerID)
	if err != nil {
		return fmt.Errorf("failed to list fleet: %w", err)
	}

	satellites := 0
	dedicatedBuyers := 0
	for _, ship := range ships {
		if ship.IsScoutType() {
			satellites++
		}
		if ship.DedicatedFleet() == ProbeBuyerFleet {
			dedicatedBuyers++
		}
	}

	// Recruit toward K buyers — only while below the cap, and never in dry-run (tagging is a write).
	if !cmd.DryRun && dedicatedBuyers < buyerCount && satellites < fleetCap {
		h.recruitOne(ctx, cmd, ships)
	}

	// At/over the cap the fleet is full: the buyers idle, nothing is bought (RULINGS #4).
	if satellites >= fleetCap {
		return nil
	}

	// Buy up to K probes in-place through the stationed dedicated buyers, each behind the FULL reused
	// guard stack (25% treasury, working-capital floor, fleet cap, price ceiling). supply increments
	// across the tick so the cap is honored buy-by-buy — the fleet grows by the number that land.
	bought := 0
	for i := 0; i < buyerCount; i++ {
		supply := satellites + bought
		if supply >= fleetCap {
			break // reached the cap mid-tick
		}
		outcome := h.buyer.MaybeBuy(ctx, cmd.PlayerID, fleetCap /*demand*/, supply, cmd.DryRun, probebuy.ProbeTarget{
			ClaimOwnerContainerID: cmd.ContainerID,
			MaxProbePriceCredits:  probeBuyerMaxProbePrice,
		})
		if outcome.Bought {
			bought++
			continue
		}
		break // a guard refused the buy, or no buyer sits at a priceable yard — stop attempting this tick
	}

	// Short of K while still below the cap → station/rotate a buyer so a fresh, priceable yard reads
	// next tick (breaks the "no buyer at a yard" and "current yard over the ceiling" stalls). It never
	// buys and never poaches (RULINGS #4/#7). Suppressed in dry-run (it moves a real hull) and never
	// once the cap is reached (reaching the cap is not a shortfall).
	if !cmd.DryRun && bought < buyerCount && (satellites+bought) < fleetCap {
		if _, err := h.positioner.PositionProbeBuyer(ctx, cmd.PlayerID); err != nil {
			common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("probe-buyer positioning failed: %v", err), map[string]interface{}{
				"action": "probe_buyer_position_failed",
				"error":  err.Error(),
			})
		}
	}
	return nil
}

// expansionReached reports whether the bootstrap-derived phase is EXPANSION, surfacing the honest
// no-work reason when it is not (never a silent stall). FAIL-CLOSED on every unreadable input
// (RULINGS #4 — a spender guard never defaults open): a nil reader (mis-wire) and a read error both
// hold the coordinator inert, each with its own loud line so the wedge is visible on the first look.
func (h *RunProbeBuyerFleetCoordinatorHandler) expansionReached(ctx context.Context, cmd *RunProbeBuyerFleetCoordinatorCommand) bool {
	logger := common.LoggerFromContext(ctx)
	if h.phase == nil {
		logger.Log("WARNING", "probe buying held (fail-closed): no bootstrap-phase reader wired — cannot verify the EXPANSION phase, and a spender guard never defaults open (sp-f3mcc)", map[string]interface{}{
			"action": "probe_buyer_phase_unreadable",
		})
		return false
	}
	inExpansion, err := h.phase.InExpansion(ctx, cmd.PlayerID)
	if err != nil {
		logger.Log("WARNING", fmt.Sprintf("probe buying held (fail-closed): bootstrap phase unreadable — no buys on an unknown phase (sp-f3mcc): %v", err), map[string]interface{}{
			"action": "probe_buyer_phase_unreadable",
			"error":  err.Error(),
		})
		return false
	}
	if !inExpansion {
		logger.Log("INFO", "probe buying deferred: bootstrap phase pre-EXPANSION (jump-gate construction incomplete — the world is still in DATA/INCOME/GATE); probes are bought only in EXPANSION (Admiral 2026-07-24, sp-f3mcc)", map[string]interface{}{
			"action": "probe_buyer_phase_deferred",
		})
		return false
	}
	return true
}

// resolveConfig resolves the effective cap + K for this tick: a positive live-config override wins
// (the tune knobs), else the launch command value, else the documented default. A nil reader or an
// unreadable snapshot falls back to the launch values (launch-frozen behavior).
func (h *RunProbeBuyerFleetCoordinatorHandler) resolveConfig(ctx context.Context, cmd *RunProbeBuyerFleetCoordinatorCommand) (fleetCap, buyerCount int) {
	fleetCap = cmd.MaxProbeFleet
	buyerCount = cmd.ProbeBuyerCount
	if h.liveConfig != nil {
		if snapshot, err := h.liveConfig.Snapshot(ctx, cmd.ContainerID, cmd.PlayerID.Value()); err == nil {
			if value := snapshot.PositiveIntOrZero("max_probe_fleet"); value > 0 {
				fleetCap = value
			}
			if value := snapshot.PositiveIntOrZero("probe_buyer_count"); value > 0 {
				buyerCount = value
			}
		}
	}
	if fleetCap <= 0 {
		fleetCap = DefaultProbeBuyerMaxFleet
	}
	if buyerCount <= 0 {
		buyerCount = DefaultProbeBuyerCount
	}
	return fleetCap, buyerCount
}

// ProbeBuyerTunableDefaults returns the coordinator's live-tunable knob defaults, the single source
// of record the tune registry reads (mirrors ScoutPostTunableDefaults / SizerTunableDefaults).
func ProbeBuyerTunableDefaults() map[string]int {
	return map[string]int{
		"probe_buyer_count": DefaultProbeBuyerCount,
		"max_probe_fleet":   DefaultProbeBuyerMaxFleet,
	}
}

// recruitOne tags ONE nearest-reachable idle undedicated satellite into the probe-buyer fleet through
// the single dedicated_fleet write path (AssignFleet). It NEVER poaches a hull dedicated to another
// fleet and NEVER touches the command frigate (RULINGS #7), and pulls only IDLE hulls (a working
// hull's post is left intact — pull-from-a-manned-post is a documented follow-up). One recruit/tick.
func (h *RunProbeBuyerFleetCoordinatorHandler) recruitOne(ctx context.Context, cmd *RunProbeBuyerFleetCoordinatorCommand, ships []*navigation.Ship) {
	candidates := make([]*navigation.Ship, 0, len(ships))
	for _, ship := range ships {
		if !ship.IsScoutType() {
			continue // only satellites become probe buyers
		}
		if ship.DedicatedFleet() != "" {
			continue // never poach a hull dedicated to ANOTHER fleet (RULINGS #7)
		}
		if domainContract.IsCommandHull(ship) {
			continue // never the command frigate (RULINGS #7)
		}
		if !ship.IsIdle() {
			continue // never pull a working hull off its post (idle-only recruitment — follow-up: pull-from-manning)
		}
		if ship.CurrentLocation() == nil {
			continue // cannot anchor yard reachability without a location
		}
		candidates = append(candidates, ship)
	}
	chosen := h.pickReachableRecruit(ctx, cmd.PlayerID, candidates)
	if chosen == nil {
		return // nothing idle + reachable to recruit this tick — retry next tick
	}
	if err := h.fleetRepo.AssignFleet(ctx, chosen.ShipSymbol(), ProbeBuyerFleet, cmd.PlayerID); err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("probe-buyer recruit failed for %s: %v", chosen.ShipSymbol(), err), map[string]interface{}{
			"action":      "probe_buyer_recruit_failed",
			"ship_symbol": chosen.ShipSymbol(),
			"error":       err.Error(),
		})
		return
	}
	common.LoggerFromContext(ctx).Log("INFO", fmt.Sprintf("Recruited probe-buyer %s (dedicated_fleet=%q) to grow the probe fleet", chosen.ShipSymbol(), ProbeBuyerFleet), map[string]interface{}{
		"action":      "probe_buyer_recruited",
		"ship_symbol": chosen.ShipSymbol(),
		"fleet":       ProbeBuyerFleet,
	})
}

// pickReachableRecruit returns the first candidate whose OWN system reaches a scanned probe-yard, so
// a tagged buyer can actually get to a yard (reachability anchored where the hull is). With no yard
// finder wired it returns the first candidate (the positioner handles reachability post-tag).
// Nearest-hull ranking across reachable candidates is a documented follow-up.
func (h *RunProbeBuyerFleetCoordinatorHandler) pickReachableRecruit(ctx context.Context, playerID shared.PlayerID, candidates []*navigation.Ship) *navigation.Ship {
	if len(candidates) == 0 {
		return nil
	}
	if h.yardFinder == nil {
		return candidates[0]
	}
	for _, ship := range candidates {
		yards, err := h.yardFinder.NearestYardsSelling(ctx, playerID.Value(), []string{probeShipType}, []string{ship.CurrentLocation().SystemSymbol})
		if err != nil || len(yards) == 0 {
			continue // this hull's system reaches no probe-yard — pass it over
		}
		return ship
	}
	return nil
}
