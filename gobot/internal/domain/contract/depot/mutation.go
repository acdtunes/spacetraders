package depot

import "fmt"

// Role names one of a depot's four element classes. It is the parameter the CLI's
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
	return 0, fmt.Errorf("unknown depot element role %q (want one of warehouse, stocker, delivery-hull, source-hub)", name)
}

// roleTarget returns a pointer to the working slice for the named role, so each
// mutation touches exactly the element class the caller named.
func roleTarget(role Role, warehouses, stockers, deliveryHulls, sourceHubs *[]Element) (*[]Element, error) {
	switch role {
	case RoleWarehouse:
		return warehouses, nil
	case RoleStocker:
		return stockers, nil
	case RoleDeliveryHull:
		return deliveryHulls, nil
	case RoleSourceHub:
		return sourceHubs, nil
	default:
		return nil, fmt.Errorf("unknown depot element role %q", role)
	}
}

// WithElementAdded returns a NEW depot with e appended to the named role's elements,
// leaving the receiver untouched (immutable functional update). The reconstruction runs
// through NewContractDepot so the depot invariants hold for the result.
func (c *ContractDepot) WithElementAdded(role Role, e Element) (*ContractDepot, error) {
	w, s, d, h := c.Warehouses(), c.Stockers(), c.DeliveryHulls(), c.SourceHubs()
	target, err := roleTarget(role, &w, &s, &d, &h)
	if err != nil {
		return nil, err
	}
	*target = append(*target, e)
	return NewContractDepot(c.id, w, s, d, h)
}

// WithElementRemoved returns a NEW depot with the element crewed by shipSymbol dropped
// from the named role, leaving the receiver untouched. It errors when no element in that
// role is crewed by shipSymbol (so the CLI can report "nothing to remove"), and — via
// NewContractDepot — when the removal would leave the depot with no destination
// warehouse (the one structural invariant).
func (c *ContractDepot) WithElementRemoved(role Role, shipSymbol string) (*ContractDepot, error) {
	w, s, d, h := c.Warehouses(), c.Stockers(), c.DeliveryHulls(), c.SourceHubs()
	target, err := roleTarget(role, &w, &s, &d, &h)
	if err != nil {
		return nil, err
	}
	filtered, found := removeByShip(*target, shipSymbol)
	if !found {
		return nil, fmt.Errorf("no %s element crewed by ship %q in depot %q", role, shipSymbol, c.id)
	}
	*target = filtered
	return NewContractDepot(c.id, w, s, d, h)
}

// WithElementPlaced returns a NEW depot with the element crewed by shipSymbol in the
// named role repositioned to waypoint, preserving its identity and slice order. It is
// the parametrized positioning op (e.g. parking a delivery hull at its warehouse per the
// analyst's co-location policy) — it invents no placement, the caller supplies the
// waypoint. It errors when no such element exists (place repositions an existing member,
// use WithElementAdded to introduce one).
func (c *ContractDepot) WithElementPlaced(role Role, shipSymbol, waypoint string) (*ContractDepot, error) {
	w, s, d, h := c.Warehouses(), c.Stockers(), c.DeliveryHulls(), c.SourceHubs()
	target, err := roleTarget(role, &w, &s, &d, &h)
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("no %s element crewed by ship %q in depot %q to place", role, shipSymbol, c.id)
	}
	return NewContractDepot(c.id, w, s, d, h)
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
