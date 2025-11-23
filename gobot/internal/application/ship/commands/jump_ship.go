package commands

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// JumpShipCommand represents a command to jump a ship to a different system
type JumpShipCommand struct {
	ShipSymbol        string // Required: ship symbol to jump
	DestinationSystem string // Required: destination system symbol
	PlayerID          *int   // Optional: player ID
	AgentSymbol       string // Optional: agent symbol
}

// JumpShipResponse represents the result of a jump operation
type JumpShipResponse struct {
	Success            bool
	NavigatedToGate    bool
	JumpGateSymbol     string
	DestinationSystem  string
	CooldownSeconds    int
	Message            string
}

// JumpShipHandler handles the JumpShip command with auto-navigation
type JumpShipHandler struct {
	shipRepo       domainNavigation.ShipRepository
	playerRepo     player.PlayerRepository
	apiClient      ports.APIClient
	mediator       common.Mediator
	playerResolver *common.PlayerResolver
}

// NewJumpShipHandler creates a new JumpShipHandler
func NewJumpShipHandler(
	shipRepo domainNavigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient ports.APIClient,
	mediator common.Mediator,
) *JumpShipHandler {
	return &JumpShipHandler{
		shipRepo:       shipRepo,
		playerRepo:     playerRepo,
		apiClient:      apiClient,
		mediator:       mediator,
		playerResolver: common.NewPlayerResolver(playerRepo),
	}
}

// Handle executes the JumpShip command
func (h *JumpShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*JumpShipCommand)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *JumpShipCommand")
	}

	if cmd.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	if cmd.DestinationSystem == "" {
		return nil, fmt.Errorf("destination_system is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, cmd.PlayerID, cmd.AgentSymbol)
	if err != nil {
		return nil, err
	}

	logger := common.LoggerFromContext(ctx)
	logger.Log("INFO", "Starting jump operation", map[string]interface{}{
		"ship":        cmd.ShipSymbol,
		"destination": cmd.DestinationSystem,
	})

	// 1. Fetch ship from repository
	ship, err := h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get ship: %w", err)
	}

	// 2. Validate ship has jump drive module
	if !ship.HasJumpDrive() {
		return nil, fmt.Errorf("ship %s does not have a jump drive module", cmd.ShipSymbol)
	}

	logger.Log("INFO", "Ship has jump drive", map[string]interface{}{
		"range": ship.GetJumpDriveRange(),
	})

	// 3. Check if ship is at a jump gate
	currentLocation := ship.CurrentLocation()
	currentSystem := currentLocation.SystemSymbol
	navigatedToGate := false
	jumpGateSymbol := currentLocation.Symbol

	if !currentLocation.IsJumpGate() {
		logger.Log("INFO", "Ship not at jump gate, finding nearest", map[string]interface{}{
			"current": currentLocation.Symbol,
		})

		// 4. Find nearest jump gate
		playerIDInt := playerID.Value()
		findQuery := &queries.FindNearestJumpGateQuery{
			ShipSymbol: cmd.ShipSymbol,
			PlayerID:   &playerIDInt,
		}

		findResult, err := h.mediator.Send(ctx, findQuery)
		if err != nil {
			return nil, fmt.Errorf("failed to find jump gate: %w", err)
		}

		findResp, ok := findResult.(*queries.FindNearestJumpGateResponse)
		if !ok {
			return nil, fmt.Errorf("unexpected response type from FindNearestJumpGate")
		}

		jumpGateSymbol = findResp.JumpGate.Symbol
		logger.Log("INFO", "Found nearest jump gate", map[string]interface{}{
			"gate":     jumpGateSymbol,
			"distance": findResp.Distance,
		})

		// 5. Navigate to jump gate using existing NavigateRouteCommand
		navCmd := &NavigateRouteCommand{
			ShipSymbol:  cmd.ShipSymbol,
			Destination: jumpGateSymbol,
			PlayerID:    playerID,
		}

		_, err = h.mediator.Send(ctx, navCmd)
		if err != nil {
			return nil, fmt.Errorf("failed to navigate to jump gate: %w", err)
		}

		navigatedToGate = true
		logger.Log("INFO", "Navigated to jump gate", map[string]interface{}{
			"gate": jumpGateSymbol,
		})

		// 6. Reload ship after navigation
		ship, err = h.shipRepo.FindBySymbol(ctx, cmd.ShipSymbol, playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to reload ship after navigation: %w", err)
		}
	} else {
		logger.Log("INFO", "Ship already at jump gate", map[string]interface{}{
			"gate": currentLocation.Symbol,
		})
	}

	// 7. Verify ship is now at jump gate
	if !ship.CurrentLocation().IsJumpGate() {
		return nil, fmt.Errorf("ship is not at a jump gate after navigation")
	}

	// 8. Execute jump via API
	// Get player to obtain token
	playerEntity, err := h.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get player: %w", err)
	}

	logger.Log("INFO", "Executing jump", map[string]interface{}{
		"from": currentSystem,
		"to":   cmd.DestinationSystem,
	})

	jumpResult, err := h.apiClient.JumpShip(ctx, cmd.ShipSymbol, cmd.DestinationSystem, playerEntity.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to execute jump: %w", err)
	}

	logger.Log("INFO", "Jump successful", map[string]interface{}{
		"destination_system":   jumpResult.DestinationSystem,
		"destination_waypoint": jumpResult.DestinationWaypoint,
		"cooldown":             jumpResult.CooldownSeconds,
	})

	// 9. Return success response
	return &JumpShipResponse{
		Success:           true,
		NavigatedToGate:   navigatedToGate,
		JumpGateSymbol:    jumpGateSymbol,
		DestinationSystem: jumpResult.DestinationSystem,
		CooldownSeconds:   jumpResult.CooldownSeconds,
		Message:           fmt.Sprintf("Ship %s jumped from %s to %s", cmd.ShipSymbol, currentSystem, jumpResult.DestinationSystem),
	}, nil
}
