package cluster

import "fmt"

// Role names one of a cluster's four element classes. It is the parameter the CLI's
// granular add / remove / place operations take so no element class is hardcoded at a
// call site: a caller names the role, and the mutation touches exactly that slice.
type Role int

const (
	RoleWarehouse Role = iota
	RoleStocker
	RoleDeliveryHull
	RoleSourceHub
)

// roleNames is the single source of truth mapping each Role to its stable string form
// (used by both String and ParseRole, so the round-trip can never drift).
var roleNames = map[Role]string{
	RoleWarehouse:    "warehouse",
	RoleStocker:      "stocker",
	RoleDeliveryHull: "delivery-hull",
	RoleSourceHub:    "source-hub",
}

// String returns the stable, CLI-facing name of the role.
func (r Role) String() string {
	if name, ok := roleNames[r]; ok {
		return name
	}
	return fmt.Sprintf("role(%d)", int(r))
}

// ParseRole maps a CLI role name back to a Role, erroring on any unknown name so a
// mistyped `--role` is rejected loudly rather than silently touching the wrong class.
func ParseRole(name string) (Role, error) {
	for role, n := range roleNames {
		if n == name {
			return role, nil
		}
	}
	return 0, fmt.Errorf("unknown cluster element role %q (want one of warehouse, stocker, delivery-hull, source-hub)", name)
}

// WithElementAdded returns a NEW cluster with e appended to the named role's elements,
// leaving the receiver untouched (immutable functional update). The reconstruction runs
// through NewContractCluster so the cluster invariants hold for the result.
func (c *ContractCluster) WithElementAdded(role Role, e Element) (*ContractCluster, error) {
	w, s, d, h := c.Warehouses(), c.Stockers(), c.DeliveryHulls(), c.SourceHubs()
	switch role {
	case RoleWarehouse:
		w = append(w, e)
	case RoleStocker:
		s = append(s, e)
	case RoleDeliveryHull:
		d = append(d, e)
	case RoleSourceHub:
		h = append(h, e)
	default:
		return nil, fmt.Errorf("unknown cluster element role %q", role)
	}
	return NewContractCluster(c.id, w, s, d, h)
}

// WithElementRemoved returns a NEW cluster with the element crewed by shipSymbol dropped
// from the named role, leaving the receiver untouched. It errors when no element in that
// role is crewed by shipSymbol (so the CLI can report "nothing to remove"), and — via
// NewContractCluster — when the removal would leave the cluster with no destination
// warehouse (the one structural invariant).
func (c *ContractCluster) WithElementRemoved(role Role, shipSymbol string) (*ContractCluster, error) {
	w, s, d, h := c.Warehouses(), c.Stockers(), c.DeliveryHulls(), c.SourceHubs()
	var target *[]Element
	switch role {
	case RoleWarehouse:
		target = &w
	case RoleStocker:
		target = &s
	case RoleDeliveryHull:
		target = &d
	case RoleSourceHub:
		target = &h
	default:
		return nil, fmt.Errorf("unknown cluster element role %q", role)
	}
	filtered, found := removeByShip(*target, shipSymbol)
	if !found {
		return nil, fmt.Errorf("no %s element crewed by ship %q in cluster %q", role, shipSymbol, c.id)
	}
	*target = filtered
	return NewContractCluster(c.id, w, s, d, h)
}

// WithElementPlaced returns a NEW cluster with the element crewed by shipSymbol in the
// named role repositioned to waypoint, preserving its identity and slice order. It is
// the parametrized positioning op (e.g. parking a delivery hull at its warehouse per the
// analyst's co-location policy) — it invents no placement, the caller supplies the
// waypoint. It errors when no such element exists (place repositions an existing member,
// use WithElementAdded to introduce one).
func (c *ContractCluster) WithElementPlaced(role Role, shipSymbol, waypoint string) (*ContractCluster, error) {
	w, s, d, h := c.Warehouses(), c.Stockers(), c.DeliveryHulls(), c.SourceHubs()
	var target *[]Element
	switch role {
	case RoleWarehouse:
		target = &w
	case RoleStocker:
		target = &s
	case RoleDeliveryHull:
		target = &d
	case RoleSourceHub:
		target = &h
	default:
		return nil, fmt.Errorf("unknown cluster element role %q", role)
	}
	placed := false
	for i := range *target {
		if (*target)[i].ShipSymbol == shipSymbol {
			(*target)[i].Waypoint = waypoint
			placed = true
			break
		}
	}
	if !placed {
		return nil, fmt.Errorf("no %s element crewed by ship %q in cluster %q to place", role, shipSymbol, c.id)
	}
	return NewContractCluster(c.id, w, s, d, h)
}

// removeByShip returns src without the first element crewed by shipSymbol and whether one
// was found. The input is a defensive copy (the accessor already cloned it), so the
// filtered slice never aliases the receiver's backing array.
func removeByShip(src []Element, shipSymbol string) ([]Element, bool) {
	for i := range src {
		if src[i].ShipSymbol == shipSymbol {
			return append(src[:i:i], src[i+1:]...), true
		}
	}
	return src, false
}
