package expansion

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// heavyYardInventory is the narrow shipyard-inventory slice the objective reads: whether ANY
// heavy-freighter yard has been discovered this era. Satisfied by the inventory repo.
type heavyYardInventory interface {
	HasAnyOfTypes(ctx context.Context, playerID int, shipTypes []string) (bool, error)
}

// heavyLaneCounter is the profitable-heavy-lane count across systems (the autosizer's
// unserved-demand basis). Satisfied by *trading/queries.ProfitableLaneReader — read-only, off the
// persisted market cache, so the objective read never perturbs the trade coordinator.
type heavyLaneCounter interface {
	CountProfitableLanes(ctx context.Context, playerID int, systems []string) (int, bool, error)
}

// heavyFleetReader lists the player's hulls (their systems seed the lane count; the trade-dedicated
// subset is subtracted from the profitable lanes to yield the shortfall). Satisfied by the ship repo.
type heavyFleetReader interface {
	FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error)
}

// heavyDedicatedFleet is the dedication tag heavy trade hulls carry (autosizerDedicatedFleet's
// heavy case). A hull under this tag is heavy capacity already in the fleet.
const heavyDedicatedFleet = "trade"

// DepthObjectiveReader is the frontier depth slice's deep-resource objective: it
// combines the autosizer's heavy capacity shortfall (profitable-but-unflown lanes beyond
// the current heavy pool) with the shipyard-inventory yard-known predicate. The depth
// slice biases toward depth while the fleet needs heavies (shortfall > 0) AND cannot yet buy them
// (no heavy yard discovered) — punch outward to FIND the yard — and relaxes once a yard is known.
// Every read fails SAFE to "no bias" (readable=false): this shifts a policy split, never a spend,
// so an unreadable signal simply leaves the baseline split standing. It implements the coordinator's
// commands.DepthObjectiveReader driven port.
type DepthObjectiveReader struct {
	inventory  heavyYardInventory
	lanes      heavyLaneCounter
	fleet      heavyFleetReader
	heavyTypes []string
}

// NewDepthObjectiveReader wires the objective over the shipyard-inventory yard-known predicate, the
// profitable-heavy-lane counter, and the fleet reader. The heavy ship-type set is the configured
// default (SHIP_HEAVY_FREIGHTER / SHIP_BULK_FREIGHTER).
func NewDepthObjectiveReader(inventory heavyYardInventory, lanes heavyLaneCounter, fleet heavyFleetReader) *DepthObjectiveReader {
	return &DepthObjectiveReader{
		inventory:  inventory,
		lanes:      lanes,
		fleet:      fleet,
		heavyTypes: shipyard.DefaultHeavyShipTypes,
	}
}

// HeavyYardObjective returns the heavy capacity shortfall, whether a heavy yard is known, and
// whether the whole signal is readable. Any read failure yields readable=false so the caller
// applies no depth bias (fail-safe).
func (r *DepthObjectiveReader) HeavyYardObjective(ctx context.Context, playerID int) (int, bool, bool, error) {
	yardKnown, err := r.inventory.HasAnyOfTypes(ctx, playerID, r.heavyTypes)
	if err != nil {
		return 0, false, false, nil
	}
	pid, err := shared.NewPlayerID(playerID)
	if err != nil {
		return 0, false, false, nil
	}
	ships, err := r.fleet.FindAllByPlayer(ctx, pid)
	if err != nil {
		return 0, false, false, nil
	}
	lanes, readable, err := r.lanes.CountProfitableLanes(ctx, playerID, distinctSystemsOf(ships))
	if err != nil || !readable {
		return 0, false, false, nil
	}
	return heavyShortfall(lanes, ships), yardKnown, true, nil
}

// heavyShortfall is the profitable heavy lanes beyond the current trade-dedicated pool, floored at 0.
func heavyShortfall(profitableLanes int, ships []*navigation.Ship) int {
	shortfall := profitableLanes - countHeavies(ships)
	if shortfall < 0 {
		return 0
	}
	return shortfall
}

// countHeavies counts the trade-dedicated (heavy) hulls in the fleet.
func countHeavies(ships []*navigation.Ship) int {
	n := 0
	for _, ship := range ships {
		if ship.DedicatedFleet() == heavyDedicatedFleet {
			n++
		}
	}
	return n
}

// distinctSystemsOf returns the distinct systems the fleet occupies — the trading grounds the
// profitable-lane count scans (mirrors the autosizer's system discovery).
func distinctSystemsOf(ships []*navigation.Ship) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ships))
	for _, ship := range ships {
		location := ship.CurrentLocation()
		if location == nil {
			continue
		}
		systemSymbol := shared.ExtractSystemSymbol(location.Symbol)
		if seen[systemSymbol] {
			continue
		}
		seen[systemSymbol] = true
		out = append(out, systemSymbol)
	}
	return out
}
