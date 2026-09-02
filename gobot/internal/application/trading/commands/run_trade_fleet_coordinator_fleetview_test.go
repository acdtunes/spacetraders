package commands

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// tfIdleShip builds a parked (idle, in-orbit) hull pinned to the given dedicated_fleet
// tag — the fleet-view partition's only two inputs. Empty fleet models an UNPINNED hull
// (the captain's per-hull opt-out).
func tfIdleShip(t *testing.T, symbol, fleet string) *navigation.Ship {
	t.Helper()
	ship := tradeHull(t, symbol)
	ship.SetDedicatedFleet(fleet)
	return ship
}

func TestPartitionTradeFleet_AcceptsAllTradeTags(t *testing.T) {
	ships := []*navigation.Ship{
		tfIdleShip(t, "T-OLD", "trade"),
		tfIdleShip(t, "T-MVT", "trade-mvt"),
		tfIdleShip(t, "T-LANE", "trade-lane"),
		tfIdleShip(t, "T-OTHER", "contract"),
		tfIdleShip(t, "T-NONE", ""),
	}
	idle, running := partitionTradeFleet(ships)
	if len(running) != 0 {
		t.Fatalf("running = %d", len(running))
	}
	got := map[string]bool{}
	for _, s := range idle {
		got[s.ShipSymbol()] = true
	}
	if !got["T-OLD"] || !got["T-MVT"] || !got["T-LANE"] || got["T-OTHER"] || got["T-NONE"] {
		t.Fatalf("idle = %v", got)
	}
}
