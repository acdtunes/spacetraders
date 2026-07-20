package grpc

import (
	"context"

	capacityCmd "github.com/andrescamacho/spacetraders-go/internal/application/capacity/commands"
)

// This file is the reuse-path depot LAUNCH bridge: it adapts the capacity reconciler's
// DepotLauncher port to the StartDepot lifecycle, so when the reconciler reuses idle
// hulls to FULLY staff a hub's depot roles those hulls actually operate instead of
// sitting dedicated-but-idle. It is the reuse-path twin of the autosizer's buy-path
// StartWarehouse bridge: the reconciler decides WHICH complete hub to launch
// (complete-hub-first); StartDepot does the durable activation — persist the depot
// (upsert, so a re-launch never duplicates) + idempotently launch its coordinators (the
// idle-gate skips any already-running one). No business logic lives here — only the
// neutral-DTO → grpc DepotSpec translation. The daemon is the single writer of ship state
// (RULINGS #3), so the launch is dispatched through it, never CLI-side.

var _ capacityCmd.DepotLauncher = capacityReconcilerDepotLauncher{}

// capacityReconcilerDepotLauncher launches a reuse-staffed depot through the daemon's
// StartDepot lifecycle.
type capacityReconcilerDepotLauncher struct {
	server *DaemonServer
}

// NewCapacityReconcilerDepotLauncher wires the reuse-path depot launcher over the daemon.
// Returned as the port interface so main injects it with SetDepotLauncher; a nil launcher
// (never wired) leaves the reuse path byte-identical.
func NewCapacityReconcilerDepotLauncher(server *DaemonServer) capacityCmd.DepotLauncher {
	return capacityReconcilerDepotLauncher{server: server}
}

// LaunchDepot persists + idempotently launches the hub's depot coordinators via the
// StartDepot lifecycle.
func (l capacityReconcilerDepotLauncher) LaunchDepot(ctx context.Context, playerID int, spec capacityCmd.DepotLaunchSpec) error {
	_, err := l.server.StartDepot(ctx, playerID, reuseDepotSpec(spec))
	return err
}

// reuseDepotSpec maps the neutral reuse-path spec onto the grpc DepotSpec: the hub symbol
// is the stable depot identity (one depot per hub, so a re-launch upserts), and each
// role's reused hulls become the matching element class — the capacity "worker" is the
// depot DELIVERY hull. Source hubs are config-only and not part of a reuse-staffed depot.
func reuseDepotSpec(spec capacityCmd.DepotLaunchSpec) DepotSpec {
	return DepotSpec{
		ID:            spec.HubSymbol,
		Warehouses:    reuseElementSpecs(spec.Warehouses),
		Stockers:      reuseElementSpecs(spec.Stockers),
		DeliveryHulls: reuseElementSpecs(spec.DeliveryHulls),
	}
}

func reuseElementSpecs(elements []capacityCmd.DepotLaunchElement) []ElementSpec {
	if len(elements) == 0 {
		return nil
	}
	out := make([]ElementSpec, len(elements))
	for i, element := range elements {
		out[i] = ElementSpec{Waypoint: element.Waypoint, ShipSymbol: element.ShipSymbol}
	}
	return out
}
