package grpc

import (
	"context"
	"fmt"

	shipQuery "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	shipyardQuery "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// ListShips handles ship listing requests
func (s *DaemonServer) ListShips(ctx context.Context, playerID *int, agentSymbol string) ([]*pb.ShipInfo, error) {
	// Create query
	query := &shipQuery.ListShipsQuery{
		PlayerID:    playerID,
		AgentSymbol: agentSymbol,
	}

	// Execute via mediator
	response, err := s.mediator.Send(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list ships: %w", err)
	}

	// Convert response
	listResp, ok := response.(*shipQuery.ListShipsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type")
	}

	// Convert domain ships to proto ships
	var ships []*pb.ShipInfo
	for _, domainShip := range listResp.Ships {
		ships = append(ships, &pb.ShipInfo{
			Symbol:        domainShip.ShipSymbol(),
			Location:      domainShip.CurrentLocation().Symbol,
			NavStatus:     string(domainShip.NavStatus()),
			FuelCurrent:   int32(domainShip.Fuel().Current),
			FuelCapacity:  int32(domainShip.Fuel().Capacity),
			CargoUnits:    int32(domainShip.CargoUnits()),
			CargoCapacity: int32(domainShip.CargoCapacity()),
			EngineSpeed:   int32(domainShip.EngineSpeed()),
		})
	}

	return ships, nil
}

// GetShip handles ship detail requests
func (s *DaemonServer) GetShip(ctx context.Context, shipSymbol string, playerID *int, agentSymbol string) (*pb.ShipDetail, error) {
	// Create query
	query := &shipQuery.GetShipQuery{
		ShipSymbol:  shipSymbol,
		PlayerID:    playerID,
		AgentSymbol: agentSymbol,
	}

	// Execute via mediator
	response, err := s.mediator.Send(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to get ship: %w", err)
	}

	// Convert response
	getResp, ok := response.(*shipQuery.GetShipResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type")
	}

	domainShip := getResp.Ship

	// Convert cargo items
	var cargoItems []*pb.CargoItem
	for _, item := range domainShip.Cargo().Inventory {
		cargoItems = append(cargoItems, &pb.CargoItem{
			Symbol: item.Symbol,
			Name:   item.Name,
			Units:  int32(item.Units),
		})
	}

	// Build ship detail
	shipDetail := &pb.ShipDetail{
		Symbol:         domainShip.ShipSymbol(),
		Location:       domainShip.CurrentLocation().Symbol,
		NavStatus:      string(domainShip.NavStatus()),
		FuelCurrent:    int32(domainShip.Fuel().Current),
		FuelCapacity:   int32(domainShip.Fuel().Capacity),
		CargoUnits:     int32(domainShip.CargoUnits()),
		CargoCapacity:  int32(domainShip.CargoCapacity()),
		CargoInventory: cargoItems,
		EngineSpeed:    int32(domainShip.EngineSpeed()),
		Role:           domainShip.Role(),
	}

	return shipDetail, nil
}

// GetShipyardListings retrieves available ships at a shipyard
func (s *DaemonServer) GetShipyardListings(ctx context.Context, systemSymbol, waypointSymbol string, playerID *int, agentSymbol string) ([]*pb.ShipListing, string, int32, error) {
	// Require player ID for now (agent symbol resolution can be added later)
	if playerID == nil || *playerID == 0 {
		return nil, "", 0, fmt.Errorf("player_id is required")
	}

	// Create query
	query := &shipyardQuery.GetShipyardListingsQuery{
		SystemSymbol:   systemSymbol,
		WaypointSymbol: waypointSymbol,
		PlayerID:       shared.MustNewPlayerID(*playerID),
	}

	// Execute via mediator
	response, err := s.mediator.Send(ctx, query)
	if err != nil {
		return nil, "", 0, fmt.Errorf("failed to get shipyard listings: %w", err)
	}

	// Convert response
	listingsResp, ok := response.(*shipyardQuery.GetShipyardListingsResponse)
	if !ok {
		return nil, "", 0, fmt.Errorf("unexpected response type")
	}

	// Convert to protobuf format
	listings := make([]*pb.ShipListing, len(listingsResp.Shipyard.Listings))
	for i, listing := range listingsResp.Shipyard.Listings {
		listings[i] = &pb.ShipListing{
			ShipType:      listing.ShipType,
			Name:          listing.Name,
			Description:   listing.Description,
			PurchasePrice: int32(listing.PurchasePrice),
		}
	}

	return listings, listingsResp.Shipyard.Symbol, int32(listingsResp.Shipyard.ModificationFee), nil
}
