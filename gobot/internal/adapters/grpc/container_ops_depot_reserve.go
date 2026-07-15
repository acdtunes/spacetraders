package grpc

import (
	"context"
	"encoding/json"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// This file is the CONTRACT-WORKER RESERVE FLOOR (bead sp-mzdk): the guard that stops the depot
// topology from pinning the ENTIRE flexible contract-worker pool to hubs as depot-delivery.
//
// The depot topology (sp-u9xa/sp-9j9c/sp-3l64) converts a declared delivery hull from an
// undedicated HOME general hauler into a hull PINNED to its hub (DedicatedFleet =
// depot.DeliveryHullFleet) and EXCLUDED from the contract coordinator's grab. That is correct for
// BUFFERED goods (co-located stock delivers locally). But an UNBUFFERED-good contract still needs
// a general SOURCING worker to fly out and buy it — and if the topology declares delivery hulls
// for EVERY home hauler, pinning them all leaves ZERO general workers, so an unbuffered contract
// starves while idle hulls sit hub-locked (the live incident: COPPER_ORE -> H51, 0/60 for 80min).
//
// The floor RESERVES min_home_contract_workers undedicated home general haulers that are NEVER
// converted to a depot-delivery pin. It is PREVENTIVE: it caps a FRESH pin (a hull undedicated at
// launch time), leaving the reserve undedicated + home + available to the contract grab. Delivery
// hulls ABOVE the floor still pin, so buffered delivery is unaffected (regression-safe).

// MinHomeContractWorkersDefault is the documented default reserve: the number of undedicated home
// general haulers kept out of depot-delivery pinning so the contract coordinator always has a
// pool to source an UNBUFFERED-good contract with (per Admiral). It applies when neither the live
// contract-coordinator config nor the config.yaml launch value carries a positive one.
const MinHomeContractWorkersDefault = 6

// minHomeContractWorkersConfigKey is the config-column / launch-config key the reserve floor is
// tuned by. The tune bounds registry (container_ops_tune.go) and the live resolver below name the
// SAME key so a `tune --operation contract` write is what the census reads back on the next launch.
const minHomeContractWorkersConfigKey = "min_home_contract_workers"

// DepotBufferMinSourceDistanceDefault is the documented default for the sp-rxrg gate-3 source-distance
// floor: a destination-depot warehouse never buffers a good whose nearest EXTERNAL source sits at or
// below this many coordinate units. A homed hauler buys such a near/local-sourced good on-site about
// as cheaply as the buffer would pre-stage it, so the warehouse slot and the stocker haul are wasted
// (the DRUGS@J58 incident: DRUGS is exported at J58, so its source is co-located — distance ~0). It
// applies when the live contract-coordinator config carries no positive override. Live-tunable
// without restart via `tune --operation contract --key depot_buffer_min_source_distance`.
const DepotBufferMinSourceDistanceDefault = 25

// depotBufferMinSourceDistanceConfigKey is the config-column key the gate-3 floor is tuned by. The
// tune bounds registry (container_ops_tune.go) and the live resolver below name the SAME key so a
// `tune --operation contract` write is what the next warehouse (re)solve reads back.
const depotBufferMinSourceDistanceConfigKey = "depot_buffer_min_source_distance"

// ContractCoordinatorTunableDefaults maps every LIVE-tunable contract-fleet-coordinator knob to
// its documented default — mirroring SizerTunableDefaults / ScoutPostTunableDefaults so the
// daemon's tune bounds registry reads the default-of-record from next to the const it mirrors.
func ContractCoordinatorTunableDefaults() map[string]int {
	return map[string]int{
		minHomeContractWorkersConfigKey:       MinHomeContractWorkersDefault,
		depotBufferMinSourceDistanceConfigKey: DepotBufferMinSourceDistanceDefault,
	}
}

// resolveDepotBufferMinSourceDistance resolves the sp-rxrg gate-3 source-distance floor from the
// live>default chain: a positive value on the active contract-coordinator's config column (what
// `tune --operation contract --key depot_buffer_min_source_distance` writes) wins; else the
// documented default. A zero/absent live value means "unset" and defers to the default, matching the
// tune mechanism's revert-to-default semantics.
func resolveDepotBufferMinSourceDistance(live map[string]interface{}) int {
	if live != nil {
		if v, ok := intValue(live[depotBufferMinSourceDistanceConfigKey]); ok && v > 0 {
			return v
		}
	}
	return DepotBufferMinSourceDistanceDefault
}

// liveDepotBufferMinSourceDistance reads the gate-3 floor at warehouse-(re)solve time, resolving
// live>default off the active contract-fleet-coordinator container's config column (the SAME LIVE
// tier the reserve floor reads). A missing coordinator / DB hiccup degrades to the documented
// default — never blocks a warehouse launch.
func (s *DaemonServer) liveDepotBufferMinSourceDistance(ctx context.Context, playerID int) int {
	return resolveDepotBufferMinSourceDistance(s.activeContractCoordinatorConfig(ctx, playerID))
}

// deliveryPinBudget is the reserve-floor census the delivery-hull launch consults before pinning
// (bead sp-mzdk). Available is the count of undedicated home general haulers currently backing the
// contract coordinator's grab (the FindIdleLightHaulers pool); Floor is min_home_contract_workers,
// the number of them to keep undedicated; InPool[ship] reports whether that ship is one of them
// right now — so pinning it is what draws the pool down. The zero value (Available 0, Floor 0, nil
// InPool) reserves nothing: the regression-safe, pre-sp-mzdk pin-everything behavior for a launch
// with no census (feature off / degraded).
type deliveryPinBudget struct {
	Available int
	Floor     int
	InPool    map[string]bool
}

// remaining is how many home general haulers may still be converted to delivery pins before the
// floor binds: Available - Floor, clamped at 0 so a pool already at/under the floor converts none
// (and a negative never "owes" pins — degrade gracefully when there are fewer haulers than floor).
func (b deliveryPinBudget) remaining() int {
	if b.Available <= b.Floor {
		return 0
	}
	return b.Available - b.Floor
}

// reserveHomeContractWorkers returns the set of delivery-hull ship symbols to RESERVE — left
// undedicated + home + available to the contract coordinator's grab instead of pinned — so the
// undedicated home general pool never drops below the floor (bead sp-mzdk). It walks the launch
// intents in stable order; a delivery hull currently IN the home general pool (InPool) consumes
// one unit of the Available-Floor budget, and once that budget is spent every FURTHER in-pool
// delivery hull is reserved. A delivery hull NOT in the pool — already depot-delivery pinned,
// foreign-fleet, or out of the home system — is never gated: it pins regardless and spends no
// budget, so a reload re-positions the already-pinned fleet unchanged and only a FRESH conversion
// of a home general hauler is what the floor caps.
//
// A non-delivery intent (warehouse/stocker/source-hub) is never reserved — the floor governs the
// delivery-pin conversion only. An empty result means "reserve nothing" (pin everything), the
// regression-safe default.
func reserveHomeContractWorkers(intents []depotLaunchIntent, budget deliveryPinBudget) map[string]bool {
	reserved := map[string]bool{}
	remaining := budget.remaining()
	for _, intent := range intents {
		if intent.role != depot.RoleDeliveryHull {
			continue // the floor governs delivery-pin conversion only
		}
		if !budget.InPool[intent.shipSymbol] {
			continue // already pinned / foreign / off-home — not a fresh pool conversion, never gated
		}
		if remaining > 0 {
			remaining-- // within the Available-Floor budget — pin it
			continue
		}
		reserved[intent.shipSymbol] = true // floor binds — keep this hull undedicated + available
	}
	return reserved
}

// resolveMinHomeContractWorkers resolves the reserve floor from the live>launch>default chain
// (bead sp-mzdk), mirroring the tune-config resolution idiom (a positive live value wins; else the
// launch/config.yaml value; else the documented default). live is the active contract fleet
// coordinator's persisted config column (nil when there is none), launch is the config.yaml value.
func resolveMinHomeContractWorkers(live map[string]interface{}, launch int) int {
	if live != nil {
		if v, ok := intValue(live[minHomeContractWorkersConfigKey]); ok && v > 0 {
			return v // live: a `tune --operation contract` write on the coordinator's config
		}
	}
	if launch > 0 {
		return launch // launch: config.yaml [contract] min_home_contract_workers
	}
	return MinHomeContractWorkersDefault
}

// liveMinHomeContractWorkers reads the reserve floor at launch time, resolving live>launch>default
// (bead sp-mzdk). The LIVE tier is the active contract fleet coordinator container's config column
// (what `tune --operation contract` writes); the LAUNCH tier is config.yaml. A missing coordinator
// / DB hiccup degrades to launch-or-default, never blocking the launch.
func (s *DaemonServer) liveMinHomeContractWorkers(ctx context.Context, playerID int) int {
	launch := s.contractConfig.MinHomeContractWorkers
	live := s.activeContractCoordinatorConfig(ctx, playerID)
	return resolveMinHomeContractWorkers(live, launch)
}

// activeContractCoordinatorConfig returns the active contract fleet coordinator's persisted config
// map, or nil when there is none / it cannot be read — the LIVE tune tier for the reserve floor.
func (s *DaemonServer) activeContractCoordinatorConfig(ctx context.Context, playerID int) map[string]interface{} {
	if s.containerRepo == nil {
		return nil
	}
	model, err := s.containerRepo.FindActiveCoordinatorByType(ctx, string(container.ContainerTypeContractFleetCoordinator), playerID)
	if err != nil || model == nil || model.Config == "" {
		return nil
	}
	config := map[string]interface{}{}
	if err := json.Unmarshal([]byte(model.Config), &config); err != nil {
		return nil
	}
	return config
}

// homeContractWorkerReserve (depotCoordinatorSink) is the production reserve-floor census: it
// resolves the live floor and counts the undedicated home general haulers currently backing the
// contract coordinator's grab in the depot's hub system(s) (bead sp-mzdk). "Home" is the system of
// the registry's declared delivery-hull hubs — exactly the region the pinning depletes — so the
// reserve keeps a HOME sourcing worker rather than forcing an expensive cross-gate foreign grab.
// Fail-open: a nil ship repo / registry, an un-derivable home system, or a fetch error yields a
// census that reserves nothing beyond the trivial floor, never blocking the launch.
func (s *DaemonServer) homeContractWorkerReserve(ctx context.Context, reg *depot.Registry, playerID int) deliveryPinBudget {
	budget := deliveryPinBudget{Floor: s.liveMinHomeContractWorkers(ctx, playerID)}
	if s.shipRepo == nil || reg == nil {
		return budget
	}
	homeSystems := deliveryHubSystems(reg)
	if len(homeSystems) == 0 {
		return budget // no delivery hubs declared -> no pinning to gate
	}
	ships, err := s.shipRepo.FindAllByPlayer(ctx, shared.MustNewPlayerID(playerID))
	if err != nil {
		return budget
	}
	inPool := map[string]bool{}
	for _, ship := range ships {
		if isUndedicatedHomeGeneralHauler(ship, homeSystems) {
			inPool[ship.ShipSymbol()] = true
		}
	}
	budget.Available = len(inPool)
	budget.InPool = inPool
	return budget
}

// deliveryHubSystems is the set of systems the registry's declared delivery-hull hubs sit in — the
// "home" region(s) the reserve-floor census is measured against. A hub with no resolvable system is
// skipped; an empty result means no delivery hulls are declared (nothing to gate).
func deliveryHubSystems(reg *depot.Registry) map[string]bool {
	systems := map[string]bool{}
	if reg == nil {
		return systems
	}
	for _, c := range reg.Depots() {
		for _, hull := range c.DeliveryHulls() {
			if hull.Waypoint == "" {
				continue
			}
			if system := shared.ExtractSystemSymbol(hull.Waypoint); system != "" {
				systems[system] = true
			}
		}
	}
	return systems
}

// isUndedicatedHomeGeneralHauler reports whether a ship is a member of the contract coordinator's
// general grab pool that the reserve floor protects: an UNDEDICATED (no DedicatedFleet tag), cargo-
// capable HAULER — never the command frigate (last-resort only) — currently in one of the depot's
// home hub systems. A pinned/dedicated hull, a probe (0 cargo), or an out-of-home-system hull is
// not in the pool, so pinning it never draws the reserve down.
func isUndedicatedHomeGeneralHauler(ship *navigation.Ship, homeSystems map[string]bool) bool {
	if ship == nil || ship.DedicatedFleet() != "" {
		return false
	}
	if domainContract.IsCommandHull(ship) {
		return false
	}
	if ship.Role() != roleHaulerRegistration {
		return false
	}
	if ship.CargoCapacity() == 0 {
		return false
	}
	loc := ship.CurrentLocation()
	if loc == nil || loc.Symbol == "" {
		return false
	}
	return homeSystems[shared.ExtractSystemSymbol(loc.Symbol)]
}

// roleHaulerRegistration is the registration role of a general haul hull — the same "HAULER" tag
// FindIdleLightHaulers selects the contract grab pool by (kept local to avoid a cross-package
// dependency on the application contract package's unexported constant).
const roleHaulerRegistration = "HAULER"
