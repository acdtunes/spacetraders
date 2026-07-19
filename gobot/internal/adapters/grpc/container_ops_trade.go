package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// TradeRouteOperationResult reports the container started for a single-hull arbitrage
// circuit.
type TradeRouteOperationResult struct {
	ContainerID  string
	ShipSymbol   string
	SystemSymbol string
}

// operationTrade is the trade-route daemon's fleet identity for the atomic
// ClaimShip dedication check (sp-l7h2 Phase 2). It matches the live fleet
// vocabulary the captain pins with (`fleet assign --fleet trade` — e.g. the
// bulk-circuit heavy freighter), so a trade-pinned hull is claimable by its
// own circuits and by nothing else, and a hull pinned to any other fleet is
// rejected inside ClaimShip's locked transaction.
const operationTrade = "trade"

// StartTradeRoute launches a single-hull pure-arbitrage circuit as a recovery-safe
// daemon container (sp-zewt). Templated on the gas/factory coordinator start path:
//
//   - Idle-gap discipline: it refuses any hull that is not genuinely idle BEFORE
//     persisting anything, so a refused start has no side effects and never steals a
//     hull the daemon is actively flying. The ContainerRunner's AssignToContainer is
//     the secondary guard for the narrow check→assign race.
//   - Single-writer + release-on-death: the ContainerRunner claims the hull through the
//     normal lifecycle (createShipAssignments via the ship_symbol metadata) and
//     force-releases it on every terminal path (completion, crash, cancel), so the hull
//     is never stranded.
//   - Recovery-safe: the row is created RUNNING (runner.Start transitions PENDING→RUNNING),
//     and "trade_route" is registered in the command factory, so a daemon restart rebuilds
//     the circuit from its launch config or cleanly releases the hull.
//
// Ship movement inside the circuit goes through the daemon mediator's NavigateRouteCommand
// handler, which is backed by the RouteExecutor (orbit → refuel → NavigateDirect →
// arrival events) — so the container never spawns a re-claiming child navigate, avoiding
// self-collision and orbit-before-nav races.
//
// destWaypoint is the optional --dest lane-targeting override (sp-xwa1): a destination
// waypoint or system symbol that pins the circuit to that lane instead of the ranker's
// auto-selected one. Empty preserves the original undirected auto-scan unchanged; it is
// threaded into the persisted launch config (dest_waypoint) so a recovery rebuild resumes
// the same directed lane rather than reverting to auto-scan.
func (s *DaemonServer) StartTradeRoute(
	ctx context.Context,
	shipSymbol string,
	systemSymbol string,
	maxVisits int,
	playerID int,
	destWaypoint string,
) (*TradeRouteOperationResult, error) {
	if shipSymbol == "" {
		return nil, fmt.Errorf("ship symbol is required")
	}
	if systemSymbol == "" {
		return nil, fmt.Errorf("system symbol is required")
	}

	// Idle-gap discipline: only fly a genuinely idle hull, never steal one mid-task.
	ship, err := s.shipRepo.FindBySymbol(ctx, shipSymbol, shared.MustNewPlayerID(playerID))
	if err != nil {
		return nil, fmt.Errorf("failed to load ship %s: %w", shipSymbol, err)
	}
	if ship == nil {
		return nil, fmt.Errorf("ship %s not found", shipSymbol)
	}
	if !ship.IsIdle() {
		return nil, fmt.Errorf("ship %s is not idle (assigned to %q) - trade-route only takes idle-gap hulls", shipSymbol, ship.ContainerID())
	}

	containerID := utils.GenerateContainerID("trade-route", shipSymbol)
	config := map[string]interface{}{
		"ship_symbol":   shipSymbol,
		"system_symbol": systemSymbol,
		"container_id":  containerID,
		"max_visits":    maxVisits,
		"dest_waypoint": destWaypoint,
		// The runner claims the hull through the atomic operation-checked
		// ClaimShip when this key is present (sp-l7h2 Phase 2). Persisted in
		// the launch config so a recovery rebuild claims under the same fleet
		// identity.
		"operation": operationTrade,
	}

	// Build the circuit command through the same factory recovery uses, so the launch
	// config and the recovery rebuild can never drift.
	cmd, err := s.buildCommandForType("trade_route", config, playerID, containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to create trade-route command: %w", err)
	}

	// A trade-route is ONE runner iteration that owns the whole run (sp-1hj5):
	// the coordinator loops circuits internally until its RUN-scoped visit
	// budget (max_visits) is consumed or a margin/starvation/error exit fires,
	// then returns once — iterations=1, not the daemon coordinators' infinite
	// loop. The iteration wrapper must never re-enter the coordinator: a re-run
	// cannot resume the dynamically-ranked lane, which is why a laden exit is
	// threaded back as an honest FAILURE via the response's CompletionOutcome
	// (sp-7yej invariant 2) instead of a retryable error.
	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeTrading,
		playerID,
		1,   // one iteration = the whole run; the coordinator owns max_visits
		nil, // no parent — this is a top-level coordinator, recovered independently
		config,
		nil, // default RealClock
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "trade_route"); err != nil {
		return nil, fmt.Errorf("failed to persist trade-route container: %w", err)
	}

	// The runner claims the hull (ship_symbol metadata), flips the row to RUNNING, and
	// owns release-on-death.
	s.startContainerRunner(containerEntity, cmd, containerID, "Trade-route container")

	return &TradeRouteOperationResult{
		ContainerID:  containerID,
		ShipSymbol:   shipSymbol,
		SystemSymbol: systemSymbol,
	}, nil
}
