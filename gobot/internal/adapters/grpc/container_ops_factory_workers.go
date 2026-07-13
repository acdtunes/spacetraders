package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	goodsCmd "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/commands"
)

// The daemon side of the live per-op factory worker cap (sp-ev0n). A `goods factory
// workers` RPC sets the concurrent-hull cap in a RUNNING goods_factory container's
// config (the per-op `worker_cap` key); the running coordinator re-reads that same
// config every production pass (FactoryWorkerCapConfigProvider) and converges its
// fan-out to N on the next pass — no container restart. The daemon is the SOLE
// writer of the value (RULINGS #3).
//
// This mirrors sp-jcke's live standby-hub model exactly, including WHY no seed
// marker is needed: `worker_cap` is the authoritative live per-op value, read
// directly, and it is deliberately NOT among manufacturingConfigKeys (the keys
// resolveManufacturingConfig clears + reinjects from config.yaml on every build).
// So a plain daemon restart rebuilds the SAME container (buildGoodsFactoryCoordinatorCommand)
// from the mutated config, and the live cap survives verbatim (RULINGS #2) — there
// is no config.yaml re-injection to clobber it. The GLOBAL default
// (`factory_worker_cap_default`, from [manufacturing.siting] workers_per_chain) IS
// reinjected each build, but only fills in when no per-op override is set.

// factoryWorkerCapFromConfigMap reads the live per-op cap out of a container config
// map. ok is true only when the key is present AND positive — an absent key or a
// non-positive value means "no live override" (the coordinator keeps its launch cap).
func factoryWorkerCapFromConfigMap(config map[string]interface{}) (int, bool) {
	v, ok := intValue(config["worker_cap"])
	if !ok || v <= 0 {
		return 0, false
	}
	return v, true
}

// factoryWorkerCapFromConfig decodes the live per-op cap from a container's config
// JSON — the read side of the live provider. An empty/absent config yields (0,
// false) with no error; only malformed JSON errors.
func factoryWorkerCapFromConfig(configJSON string) (int, bool, error) {
	if configJSON == "" {
		return 0, false, nil
	}
	config := map[string]interface{}{}
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return 0, false, fmt.Errorf("parse container config for worker cap: %w", err)
	}
	cap, ok := factoryWorkerCapFromConfigMap(config)
	return cap, ok, nil
}

// mutateFactoryWorkerCapConfig writes count as the per-op cap in a container-config
// map IN PLACE and returns the resulting cap plus whether it changed. Pure over the
// map: MutateFactoryWorkerCap wraps it with the find→read→write plumbing.
// changed=false (setting the cap to its current value) lets the caller skip a
// redundant DB write and report the no-op honestly.
func mutateFactoryWorkerCapConfig(config map[string]interface{}, count int) (int, bool) {
	current, _ := intValue(config["worker_cap"]) // absent → 0
	config["worker_cap"] = count
	return count, count != current
}

// MutateFactoryWorkerCap sets the live concurrent-hull cap on the RUNNING
// goods_factory container containerID for playerID, and returns the resulting cap +
// whether it changed. It locates the container by ID, verifies it is a goods_factory
// coordinator, reads its config, applies the pure mutation, and (only when changed)
// writes just the config column back — the daemon as single writer (RULINGS #3), no
// container restart. The coordinator picks up the change on its next production pass
// via FactoryWorkerCapConfigProvider. count must be >= 1 (a cap is a positive hull
// count; raise it high to effectively uncap).
func (s *DaemonServer) MutateFactoryWorkerCap(ctx context.Context, containerID string, count, playerID int) (int, bool, error) {
	if count < 1 {
		return 0, false, fmt.Errorf("worker cap must be at least 1 (got %d) — raise it to widen the fan-out, not to zero", count)
	}

	model, err := s.containerRepo.Get(ctx, containerID, playerID)
	if err != nil {
		return 0, false, fmt.Errorf("failed to locate factory container %s: %w", containerID, err)
	}
	if model == nil {
		return 0, false, fmt.Errorf("no factory container %s for player %d — start a goods factory before setting its worker cap", containerID, playerID)
	}
	if model.ContainerType != standingFactoryContainerType {
		return 0, false, fmt.Errorf("container %s is a %q, not a goods factory — worker cap applies to goods_factory operations only", containerID, model.ContainerType)
	}

	config := map[string]interface{}{}
	if model.Config != "" {
		if err := json.Unmarshal([]byte(model.Config), &config); err != nil {
			return 0, false, fmt.Errorf("failed to parse factory container %s config: %w", containerID, err)
		}
	}

	result, changed := mutateFactoryWorkerCapConfig(config, count)
	if !changed {
		return result, false, nil // idempotent verb: no DB write needed
	}

	merged, err := json.Marshal(config)
	if err != nil {
		return 0, false, fmt.Errorf("failed to serialize factory container %s config after worker-cap mutation: %w", containerID, err)
	}
	if err := s.containerRepo.UpdateContainerConfig(ctx, model.ID, playerID, string(merged)); err != nil {
		return 0, false, fmt.Errorf("failed to persist worker-cap mutation to factory container %s config: %w", containerID, err)
	}

	return result, true, nil
}

// FactoryWorkerCapConfigProvider implements goodsCmd.FactoryWorkerCapProvider by
// reading a goods_factory container's OWN config `worker_cap` (sp-ev0n) — the store
// MutateFactoryWorkerCap writes. The coordinator resolves its live cap through this
// each production pass, so a `goods factory workers` change is honored with no
// restart. Mirrors StandbyStationConfigProvider's container-repo backing.
type FactoryWorkerCapConfigProvider struct {
	containerRepo *persistence.ContainerRepositoryGORM
}

// NewFactoryWorkerCapConfigProvider wires the container-config-backed live worker-cap
// reader for the goods_factory coordinator.
func NewFactoryWorkerCapConfigProvider(containerRepo *persistence.ContainerRepositoryGORM) *FactoryWorkerCapConfigProvider {
	return &FactoryWorkerCapConfigProvider{containerRepo: containerRepo}
}

var _ goodsCmd.FactoryWorkerCapProvider = (*FactoryWorkerCapConfigProvider)(nil)

// WorkerCap returns the container's current live per-op cap and whether a positive
// override is set. A missing row errors (so the coordinator falls back to its launch
// cap rather than silently unbounding on a transient read gap); an absent/non-positive
// key is a valid "no live override" state returned without error.
func (p *FactoryWorkerCapConfigProvider) WorkerCap(ctx context.Context, containerID string, playerID int) (int, bool, error) {
	model, err := p.containerRepo.Get(ctx, containerID, playerID)
	if err != nil {
		return 0, false, fmt.Errorf("read factory container %s for live worker cap: %w", containerID, err)
	}
	if model == nil {
		return 0, false, fmt.Errorf("factory container %s not found for live worker cap", containerID)
	}
	return factoryWorkerCapFromConfig(model.Config)
}
