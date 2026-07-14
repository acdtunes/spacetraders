package clusterstore

import (
	"context"
	"sort"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/cluster"
)

// fakeRepo is an in-memory ClusterRepository — the durable port's test double at the
// hexagonal boundary. It stands in for the DB/config-backed adapter so the store's
// declarative-apply + granular-mutation logic is exercised without real I/O, and the
// "restart-safe" property is provable by building a fresh Store over the SAME repo
// (the state lives in the repo, never in the store).
type fakeRepo struct {
	byID map[string]*cluster.ContractCluster
}

func newFakeRepo() *fakeRepo { return &fakeRepo{byID: map[string]*cluster.ContractCluster{}} }

func (f *fakeRepo) List(context.Context) ([]*cluster.ContractCluster, error) {
	out := make([]*cluster.ContractCluster, 0, len(f.byID))
	for _, c := range f.byID {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out, nil
}

func (f *fakeRepo) Get(_ context.Context, id string) (*cluster.ContractCluster, bool, error) {
	c, ok := f.byID[id]
	return c, ok, nil
}

func (f *fakeRepo) Save(_ context.Context, c *cluster.ContractCluster) error {
	f.byID[c.ID()] = c
	return nil
}

func (f *fakeRepo) Delete(_ context.Context, id string) error {
	delete(f.byID, id)
	return nil
}

func mustCluster(t *testing.T, id, waypoint string) *cluster.ContractCluster {
	t.Helper()
	c, err := cluster.NewContractCluster(id,
		[]cluster.Element{{Waypoint: waypoint, ShipSymbol: id + "-WH"}},
		nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("build cluster %q: %v", id, err)
	}
	return c
}

// registryIDs is the observable read model: which cluster ids the contract engine would
// see after a restart (LoadRegistry rebuilds the immutable registry from the repo).
func registryIDs(t *testing.T, repo Repository) []string {
	t.Helper()
	reg, err := New(repo).LoadRegistry(context.Background())
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	ids := make([]string, 0)
	for _, c := range reg.Clusters() {
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

// Declarative bulk apply: the store persists exactly the given topology — new clusters
// are added, and clusters no longer in the desired set are dropped — so a restart sees
// precisely what was applied.
func TestStore_ApplyTopology_ReplacesPersistedSet(t *testing.T) {
	repo := newFakeRepo()
	s := New(repo)
	ctx := context.Background()

	if err := s.ApplyTopology(ctx, []*cluster.ContractCluster{
		mustCluster(t, "alpha", "X1-A-1"),
		mustCluster(t, "beta", "X1-B-1"),
	}); err != nil {
		t.Fatalf("ApplyTopology 1: %v", err)
	}
	if got := registryIDs(t, repo); !eq(got, []string{"alpha", "beta"}) {
		t.Fatalf("after first apply, registry = %v, want [alpha beta]", got)
	}

	// Re-apply a DIFFERENT set: beta drops out, gamma appears, alpha stays.
	if err := s.ApplyTopology(ctx, []*cluster.ContractCluster{
		mustCluster(t, "alpha", "X1-A-1"),
		mustCluster(t, "gamma", "X1-G-1"),
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
	if err := New(repo).ApplyTopology(ctx, []*cluster.ContractCluster{mustCluster(t, "alpha", "X1-A-1")}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := New(repo).AddElement(ctx, "alpha", cluster.RoleStocker, cluster.Element{Waypoint: "X1-SRC-1", ShipSymbol: "ST-9"}); err != nil {
		t.Fatalf("AddElement: %v", err)
	}

	// Simulate a restart: a brand-new store over the same durable repo.
	reg, err := New(repo).LoadRegistry(ctx)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	stockers := reg.Clusters()[0].Stockers()
	if len(stockers) != 1 || stockers[0].ShipSymbol != "ST-9" || stockers[0].Waypoint != "X1-SRC-1" {
		t.Fatalf("added stocker not durable across restart: %+v", stockers)
	}
}

func TestStore_RemoveElement_Durable(t *testing.T) {
	repo := newFakeRepo()
	ctx := context.Background()
	seed, err := cluster.NewContractCluster("alpha",
		[]cluster.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH"}},
		[]cluster.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-9"}}, nil, nil)
	if err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	if err := New(repo).ApplyTopology(ctx, []*cluster.ContractCluster{seed}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := New(repo).RemoveElement(ctx, "alpha", cluster.RoleStocker, "ST-9"); err != nil {
		t.Fatalf("RemoveElement: %v", err)
	}
	reg, _ := New(repo).LoadRegistry(ctx)
	if n := len(reg.Clusters()[0].Stockers()); n != 0 {
		t.Fatalf("stocker not removed durably: count = %d", n)
	}
}

func TestStore_PlaceElement_Durable(t *testing.T) {
	repo := newFakeRepo()
	ctx := context.Background()
	seed, err := cluster.NewContractCluster("alpha",
		[]cluster.Element{{Waypoint: "X1-A-1", ShipSymbol: "WH"}},
		nil,
		[]cluster.Element{{Waypoint: "X1-OFF-1", ShipSymbol: "DH-1"}}, nil)
	if err != nil {
		t.Fatalf("seed cluster: %v", err)
	}
	if err := New(repo).ApplyTopology(ctx, []*cluster.ContractCluster{seed}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := New(repo).PlaceElement(ctx, "alpha", cluster.RoleDeliveryHull, "DH-1", "X1-A-1"); err != nil {
		t.Fatalf("PlaceElement: %v", err)
	}
	reg, _ := New(repo).LoadRegistry(ctx)
	hulls := reg.Clusters()[0].DeliveryHulls()
	if len(hulls) != 1 || hulls[0].Waypoint != "X1-A-1" {
		t.Fatalf("hull not repositioned durably: %+v", hulls)
	}
}

// AddCluster is the granular cluster-level create: it persists ONE cluster without
// touching the rest of the set (unlike ApplyTopology, which replaces the whole set),
// so the CLI's `cluster add` grows the topology one cluster at a time.
func TestStore_AddCluster_AddsOneWithoutReplacingSet(t *testing.T) {
	repo := newFakeRepo()
	ctx := context.Background()
	s := New(repo)

	if err := s.AddCluster(ctx, mustCluster(t, "alpha", "X1-A-1")); err != nil {
		t.Fatalf("AddCluster alpha: %v", err)
	}
	if err := s.AddCluster(ctx, mustCluster(t, "beta", "X1-B-1")); err != nil {
		t.Fatalf("AddCluster beta: %v", err)
	}
	if got := registryIDs(t, repo); !eq(got, []string{"alpha", "beta"}) {
		t.Fatalf("after two AddCluster, registry = %v, want [alpha beta] (neither replaced the other)", got)
	}
}

// RemoveCluster is the granular cluster-level delete: it drops ONE cluster and leaves
// the rest, and is idempotent (removing an absent cluster is not an error).
func TestStore_RemoveCluster_DropsOneKeepsRest(t *testing.T) {
	repo := newFakeRepo()
	ctx := context.Background()
	s := New(repo)
	if err := s.ApplyTopology(ctx, []*cluster.ContractCluster{
		mustCluster(t, "alpha", "X1-A-1"),
		mustCluster(t, "beta", "X1-B-1"),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := s.RemoveCluster(ctx, "alpha"); err != nil {
		t.Fatalf("RemoveCluster alpha: %v", err)
	}
	if got := registryIDs(t, repo); !eq(got, []string{"beta"}) {
		t.Fatalf("after RemoveCluster alpha, registry = %v, want [beta]", got)
	}
	if err := s.RemoveCluster(ctx, "ghost"); err != nil {
		t.Fatalf("RemoveCluster of an absent cluster must be a no-op, got: %v", err)
	}
}

// A granular op naming a cluster that does not exist errors — the CLI reports it rather
// than silently creating a malformed cluster.
func TestStore_GranularOp_UnknownClusterErrors(t *testing.T) {
	repo := newFakeRepo()
	ctx := context.Background()
	if err := New(repo).AddElement(ctx, "ghost", cluster.RoleStocker, cluster.Element{Waypoint: "X1-1", ShipSymbol: "S"}); err == nil {
		t.Fatalf("AddElement on an unknown cluster must error")
	}
}

// The regression-safe default: no clusters persisted -> an empty registry (destination
// warehousing entirely OFF), never an error.
func TestStore_LoadRegistry_EmptyRepo_OwnsNothing(t *testing.T) {
	reg, err := New(newFakeRepo()).LoadRegistry(context.Background())
	if err != nil {
		t.Fatalf("LoadRegistry on empty repo: %v", err)
	}
	if len(reg.Clusters()) != 0 {
		t.Fatalf("empty repo must yield an empty registry, got %d clusters", len(reg.Clusters()))
	}
}
