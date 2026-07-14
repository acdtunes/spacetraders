package contract

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/cluster"
)

// These tests cover the injection boundary of the FINAL sp-u9xa seam: ResolveClusterRegistry
// is how the live contract coordinator obtains the cluster routing registry each pass,
// sourced from the boot-loaded durable store via the narrow ClusterRegistryProvider port.
// It is fail-safe by construction for the dominant-income contract engine: a provider that
// is absent (feature unwired) or errors (durable-store hiccup) resolves to a nil registry,
// which routeContractViaCluster degrades to the default long-haul path. Reading the store
// each pass is what makes a `cluster add|remove` on the running daemon live with no restart.

type fakeClusterRegistryProvider struct {
	reg *cluster.Registry
	err error
}

func (f *fakeClusterRegistryProvider) LoadClusterRegistry(_ context.Context, _ int) (*cluster.Registry, error) {
	return f.reg, f.err
}

// TestResolveClusterRegistry_AbsentOrErrored_FailsOpenToNil is the fail-safe guard: no
// provider wired (tests, or a daemon predating the wiring) and a durable-store read error
// BOTH resolve to nil — the coordinator then runs its unchanged default routing. The error
// case additionally warns, never propagating the failure into the coordinator loop.
func TestResolveClusterRegistry_AbsentOrErrored_FailsOpenToNil(t *testing.T) {
	cases := []struct {
		name        string
		provider    ClusterRegistryProvider
		wantWarning bool
	}{
		{"nil provider (feature unwired)", nil, false},
		{"provider read error (durable-store hiccup)", &fakeClusterRegistryProvider{err: fmt.Errorf("db unavailable")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger := &standbyCapturingLogger{}
			reg := ResolveClusterRegistry(context.Background(), logger, tc.provider, 2)
			if reg != nil {
				t.Fatalf("must fail open to a nil registry (default long-haul path), got %v", reg)
			}
			if tc.wantWarning && !logger.hasWarning() {
				t.Fatalf("expected a WARNING on a live cluster-registry read failure, got levels %v", logger.levels)
			}
		})
	}
}

// TestResolveClusterRegistry_LiveRegistryWins proves the resolver returns the LIVE registry
// rebuilt from the durable store, so a cluster mutation on the running daemon is honored on
// the next pass with no restart.
func TestResolveClusterRegistry_LiveRegistryWins(t *testing.T) {
	c, err := cluster.NewContractCluster("alpha",
		[]cluster.Element{{Waypoint: "X1-SYS-DEST", ShipSymbol: "WH-1"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("build cluster: %v", err)
	}
	want := cluster.NewRegistry([]*cluster.ContractCluster{c})

	got := ResolveClusterRegistry(context.Background(), &standbyCapturingLogger{},
		&fakeClusterRegistryProvider{reg: want}, 2)

	if got != want {
		t.Fatalf("the resolver must return the provider's live registry, got %v want %v", got, want)
	}
}
