package navigation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
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
	Success           bool
	NavigatedToGate   bool
	JumpGateSymbol    string
	DestinationSystem string
	CooldownSeconds   int
	Message           string
}

// ContainerRepository is the minimal container-persistence port
// JumpShipHandler needs. Jump claims the ship directly
// (AssignToContainer/ForceRelease) rather than running through
// ContainerRunner - it needs to return a rich, typed response synchronously -
// so it needs a lightweight container record purely to satisfy the
// ship_assignments table's (container_id, player_id) foreign key. Mirrors
// the local ContainerRepository declared in balance_ship_position.go.
type ContainerRepository interface {
	Add(ctx context.Context, containerEntity *domainContainer.Container, commandType string) error
	Remove(ctx context.Context, containerID string, playerID int) error
}

// JumpShipHandler handles the JumpShip command with auto-navigation
type JumpShipHandler struct {
	shipRepo       domainNavigation.ShipRepository
	playerRepo     player.PlayerRepository
	apiClient      ports.APIClient
	mediator       common.Mediator
	containerRepo  ContainerRepository
	clock          shared.Clock
	playerResolver *common.PlayerResolver
}

// NewJumpShipHandler creates a new JumpShipHandler. If clock is nil, uses
// RealClock (production default).
func NewJumpShipHandler(
	shipRepo domainNavigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient ports.APIClient,
	mediator common.Mediator,
	containerRepo ContainerRepository,
	clock shared.Clock,
) *JumpShipHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &JumpShipHandler{
		shipRepo:       shipRepo,
		playerRepo:     playerRepo,
		apiClient:      apiClient,
		mediator:       mediator,
		containerRepo:  containerRepo,
		clock:          clock,
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

	// 8. Claim the ship for the duration of the jump. Jump does not run
	// through ContainerRunner - it needs to return a rich, typed response
	// synchronously - so, mirroring balance_ship_position.go, it creates a
	// lightweight container record purely to satisfy the
	// ship_assignments(container_id, player_id) foreign key, then claims the
	// ship directly. Both are released unconditionally on the way out,
	// regardless of success or failure below.
	jumpContainerID := fmt.Sprintf("ship-jump-%s", cmd.ShipSymbol)
	jumpContainer := domainContainer.NewContainer(
		jumpContainerID,
		domainContainer.ContainerTypeJump,
		playerID.Value(),
		1,
		nil,
		map[string]interface{}{
			"ship_symbol": cmd.ShipSymbol,
			"destination": cmd.DestinationSystem,
		},
		h.clock,
	)
	if err := h.containerRepo.Add(ctx, jumpContainer, "jump_ship"); err != nil {
		return nil, fmt.Errorf("failed to create jump container record: %w", err)
	}
	defer func() {
		_ = h.containerRepo.Remove(ctx, jumpContainerID, playerID.Value())
	}()

	if err := ship.AssignToContainer(jumpContainerID, h.clock); err != nil {
		return nil, fmt.Errorf("failed to claim ship for jump: %w", err)
	}
	if err := h.shipRepo.Save(ctx, ship); err != nil {
		return nil, fmt.Errorf("failed to save ship claim: %w", err)
	}
	defer func() {
		ship.ForceRelease("jump_complete", h.clock)
		_ = h.shipRepo.Save(ctx, ship)
	}()

	// 9. Execute jump via API
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
		// The server reports error 4262 when the destination system's jump
		// gate is still under construction. Surface this as a clean,
		// user-facing error instead of the raw API/JSON failure.
		if isDestinationGateUnderConstructionError(err) {
			return nil, fmt.Errorf("cannot jump to %s: destination jump gate is still under construction", cmd.DestinationSystem)
		}
		return nil, fmt.Errorf("failed to execute jump: %w", err)
	}

	logger.Log("INFO", "Jump successful", map[string]interface{}{
		"destination_system":   jumpResult.DestinationSystem,
		"destination_waypoint": jumpResult.DestinationWaypoint,
		"cooldown":             jumpResult.CooldownSeconds,
	})

	// 10. Sync ship nav state to the destination - mirrors how navigate
	// persists the ship's location/cooldown after a successful API call.
	destinationWaypoint, err := shared.NewWaypoint(jumpResult.DestinationWaypoint, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("invalid destination waypoint: %w", err)
	}
	ship.SetLocation(destinationWaypoint)
	ship.SetCooldown(h.clock.Now().Add(time.Duration(jumpResult.CooldownSeconds) * time.Second))
	if err := h.shipRepo.Save(ctx, ship); err != nil {
		return nil, fmt.Errorf("failed to save ship state after jump: %w", err)
	}

	// 11. Return success response
	return &JumpShipResponse{
		Success:           true,
		NavigatedToGate:   navigatedToGate,
		JumpGateSymbol:    jumpGateSymbol,
		DestinationSystem: jumpResult.DestinationSystem,
		CooldownSeconds:   jumpResult.CooldownSeconds,
		Message:           fmt.Sprintf("Ship %s jumped from %s to %s", cmd.ShipSymbol, currentSystem, jumpResult.DestinationSystem),
	}, nil
}

// isDestinationGateUnderConstructionError reports whether the API rejected a
// jump because the destination system's jump gate is still under
// construction (error 4262). Mirrors isAlreadyAtDestinationError's
// string-matching approach in navigate_direct.go.
func isDestinationGateUnderConstructionError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "4262") || strings.Contains(msg, "under construction")
}
