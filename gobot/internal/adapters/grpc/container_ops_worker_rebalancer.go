package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// WorkerRebalancerCoordinator starts the standing worker-rebalancer coordinator (sp-f5pr):
// a recovery-safe container that ferries idle undedicated light-haulers cross-system to
// worker-starved factory systems. It mirrors FrontierExpansionCoordinator's shape (build
// config → buildCommandForType so creation and recovery share one builder → NewContainer
// with iterations=-1 for the infinite reconcile loop → Add → runner → registerContainer →
// go Start). All the tuning (enabled/vacancy-clock/source-floor/cooldown/caps) is resolved
// LIVE from config.yaml's [worker_rebalancer] section inside buildCommandForType
// (resolveWorkerRebalancerConfig), so the launch config carries only the identity + the
// dry_run launch flag here; a config edit + restart retunes even a recovered coordinator
// (sp-ts82 live-config pattern). dryRun logs decisions without ferrying anything.
func (s *DaemonServer) WorkerRebalancerCoordinator(ctx context.Context, playerID int, agentSymbol string, dryRun bool) (string, error) {
	containerID := utils.GenerateContainerID("worker_rebalancer_coordinator", fmt.Sprintf("player-%d", playerID))

	// Identity + the dry_run launch flag only — the [worker_rebalancer] knobs are injected
	// by resolveWorkerRebalancerConfig inside buildCommandForType (below), the single
	// injection point shared by creation and restart recovery. dry_run is a launch-time
	// decision (like the frontier coordinator's), NOT a config.yaml knob, so it survives a
	// rebuild untouched.
	config := map[string]interface{}{
		"container_id": containerID,
		"agent_symbol": agentSymbol,
		"dry_run":      dryRun,
	}

	cmd, err := s.buildCommandForType("worker_rebalancer_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create worker rebalancer coordinator command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeWorkerRebalancerCoordinator,
		playerID,
		-1,  // Infinite iterations (reconcile loop) — NOT a CoordinatorOwnsIterations type
		nil, // No parent container
		config,
		nil,
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "worker_rebalancer_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist worker rebalancer coordinator container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Worker rebalancer coordinator container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// workerRebalancerConfigKeys enumerates every launch-config key the [worker_rebalancer]
// knobs occupy. resolveWorkerRebalancerConfig clears these before re-injecting the live
// values, so a stale persisted copy from a prior boot can never shadow the current
// config.yaml (the sp-ts82 live-config discipline). container_id, agent_symbol, and dry_run
// are the coordinator's IDENTITY/launch-flag (set once at creation) and are deliberately
// NOT in this list — they must survive a rebuild untouched.
var workerRebalancerConfigKeys = []string{
	"worker_rebalancer_disabled",
	"worker_rebalancer_tick_secs",
	"worker_rebalancer_vacancy_min_minutes",
	"worker_rebalancer_source_min_idle",
	"worker_rebalancer_ferry_cooldown_secs",
	"worker_rebalancer_max_concurrent_ferries",
	"worker_rebalancer_max_lights_per_system",
}

// resolveWorkerRebalancerConfig makes config.yaml the single LIVE source of truth for the
// worker-rebalancer coordinator's knobs (sp-f5pr, mirroring resolveTradeFleetConfig). It
// clears any worker_rebalancer_* keys already in the launch config (stale copies persisted
// at a prior boot) and re-injects the daemon's boot-loaded values, so the rebuilt command
// reflects the CURRENT config.yaml on every build — creation and restart recovery alike.
func (s *DaemonServer) resolveWorkerRebalancerConfig(config map[string]interface{}) {
	for _, key := range workerRebalancerConfigKeys {
		delete(config, key)
	}
	s.injectWorkerRebalancerConfig(config)
}

// injectWorkerRebalancerConfig writes the [worker_rebalancer] knobs from config.yaml
// (s.workerRebalancerConfig) into a coordinator container's launch config. Only keys the
// captain actually set (non-zero) are written, so an unset knob defers to the
// coordinator's own documented default (RULINGS #5). Enabled is inverted to
// worker_rebalancer_disabled, written ONLY when the coordinator is off: an absent key
// therefore reads as enabled, so the default-ON intent survives both a fresh start and a
// recovery from an old config that predates the key.
func (s *DaemonServer) injectWorkerRebalancerConfig(config map[string]interface{}) {
	wr := s.workerRebalancerConfig
	if !wr.EnabledOrDefault() {
		config["worker_rebalancer_disabled"] = true
	}
	if wr.TickSeconds != 0 {
		config["worker_rebalancer_tick_secs"] = wr.TickSeconds
	}
	if wr.VacancyMinMinutes != 0 {
		config["worker_rebalancer_vacancy_min_minutes"] = wr.VacancyMinMinutes
	}
	if wr.SourceMinIdle != 0 {
		config["worker_rebalancer_source_min_idle"] = wr.SourceMinIdle
	}
	if wr.FerryCooldownSeconds != 0 {
		config["worker_rebalancer_ferry_cooldown_secs"] = wr.FerryCooldownSeconds
	}
	if wr.MaxConcurrentFerries != 0 {
		config["worker_rebalancer_max_concurrent_ferries"] = wr.MaxConcurrentFerries
	}
	if wr.MaxLightsPerSystem != 0 {
		config["worker_rebalancer_max_lights_per_system"] = wr.MaxLightsPerSystem
	}
}

// PersistWorkerFerryWorker persists (but does NOT start) a worker_ferry container the
// worker_rebalancer_coordinator manages (sp-f5pr). Like a scout_reposition worker it
// carries a coordinator_id and a parent link, so daemon restart recovery SKIPS it (marks
// it worker_interrupted, preserving the ship claim) and leaves the reclaim/re-evaluation
// to the coordinator's reconcile pass. It wraps exactly ONE iteration — the whole ferry —
// and the coordinator owns re-dispatch (CoordinatorOwnsIterations in the registry). Twin of
// PersistScoutRepositionWorker.
func (s *DaemonServer) PersistWorkerFerryWorker(
	ctx context.Context,
	containerID string,
	shipSymbol string,
	destinationWaypoint string,
	playerID int,
	coordinatorID string,
) error {
	config := map[string]interface{}{
		"ship_symbol":    shipSymbol,
		"destination":    destinationWaypoint,
		"coordinator_id": coordinatorID,
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeWorkerFerry,
		playerID,
		1, // one iteration = the whole ferry; the coordinator owns re-dispatch
		&coordinatorID,
		config,
		nil,
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "worker_ferry"); err != nil {
		return fmt.Errorf("failed to persist worker ferry worker: %w", err)
	}
	return nil
}

// StartWorkerFerry starts a previously persisted worker_ferry container (the
// coordinator-managed ferry path). Mirrors StartScoutReposition: load the persisted model,
// rebuild the command from its config, and run it.
func (s *DaemonServer) StartWorkerFerry(ctx context.Context, containerID string) error {
	allContainers, err := s.containerRepo.ListAll(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to list containers: %w", err)
	}

	var containerModel *persistence.ContainerModel
	for _, c := range allContainers {
		if c.ID == containerID {
			containerModel = c
			break
		}
	}
	if containerModel == nil {
		return fmt.Errorf("container %s not found", containerID)
	}

	var config map[string]interface{}
	if err := json.Unmarshal([]byte(containerModel.Config), &config); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	cmd, err := s.buildCommandForType("worker_ferry", config, containerModel.PlayerID, containerModel.ID)
	if err != nil {
		return fmt.Errorf("failed to create command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerModel.ID,
		container.ContainerType(containerModel.ContainerType),
		containerModel.PlayerID,
		1, // one iteration = the whole ferry
		containerModel.ParentContainerID,
		config,
		nil,
	)

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Container %s failed: %v\n", containerID, err)
		}
	}()

	return nil
}

// factoryContainerCommandTypes are the CommandTypes whose RUNNING rows signal active
// factory work (sp-f5pr). Both persist system_symbol in their config JSON. Kept here (an
// infrastructure concern — which DB rows are "factory containers") rather than in the
// application layer.
var factoryContainerCommandTypes = map[string]bool{
	"goods_factory_coordinator": true,
	"manufacturing_coordinator": true,
}

// workerRebalancerContainerQuery adapts the GORM container repository to the
// worker-rebalancer coordinator's RebalancerContainerQuery port (sp-f5pr): it reads
// RUNNING factory containers (with their system + StartedAt parsed from config JSON) and
// the recent + RUNNING worker_ferry rows. It keeps the GORM model out of the application
// layer by projecting to the coordinator's small DTOs.
type workerRebalancerContainerQuery struct {
	containerRepo *persistence.ContainerRepositoryGORM
}

// NewWorkerRebalancerContainerQuery wires the adapter over the container repo and returns
// it as the coordinator's RebalancerContainerQuery port (sp-f5pr) — the daemon binary
// constructs it and injects it into the coordinator handler.
func NewWorkerRebalancerContainerQuery(repo *persistence.ContainerRepositoryGORM) tradingCmd.RebalancerContainerQuery {
	return &workerRebalancerContainerQuery{containerRepo: repo}
}

// ActiveFactoryContainers returns every RUNNING factory container for the player, with its
// system and StartedAt (sp-f5pr). A row with an unparseable system or a nil StartedAt is
// dropped (fail-closed: a container the coordinator cannot place or time never drives a
// vacancy).
func (q *workerRebalancerContainerQuery) ActiveFactoryContainers(ctx context.Context, playerID int) ([]tradingCmd.ActiveFactoryContainer, error) {
	models, err := q.containerRepo.ListByStatus(ctx, container.ContainerStatusRunning, &playerID)
	if err != nil {
		return nil, err
	}
	var out []tradingCmd.ActiveFactoryContainer
	for _, m := range models {
		if !factoryContainerCommandTypes[m.CommandType] {
			continue
		}
		system := containerConfigString(m.Config, "system_symbol")
		if system == "" || m.StartedAt == nil {
			continue
		}
		out = append(out, tradingCmd.ActiveFactoryContainer{
			SystemSymbol: system,
			StartedAt:    *m.StartedAt,
		})
	}
	return out, nil
}

// RecentFerries returns the player's worker_ferry containers that started at/after `since`
// UNION all RUNNING worker_ferry containers regardless of age (sp-f5pr). The RUNNING-any-age
// inclusion is what makes the coordinator's concurrency cap and reclaim RUNNING-set exact
// even for a ferry that has been airborne longer than the cooldown window — so a long-lived
// relay is never miscounted or wrongly reclaimed. A nil StartedAt reads as the zero time
// (a RUNNING ferry with no start still counts against the cap via its RUNNING status).
func (q *workerRebalancerContainerQuery) RecentFerries(ctx context.Context, playerID int, since time.Time) ([]tradingCmd.FerryContainer, error) {
	recent, err := q.containerRepo.ListByCommandTypeSince(ctx, "worker_ferry", since, playerID)
	if err != nil {
		return nil, err
	}
	running, err := q.containerRepo.ListByStatus(ctx, container.ContainerStatusRunning, &playerID)
	if err != nil {
		return nil, err
	}

	byID := make(map[string]*persistence.ContainerModel, len(recent)+len(running))
	for _, m := range recent {
		byID[m.ID] = m
	}
	for _, m := range running {
		if m.CommandType == "worker_ferry" {
			byID[m.ID] = m
		}
	}

	out := make([]tradingCmd.FerryContainer, 0, len(byID))
	for _, m := range byID {
		var started time.Time
		if m.StartedAt != nil {
			started = *m.StartedAt
		}
		out = append(out, tradingCmd.FerryContainer{
			ID:                  m.ID,
			DestinationWaypoint: containerConfigString(m.Config, "destination"),
			Status:              m.Status,
			StartedAt:           started,
		})
	}
	return out, nil
}

// containerConfigString extracts a top-level string field from a container's config JSON,
// returning "" when the config is absent, unparseable, or the key is missing/non-string.
func containerConfigString(configJSON, key string) string {
	if configJSON == "" {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(configJSON), &m); err != nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
