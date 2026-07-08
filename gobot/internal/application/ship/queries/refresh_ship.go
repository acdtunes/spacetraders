package queries

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// releaseReasonStaleClaim marks assignments cleared by refresh-time stale-claim
// reconciliation, keeping them distinguishable in the audit trail from ordinary
// releases (a worker finishing) and from daemon-startup ReleaseAllActive cleanup.
const releaseReasonStaleClaim = "stale_claim_reconciled"

// ContainerStatusReader reports the lifecycle status of the container that owns a
// ship's claim, so RefreshShip can tell a live worker's claim apart from an
// orphaned one left behind by a trade-route CLI runner that died mid-circuit
// (sp-vjwb).
//
// found=false means the container row no longer exists.
type ContainerStatusReader interface {
	ContainerStatus(ctx context.Context, containerID string, playerID shared.PlayerID) (status string, found bool, err error)
}

// RefreshShipQuery forces a resync of a ship's state from the SpaceTraders API,
// overwriting the daemon's local cache. Unlike GetShip (which serves the cache),
// RefreshShip reconciles a desynced cache without a daemon restart.
type RefreshShipQuery struct {
	ShipSymbol  string // Required: ship symbol to refresh
	PlayerID    *int   // Optional: query by player ID
	AgentSymbol string // Optional: query by agent symbol
}

// RefreshShipResponse holds the server-true ship state after reconciliation.
type RefreshShipResponse struct {
	Ship *navigation.Ship
}

// RefreshShipHandler handles the RefreshShip query
type RefreshShipHandler struct {
	shipRepo        navigation.ShipRepository
	playerResolver  *common.PlayerResolver
	containerReader ContainerStatusReader
	clock           shared.Clock
}

// NewRefreshShipHandler creates a new RefreshShipHandler.
//
// containerReader may be nil to disable stale-claim reconciliation (refresh then
// only resyncs ship state). A nil clock defaults to the real wall clock.
func NewRefreshShipHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	containerReader ContainerStatusReader,
	clock shared.Clock,
) *RefreshShipHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &RefreshShipHandler{
		shipRepo:        shipRepo,
		playerResolver:  common.NewPlayerResolver(playerRepo),
		containerReader: containerReader,
		clock:           clock,
	}
}

// Handle executes the RefreshShip query
func (h *RefreshShipHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*RefreshShipQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *RefreshShipQuery")
	}

	if query.ShipSymbol == "" {
		return nil, fmt.Errorf("ship_symbol is required")
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, query.PlayerID, query.AgentSymbol)
	if err != nil {
		return nil, err
	}

	// Force a fresh GET /my/ships/<symbol> and write it through to the cache,
	// overwriting stale cargo + nav state. This is the reconciliation a daemon
	// restart performs today, exposed as a Captain-accessible verb. SyncShipFromAPI
	// preserves the existing assignment columns, so the returned ship still carries
	// any claim the ships row holds.
	ship, err := h.shipRepo.SyncShipFromAPI(ctx, query.ShipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh ship: %w", err)
	}

	// Self-heal a hull deadlocked by a dead trade-route CLI runner: if this ship
	// still carries a claim whose owning container is orphaned, clear it so the
	// hull is free for the mfg coordinator / trade-route to pick up (sp-vjwb).
	if err := h.reconcileStaleClaim(ctx, ship, playerID); err != nil {
		return nil, fmt.Errorf("failed to reconcile stale claim for ship %s: %w", query.ShipSymbol, err)
	}

	return &RefreshShipResponse{
		Ship: ship,
	}, nil
}

// reconcileStaleClaim clears the ship's assignment when the container that owns
// the claim is orphaned. It clears ONLY on positive evidence of orphaning; any
// uncertainty (no reader wired, ship unassigned, container-status read error)
// leaves the claim untouched, so a live worker's ship is never ripped away.
func (h *RefreshShipHandler) reconcileStaleClaim(ctx context.Context, ship *navigation.Ship, playerID shared.PlayerID) error {
	if h.containerReader == nil || !ship.IsAssigned() {
		return nil
	}

	// sp-i1ku: a captain reservation is an active assignment with no
	// container_id — it was never a container claim. Without this guard it
	// would fall straight into the lookup below, ask the container reader
	// about an empty ID, get back "not found", and get reaped by
	// isClaimOrphaned exactly like a dead CLI-runner's claim. A captain
	// reservation has no container to go stale, so it is excluded before the
	// lookup ever happens, not just from the orphan verdict.
	if ship.IsReservedByCaptain() {
		return nil
	}

	status, found, err := h.containerReader.ContainerStatus(ctx, ship.ContainerID(), playerID)
	if err != nil {
		// Can't determine the owner's state — conservatively keep the claim. The
		// next refresh retries; we never clear without positive orphan evidence.
		return nil
	}

	if !isClaimOrphaned(status, found) {
		return nil
	}

	// Positive orphan evidence — free the hull. ForceRelease + Save clears
	// container_id -> NULL, assignment_status -> idle, and records released_at +
	// release_reason. This is the same release path daemon workers run on stop
	// (ContainerRunner.releaseShipAssignments), so the ships row ends up in a
	// clean idle state the mfg coordinator / trade-route can re-claim.
	ship.ForceRelease(releaseReasonStaleClaim, h.clock)
	if err := h.shipRepo.Save(ctx, ship); err != nil {
		return fmt.Errorf("failed to persist stale-claim release: %w", err)
	}
	return nil
}

// isClaimOrphaned reports whether a ship claim whose owning container has the
// given (status, found) is a stale artifact that is safe to clear.
//
// Orphaned iff the container row is GONE, or the container is PENDING. A PENDING
// container holding a claim is by definition a dead trade-route CLI-runner
// artifact: daemon workers persist RUNNING *before* they claim a ship
// (ContainerRunner.Start), and restart recovery resurrects only RUNNING /
// INTERRUPTED containers — so no live or recoverable daemon worker ever owns a
// claim through a PENDING container. RUNNING / INTERRUPTED are live or
// recoverable and are never cleared.
func isClaimOrphaned(status string, found bool) bool {
	if !found {
		return true
	}
	return status == string(container.ContainerStatusPending)
}
