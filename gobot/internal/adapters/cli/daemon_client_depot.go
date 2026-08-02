package cli

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// DepotElementDTO mirrors the protobuf DepotElement for CLI display and spec parsing.
// ShipSymbol may be empty (a declared-but-uncrewed slot). The json tags define the
// operator spec-file format the `depot apply` verb reads.
type DepotElementDTO struct {
	Waypoint   string `json:"waypoint"`
	ShipSymbol string `json:"ship_symbol"`
}

// DepotDTO mirrors the protobuf DepotSpec for CLI display and spec parsing.
type DepotDTO struct {
	ID            string            `json:"id"`
	Warehouses    []DepotElementDTO `json:"warehouses"`
	Stockers      []DepotElementDTO `json:"stockers"`
	DeliveryHulls []DepotElementDTO `json:"delivery_hulls"`
	SourceHubs    []DepotElementDTO `json:"source_hubs"`
}

// ApplyDepotTopology sends the whole-topology DECLARATIVE bulk apply. Returns the
// number of depots the daemon persisted.
func (c *DaemonClient) ApplyDepotTopology(ctx context.Context, playerID int, agentSymbol string, depots []DepotDTO) (int, error) {
	req := &pb.ApplyDepotTopologyRequest{PlayerId: int32(playerID), Depots: depotDTOsToProto(depots)}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.ApplyDepotTopology(ctx, req)
	if err != nil {
		return 0, fmt.Errorf(grpcCallFailed, err)
	}
	return int(resp.DepotCount), nil
}

// AddDepot adds one depot (granular).
func (c *DaemonClient) AddDepot(ctx context.Context, playerID int, agentSymbol string, spec DepotDTO) error {
	req := &pb.AddDepotRequest{PlayerId: int32(playerID), Depot: depotDTOToProto(spec)}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if _, err := c.client.AddDepot(ctx, req); err != nil {
		return fmt.Errorf(grpcCallFailed, err)
	}
	return nil
}

// RemoveDepot removes one depot by id (granular).
func (c *DaemonClient) RemoveDepot(ctx context.Context, playerID int, agentSymbol, depotID string) error {
	req := &pb.RemoveDepotRequest{PlayerId: int32(playerID), DepotId: depotID}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if _, err := c.client.RemoveDepot(ctx, req); err != nil {
		return fmt.Errorf(grpcCallFailed, err)
	}
	return nil
}

// AddDepotElement adds one element to a depot role (granular).
func (c *DaemonClient) AddDepotElement(ctx context.Context, playerID int, agentSymbol, depotID, role string, element depot.Element) error {
	req := &pb.AddDepotElementRequest{PlayerId: int32(playerID), DepotId: depotID, Role: role, Waypoint: element.Waypoint, ShipSymbol: element.ShipSymbol}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if _, err := c.client.AddDepotElement(ctx, req); err != nil {
		return fmt.Errorf(grpcCallFailed, err)
	}
	return nil
}

// RemoveDepotElement removes the element crewed by shipSymbol from a role (granular).
func (c *DaemonClient) RemoveDepotElement(ctx context.Context, playerID int, agentSymbol, depotID, role, shipSymbol string) error {
	req := &pb.RemoveDepotElementRequest{PlayerId: int32(playerID), DepotId: depotID, Role: role, ShipSymbol: shipSymbol}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if _, err := c.client.RemoveDepotElement(ctx, req); err != nil {
		return fmt.Errorf(grpcCallFailed, err)
	}
	return nil
}

// PlaceDepotElement repositions the element crewed by shipSymbol to a waypoint (granular).
func (c *DaemonClient) PlaceDepotElement(ctx context.Context, playerID int, agentSymbol, depotID, role string, element depot.Element) error {
	req := &pb.PlaceDepotElementRequest{PlayerId: int32(playerID), DepotId: depotID, Role: role, ShipSymbol: element.ShipSymbol, Waypoint: element.Waypoint}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	if _, err := c.client.PlaceDepotElement(ctx, req); err != nil {
		return fmt.Errorf(grpcCallFailed, err)
	}
	return nil
}

// ListDepots returns the player's persisted depots for CLI display.
func (c *DaemonClient) ListDepots(ctx context.Context, playerID int, agentSymbol string) ([]*DepotDTO, error) {
	req := &pb.ListDepotsRequest{PlayerId: int32(playerID)}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.ListDepots(ctx, req)
	if err != nil {
		return nil, fmt.Errorf(grpcCallFailed, err)
	}
	out := make([]*DepotDTO, 0, len(resp.Depots))
	for _, pc := range resp.Depots {
		out = append(out, protoToDepotDTO(pc))
	}
	return out, nil
}

// StartDepot persists one depot's topology and launches its coordinators in one shot
// (sp-38xc). Returns the number of coordinators launched.
func (c *DaemonClient) StartDepot(ctx context.Context, playerID int, agentSymbol string, spec DepotDTO) (int, error) {
	req := &pb.StartDepotRequest{PlayerId: int32(playerID), Depot: depotDTOToProto(spec)}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.StartDepot(ctx, req)
	if err != nil {
		return 0, fmt.Errorf(grpcCallFailed, err)
	}
	return int(resp.Launched), nil
}

// StopDepot tears down the named depot's running coordinators (sp-38xc). Returns the
// number of containers stopped.
func (c *DaemonClient) StopDepot(ctx context.Context, playerID int, agentSymbol, depotID string) (int, error) {
	req := &pb.StopDepotRequest{PlayerId: int32(playerID), DepotId: depotID}
	if agentSymbol != "" {
		req.AgentSymbol = &agentSymbol
	}
	resp, err := c.client.StopDepot(ctx, req)
	if err != nil {
		return 0, fmt.Errorf(grpcCallFailed, err)
	}
	return int(resp.Stopped), nil
}

func depotDTOsToProto(depots []DepotDTO) []*pb.DepotSpec {
	out := make([]*pb.DepotSpec, 0, len(depots))
	for _, c := range depots {
		out = append(out, depotDTOToProto(c))
	}
	return out
}

func depotDTOToProto(c DepotDTO) *pb.DepotSpec {
	return &pb.DepotSpec{
		Id:            c.ID,
		Warehouses:    depotElementDTOsToProto(c.Warehouses),
		Stockers:      depotElementDTOsToProto(c.Stockers),
		DeliveryHulls: depotElementDTOsToProto(c.DeliveryHulls),
		SourceHubs:    depotElementDTOsToProto(c.SourceHubs),
	}
}

func depotElementDTOsToProto(es []DepotElementDTO) []*pb.DepotElement {
	if len(es) == 0 {
		return nil
	}
	out := make([]*pb.DepotElement, 0, len(es))
	for _, e := range es {
		out = append(out, &pb.DepotElement{Waypoint: e.Waypoint, ShipSymbol: e.ShipSymbol})
	}
	return out
}

func protoToDepotDTO(pc *pb.DepotSpec) *DepotDTO {
	return &DepotDTO{
		ID:            pc.Id,
		Warehouses:    protoToDepotElementDTOs(pc.Warehouses),
		Stockers:      protoToDepotElementDTOs(pc.Stockers),
		DeliveryHulls: protoToDepotElementDTOs(pc.DeliveryHulls),
		SourceHubs:    protoToDepotElementDTOs(pc.SourceHubs),
	}
}

func protoToDepotElementDTOs(pes []*pb.DepotElement) []DepotElementDTO {
	out := make([]DepotElementDTO, 0, len(pes))
	for _, pe := range pes {
		out = append(out, DepotElementDTO{Waypoint: pe.Waypoint, ShipSymbol: pe.ShipSymbol})
	}
	return out
}
