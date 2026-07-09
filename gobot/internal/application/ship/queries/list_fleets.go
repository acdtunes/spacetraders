package queries

import (
	"context"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// ListFleetsQuery lists every dedicated fleet and its member ships (sp-l7h2).
// Fleets exist implicitly as the distinct non-empty DedicatedFleet tags across
// the player's ships — there is no fleet table; an empty result means no ship
// is dedicated to anything.
type ListFleetsQuery struct {
	PlayerID    *int   // Optional: query by player ID
	AgentSymbol string // Optional: query by agent symbol
}

// FleetShipInfo is one member ship of a fleet.
type FleetShipInfo struct {
	ShipSymbol string
	Idle       bool // No active assignment and not in transit — claimable now
}

// FleetInfo is one named fleet and its members, sorted by ship symbol.
type FleetInfo struct {
	Name  string
	Ships []FleetShipInfo
}

// ListFleetsResponse carries every fleet, sorted by name for stable output.
type ListFleetsResponse struct {
	Fleets []FleetInfo
}

// ListFleetsHandler handles the ListFleets query.
type ListFleetsHandler struct {
	shipRepo       navigation.ShipRepository
	playerResolver *common.PlayerResolver
}

// NewListFleetsHandler creates a new ListFleetsHandler.
func NewListFleetsHandler(shipRepo navigation.ShipRepository, playerRepo player.PlayerRepository) *ListFleetsHandler {
	return &ListFleetsHandler{
		shipRepo:       shipRepo,
		playerResolver: common.NewPlayerResolver(playerRepo),
	}
}

// Handle executes the ListFleets query.
func (h *ListFleetsHandler) Handle(ctx context.Context, request common.Request) (common.Response, error) {
	query, ok := request.(*ListFleetsQuery)
	if !ok {
		return nil, fmt.Errorf("invalid request type: expected *ListFleetsQuery, got %T", request)
	}

	playerID, err := h.playerResolver.ResolvePlayerID(ctx, query.PlayerID, query.AgentSymbol)
	if err != nil {
		return nil, err
	}

	ships, err := h.shipRepo.FindAllByPlayer(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list ships: %w", err)
	}

	byFleet := make(map[string][]FleetShipInfo)
	for _, ship := range ships {
		fleet := ship.DedicatedFleet()
		if fleet == "" {
			continue
		}
		idle := ship.IsIdle() && ship.NavStatus() != navigation.NavStatusInTransit
		byFleet[fleet] = append(byFleet[fleet], FleetShipInfo{
			ShipSymbol: ship.ShipSymbol(),
			Idle:       idle,
		})
	}

	fleets := make([]FleetInfo, 0, len(byFleet))
	for name, members := range byFleet {
		sort.Slice(members, func(i, j int) bool { return members[i].ShipSymbol < members[j].ShipSymbol })
		fleets = append(fleets, FleetInfo{Name: name, Ships: members})
	}
	sort.Slice(fleets, func(i, j int) bool { return fleets[i].Name < fleets[j].Name })

	return &ListFleetsResponse{Fleets: fleets}, nil
}
