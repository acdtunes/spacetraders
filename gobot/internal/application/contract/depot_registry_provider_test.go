package contract

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
)

// These tests cover the injection boundary of the FINAL sp-u9xa seam: ResolveDepotRegistry
// is how the live contract coordinator obtains the depot routing registry each pass,
// sourced from the boot-loaded durable store via the narrow DepotRegistryProvider port.
// It is fail-safe by construction for the dominant-income contract engine: a provider that
// is absent (feature unwired) or errors (durable-store hiccup) resolves to a nil registry,
// which routeContractViaDepot degrades to the default long-haul path. Reading the store
// each pass is what makes a `depot add|remove` on the running daemon live with no restart.

type fakeDepotRegistryProvider struct {
	reg *depot.Registry
	err error
}

func (f *fakeDepotRegistryProvider) LoadDepotRegistry(_ context.Context, _ int) (*depot.Registry, error) {
	return f.reg, f.err
}

// TestResolveDepotRegistry_AbsentOrErrored_FailsOpenToNil is the fail-safe guard: no
// provider wired (tests, or a daemon predating the wiring) and a durable-store read error
// BOTH resolve to nil — the coordinator then runs its unchanged default routing. The error
// case additionally warns, never propagating the failure into the coordinator loop.
func TestResolveDepotRegistry_AbsentOrErrored_FailsOpenToNil(t *testing.T) {
	cases := []struct {
		name        string
		provider    DepotRegistryProvider
		wantWarning bool
	}{
		{"nil provider (feature unwired)", nil, false},
		{"provider read error (durable-store hiccup)", &fakeDepotRegistryProvider{err: fmt.Errorf("db unavailable")}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger := &standbyCapturingLogger{}
			reg := ResolveDepotRegistry(context.Background(), logger, tc.provider, 2)
			if reg != nil {
				t.Fatalf("must fail open to a nil registry (default long-haul path), got %v", reg)
			}
			if tc.wantWarning && !logger.hasWarning() {
				t.Fatalf("expected a WARNING on a live depot-registry read failure, got levels %v", logger.levels)
			}
		})
	}
}

// TestResolveDepotRegistry_LiveRegistryWins proves the resolver returns the LIVE registry
// rebuilt from the durable store, so a depot mutation on the running daemon is honored on
// the next pass with no restart.
func TestResolveDepotRegistry_LiveRegistryWins(t *testing.T) {
	c, err := depot.NewContractDepot("alpha",
		[]depot.Element{{Waypoint: "X1-SYS-DEST", ShipSymbol: "WH-1"}}, nil, nil, nil)
	if err != nil {
		t.Fatalf("build depot: %v", err)
	}
	want := depot.NewRegistry([]*depot.ContractDepot{c})

	got := ResolveDepotRegistry(context.Background(), &standbyCapturingLogger{},
		&fakeDepotRegistryProvider{reg: want}, 2)

	if got != want {
		t.Fatalf("the resolver must return the provider's live registry, got %v want %v", got, want)
	}
}
