// Package gate holds the gate-construction delivery fleet's POLICY objects: the role
// vocabulary, the supply-anchored buy/pause rule, and the greedy mixed fill.
//
// Everything here is pure. No I/O, no clock, no repository — a policy that cannot be
// exercised without a fixture is a policy that gets tested by running the fleet, which
// is exactly the opacity this design exists to remove. It is the gate-side parallel of
// internal/domain/contract/depot: one container, several roles.
package gate

// Role names one of the two gate-construction worker roles. One container type, two
// roles, assigned in real time via the ship's dedicated_fleet tag — the depot.Role
// precedent, not a second container type.
type Role int

const (
	// RoleDelivery buys terminal-factory OUTPUT and hauls it to the gate. It never fabricates.
	RoleDelivery Role = iota
	// RoleFactory feeds the production chain INTO the terminal factories. It never touches
	// their output. (Phase 3 gives this role its behaviour; phase 2 only tags for it.)
	RoleFactory
)

const (
	// LegacyFleetTag is the pre-role gate tag. Hulls carrying it have NO role and stay on
	// the existing path until phase 3's reallocation re-roles them. It is still a GATE tag:
	// the observer counts all three, which is what keeps the worker ramp stopping at its
	// target instead of ramping past a total that undercounts live hulls.
	LegacyFleetTag = "manufacturing"
	// DeliveryFleetTag and FactoryFleetTag are BOTH the dedicated_fleet column value AND the
	// ClaimShip operation string for their role. They must be one value each, because
	// ClaimShip authorizes a new claim only when the hull's tag EQUALS the operation: a hull
	// discovered by tag but claimed under a different identity is rejected at the DB and
	// silently never works.
	DeliveryFleetTag = "gate-delivery"
	FactoryFleetTag  = "gate-factory"
)

// ConstructionReleaseReason is stamped when a gate leg ends; the general pool's hand-back hold reads it.
const ConstructionReleaseReason = "construction_supply_complete"

// roleTags is the single source of truth for the Role <-> tag mapping, so String,
// FleetTag and ParseFleetTag cannot drift apart.
var roleTags = map[Role]string{
	RoleDelivery: DeliveryFleetTag,
	RoleFactory:  FactoryFleetTag,
}

// purchaseOrder is the GATE phase's hull-buying order: delivery, factory, factory, delivery.
//
// The interleaving is the point, and it is load-bearing at every PARTIAL state — which is
// the state that actually occurs when treasury is tight. First hull is delivery so any
// already-accumulated factory stock starts moving immediately; stopping after two leaves
// one of each rather than two of the same.
var purchaseOrder = [...]Role{RoleDelivery, RoleFactory, RoleFactory, RoleDelivery}

// String is the stable, operator-facing name of the role (log lines, CLI output).
func (r Role) String() string {
	switch r {
	case RoleDelivery:
		return "delivery"
	case RoleFactory:
		return "factory"
	default:
		return "unknown"
	}
}

// FleetTag is the dedicated_fleet tag — and therefore the ClaimShip operation — for the role.
func (r Role) FleetTag() string { return roleTags[r] }

// ParseFleetTag maps a dedicated_fleet tag back to its Role. ok is false for the legacy
// tag, for a foreign fleet, and for the undedicated empty tag: only a ROLE-tagged hull
// has a role, and a caller must not infer one for anything else.
func ParseFleetTag(tag string) (Role, bool) {
	for role, t := range roleTags {
		if t == tag {
			return role, true
		}
	}
	return 0, false
}

// IsGateFleetTag reports whether tag belongs to the gate workforce at all — the two role
// tags PLUS the legacy one. This is the observer's count predicate and the re-tag guards'
// membership test. Deliberately broader than ParseFleetTag: a legacy hull has no role but
// is unmistakably a gate worker, and treating it as anything else would let the ramp
// over-buy against a total that ignores it.
func IsGateFleetTag(tag string) bool {
	if tag == LegacyFleetTag {
		return true
	}
	_, ok := ParseFleetTag(tag)
	return ok
}

// RoleFleetTags lists the ROLE tags, for discovery that must look up each pool separately.
// The legacy tag is deliberately absent: the drain already discovers it as its default
// identity, and listing it here would make discovery scan that pool twice.
func RoleFleetTags() []string {
	return []string{DeliveryFleetTag, FactoryFleetTag}
}

// NextRole is the role the NEXT gate hull should carry, derived purely from how many of
// each the fleet already holds. Deriving it from LIVE counts rather than from a stored
// cursor is what makes the order correct after a restart, a manual re-tag, or a hull loss:
// there is no cursor to get out of step with the fleet.
//
// TOTAL by construction — it cycles the 4-slot order — so a count past the target (a
// miscount, an operator re-tag) still answers, and still answers in balance.
func NextRole(delivery, factory int) Role {
	if delivery < 0 {
		delivery = 0
	}
	if factory < 0 {
		factory = 0
	}
	return purchaseOrder[(delivery+factory)%len(purchaseOrder)]
}
