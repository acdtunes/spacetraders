package routing

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"

	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/routing"
)

// A FALLBACK partition arrives as a SUCCESS carrying a usable assignment, and for
// a long time that was all it arrived as: when the VRP failed, the service split
// the work round-robin and said nothing. Charting crews of 10+ hulls ran that way
// in production for the whole era while the caller read them as solved (sp-ev79y).
// The response now carries the distinction and the client must hand it on, because
// a signal nothing reads is the same as no signal.

// stubPartitioner answers PartitionFleet with a fixed reply.
type stubPartitioner struct {
	pb.UnimplementedRoutingServiceServer
	reply *pb.PartitionFleetResponse
}

func (s *stubPartitioner) PartitionFleet(
	_ context.Context, _ *pb.PartitionFleetRequest,
) (*pb.PartitionFleetResponse, error) {
	return s.reply, nil
}

// servePartitioner runs the stub on a local port and returns its address.
func servePartitioner(t *testing.T, reply *pb.PartitionFleetResponse) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	server := grpc.NewServer()
	pb.RegisterRoutingServiceServer(server, &stubPartitioner{reply: reply})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return listener.Addr().String()
}

func partitionOnce(t *testing.T, reply *pb.PartitionFleetResponse) *domainRouting.VRPResponse {
	t.Helper()
	client, err := NewGRPCRoutingClient(servePartitioner(t, reply))
	if err != nil {
		t.Fatalf("unexpected constructor error: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.PartitionFleet(ctx, &domainRouting.VRPRequest{
		SystemSymbol:    "X1-TEST",
		ShipSymbols:     []string{"HULL-1", "HULL-2"},
		MarketWaypoints: []string{"X1-TEST-A", "X1-TEST-B"},
		ShipConfigs: map[string]*domainRouting.ShipConfigData{
			"HULL-1": {CurrentLocation: "X1-TEST-A", FuelCapacity: 400, EngineSpeed: 30},
			"HULL-2": {CurrentLocation: "X1-TEST-B", FuelCapacity: 400, EngineSpeed: 30},
		},
		AllWaypoints: []*system.WaypointData{
			{Symbol: "X1-TEST-A"}, {Symbol: "X1-TEST-B"},
		},
	})
	if err != nil {
		t.Fatalf("PartitionFleet returned an error: %v", err)
	}
	return resp
}

func twoHullReply(fallback bool, status string) *pb.PartitionFleetResponse {
	return &pb.PartitionFleetResponse{
		Success: true,
		Assignments: map[string]*pb.ShipTour{
			"HULL-1": {Waypoints: []string{"X1-TEST-A"}},
			"HULL-2": {Waypoints: []string{"X1-TEST-B"}},
		},
		Fallback:     fallback,
		SolverStatus: &status,
	}
}

func TestPartitionFleet_ReportsAFallbackAsAFallback(t *testing.T) {
	resp := partitionOnce(t, twoHullReply(true, "fallback:no-solution"))

	if !resp.Fallback {
		t.Fatal("a round-robin answer must reach the caller marked as a fallback")
	}
	if resp.SolverStatus != "fallback:no-solution" {
		t.Fatalf("solver status not carried through: got %q", resp.SolverStatus)
	}
	if len(resp.Assignments) != 2 {
		t.Fatalf("a fallback is still a usable partition: got %d shares", len(resp.Assignments))
	}
}

func TestPartitionFleet_ReportsASolvedAnswerAsSolved(t *testing.T) {
	resp := partitionOnce(t, twoHullReply(false, "solved"))

	if resp.Fallback {
		t.Fatal("a solved partition must not be reported as a fallback")
	}
	if resp.SolverStatus != "solved" {
		t.Fatalf("solver status not carried through: got %q", resp.SolverStatus)
	}
}
