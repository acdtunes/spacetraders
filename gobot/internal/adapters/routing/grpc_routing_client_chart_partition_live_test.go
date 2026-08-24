package routing

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// The charting crew's partition is decided by the LIVE routing service, so the
// property it rests on — disjoint shares covering every outstanding stop — has to
// be checked against the real solver and not only against a recorded answer. The
// engine's own tests fake the response; this one does not, which is the only way
// to catch the solver changing shape underneath the caller.
//
// SKIP-IF-DOWN, deliberately. The routing service is a separate process, so a
// developer machine without it running must not fail the suite; a hard failure
// here would also couple the merge gate to a service the gate does not start. The
// skip is the honest reading of "cannot be checked", not of "checked and fine".

// liveRoutingAddr is the running routing service, or "" when nothing answers.
func liveRoutingAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("ROUTING_ADDRESS")
	if addr == "" {
		addr = "localhost:50051"
	}
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return ""
	}
	_ = conn.Close()
	return addr
}

// darkSystemStops is a dark system's outstanding waypoints laid evenly around its
// centre — the one shape whose partition can be judged for BALANCE as well as for
// disjointness, since no share is geographically privileged.
func darkSystemStops() ([]string, []*system.WaypointData) {
	coords := [][2]float64{
		{240, 0}, {170, 170}, {0, 240}, {-170, 170},
		{-240, 0}, {-170, -170}, {0, -240}, {170, -170},
	}
	symbols := []string{
		"X1-DARK-A1", "X1-DARK-A2", "X1-DARK-A3", "X1-DARK-A4",
		"X1-DARK-B1", "X1-DARK-B2", "X1-DARK-B3", "X1-DARK-B4",
	}
	waypoints := make([]*system.WaypointData, 0, len(symbols))
	for i, symbol := range symbols {
		waypoints = append(waypoints, &system.WaypointData{Symbol: symbol, X: coords[i][0], Y: coords[i][1]})
	}
	return symbols, waypoints
}

func TestGRPCRoutingClient_LivePartitionOfADarkSystemIsAPartition(t *testing.T) {
	addr := liveRoutingAddr(t)
	if addr == "" {
		t.Skip("no routing service listening; the live partition cannot be checked")
	}
	client, err := NewGRPCRoutingClient(addr)
	if err != nil {
		t.Fatalf("unexpected constructor error: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	stops, waypoints := darkSystemStops()
	crew := []string{"PROBE-A", "PROBE-B", "PROBE-C"}
	configs := map[string]*domainRouting.ShipConfigData{}
	for _, ship := range crew {
		// Every hull on the system's gate, which is the shape a crew arrives in.
		configs[ship] = &domainRouting.ShipConfigData{
			CurrentLocation: "X1-DARK-GATE", FuelCapacity: 400, EngineSpeed: 30,
		}
	}

	// Generous, because the service runs its own search budget to the end; the
	// engine bounds the same call for the same reason.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	resp, err := client.PartitionFleet(ctx, &domainRouting.VRPRequest{
		SystemSymbol:    "X1-DARK",
		ShipSymbols:     crew,
		MarketWaypoints: stops,
		ShipConfigs:     configs,
		AllWaypoints:    waypoints,
	})
	if err != nil {
		t.Fatalf("the live routing service refused to partition a dark system: %v", err)
	}

	aboard := map[string]bool{}
	for _, ship := range crew {
		aboard[ship] = true
	}
	owner := map[string]string{}
	for ship, tour := range resp.Assignments {
		if !aboard[ship] {
			t.Fatalf("the partition gives work to %q, which is not on the crew", ship)
		}
		for _, waypoint := range tour.Waypoints {
			if held, taken := owner[waypoint]; taken {
				t.Fatalf("%s is owned by both %s and %s — two hulls charting one waypoint", waypoint, held, ship)
			}
			owner[waypoint] = ship
		}
	}
	if len(owner) != len(stops) {
		t.Fatalf("the crew owns %d of %d stops, want every one — an unowned waypoint never gets charted",
			len(owner), len(stops))
	}
	if len(resp.Assignments) != len(crew) {
		t.Fatalf("%d of %d hulls were given work; an idle hull is a probe bought and not used",
			len(resp.Assignments), len(crew))
	}
}
