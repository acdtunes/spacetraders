package grpc

// This file wires the fleet autosizer's WAREHOUSE application ports (sp-3yqa — the daemon
// read-path for the sp-1j3f WarehouseDemandProvider) to the concrete daemon collaborators. It is
// the warehouse M6-equivalent: the twin of siting_ports.go / fleet_autosizer_ports.go. sp-1j3f
// landed the demand + DISPATCH logic against three narrow ports; those ports stayed unwired, so
// the warehouse class was DORMANT even when opted in. These bridges make it live:
//
//   - warehousePortfolioSource — the durable running-chain portfolio for warehouse demand:
//     vdld running standing goods_factory chains ∩ the good's in-system EXPORT waypoint
//     (MarketLocator.FindExportMarket, where the warehouse co-locates) ∩ the rh2z chain_pnl
//     realized $/hr (the pay gate). Fails the WHOLE pass closed (readable=false) on an unreadable
//     chain set — a missing portfolio must never spend a credit or move a hull (RULINGS #4).
//   - warehouseHullSource — the player's warehouse-dedicated hulls and the waypoint each is parked
//     at (its running WAREHOUSE container's waypoint, or "" when unplaced). An error fails the buy
//     path closed (the pool is unknowable, so a shortfall cannot be sized without risking over-buy).
//   - warehouseDispatchBridge — navigates an idle warehouse hull to a durable export waypoint via
//     DaemonServer.StartWarehouse and un-strands a hull holding a retired chain by stopping its
//     container so the next tick re-places it on the durable target (StartWarehouse takes only idle
//     hulls). Moving an already-owned hull is never a credit spend.
//
// No business logic lives here — the durable-target math, hysteresis, pay gate, and co-export
// dedupe are all sp-1j3f's WarehouseDemandProvider. These forward to existing repos/clients.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	goodsServices "github.com/andrescamacho/spacetraders-go/internal/application/manufacturing/services"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// warehousePnLWindowHours is the trailing window the warehouse pay gate reads realized $/hr over.
// Matches the autosizer LIGHT source's read window so both classes judge chain earning on the same
// horizon.
const warehousePnLWindowHours = 2.0

// standingFactoryContainerType is the ContainerModel.ContainerType of a managed goods_factory
// chain (the vdld portfolio member the siting controller enumerates). Matches
// sitingChainController.RunningChains and autosizerLightSources.DesiredChains.
const standingFactoryContainerType = "goods_factory_coordinator"

// --- narrow collaborator interfaces (so the ports depend on behaviour, testable with fakes) ------

// warehouseContainerLister is the slice of the container repo the warehouse read-path needs: list
// the player's containers in a status. *persistence.ContainerRepositoryGORM satisfies it.
type warehouseContainerLister interface {
	ListByStatus(ctx context.Context, status container.ContainerStatus, playerID *int) ([]*persistence.ContainerModel, error)
}

// warehouseExportMarketLocator resolves a good's in-system EXPORT market waypoint — where the
// factory sells the good, so where a warehouse co-locates to absorb the output at basis.
// *goodsServices.MarketLocator satisfies it structurally.
type warehouseExportMarketLocator interface {
	FindExportMarket(ctx context.Context, good, systemSymbol string, playerID int) (*goodsServices.MarketLocatorResult, error)
}

// warehouseDispatcher is the slice of *DaemonServer the dispatch bridge drives: start a warehouse
// on an idle hull, or stop a stranded hull's container so it frees for re-placement.
type warehouseDispatcher interface {
	StartWarehouse(ctx context.Context, shipSymbol, waypointSymbol string, supportedGoods []string, playerID int) (*WarehouseOperationResult, error)
	StopContainer(containerID string) error
}

// --- WarehousePortfolioSource (vdld chains ∩ export waypoint ∩ chain_pnl) ---------------------

type warehousePortfolioSource struct {
	lister   warehouseContainerLister
	locator  warehouseExportMarketLocator
	chainPnL goodsServices.ChainPnLReader
}

func newWarehousePortfolioSource(lister warehouseContainerLister, locator warehouseExportMarketLocator, chainPnL goodsServices.ChainPnLReader) *warehousePortfolioSource {
	return &warehousePortfolioSource{lister: lister, locator: locator, chainPnL: chainPnL}
}

// RunningChains joins the durable running-chain portfolio for warehouse demand. It reads the vdld
// standing goods_factory chains, resolves each good's in-system export waypoint (the warehouse's
// home), and attaches its rh2z chain_pnl realized $/hr. An unreadable chain set fails the WHOLE
// pass closed (readable=false) so the demand+dispatch never runs on a missing portfolio.
func (s *warehousePortfolioSource) RunningChains(ctx context.Context, playerID int) ([]fleetCmd.PortfolioChain, bool, error) {
	models, err := s.lister.ListByStatus(ctx, container.ContainerStatusRunning, &playerID)
	if err != nil {
		return nil, false, err // RULINGS #4: unreadable chain set fails the whole pass closed
	}

	// Realized $/hr per good over the trailing window (rh2z chain_pnl). A nil reader or a read
	// error leaves every chain unproven (RealizedReadable=false): the portfolio (chain set) is
	// still readable, but no chain clears the pay gate, so nothing pulls a warehouse — the correct
	// fail-closed at chain granularity (a missing earnings proof never spends).
	realized := map[string]goodsServices.ChainPnLResult{}
	if s.chainPnL != nil {
		since := time.Now().Add(-time.Duration(warehousePnLWindowHours * float64(time.Hour)))
		if raw, rerr := s.chainPnL.ReadRealizedPnL(ctx, playerID, since); rerr == nil {
			realized = goodsServices.ComputeChainPnL(raw, warehousePnLWindowHours)
		}
	}

	logger := common.LoggerFromContext(ctx)
	var chains []fleetCmd.PortfolioChain
	for _, m := range models {
		if m.ContainerType != standingFactoryContainerType {
			continue
		}
		var cfg map[string]interface{}
		if m.Config != "" {
			if json.Unmarshal([]byte(m.Config), &cfg) != nil {
				continue
			}
		}
		// Standing chains only (iterations=-1) — a one-shot factory run is not a portfolio member.
		if iter, ok := cfg["max_iterations"].(float64); !ok || iter != -1 {
			continue
		}
		good, _ := cfg["target_good"].(string)
		system, _ := cfg["system_symbol"].(string)
		if good == "" || system == "" {
			continue
		}
		// Where the good is EXPORTed in-system — the warehouse's home. A good with no resolvable
		// export market cannot host a warehouse, so it is dropped from the portfolio (not a
		// whole-pass failure); a transient locator error drops just that chain, loudly.
		res, lerr := s.locator.FindExportMarket(ctx, good, system, playerID)
		if lerr != nil {
			logger.Log("WARNING", fmt.Sprintf("warehouse portfolio: export-market read for %s@%s failed: %v — chain skipped this tick", good, system, lerr), map[string]interface{}{
				"action": "warehouse_portfolio_export_read_error", "good": good, "system": system,
			})
			continue
		}
		if res == nil || res.WaypointSymbol == "" {
			continue
		}
		pnl, ok := realized[good]
		chains = append(chains, fleetCmd.PortfolioChain{
			Good:            good,
			ExportWaypoint:  res.WaypointSymbol,
			RealizedPerHour: pnl.NetPerHour,
			// Readable only when a realized P&L row exists AND it has realized output — a
			// pre-realization chain (no proven earning yet) fails the pay gate closed.
			RealizedReadable: ok && pnl.HasRealization,
		})
	}
	return chains, true, nil
}

// --- WarehouseHullSource (warehouse-dedicated hulls + parked waypoint) --------------------------

type warehouseHullSource struct {
	lister   warehouseContainerLister
	shipRepo navigation.ShipRepository
}

func newWarehouseHullSource(lister warehouseContainerLister, shipRepo navigation.ShipRepository) *warehouseHullSource {
	return &warehouseHullSource{lister: lister, shipRepo: shipRepo}
}

// WarehouseHulls returns the player's warehouse-dedicated hulls, each tagged with the waypoint of
// its running WAREHOUSE container (its parked/coverage location, "" when unplaced). Any read error
// fails the buy path closed — the pool must be known before a shortfall is sized.
func (s *warehouseHullSource) WarehouseHulls(ctx context.Context, playerID int) ([]fleetCmd.WarehouseHull, error) {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return nil, err
	}
	ships, err := s.shipRepo.FindAllByPlayer(ctx, pid)
	if err != nil {
		return nil, err // an unreadable fleet fails the buy path closed
	}
	parked, err := s.parkedWaypoints(ctx, playerID)
	if err != nil {
		return nil, err
	}
	var hulls []fleetCmd.WarehouseHull
	for _, sh := range ships {
		if sh.DedicatedFleet() != operationWarehouse {
			continue
		}
		hulls = append(hulls, fleetCmd.WarehouseHull{
			ShipSymbol:     sh.ShipSymbol(),
			ParkedWaypoint: parked[sh.ShipSymbol()],
		})
	}
	return hulls, nil
}

// parkedWaypoints maps each warehouse hull currently running a WAREHOUSE container to that
// container's waypoint — the hull's parked location. Hulls with no running warehouse container are
// absent (unplaced, "").
func (s *warehouseHullSource) parkedWaypoints(ctx context.Context, playerID int) (map[string]string, error) {
	models, err := s.lister.ListByStatus(ctx, container.ContainerStatusRunning, &playerID)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, m := range models {
		if m.ContainerType != string(container.ContainerTypeWarehouse) {
			continue
		}
		var cfg map[string]interface{}
		if m.Config != "" {
			if json.Unmarshal([]byte(m.Config), &cfg) != nil {
				continue
			}
		}
		ship, _ := cfg["ship_symbol"].(string)
		waypoint, _ := cfg["waypoint_symbol"].(string)
		if ship != "" && waypoint != "" {
			out[ship] = waypoint
		}
	}
	return out, nil
}

// --- WarehouseDispatchPort bridge (idle→start, stranded→stop) -----------------------------------

type warehouseDispatchBridge struct {
	dispatcher warehouseDispatcher
	shipRepo   navigation.ShipRepository
}

func newWarehouseDispatchBridge(dispatcher warehouseDispatcher, shipRepo navigation.ShipRepository) *warehouseDispatchBridge {
	return &warehouseDispatchBridge{dispatcher: dispatcher, shipRepo: shipRepo}
}

// DispatchWarehouse places a warehouse hull on a durable export waypoint. An IDLE hull is started
// directly (StartWarehouse navigates + parks it buffering the co-exported goods). A hull STRANDED
// on a retired chain (holding a warehouse container elsewhere) is un-stranded by stopping that
// container — StartWarehouse takes only idle hulls, so the freed hull is re-placed on the durable
// target on the next tick. A warehouse hull busy on a non-warehouse container is refused loudly
// rather than disturbed.
func (b *warehouseDispatchBridge) DispatchWarehouse(ctx context.Context, playerID int, shipSymbol, waypoint string, goods []string) error {
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return err
	}
	ship, err := b.shipRepo.FindBySymbol(ctx, shipSymbol, pid)
	if err != nil {
		return fmt.Errorf("warehouse dispatch: load %s: %w", shipSymbol, err)
	}
	if ship == nil {
		return fmt.Errorf("warehouse dispatch: ship %s not found", shipSymbol)
	}
	if ship.IsIdle() {
		_, serr := b.dispatcher.StartWarehouse(ctx, shipSymbol, waypoint, goods, playerID)
		return serr
	}
	containerID := ship.ContainerID()
	if !isWarehouseContainer(containerID) {
		return fmt.Errorf("warehouse dispatch: ship %s is busy on non-warehouse container %q — refusing to disturb", shipSymbol, containerID)
	}
	return b.dispatcher.StopContainer(containerID)
}

// isWarehouseContainer reports whether a container ID belongs to a warehouse operation. Warehouse
// container IDs are minted GenerateContainerID("warehouse", ship) → "warehouse-<ship>-<uuid>".
func isWarehouseContainer(containerID string) bool {
	return strings.HasPrefix(containerID, operationWarehouse+"-")
}
