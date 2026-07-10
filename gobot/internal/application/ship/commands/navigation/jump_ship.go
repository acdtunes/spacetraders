package navigation

import (
	"context"
	"fmt"
	"strings"
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
	// its own container (e.g. a trade-route coordinator mid-circuit,
	// sp-wlev). When true, Handle does not create/remove the lightweight
	// "ship-jump-<symbol>" container record and does not
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

	// 2. Validate the ship can jump. SpaceTraders rule: a ship with a
	// jump-drive module can jump from anywhere; a ship WITHOUT a drive can
	// only jump if it is currently at a COMPLETE jump gate - gate-adjacent
	// driveless jumps are legal (sp-n0x7), but a gate still under
	// construction is not a valid source.
	if ship.HasJumpDrive() {
		logger.Log("INFO", "Ship has jump drive", map[string]interface{}{
			"range": ship.GetJumpDriveRange(),
		})
	} else {
		if !currentLocation.IsJumpGate() {
			return nil, fmt.Errorf("ship %s cannot jump: no jump drive module and not at a jump gate", cmd.ShipSymbol)
		}

		complete, err := h.sourceGateComplete(ctx, currentLocation.Symbol, playerID.Value())
		if err != nil {
			// Fail open: if we can't verify construction status, don't block
			// an otherwise-legal jump on a repository/API hiccup - the live
			// jump API is the final, authoritative arbiter and will reject
			// it if it's actually still under construction (mirrors the
			// existing 4262 destination-gate handling below).
			logger.Log("WARN", "could not verify source jump gate construction status, proceeding", map[string]interface{}{
				"gate":  currentLocation.Symbol,
				"error": err.Error(),
			})
		} else if !complete {
			return nil, fmt.Errorf("ship %s cannot jump: jump gate %s is still under construction", cmd.ShipSymbol, currentLocation.Symbol)
		}

		logger.Log("INFO", "Ship is driveless but at a complete jump gate", map[string]interface{}{
			"gate": currentLocation.Symbol,
		})
	}

	// 3. Check if ship is at a jump gate
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
	//
	// SkipClaim (sp-wlev) opts out of all of this: a caller that already
	// holds the ship claimed under its own container (e.g. a trade-route
	// coordinator mid-circuit) sets it so jump_ship trusts that existing
	// claim instead of taking a second, conflicting one - AssignToContainer
	// would otherwise error "already assigned to container X", and
	// ForceRelease on the way out would wrongly drop the caller's claim.
	if !cmd.SkipClaim {
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
	}

	// 9. Execute jump via API
	// Get player to obtain token
	playerEntity, err := h.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get player: %w", err)
	}

	// The live jump API requires the destination JUMP GATE WAYPOINT, not the
	// bare destination system symbol (sp-n0x7 round 2) - posting the system
	// symbol 422s with "waypointSymbol Required, received undefined".
	// Resolve it via the origin gate's connections list, which carries the
	// full waypoint symbol of every system it's linked to.
	originGateSymbol := ship.CurrentLocation().Symbol
	gateData, err := h.apiClient.GetJumpGate(ctx, shared.ExtractSystemSymbol(originGateSymbol), originGateSymbol, playerEntity.Token)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve jump gate connections for %s: %w", originGateSymbol, err)
	}
	destinationGateWaypointSymbol, err := destinationGateWaypoint(gateData.Connections, cmd.DestinationSystem)
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
	// orbit", killing the tour (sp-28n2, a class distinct from the wc5h
	// cooldown-409). Every navigate path already orbits before departing
	// (navigate_direct's EnsureInOrbit, RouteExecutor.ensureShipInOrbit); the jump
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

// maxJumpOrbitRetries bounds how many times jumpWithOrbitRetry re-orbits and
// retries a jump the live API rejected as not-in-orbit (4236). One retry clears
// the realistic case (a single stale nav_status), and the bound guarantees a
// jump that keeps 4236-ing for any OTHER reason surfaces the error instead of
// looping forever.
const maxJumpOrbitRetries = 2

// jumpWithOrbitRetry executes the live jump, riding out a not-in-orbit rejection
// (400 code 4236) instead of hard-failing on it (sp-28n2). Handle's proactive
// guard already orbits a hull it READ as docked; this covers the residual race
// where the persisted nav_status lagged a server-side dock, so the hull is
// docked on the server while the daemon believed it orbited. It mirrors how the
// trade-route coordinator rides a cooldown-409 (wc5h jumpHop): classify the one
// recoverable error, take the corrective action (orbit live), retry — bounded,
// with every other error propagated on the first attempt so a genuine jump
// failure (4262, a missing gate connection, an auth error) is never masked as a
// stale orbit.
func (h *JumpShipHandler) jumpWithOrbitRetry(
	ctx context.Context,
	ship *domainNavigation.Ship,
	cmd *JumpShipCommand,
	destinationGateWaypointSymbol, token string,
	playerID shared.PlayerID,
) (*ports.JumpResult, error) {
	logger := common.LoggerFromContext(ctx)
	for attempt := 0; ; attempt++ {
		jumpResult, err := h.apiClient.JumpShip(ctx, cmd.ShipSymbol, destinationGateWaypointSymbol, token)
		if err == nil {
			return jumpResult, nil
		}
		if !isNotInOrbitError(err) || attempt >= maxJumpOrbitRetries {
			return nil, err
		}
		logger.Log("WARNING", "Jump rejected as not-in-orbit (4236) — orbiting live and retrying (raced nav_status; resume-safe, sp-28n2)", map[string]interface{}{
			"ship_symbol":        cmd.ShipSymbol,
			"destination_system": cmd.DestinationSystem,
			"attempt":            attempt + 1,
		})
		if oerr := h.shipRepo.Orbit(ctx, ship, playerID); oerr != nil {
			return nil, fmt.Errorf("failed to orbit %s after a not-in-orbit jump rejection: %w", cmd.ShipSymbol, oerr)
		}
	}
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

// isNotInOrbitError reports whether the API rejected an action because the ship
// is not in orbit (error 4236). Mirrors isDestinationGateUnderConstructionError's
// string-matching approach — the wire form is
// `API error (status 400): {"error":{"code":4236,"message":"Ship ... is not currently in orbit ..."}}`.
func isNotInOrbitError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "4236") || strings.Contains(msg, "not currently in orbit")
}

// destinationGateWaypoint finds the connection in a jump gate's connections
// list whose system matches destinationSystem, returning its full waypoint
// symbol (e.g. "X1-GQ92-I51"). The live SpaceTraders jump API requires this
// WAYPOINT - not the bare system symbol - as waypointSymbol in the request
// body (sp-n0x7 round 2).
func destinationGateWaypoint(connections []string, destinationSystem string) (string, error) {
	for _, conn := range connections {
		if shared.ExtractSystemSymbol(conn) == destinationSystem {
			return conn, nil
		}
	}
	return "", fmt.Errorf("no jump gate connection from origin gate to system %s", destinationSystem)
}

// sourceGateComplete reports whether the jump gate at waypointSymbol has
// finished construction, i.e. is a valid SOURCE gate for a driveless jump.
// Returns an error if construction status could not be determined (no
// repository configured, or the lookup itself failed) - callers should fail
// open on error rather than block an otherwise-legal jump.
func (h *JumpShipHandler) sourceGateComplete(ctx context.Context, waypointSymbol string, playerID int) (bool, error) {
	if h.constructionRepo == nil {
		return false, fmt.Errorf("construction repository not configured")
	}
	site, err := h.constructionRepo.FindByWaypoint(ctx, waypointSymbol, playerID)
	if err != nil {
		return false, err
	}
	return site.IsComplete(), nil
}
