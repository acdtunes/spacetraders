package grpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appContract "github.com/andrescamacho/spacetraders-go/internal/application/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// The daemon side of the operation-level live hub model (sp-jcke). A `fleet hub
// add|remove` RPC mutates the standby-station ("hub") set persisted in a RUNNING
// coordinator's container config, and the coordinator reads that same config live
// each discovery pass (StandbyStationConfigProvider). The daemon is the SOLE
// writer of the set (RULINGS #3); a live change survives a daemon restart because
// the restart rebuild (buildContractFleetCoordinatorCommand) reads the mutated
// config key — no launch-flag replay re-applies a stale value (RULINGS #2).
//
// WHY NO sp-86vb-style seed marker here: the dedicated-SHIPS seed needs a
// first-boot marker because the coordinator actively REPLAYS the launch
// --dedicated-ships list into a SEPARATE store (the per-ship dedicated_fleet tag)
// on every boot, so a live tag change must be shielded from that replay. Standby
// stations have no separate store and no replay step: the container-config
// `standby_stations` key IS the authoritative live set, read directly. A live
// mutation rewrites it in place, and a plain daemon restart rebuilds the SAME
// container (its ID carries a random suffix, so a `contract start` relaunch makes
// a different container, never clobbering this one) from the mutated config. There
// is thus no stale-seed resurrection vector to gate — the restart-resilience the
// marker would provide is already intrinsic, and is proven across a simulated
// restart in TestHubChange_SurvivesRestart_NotResurrectedBySeed.

// hubCapableCoordinatorTypes maps an operation to the coordinator container type
// that owns its standby-station set. Only "contract" has a hub set today; other
// operations gain live hubs by adding an entry here (RULINGS #5: a small config
// map, not a constant baked into the flow).
var hubCapableCoordinatorTypes = map[string]string{
	"contract": string(container.ContainerTypeContractFleetCoordinator),
}

// standbyStationsFromConfigMap reads the standby-station set out of a container
// config map, reusing stringSliceValue so a set carried as []string (fresh
// in-memory) or []interface{} (JSON-recovered) decodes identically. ok is false
// when the key is absent or wrong-typed (→ empty set, homing disabled).
func standbyStationsFromConfigMap(config map[string]interface{}) ([]string, bool) {
	return stringSliceValue(config["standby_stations"])
}

// standbyStationsFromConfig decodes the standby-station set from a container's
// config JSON — the read side of the live provider. An empty/absent config yields
// an empty set (homing disabled), never an error; only malformed JSON errors.
func standbyStationsFromConfig(configJSON string) ([]string, error) {
	if configJSON == "" {
		return nil, nil
	}
	config := map[string]interface{}{}
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		return nil, fmt.Errorf("parse container config for standby stations: %w", err)
	}
	stations, _ := standbyStationsFromConfigMap(config)
	return stations, nil
}

// mutateStandbyStationsConfig applies a hub add/remove to a container-config map
// IN PLACE and returns the resulting set plus whether it changed. Pure over the
// map: the daemon method wraps it with the find→read→write plumbing. changed=false
// (add of an existing hub, remove of an absent one) lets the caller skip a
// redundant DB write and report the no-op honestly.
func mutateStandbyStationsConfig(config map[string]interface{}, waypoint string, add bool) ([]string, bool) {
	current, _ := standbyStationsFromConfigMap(config)
	result, changed := appContract.ApplyStandbyStationChange(current, waypoint, add)
	config["standby_stations"] = result
	return result, changed
}

// MutateStandbyStation adds or removes a hub on the RUNNING coordinator of
// operation for playerID, live, and returns the resulting set + whether it
// changed. It locates the coordinator by type, reads its config, applies the pure
// mutation, and (only when changed) writes just the config column back — the
// daemon as single writer (RULINGS #3), no container restart. The coordinator
// picks up the change on its next discovery pass via StandbyStationConfigProvider.
func (s *DaemonServer) MutateStandbyStation(ctx context.Context, operation, waypoint string, add bool, playerID int) ([]string, bool, error) {
	coordType, ok := hubCapableCoordinatorTypes[operation]
	if !ok {
		return nil, false, fmt.Errorf("operation %q has no live-hub-capable coordinator (supported: contract)", operation)
	}

	model, err := s.containerRepo.FindActiveCoordinatorByType(ctx, coordType, playerID)
	if err != nil {
		return nil, false, fmt.Errorf("failed to locate the running %s coordinator: %w", operation, err)
	}
	if model == nil {
		return nil, false, fmt.Errorf("no running %s coordinator for player %d — start one before adding or removing hubs", operation, playerID)
	}

	config := map[string]interface{}{}
	if model.Config != "" {
		if err := json.Unmarshal([]byte(model.Config), &config); err != nil {
			return nil, false, fmt.Errorf("failed to parse %s coordinator config: %w", operation, err)
		}
	}

	result, changed := mutateStandbyStationsConfig(config, waypoint, add)
	if !changed {
		return result, false, nil // idempotent verb: no DB write needed
	}

	merged, err := json.Marshal(config)
	if err != nil {
		return nil, false, fmt.Errorf("failed to serialize %s coordinator config after hub mutation: %w", operation, err)
	}
	if err := s.containerRepo.UpdateContainerConfig(ctx, model.ID, playerID, string(merged)); err != nil {
		return nil, false, fmt.Errorf("failed to persist hub mutation to the %s coordinator config: %w", operation, err)
	}

	return result, true, nil
}

// StandbyStationConfigProvider implements appContract.StandbyStationProvider by
// reading a coordinator's OWN container config standby_stations (sp-jcke) — the
// store MutateStandbyStation writes. The coordinator resolves its live hub set
// through this each discovery pass, so a `fleet hub` change is honored with no
// restart. Mirrors the DedicatedFleetSeedConfigPersister's container-repo backing.
type StandbyStationConfigProvider struct {
	containerRepo *persistence.ContainerRepositoryGORM
}

// NewStandbyStationConfigProvider wires the container-config-backed live standby
// reader for the contract coordinator.
func NewStandbyStationConfigProvider(containerRepo *persistence.ContainerRepositoryGORM) *StandbyStationConfigProvider {
	return &StandbyStationConfigProvider{containerRepo: containerRepo}
}

var _ appContract.StandbyStationProvider = (*StandbyStationConfigProvider)(nil)

// StandbyStations returns the coordinator container's current standby-station set.
// A missing row errors (so the resolver falls back to the launch snapshot rather
// than silently disabling homing on a transient read gap); an empty/absent set is
// a valid "homing disabled" state returned without error.
func (p *StandbyStationConfigProvider) StandbyStations(ctx context.Context, containerID string, playerID int) ([]string, error) {
	model, err := p.containerRepo.Get(ctx, containerID, playerID)
	if err != nil {
		return nil, fmt.Errorf("read coordinator %s container for live standby stations: %w", containerID, err)
	}
	if model == nil {
		return nil, fmt.Errorf("coordinator container %s not found for live standby stations", containerID)
	}
	return standbyStationsFromConfig(model.Config)
}
