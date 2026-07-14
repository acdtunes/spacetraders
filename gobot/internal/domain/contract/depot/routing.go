package depot

// Registry is the configured set of contract depots the contract engine consults to
// route the single active (serialized) contract. It is immutable; RouteContract is a
// pure query with no I/O, so the contract coordinator can consult it every pass without
// side effects.
type Registry struct {
	depots []*ContractDepot
}

// NewRegistry builds a registry over the given depots. Routing is deterministic
// regardless of input order. A nil/empty slice is a valid registry that owns nothing —
// destination warehousing entirely OFF, the regression-safe default.
func NewRegistry(depots []*ContractDepot) *Registry {
	out := make([]*ContractDepot, 0, len(depots))
	out = append(out, depots...)
	return &Registry{depots: out}
}

// Depots returns a copy of the registered depots (order-preserving) — a read model
// for the application layer / CLI status.
func (r *Registry) Depots() []*ContractDepot {
	out := make([]*ContractDepot, len(r.depots))
	copy(out, r.depots)
	return out
}

// RouteContract returns the depot that OWNS the contract's destination geometry: the
// depot whose destination warehouse(s) cover the MOST of the contract's delivery
// destinations, breaking ties by lowest depot id for determinism. It returns nil when
// no depot covers any destination — the caller then falls back to the legacy
// long-haul path (regression-safe: destination warehousing is purely additive).
func (r *Registry) RouteContract(destinationSymbols []string) *ContractDepot {
	var best *ContractDepot
	bestCovered := 0
	for _, c := range r.depots {
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

// Owns reports whether the depot's destination warehouse(s) sit at destinationSymbol —
// the geometric predicate the contract engine uses to decide a contract belongs to this
// depot's co-located delivery hull rather than the long-haul path.
func (c *ContractDepot) Owns(destinationSymbol string) bool {
	for _, w := range c.warehouses {
		if w.Waypoint == destinationSymbol {
			return true
		}
	}
	return false
}

// countCoveredDestinations counts how many of a contract's delivery destinations this
// depot's warehouses cover — the routing score.
func (c *ContractDepot) countCoveredDestinations(destinations []string) int {
	covered := 0
	for _, d := range destinations {
		if c.Owns(d) {
			covered++
		}
	}
	return covered
}
