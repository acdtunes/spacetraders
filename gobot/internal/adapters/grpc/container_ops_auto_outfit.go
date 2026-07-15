package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	expansionAdapters "github.com/andrescamacho/spacetraders-go/internal/adapters/expansion"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	autooutfitCmd "github.com/andrescamacho/spacetraders-go/internal/application/autooutfit"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipOutfit "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/outfitting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/market"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainOutfit "github.com/andrescamacho/spacetraders-go/internal/domain/outfitting"
	"github.com/andrescamacho/spacetraders-go/pkg/utils"
)

// This file wires the guarded auto-outfit coordinator's launch path (sp-buyd) + its
// concrete driven-port adapters. The launch trigger mirrors the capacity reconciler
// (container_ops_capacity_reconciler.go): identity-only launch config →
// buildCommandForType (the single builder shared by creation and recovery) → NewContainer
// with iterations=-1 for the infinite reconcile loop → Add → runner → registerContainer →
// go Start.
//
// DEPLOY-INERT (sp-buyd requirement): this coordinator is deliberately NOT a member of
// bootStandingCoordinatorTypes (contrast the market-freshness sizer). Nothing launches it
// at boot; a fresh deploy changes nothing for live players. It runs ONLY when explicitly
// started:
//
//	spacetraders workflow auto-outfit --agent <AGENT> [--dry-run]
//
// Once started it is restart-safe: the container persists as RUNNING, and a daemon restart
// re-adopts it through RecoverRunningContainers → buildCommandForType (RULINGS #2). Stop
// with `spacetraders container stop <id>`.

// AutoOutfitCoordinator starts the standing guarded auto-outfit coordinator for a player:
// a recovery-safe container that each tick measures per-hull saturation, catalogs
// available modules, and installs the highest-marginal-value upgrade behind the fail-closed
// guard stack. dryRun arms observe-only mode (the recommended first-start posture): it
// evaluates and logs every WOULD-install but actuates nothing.
func (s *DaemonServer) AutoOutfitCoordinator(ctx context.Context, playerID int, dryRun bool) (string, error) {
	// Double-launch guard: ONE coordinator per player. A twin loop would double-spend on
	// the same measured gaps — refuse loudly, matching the guarded launches elsewhere.
	existingID, err := firstContainerIDOfType(ctx, s.containerRepo, playerID, container.ContainerTypeAutoOutfitCoordinator)
	if err != nil {
		return "", fmt.Errorf("failed to check for a running auto-outfit coordinator: %w", err)
	}
	if existingID != "" {
		return "", fmt.Errorf("auto-outfit coordinator already running for player %d (container %s) — stop it first: spacetraders container stop %s",
			playerID, existingID, existingID)
	}

	containerID := utils.GenerateContainerID("auto_outfit", fmt.Sprintf("player-%d", playerID))

	// Identity only — the tunable knobs default in the coordinator and are live-tunable via
	// `tune --operation autooutfit`. auto_outfit_launch_dry_run is an IDENTITY flag (the
	// launch-time --dry-run decision, persisted so a recovered container stays observe-only
	// until stopped and relaunched — mirrors capacity_launch_dry_run).
	config := map[string]interface{}{
		"container_id": containerID,
	}
	if dryRun {
		config["auto_outfit_launch_dry_run"] = true
	}

	cmd, err := s.buildCommandForType("auto_outfit_coordinator", config, playerID, containerID)
	if err != nil {
		return "", fmt.Errorf("failed to create auto-outfit command: %w", err)
	}

	containerEntity := container.NewContainer(
		containerID,
		container.ContainerTypeAutoOutfitCoordinator,
		playerID,
		-1,  // Infinite iterations (reconcile loop) — NOT a CoordinatorOwnsIterations type
		nil, // No parent container
		config,
		nil, // Use default RealClock for production
	)

	if err := s.containerRepo.Add(ctx, containerEntity, "auto_outfit_coordinator"); err != nil {
		return "", fmt.Errorf("failed to persist auto-outfit container: %w", err)
	}

	runner := NewContainerRunner(containerEntity, s.mediator, cmd, s.logRepo, s.containerRepo, s.shipRepo, s.clock)
	s.registerContainer(containerID, runner)

	go func() {
		if err := runner.Start(); err != nil {
			fmt.Printf("Auto-outfit container %s failed: %v\n", containerID, err)
		}
	}()

	return containerID, nil
}

// NewAutoOutfitCoordinatorHandler assembles the auto-outfit handler, wiring every concrete
// driven port to the daemon's live collaborators. Read ports (telemetry, fleet) are the
// existing repositories; the money guard reuses the same treasury reader the freshness
// sizer wires; the catalog reads the persisted market cache; the watchlist and install
// events ride the captain event store; and the outfitter drives the existing atomic,
// guarded InstallModuleCommand.
func NewAutoOutfitCoordinatorHandler(
	apiClient *api.SpaceTradersClient,
	shipRepo navigation.ShipRepository,
	tourTelemetry *persistence.TourTelemetryRepositoryGORM,
	marketRepo market.MarketRepository,
	med common.Mediator,
	eventStore captain.EventStore,
	containerRepo *persistence.ContainerRepositoryGORM,
) *autooutfitCmd.RunAutoOutfitCoordinatorHandler {
	h := autooutfitCmd.NewRunAutoOutfitCoordinatorHandler(
		tourTelemetry,
		shipRepo,
		&autoOutfitCatalogReader{marketRepo: marketRepo},
		nil, // RealClock
	)
	h.SetTreasuryReader(expansionAdapters.NewTreasuryReader(apiClient))
	h.SetOutfitter(&autoOutfitInstaller{med: med})
	h.SetWatchlistNotifier(&autoOutfitWatchlist{store: eventStore})
	h.SetNewHullCostReader(&autoOutfitNewHullCost{})
	h.SetLiveConfigReader(NewContainerConfigReader(containerRepo))
	h.SetEventRecorder(eventStore)
	return h
}

// --- catalog (reads the persisted market cache for wanted-module offers) ---

// autoOutfitCatalogReader builds the module catalog off the persisted market_data cache:
// for each wanted module, the cheapest market selling it across the player's systems (the
// market ask = SellPrice), the module's known capacity, and its source waypoint. A market
// that never appeared in a scanned system simply yields no offer (a readable zero, not a
// fail-close). readable=false only when no market surface is wired at all.
type autoOutfitCatalogReader struct {
	marketRepo market.MarketRepository
}

func (r *autoOutfitCatalogReader) ReadCatalog(ctx context.Context, playerID int, systems, wanted []string) ([]domainOutfit.ModuleOffer, bool, error) {
	if r.marketRepo == nil {
		return nil, false, nil // no market surface wired → fail closed
	}
	offers := make([]domainOutfit.ModuleOffer, 0, len(wanted))
	for _, symbol := range wanted {
		best, bestSystem := r.cheapestAcrossSystems(ctx, symbol, systems, playerID)
		if best == nil {
			continue // not selling in any reachable system yet
		}
		offers = append(offers, domainOutfit.ModuleOffer{
			Symbol:         symbol,
			Class:          domainOutfit.ClassifyModule(symbol),
			Price:          best.SellPrice,
			CapacityGained: domainOutfit.KnownModuleCapacity(symbol),
			Waypoint:       best.WaypointSymbol,
			System:         bestSystem,
			// ReachHops 0: in-system source (the market data already implies reachability —
			// scouts only scan flyable systems). Cross-system divert-hop costing is banked.
			ReachHops: 0,
		})
	}
	return offers, true, nil
}

func (r *autoOutfitCatalogReader) cheapestAcrossSystems(ctx context.Context, symbol string, systems []string, playerID int) (*market.CheapestMarketResult, string) {
	var best *market.CheapestMarketResult
	bestSystem := ""
	for _, system := range systems {
		res, err := r.marketRepo.FindCheapestMarketSelling(ctx, symbol, system, playerID)
		if err != nil || res == nil {
			continue
		}
		if best == nil || res.SellPrice < best.SellPrice {
			best, bestSystem = res, system
		}
	}
	return best, bestSystem
}

// --- outfitter (drives the atomic, guarded InstallModuleCommand) ---

// autoOutfitInstaller actuates an install by driving the existing InstallModuleCommand —
// the atomic op that claims the hull, gates the shipyard fee on the working-capital floor,
// docks, installs, and persists the new capacity. The buy-at-market and deliver-to-cargo
// steps that must precede it are the banked actuation enrichment: InstallModuleCommand
// requires the module already in the hull's cargo and returns an honest "buy it first"
// error otherwise, which the coordinator logs as a fail-closed no-op (never a crash).
type autoOutfitInstaller struct {
	med common.Mediator
}

func (o *autoOutfitInstaller) AcquireAndInstall(ctx context.Context, playerID int, shipSymbol, moduleSymbol, buyAtWaypoint string) (int, error) {
	pid := playerID
	resp, err := o.med.Send(ctx, &shipOutfit.InstallModuleCommand{
		ShipSymbol:   shipSymbol,
		ModuleSymbol: moduleSymbol,
		PlayerID:     &pid,
	})
	if err != nil {
		return 0, err
	}
	out, ok := resp.(*shipOutfit.InstallModuleResponse)
	if !ok || out == nil {
		return 0, fmt.Errorf("unexpected response type from InstallModuleCommand")
	}
	return out.CargoCapacity, nil
}

// --- watchlist (first-sighting announcement, event-store deduped) ---

// autoOutfitWatchlist announces a wanted module the first time it enters reach, deduped by
// the captain event store: an in-reach event already unprocessed for the same module is
// not re-announced (announced=false). The module symbol is used as the event's ship-scope
// key so each watchlisted module dedupes independently.
type autoOutfitWatchlist struct {
	store captain.EventStore
}

func (w *autoOutfitWatchlist) AnnounceInReach(ctx context.Context, playerID int, moduleSymbol, waypoint string, price int) (bool, error) {
	if w.store == nil {
		return false, nil
	}
	exists, err := w.store.HasUnprocessed(ctx, playerID, captain.EventAutoOutfitModuleInReach, moduleSymbol)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil // already announced (still unprocessed) — do not duplicate
	}
	payload := fmt.Sprintf(`{"module":%q,"waypoint":%q,"price":%d}`, moduleSymbol, waypoint, price)
	if err := w.store.Record(ctx, &captain.Event{
		Type:     captain.EventAutoOutfitModuleInReach,
		Ship:     moduleSymbol,
		PlayerID: playerID,
		Payload:  payload,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// --- new-hull cost (the autosizer buy-hull payback baseline) ---

// autoOutfitNewHullCost is the relative-payback baseline reader. It currently fails CLOSED
// (readable=false → the relative payback gate is OFF), banking the live wiring of the
// autosizer's yard-price → $/unit-cargo-capacity signal. The domain scorer and coordinator
// already enforce the gate (proven by tests); this is the live-signal follow-up. Until it
// is wired, the standalone spend guards and the absolute payback gate are the protection.
type autoOutfitNewHullCost struct{}

func (n *autoOutfitNewHullCost) CostPerUnitCapacity(ctx context.Context, playerID int) (float64, bool, error) {
	return 0, false, nil
}
