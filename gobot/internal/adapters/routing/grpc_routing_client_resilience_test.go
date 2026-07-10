package routing

import (
	"context"
	"net"
	"testing"
	"time"

	domainRouting "github.com/andrescamacho/spacetraders-go/internal/domain/routing"
)

// closedLocalAddr reserves a localhost port and immediately frees it, returning
// an address that is guaranteed to have nothing listening on it. Hermetic: no
// real routing service, no external network.
func closedLocalAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve a local port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("failed to free the reserved port: %v", err)
	}
	return addr
}

// The resilience property itself (sp-g5ct): constructing the routing client must
// NOT fail when the routing service is down. Before the lazy-conn change the
// constructor blocked on the dial (grpc.WithBlock) and errored when nothing was
// listening, which made the whole daemon refuse to boot if routing happened to be
// down during a restart (RULINGS #2). The connection is now established lazily, so
// construction succeeds regardless of service availability.
func TestNewGRPCRoutingClient_SucceedsWhenServiceDown(t *testing.T) {
	client, err := NewGRPCRoutingClient(closedLocalAddr(t))
	if err != nil {
		t.Fatalf("constructor must succeed with the routing service down, got error: %v", err)
	}
	if client == nil {
		t.Fatal("constructor returned a nil client")
	}
	t.Cleanup(func() { _ = client.Close() })
}

// A routing RPC against a dead address must return an error PROMPTLY (fail fast
// with Unavailable), never hang. Callers rely on this: the container runner and
// tour coordinator handle the error on their normal path (park/fail-open), which
// only works if the RPC actually returns.
func TestGRPCRoutingClient_RPCFailsPromptlyWhenServiceDown(t *testing.T) {
	client, err := NewGRPCRoutingClient(closedLocalAddr(t))
	if err != nil {
		t.Fatalf("unexpected constructor error: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, rpcErr := client.PlanRoute(ctx, &domainRouting.RouteRequest{
			SystemSymbol:  "X1-TEST",
			StartWaypoint: "X1-TEST-A1",
			GoalWaypoint:  "X1-TEST-B2",
		})
		done <- rpcErr
	}()

	select {
	case rpcErr := <-done:
		if rpcErr == nil {
			t.Fatal("expected an error from an RPC against a dead routing address, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RPC hung against a dead routing address — it must fail fast, not block")
	}
}

// The boot-time reachability probe must also fail promptly (within its bounded
// ctx) when the service is down — it is what lets main.go log honest routing state
// at startup without coupling boot to routing being up.
func TestGRPCRoutingClient_WaitForReadyFailsPromptlyWhenServiceDown(t *testing.T) {
	client, err := NewGRPCRoutingClient(closedLocalAddr(t))
	if err != nil {
		t.Fatalf("unexpected constructor error: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- client.WaitForReady(ctx) }()

	select {
	case probeErr := <-done:
		if probeErr == nil {
			t.Fatal("expected WaitForReady to report the service unreachable, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitForReady hung against a dead routing address — it must respect its bounded ctx")
	}
}
