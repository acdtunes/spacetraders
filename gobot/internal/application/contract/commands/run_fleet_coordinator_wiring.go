package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	playerQueries "github.com/andrescamacho/spacetraders-go/internal/application/player/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// newIdleArbDispatcher builds the idle-arb dispatcher wired to this coordinator's
// repositories, treasury reader and live standby resolution.
func (h *RunFleetCoordinatorHandler) newIdleArbDispatcher(cmd *RunFleetCoordinatorCommand) *appContract.IdleArbDispatcher {
	// Post-leg re-homing reuses the coordinator's own balanced-standby homing
	// (HomeShipCommand) — never a parallel homing path (RULINGS #7). Empty
	// StandbyStations leaves re-homing off, exactly as it leaves the
	// contract-handoff homing off.
	homer := &mediatorShipHomer{
		mediator:          h.fleetPoolManager.GetMediator(),
		shipRepo:          h.shipRepo,
		playerID:          cmd.PlayerID,
		fleet:             dedicatedFleetContract,
		placementProvider: h.standbyPlacementProvider,
	}
	dispatcher := appContract.NewIdleArbDispatcher(
		h.shipRepo,
		h.marketRepo,
		h.graphProvider,
		h.idleArbLauncher,
		homer,
		appContract.NewActiveContractGoods(h.contractRepo),
		h.clock,
		cmd.PlayerID,
		dedicatedFleetContract,
		appContract.IdleArbConfig{
			ReserveHulls:     cmd.IdleArbReserveHulls,
			HubRadius:        cmd.IdleArbHubRadius,
			LeashRadius:      cmd.IdleArbLeashRadius,
			MaxLegDuration:   time.Duration(cmd.IdleArbMaxLegSecs) * time.Second,
			MaxSpendPerLeg:   cmd.IdleArbMaxSpend,
			MinMarginPerUnit: cmd.IdleArbMinMargin,
			// Percent → fraction (0 → WithDefaults applies the 0.80 default).
			MarginVerifyFraction: float64(cmd.IdleArbMarginVerifyPct) / 100.0,
			Blacklist:            cmd.IdleArbBlacklist,
			StandbyStations:      cmd.StandbyStations,
			Interval:             time.Duration(cmd.IdleArbIntervalSecs) * time.Second,
			// Lane mutex recovery hold (0 → WithDefaults applies 20min).
			RecoveryHold: time.Duration(cmd.IdleArbRecoveryHoldSecs) * time.Second,
			// Per-trip profitability floor. Percent → fraction (0 →
			// WithDefaults applies 100/u, 0.20, 35/u fuel).
			MinNetProfitPerUnit: cmd.IdleArbMinNetProfit,
			NetProfitFraction:   float64(cmd.IdleArbNetProfitPct) / 100.0,
			FuelCostPerUnit:     cmd.IdleArbFuelCostPerUnit,
		},
	)
	// Wires the cross-engine absorption ledger so the dispatcher consults it
	// (skip:reserved) and records launched legs. Inert when unwired.
	dispatcher.SetAbsorptionLedger(h.absorptionLedger, h.absorptionPlannedTTLSlack)
	// Live-treasury source for the working-capital reserve gate, so the pass's concurrent legs can
	// never collectively drain treasury below the immutable reserve. Reads the same live balance
	// the contract park path reads; a read failure fails the gate CLOSED, holding the pass.
	dispatcher.SetTreasuryReader(&mediatorTreasuryReader{
		mediator: h.fleetPoolManager.GetMediator(),
		playerID: cmd.PlayerID,
	})
	// LIVE hub set: resolves the CURRENT standby set each pass from this coordinator's container
	// config, so `fleet hub add|remove` re-homes idle hulls across the new set with no restart.
	// Falls back to cmd.StandbyStations on a read failure or with no provider.
	dispatcher.SetStandbyResolver(func(resolveCtx context.Context) []string {
		return appContract.ResolveStandbyStations(resolveCtx, common.LoggerFromContext(resolveCtx), h.standbyProvider, cmd.ContainerID, cmd.PlayerID.Value(), cmd.StandbyStations)
	})
	// LIVE fixed-placement auto-resolution: when no `fleet hub` is pinned, the standing
	// re-home sweep resolves its standby set from the ≤6 fixed placement slots (sp-bu6ma /
	// sp-mtgje) via the SAME provider the between-legs hook uses, so a SITTING idle pool homes
	// to the fixed one-per-waypoint slots instead of piling. Nil-safe (byte-identical unwired).
	dispatcher.SetStandbyPlacementProvider(h.standbyPlacementProvider)
	return dispatcher
}

// mediatorShipHomer implements appContract.ShipHomer by dispatching the EXISTING balanced-standby
// HomeShipCommand through the mediator: the idle-arb dispatcher's post-leg re-home reuses the
// coordinator's own homing machinery verbatim, with the same standby-station set and fleet-peer
// list the contract-handoff homing uses (RULINGS #7: no parallel homing algorithm).
//
// Both membership inputs are LIVE, not frozen launch snapshots. The standby set is passed in per
// re-home from the CURRENT hub set in container config, so `fleet hub add|remove` re-homes across
// the new set with no restart; the fleet-peer list is resolved live from the dedicated_fleet tag,
// so a hull added after launch counts toward standby-station occupancy and a removed one does not.
//
// Navigation runs FIRE-AND-FORGET, mirroring the coordinator's own homing hook: HomeShipCommand
// blocks until the hull ARRIVES, so a synchronous call would stall the dispatcher tick for the full
// flight. HomeShip returns as soon as the home is DISPATCHED; the detached goroutine carries the
// container logger on a background context that outlives the request ctx, and logs a homing failure
// at WARNING rather than surfacing it, re-homing being best-effort.
type mediatorShipHomer struct {
	mediator common.Mediator
	shipRepo navigation.ShipRepository
	playerID shared.PlayerID
	fleet    string
	// placementProvider resolves the ≤6 FIXED placement slots and auto-drives the standby set
	// from them when the passed live set is empty — the SAME provider the coordinator's
	// between-legs homing uses, so idle-arb re-homes track contract homing (RULINGS #7, one
	// homing algorithm). Nil-safe: the passed set is kept unchanged.
	placementProvider appContract.StandbyPlacementProvider
}

// HomeShip re-homes the hull to the LIVE standby set the dispatcher resolved
// this pass, passed in rather than frozen on the homer, so an idle-arb re-home
// tracks a `fleet hub add|remove` with no restart — the same live set the
// coordinator's between-legs homing uses.
func (m *mediatorShipHomer) HomeShip(ctx context.Context, shipSymbol string, standbyStations []string) error {
	logger := common.LoggerFromContext(ctx)
	// When the passed live set is empty, auto-drive it from the ≤6 FIXED placement slots — the SAME
	// resolution the coordinator's between-legs homing uses (nil-safe → the passed set unchanged). The
	// homing zips this hull to its slot by symbol against the dedicated roster (FleetShips) — no demand.
	standbyStations = appContract.ResolveStandbyForHoming(ctx, logger, m.placementProvider, m.playerID.Value(), standbyStations)
	homeCmd := &HomeShipCommand{
		ShipSymbol:      shipSymbol,
		PlayerID:        m.playerID,
		StandbyStations: standbyStations,
		FleetShips:      resolveDedicatedMembersForHoming(ctx, logger, m.shipRepo, m.playerID, m.fleet, nil),
	}
	opCtx := shared.OperationContextFromContext(ctx)
	go func() {
		// Background context, because the dispatch ctx is cancelled when the coordinator stops
		// and this flight must outlive it. Cancellation is the only thing that must not cross
		// the boundary: the container logger and the operation the work belongs to both do, or
		// the fuel the re-home burns is spend nobody can attribute.
		homeCtx := shared.WithOperationContext(common.WithLogger(context.Background(), logger), opCtx)
		if _, err := m.mediator.Send(homeCtx, homeCmd); err != nil {
			logger.Log("WARNING", fmt.Sprintf("Idle-arb re-home: homing %s failed: %v", shipSymbol, err), nil)
		}
	}()
	return nil
}

// mediatorTreasuryReader reads live treasury for the idle-arb dispatcher's working-
// capital reserve gate (sp-zq635 §4a) over the mediator's GetPlayerQuery — the same live
// balance the contract park path reads, bound to this coordinator's player.
type mediatorTreasuryReader struct {
	mediator common.Mediator
	playerID shared.PlayerID
}

func (r *mediatorTreasuryReader) LiveTreasury(ctx context.Context) (int64, error) {
	pid := r.playerID.Value()
	resp, err := r.mediator.Send(ctx, &playerQueries.GetPlayerQuery{PlayerID: &pid})
	if err != nil {
		return 0, err
	}
	playerResp, ok := resp.(*playerQueries.GetPlayerResponse)
	if !ok || playerResp.Player == nil {
		return 0, fmt.Errorf("idle-arb treasury read: unexpected GetPlayer response %T", resp)
	}
	return int64(playerResp.Player.Credits), nil
}

var _ appContract.ShipHomer = (*mediatorShipHomer)(nil)

var _ appContract.TreasuryReader = (*mediatorTreasuryReader)(nil)
