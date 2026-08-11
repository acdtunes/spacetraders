package api

import (
	"context"
	"errors"
	"testing"

	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

type countingGateAPI struct {
	waypointCalls int
	jumpGateCalls int
	chartCalls    int
	detail        *domainPorts.WaypointDetail
	err           error
}

func (f *countingGateAPI) GetJumpGate(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.JumpGateData, error) {
	f.jumpGateCalls++
	return &domainPorts.JumpGateData{Symbol: waypointSymbol, Connections: []string{"X1-PA3-I51"}}, nil
}

func (f *countingGateAPI) GetWaypoint(ctx context.Context, systemSymbol, waypointSymbol, token string) (*domainPorts.WaypointDetail, error) {
	f.waypointCalls++
	if f.err != nil {
		return nil, f.err
	}
	if f.detail != nil {
		return f.detail, nil
	}
	return &domainPorts.WaypointDetail{Symbol: waypointSymbol}, nil
}

func (f *countingGateAPI) CreateChart(ctx context.Context, shipSymbol, token string) (*domainPorts.ChartResult, error) {
	f.chartCalls++
	return &domainPorts.ChartResult{}, nil
}

type stubBuiltGateStore struct {
	built map[string]bool
	err   error
	calls int
}

func (s *stubBuiltGateStore) RecordedBuiltGate(ctx context.Context, gateWaypoint string) (bool, error) {
	s.calls++
	if s.err != nil {
		// Adversarial: a failing store also returns a permissive verdict, so the
		// decorator must reject it on the error alone.
		return true, s.err
	}
	return s.built[gateWaypoint], nil
}

// THE SAVING: a gate already recorded built costs ZERO API requests.
func TestGateConstructionProbe_RecordedBuiltGateSkipsTheLiveRead(t *testing.T) {
	inner := &countingGateAPI{}
	store := &stubBuiltGateStore{built: map[string]bool{"X1-PA3-I51": true}}
	probe := NewGateConstructionProbe(inner, store)

	detail, err := probe.GetWaypoint(context.Background(), "X1-PA3", "X1-PA3-I51", "tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.waypointCalls != 0 {
		t.Fatalf("expected 0 live waypoint reads, got %d", inner.waypointCalls)
	}
	if detail.Symbol != "X1-PA3-I51" || detail.IsUnderConstruction {
		t.Fatalf("expected a built verdict for the gate, got %+v", detail)
	}
}

// GUARD: a gate NOT recorded built is always read live — the store can only
// suppress a redundant confirmation, never substitute for an unknown verdict.
func TestGateConstructionProbe_UnrecordedGateIsReadLive(t *testing.T) {
	inner := &countingGateAPI{detail: &domainPorts.WaypointDetail{Symbol: "X1-AF2-I90", IsUnderConstruction: true}}
	store := &stubBuiltGateStore{built: map[string]bool{}}
	probe := NewGateConstructionProbe(inner, store)

	detail, err := probe.GetWaypoint(context.Background(), "X1-AF2", "X1-AF2-I90", "tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.waypointCalls != 1 {
		t.Fatalf("expected exactly 1 live waypoint read, got %d", inner.waypointCalls)
	}
	if !detail.IsUnderConstruction {
		t.Fatal("the live under-construction verdict must survive the decorator")
	}
}

// GUARD: a store failure must never be read as "built". It falls through to the
// live probe, so the worst case is the unoptimised behaviour, never a permissive one.
func TestGateConstructionProbe_StoreErrorFallsThroughToLive(t *testing.T) {
	inner := &countingGateAPI{detail: &domainPorts.WaypointDetail{Symbol: "X1-PA3-I51", IsUnderConstruction: true}}
	store := &stubBuiltGateStore{err: errors.New("db down")}
	probe := NewGateConstructionProbe(inner, store)

	detail, err := probe.GetWaypoint(context.Background(), "X1-PA3", "X1-PA3-I51", "tok")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.waypointCalls != 1 {
		t.Fatalf("a store error must fall through to the live read, got %d live reads", inner.waypointCalls)
	}
	if !detail.IsUnderConstruction {
		t.Fatal("the live verdict must win after a store error")
	}
}

// GUARD: a live probe failure propagates unchanged, so the caller's fail-closed
// handling (treat the edge as under construction) still fires.
func TestGateConstructionProbe_LiveErrorPropagates(t *testing.T) {
	boom := errors.New("gate probe 400")
	inner := &countingGateAPI{err: boom}
	probe := NewGateConstructionProbe(inner, &stubBuiltGateStore{built: map[string]bool{}})

	if _, err := probe.GetWaypoint(context.Background(), "X1-AF2", "X1-AF2-I90", "tok"); !errors.Is(err, boom) {
		t.Fatalf("expected the live error to propagate, got %v", err)
	}
}

// A nil store degrades to a plain pass-through: no behaviour change at all.
func TestGateConstructionProbe_NilStoreIsPassThrough(t *testing.T) {
	inner := &countingGateAPI{}
	probe := NewGateConstructionProbe(inner, nil)

	if _, err := probe.GetWaypoint(context.Background(), "X1-PA3", "X1-PA3-I51", "tok"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.waypointCalls != 1 {
		t.Fatalf("a nil store must pass through, got %d live reads", inner.waypointCalls)
	}
}

// The other two gate calls are untouched pass-throughs — the decorator changes the
// construction probe only, never the topology fetch or the public chart.
func TestGateConstructionProbe_JumpGateAndChartPassThrough(t *testing.T) {
	inner := &countingGateAPI{}
	probe := NewGateConstructionProbe(inner, &stubBuiltGateStore{built: map[string]bool{"X1-PA3-I51": true}})
	ctx := context.Background()

	if _, err := probe.GetJumpGate(ctx, "X1-KA42", "X1-KA42-I50", "tok"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := probe.CreateChart(ctx, "ORION-1", "tok"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.jumpGateCalls != 1 || inner.chartCalls != 1 {
		t.Fatalf("expected both calls to pass through, got jumpGate=%d chart=%d", inner.jumpGateCalls, inner.chartCalls)
	}
}
