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

	// Fetch current credits (balance before)
	balanceBefore, err := h.fetchCurrentCredits(ctx)
	if err != nil {
		// Log warning but don't fail the operation
		logger := logging.LoggerFromContext(ctx)
		logger.Log("WARN", "Failed to fetch credits before refuel, ledger entry will not be recorded", map[string]interface{}{
			"error": err.Error(),
			"ship":  cmd.ShipSymbol,
		})
	}

	fuelBefore := ship.Fuel().Current

	if err := h.refuelShipViaAPI(ctx, ship, cmd); err != nil {
		return nil, err
	}

	response := h.buildRefuelResponse(ship, fuelBefore)

	// Record fuel purchase metrics
	metrics.RecordFuelPurchase(
		cmd.PlayerID.Value(),
		ship.CurrentLocation().Symbol,
		response.FuelAdded,
	)

	// Record transaction asynchronously (non-blocking)
	if balanceBefore > 0 { // Only record if we successfully fetched balance
		go h.recordRefuelTransaction(ctx, cmd, response, balanceBefore)
	}

	return response, nil
}

func (h *RefuelShipHandler) loadShip(ctx context.Context, cmd *types.RefuelShipCommand) (*navigation.Ship, error) {
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

func (h *RefuelShipHandler) refuelShipViaAPI(ctx context.Context, ship *navigation.Ship, cmd *types.RefuelShipCommand) error {
	if err := h.shipRepo.Refuel(ctx, ship, cmd.PlayerID, cmd.Units); err != nil {
		return fmt.Errorf("failed to refuel ship: %w", err)
	}
	return nil
}

func (h *RefuelShipHandler) buildRefuelResponse(ship *navigation.Ship, fuelBefore int) *types.RefuelShipResponse {
	fuelAdded := ship.Fuel().Current - fuelBefore
	creditsCost := fuelAdded * 100

	return &types.RefuelShipResponse{
		FuelAdded:    fuelAdded,
		CurrentFuel:  ship.Fuel().Current,
		CreditsCost:  creditsCost,
		Status:       "refueled",
		FuelCapacity: ship.Fuel().Capacity,
	}
}

// fetchCurrentCredits fetches the player's current credits from the API
func (h *RefuelShipHandler) fetchCurrentCredits(ctx context.Context) (int, error) {
	token, err := common.PlayerTokenFromContext(ctx)
	if err != nil {
		return 0, fmt.Errorf("player token not found in context: %w", err)
	}

	agent, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		return 0, fmt.Errorf("failed to fetch agent credits: %w", err)
	}

	return agent.Credits, nil
}

// recordRefuelTransaction records the refuel transaction in the ledger
func (h *RefuelShipHandler) recordRefuelTransaction(
	ctx context.Context,
	cmd *types.RefuelShipCommand,
	response *types.RefuelShipResponse,
	balanceBefore int,
) {
	logger := logging.LoggerFromContext(ctx)

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

	// Propagate operation context if present
	if cmd.Context != nil && cmd.Context.IsValid() {
		recordCmd.RelatedEntityType = "container"
		recordCmd.RelatedEntityID = cmd.Context.ContainerID
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
