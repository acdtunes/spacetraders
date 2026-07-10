// Package outfitting implements ship module install/remove operations
// (sp-wh0t). A module install/remove CHANGES ship state (cargo capacity), so
// per RULING #3 it is a daemon-side operation and never a CLI-side API call.
// Every modification atomically claims the hull (RULING #7), gates the shipyard
// modification fee on the working-capital floor (RULING #4), and persists the
// ship's new capacity.
package outfitting

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// outfittingOperation is the claim op string (RULING #7 fleet identity). It
// deliberately matches no dedicated-fleet tag, so ClaimShip refuses to outfit a
// hull that is pinned to a real fleet ("contract", "manufacturing", ...) or
// claimed by another coordinator — the captain must free the hull first.
const outfittingOperation = "outfitting"

// defaultWorkingCapitalReserve is the hard, non-tunable working-capital floor
// (RULING #4/#5) applied to the modification fee: an install/remove must never
// drop live treasury below this. Mirrors the identically-named consts in the
// trade-circuit (bp6f) and factory (sp-9aoc) guards — deliberately duplicated,
// not shared, and never weakened.
const defaultWorkingCapitalReserve = 50000

// releaseContextTimeout bounds the cancellation-proof claim-release DB work.
const releaseContextTimeout = 10 * time.Second

// ContainerRepository is the minimal container-persistence port the outfitting
// handler needs. The op claims the ship directly (ClaimShip / ForceRelease)
// rather than running through ContainerRunner — it must return a rich, typed
// response synchronously — so it needs a lightweight container record purely to
// satisfy the ships table's (container_id, player_id) foreign key. Mirrors the
// ContainerRepository declared in jump_ship.go.
type ContainerRepository interface {
	Add(ctx context.Context, containerEntity *domainContainer.Container, commandType string) error
	Remove(ctx context.Context, containerID string, playerID int) error
}

// OutfittingHandler serves InstallModule, RemoveModule and ListShipModules. A
// single handler backs all three (registered against each request type) because
// they share the ship-outfitting deps and claim/persist machinery.
type OutfittingHandler struct {
	shipRepo       navigation.ShipRepository
	playerRepo     player.PlayerRepository
	apiClient      ports.APIClient
	containerRepo  ContainerRepository
	clock          shared.Clock
	playerResolver *common.PlayerResolver
}

// NewOutfittingHandler creates an OutfittingHandler. If clock is nil, uses the
// real clock (production default).
func NewOutfittingHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient ports.APIClient,
	containerRepo ContainerRepository,
	clock shared.Clock,
) *OutfittingHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &OutfittingHandler{
		shipRepo:       shipRepo,
		playerRepo:     playerRepo,
		apiClient:      apiClient,
		containerRepo:  containerRepo,
		clock:          clock,
		playerResolver: common.NewPlayerResolver(playerRepo),
	}
}

// Handle dispatches to the install/remove/list flows by request type. One
// handler instance is registered for all three commands.
func (h *OutfittingHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	switch cmd := request.(type) {
	case *InstallModuleCommand:
		return h.handleInstall(ctx, cmd)
	case *RemoveModuleCommand:
		return h.handleRemove(ctx, cmd)
	case *ListShipModulesQuery:
		return h.handleList(ctx, cmd)
	default:
		return nil, fmt.Errorf("OutfittingHandler: unsupported request type %T", request)
	}
}

// moduleOutcome is the shared result of an install or remove.
type moduleOutcome struct {
	CargoCapacity int
	Fee           int
	Modules       []ports.ModuleInfo
}

// modifyModule is the shared install/remove flow. verb is "install" or
// "remove" (for messages/logs); preCheck returns an honest user-facing error if
// the modification is not legal for the ship's current state; apiCall performs
// the actual API modification. The flow, in order (mirrors the dispatch brief):
// create the FK-parent container → atomically claim the hull → reload the
// claim-aware ship → pre-check → gate the fee on the working-capital floor →
// dock → modify via API → persist the new capacity → release the claim.
func (h *OutfittingHandler) modifyModule(
	ctx context.Context,
	verb string,
	shipSymbol string,
	moduleSymbol string,
	playerID shared.PlayerID,
	preCheck func(ship *navigation.Ship) error,
	apiCall func(ctx context.Context, token string) (*ports.ModuleModificationResult, error),
) (*moduleOutcome, error) {
	logger := common.LoggerFromContext(ctx)

	player, err := h.playerRepo.FindByID(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get player: %w", err)
	}

	// Existence guarantee: FindBySymbol SyncFromAPI-falls-back when the row is
	// missing, so the ships row exists before ClaimShip locks it.
	if _, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, playerID); err != nil {
		return nil, fmt.Errorf("failed to get ship %s: %w", shipSymbol, err)
	}

	// 1. Persist the FK-parent container row FIRST (the claim writes
	//    ships.container_id, which references containers(id, player_id)), then
	//    take the atomic, operation-checked claim (RULING #3/#7). A unique id
	//    avoids colliding with a concurrent outfit of the same hull — that race
	//    is arbitrated by ClaimShip, which refuses the second claimant.
	containerID := fmt.Sprintf("ship-outfit-%s-%d", shipSymbol, h.clock.Now().UnixNano())
	containerEntity := domainContainer.NewContainer(
		containerID,
		domainContainer.ContainerTypeOutfitting,
		playerID.Value(),
		1,
		nil,
		map[string]interface{}{
			"ship_symbol": shipSymbol,
			"module":      moduleSymbol,
			"action":      verb,
		},
		h.clock,
	)
	if err := h.containerRepo.Add(ctx, containerEntity, "outfit_ship"); err != nil {
		return nil, fmt.Errorf("failed to create outfitting container record: %w", err)
	}
	defer h.removeContainer(containerID, playerID.Value())

	if err := h.shipRepo.ClaimShip(ctx, shipSymbol, containerID, playerID, outfittingOperation); err != nil {
		// Honest refusal: hull dedicated to another fleet, claimed by another
		// container, or reserved by the captain (RULING #7). Nothing was
		// claimed; the container row is cleaned up by the defer above.
		return nil, fmt.Errorf("cannot %s module on %s: %w", verb, shipSymbol, err)
	}
	// The hull is claimed — guarantee release on every subsequent exit.
	defer h.releaseClaim(shipSymbol, playerID, fmt.Sprintf("outfit_%s_done", verb))

	// 2. Reload the ship so the in-memory entity carries the claim (Dock's Save
	//    must preserve it) and reflects the persisted cargo/modules.
	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship %s after claim: %w", shipSymbol, err)
	}

	// 3. Pre-check: honest error if the modification is not legal for the ship.
	if err := preCheck(ship); err != nil {
		return nil, err
	}

	// 4. Money guard (RULING #4, fails CLOSED): the modification carries a
	//    shipyard fee. If the live balance OR the fee cannot be read, do not
	//    spend.
	breached, credits, fee, reason := h.floorGuardBreached(ctx, ship, player.Token)
	if breached {
		logger.Log("WARNING", fmt.Sprintf("Parked %s of %s on %s — %s", verb, moduleSymbol, shipSymbol, reason), map[string]interface{}{
			"ship":    shipSymbol,
			"module":  moduleSymbol,
			"credits": credits,
			"fee":     fee,
			"reserve": defaultWorkingCapitalReserve,
		})
		return nil, fmt.Errorf("cannot %s module %s on %s: %s", verb, moduleSymbol, shipSymbol, reason)
	}

	// 5. Ensure the ship is docked (module modifications require a docked ship
	//    at a shipyard). Dock is idempotent.
	if err := h.shipRepo.Dock(ctx, ship, playerID); err != nil {
		return nil, fmt.Errorf("failed to dock %s to %s module: %w", shipSymbol, verb, err)
	}

	// 6. Perform the modification via the API.
	result, err := apiCall(ctx, player.Token)
	if err != nil {
		return nil, err
	}

	// 7. Persist the ship's updated state — the new cargo capacity is the whole
	//    point (RULING #3: the daemon writes ship state). SyncShipFromAPI
	//    re-fetches the full ship and preserves the claim columns.
	capacity := result.CargoCapacity
	synced, err := h.shipRepo.SyncShipFromAPI(ctx, shipSymbol, playerID)
	if err != nil {
		// The modification already succeeded server-side and cannot be rolled
		// back; a persist failure is surfaced but the fresh capacity from the
		// API response is still authoritative for the response. The daemon's
		// next ship refresh reconciles the row.
		logger.Log("WARNING", fmt.Sprintf("Completed %s of module %s on %s but failed to persist ship state: %v", verb, moduleSymbol, shipSymbol, err), map[string]interface{}{
			"ship":   shipSymbol,
			"module": moduleSymbol,
		})
	} else if synced != nil {
		capacity = synced.CargoCapacity()
	}

	logger.Log("INFO", fmt.Sprintf("Completed %s of module %s on %s: fee %d, cargo capacity now %d", verb, moduleSymbol, shipSymbol, result.Fee, capacity), map[string]interface{}{
		"ship":           shipSymbol,
		"module":         moduleSymbol,
		"fee":            result.Fee,
		"cargo_capacity": capacity,
	})

	return &moduleOutcome{
		CargoCapacity: capacity,
		Fee:           result.Fee,
		Modules:       result.Modules,
	}, nil
}

// floorGuardBreached reports whether performing the modification would breach
// the hard working-capital reserve (RULING #4, fails CLOSED). The projected
// cost is the shipyard modification fee at the ship's current waypoint — the
// price the modification will charge. Per RULING #4, if the live balance OR the
// price cannot be read, the guard returns breached=true (do not spend).
//
// apiClient == nil returns breached=false: the guard is unavailable only in
// unit-test fixtures that inject no API client, mirroring the codebase's
// spend-floor convention.
func (h *OutfittingHandler) floorGuardBreached(ctx context.Context, ship *navigation.Ship, token string) (breached bool, credits int, fee int, reason string) {
	if h.apiClient == nil {
		return false, 0, 0, ""
	}

	waypoint := ship.CurrentLocation().Symbol
	systemSymbol := shared.ExtractSystemSymbol(waypoint)
	shipyard, err := h.apiClient.GetShipyard(ctx, systemSymbol, waypoint, token)
	if err != nil {
		return true, 0, 0, fmt.Sprintf("could not read the shipyard modification fee at %s — the ship must be at a shipyard to be outfitted (fail-closed): %v", waypoint, err)
	}
	fee = shipyard.ModificationFee

	agent, err := h.apiClient.GetAgent(ctx, token)
	if err != nil {
		return true, 0, fee, fmt.Sprintf("could not read live treasury for the spend-floor check (fail-closed): %v", err)
	}

	if agent.Credits-fee < defaultWorkingCapitalReserve {
		return true, agent.Credits, fee, fmt.Sprintf("modification fee %d would drop treasury %d below the %d working-capital reserve", fee, agent.Credits, defaultWorkingCapitalReserve)
	}
	return false, agent.Credits, fee, ""
}

// releaseClaim releases the outfitting claim on shipSymbol. It reloads the ship
// FRESH from the DB (so it carries whatever capacity was just persisted) before
// clearing the assignment, guaranteeing the release never clobbers the new
// cargo capacity. Runs on a cancellation-proof context so release survives even
// if the RPC's context was cancelled (RULING #2). Best-effort: the daemon's
// startup ReleaseAllActive sweep is the backstop.
func (h *OutfittingHandler) releaseClaim(shipSymbol string, playerID shared.PlayerID, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), releaseContextTimeout)
	defer cancel()

	ship, err := h.shipRepo.FindBySymbol(ctx, shipSymbol, playerID)
	if err != nil {
		return
	}
	ship.ForceRelease(reason, h.clock)
	_ = h.shipRepo.Save(ctx, ship)
}

// removeContainer deletes the lightweight outfitting container row on a
// cancellation-proof context.
func (h *OutfittingHandler) removeContainer(containerID string, playerID int) {
	ctx, cancel := context.WithTimeout(context.Background(), releaseContextTimeout)
	defer cancel()
	_ = h.containerRepo.Remove(ctx, containerID, playerID)
}
