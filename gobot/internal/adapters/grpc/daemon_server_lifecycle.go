package grpc

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
	"google.golang.org/grpc"
)

// Start begins serving gRPC requests
func (s *DaemonServer) Start() error {
	fmt.Printf("Daemon server listening on unix socket: %s\n", s.listener.Addr().String())

	// The captain recorder was installed by main before Start; nil just means no events.
	s.runCtx, s.runCancel = context.WithCancel(context.Background())
	bootCtx, bootCancel := context.WithTimeout(s.runCtx, 10*time.Second)
	s.sup = supervise.New(
		currentCaptainEventRecorder(),
		s.primaryPlayerID(bootCtx),
		s.clock,
		supervise.WithOnRestart(metrics.RecordDaemonComponentRestart),
	)
	bootCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	s.releaseZombieAssignments(ctx)

	// Reset orphaned ASSIGNED manufacturing tasks to READY
	// This fixes tasks stuck in ASSIGNED state from failed worker container creation
	if err := s.resetOrphanedManufacturingTasks(); err != nil {
		fmt.Printf("Warning: Failed to reset orphaned manufacturing tasks: %v\n", err)
	}

	// Sync all ships from API to database (database becomes source of truth after this)
	if err := s.syncAllShipsOnStartup(); err != nil {
		fmt.Printf("Warning: Ship startup sync failed: %v\n", err)
		// Continue - we can still operate with stale data
	}

	// Schedule timers for pending arrivals and cooldowns
	if s.shipStateScheduler != nil {
		scheduleCtx, scheduleCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer scheduleCancel()
		if err := s.shipStateScheduler.ScheduleAllPending(scheduleCtx); err != nil {
			fmt.Printf("Warning: Failed to schedule pending state transitions: %v\n", err)
		}
		// Start background sweeper under supervision: restarts
		// with backoff on crash, escalates a crash loop to the captain.
		s.sup.Go(s.runCtx, "ship-state-sweeper", s.shipStateScheduler.RunSweeper)
	}

	// Keeps DB ship state from drifting vs the live API between the event-driven updates.
	if s.shipResyncScheduler != nil {
		s.sup.Go(s.runCtx, "ship-resync", s.shipResyncScheduler.Run)
	}

	// Bounds the containers table. Sweeps once at start, then daily.
	if s.containerRetentionScheduler != nil {
		s.sup.Go(s.runCtx, "container-retention", s.containerRetentionScheduler.Run)
	}

	// Bounds container_logs, the highest-volume table in the database. Sweeps once at start,
	// then daily, in bounded batches. Nil only when the operator disabled it outright.
	if s.containerLogRetentionScheduler != nil {
		s.sup.Go(s.runCtx, "container-log-retention", s.containerLogRetentionScheduler.Run)
	}

	// Unconditional, like the ship state scheduler — not gated behind metricsConfig.Enabled.
	if s.dutyCycleSampler != nil {
		s.dutyCycleSampler.Start()
	}

	// A bind failure is FATAL: a taken port means another daemon already holds it, and
	// running headless beside a stale writer is worse than not starting.
	if err := s.startMetricsServerOrFail(); err != nil {
		return err
	}
	s.startPollingCollectors()

	// Guard, not sup.Go: recovery re-adopts containers and is NOT safely re-runnable —
	// a restart could double-adopt. One attempt, loudly logged, panic-isolated.
	go supervise.Guard("container-recovery", s.recoverPreviousInstance)

	go s.handleShutdown()

	grpcServer := grpc.NewServer()
	pb.RegisterDaemonServiceServer(grpcServer, newDaemonServiceImpl(s))
	return s.serveUntilShutdown(grpcServer)
}

func (s *DaemonServer) releaseZombieAssignments(ctx context.Context) {
	if s.shipRepo == nil {
		return
	}
	openEra, err := persistence.NewEraRepository(s.db).FindOpenEra(ctx)
	if err != nil {
		fmt.Printf("Warning: Failed to resolve open era for zombie assignment release: %v\n", err)
		return
	}
	if openEra == nil {
		fmt.Println("No open era - skipping zombie assignment release")
		return
	}
	count, err := s.shipRepo.ReleaseAllActive(ctx, shared.MustNewPlayerID(openEra.PlayerID), "daemon_restart")
	if err != nil {
		fmt.Printf("Warning: Failed to release zombie assignments: %v\n", err)
	} else if count > 0 {
		fmt.Printf("Released %d zombie ship assignment(s) on daemon startup\n", count)
	}
}

// startPollingCollectors starts the collectors that own a scrape goroutine. The
// event-driven ones need no start; they record through their global.
func (s *DaemonServer) startPollingCollectors() {
	if s.metricsConfig == nil || !s.metricsConfig.Enabled {
		return
	}
	if s.containerMetricsCollector != nil {
		s.containerMetricsCollector.Start(context.Background())
	}
	if s.financialMetricsCollector != nil {
		s.financialMetricsCollector.Start(context.Background())
	}
	if s.marketMetricsCollector != nil {
		s.marketMetricsCollector.Start(context.Background())
	}
	if s.manufacturingMetricsCollector != nil {
		s.manufacturingMetricsCollector.Start(context.Background())
	}
}

// recoverPreviousInstance re-adopts the previous daemon's containers, then re-seeds the
// StorageCoordinator — which MUST follow it, or contract market-buys already-stored goods.
func (s *DaemonServer) recoverPreviousInstance() {
	recoveryCtx, recoveryCancel := context.WithTimeout(s.runCtx, 30*time.Second)
	defer recoveryCancel()

	if err := s.RecoverRunningContainers(recoveryCtx); err != nil {
		fmt.Printf("Warning: Container recovery failed: %v\n", err)
	}

	s.recoverStorageOperations(recoveryCtx)
	s.launchBootStandingAfterRecovery()
}

func (s *DaemonServer) serveUntilShutdown(grpcServer *grpc.Server) error {
	errChan := make(chan error, 1)
	go func() {
		if err := grpcServer.Serve(s.listener); err != nil {
			errChan <- fmt.Errorf("gRPC server error: %w", err)
		}
	}()

	select {
	case err := <-errChan:
		return err
	case <-s.done:
		fmt.Println("Initiating graceful shutdown of gRPC server...")
		grpcServer.GracefulStop()
		return nil
	}
}

// handleShutdown manages graceful shutdown
// GracefulShutdownTimeout is the maximum time to wait for containers to finish
const GracefulShutdownTimeout = 30 * time.Second

func (s *DaemonServer) handleShutdown() {
	<-s.shutdownChan
	fmt.Println("\nShutdown signal received, initiating graceful shutdown...")

	// Cancel supervised components first so the sweeper stops
	// scheduling new writes while containers drain.
	if s.runCancel != nil {
		s.runCancel()
	}

	// Stop ship state scheduler (cancels timers and stops background sweeper)
	if s.shipStateScheduler != nil {
		s.shipStateScheduler.Stop()
	}

	if s.dutyCycleSampler != nil {
		s.dutyCycleSampler.Stop()
	}

	// BUG FIX #5: Graceful shutdown with timeout
	// Give containers time to complete their current operation before force-interrupting
	s.gracefulShutdownWithTimeout(GracefulShutdownTimeout)

	s.stopMetricsServer()

	if s.listener != nil {
		s.listener.Close()
	}

	// Join supervised components — they exit promptly on runCtx cancel.
	if s.sup != nil {
		s.sup.Wait()
	}

	close(s.done)
}

// primaryPlayerID resolves the player that daemon-scoped captain events are
// attributed to: the open era's player (the same identity the zombie-release
// block at Start uses), falling back to the first player row, else 0.
func (s *DaemonServer) primaryPlayerID(ctx context.Context) int {
	if s.db != nil {
		eraRepo := persistence.NewEraRepository(s.db)
		if openEra, err := eraRepo.FindOpenEra(ctx); err == nil && openEra != nil {
			return openEra.PlayerID
		}
	}
	if s.playerRepo != nil {
		if players, err := s.playerRepo.ListAll(ctx); err == nil && len(players) > 0 {
			return players[0].ID.Value()
		}
	}
	return 0
}

// syncAllShipsOnStartup syncs the live (open-era) player's ships from the API
// into the database at daemon boot. After this sync, the database becomes the
// source of truth for ship state. Thin wrapper over the shared syncAllShips
// core, which the periodic ShipResyncScheduler also drives.
func (s *DaemonServer) syncAllShipsOnStartup() error {
	return s.syncAllShips(context.Background())
}

// syncAllShips re-syncs the live (open-era) player's ships from the API into
// the DB. It is the shared core called at startup AND on every periodic resync
// tick. The write path (SyncAllFromAPI) preserves the daemon-owned
// dedicated_fleet tag per ship, so a repeated hourly resync
// cannot clobber a `fleet assign` pin. The parent ctx bounds the sync (canceled
// at shutdown) under a 60s timeout.
//
// Sync ONLY s.primaryPlayerID — the open era's player — NOT every
// player row. A universe reset leaves dead prior-era rows behind (empty or
// reset-date-mismatched tokens); a playerRepo.ListAll loop syncs them
// too, and because every player shares this ONE 60s deadline, each dead row's
// 401 burns the budget so the live player's ships never land fresh —
// fleet-wide synced_at froze 12h+. primaryPlayerID is the canonical open-era
// resolver every other boot-scoped path already uses (ensureBootStandingCoordinators,
// depot registry). On a normal single-player era it resolves the only player, so
// the observable outcome is unchanged — this only STOPS syncing dead rows.
func (s *DaemonServer) syncAllShips(parent context.Context) error {
	if s.shipRepo == nil || s.playerRepo == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()

	pid := s.primaryPlayerID(ctx)
	if pid == 0 {
		fmt.Println("No open-era player found - skipping ship resync")
		return nil
	}

	playerID, err := shared.NewPlayerID(pid)
	if err != nil {
		return fmt.Errorf("resolve primary player id %d: %w", pid, err)
	}

	// Agent symbol for the log line only; the expensive API round-trip is
	// SyncAllFromAPI below, not this single-row lookup. Fall back to the numeric
	// id if the row can't be read — the sync still runs.
	agentLabel := fmt.Sprintf("player_id=%d", pid)
	if p, err := s.playerRepo.FindByID(ctx, playerID); err == nil && p != nil {
		agentLabel = p.AgentSymbol
	}

	count, err := s.shipRepo.SyncAllFromAPI(ctx, playerID)
	if err != nil {
		return fmt.Errorf("failed to sync ships for player %s: %w", agentLabel, err)
	}

	fmt.Printf("Synced %d ship(s) for player %s\n", count, agentLabel)
	fmt.Printf("Ship sync complete: %d total ship(s) synced across 1 player(s)\n", count)
	return nil
}

// resetOrphanedManufacturingTasks resets ASSIGNED manufacturing tasks on daemon startup.
// A task can get stuck in ASSIGNED status because:
// 1. AssignTaskAtomically succeeds (task.assigned_ship is set)
// 2. But PersistManufacturingTaskWorkerContainer or shipRepo.Save fails
// 3. Rollback errors are ignored, leaving task ASSIGNED with no worker container
//
// This cleanup runs on daemon startup to reset any such orphaned tasks.
func (s *DaemonServer) resetOrphanedManufacturingTasks() error {
	if s.db == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Reset all ASSIGNED tasks to READY and clear their assigned_ship
	// This allows them to be picked up by a manufacturing coordinator when it starts
	result := s.db.WithContext(ctx).
		Table("manufacturing_tasks").
		Where("status = ?", "ASSIGNED").
		Updates(map[string]interface{}{
			"status":        "READY",
			"assigned_ship": nil,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to reset orphaned manufacturing tasks: %w", result.Error)
	}

	if result.RowsAffected > 0 {
		fmt.Printf("Reset %d orphaned ASSIGNED manufacturing task(s) to READY on daemon startup\n", result.RowsAffected)
	}

	return nil
}

// gracefulShutdownWithTimeout waits for containers to complete or times out
// BUG FIX #5: This prevents context cancellation cascades that corrupt state
func (s *DaemonServer) gracefulShutdownWithTimeout(timeout time.Duration) {
	s.containersMu.RLock()
	containerCount := len(s.containers)
	s.containersMu.RUnlock()

	if containerCount == 0 {
		fmt.Println("No running containers to stop")
		return
	}

	fmt.Printf("Waiting up to %s for %d container(s) to complete current operations...\n",
		timeout, containerCount)

	// Create a done channel to track when containers finish
	allDone := make(chan struct{})

	go func() {
		// Wait for all containers to finish their done channels
		s.containersMu.RLock()
		runners := make([]*ContainerRunner, 0, len(s.containers))
		for _, runner := range s.containers {
			runners = append(runners, runner)
		}
		s.containersMu.RUnlock()

		// Signal each container to stop (sets stopping flag, doesn't cancel context yet)
		for _, runner := range runners {
			// Try graceful stop first - this sets the stopping flag
			runner.mu.Lock()
			_ = runner.containerEntity.Stop()
			runner.mu.Unlock()
		}

		// Wait for each container's done channel
		for _, runner := range runners {
			select {
			case <-runner.done:
				// Container finished gracefully
			case <-time.After(timeout):
				// This container took too long - will be force-interrupted
			}
		}
		close(allDone)
	}()

	// Wait for graceful completion or timeout
	select {
	case <-allDone:
		fmt.Println("All containers completed gracefully")
	case <-time.After(timeout):
		fmt.Printf("Graceful shutdown timeout (%s) exceeded, force-interrupting remaining containers...\n", timeout)
		// Force-interrupt any remaining containers
		s.interruptAllContainers()
	}
}
