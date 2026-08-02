package navigation

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
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

	// SkipClaim indicates the caller already holds the ship claimed under
	// its own container (e.g. a trade-route coordinator mid-circuit). When
	// true, Handle does not create/remove the lightweight
	// "ship-jump-<symbol>-<nanos>" container record and does not
	// AssignToContainer/ForceRelease the ship - it trusts the caller's
	// existing claim instead of taking a second, conflicting one. Defaults
	// to false, preserving today's self-claiming behavior for every
	// existing caller.
	SkipClaim bool
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
	// ListJumpContainersForShip returns the IDs of every JUMP container row that names
	// this hull, in any status. It is what lets a jump clear the claim records its own
	// earlier attempts leaked.
	ListJumpContainersForShip(ctx context.Context, shipSymbol string, playerID int) ([]string, error)
}

// JumpShipHandler handles the JumpShip command with auto-navigation
type JumpShipHandler struct {
	shipRepo         domainNavigation.ShipRepository
	playerRepo       player.PlayerRepository
	apiClient        ports.APIClient
	mediator         common.Mediator
	containerRepo    ContainerRepository
	constructionRepo manufacturing.ConstructionSiteRepository
	clock            shared.Clock
	playerResolver   *common.PlayerResolver
	topologyStore    JumpTopologyStore
}

// SetJumpTopologyStore attaches the persisted gate graph so a jump can answer its
// topology questions without spending API requests. Optional: a nil store leaves the
// live reads exactly as they were.
func (h *JumpShipHandler) SetJumpTopologyStore(store JumpTopologyStore) {
	h.topologyStore = store
}

// NewJumpShipHandler creates a new JumpShipHandler. If clock is nil, uses
// RealClock (production default). constructionRepo may be nil; if so, the
// source-gate construction-completeness check is skipped and the fail-open
// path (defer to the live jump API) is always taken for driveless jumps.
func NewJumpShipHandler(
	shipRepo domainNavigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient ports.APIClient,
	mediator common.Mediator,
	containerRepo ContainerRepository,
	constructionRepo manufacturing.ConstructionSiteRepository,
	clock shared.Clock,
) *JumpShipHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &JumpShipHandler{
		shipRepo:         shipRepo,
		playerRepo:       playerRepo,
		apiClient:        apiClient,
		mediator:         mediator,
		containerRepo:    containerRepo,
		constructionRepo: constructionRepo,
		clock:            clock,
		playerResolver:   common.NewPlayerResolver(playerRepo),
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

	currentLocation := ship.CurrentLocation()

	if err := h.validateJumpSource(ctx, ship, cmd.ShipSymbol, playerID); err != nil {
		return nil, err
	}

	currentSystem := currentLocation.SystemSymbol

	ship, jumpGateSymbol, navigatedToGate, err := h.ensureAtJumpGate(ctx, ship, cmd.ShipSymbol, playerID)
	if err != nil {
		return nil, err
	}

	// SkipClaim trusts a caller that already holds the hull: a second claim would
	// error, and releasing on the way out would drop the caller's own.
	if !cmd.SkipClaim {
		release, err := h.claimShipForJump(ctx, cmd, playerID, logger)
		if err != nil {
			return nil, err
		}
		defer release()
	}

	// 9. Execute jump via API
	// Get player to obtain token
	playerEntity, err := h.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get player: %w", err)
	}

	// The live jump API requires the destination JUMP GATE WAYPOINT, not the
	// bare destination system symbol - posting the system
	// symbol 422s with "waypointSymbol Required, received undefined".
	// Resolve it via the origin gate's connections list, which carries the
	// full waypoint symbol of every system it's linked to. The resolve RE-READS
	// the gate a bounded few times if the destination is missing: the
	// live jump-gate endpoint intermittently returns an incomplete/empty 200, and
	// treating that transient read as a permanent "no connection" is what bounced
	// hulls forever between systems.
	originGateSymbol := ship.CurrentLocation().Symbol
	destinationGateWaypointSymbol, err := h.resolveDestinationGateWaypoint(ctx, originGateSymbol, cmd.DestinationSystem, playerEntity.Token)
	if err != nil {
		return nil, err
	}

	logger.Log("INFO", "Executing jump", map[string]interface{}{
		"from":                      currentSystem,
		"to":                        cmd.DestinationSystem,
		"destination_gate_waypoint": destinationGateWaypointSymbol,
	})

	// A jump requires the hull IN ORBIT, but a cross-system leg refuels at the
	// gate on arrival (route_executor.handlePostArrivalRefueling docks to refuel
	// and does not re-orbit), so the hull can reach here still DOCKED — and the
	// live jump API then hard-rejects it with 400 code 4236 "not currently in
	// orbit", killing the tour (a class distinct from the cooldown-409). Every
	// navigate path already orbits before departing (navigate_direct's
	// EnsureInOrbit, RouteExecutor.ensureShipInOrbit); the jump
	// path was the one mover that did not. Orbit proactively when we already read
	// the hull as DOCKED (no wasted jump attempt), and reactively in
	// jumpWithOrbitRetry if the API still reports not-in-orbit under a raced
	// nav_status. Orbit is idempotent and free, so a hull already in orbit (the
	// common case) skips the call entirely.
	if ship.NavStatus() == domainNavigation.NavStatusDocked {
		if err := h.shipRepo.Orbit(ctx, ship, playerID); err != nil {
			return nil, fmt.Errorf("failed to orbit %s at gate %s before jump: %w", cmd.ShipSymbol, ship.CurrentLocation().Symbol, err)
		}
	}

	jumpResult, err := h.jumpWithOrbitRetry(ctx, ship, cmd, destinationGateWaypointSymbol, playerEntity.Token, playerID)
	if err != nil {
		return nil, h.explainJumpFailure(ctx, cmd, playerID, err)
	}

	logger.Log("INFO", "Jump successful", map[string]interface{}{
		"destination_system":   jumpResult.DestinationSystem,
		"destination_waypoint": jumpResult.DestinationWaypoint,
		"cooldown":             jumpResult.CooldownSeconds,
		"gate_fee":             jumpResult.TotalPrice,
	})

	// Record the gate fee BEFORE persisting nav state. The credits
	// have already left the account server-side; a failure in any later step
	// must not be the reason the spend goes unrecorded. Recording is
	// best-effort and never fails the jump — the hull HAS moved, so returning
	// an error here would strand the caller's model of where it is.
	h.recordJumpFee(ctx, cmd, playerID, playerEntity.AgentSymbol, currentSystem, jumpResult)

	if err := h.persistPostJumpNav(ctx, ship, cmd.ShipSymbol, playerID, jumpResult); err != nil {
		return nil, err
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

// explainJumpFailure names the refusal: a destination gate under construction (4262),
// or a non-adjacent gate (4255) — proof our position is wrong, so it re-anchors.
func (h *JumpShipHandler) explainJumpFailure(ctx context.Context, cmd *JumpShipCommand, playerID shared.PlayerID, err error) error {
	if isDestinationGateUnderConstructionError(err) {
		return fmt.Errorf("cannot jump to %s: destination jump gate is still under construction", cmd.DestinationSystem)
	}
	if connections, ok := notConnectedTruth(err); ok {
		return h.reAnchorAfterNotConnected(ctx, cmd, playerID, connections, err)
	}
	return fmt.Errorf("failed to execute jump: %w", err)
}

// persistPostJumpNav CAS-writes ONLY location and cooldown on the FRESH row, so a
// concurrent cargo/fuel write survives this handler's pre-jump snapshot.
func (h *JumpShipHandler) persistPostJumpNav(ctx context.Context, ship *domainNavigation.Ship, shipSymbol string, playerID shared.PlayerID, jumpResult *ports.JumpResult) error {
	destinationWaypoint, err := shared.NewWaypoint(jumpResult.DestinationWaypoint, 0, 0)
	if err != nil {
		return fmt.Errorf("invalid destination waypoint: %w", err)
	}
	cooldownUntil := h.clock.Now().Add(time.Duration(jumpResult.CooldownSeconds) * time.Second)
	ship.SetLocation(destinationWaypoint)
	ship.SetCooldown(cooldownUntil)

	if _, _, err := h.shipRepo.SaveWithRetry(ctx, shipSymbol, playerID,
		func(sh *domainNavigation.Ship) (bool, error) {
			sh.SetLocation(destinationWaypoint)
			sh.SetCooldown(cooldownUntil)
			return true, nil
		}); err != nil {
		return fmt.Errorf("failed to save ship state after jump: %w", err)
	}
	return nil
}

// validateJumpSource: a jump-drive hull may jump from anywhere, a driveless one only
// from a COMPLETE jump gate.
func (h *JumpShipHandler) validateJumpSource(ctx context.Context, ship *domainNavigation.Ship, shipSymbol string, playerID shared.PlayerID) error {
	logger := common.LoggerFromContext(ctx)
	currentLocation := ship.CurrentLocation()

	if ship.HasJumpDrive() {
		logger.Log("INFO", "Ship has jump drive", map[string]interface{}{
			"range": ship.GetJumpDriveRange(),
		})
		return nil
	}

	if !currentLocation.IsJumpGate() {
		return fmt.Errorf("ship %s cannot jump: no jump drive module and not at a jump gate", shipSymbol)
	}

	complete, err := h.sourceGateComplete(ctx, currentLocation.Symbol, playerID.Value())
	if err != nil {
		// Fail open: the live jump API rejects an under-construction gate itself, so a
		// repository hiccup must not block an otherwise-legal jump.
		logger.Log("WARN", "could not verify source jump gate construction status, proceeding", map[string]interface{}{
			"gate":  currentLocation.Symbol,
			"error": err.Error(),
		})
	} else if !complete {
		return fmt.Errorf("ship %s cannot jump: jump gate %s is still under construction", shipSymbol, currentLocation.Symbol)
	}

	logger.Log("INFO", "Ship is driveless but at a complete jump gate", map[string]interface{}{
		"gate": currentLocation.Symbol,
	})
	return nil
}

// ensureAtJumpGate navigates the ship to its nearest jump gate when it is not on
// one already, and returns the reloaded ship standing at that gate.
func (h *JumpShipHandler) ensureAtJumpGate(ctx context.Context, ship *domainNavigation.Ship, shipSymbol string, playerID shared.PlayerID) (*domainNavigation.Ship, string, bool, error) {
	logger := common.LoggerFromContext(ctx)
	currentLocation := ship.CurrentLocation()
	jumpGateSymbol := currentLocation.Symbol
	navigatedToGate := false

	if currentLocation.IsJumpGate() {
		logger.Log("INFO", "Ship already at jump gate", map[string]interface{}{
			"gate": currentLocation.Symbol,
		})
	} else {
		logger.Log("INFO", "Ship not at jump gate, finding nearest", map[string]interface{}{
			"current": currentLocation.Symbol,
		})

		playerIDInt := playerID.Value()
		findQuery := &queries.FindNearestJumpGateQuery{
			ShipSymbol: shipSymbol,
			PlayerID:   &playerIDInt,
		}

		findResult, err := h.mediator.Send(ctx, findQuery)
		if err != nil {
			return nil, "", false, fmt.Errorf("failed to find jump gate: %w", err)
		}

		findResp, ok := findResult.(*queries.FindNearestJumpGateResponse)
		if !ok {
			return nil, "", false, fmt.Errorf("unexpected response type from FindNearestJumpGate")
		}

		jumpGateSymbol = findResp.JumpGate.Symbol
		logger.Log("INFO", "Found nearest jump gate", map[string]interface{}{
			"gate":     jumpGateSymbol,
			"distance": findResp.Distance,
		})

		navCmd := &NavigateRouteCommand{
			ShipSymbol:  shipSymbol,
			Destination: jumpGateSymbol,
			PlayerID:    playerID,
		}

		if _, err := h.mediator.Send(ctx, navCmd); err != nil {
			return nil, "", false, fmt.Errorf("failed to navigate to jump gate: %w", err)
		}

		navigatedToGate = true
		logger.Log("INFO", "Navigated to jump gate", map[string]interface{}{
			"gate": jumpGateSymbol,
		})

		ship, err = h.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
		if err != nil {
			return nil, "", false, fmt.Errorf("failed to reload ship after navigation: %w", err)
		}
	}

	if !ship.CurrentLocation().IsJumpGate() {
		return nil, "", false, fmt.Errorf("ship is not at a jump gate after navigation")
	}
	return ship, jumpGateSymbol, navigatedToGate, nil
}
