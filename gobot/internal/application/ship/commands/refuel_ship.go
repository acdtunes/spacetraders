package commands

import (
	"context"
	"fmt"

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

	ship, err := h.loadShip(ctx, cmd)
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

	refuelResult, err := h.refuelShipViaAPI(ctx, ship, cmd)
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

	// OPTIMIZATION: Skip balance fetch (saves 1 API call per refuel)
	// Ledger entries will have balance=0 but transaction amounts are still tracked
	go h.recordRefuelTransaction(ctx, cmd, response, 0)

	return response, nil
}

func (h *RefuelShipHandler) loadShip(ctx context.Context, cmd *types.RefuelShipCommand) (*navigation.Ship, error) {
	// OPTIMIZATION: Use ship if provided (avoids API call)
	if cmd.Ship != nil {
		return cmd.Ship, nil
	}
	// Fall back to API fetch (backward compatibility)
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, cmd.PlayerID)
	if err != nil {
		return nil, fmt.Errorf("ship not found: %w", err)
	}
	return ship, nil
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


// recordRefuelTransaction records the refuel transaction in the ledger
func (h *RefuelShipHandler) recordRefuelTransaction(
	ctx context.Context,
	cmd *types.RefuelShipCommand,
	response *types.RefuelShipResponse,
	balanceBefore int,
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

	// Calculate balance after
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
		PlayerID:        cmd.PlayerID.Value(),
		TransactionType: "REFUEL",
		Amount:          -response.CreditsCost, // Negative for expense
		BalanceBefore:   balanceBefore,
		BalanceAfter:    balanceAfter,
		Description:     fmt.Sprintf("Refueled ship %s", cmd.ShipSymbol),
		Metadata:        metadata,
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
