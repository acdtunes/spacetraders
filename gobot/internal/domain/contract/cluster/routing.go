package cluster

// Registry is the configured set of contract clusters the contract engine consults to
// route the single active (serialized) contract. It is immutable; RouteContract is a
// pure query with no I/O, so the contract coordinator can consult it every pass without
// side effects.
type Registry struct {
	clusters []*ContractCluster
}

// NewRegistry builds a registry over the given clusters. Routing is deterministic
// regardless of input order. A nil/empty slice is a valid registry that owns nothing —
// destination warehousing entirely OFF, the regression-safe default.
func NewRegistry(clusters []*ContractCluster) *Registry {
	out := make([]*ContractCluster, 0, len(clusters))
	out = append(out, clusters...)
	return &Registry{clusters: out}
}

// Clusters returns a copy of the registered clusters (order-preserving) — a read model
// for the application layer / CLI status.
func (r *Registry) Clusters() []*ContractCluster {
	out := make([]*ContractCluster, len(r.clusters))
	copy(out, r.clusters)
	return out
}

// RouteContract returns the cluster that OWNS the contract's destination geometry: the
// cluster whose destination warehouse(s) cover the MOST of the contract's delivery
// destinations, breaking ties by lowest cluster id for determinism. It returns nil when
// no cluster covers any destination — the caller then falls back to the legacy
// long-haul path (regression-safe: destination warehousing is purely additive).
func (r *Registry) RouteContract(destinationSymbols []string) *ContractCluster {
	var best *ContractCluster
	bestCovered := 0
	for _, c := range r.clusters {
		covered := c.countCoveredDestinations(destinationSymbols)
		if covered == 0 {
			continue
		}
		if best == nil || covered > bestCovered || (covered == bestCovered && c.id < best.id) {
			best = c
			bestCovered = covered
		}
	}
	return best
}

// Owns reports whether the cluster's destination warehouse(s) sit at destinationSymbol —
// the geometric predicate the contract engine uses to decide a contract belongs to this
// cluster's co-located delivery hull rather than the long-haul path.
func (c *ContractCluster) Owns(destinationSymbol string) bool {
	for _, w := range c.warehouses {
		if w.Waypoint == destinationSymbol {
			return true
		}
	}
	return false
}

// countCoveredDestinations counts how many of a contract's delivery destinations this
// cluster's warehouses cover — the routing score.
func (c *ContractCluster) countCoveredDestinations(destinations []string) int {
	covered := 0
	for _, d := range destinations {
		if c.Owns(d) {
			covered++
		}
	}
	return covered
}
