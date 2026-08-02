// Package outfitting implements ship module install/remove operations.
// A module install/remove CHANGES ship state (cargo capacity), so
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
	ledgerCommands "github.com/andrescamacho/spacetraders-go/internal/application/ledger/commands"
	domainContainer "github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
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
// drop live treasury below this. Sourced from the ONE shared immutable floor
// (common.ImmutableReserveFloor) that the trade-circuit (bp6f) and
// factory guards now also reference — a single definition the guards
// can never disagree on, never weakened.
const defaultWorkingCapitalReserve = common.ImmutableReserveFloor

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

// yardFeeReader is the one verb the outfitting spend-floor needs from the fleet's
// shipyard scanner: a METERED live shipyard read, from which the modification fee
// is taken. *ship.ShipyardScanner satisfies it.
//
// The fee is not persisted anywhere — the shipyard inventory store records ship
// types and their purchase prices, not the counter's modification charge — so
// unlike the catalogue-searching callers this guard has no store to fall back on
// and its read is genuinely undeniable. That is precisely the Earning class:
// metered against the same allowance, never declined.
type yardFeeReader interface {
	ReadShipyard(ctx context.Context, playerID uint, waypointSymbol string, class marketscan.Class) (*ports.ShipyardData, error)
}

// OutfittingHandler serves InstallModule, RemoveModule and ListShipModules. A
// single handler backs all three (registered against each request type) because
// they share the ship-outfitting deps and claim/persist machinery.
type OutfittingHandler struct {
	shipRepo       navigation.ShipRepository
	playerRepo     player.PlayerRepository
	apiClient      ports.APIClient
	yards          yardFeeReader
	containerRepo  ContainerRepository
	mediator       common.Mediator
	clock          shared.Clock
	playerResolver *common.PlayerResolver
}

// NewOutfittingHandler creates an OutfittingHandler. If clock is nil, uses the
// real clock (production default). yards is the metered shipyard reader the
// spend-floor takes the modification fee from; a nil one leaves the guard
// unavailable rather than reaching the API unmetered. mediator carries the
// shipyard fee to the financial ledger; a nil one records nothing
// and is logged loudly at spend time rather than silently dropping the row.
func NewOutfittingHandler(
	shipRepo navigation.ShipRepository,
	playerRepo player.PlayerRepository,
	apiClient ports.APIClient,
	yards yardFeeReader,
	containerRepo ContainerRepository,
	mediator common.Mediator,
	clock shared.Clock,
) *OutfittingHandler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &OutfittingHandler{
		shipRepo:       shipRepo,
		playerRepo:     playerRepo,
		apiClient:      apiClient,
		yards:          yards,
		containerRepo:  containerRepo,
		mediator:       mediator,
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
// moduleOp names one module modification: the verb used in logs and operation
// records, the hull, the module, and the player it runs under.
type moduleOp struct {
	verb         string
	shipSymbol   string
	moduleSymbol string
	playerID     shared.PlayerID
}

func (h *OutfittingHandler) modifyModule(
	ctx context.Context,
	op moduleOp,
	preCheck func(ship *navigation.Ship) error,
	apiCall func(ctx context.Context, token string) (*ports.ModuleModificationResult, error),
) (*moduleOutcome, error) {
	logger := common.LoggerFromContext(ctx)

	player, err := h.playerRepo.FindByID(ctx, op.playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get player: %w", err)
	}

	// Existence guarantee: FindBySymbol SyncFromAPI-falls-back when the row is
	// missing, so the ships row exists before ClaimShip locks it.
	if _, err := h.shipRepo.FindBySymbol(ctx, op.shipSymbol, op.playerID); err != nil {
		return nil, fmt.Errorf("failed to get ship %s: %w", op.shipSymbol, err)
	}

	// 1. Persist the FK-parent container row FIRST (the claim writes
	//    ships.container_id, which references containers(id, player_id)), then
	//    take the atomic, operation-checked claim (RULING #3/#7). A unique id
	//    avoids colliding with a concurrent outfit of the same hull — that race
	//    is arbitrated by ClaimShip, which refuses the second claimant.
	containerID, containerEntity := h.newOutfittingContainer(op)
	if err := h.containerRepo.Add(ctx, containerEntity, "outfit_ship"); err != nil {
		return nil, fmt.Errorf("failed to create outfitting container record: %w", err)
	}
	defer h.removeContainer(containerID, op.playerID.Value())

	if err := h.shipRepo.ClaimShip(ctx, op.shipSymbol, containerID, op.playerID, outfittingOperation); err != nil {
		// Honest refusal: hull dedicated to another fleet, claimed by another
		// container, or reserved by the captain (RULING #7). Nothing was
		// claimed; the container row is cleaned up by the defer above.
		return nil, fmt.Errorf("cannot %s module on %s: %w", op.verb, op.shipSymbol, err)
	}
	// The hull is claimed — guarantee release on every subsequent exit.
	defer h.releaseClaim(op.shipSymbol, op.playerID, fmt.Sprintf("outfit_%s_done", op.verb))

	// 2. Reload the ship so the in-memory entity carries the claim (Dock's Save
	//    must preserve it) and reflects the persisted cargo/modules.
	ship, err := h.shipRepo.FindBySymbol(ctx, op.shipSymbol, op.playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to reload ship %s after claim: %w", op.shipSymbol, err)
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
		logger.Log("WARNING", fmt.Sprintf("Parked %s of %s on %s — %s", op.verb, op.moduleSymbol, op.shipSymbol, reason), map[string]interface{}{
			"ship":    op.shipSymbol,
			"module":  op.moduleSymbol,
			"credits": credits,
			"fee":     fee,
			"reserve": defaultWorkingCapitalReserve,
		})
		return nil, fmt.Errorf("cannot %s module %s on %s: %s", op.verb, op.moduleSymbol, op.shipSymbol, reason)
	}

	// 5. Ensure the ship is docked (module modifications require a docked ship
	//    at a shipyard). Dock is idempotent.
	if err := h.shipRepo.Dock(ctx, ship, op.playerID); err != nil {
		return nil, fmt.Errorf("failed to dock %s to %s module: %w", op.shipSymbol, op.verb, err)
	}

	// 6. Perform the modification via the API.
	result, err := apiCall(ctx, player.Token)
	if err != nil {
		return nil, err
	}

	// 7. Record the shipyard fee in the financial ledger BEFORE the
	//    state persist: the credits are already gone server-side, and a persist
	//    failure must not be the reason the spend goes unrecorded.
	h.recordModificationFee(ctx, op, player.AgentSymbol, result)

	// 8. Persist the ship's updated state — the new cargo capacity is the whole
	//    point (RULING #3: the daemon writes ship state). SyncShipFromAPI
	//    re-fetches the full ship and preserves the claim columns.
	capacity := h.persistOutfittedShip(ctx, op, result.CargoCapacity)

	logger.Log("INFO", fmt.Sprintf("Completed %s of module %s on %s: fee %d, cargo capacity now %d", op.verb, op.moduleSymbol, op.shipSymbol, result.Fee, capacity), map[string]interface{}{
		"ship":           op.shipSymbol,
		"module":         op.moduleSymbol,
		"fee":            result.Fee,
		"cargo_capacity": capacity,
	})

	return &moduleOutcome{
		CargoCapacity: capacity,
		Fee:           result.Fee,
		Modules:       result.Modules,
	}, nil
}

// newOutfittingContainer builds the FK-parent container row. The id carries a nonce
// so a concurrent outfit of the same hull cannot collide on the primary key.
func (h *OutfittingHandler) newOutfittingContainer(op moduleOp) (string, *domainContainer.Container) {
	containerID := fmt.Sprintf("ship-outfit-%s-%d", op.shipSymbol, h.clock.Now().UnixNano())
	return containerID, domainContainer.NewContainer(
		containerID,
		domainContainer.ContainerTypeOutfitting,
		op.playerID.Value(),
		1,
		nil,
		map[string]interface{}{
			"ship_symbol": op.shipSymbol,
			"module":      op.moduleSymbol,
			"action":      op.verb,
		},
		h.clock,
	)
}

// persistOutfittedShip re-fetches the hull for the new capacity. The modification
// cannot be rolled back, so apiCapacity stays authoritative if the persist fails.
func (h *OutfittingHandler) persistOutfittedShip(ctx context.Context, op moduleOp, apiCapacity int) int {
	synced, err := h.shipRepo.SyncShipFromAPI(ctx, op.shipSymbol, op.playerID)
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Completed %s of module %s on %s but failed to persist ship state: %v", op.verb, op.moduleSymbol, op.shipSymbol, err), map[string]interface{}{
			"ship":   op.shipSymbol,
			"module": op.moduleSymbol,
		})
		return apiCapacity
	}
	if synced != nil {
		return synced.CargoCapacity()
	}
	return apiCapacity
}

// recordModificationFee writes the shipyard modification fee to the financial
// ledger. Same defect class as the jump gate fee: the API charges it,
// the adapter parsed it into ModuleModificationResult.Fee (and the in-band
// post-transaction credits into AgentCredits), and nothing consumed either — so
// an autonomous auto-outfit install moved credits with no ledger row, letting the
// ledger over-report the balance. Over-reporting is the one direction a
// fail-closed money guard cannot survive.
//
// AgentCredits is passed as AuthoritativeBalance so the row RE-ANCHORS the running
// chain to API truth instead of merely appending to it. Best-effort: a ledger
// failure is logged, never returned — the module is already installed and the fee
// already paid, so failing here would only discard a correct outcome.
func (h *OutfittingHandler) recordModificationFee(
	ctx context.Context,
	op moduleOp,
	agentSymbol string,
	result *ports.ModuleModificationResult,
) {
	logger := common.LoggerFromContext(ctx)

	// A zero fee is not a transaction: ledger.Validate rejects amount == 0.
	if result.Fee == 0 {
		return
	}

	if h.mediator == nil {
		logger.Log("ERROR", "Cannot record shipyard modification fee: no mediator wired", map[string]interface{}{
			"ship":   op.shipSymbol,
			"module": op.moduleSymbol,
			"fee":    result.Fee,
		})
		return
	}

	txType := ledger.TransactionTypeModuleInstall
	if op.verb == "remove" {
		txType = ledger.TransactionTypeModuleRemove
	}

	if agentSymbol == "" {
		agentSymbol = "UNKNOWN"
	}

	// Zero baseline: nil AgentCredits reconstructs from the running chain, a set
	// one re-anchors to API truth.
	const balanceBefore = 0

	recordCmd := &ledgerCommands.RecordTransactionCommand{
		PlayerID:             op.playerID.Value(),
		TransactionType:      string(txType),
		Amount:               -result.Fee, // Negative: the fee is charged in BOTH directions, install and remove
		BalanceBefore:        balanceBefore,
		BalanceAfter:         balanceBefore - result.Fee,
		AuthoritativeBalance: result.AgentCredits,
		Description:          fmt.Sprintf("Shipyard fee to %s module %s on %s", op.verb, op.moduleSymbol, op.shipSymbol),
		Metadata: map[string]interface{}{
			"agent":       agentSymbol,
			"ship_symbol": op.shipSymbol,
			"module":      op.moduleSymbol,
			"action":      op.verb,
		},
	}

	if opCtx := shared.OperationContextFromContext(ctx); opCtx != nil && opCtx.IsValid() {
		recordCmd.RelatedEntityType = "container"
		recordCmd.RelatedEntityID = opCtx.ContainerID
		recordCmd.OperationType = opCtx.NormalizedOperationType()
	} else {
		recordCmd.OperationType = "manual"
	}

	// context.Background(): the spend is already real, so the record must not be
	// lost to a cancelled caller context (mirrors refuel/cargo/jump recording).
	if _, err := h.mediator.Send(context.Background(), recordCmd); err != nil {
		logger.Log("ERROR", "Failed to record shipyard modification fee in ledger", map[string]interface{}{
			"error":     err.Error(),
			"ship":      op.shipSymbol,
			"module":    op.moduleSymbol,
			"fee":       result.Fee,
			"player_id": op.playerID.Value(),
		})
	}
}

// floorGuardBreached reports whether performing the modification would breach
// the hard working-capital reserve (RULING #4, fails CLOSED). The projected
// cost is the shipyard modification fee at the ship's current waypoint — the
// price the modification will charge. Per RULING #4, if the live balance OR the
// price cannot be read, the guard returns breached=true (do not spend).
//
// A nil apiClient or nil yard reader returns breached=false: the guard is
// unavailable only in unit-test fixtures that inject neither, mirroring the
// codebase's spend-floor convention.
//
// The fee read is Earning-class against the fleet's shipyard-read budget — metered
// like every other shipyard request, but never declined, because there is no
// stored modification fee to fall back on and a guard answered from memory is not
// a guard (RULINGS #4).
func (h *OutfittingHandler) floorGuardBreached(ctx context.Context, ship *navigation.Ship, token string) (breached bool, credits int, fee int, reason string) {
	if h.apiClient == nil || h.yards == nil {
		return false, 0, 0, ""
	}

	waypoint := ship.CurrentLocation().Symbol
	shipyard, err := h.yards.ReadShipyard(ctx, uint(ship.PlayerID().Value()), waypoint, marketscan.Earning)
	if err != nil && shipyard == nil {
		return true, 0, 0, fmt.Sprintf("could not read the shipyard modification fee at %s — the ship must be at a shipyard to be outfitted (fail-closed): %v", waypoint, err)
	}
	if shipyard == nil {
		return true, 0, 0, fmt.Sprintf("the shipyard modification fee at %s was not read live (fail-closed)", waypoint)
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

// releaseClaim releases the outfitting claim on shipSymbol under CAS-retry:
// SaveWithRetry re-loads the ship FRESH (so it carries whatever
// capacity/cargo was just persisted) and re-applies ForceRelease on that fresh
// row on every version conflict, so the release never clobbers a concurrent
// writer's fields — closing the residual find→save race the plain Save left open.
// Skips the write when the hull is already idle (changed=false), so no spurious
// version bump. Runs on a cancellation-proof context so release survives even if
// the RPC's context was cancelled (RULING #2). Best-effort: the daemon's startup
// ReleaseAllActive sweep is the backstop.
func (h *OutfittingHandler) releaseClaim(shipSymbol string, playerID shared.PlayerID, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), releaseContextTimeout)
	defer cancel()

	_, _, _ = h.shipRepo.SaveWithRetry(ctx, shipSymbol, playerID,
		func(sh *navigation.Ship) (bool, error) {
			if !sh.IsAssigned() {
				return false, nil
			}
			sh.ForceRelease(reason, h.clock)
			return true, nil
		})
}

// removeContainer deletes the lightweight outfitting container row on a
// cancellation-proof context.
func (h *OutfittingHandler) removeContainer(containerID string, playerID int) {
	ctx, cancel := context.WithTimeout(context.Background(), releaseContextTimeout)
	defer cancel()
	_ = h.containerRepo.Remove(ctx, containerID, playerID)
}
