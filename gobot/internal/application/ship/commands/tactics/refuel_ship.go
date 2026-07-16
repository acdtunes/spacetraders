package tactics

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/types"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// RefuelShipHandler - Handles refuel ship commands
type RefuelShipHandler struct {
	shipRepo   navigation.ShipRepository
	playerRepo player.PlayerRepository
	apiClient  domainPorts.APIClient
	mediator   common.Mediator
}

// NewRefuelShipHandler creates a new refuel ship handler
func NewRefuelShipHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient domainPorts.APIClient,
	mediator common.Mediator,
) *RefuelShipHandler {
	return &RefuelShipHandler{
		shipRepo:   shipRepo,
		playerRepo: playerRepo,
		apiClient:  apiClient,
		mediator:   mediator,
	}
}

// Handle executes the refuel ship command
func (h *RefuelShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*types.RefuelShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type")
	}

	ship, err := types.LoadShip(ctx, h.shipRepo, cmd)
	if err != nil {
		return nil, err
	}

	if err := h.validateAtFuelStation(ship); err != nil {
		return nil, err
	}

	if err := h.ensureShipDockedForRefuel(ctx, ship, cmd.PlayerID); err != nil {
		return nil, err
	}

	fuelBefore := ship.Fuel().Current

	refuelResult, err := h.refuelWithDockSelfHeal(ctx, ship, cmd)
	if err != nil {
		return nil, err
	}

	response := h.buildRefuelResponse(ship, fuelBefore, refuelResult)

	// Record fuel purchase metrics
	metrics.RecordFuelPurchase(
		cmd.PlayerID.Value(),
		ship.CurrentLocation().Symbol,
		response.FuelAdded,
	)

	// No separate balance fetch: the refuel response carries the agent's
	// post-transaction credits in-band, which is the authoritative balance_after
	// for the ledger. When absent (older API/mock) the ledger reconstructs from
	// the running chain (balance_before=0 baseline).
	go h.recordRefuelTransaction(ctx, cmd, response, refuelResult.AgentCredits)

	return response, nil
}

func (h *RefuelShipHandler) validateAtFuelStation(ship *navigation.Ship) error {
	if !ship.CurrentLocation().HasFuel {
		return fmt.Errorf("waypoint does not have fuel station")
	}
	return nil
}

func (h *RefuelShipHandler) ensureShipDockedForRefuel(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	stateChanged, err := ship.EnsureDocked()
	if err != nil {
		return err
	}

	if stateChanged {
		if err := h.shipRepo.Dock(ctx, ship, playerID); err != nil {
			return fmt.Errorf("failed to dock ship: %w", err)
		}
	}
	return nil
}

func (h *RefuelShipHandler) refuelShipViaAPI(ctx context.Context, ship *navigation.Ship, cmd *types.RefuelShipCommand) (*navigation.RefuelResult, error) {
	result, err := h.shipRepo.Refuel(ctx, ship, cmd.PlayerID, cmd.Units)
	if err != nil {
		return nil, fmt.Errorf("failed to refuel ship: %w", err)
	}
	return result, nil
}

// refuelWithDockSelfHeal refuels, self-healing a wrong idempotent-dock skip
// (sp-yd84 SAFETY item 2). The idempotent dock optimization (CUT 1) trusts the
// in-memory NavStatus; if that has drifted from server reality, the skipped dock
// leaves the ship undocked and the refuel is rejected with the API's 4214/4244
// "must be docked". Rather than fail the leg, issue a REAL dock (h.shipRepo.Dock
// fires the API unconditionally, correcting the drift) and retry the refuel
// exactly once. Any other error — or a second failure after re-docking — is
// propagated unchanged so a genuine failure (no fuel station, insufficient
// credits) is never masked. Mirrors the codebase's existing recover-then-retry
// idioms (production_executor.isTransientDockStateError, jump_ship's orbit retry).
func (h *RefuelShipHandler) refuelWithDockSelfHeal(ctx context.Context, ship *navigation.Ship, cmd *types.RefuelShipCommand) (*navigation.RefuelResult, error) {
	result, err := h.refuelShipViaAPI(ctx, ship, cmd)
	if err == nil {
		return result, nil
	}
	if !isMustBeDockedError(err) {
		return nil, err
	}

	logging.LoggerFromContext(ctx).Log("WARNING", "Refuel rejected as not-docked (4214/4244) - docking live and retrying (idempotent-skip drift self-heal, sp-yd84)", map[string]interface{}{
		"ship_symbol": ship.ShipSymbol(),
		"action":      "refuel_dock_self_heal",
		"waypoint":    ship.CurrentLocation().Symbol,
	})

	if derr := h.shipRepo.Dock(ctx, ship, cmd.PlayerID); derr != nil {
		return nil, fmt.Errorf("self-heal dock after a not-docked refuel rejection failed: %w", derr)
	}
	return h.refuelShipViaAPI(ctx, ship, cmd)
}

// isMustBeDockedError reports whether err is the recoverable "ship must be
// docked" precondition — the live API's 4214/4244 codes — rather than a genuine
// failure (insufficient credits, no fuel station). Only these are safe to retry
// after re-docking. Mirrors production_executor.isTransientDockStateError.
func isMustBeDockedError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "must be docked") ||
		strings.Contains(msg, "4214") ||
		strings.Contains(msg, "4244")
}

func (h *RefuelShipHandler) buildRefuelResponse(ship *navigation.Ship, fuelBefore int, refuelResult *navigation.RefuelResult) *types.RefuelShipResponse {
	fuelAdded := ship.Fuel().Current - fuelBefore

	return &types.RefuelShipResponse{
		FuelAdded:    fuelAdded,
		CurrentFuel:  ship.Fuel().Current,
		CreditsCost:  refuelResult.CreditsCost, // Use actual cost from API
		Status:       "refueled",
		FuelCapacity: ship.Fuel().Capacity,
	}
}

// recordRefuelTransaction records the refuel transaction in the ledger.
// authoritativeBalance, when non-nil, is the agent's post-refuel credits as
// reported in-band by the refuel API response; the ledger anchors on it.
func (h *RefuelShipHandler) recordRefuelTransaction(
	ctx context.Context,
	cmd *types.RefuelShipCommand,
	response *types.RefuelShipResponse,
	authoritativeBalance *int,
) {
	logger := logging.LoggerFromContext(ctx)

	// Skip recording if cost is zero (free refuel or already full)
	// Transaction validation requires amount != 0
	if response.CreditsCost == 0 {
		logger.Log("DEBUG", "Skipping ledger entry for zero-cost refuel", map[string]interface{}{
			"ship":       cmd.ShipSymbol,
			"fuel_added": response.FuelAdded,
		})
		return
	}

	// Zero baseline: when authoritativeBalance is nil the ledger reconstructs
	// balance_after from the running chain; when set it re-anchors to API truth.
	const balanceBefore = 0
	balanceAfter := balanceBefore - response.CreditsCost

	// Fetch player to get agent symbol
	playerData, err := h.playerRepo.FindByID(ctx, cmd.PlayerID)
	agentSymbol := "UNKNOWN"
	if err == nil && playerData != nil {
		agentSymbol = playerData.AgentSymbol
	}

	// Build metadata
	metadata := map[string]interface{}{
		"agent":       agentSymbol,
		"ship_symbol": cmd.ShipSymbol,
		"fuel_added":  response.FuelAdded,
	}

	// Create record transaction command
	recordCmd := &ledgerCommands.RecordTransactionCommand{
		PlayerID:             cmd.PlayerID.Value(),
		TransactionType:      "REFUEL",
		Amount:               -response.CreditsCost, // Negative for expense
		BalanceBefore:        balanceBefore,
		BalanceAfter:         balanceAfter,
		AuthoritativeBalance: authoritativeBalance,
		Description:          fmt.Sprintf("Refueled ship %s", cmd.ShipSymbol),
		Metadata:             metadata,
	}

	// Propagate operation context if present in the context
	if opCtx := shared.OperationContextFromContext(ctx); opCtx != nil && opCtx.IsValid() {
		recordCmd.RelatedEntityType = "container"
		recordCmd.RelatedEntityID = opCtx.ContainerID
		recordCmd.OperationType = opCtx.NormalizedOperationType()
	} else {
		// No operation context - mark as manual transaction
		recordCmd.OperationType = "manual"
	}

	// Record transaction via mediator
	_, err = h.mediator.Send(context.Background(), recordCmd)
	if err != nil {
		// Log error but don't fail the operation
		logger.Log("ERROR", "Failed to record refuel transaction in ledger", map[string]interface{}{
			"error":     err.Error(),
			"ship":      cmd.ShipSymbol,
			"cost":      response.CreditsCost,
			"player_id": cmd.PlayerID.Value(),
		})
	} else {
		logger.Log("DEBUG", "Refuel transaction recorded in ledger", map[string]interface{}{
			"ship": cmd.ShipSymbol,
			"cost": response.CreditsCost,
		})
	}
}
