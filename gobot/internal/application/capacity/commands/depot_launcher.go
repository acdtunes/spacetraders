package commands

import "context"

// The reuse-path depot LAUNCH seam. When the reconciler reuses idle hulls into a hub's
// depot roles it must not merely re-tag the dedication — it must LAUNCH the depot so the
// reused hulls orbit and operate (and the hub becomes covered, closing the gap the
// autosizer would otherwise keep buying against). These neutral DTOs let the application
// layer express "launch this hub's depot" without depending on the grpc DepotSpec; the
// grpc adapter (which owns the StartDepot lifecycle) translates.

// DepotLaunchElement is one crewed depot member — a {waypoint, ship} pair the reuse path
// built from a tier-1 reassign (the ship is the reused idle hull; the waypoint its
// role's target, defaulting to the hub).
type DepotLaunchElement struct {
	Waypoint   string
	ShipSymbol string
}

// DepotLaunchSpec is one hub's fully-reuse-staffed depot: its hub symbol (the stable
// depot identity) and each role's reused hulls. It is complete-hub-first by
// construction — the reconciler only builds one when the reassigns satisfy the hub's
// desired warehouse+stocker+worker counts, so a launched depot is always functional.
type DepotLaunchSpec struct {
	HubSymbol     string
	Warehouses    []DepotLaunchElement
	Stockers      []DepotLaunchElement
	DeliveryHulls []DepotLaunchElement
}

// DepotLauncher persists a hub's depot and idempotently launches its coordinators — the
// same live-activation the buy path uses (the StartDepot lifecycle: upsert + idle-gated
// coordinator launch). Optional-injection on the reconciler handler: a nil launcher is
// byte-identical OFF (the reuse path re-tags but does not launch, as before).
// Restart-safe by construction — depots persist and the boot reload re-adopts running
// coordinators without a double-launch.
type DepotLauncher interface {
	LaunchDepot(ctx context.Context, playerID int, spec DepotLaunchSpec) error
}
