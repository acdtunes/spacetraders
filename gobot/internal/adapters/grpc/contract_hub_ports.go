package grpc

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// This file wires the contract-HUB placement coordinator's application ports (sp-q2zq) to
// the concrete daemon collaborators — the exact analogue of siting_ports.go for the
// factory-siting sibling. The coordinator engine (run_contract_hub_coordinator*.go) depends
// only on four narrow interfaces (HubCandidateSource / ContractDemandSource /
// HaulerHomeSource / HomeAssigner), tested against fakes; these are the thin bridges the
// daemon injects at boot. No business logic lives here — every method forwards to an
// existing repo/service and adapts its shape.
//
// PERSISTENCE MODEL (the design decision the bead parked on — resolved here). A contract
// hauler's "home" is NOT a per-ship field: the operation homes idle dedicated hulls to a
// SET of standby stations and serves each contract drop closest-ship-wins (the bead's own
// coverage guardrail: "home POSITIONS" are what matter, not per-ship attribution). That set
// already IS a daemon-single-writer (RULINGS #3), restart-resilient (RULINGS #2) store: the
// contract coordinator's container-config `standby_stations` key, mutated live by the
// `fleet hub` RPC (sp-jcke, container_ops_fleet_hub.go) and reloaded on boot by the
// coordinator rebuild. So:
//   - HomeAssigner ADDS the computed hub to that set (MutateStandbyStation) — it never
//     claims or moves a ship (RULINGS #7); the physical relocation stays with the contract
//     coordinator's existing HomeShip pass, which already respects pins/claims.
//   - HaulerHomeSource reports a hauler as HOMED iff it currently sits at a hub in that set
//     (a fed coverage position + per-hub count); a hull NOT at any hub is the placement set.
//     The round-trip converges: a placed hub joins the set, the hull homes there via the
//     existing balancer, and next tick it reads back HOMED — no thrash, growth self-limits
//     at the demand-cluster count via the facility-location score.
// This reuses option (B) from the bead (container-config-backed) with ZERO new persistence
// surface, honoring RULINGS #2/#3 intrinsically.

const (
	// contractHubOperation is the `fleet hub` operation key whose coordinator owns the
	// standby-station ("home") set — the contract fleet (see hubCapableCoordinatorTypes).
	contractHubOperation = "contract"

	// contractDedicatedFleet is the DedicatedFleet tag a contract-hauler hull carries
	// (matches the contract package's dedicated pool and hubCapableCoordinatorTypes key). A
	// hull carrying it is a member of the contract fleet the coordinator homes.
	contractDedicatedFleet = "contract"
)

// NewContractHubCoordinatorHandler assembles the contract-hub placement coordinator handler
// (sp-q2zq), wiring every concrete port to the daemon's live collaborators — mirroring
// NewSitingCoordinatorHandler. The demand reader + market + waypoint + ship repos are the
// SAME instances the contract/scouting paths already hold, so a hub is scored off exactly
// the market/contract data the live engine sees. clock defaults to the real clock (nil).
func NewContractHubCoordinatorHandler(
	server *DaemonServer,
	contracts contractDemandReader,
	marketReader market.MarketRepository,
	waypoints system.WaypointRepository,
	shipRepo navigation.ShipRepository,
) *contractCmd.RunContractHubCoordinatorHandler {
	return contractCmd.NewRunContractHubCoordinatorHandler(
		&contractHubCandidateSource{contracts: contracts, market: marketReader, waypoints: waypoints, ships: shipRepo},
		&contractHubDemandSource{contracts: contracts},
		&contractHubHaulerSource{ships: shipRepo, server: server},
		&contractHubHomeAssigner{server: server},
		nil, // nil = use RealClock
	)
}

// contractDemandReader is the read-only recent-contracts projection both the SCAN and the
// DEMAND ports fold (persistence.GormContractRepository.RecentContractDemand). Kept narrow
// so the coordinator wiring depends on the projection, not the whole contract repo.
type contractDemandReader interface {
	RecentContractDemand(ctx context.Context, playerID, limit int) ([]persistence.RecentContractDemand, error)
}

// --- DEMAND: recent-contract EWMA source (ContractDemandSource) ---

// contractHubDemandSource is the concrete contractCmd.ContractDemandSource: it forwards the
// player's recent contracts (oldest→newest) as demand records the coordinator's EWMA folds.
// A read error is surfaced so the tick leaves every home untouched (fail-safe, acceptance #4).
type contractHubDemandSource struct {
	contracts contractDemandReader
}

func (s *contractHubDemandSource) RecentContracts(ctx context.Context, playerID int) ([]contractCmd.ContractDemandRecord, error) {
	rows, err := s.contracts.RecentContractDemand(ctx, playerID, 0) // 0 → repo default limit
	if err != nil {
		return nil, err
	}
	out := make([]contractCmd.ContractDemandRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, contractCmd.ContractDemandRecord{Goods: r.Goods, PaymentOnFulfilled: r.PaymentOnFulfilled})
	}
	return out, nil
}

// --- SCAN: candidate hubs + per-good cheapest sources (HubCandidateSource) ---

// contractHubCandidateSource is the concrete contractCmd.HubCandidateSource: for the fleet's
// single operating system (RULINGS #14) it resolves, per recent-contract good, the cheapest
// in-system source S_G and its position, and offers each distinct source waypoint as a
// candidate hub. It hides the market_data joins; the engine is geometry-only. A market/fleet
// read ERROR is surfaced (fail-safe: the tick assigns nothing); a good with no in-system
// source or unknown position is cleanly skipped.
type contractHubCandidateSource struct {
	contracts contractDemandReader
	market    market.MarketRepository
	waypoints system.WaypointRepository
	ships     navigation.ShipRepository
}

func (s *contractHubCandidateSource) ScanHubs(ctx context.Context, playerID int) (contractCmd.HubScan, error) {
	// Single-system pre-gate (RULINGS #14): the contract fleet works ONE system; sourcing
	// legs never leave it. Anchor the scan to where the dedicated haulers physically are.
	operatingSystem, err := contractOperatingSystem(ctx, s.ships, playerID)
	if err != nil {
		return contractCmd.HubScan{}, err
	}
	if operatingSystem == "" {
		return contractCmd.HubScan{}, nil // no contract fleet yet → nothing to place
	}

	rows, err := s.contracts.RecentContractDemand(ctx, playerID, 0)
	if err != nil {
		return contractCmd.HubScan{}, err
	}

	var (
		sources    []contractCmd.GoodSource
		candidates []contractCmd.HubCandidate
		candSeen   = map[string]struct{}{}
		goodSeen   = map[string]struct{}{}
	)
	for _, row := range rows {
		for _, good := range row.Goods {
			if _, done := goodSeen[good]; done {
				continue // resolve each distinct good once
			}
			goodSeen[good] = struct{}{}

			res, err := s.market.FindCheapestMarketSelling(ctx, good, operatingSystem, playerID)
			if err != nil {
				return contractCmd.HubScan{}, err // transient market read → fail-safe
			}
			if res == nil {
				continue // no in-system source for this good → not a candidate
			}
			wp, err := s.waypoints.FindBySymbol(ctx, res.WaypointSymbol, operatingSystem)
			if err != nil || wp == nil {
				continue // position unknown → cannot score its geometry; skip
			}

			sources = append(sources, contractCmd.GoodSource{Good: good, Waypoint: res.WaypointSymbol, X: wp.X, Y: wp.Y})
			if _, ok := candSeen[res.WaypointSymbol]; !ok {
				candSeen[res.WaypointSymbol] = struct{}{}
				candidates = append(candidates, contractCmd.HubCandidate{Waypoint: res.WaypointSymbol, X: wp.X, Y: wp.Y})
			}
		}
	}
	return contractCmd.HubScan{Candidates: candidates, Sources: sources}, nil
}

// --- FLEET: hauler homes (HaulerHomeSource) ---

// contractHubHaulerSource is the concrete contractCmd.HaulerHomeSource. It reports each
// contract-dedicated hull with its idle flag and its home — HOMED iff the hull currently
// sits at one of the operation's standby stations (the persisted home set); otherwise
// unhomed (the placement set). A homed hull feeds the coverage baseline + per-hub count and
// is left alone (Phase 1 never re-homes). A repo read error is surfaced (fail-safe).
type contractHubHaulerSource struct {
	ships  navigation.ShipRepository
	server *DaemonServer
}

func (s *contractHubHaulerSource) Haulers(ctx context.Context, playerID int) ([]contractCmd.HaulerHome, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return nil, err
	}
	allShips, err := s.ships.FindAllByPlayer(ctx, pid)
	if err != nil {
		return nil, err
	}
	homeSet, err := s.server.contractStandbyStations(ctx, playerID)
	if err != nil {
		return nil, err
	}
	inHomeSet := make(map[string]struct{}, len(homeSet))
	for _, wp := range homeSet {
		inHomeSet[wp] = struct{}{}
	}

	var out []contractCmd.HaulerHome
	for _, ship := range allShips {
		if ship.DedicatedFleet() != contractDedicatedFleet {
			continue
		}
		hh := contractCmd.HaulerHome{
			ShipSymbol: ship.ShipSymbol(),
			Idle:       ship.IsIdle() && !ship.IsInTransit(),
		}
		if loc := ship.CurrentLocation(); loc != nil {
			if _, homed := inHomeSet[loc.Symbol]; homed {
				hh.HomeWaypoint = loc.Symbol
				hh.HomeX = loc.X
				hh.HomeY = loc.Y
			}
		}
		out = append(out, hh)
	}
	return out, nil
}

// --- ACT: home assigner (HomeAssigner) ---

// contractHubHomeAssigner is the concrete contractCmd.HomeAssigner. It persists a computed
// home by ADDING the hub to the contract coordinator's standby-station set through the
// daemon (the single writer, RULINGS #3), restart-resilient via the coordinator's container
// config (RULINGS #2). It NEVER claims or moves a ship (RULINGS #7): the physical relocation
// is the contract coordinator's existing HomeShip pass. The ship symbol is attribution only
// (homing is closest-ship-wins over the hub geometry, exactly the bead's coverage model).
type contractHubHomeAssigner struct {
	server *DaemonServer
}

func (a *contractHubHomeAssigner) AssignHome(ctx context.Context, playerID int, shipSymbol, hubWaypoint string) error {
	_, _, err := a.server.MutateStandbyStation(ctx, contractHubOperation, hubWaypoint, true, playerID)
	return err
}

// --- shared helpers ---

// contractStandbyStations reads the contract coordinator's live standby-station ("home") set
// — the store MutateStandbyStation writes. No running coordinator → empty set (nothing homed
// yet), never an error; a malformed config surfaces so the caller stays fail-safe.
func (s *DaemonServer) contractStandbyStations(ctx context.Context, playerID int) ([]string, error) {
	model, err := s.containerRepo.FindActiveCoordinatorByType(ctx, string(container.ContainerTypeContractFleetCoordinator), playerID)
	if err != nil {
		return nil, err
	}
	if model == nil {
		return nil, nil
	}
	return standbyStationsFromConfig(model.Config)
}

// contractOperatingSystem returns the single system the contract fleet operates in
// (RULINGS #14): the modal current-system of the contract-dedicated hulls, lexicographic
// tie-break for determinism. "" when the player has no contract fleet yet (→ nothing to
// place). A repo read error is surfaced so the caller stays fail-safe.
func contractOperatingSystem(ctx context.Context, ships navigation.ShipRepository, playerID int) (string, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return "", err
	}
	allShips, err := ships.FindAllByPlayer(ctx, pid)
	if err != nil {
		return "", err
	}
	counts := map[string]int{}
	for _, ship := range allShips {
		if ship.DedicatedFleet() != contractDedicatedFleet {
			continue
		}
		loc := ship.CurrentLocation()
		if loc == nil || loc.SystemSymbol == "" {
			continue
		}
		counts[loc.SystemSymbol]++
	}
	best := ""
	for sys, n := range counts {
		if n > counts[best] || (n == counts[best] && (best == "" || sys < best)) {
			best = sys
		}
	}
	return best, nil
}
