package commands

// run_longhaul_arb_worker.go — the per-hull long-haul WORKER (sp-mepj §4). One episode:
// discover -> select+size -> reposition to source -> run the OUT leg -> opportunistic
// backhaul or deadhead. It COMPOSES the reused primitives behind narrow ports rather than
// re-implementing them: directedLegExecutor is backed by the REUSED RunArbCoordinatorHandler
// (buy@source under the envelope caps -> cross-gate travel -> per-tranche sell ->
// held-cargo-as-failure), hullRepositioner by the shared travel machinery. On restart it
// re-derives statelessly from the DB: a hull holding cargo from an interrupted episode
// resumes SELLING it (never re-buys on top of a laden hull).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	"github.com/andrescamacho/spacetraders-go/internal/domain/absorption"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// defaultLongHaulIdleBackoff is how long a worker that found no tradeable lane waits before
// re-scanning — the exotic ground it works recovers over minutes, so a tight retry would just
// re-rank the same tapped markets.
const defaultLongHaulIdleBackoff = 90 * time.Second

// directedLegCommand describes one directed single-good leg for the reused executor: buy Good
// at BuyAt, sell at SellAt, up to Units, under the per-haul spend cap. The production executor
// maps this onto a RunArbCoordinatorCommand and sets WorkingCapitalReserve to the 200k cushion
// fence, so the reused arb guards backstop the money envelope.
type directedLegCommand struct {
	ShipSymbol  string
	Good        string
	BuyAt       string
	SellAt      string
	Units       int
	PerHaulCap  int64
	MinMargin   int
	PlayerID    int
	ContainerID string
}

// directedLegResult reports one leg's realized outcome.
type directedLegResult struct {
	UnitsTraded int
	NetProfit   int
	Aborted     bool
	AbortReason string
}

// directedLegExecutor runs one directed buy->travel->sell leg. Backed in production by the
// REUSED RunArbCoordinatorHandler — the worker composes it (out + backhaul) rather than
// re-implementing buy/travel/sell/held-cargo.
type directedLegExecutor interface {
	RunLeg(ctx context.Context, cmd directedLegCommand) (directedLegResult, error)
}

// hullRepositioner moves a hull to a (possibly cross-gate) waypoint — backed by the shared
// travel machinery (legs.RepositionToWaypoint), so the worker never forks route logic.
type hullRepositioner interface {
	RepositionToWaypoint(ctx context.Context, shipSymbol, destination string, playerID int) error
}

// laneDiscoverer returns the ranked, priced, out-of-horizon long-haul lanes for the player —
// the composed discovery (goods universe -> the shared BestSinks/BestSources scanners ->
// assembleLongHaulCandidates -> rankLongHaulLanes). One port so the worker is testable.
type laneDiscoverer interface {
	DiscoverLanes(ctx context.Context, playerID int) ([]pricedLongHaulLane, error)
}

// longHaulTreasuryReader live-reads treasury for the envelope cushion fence (the same narrow
// shape idle-arb's TreasuryReader uses).
type longHaulTreasuryReader interface {
	LiveTreasury(ctx context.Context) (int64, error)
}

// shipLoader loads one hull for its cargo + location (for sizing + the mid-trip re-derive).
type shipLoader interface {
	LoadShip(ctx context.Context, shipSymbol string, playerID int) (*navigation.Ship, error)
}

// RunLongHaulArbCommand launches one per-hull long-haul worker (built from the fleet
// coordinator's LongHaulLaunchSpec). Iterations=-1 runs continuous episodes.
type RunLongHaulArbCommand struct {
	ShipSymbol  string
	AgentSymbol string
	PlayerID    int
	ContainerID string
	Iterations  int
	PerHaulCap  int64
	// TotalExposureCap is carried for parity with the launch spec. The coordinator does NOT
	// enforce a total in-flight ceiling (Admiral uncap order), so the worker itself
	// only applies the per-haul cap + cushion fence.
	TotalExposureCap int64
	// MinMargin is the per-unit floor handed to each leg's reused arb guard; 0 leaves the
	// executor's own non-positive-margin rejection + sell floor as the backstop (the lane
	// already cleared the ranking's min-spread floor).
	MinMargin int
	// IdleBackoffSecs is the wait after a no-lane episode; <=0 uses defaultLongHaulIdleBackoff.
	IdleBackoffSecs int
}

// RunLongHaulArbHandler runs the per-hull episode loop. It owns the orchestration; every
// primitive (execution, travel, discovery, treasury) is a reused component behind a port.
type RunLongHaulArbHandler struct {
	loader     shipLoader
	discoverer laneDiscoverer
	legs       directedLegExecutor
	reposition hullRepositioner
	treasury   longHaulTreasuryReader
	// absorptionLedger is the worker's live sink-depth consult: each lane's headroom is its
	// VolumeCap minus what OTHER engines already hold in flight against the same sink. nil →
	// unbounded by absorption (the pre-sp-kw2em behaviour, kept for tests that drive the worker
	// without a ledger).
	absorptionLedger longHaulAbsorptionReader
}

// longHaulAbsorptionReader is the one method the worker needs from the absorption ledger,
// declared here at the consumer so this package depends on the read rather than the whole
// ledger surface. *persistence.AbsorptionLedgerGORM satisfies it.
type longHaulAbsorptionReader interface {
	Outstanding(ctx context.Context, playerID int) (map[absorption.LaneKey]absorption.KeyOccupancy, error)
}

// NewRunLongHaulArbHandler wires the worker with its reused-component ports.
func NewRunLongHaulArbHandler(loader shipLoader, discoverer laneDiscoverer, legs directedLegExecutor, reposition hullRepositioner, treasury longHaulTreasuryReader) *RunLongHaulArbHandler {
	return &RunLongHaulArbHandler{loader: loader, discoverer: discoverer, legs: legs, reposition: reposition, treasury: treasury}
}

// SetAbsorptionLedger wires the live sink-depth consult (sp-kw2em). It replaces
// SetAbsorptionHeadroom, which took a per-lane func and had ZERO call sites for its whole
// lifetime — so the clamp it fed was never once applied. Three things were wrong with that
// signature and all three are why it was never wired: it carried no context (so the fail-closed
// warning every sibling consult emits had nowhere to go), no player id, and it was invoked once
// per ranked candidate inside selectHauls, which would have meant a database round trip per lane
// per episode. This takes the ledger itself and reads it ONCE per episode, matching the
// trade-route, tour and idle-arb consults.
func (h *RunLongHaulArbHandler) SetAbsorptionLedger(ledger longHaulAbsorptionReader) {
	h.absorptionLedger = ledger
}

// absorptionHeadroomFn returns the per-lane headroom consult for ONE episode, from a single
// batched ledger read.
//
// The headroom is VolumeCap minus what other engines already hold in flight against the same
// sink. It only ever REDUCES the sized buy — achievableUnits folds it in with a min — so this
// can make a buy smaller or leave it unchanged, never larger (RULINGS #4).
//
// Note what it does and does not bound. OptimalUnits is already clamped to VolumeCap upstream,
// so for a hull trading alone this consult is a no-op by construction. What it catches is the
// case VolumeCap cannot see: several engines dumping into the SAME sink at the same time, where
// each one's buy looks individually within depth and their sum is not.
//
// FAIL-CLOSED on an unreadable ledger: every lane sizes to zero and the episode trades nothing.
// That matches the idle-arb and trade-route consults, and it is the only safe direction for a
// guard whose job is bounding a spend — a consult that cannot read must not wave the buy through.
//
// A nil ledger returns nil, which selectHauls reads as "not consulted" (headroom -1) and leaves
// sizing exactly as it was before this was wired.
func (h *RunLongHaulArbHandler) absorptionHeadroomFn(ctx context.Context, playerID int) func(lane trading.ArbitrageLane) int {
	if h.absorptionLedger == nil {
		return nil
	}

	pools, err := h.absorptionLedger.Outstanding(ctx, playerID)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf(
			"Long-haul absorption consult: ledger read failed, sizing every lane to zero this episode (fail-closed): %v", err), map[string]interface{}{
			"action": "longhaul_absorption_unreadable",
		})
		return func(trading.ArbitrageLane) int { return 0 }
	}

	return func(lane trading.ArbitrageLane) int {
		occ := pools[absorption.LaneKey{Waypoint: lane.DestWaypoint, Good: lane.Good, Side: absorption.SideSell}]
		// A live recovery shadow on the sink means an earlier dump is still being absorbed.
		// Zero headroom, matching the trade-route consult's outright block on the same signal.
		if occ.RecoveringResidual > 0 {
			return 0
		}
		remaining := lane.VolumeCap - occ.PlannedUnits
		if remaining < 0 {
			return 0
		}
		return remaining
	}
}

// Handle runs continuous episodes until the context is cancelled (Iterations=-1) or the
// finite budget is exhausted. A no-lane episode backs off before re-scanning.
func (h *RunLongHaulArbHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*RunLongHaulArbCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}
	backoff := time.Duration(cmd.IdleBackoffSecs) * time.Second
	if backoff <= 0 {
		backoff = defaultLongHaulIdleBackoff
	}
	iterations := 0
	for {
		select {
		case <-ctx.Done():
			return &RunLongHaulArbResponse{Episodes: iterations}, ctx.Err()
		default:
		}
		didWork, err := h.runEpisode(ctx, cmd)
		iterations++
		if err != nil {
			common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Long-haul episode error for %s: %v", cmd.ShipSymbol, err), map[string]interface{}{
				"action": "longhaul_episode_error", "ship_symbol": cmd.ShipSymbol,
			})
		}
		if cmd.Iterations >= 0 && iterations >= cmd.Iterations {
			return &RunLongHaulArbResponse{Episodes: iterations}, nil
		}
		if !didWork {
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return &RunLongHaulArbResponse{Episodes: iterations}, ctx.Err()
			}
		}
	}
}

// RunLongHaulArbResponse reports how many episodes ran (observed on cancellation / finite budget).
type RunLongHaulArbResponse struct {
	Episodes int
}

// runEpisode runs ONE episode — the unit the tests drive. Returns didWork=true when it
// executed a leg (so the loop retries immediately), false when it found nothing to do (the
// loop backs off).
func (h *RunLongHaulArbHandler) runEpisode(ctx context.Context, cmd *RunLongHaulArbCommand) (bool, error) {
	logger := common.LoggerFromContext(ctx)

	hull, err := h.loader.LoadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return false, err
	}

	// MID-TRIP RE-DERIVE (stateless recovery, sp-m3122 allowance): a hull holding cargo from an
	// interrupted episode RESUMES selling it toward its best sink — never re-derives a fresh buy
	// on top of a laden hull. The reused arb executor's holding->skip-buy resume does the sell.
	if held, good := heldCargo(hull); held > 0 {
		return h.resumeHeldCargo(ctx, cmd, good, held, logger)
	}

	// FRESH EPISODE.
	envelope := h.readEnvelope(ctx, cmd)
	ranked, err := h.discoverer.DiscoverLanes(ctx, cmd.PlayerID)
	if err != nil {
		return false, fmt.Errorf("long-haul discovery failed: %w", err)
	}
	hauls := selectHauls(ranked, hull.AvailableCargoSpace(), envelope, h.absorptionHeadroomFn(ctx, cmd.PlayerID))
	if len(hauls) == 0 {
		logger.Log("INFO", fmt.Sprintf("Long-haul %s: no lane clears the floor+envelope this scan — idling", cmd.ShipSymbol), map[string]interface{}{
			"action": "longhaul_no_lane", "ship_symbol": cmd.ShipSymbol,
		})
		return false, nil
	}

	// Try the viable lanes in realized-$/hr order: reposition to the source (cross-gate), then run
	// the OUT leg on the FIRST lane the hull can actually reach. The engine deliberately ranks far
	// multi-hop exotic lanes, some structurally unreachable for this hull's supply; a gate-
	// UNROUTABLE source is SKIPPED for the next lane rather than error-returned: error-returning
	// on the first pick loops the same deterministic top lane forever, capturing zero value.
	// A NON-unroutable reposition failure (a transient API blip) still fails the episode so the
	// next cycle retries. Reachability is checked BEFORE any buy, so RULINGS #4 is untouched —
	// still no spend without a completed reposition.
	for _, haul := range hauls {
		lane, units := haul.lane, haul.units
		if err := h.reposition.RepositionToWaypoint(ctx, cmd.ShipSymbol, lane.Lane.SourceWaypoint, cmd.PlayerID); err != nil {
			if errors.Is(err, gategraph.ErrUnroutable) {
				logger.Log("INFO", fmt.Sprintf("Long-haul %s: top lane source %s is gate-unroutable — skipping to the next reachable lane", cmd.ShipSymbol, lane.Lane.SourceWaypoint), map[string]interface{}{
					"action": "longhaul_source_unroutable", "ship_symbol": cmd.ShipSymbol, "source": lane.Lane.SourceWaypoint, "good": lane.Lane.Good,
				})
				continue
			}
			return false, fmt.Errorf("reposition to source %s failed: %w", lane.Lane.SourceWaypoint, err)
		}
		logger.Log("INFO", fmt.Sprintf(
			"Long-haul %s: %s %s->%s x%d (realized ~%d/hr, %d hops)",
			cmd.ShipSymbol, lane.Lane.Good, lane.Lane.SourceWaypoint, lane.Lane.DestWaypoint, units, int(lane.RealizedCreditsPerHour), lane.GateHops),
			map[string]interface{}{
				"action": "longhaul_out_leg", "ship_symbol": cmd.ShipSymbol, "good": lane.Lane.Good,
				"source": lane.Lane.SourceWaypoint, "sink": lane.Lane.DestWaypoint, "units": units,
			})
		if _, err := h.legs.RunLeg(ctx, h.legCommand(cmd, lane.Lane, units, envelope)); err != nil {
			return true, fmt.Errorf("out leg %s->%s failed: %w", lane.Lane.SourceWaypoint, lane.Lane.DestWaypoint, err)
		}

		// OPPORTUNISTIC BACKHAUL: re-rank restricted to a lane sourced near where the hull now sits
		// (the sink system); if one clears the envelope, run it — else deadhead (the hull stays, the
		// next episode discovers from here). Best-effort: a backhaul failure never fails the episode.
		h.runBackhaul(ctx, cmd, lane.Lane.DestWaypoint, logger)
		return true, nil
	}

	// Every viable lane this scan was gate-unroutable from where the hull sits — idle and back off
	// rather than error-loop (sp-e059j). The far ground recovers/re-ranks over the idle window.
	logger.Log("INFO", fmt.Sprintf("Long-haul %s: no viable lane is reachable this scan (all unroutable) — idling", cmd.ShipSymbol), map[string]interface{}{
		"action": "longhaul_all_unroutable", "ship_symbol": cmd.ShipSymbol,
	})
	return false, nil
}

// resumeHeldCargo sells a hull's held cargo toward the good's best current sink (the reused
// executor resumes: it sees the good aboard, skips the buy, sells). If no sink is discoverable
// for the held good, it logs for the held-cargo offload path (reused, wired at execution) —
// never re-buying on a laden hull.
func (h *RunLongHaulArbHandler) resumeHeldCargo(ctx context.Context, cmd *RunLongHaulArbCommand, good string, held int, logger common.ContainerLogger) (bool, error) {
	ranked, err := h.discoverer.DiscoverLanes(ctx, cmd.PlayerID)
	if err != nil {
		return false, fmt.Errorf("long-haul resume discovery failed: %w", err)
	}
	for _, lane := range ranked {
		if lane.Lane.Good != good {
			continue
		}
		logger.Log("INFO", fmt.Sprintf("Long-haul %s: resuming — selling %d held %s to %s (no re-buy)", cmd.ShipSymbol, held, good, lane.Lane.DestWaypoint), map[string]interface{}{
			"action": "longhaul_resume_sell", "ship_symbol": cmd.ShipSymbol, "good": good, "held": held, "sink": lane.Lane.DestWaypoint,
		})
		leg := h.legCommand(cmd, lane.Lane, held, h.readEnvelope(ctx, cmd))
		if _, err := h.legs.RunLeg(ctx, leg); err != nil {
			return true, fmt.Errorf("resume sell of %d %s failed: %w", held, good, err)
		}
		return true, nil
	}
	logger.Log("WARNING", fmt.Sprintf("Long-haul %s: holding %d %s but no sink discoverable — offload path (held-cargo-aware) must clear it", cmd.ShipSymbol, held, good), map[string]interface{}{
		"action": "longhaul_held_no_sink", "ship_symbol": cmd.ShipSymbol, "good": good, "held": held,
	})
	return false, nil
}

// runBackhaul re-ranks and runs the best lane SOURCED in the sink's system (origin-near-sink),
// if it clears the envelope — else deadheads. Best-effort (a backhaul failure is logged, never
// propagated: the out leg already completed).
func (h *RunLongHaulArbHandler) runBackhaul(ctx context.Context, cmd *RunLongHaulArbCommand, sinkWaypoint string, logger common.ContainerLogger) {
	sinkSystem := shared.ExtractSystemSymbol(sinkWaypoint)
	ranked, err := h.discoverer.DiscoverLanes(ctx, cmd.PlayerID)
	if err != nil {
		return
	}
	nearSource := make([]pricedLongHaulLane, 0, len(ranked))
	for _, lane := range ranked {
		if shared.ExtractSystemSymbol(lane.Lane.SourceWaypoint) == sinkSystem {
			nearSource = append(nearSource, lane)
		}
	}
	envelope := h.readEnvelope(ctx, cmd)
	hull, err := h.loader.LoadShip(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return
	}
	// Iterate the viable near-sink lanes in $/hr order, mirroring the OUT leg's reachability
	// fallback (sp-e059j): a gate-UNROUTABLE near-source is skipped for the next candidate rather
	// than deadheading on the first one — the backhaul is opportunistic, so an unreachable top
	// pick must not forfeit a reachable second. Best-effort throughout: any failure just deadheads.
	for _, haul := range selectHauls(nearSource, hull.AvailableCargoSpace(), envelope, h.absorptionHeadroomFn(ctx, cmd.PlayerID)) {
		lane, units := haul.lane, haul.units
		if err := h.reposition.RepositionToWaypoint(ctx, cmd.ShipSymbol, lane.Lane.SourceWaypoint, cmd.PlayerID); err != nil {
			if errors.Is(err, gategraph.ErrUnroutable) {
				continue
			}
			return
		}
		logger.Log("INFO", fmt.Sprintf("Long-haul %s: backhaul %s %s->%s x%d", cmd.ShipSymbol, lane.Lane.Good, lane.Lane.SourceWaypoint, lane.Lane.DestWaypoint, units), map[string]interface{}{
			"action": "longhaul_backhaul", "ship_symbol": cmd.ShipSymbol, "good": lane.Lane.Good, "units": units,
		})
		if _, err := h.legs.RunLeg(ctx, h.legCommand(cmd, lane.Lane, units, envelope)); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Long-haul %s: backhaul leg failed (out leg already booked): %v", cmd.ShipSymbol, err), map[string]interface{}{
				"action": "longhaul_backhaul_failed", "ship_symbol": cmd.ShipSymbol,
			})
		}
		return
	}
	logger.Log("INFO", fmt.Sprintf("Long-haul %s: no reachable backhaul from %s clears the envelope — deadheading", cmd.ShipSymbol, sinkSystem), map[string]interface{}{
		"action": "longhaul_deadhead", "ship_symbol": cmd.ShipSymbol, "sink_system": sinkSystem,
	})
}

// readEnvelope live-reads treasury into the money envelope (per-haul cap + 200k cushion fence).
// A treasury read failure yields the fail-closed fence (spendCeiling 0 -> the worker trades
// nothing that episode) rather than spending blind.
func (h *RunLongHaulArbHandler) readEnvelope(ctx context.Context, cmd *RunLongHaulArbCommand) longHaulEnvelope {
	if h.treasury == nil {
		return newLongHaulEnvelope(common.ReserveFloorGate{}, cmd.PerHaulCap)
	}
	balance, err := h.treasury.LiveTreasury(ctx)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Long-haul %s: live treasury read failed — trading nothing this episode (fail-closed): %v", cmd.ShipSymbol, err), map[string]interface{}{
			"action": "longhaul_treasury_read_failed", "ship_symbol": cmd.ShipSymbol,
		})
		return newLongHaulEnvelope(unreadableLongHaulFence(), cmd.PerHaulCap)
	}
	return newLongHaulEnvelope(newLongHaulFence(balance), cmd.PerHaulCap)
}

// legCommand maps a chosen lane + sized units onto a directed leg for the reused executor.
func (h *RunLongHaulArbHandler) legCommand(cmd *RunLongHaulArbCommand, lane trading.ArbitrageLane, units int, envelope longHaulEnvelope) directedLegCommand {
	return directedLegCommand{
		ShipSymbol:  cmd.ShipSymbol,
		Good:        lane.Good,
		BuyAt:       lane.SourceWaypoint,
		SellAt:      lane.DestWaypoint,
		Units:       units,
		PerHaulCap:  envelope.perHaulCap,
		MinMargin:   cmd.MinMargin,
		PlayerID:    cmd.PlayerID,
		ContainerID: cmd.ContainerID,
	}
}

// heldCargo reports the largest single-good tranche a hull is holding (units, good), or (0, "")
// when empty — the mid-trip re-derive signal.
func heldCargo(ship *navigation.Ship) (int, string) {
	cargo := ship.Cargo()
	if cargo == nil {
		return 0, ""
	}
	bestUnits, bestGood := 0, ""
	for _, item := range cargo.Inventory {
		if item != nil && item.Units > bestUnits {
			bestUnits, bestGood = item.Units, item.Symbol
		}
	}
	return bestUnits, bestGood
}
