package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/cluster"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// This file is the gRPC service surface for contract-cluster management (bead sp-u9xa):
// it resolves the player, translates the proto request into the grpc-package spec DTOs,
// and delegates to the DaemonServer handlers (container_ops_cluster.go), which drive the
// application Store. Pure translation — the durable behaviour lives one layer down.

// ApplyClusterTopology (mode A, declarative bulk) makes the persisted set exactly the
// requested clusters.
func (s *daemonServiceImpl) ApplyClusterTopology(ctx context.Context, req *pb.ApplyClusterTopologyRequest) (*pb.ApplyClusterTopologyResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	spec := ClusterTopologySpec{Clusters: protoClustersToSpecs(req.Clusters)}
	if err := s.daemon.ApplyClusterTopology(ctx, playerID, spec); err != nil {
		return nil, fmt.Errorf("failed to apply cluster topology: %w", err)
	}
	return &pb.ApplyClusterTopologyResponse{Status: "APPLIED", ClusterCount: int32(len(spec.Clusters))}, nil
}

// AddCluster (mode B, granular) adds one cluster.
func (s *daemonServiceImpl) AddCluster(ctx context.Context, req *pb.AddClusterRequest) (*pb.AddClusterResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if req.Cluster == nil {
		return nil, fmt.Errorf("add cluster: missing cluster spec")
	}
	if err := s.daemon.AddCluster(ctx, playerID, protoClusterToSpec(req.Cluster)); err != nil {
		return nil, fmt.Errorf("failed to add cluster: %w", err)
	}
	return &pb.AddClusterResponse{Status: "ADDED"}, nil
}

// RemoveCluster (mode B, granular) removes one cluster by id.
func (s *daemonServiceImpl) RemoveCluster(ctx context.Context, req *pb.RemoveClusterRequest) (*pb.RemoveClusterResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if err := s.daemon.RemoveCluster(ctx, playerID, req.ClusterId); err != nil {
		return nil, fmt.Errorf("failed to remove cluster: %w", err)
	}
	return &pb.RemoveClusterResponse{Status: "REMOVED"}, nil
}

// AddClusterElement (mode B, granular) adds one element to a cluster role.
func (s *daemonServiceImpl) AddClusterElement(ctx context.Context, req *pb.AddClusterElementRequest) (*pb.ClusterElementResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if err := s.daemon.AddClusterElement(ctx, playerID, req.ClusterId, req.Role, req.Waypoint, req.ShipSymbol); err != nil {
		return nil, fmt.Errorf("failed to add cluster element: %w", err)
	}
	return &pb.ClusterElementResponse{Status: "ADDED"}, nil
}

// RemoveClusterElement (mode B, granular) removes the element crewed by a ship.
func (s *daemonServiceImpl) RemoveClusterElement(ctx context.Context, req *pb.RemoveClusterElementRequest) (*pb.ClusterElementResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if err := s.daemon.RemoveClusterElement(ctx, playerID, req.ClusterId, req.Role, req.ShipSymbol); err != nil {
		return nil, fmt.Errorf("failed to remove cluster element: %w", err)
	}
	return &pb.ClusterElementResponse{Status: "REMOVED"}, nil
}

// PlaceClusterElement (mode B, granular) repositions an element to a waypoint.
func (s *daemonServiceImpl) PlaceClusterElement(ctx context.Context, req *pb.PlaceClusterElementRequest) (*pb.ClusterElementResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	if err := s.daemon.PlaceClusterElement(ctx, playerID, req.ClusterId, req.Role, req.ShipSymbol, req.Waypoint); err != nil {
		return nil, fmt.Errorf("failed to place cluster element: %w", err)
	}
	return &pb.ClusterElementResponse{Status: "PLACED"}, nil
}

// ListClusters returns the player's persisted clusters as proto specs.
func (s *daemonServiceImpl) ListClusters(ctx context.Context, req *pb.ListClustersRequest) (*pb.ListClustersResponse, error) {
	playerID, err := s.resolvePlayerID(ctx, req.PlayerId, req.AgentSymbol)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve player: %w", err)
	}
	clusters, err := s.daemon.ListClusters(ctx, playerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}
	return &pb.ListClustersResponse{Clusters: domainClustersToProto(clusters)}, nil
}

// --- proto <-> spec/domain translation ---

func protoClustersToSpecs(pcs []*pb.ClusterSpec) []ClusterSpec {
	out := make([]ClusterSpec, 0, len(pcs))
	for _, pc := range pcs {
		if pc == nil {
			continue
		}
		out = append(out, protoClusterToSpec(pc))
	}
	return out
}

func protoClusterToSpec(pc *pb.ClusterSpec) ClusterSpec {
	return ClusterSpec{
		ID:            pc.Id,
		Warehouses:    protoElementsToSpecs(pc.Warehouses),
		Stockers:      protoElementsToSpecs(pc.Stockers),
		DeliveryHulls: protoElementsToSpecs(pc.DeliveryHulls),
		SourceHubs:    protoElementsToSpecs(pc.SourceHubs),
	}
}

func protoElementsToSpecs(pes []*pb.ClusterElement) []ElementSpec {
	if len(pes) == 0 {
		return nil
	}
	out := make([]ElementSpec, 0, len(pes))
	for _, pe := range pes {
		if pe == nil {
			continue
		}
		out = append(out, ElementSpec{Waypoint: pe.Waypoint, ShipSymbol: pe.ShipSymbol})
	}
	return out
}

func domainClustersToProto(clusters []*cluster.ContractCluster) []*pb.ClusterSpec {
	out := make([]*pb.ClusterSpec, 0, len(clusters))
	for _, c := range clusters {
		out = append(out, &pb.ClusterSpec{
			Id:            c.ID(),
			Warehouses:    domainElementsToProto(c.Warehouses()),
			Stockers:      domainElementsToProto(c.Stockers()),
			DeliveryHulls: domainElementsToProto(c.DeliveryHulls()),
			SourceHubs:    domainElementsToProto(c.SourceHubs()),
		})
	}
	return out
}

func domainElementsToProto(es []cluster.Element) []*pb.ClusterElement {
	if len(es) == 0 {
		return nil
	}
	out := make([]*pb.ClusterElement, 0, len(es))
	for _, e := range es {
		out = append(out, &pb.ClusterElement{Waypoint: e.Waypoint, ShipSymbol: e.ShipSymbol})
	}
	return out
}
