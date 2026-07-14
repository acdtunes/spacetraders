package depotstore

import (
	"context"
	"sort"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
)

// fakeRepo is an in-memory DepotRepository — the durable port's test double at the
// hexagonal boundary. It stands in for the DB/config-backed adapter so the store's
// declarative-apply + granular-mutation logic is exercised without real I/O, and the
// "restart-safe" property is provable by building a fresh Store over the SAME repo
// (the state lives in the repo, never in the store).
type fakeRepo struct {
	byID map[string]*depot.ContractDepot
}

func newFakeRepo() *fakeRepo { return &fakeRepo{byID: map[string]*depot.ContractDepot{}} }

func (f *fakeRepo) List(context.Context) ([]*depot.ContractDepot, error) {
	out := make([]*depot.ContractDepot, 0, len(f.byID))
	for _, c := range f.byID {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out, nil
}

func (f *fakeRepo) Get(_ context.Context, id string) (*depot.ContractDepot, bool, error) {
	c, ok := f.byID[id]
	return c, ok, nil
}

func (f *fakeRepo) Save(_ context.Context, c *depot.ContractDepot) error {
	f.byID[c.ID()] = c
	return nil
}

func (f *fakeRepo) Delete(_ context.Context, id string) error {
	delete(f.byID, id)
	return nil
}

func mustDepot(t *testing.T, id, waypoint string) *depot.ContractDepot {
	t.Helper()
	c, err := depot.NewContractDepot(id,
		[]depot.Element{{Waypoint: waypoint, ShipSymbol: id + "-WH"}},
		nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("build depot %q: %v", id, err)
	}
	return c
}

// registryIDs is the observable read model: which depot ids the contract engine would
// see after a restart (LoadRegistry rebuilds the immutable registry from the repo).
func registryIDs(t *testing.T, repo Repository) []string {
	t.Helper()
	reg, err := New(repo).LoadRegistry(context.Background())
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	ids := make([]string, 0)
	for _, c := range reg.Depots() {
		ids = append(ids, c.ID())
	}
	sort.Strings(ids)
	return ids
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Declarative bulk apply: the store persists exactly the given topology — new depots
// are added, and depots no longer in the desired set are dropped — so a restart sees
// precisely what was applied.
func TestStore_ApplyTopology_ReplacesPersistedSet(t *testing.T) {
	repo := newFakeRepo()
	s := New(repo)
	ctx := context.Background()

	if err := s.ApplyTopology(ctx, []*depot.ContractDepot{
		mustDepot(t, "alpha", "X1-A-1"),
		mustDepot(t, "beta", "X1-B-1"),
	}); err != nil {
		t.Fatalf("ApplyTopology 1: %v", err)
	}
	if got := registryIDs(t, repo); !eq(got, []string{"alpha", "beta"}) {
		t.Fatalf("after first apply, registry = %v, want [alpha beta]", got)
	}

	// Re-apply a DIFFERENT set: beta drops out, gamma appears, alpha stays.
	if err := s.ApplyTopology(ctx, []*depot.ContractDepot{
		mustDepot(t, "alpha", "X1-A-1"),
		mustDepot(t, "gamma", "X1-G-1"),
	}); err != nil {
		t.Fatalf("ApplyTopology 2: %v", err)
	}
	if got := registryIDs(t, repo); !eq(got, []string{"alpha", "gamma"}) {
		t.Fatalf("after re-apply, registry = %v, want [alpha gamma] (beta must be dropped)", got)
	}
}

// Granular add is durable + restart-safe: the added element is visible via a FRESH store
// over the same repo, proving the change lives in the durable port, not the store.
func TestStore_AddElement_DurableAcrossRestart(t *testing.T) {
	repo := newFakeRepo()
	ctx := context.Background()
	if err := New(repo).ApplyTopology(ctx, []*depot.ContractDepot{mustDepot(t, "alpha", "X1-A-1")}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := New(repo).AddElement(ctx, "alpha", depot.RoleStocker, depot.Element{Waypoint: "X1-SRC-1", ShipSymbol: "ST-9"}); err != nil {
		t.Fatalf("AddElement: %v", err)
	}

	// Simulate a restart: a brand-new store over the same durable repo.
	reg, err := New(repo).LoadRegistry(ctx)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	stockers := reg.Depots()[0].Stockers()
	if len(stockers) != 1 || stockers[0].ShipSymbol != "ST-9" || stockers[0].Waypoint != "X1-SRC-1" {
		t.Fatalf("added stocker not durable across restart: %+v", stockers)
	}
}

func TestStore_RemoveElement_Durable(t *testing.T) {
	repo := newFakeRepo()
	ctx := context.Background()
	seed, err := depot.NewContractDepot("alpha",
		[]depot.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH"}},
		[]depot.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-9"}}, nil, nil)
	if err != nil {
		t.Fatalf("seed depot: %v", err)
	}
	if err := New(repo).ApplyTopology(ctx, []*depot.ContractDepot{seed}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := New(repo).RemoveElement(ctx, "alpha", depot.RoleStocker, "ST-9"); err != nil {
		t.Fatalf("RemoveElement: %v", err)
	}
	reg, _ := New(repo).LoadRegistry(ctx)
	if n := len(reg.Depots()[0].Stockers()); n != 0 {
		t.Fatalf("stocker not removed durably: count = %d", n)
	}
}

func TestStore_PlaceElement_Durable(t *testing.T) {
	repo := newFakeRepo()
	ctx := context.Background()
	seed, err := depot.NewContractDepot("alpha",
		[]depot.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH"}},
		nil,
		[]depot.Element{{Waypoint: "X1-OFF-1", ShipSymbol: "DH-1"}}, nil)
	if err != nil {
		t.Fatalf("seed depot: %v", err)
	}
	if err := New(repo).ApplyTopology(ctx, []*depot.ContractDepot{seed}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := New(repo).PlaceElement(ctx, "alpha", depot.RoleDeliveryHull, "DH-1", "X1-A-1"); err != nil {
		t.Fatalf("PlaceElement: %v", err)
	}
	reg, _ := New(repo).LoadRegistry(ctx)
	hulls := reg.Depots()[0].DeliveryHulls()
	if len(hulls) != 1 || hulls[0].Waypoint != "X1-A-1" {
		t.Fatalf("hull not repositioned durably: %+v", hulls)
	}
}

// AddDepot is the granular depot-level create: it persists ONE depot without
// touching the rest of the set (unlike ApplyTopology, which replaces the whole set),
// so the CLI's `depot add` grows the topology one depot at a time.
func TestStore_AddDepot_AddsOneWithoutReplacingSet(t *testing.T) {
	repo := newFakeRepo()
	ctx := context.Background()
	s := New(repo)

	if err := s.AddDepot(ctx, mustDepot(t, "alpha", "X1-A-1")); err != nil {
		t.Fatalf("AddDepot alpha: %v", err)
	}
	if err := s.AddDepot(ctx, mustDepot(t, "beta", "X1-B-1")); err != nil {
		t.Fatalf("AddDepot beta: %v", err)
	}
	if got := registryIDs(t, repo); !eq(got, []string{"alpha", "beta"}) {
		t.Fatalf("after two AddDepot, registry = %v, want [alpha beta] (neither replaced the other)", got)
	}
}

// RemoveDepot is the granular depot-level delete: it drops ONE depot and leaves
// the rest, and is idempotent (removing an absent depot is not an error).
func TestStore_RemoveDepot_DropsOneKeepsRest(t *testing.T) {
	repo := newFakeRepo()
	ctx := context.Background()
	s := New(repo)
	if err := s.ApplyTopology(ctx, []*depot.ContractDepot{
		mustDepot(t, "alpha", "X1-A-1"),
		mustDepot(t, "beta", "X1-B-1"),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := s.RemoveDepot(ctx, "alpha"); err != nil {
		t.Fatalf("RemoveDepot alpha: %v", err)
	}
	if got := registryIDs(t, repo); !eq(got, []string{"beta"}) {
		t.Fatalf("after RemoveDepot alpha, registry = %v, want [beta]", got)
	}
	if err := s.RemoveDepot(ctx, "ghost"); err != nil {
		t.Fatalf("RemoveDepot of an absent depot must be a no-op, got: %v", err)
	}
}

// A granular op naming a depot that does not exist errors — the CLI reports it rather
// than silently creating a malformed depot.
func TestStore_GranularOp_UnknownDepotErrors(t *testing.T) {
	repo := newFakeRepo()
	ctx := context.Background()
	if err := New(repo).AddElement(ctx, "ghost", depot.RoleStocker, depot.Element{Waypoint: "X1-1", ShipSymbol: "S"}); err == nil {
		t.Fatalf("AddElement on an unknown depot must error")
	}
}

// The regression-safe default: no depots persisted -> an empty registry (destination
// warehousing entirely OFF), never an error.
func TestStore_LoadRegistry_EmptyRepo_OwnsNothing(t *testing.T) {
	reg, err := New(newFakeRepo()).LoadRegistry(context.Background())
	if err != nil {
		t.Fatalf("LoadRegistry on empty repo: %v", err)
	}
	if len(reg.Depots()) != 0 {
		t.Fatalf("empty repo must yield an empty registry, got %d depots", len(reg.Depots()))
	}
}
