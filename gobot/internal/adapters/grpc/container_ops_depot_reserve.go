package grpc

import (
	"context"
	"encoding/json"
	"fmt"

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
// The floor RESERVES min_home_contract_workers home general haulers that are NEVER converted to a
// depot-delivery pin, and — the sp-7zoq fix — DEDICATES each of them to the exclusive "contract"
// fleet. Merely leaving the reserve undedicated (the sp-mzdk behavior) left it in the shared idle pool
// where ANY coordinator could poach it: the live incident was the goods_factory eating 4 of 6 reserve
// workers as opportunistic idle hulls. An exclusive "contract" dedication removes a hull from that pool
// (FindIdleLightHaulers, the reconciler SENSE filter, and ClaimShip's atomic guard all skip any hull
// dedicated to another fleet), so the reserve is poach-proof while STILL serving contracts through the
// coordinator's own FindIdleShipsByFleet("contract") lookup. Delivery hulls ABOVE the floor still pin,
// so buffered delivery is unaffected (regression-safe). A reload that finds too few undedicated hulls
// to reach the floor RECLAIMS the shortfall from already-pinned delivery hulls, re-dedicating them to
// "contract" (the deferred sp-mzdk temp-un-pin — a re-dedication, not an un-dedication to the pool).

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
// (bead sp-mzdk / sp-7zoq). Available is the count of UNDEDICATED home general haulers currently in
// the shared idle pool; Floor is min_home_contract_workers, the number of home haulers to hold as
// the contract sourcing reserve; InPool[ship] reports whether that ship is one of the undedicated
// pool right now — so pinning it is what draws the pool down.
//
// sp-7zoq additions (make the reserve poach-proof, not merely undedicated): ContractDedicated is how
// many home general haulers ALREADY carry the exclusive "contract" tag (they already satisfy the
// floor, so the fresh reserve only tops UP to Floor — never over-dedicates past it); Pinned lists the
// home general haulers currently pinned to depot-delivery (depot.DeliveryHullFleet), in stable order,
// the reclaim pool the floor re-dedicates to "contract" when too few undedicated hulls remain to reach
// the floor (the deferred sp-mzdk "temp-un-pin", done as a re-dedication TO contract rather than an
// un-dedication back to the poachable pool).
//
// The zero value (all zero, nil maps/slices) reserves + reclaims nothing: the regression-safe,
// pre-sp-mzdk pin-everything behavior for a launch with no census (feature off / degraded).
type deliveryPinBudget struct {
	Available         int
	Floor             int
	InPool            map[string]bool
	ContractDedicated int
	Pinned            []string
}

// remaining is how many home general haulers may still be converted to delivery pins before the
// floor binds: Available - need, where need = Floor - ContractDedicated is how many MORE home haulers
// must be dedicated to "contract" to reach the floor (sp-7zoq). Clamped at 0 so a pool already at/under
// the outstanding need converts none, and a negative never "owes" pins. Subtracting ContractDedicated
// is what stops a reload from OVER-dedicating: once C home haulers already carry the "contract" tag
// only Floor-C more are held back from pinning, never a fresh Floor on top of them.
func (b deliveryPinBudget) remaining() int {
	need := b.Floor - b.ContractDedicated
	if need < 0 {
		need = 0
	}
	if b.Available <= need {
		return 0
	}
	return b.Available - need
}

// reclaimPinnedForFloor returns the depot-delivery-pinned home haulers to RECLAIM by re-dedicating
// them to the exclusive "contract" fleet — the deferred sp-mzdk "temp-un-pin", done correctly
// (sp-7zoq): a reclaimed hull is dedicated TO contract, never un-dedicated back to the poachable pool.
//
// It fires only when the fresh undedicated reserve cannot reach the floor on its own: outstanding =
// Floor - ContractDedicated - freshlyReserved is the shortfall AFTER the already-contract hulls and the
// hulls freshly dedicated this launch are counted. The shortfall is drawn from Pinned in stable order
// and CAPPED at the shortfall (never past it) and at how many pinned hulls exist — so the floor lands
// at exactly Floor contract-dedicated home haulers, never more (the "don't over-dedicate" guardrail),
// and reclaims the fewest buffered-delivery pins needed to restore the sourcing floor.
func reclaimPinnedForFloor(budget deliveryPinBudget, freshlyReserved int) []string {
	outstanding := budget.Floor - budget.ContractDedicated - freshlyReserved
	if outstanding <= 0 {
		return nil
	}
	if outstanding > len(budget.Pinned) {
		outstanding = len(budget.Pinned)
	}
	return budget.Pinned[:outstanding]
}

// reserveHomeContractWorkers returns the set of delivery-hull ship symbols to RESERVE — held back
// from depot-delivery pinning and (sp-7zoq) dedicated by the caller to the exclusive "contract" fleet
// instead — so the contract sourcing reserve reaches the floor (bead sp-mzdk). It walks the launch
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
		reserved[intent.shipSymbol] = true // floor binds — hold this hull back to dedicate to "contract"
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
	var pinned []string
	contractDedicated := 0
	for _, ship := range ships {
		fleet, ok := homeGeneralHaulerFleet(ship, homeSystems)
		if !ok {
			continue // not a home general hauler — not the floor's concern
		}
		switch fleet {
		case "":
			inPool[ship.ShipSymbol()] = true // undedicated — the fresh reserve pool
		case contractDedicatedFleet:
			contractDedicated++ // already poach-proof — counts toward the floor (sp-7zoq)
		case depot.DeliveryHullFleet:
			pinned = append(pinned, ship.ShipSymbol()) // reclaim pool when the fresh reserve is short
		}
	}
	budget.Available = len(inPool)
	budget.InPool = inPool
	budget.ContractDedicated = contractDedicated
	budget.Pinned = pinned
	return budget
}

// dedicateContractReserve (depotCoordinatorSink) fleet-ASSIGNS a reserved or reclaimed home general
// hauler to the exclusive "contract" fleet (bead sp-7zoq) — the write that makes the reserve
// poach-proof. It writes through the SAME single AssignFleet dedication column the whole depot
// launch already re-dedicates warehouse/stocker/delivery hulls through (positionDepotElementHull),
// so every idle-grab exclusion built on that tag — FindIdleLightHaulers, the reconciler SENSE
// filter, and ClaimShip's atomic no-poach guard — takes effect for free. The census only ever
// selects cargo-capable haulers, so no cargo-floor gate is needed here; the underlying AssignFleet
// is idempotent (a hull already tagged "contract" performs zero DB writes), so a reload re-dedicating
// the converged reserve churns nothing.
func (s *DaemonServer) dedicateContractReserve(ctx context.Context, shipSymbol string, playerID int) error {
	if s.shipRepo == nil {
		return fmt.Errorf("dedicate contract reserve %s: no ship repository wired", shipSymbol)
	}
	if err := s.shipRepo.AssignFleet(ctx, shipSymbol, contractDedicatedFleet, shared.MustNewPlayerID(playerID)); err != nil {
		return fmt.Errorf("dedicate contract reserve %s: %w", shipSymbol, err)
	}
	return nil
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

// homeGeneralHaulerFleet reports whether a ship is a home general hauler the reserve floor governs —
// a cargo-capable HAULER, never the command frigate (last-resort only), currently in one of the
// depot's home hub systems — and if so returns its current DedicatedFleet tag ("" = undedicated). A
// probe (0 cargo), the command hull, or an out-of-home-system hull yields ok=false: it is not the
// floor's concern, so it is neither counted toward the reserve nor eligible to be pinned/reclaimed.
// The census partitions the home haulers by the returned tag: "" is the fresh reserve pool,
// "contract" already satisfies the floor, depot.DeliveryHullFleet is the reclaim pool (sp-7zoq).
func homeGeneralHaulerFleet(ship *navigation.Ship, homeSystems map[string]bool) (string, bool) {
	if ship == nil {
		return "", false
	}
	if domainContract.IsCommandHull(ship) {
		return "", false
	}
	if ship.Role() != roleHaulerRegistration {
		return "", false
	}
	if ship.CargoCapacity() == 0 {
		return "", false
	}
	loc := ship.CurrentLocation()
	if loc == nil || loc.Symbol == "" {
		return "", false
	}
	if !homeSystems[shared.ExtractSystemSymbol(loc.Symbol)] {
		return "", false
	}
	return ship.DedicatedFleet(), true
}

// roleHaulerRegistration is the registration role of a general haul hull — the same "HAULER" tag
// FindIdleLightHaulers selects the contract grab pool by (kept local to avoid a cross-package
// dependency on the application contract package's unexported constant).
const roleHaulerRegistration = "HAULER"
