package queries

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// YardCandidate is one reachable shipyard selling a requested ship type,
// ranked by gate-graph hop distance first, then price (sp-42ow) — the
// "nearest reachable heavy yard" signal the fleet autosizer's fail-closed
// heavy branch consumes.
type YardCandidate struct {
	SystemSymbol   string
	WaypointSymbol string
	ShipType       string
	PurchasePrice  int
	// Hops is the minimum gate-jump count from the NEAREST of the caller's
	// reference systems (0 = a yard inside one of them).
	Hops        int
	LastScanned int64 // unix seconds, for freshness display/telemetry
}

// gateAdjacencyReader is the narrow gate-graph slice the ranking needs: the
// stored, era-scoped adjacency (a pure store read — NO fetch-through, so an
// autosizer tick never spends API budget ranking yards). Satisfied by
// *gategraph.Service (and by the underlying gate-edge repository).
type gateAdjacencyReader interface {
	Adjacency(ctx context.Context) (map[string][]system.GateEdge, error)
}

// inventoryByTypeReader is the narrow shipyard-inventory slice the ranking
// needs. Satisfied by *persistence.ShipyardInventoryRepositoryGORM.
type inventoryByTypeReader interface {
	ListByTypes(ctx context.Context, playerID int, shipTypes []string) ([]shipyard.ShipTypeAvailability, error)
}

// ReachableYardFinder ranks scanned shipyards selling the requested ship types
// by hop distance from the caller's reference systems, then by price (sp-42ow).
// Distances run over the PERSISTED gate adjacency with under-construction
// edges excluded (the gategraph BFS semantics), bounded to gategraph.MaxJumpPath
// — the reach heavies are held to. The rank is a SIGNAL: the actual purchase
// path still runs its own strict pre-buy routability guard before any spend,
// so an optimistic rank can never buy into an unroutable yard. An empty scan
// store yields an empty rank (the autosizer's price guard then fails closed
// exactly as before this seam existed).
type ReachableYardFinder struct {
	inventory inventoryByTypeReader
	gates     gateAdjacencyReader
	maxJumps  int
}

// NewReachableYardFinder wires the finder over the scan store and the stored
// gate adjacency, bounded to the strict heavy-class reach (gategraph.MaxJumpPath).
func NewReachableYardFinder(inventory inventoryByTypeReader, gates gateAdjacencyReader) *ReachableYardFinder {
	return &ReachableYardFinder{inventory: inventory, gates: gates, maxJumps: gategraph.MaxJumpPath}
}

// NearestYardsSelling returns the PRICED scanned yards selling any of shipTypes,
// reachable within the jump bound from fromSystems, ranked hops-then-price
// (ties broken by waypoint then ship type for a deterministic rank). Unpriced
// rows (availability known, price 0) are excluded — they can prove discovery
// but never feed a price guard.
func (f *ReachableYardFinder) NearestYardsSelling(ctx context.Context, playerID int, shipTypes []string, fromSystems []string) ([]YardCandidate, error) {
	if len(shipTypes) == 0 || len(fromSystems) == 0 {
		return nil, nil
	}
	rows, err := f.inventory.ListByTypes(ctx, playerID, shipTypes)
	if err != nil {
		return nil, fmt.Errorf("reachable yards: failed to read shipyard inventory: %w", err)
	}
	priced := pricedRows(rows)
	if len(priced) == 0 {
		return nil, nil
	}

	hops, err := f.hopDistances(ctx, fromSystems)
	if err != nil {
		return nil, err
	}

	candidates := make([]YardCandidate, 0, len(priced))
	for _, row := range priced {
		distance, reachable := hops[row.SystemSymbol]
		if !reachable {
			continue
		}
		candidates = append(candidates, YardCandidate{
			SystemSymbol:   row.SystemSymbol,
			WaypointSymbol: row.WaypointSymbol,
			ShipType:       row.ShipType,
			PurchasePrice:  row.PurchasePrice,
			Hops:           distance,
			LastScanned:    row.LastScanned.Unix(),
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.Hops != b.Hops {
			return a.Hops < b.Hops
		}
		if a.PurchasePrice != b.PurchasePrice {
			return a.PurchasePrice < b.PurchasePrice
		}
		if a.WaypointSymbol != b.WaypointSymbol {
			return a.WaypointSymbol < b.WaypointSymbol
		}
		return a.ShipType < b.ShipType
	})
	return candidates, nil
}

// pricedRows filters out availability-only rows (price 0).
func pricedRows(rows []shipyard.ShipTypeAvailability) []shipyard.ShipTypeAvailability {
	out := make([]shipyard.ShipTypeAvailability, 0, len(rows))
	for _, r := range rows {
		if r.PurchasePrice > 0 {
			out = append(out, r)
		}
	}
	return out
}

// hopDistances runs ONE multi-source BFS from all reference systems over the
// stored gate adjacency, returning each reachable system's minimum jump count
// (0 for a reference system itself), bounded to f.maxJumps. Edges whose
// neighbor gate is UNDER CONSTRUCTION are never traversed — the same forward
// filter both gategraph.Path and RepositionPath apply (sp-8qhu: a jump into an
// unbuilt gate crashes at hop time). A store read failure surfaces as an error
// (fail closed), never as an empty "nothing reachable".
func (f *ReachableYardFinder) hopDistances(ctx context.Context, fromSystems []string) (map[string]int, error) {
	adjacency, err := f.gates.Adjacency(ctx)
	if err != nil {
		return nil, fmt.Errorf("reachable yards: failed to read stored gate adjacency: %w", err)
	}
	distances := make(map[string]int, len(adjacency))
	queue := make([]string, 0, len(fromSystems))
	for _, s := range fromSystems {
		if s == "" {
			continue
		}
		if _, seen := distances[s]; seen {
			continue
		}
		distances[s] = 0
		queue = append(queue, s)
	}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if distances[current] >= f.maxJumps {
			continue
		}
		for _, edge := range adjacency[current] {
			if edge.UnderConstruction {
				continue
			}
			neighbor := edge.ConnectedSystem
			if neighbor == "" {
				continue
			}
			if _, seen := distances[neighbor]; seen {
				continue
			}
			distances[neighbor] = distances[current] + 1
			queue = append(queue, neighbor)
		}
	}
	return distances, nil
}
