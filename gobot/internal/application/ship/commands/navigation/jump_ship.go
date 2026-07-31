package navigation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
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
	// earlier attempts leaked (sp-rqhzh).
	ListJumpContainersForShip(ctx context.Context, shipSymbol string, playerID int) ([]string, error)
}

// JumpTopologyStore answers a jump's two topology questions from the persisted gate
// graph the router already maintains: which gate waypoint a hop leaves for, and whether
// a gate has finished building. Both are era-scoped and freshness-bounded by the store,
// and both report a miss for anything uncertain, so the handler falls through to the
// live read rather than acting on a guess.
type JumpTopologyStore interface {
	StoredGateWaypoint(ctx context.Context, fromSystem, toSystem string) (string, bool, error)
	RecordedBuiltGate(ctx context.Context, gateWaypoint string) (bool, error)
	// PruneContradictedEdges reconciles systemSymbol's stored edges against the server's
	// authoritative connection set, deleting the ones it contradicts and returning how many
	// went. It is REMOVAL-ONLY by contract — an edge the set names but we do not hold is
	// never created — so it can only ever shrink the routable graph (RULINGS #4).
	PruneContradictedEdges(ctx context.Context, systemSymbol string, authoritativeConnections []string) (int, error)
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
		// UNIQUE PER ATTEMPT (sp-rqhzh). This ID used to be a bare
		// "ship-jump-<symbol>" — deterministic, with nothing to distinguish one
		// attempt from the next. A jump that left its row behind (the deferred
		// Remove below never ran, because the daemon died mid-jump, or it ran and
		// the delete failed — its error is discarded) therefore made every LATER
		// jump for that hull collide on containers_pkey, forever: the hull could
		// never jump again, and kept consuming a probe-cap slot it could not use.
		// Measured on the live fleet as 156 failures across two permanently wedged
		// hulls, whose leftover rows named a destination they had ALREADY reached.
		//
		// A per-attempt nonce makes the collision structurally impossible rather
		// than recoverable, and it costs no exclusivity: two jumps racing the same
		// hull were only ever serialized by that pkey collision ACCIDENTALLY, and
		// are still serialized deliberately by AssignToContainer below, which
		// rejects an already-claimed hull whatever container holds it.
		//
		// Each attempt owning its OWN row is also what makes the deferred Remove
		// safe: ships.container_id references containers ON DELETE SET NULL, so
		// deleting a row silently strips the claim of whatever hull points at it.
		// Under the old shared ID, a losing attempt's cleanup would have silently
		// unclaimed the WINNER's hull mid-jump. Now every Remove can only ever
		// touch the row its own attempt created.
		//
		// Mirrors ship-outfit-<symbol>-<nanos> in outfitting.go, the sibling
		// operation that already claims a hull this way.
		jumpContainerID := fmt.Sprintf("ship-jump-%s-%d", cmd.ShipSymbol, h.clock.Now().UnixNano())
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

		// Claim under CAS-retry (sp-wa7c): the closure re-applies AssignToContainer
		// on the FRESH row so a concurrent writer's cargo/nav/fuel update on the
		// same hull survives instead of being last-write-wins clobbered by this
		// handler's pre-jump snapshot. This op owns ONLY the assignment; a fresh row
		// already assigned to someone else still surfaces the AssignToContainer
		// error, so exclusivity (RULINGS #7) is unchanged.
		if _, _, err := h.shipRepo.SaveWithRetry(ctx, cmd.ShipSymbol, playerID,
			func(sh *domainNavigation.Ship) (bool, error) {
				if aerr := sh.AssignToContainer(jumpContainerID, h.clock); aerr != nil {
					return false, aerr
				}
				return true, nil
			}); err != nil {
			return nil, fmt.Errorf("failed to save ship claim: %w", err)
		}

		// The claim has landed, so this is the one moment where the leftovers of
		// this hull's earlier jumps are PROVABLY dead. See reapStrandedJumpContainers.
		h.reapStrandedJumpContainers(ctx, cmd.ShipSymbol, jumpContainerID, playerID, logger)

		defer func() {
			// Release under CAS-retry too: re-apply ForceRelease on the fresh row,
			// touching only the assignment, and skip if the hull is no longer this
			// jump's claim (already released / re-claimed) so nothing else is clobbered.
			_, _, _ = h.shipRepo.SaveWithRetry(ctx, cmd.ShipSymbol, playerID,
				func(sh *domainNavigation.Ship) (bool, error) {
					if !sh.IsAssigned() || sh.ContainerID() != jumpContainerID {
						return false, nil
					}
					sh.ForceRelease("jump_complete", h.clock)
					return true, nil
				})
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
	// full waypoint symbol of every system it's linked to. The resolve RE-READS
	// the gate a bounded few times if the destination is missing (sp-hguq3): the
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
		// The server refused because the gate we asked for is not adjacent to where the hull
		// ACTUALLY is (4255). That is first-hand proof our believed position is wrong, and the
		// refusal carries the truth — re-anchor on it instead of discarding it into a string.
		if connections, ok := notConnectedTruth(err); ok {
			return nil, h.reAnchorAfterNotConnected(ctx, cmd, playerID, connections, err)
		}
		return nil, fmt.Errorf("failed to execute jump: %w", err)
	}

	logger.Log("INFO", "Jump successful", map[string]interface{}{
		"destination_system":   jumpResult.DestinationSystem,
		"destination_waypoint": jumpResult.DestinationWaypoint,
		"cooldown":             jumpResult.CooldownSeconds,
		"gate_fee":             jumpResult.TotalPrice,
	})

	// Record the gate fee BEFORE persisting nav state (sp-shq63). The credits
	// have already left the account server-side; a failure in any later step
	// must not be the reason the spend goes unrecorded. Recording is
	// best-effort and never fails the jump — the hull HAS moved, so returning
	// an error here would strand the caller's model of where it is.
	h.recordJumpFee(ctx, cmd, playerID, playerEntity.AgentSymbol, jumpResult)

	// 10. Sync ship nav state to the destination - mirrors how navigate
	// persists the ship's location/cooldown after a successful API call.
	destinationWaypoint, err := shared.NewWaypoint(jumpResult.DestinationWaypoint, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("invalid destination waypoint: %w", err)
	}
	// Persist the post-jump nav state under CAS-retry (sp-wa7c): the closure
	// re-applies ONLY this op's own fields — destination location + jump cooldown —
	// on the FRESH row, so a concurrent writer's cargo/fuel update on the same hull
	// survives instead of being last-write-wins clobbered by this handler's
	// pre-jump snapshot (the reported nav-write-vs-cargo desync). Keep the local
	// pointer consistent for any post-return use, then persist the fresh row.
	ship.SetLocation(destinationWaypoint)
	cooldownUntil := h.clock.Now().Add(time.Duration(jumpResult.CooldownSeconds) * time.Second)
	ship.SetCooldown(cooldownUntil)
	if _, _, err := h.shipRepo.SaveWithRetry(ctx, cmd.ShipSymbol, playerID,
		func(sh *domainNavigation.Ship) (bool, error) {
			sh.SetLocation(destinationWaypoint)
			sh.SetCooldown(cooldownUntil)
			return true, nil
		}); err != nil {
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

// recordJumpFee writes the jump gate fee to the financial ledger (sp-shq63).
//
// Before this existed the fee was the ledger's one credit-DECREASING blind spot:
// the API charged it, the adapter parsed it into JumpResult.TotalPrice, and
// nothing consumed the value. That is the single failure direction a fail-closed
// money guard cannot tolerate — an unrecorded spend makes the ledger report MORE
// credits than exist, which is the direction that authorises an unaffordable buy.
// (Staleness alone is safe; it only ever under-reports.)
//
// AuthoritativeBalance is the whole point. The jump response carries the agent's
// post-fee credits in-band (data.agent.credits, mandatory in the API schema), so
// the row RE-ANCHORS the running chain to API truth. A row that only appended
// `amount` would make the chain longer without making it any more trustworthy.
// When the API omits the agent block the pointer is nil and the ledger falls back
// to reconstruction — still recorded, just not re-anchored.
//
// Best-effort by design: a ledger failure is logged, never returned. The hull has
// already moved and the credits have already gone; failing the jump here would
// trade a bookkeeping gap for a lost hull position.
func (h *JumpShipHandler) recordJumpFee(
	ctx context.Context,
	cmd *JumpShipCommand,
	playerID shared.PlayerID,
	agentSymbol string,
	jumpResult *ports.JumpResult,
) {
	logger := common.LoggerFromContext(ctx)

	// A zero fee is not a transaction: ledger.Validate rejects amount == 0
	// (same guard refuel_ship applies to a free top-up). Nothing was spent, so
	// there is nothing to record and nothing to re-anchor.
	if jumpResult.TotalPrice == 0 {
		return
	}

	if h.mediator == nil {
		logger.Log("ERROR", "Cannot record jump gate fee: no mediator wired", map[string]interface{}{
			"ship": cmd.ShipSymbol,
			"fee":  jumpResult.TotalPrice,
		})
		return
	}

	if agentSymbol == "" {
		agentSymbol = "UNKNOWN"
	}

	// Zero baseline: when AgentCredits is nil the ledger reconstructs
	// balance_after from the running chain; when set it re-anchors to API truth.
	const balanceBefore = 0
	balanceAfter := balanceBefore - jumpResult.TotalPrice

	recordCmd := &ledgerCommands.RecordTransactionCommand{
		PlayerID:             playerID.Value(),
		TransactionType:      string(ledger.TransactionTypeJump),
		Amount:               -jumpResult.TotalPrice, // Negative: a gate fee is an expense
		BalanceBefore:        balanceBefore,
		BalanceAfter:         balanceAfter,
		AuthoritativeBalance: jumpResult.AgentCredits,
		Description: fmt.Sprintf("Jump gate fee for %s to %s",
			cmd.ShipSymbol, jumpResult.DestinationSystem),
		Metadata: map[string]interface{}{
			"agent":                agentSymbol,
			"ship_symbol":          cmd.ShipSymbol,
			"destination_system":   jumpResult.DestinationSystem,
			"destination_waypoint": jumpResult.DestinationWaypoint,
		},
	}

	// Propagate operation context if present (attributes the fee to the tour /
	// contract / expansion run that spent it).
	if opCtx := shared.OperationContextFromContext(ctx); opCtx != nil && opCtx.IsValid() {
		recordCmd.RelatedEntityType = "container"
		recordCmd.RelatedEntityID = opCtx.ContainerID
		recordCmd.OperationType = opCtx.NormalizedOperationType()
	} else {
		recordCmd.OperationType = "manual"
	}

	// context.Background(): the spend is already real, so the record must not be
	// lost to a cancelled caller context (mirrors refuel/cargo recording).
	if _, err := h.mediator.Send(context.Background(), recordCmd); err != nil {
		logger.Log("ERROR", "Failed to record jump gate fee in ledger", map[string]interface{}{
			"error":     err.Error(),
			"ship":      cmd.ShipSymbol,
			"fee":       jumpResult.TotalPrice,
			"player_id": playerID.Value(),
		})
	}
}

// maxJumpOrbitRetries bounds how many times jumpWithOrbitRetry re-orbits and
// retries a jump the live API rejected as not-in-orbit (4236). One retry clears
// the realistic case (a single stale nav_status), and the bound guarantees a
// jump that keeps 4236-ing for any OTHER reason surfaces the error instead of
// looping forever.
// reapStrandedJumpContainers deletes the leftover JUMP container rows this hull
// accumulated from earlier jumps, so a crash-leaked claim record cannot outlive the
// fleet (sp-rqhzh). activeContainerID is this attempt's own row, which is never touched.
//
// WHY THIS CANNOT CLEAR A JUMP THAT IS ACTUALLY IN FLIGHT. It runs only AFTER this
// handler's own AssignToContainer has committed, so this hull's single assignment row
// points at activeContainerID right now. A jump holds its hull claimed for its entire
// duration, so no other jump for this hull can be in flight while we hold that claim —
// any concurrent attempt is already guaranteed to lose at AssignToContainer, and its own
// deferred Remove is a harmless no-op against a row that is already gone.
//
// That held claim is POSITIVE evidence — a fact about the present, read from the same
// assignment row the rest of the fleet reads ownership from — and not an age heuristic.
// An age rule would have been the wrong instrument here: a jump row is written BEFORE its
// claim, so "old and unclaimed" cannot tell a stranded row from one whose jump is a
// millisecond away from claiming, and picking the wrong side of that rips a hull mid-spawn.
// Holding the claim removes the ambiguity entirely instead of trading it off.
//
// BEST-EFFORT BY CONSTRUCTION. With per-attempt IDs a leftover row is inert — it can no
// longer wedge anything — so this is hygiene, not the fix, and a failure here must never
// fail the jump. Both outcomes are counted, because a reap that never runs and a reap
// that runs and fails must not emit the same signal.
func (h *JumpShipHandler) reapStrandedJumpContainers(
	ctx context.Context,
	shipSymbol string,
	activeContainerID string,
	playerID shared.PlayerID,
	logger common.ContainerLogger,
) {
	if h.containerRepo == nil {
		return
	}

	ids, err := h.containerRepo.ListJumpContainersForShip(ctx, shipSymbol, playerID.Value())
	if err != nil {
		logger.Log("WARN", "could not look for stranded jump claim records, continuing with the jump", map[string]interface{}{
			"ship":  shipSymbol,
			"error": err.Error(),
		})
		return
	}

	for _, id := range ids {
		if id == activeContainerID {
			continue // this jump's own claim record
		}
		if err := h.containerRepo.Remove(ctx, id, playerID.Value()); err != nil {
			metrics.RecordStrandedJumpContainer(playerID.Value(), "clear_failed")
			logger.Log("WARN", "found a stranded jump claim record but could not clear it", map[string]interface{}{
				"ship":         shipSymbol,
				"container_id": id,
				"error":        err.Error(),
			})
			continue
		}
		metrics.RecordStrandedJumpContainer(playerID.Value(), "cleared")
		logger.Log("INFO", "cleared a stranded jump claim record left by an earlier jump", map[string]interface{}{
			"ship":         shipSymbol,
			"container_id": id,
		})
	}
}

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

// notConnectedCode is the SpaceTraders verdict that the waypoint we asked to jump to is not
// adjacent to the hull's ACTUAL current location. Its payload carries data.connections — the
// complete gate set of the system the hull is really standing on — which is the only first-hand
// evidence we ever get that our believed position is wrong.
const notConnectedCode = 4255

// notConnectedTruth reports whether err is a not-connected refusal (4255) and, if so, returns
// whatever authoritative connection set the server attached to it.
//
// The two results are deliberately independent. The VERDICT alone is position evidence — it
// says the gate we asked for is not adjacent to where the hull IS — and that is what drives the
// re-anchor, with or without a connection list. The list is the separate, optional evidence
// that drives the adjacency reconcile; an absent one simply means there is nothing to reconcile
// against (the jump-gate endpoint is known to return empty reads, sp-hguq3/sp-dmxy5).
//
// It reads the TYPED *ports.APIError body rather than string-matching the wrapped message, so
// it cannot be fooled by an unrelated error that happens to mention a code, and it survives the
// adapter's %w wrapping. The code — not the payload SHAPE — is the verdict: another refusal
// carrying a connections blob is not a position correction.
func notConnectedTruth(err error) ([]string, bool) {
	var apiErr *ports.APIError
	if !errors.As(err, &apiErr) {
		return nil, false
	}
	var payload struct {
		Error struct {
			Code int `json:"code"`
			Data struct {
				Connections []string `json:"connections"`
			} `json:"data"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(apiErr.Body), &payload) != nil {
		return nil, false
	}
	if payload.Error.Code != notConnectedCode {
		return nil, false
	}
	return payload.Error.Data.Connections, true
}

// reAnchorAfterNotConnected is the root-cause fix for the live TORWIND-41 incident. The
// planner had routed a hull standing on X1-KC84's gate as though it were at X1-GF41: the
// persisted row said GF41, so candidate discovery, the BFS and finally
// StoredGateWaypoint("X1-GF41","X1-KP23") all resolved faithfully off that one stale fact and
// posted X1-KP23-I53, a gate KC84 does not reach. Nothing reconciled that belief against the
// server, so the next tick re-derived the SAME impossible jump — four crashes in 27 minutes.
//
// A 4255 refusal settles it: the server is telling us where the hull is NOT. So re-read the
// hull and write it through to durable state, which is what the next tick re-derives from
// (RULINGS #2 — the correction lives in the durable row, no routing state is carried across
// ticks). Then, knowing at last which system the hull actually occupies, reconcile THAT
// system's stored edges against the connection set the refusal handed us.
//
// Attribution is the whole subtlety, and getting it wrong is worse than doing nothing: the
// connection set describes the gate the hull is STANDING ON, not the system we mistakenly
// planned from. In the incident GF41's stored edges were perfectly correct; charging them with
// this evidence would have corrupted healthy topology on every crash. So if the re-anchor
// cannot tell us where the hull really is, the evidence is unattributable and the adjacency is
// left completely alone — fail closed.
//
// It ALWAYS returns an error (the jump did fail), wrapping the original cause so the existing
// failure path, retries and operator triage are unchanged.
func (h *JumpShipHandler) reAnchorAfterNotConnected(
	ctx context.Context,
	cmd *JumpShipCommand,
	playerID shared.PlayerID,
	connections []string,
	cause error,
) error {
	logger := common.LoggerFromContext(ctx)

	fresh, err := h.shipRepo.SyncShipFromAPI(ctx, cmd.ShipSymbol, playerID)
	if err != nil || fresh == nil || fresh.CurrentLocation() == nil {
		logger.Log("WARNING", "Jump refused as not-connected but the hull could not be re-anchored — the server's connection set is unattributable, so the stored adjacency is left untouched (fail closed)", map[string]interface{}{
			"action":             "jump_not_connected_reanchor_failed",
			"ship_symbol":        cmd.ShipSymbol,
			"destination_system": cmd.DestinationSystem,
			"connections":        connections,
		})
		return fmt.Errorf("failed to execute jump: %w", cause)
	}

	trueSystem := fresh.CurrentLocation().SystemSymbol
	logger.Log("WARNING", fmt.Sprintf("Jump refused as not-connected — the hull is really in %s, not where routing believed; re-anchored on the server so the next tick re-plans from its true position", trueSystem), map[string]interface{}{
		"action":             "jump_not_connected_reanchored",
		"ship_symbol":        cmd.ShipSymbol,
		"destination_system": cmd.DestinationSystem,
		"true_system":        trueSystem,
		"true_waypoint":      fresh.CurrentLocation().Symbol,
		"connections":        connections,
	})

	h.reconcileAdjacencyAgainstTruth(ctx, trueSystem, connections, logger)

	return fmt.Errorf("jump of %s to %s refused: the hull is actually in %s (re-anchored on the server; next tick re-plans from there): %w",
		cmd.ShipSymbol, cmd.DestinationSystem, trueSystem, cause)
}

// reconcileAdjacencyAgainstTruth deletes any stored edge for trueSystem that the server's
// connection set contradicts, so the phantom hop can never be replanned. REMOVAL-ONLY: an
// unheld connection is never created (it carries no verified build state), so this can only
// ever shrink the routable graph and never authorise a jump that would otherwise be refused
// (RULINGS #4). Best-effort — the jump has already failed and a reconcile problem must not
// change that outcome, so a store failure or an unwired store is logged and swallowed.
func (h *JumpShipHandler) reconcileAdjacencyAgainstTruth(ctx context.Context, trueSystem string, connections []string, logger common.ContainerLogger) {
	// No list means no evidence: the re-anchor above still stands, but there is nothing to
	// reconcile against and an absent set must never be read as "connects nowhere".
	if h.topologyStore == nil || trueSystem == "" || len(connections) == 0 {
		return
	}
	removed, err := h.topologyStore.PruneContradictedEdges(ctx, trueSystem, connections)
	if err != nil {
		logger.Log("WARNING", "Could not reconcile the stored adjacency against the server's connection set (non-fatal)", map[string]interface{}{
			"action": "jump_adjacency_reconcile_failed",
			"system": trueSystem,
			"error":  err.Error(),
		})
		return
	}
	if removed > 0 {
		logger.Log("WARNING", fmt.Sprintf("Reconciled %s against the server's gate connections — %d contradicted edge(s) removed so the refused hop cannot be replanned", trueSystem, removed), map[string]interface{}{
			"action":        "jump_adjacency_reconciled",
			"system":        trueSystem,
			"removed_edges": removed,
			"authoritative": connections,
		})
	}
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

// maxJumpGateReadAttempts bounds how many times a cross-system jump re-reads the ORIGIN
// gate's live connections when the intended destination is missing from the response
// (sp-hguq3). The SpaceTraders jump-gate endpoint intermittently returns a 200 OK with an
// incomplete/empty connections list — a transient, eventually-consistent read, DISTINCT
// from a 429 (which the API client already retries on status code, so it never reaches
// here as an empty list). Treating that one bad read as a permanent "no connection" is
// what bounced hulls forever between two systems: the gate_edges cache is a faithful
// snapshot that MATCHES the live API, so the charted connection is real and reappears on
// the very next read. A few bounded re-reads recover the hop; a destination absent from
// ALL attempts fails cleanly (no infinite bounce, no cache poisoning).
const maxJumpGateReadAttempts = 3

// jumpGateReadRetryBackoff is the short settle between bounded re-reads of a gate whose
// live connections came back missing the destination — enough for an eventually-consistent
// backend to converge without stalling the jump. Applied ONLY between re-reads (never after
// the final attempt, never on the happy path), and via the handler clock so tests advance
// it instantly. Combined with the API client's own rate limiter, the re-read never spams:
// the extra reads happen only on the (rare) missing-destination path.
const jumpGateReadRetryBackoff = 750 * time.Millisecond

// resolveDestinationGateWaypoint resolves the destination system's gate WAYPOINT from the
// origin gate's LIVE connections (sp-n0x7 round 2 requires the waypoint, not the bare
// system, in the jump request body), re-reading a bounded number of times when the
// destination is missing (sp-hguq3). A single live read is NOT trusted for a NEGATIVE
// verdict: the jump-gate endpoint occasionally returns an incomplete/empty 200, and a
// charted connection reappears on the next read. The happy path (destination present on
// the first read) returns immediately with exactly one read — zero overhead, no spam. A
// destination genuinely absent from every bounded read yields a clean, terminal error (no
// infinite bounce). A hard GetJumpGate error (429-exhausted, 5xx, network) is surfaced
// immediately — the client already retried it, so re-reading here would not help.
func (h *JumpShipHandler) resolveDestinationGateWaypoint(ctx context.Context, originGateSymbol, destinationSystem, token string) (string, error) {
	originSystem := shared.ExtractSystemSymbol(originGateSymbol)
	logger := common.LoggerFromContext(ctx)
	// The router already recorded this hop's destination gate, written from this very
	// origin gate's connections list — the same string the live read returns, for a
	// symbol that does not change within an era. A miss, a stale row, or any store
	// failure falls through to the live reads below unchanged.
	if h.topologyStore != nil {
		if waypoint, ok, err := h.topologyStore.StoredGateWaypoint(ctx, originSystem, destinationSystem); err == nil && ok && waypoint != "" {
			return waypoint, nil
		}
	}
	for attempt := 0; attempt < maxJumpGateReadAttempts; attempt++ {
		gateData, err := h.apiClient.GetJumpGate(ctx, originSystem, originGateSymbol, token)
		if err != nil {
			return "", fmt.Errorf("failed to resolve jump gate connections for %s: %w", originGateSymbol, err)
		}
		if waypoint, ok := findDestinationGateWaypoint(gateData.Connections, destinationSystem); ok {
			return waypoint, nil
		}
		// Destination missing from THIS read. A charted gate momentarily returning an
		// incomplete/empty connection set is a transient live read, not a permanent
		// topology change — re-read (bounded) before concluding there is no connection.
		if attempt < maxJumpGateReadAttempts-1 {
			logger.Log("INFO", "Destination missing from live gate connections — re-reading (transient incomplete read, sp-hguq3)", map[string]interface{}{
				"action":             "jump_gate_reread",
				"origin_gate":        originGateSymbol,
				"destination_system": destinationSystem,
				"connections_seen":   len(gateData.Connections),
				"attempt":            attempt + 1,
			})
			h.clock.Sleep(jumpGateReadRetryBackoff)
		}
	}
	return "", fmt.Errorf("no jump gate connection from origin gate %s to system %s after %d live reads", originGateSymbol, destinationSystem, maxJumpGateReadAttempts)
}

// findDestinationGateWaypoint returns the connection in a jump gate's connections list
// whose system matches destinationSystem, and whether one was found. The live SpaceTraders
// jump API requires this full WAYPOINT (e.g. "X1-GQ92-I51"), not the bare system symbol, as
// waypointSymbol in the request body (sp-n0x7 round 2). Returning ok=false (rather than an
// error) lets the caller distinguish "missing from THIS read" — worth a bounded re-read
// (sp-hguq3) — from a hard fetch failure.
func findDestinationGateWaypoint(connections []string, destinationSystem string) (string, bool) {
	for _, conn := range connections {
		if shared.ExtractSystemSymbol(conn) == destinationSystem {
			return conn, true
		}
	}
	return "", false
}

// sourceGateComplete reports whether the jump gate at waypointSymbol has
// finished construction, i.e. is a valid SOURCE gate for a driveless jump.
// Returns an error if construction status could not be determined (no
// repository configured, or the lookup itself failed) - callers should fail
// open on error rather than block an otherwise-legal jump.
func (h *JumpShipHandler) sourceGateComplete(ctx context.Context, waypointSymbol string, playerID int) (bool, error) {
	// A gate the router already recorded as finished cannot have gone back to being
	// built, so re-reading it costs a request to learn nothing. Only that verdict is
	// taken from the record; anything else — not recorded, still building, stale, or a
	// store failure — is verified live below.
	if h.topologyStore != nil {
		if built, err := h.topologyStore.RecordedBuiltGate(ctx, waypointSymbol); err == nil && built {
			return true, nil
		}
	}
	if h.constructionRepo == nil {
		return false, fmt.Errorf("construction repository not configured")
	}
	site, err := h.constructionRepo.FindByWaypoint(ctx, waypointSymbol, playerID)
	if err != nil {
		return false, err
	}
	return site.IsComplete(), nil
}
