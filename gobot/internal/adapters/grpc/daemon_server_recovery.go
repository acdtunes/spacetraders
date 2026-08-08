package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// retiredCommandTypes are container command types deliberately removed from the registry by a
// retirement. A persisted RUNNING/INTERRUPTED row of one of these types has no builder, so
// generic recovery would mark it "recovery_failed" and log it as an unexplained loss. The
// recovery loop skips these explicitly — marking the row terminated and exempting it from the
// loss summary — so a post-retirement daemon boot recovers cleanly with no
// unknown-command-type errors. Data-driven: a future retirement adds one line here, with its
// attribution.
var retiredCommandTypes = map[string]bool{
	// The factory-ops retirement: goods-factory, factory-siting,
	// worker-rebalancer, and the vestigial manufacturing coordinator.
	"goods_factory_coordinator":     true,
	"siting_coordinator":            true,
	"worker_rebalancer_coordinator": true,
	"manufacturing_coordinator":     true,
	// The probe-sensing retirement: the market-freshness sizer and the frontier
	// expansion coordinator are superseded by probe_sensing_coordinator. Their
	// rows are marked terminated on the first post-retirement boot instead of
	// alarming as unexplained losses; the registry refuses to build them either way.
	"market_freshness_sizer_coordinator": true,
	"frontier_expansion_coordinator":     true,
	// The probe-buyer retirement (Admiral 2026-07-28): a second engine buying into the same probe
	// fleet the sensing drain already supplies. It spent 245,316 credits on 9 hulls in five minutes
	// on the live fleet before it was stopped by hand. Deleted outright, not disabled.
	"probe_buyer_coordinator": true,
	// The fleet-autosizer retirement: growth owns trade capacity and the scaler owns contract capacity,
	// so the second buyer sizing into the same pools against one treasury is gone. A persisted row is
	// marked terminated on the first post-retirement boot rather than alarming as an unexplained loss,
	// and with no builder left it can never be rebuilt into a ghost buyer.
	"fleet_autosizer": true,
}

// RecoverRunningContainers recovers containers that were RUNNING or INTERRUPTED when daemon stopped
// INTERRUPTED = graceful shutdown (daemon called interruptAllContainers)
// RUNNING = ungraceful shutdown (kill -9, crash) - backwards compatibility
func (s *DaemonServer) RecoverRunningContainers(ctx context.Context) error {
	// Query database for INTERRUPTED containers (graceful shutdown)
	interruptedContainers, err := s.containerRepo.ListByStatus(ctx, container.ContainerStatusInterrupted, nil)
	if err != nil {
		return fmt.Errorf("failed to list INTERRUPTED containers: %w", err)
	}

	// Query database for RUNNING containers (ungraceful shutdown - backwards compatibility)
	runningContainers, err := s.containerRepo.ListByStatus(ctx, container.ContainerStatusRunning, nil)
	if err != nil {
		return fmt.Errorf("failed to list RUNNING containers: %w", err)
	}

	// Combine both lists
	allContainers := append(interruptedContainers, runningContainers...)

	if len(allContainers) == 0 {
		fmt.Println("No containers to recover")
		return nil
	}

	fmt.Printf("Recovering %d container(s) from previous daemon instance (%d INTERRUPTED, %d RUNNING)...\n",
		len(allContainers), len(interruptedContainers), len(runningContainers))

	// After a universe reset, a prior era's containers must NOT be re-instantiated against
	// the reset universe. A resolution error aborts untouched so the next restart retries.
	openEra, err := persistence.NewEraRepository(s.db).FindOpenEra(ctx)
	if err != nil {
		return fmt.Errorf("failed to resolve open era for container recovery: %w", err)
	}

	// Track every candidate's outcome so the pass can diff expected-running against what
	// actually ended running: anything missing that is not a by-design skip is announced.
	recovered := make(map[string]bool)          // ended the pass as a running container
	exempt := make(map[string]bool)             // deliberately not running (respawn / dead-era) — no alarm
	failReason := make(map[string]recoveryLoss) // explicitly failed, with a captured reason
	coordinatorSkipCount := 0
	deadEraCount := 0

	for _, containerModel := range allContainers {
		// Before the worker-adoption checks, so an entire dead-era subtree (coordinators AND
		// their workers) stays down. A deliberate reset skip, not a loss.
		if openEra == nil || containerModel.PlayerID != openEra.PlayerID {
			s.markContainerDeadEra(ctx, containerModel, openEra)
			exempt[containerModel.ID] = true
			deadEraCount++
			continue
		}

		var config map[string]interface{}
		if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
			fmt.Printf("Container %s: Failed to parse config JSON, marking as FAILED: %v\n", containerModel.ID, err)
			s.markContainerFailed(ctx, containerModel, "invalid_config", fmt.Sprintf("JSON parse error: %v", err))
			failReason[containerModel.ID] = newRecoveryLoss(containerModel, fmt.Sprintf("invalid_config: %v", err))
			continue
		}

		// A worker is respawned by its coordinator by design, so it is NOT expected to end
		// this pass running — exempt from the loss diff, or every restart false-alarms.
		if s.interruptWorkerContainer(ctx, containerModel, config) {
			exempt[containerModel.ID] = true
			coordinatorSkipCount++
			continue
		}

		// A persisted row of a RETIRED command type has no builder. Skipping it cleanly beats
		// letting generic recovery report a spurious "recovery_failed".
		if retiredCommandTypes[containerModel.CommandType] {
			fmt.Printf("Container %s: Skipping recovery (retired command type '%s')\n", containerModel.ID, containerModel.CommandType)
			s.markContainerFailed(ctx, containerModel, "retired_command_type", fmt.Sprintf("command type '%s' retired", containerModel.CommandType))
			exempt[containerModel.ID] = true
			continue
		}

		if err := s.recoverContainer(ctx, containerModel, config); err != nil {
			fmt.Printf("Container %s: Recovery failed: %v\n", containerModel.ID, err)
			s.markContainerFailed(ctx, containerModel, "recovery_failed", err.Error())
			failReason[containerModel.ID] = newRecoveryLoss(containerModel, fmt.Sprintf("recovery_failed: %v", err))
		} else {
			recovered[containerModel.ID] = true
		}
	}

	// The summary NAMES each loss so an operator never has to guess which container it was.
	lost := s.collectAndAnnounceLostContainers(allContainers, recovered, exempt, failReason)

	fmt.Printf("Container recovery complete: %d recovered, %d lost%s, %d coordinator-managed skipped, %d dead-era skipped\n",
		len(recovered), len(lost), formatLostSummary(lost), coordinatorSkipCount, deadEraCount)
	return nil
}

func newRecoveryLoss(m *persistence.ContainerModel, reason string) recoveryLoss {
	return recoveryLoss{id: m.ID, commandType: m.CommandType, playerID: m.PlayerID, reason: reason}
}

// interruptWorkerContainer marks a coordinator-managed worker FAILED (by coordinator_id,
// parent, or type). It does NOT release ships — that would break SELL tasks holding cargo.
func (s *DaemonServer) interruptWorkerContainer(ctx context.Context, containerModel *persistence.ContainerModel, config map[string]interface{}) bool {
	if coordinatorID, hasCoordinator := config["coordinator_id"].(string); hasCoordinator && coordinatorID != "" {
		fmt.Printf("Container %s: Skipping recovery (worker container managed by coordinator %s)\n", containerModel.ID, coordinatorID)
		s.markWorkerInterrupted(ctx, containerModel, coordinatorID)
		return true
	}
	if containerModel.ParentContainerID != nil && *containerModel.ParentContainerID != "" {
		fmt.Printf("Container %s: Skipping recovery (worker container managed by parent %s)\n", containerModel.ID, *containerModel.ParentContainerID)
		s.markWorkerInterrupted(ctx, containerModel, *containerModel.ParentContainerID)
		return true
	}
	if spec, hasSpec := s.containerSpecs[containerModel.CommandType]; hasSpec && spec.IsWorker {
		fmt.Printf("Container %s: Skipping recovery (worker container type '%s' managed by coordinator)\n", containerModel.ID, containerModel.CommandType)
		s.markWorkerInterrupted(ctx, containerModel, "")
		return true
	}
	return false
}

// recoverStorageOperations re-seeds the in-memory StorageCoordinator's per-good
// cargo availability from live ship state after a daemon restart (sp-o477).
//
// The coordinator is populated only by live deposits, so on restart it starts
// EMPTY: standing warehouse/manufacturing stock becomes invisible to
// GetTotalCargoAvailable, which blinds contract inventory-first sourcing into
// market-buying goods that are physically present in the warehouse. The injected
// StorageRecoveryService reloads each RUNNING storage operation and reconstructs
// its ships' cargo from the Ship API, re-registering them with the SAME shared
// coordinator the contract StorageInventoryFinder reads.
//
// Called on every boot AFTER RecoverRunningContainers so the operations exist.
// RULINGS #1/#2: idempotent (the service skips an already-registered ship, so a
// concurrent warehouse-worker registration never double-counts) and fail-open (a
// per-player DB error is logged and skipped; per-ship API errors are handled
// inside the service — neither aborts boot nor empties good state). RULING #3:
// read-only re-registration, no ship state is mutated.
func (s *DaemonServer) recoverStorageOperations(ctx context.Context) {
	if s.storageRecovery == nil || s.playerRepo == nil {
		return
	}
	players, err := s.playerRepo.ListAll(ctx)
	if err != nil {
		fmt.Printf("Warning: storage recovery skipped — failed to list players: %v\n", err)
		return
	}
	for _, p := range players {
		if _, err := s.storageRecovery.RecoverStorageOperations(ctx, p.ID.Value(), p.Token); err != nil {
			// Fail-open (RULINGS #1/#2): one player's recovery failure never aborts
			// boot or the remaining players' recovery.
			fmt.Printf("Warning: storage recovery failed for player %s: %v\n", p.AgentSymbol, err)
		}
	}
}

// recoveryLoss identifies a container that was expected to be RUNNING after boot
// recovery but is not — carrying the id, type, and why for the loud captain event
// and the named summary line.
type recoveryLoss struct {
	id          string
	commandType string
	playerID    int
	reason      string
}

// collectAndAnnounceLostContainers diffs the loaded candidate set against what
// ended running (or was a by-design skip) and, for every container that fell
// out, records an interrupt-class captain.EventContainerLost (which wakes the
// captain via the watchkeeper) and logs a loud, greppable line naming it. This
// is the guarantee: a container that terminalizes unseen is impossible —
// whatever the cause (recovery error, or a candidate that fell through every
// branch uncategorized), the hull announces itself. Explicitly-failed candidates
// carry their captured reason; an uncategorized one is reported as an unexpected
// terminal (a guard against a future refactor adding a silent `continue`).
func (s *DaemonServer) collectAndAnnounceLostContainers(
	candidates []*persistence.ContainerModel,
	recovered, exempt map[string]bool,
	failReason map[string]recoveryLoss,
) []recoveryLoss {
	var lost []recoveryLoss
	for _, cm := range candidates {
		if recovered[cm.ID] || exempt[cm.ID] {
			continue
		}
		loss, ok := failReason[cm.ID]
		if !ok {
			loss = recoveryLoss{
				id: cm.ID, commandType: cm.CommandType, playerID: cm.PlayerID,
				reason: "unexpected_terminal: recovery pass did not account for this container",
			}
		}
		lost = append(lost, loss)
	}
	for _, loss := range lost {
		fmt.Printf("CONTAINER LOST at recovery: %s [%s] — %s\n", loss.id, loss.commandType, loss.reason)
		recordCaptainEvent(captain.EventContainerLost, loss.id, loss.playerID, map[string]any{
			"container_id":   loss.id,
			"container_type": loss.commandType,
			"reason":         loss.reason,
		})
	}
	return lost
}

// formatLostSummary renders the named-failure detail appended to the recovery
// summary line, so "N lost" is never anonymous. Empty when nothing
// was lost.
func formatLostSummary(lost []recoveryLoss) string {
	if len(lost) == 0 {
		return ""
	}
	parts := make([]string, 0, len(lost))
	for _, loss := range lost {
		parts = append(parts, fmt.Sprintf("%s [%s]: %s", loss.id, loss.commandType, loss.reason))
	}
	return " (" + strings.Join(parts, "; ") + ")"
}

// markContainerDeadEra marks a container FAILED because it belongs to a player whose
// era is closed / the universe was reset. It is NOT re-instantiated: reviving
// it would burn API calls against a dead token on a reset map. Ship assignments are
// left untouched — they belong to the reset universe, and the startup zombie-release
// path (ReleaseAllActive) deliberately scopes only to the open-era player.
func (s *DaemonServer) markContainerDeadEra(ctx context.Context, containerModel *persistence.ContainerModel, openEra *persistence.EraModel) {
	ctx, cancel := recoveryBookkeepingContext(ctx)
	defer cancel()

	livePlayer := "none (no open era)"
	if openEra != nil {
		livePlayer = fmt.Sprintf("player %d, era %q", openEra.PlayerID, openEra.Name)
	}
	detail := fmt.Sprintf("dead_era: container player %d is not the open-era player (%s); universe reset — not recovered",
		containerModel.PlayerID, livePlayer)
	fmt.Printf("Container %s: Skipping recovery (%s)\n", containerModel.ID, detail)

	exitCode := 1
	now := time.Now()
	if err := s.containerRepo.UpdateStatus(
		ctx,
		containerModel.ID,
		containerModel.PlayerID,
		container.ContainerStatusFailed,
		&now,      // stoppedAt
		&exitCode, // exitCode
		detail,
	); err != nil {
		fmt.Printf("Warning: Failed to mark container %s as dead-era: %v\n", containerModel.ID, err)
	}
}

// recoverContainer is the generic container recovery function
// Uses the command factory registry to recreate any container type
// Adding new container types only requires registering a new factory - NO changes needed here!
func (s *DaemonServer) recoverContainer(ctx context.Context, containerModel *persistence.ContainerModel, config map[string]interface{}) error {
	cmd, err := s.buildCommandForType(containerModel.CommandType, config, containerModel.PlayerID, containerModel.ID)
	if err != nil {
		return fmt.Errorf("failed to create command: %w", err)
	}

	// Extract ship symbol for assignment (if present)
	shipSymbol, hasShip := config["ship_symbol"].(string)
	if hasShip {
		// Re-assign ship using Ship aggregate pattern
		playerID := shared.MustNewPlayerID(containerModel.PlayerID)
		// Re-assign on the FRESH row: a plain Save that loses the version race has
		// its ownership columns taken from the row, so a claim persisted that way
		// would silently not stick. Re-claiming a container's own hull is a no-op.
		if _, _, err := s.shipRepo.SaveWithRetry(ctx, shipSymbol, playerID,
			func(sh *navigation.Ship) (bool, error) {
				if sh.IsAssigned() && sh.ContainerID() == containerModel.ID {
					return false, nil
				}
				if err := sh.AssignToContainer(containerModel.ID, s.clock); err != nil {
					return false, err
				}
				return true, nil
			}); err != nil {
			return fmt.Errorf("failed to reassign ship %s: %w", shipSymbol, err)
		}
	}

	// Extract iterations from config. Runner-loop types persist their budget
	// under "iterations", except goods_factory_coordinator which persists it
	// under "max_iterations" (see StartGoodsFactory) — check both so a recovered
	// factory resumes with its actual budget instead of silently collapsing to
	// the single-iteration default.
	//
	// COORDINATOR-OWNED types are pinned to 1 regardless of config (sp-7yej
	// invariant 3): their command's handler owns the whole run internally
	// (scout_tour's tour count, trade_route's visit budget), so feeding the
	// config budget to the CONTAINER as well would double-loop it on recovery —
	// a rebuilt scout_tour with iterations=3 would fly 3 runner iterations × 3
	// tours each. The config value still reaches the command through the
	// factory builder, which is the loop that actually owns it.
	iterations := 1 // Default
	if spec, ok := s.containerSpecs[containerModel.CommandType]; ok && spec.CoordinatorOwnsIterations {
		// pinned: one runner iteration wraps the whole coordinator-owned run
	} else if iter, ok := config["iterations"].(float64); ok {
		iterations = int(iter)
	} else if iter, ok := config["max_iterations"].(float64); ok {
		iterations = int(iter)
	}

	// Recreate container entity
	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		iterations,
		containerModel.ParentContainerID, // Restore parent-child relationship
		config,
		nil, // Use default RealClock for production
	)

	// Restore restart count from database
	for i := 0; i < containerModel.RestartCount; i++ {
		containerEntity.IncrementRestartCount()
	}

	s.startContainerRunner(containerEntity, cmd, containerModel.ID, "Recovered container")

	shipInfo := ""
	if hasShip {
		shipInfo = fmt.Sprintf(" for ship %s", shipSymbol)
	}
	fmt.Printf("Recovered container %s (%s%s)\n", containerModel.ID, containerModel.CommandType, shipInfo)
	return nil
}

// recoveryBookkeepingContext detaches a recovery terminalization from the recovery
// pass's own budget. The pass runs under a bounded context; when that budget expires
// mid-pass the adoption fails AND — on the shared context — so does every write that
// marks the container FAILED and releases its hull, leaving a RUNNING row with a
// claimed hull and no runner behind it. Bookkeeping about a container that is already
// not running must outlive the budget that stopped it, or recovery resurrects a corpse
// that `container list` reports as healthy indefinitely.
func recoveryBookkeepingContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), dbOperationTimeout)
}

// markContainerFailed marks a container as FAILED in the database
func (s *DaemonServer) markContainerFailed(ctx context.Context, containerModel *persistence.ContainerModel, reason string, details string) {
	ctx, cancel := recoveryBookkeepingContext(ctx)
	defer cancel()

	exitCode := 1
	now := time.Now()

	if err := s.containerRepo.UpdateStatus(
		ctx,
		containerModel.ID,
		containerModel.PlayerID,
		container.ContainerStatusFailed,
		&now,      // stoppedAt
		&exitCode, // exitCode
		fmt.Sprintf("%s: %s", reason, details),
	); err != nil {
		fmt.Printf("Warning: Failed to mark container %s as FAILED: %v\n", containerModel.ID, err)
	}

	// Releasing here prevents orphaned assignments when containers fail during recovery.
	playerID := shared.MustNewPlayerID(containerModel.PlayerID)
	assignedShips, err := s.shipRepo.FindByContainer(ctx, containerModel.ID, playerID)
	if err != nil {
		fmt.Printf("Warning: Failed to find ships for container %s: %v\n", containerModel.ID, err)
		return
	}
	for _, ship := range assignedShips {
		s.releaseShipFromFailedContainer(ctx, ship.ShipSymbol(), containerModel.ID, playerID, reason)
	}
}

// releaseShipFromFailedContainer re-applies ForceRelease on the FRESH row under CAS-retry, so
// a concurrent writer's update survives. A hull that already moved on keeps its new owner.
func (s *DaemonServer) releaseShipFromFailedContainer(ctx context.Context, shipSymbol, containerID string, playerID shared.PlayerID, reason string) {
	if _, _, err := s.shipRepo.SaveWithRetry(ctx, shipSymbol, playerID,
		func(sh *navigation.Ship) (bool, error) {
			if !sh.IsAssigned() || sh.ContainerID() != containerID {
				return false, nil
			}
			sh.ForceRelease(reason, s.clock)
			return true, nil
		}); err != nil {
		fmt.Printf("Warning: Failed to release ship %s for container %s: %v\n", shipSymbol, containerID, err)
	}
}

// markWorkerInterrupted marks a worker container as interrupted during daemon restart.
// Unlike markContainerFailed, this does NOT release ship assignments.
// The coordinator's recoverState() will handle the ship assignments when it resets tasks.
// This is critical for SELL tasks where the ship still has cargo that needs to be sold.
func (s *DaemonServer) markWorkerInterrupted(ctx context.Context, containerModel *persistence.ContainerModel, coordinatorID string) {
	ctx, cancel := recoveryBookkeepingContext(ctx)
	defer cancel()

	exitCode := 1
	now := time.Now()

	if err := s.containerRepo.UpdateStatus(
		ctx,
		containerModel.ID,
		containerModel.PlayerID,
		container.ContainerStatusFailed,
		&now,      // stoppedAt
		&exitCode, // exitCode
		fmt.Sprintf("worker_interrupted: Worker interrupted by daemon restart (coordinator: %s). Ship assignments preserved for task recovery.", coordinatorID),
	); err != nil {
		fmt.Printf("Warning: Failed to mark worker %s as interrupted: %v\n", containerModel.ID, err)
	}
	// NOTE: Intentionally NOT releasing ship assignments here.
	// The coordinator will handle this when it resets the task from EXECUTING to READY.
}
