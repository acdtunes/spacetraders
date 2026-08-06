package contractscaler

import (
	"math"
	"sort"
)

// The ERA-INVARIANT STANDBY ANCHORS.
//
// Idle contract hulls used to be parked only on the demand-ranked CENTRAL parks, because the
// candidate set came from the inner band alone (isCentralSink caps candidates at
// centralBandRadius). The universe's richest contract endpoints sit OUTSIDE that band and could
// therefore never be parked: measured over the era-5 corpus the expected idle-hull→source leg
// was 82.8u under the central-only top-6, against 35.3u for the four anchors below — four hulls
// placed right beat six placed central.
//
// The four anchors are a fixed GENERATOR TEMPLATE, verified identical across eras 3, 4 and 5
// (three different home systems, three different numberings). Waypoint NUMBERING reshuffles
// every era — the far sink's suffix moved J58→J59→J56 for one physical role — so every anchor
// resolves from DURABLE charted facts (stack composition, charted traits, radius from the star)
// and NEVER from a symbol. In placement order:
//
//	(1) H-STACK          the central location whose stack is a planet + THREE moons (a
//	                     composition unique in every measured era) — the #1 contract SOURCE
//	                     location every era, four co-located markets in one spot.
//	(2) FAR SINK         the charted PIRATE_BASE marketplace out at ~720u — the #1 delivery
//	                     sink (28.5% of all contract payment in era 5), and the single biggest
//	                     marginal cut in the greedy placement ranking.
//	(3) FAR SOURCE BASE  the OUTPOST ASTEROID_BASE marketplace just beyond the central band at
//	                     ~345u — the precious-metal/stone source arm.
//	(4) E-STACK          the innermost central planet+moon stack with NO orbital station (which
//	                     excludes the A- and F-stacks) — the #2 source location every era.
//
// Every anchor FAILS OPEN: a template that no longer matches yields "" for that anchor alone,
// and TopDeliverySlots substitutes the next demand-ranked central park for it. A broken era
// template degrades one slot; it never panics and never leaves a hole.
const (
	// AnchorHStack, AnchorFarSink, AnchorFarSourceBase, AnchorEStack are the anchors' stable
	// slot NAMES — the payload of the miss log the analyst re-ranks the corpus from.
	AnchorHStack        = "h_stack"
	AnchorFarSink       = "far_sink"
	AnchorFarSourceBase = "far_source_base"
	AnchorEStack        = "e_stack"
)

// The DURABLE charted facts the anchors key on. Trait and type symbols are universe
// vocabulary — stable across eras — unlike waypoint symbols, which are not.
const (
	traitMarketplace = "MARKETPLACE"
	traitPirateBase  = "PIRATE_BASE"
	traitOutpost     = "OUTPOST"

	typePlanet         = "PLANET"
	typeMoon           = "MOON"
	typeOrbitalStation = "ORBITAL_STATION"
	typeAsteroidBase   = "ASTEROID_BASE"
)

const (
	// hStackMoonCount is the H-stack's unique composition: one planet and exactly THREE
	// co-located moons (the A-stacks carry two, era 4's G-stack four). "Exactly" is what keeps
	// the anchor pinned to ONE ring: an at-least test would also admit a richer stack, and the
	// innermost such stack would take the slot — which is not the location the corpus ranked #1.
	// (Era 4's four-moon G-stack sits OUTSIDE the H-stack, so only an inner one could steal it —
	// see TestResolveRoles_HStackRejectsARicherInnerStack.)
	hStackMoonCount = 3

	// farSinkMinRadius / farSinkMaxRadius bound the far sink's SANITY CHECK. Measured 719.8u
	// (era 5), 722.3u (era 4), 721.6u (era 3); the band is deliberately wide. A pirate base
	// charted at a radius the template never produces is a template that CHANGED, so the
	// anchor fails open rather than parking a hull on the wrong side of the system.
	farSinkMinRadius = 600.0
	farSinkMaxRadius = 850.0

	// farSourceBaseMaxRadius is the far source base's sanity CEILING; its floor is
	// centralBandRadius itself (the "beyond the central band" cut and the 300u sanity floor
	// coincide). Measured 343.5u / 346.2u / 344.9u across eras 5 / 4 / 3, with the next
	// template ring — the jump-gate marketplace — out at ~450u.
	farSourceBaseMaxRadius = 450.0
)

// EraAnchors holds the four era-invariant standby anchors this era resolved. An empty field is
// a MISS: that anchor's durable predicate matched nothing, and its slot degrades to the
// demand-ranked central set.
type EraAnchors struct {
	HStack        string
	FarSink       string
	FarSourceBase string
	EStack        string
}

// Ordered returns the four anchors in PLACEMENT ORDER — (1) H-stack, (2) far sink, (3) far
// source base, (4) E-stack — the greedy marginal ranking over the era-5 corpus (each step the
// biggest remaining cut in the expected idle-hull→source leg, given the earlier picks). The
// order is what makes truncation correct when the fleet is smaller than the set: fewer hulls
// drop the LAST slots, never the first two.
func (a EraAnchors) Ordered() []string {
	return []string{a.HStack, a.FarSink, a.FarSourceBase, a.EStack}
}

// Misses returns the slot NAMES whose durable predicate matched nothing this era, in placement
// order — nil when the template resolved whole. Callers LOG it so the analyst can re-rank the
// slot from the contract corpus instead of discovering the silent degradation later.
func (a EraAnchors) Misses() []string {
	names := []string{AnchorHStack, AnchorFarSink, AnchorFarSourceBase, AnchorEStack}
	var missed []string
	for index, symbol := range a.Ordered() {
		if symbol == "" {
			missed = append(missed, names[index])
		}
	}
	return missed
}

// resolveAnchors resolves the four anchors from this era's charted waypoints. Deterministic: a
// pure function of the rows, never of the order the waypoint store returned them in. The
// E-stack is resolved LAST and skips the stack the H-stack anchor already claimed, so the two
// central anchors are always two distinct LOCATIONS (the H-stack is itself the innermost
// planet+moon stack, so without the exclusion both slots would land on it).
func resolveAnchors(markets []WaypointMarket) EraAnchors {
	stacks := centralStacks(markets)
	anchors := EraAnchors{
		HStack:        stackAnchor(stacks, isHStack, nil),
		FarSink:       farAnchor(markets, isFarSink),
		FarSourceBase: farAnchor(markets, isFarSourceBase),
	}
	anchors.EStack = stackAnchor(stacks, isEStack, claimedSymbols(anchors.HStack))
	return anchors
}

// centralStack is one central-band LOCATION: the co-located waypoints sharing a coordinate (a
// planet and its moons and any station in the same orbit), with the location's radius from the
// star. Stacks are the unit the central anchors identify, because each ring in the template is
// ONE physical spot carrying several stacked markets.
type centralStack struct {
	radius  float64
	members []WaypointMarket
}

// centralStacks groups the inner band into locations, nearest-first (symbol-tiebroken), so
// "innermost matching stack" is a well-defined, order-independent pick.
func centralStacks(markets []WaypointMarket) []centralStack {
	byCoord := map[[2]float64][]WaypointMarket{}
	for _, m := range markets {
		if math.Hypot(m.X, m.Y) > centralBandRadius {
			continue
		}
		coord := [2]float64{m.X, m.Y}
		byCoord[coord] = append(byCoord[coord], m)
	}

	stacks := make([]centralStack, 0, len(byCoord))
	for coord, members := range byCoord {
		sort.Slice(members, func(i, j int) bool { return members[i].Symbol < members[j].Symbol })
		stacks = append(stacks, centralStack{radius: math.Hypot(coord[0], coord[1]), members: members})
	}
	sort.SliceStable(stacks, func(i, j int) bool {
		if stacks[i].radius != stacks[j].radius {
			return stacks[i].radius < stacks[j].radius
		}
		return stacks[i].members[0].Symbol < stacks[j].members[0].Symbol
	})
	return stacks
}

func (s centralStack) count(waypointType string) int {
	total := 0
	for _, m := range s.members {
		if m.Type == waypointType {
			total++
		}
	}
	return total
}

func (s centralStack) holdsAny(claimed map[string]bool) bool {
	for _, m := range s.members {
		if claimed[m.Symbol] {
			return true
		}
	}
	return false
}

// parkable returns the stack member a hull should actually sit on: a FUELLED one, preferring the
// planet, then a moon, then anything else, symbol-tiebroken. Fuel is a hard requirement — era-5
// E44 is a moon with no market and no fuel, and a hull parked there cannot refuel to leave.
// "" when no member sells fuel, which makes the whole stack unparkable.
func (s centralStack) parkable() string {
	best, bestRank := "", 0
	for _, m := range s.members {
		if !m.HasFuel {
			continue
		}
		rank := parkPreference(m.Type)
		if best == "" || rank < bestRank || (rank == bestRank && m.Symbol < best) {
			best, bestRank = m.Symbol, rank
		}
	}
	return best
}

func parkPreference(waypointType string) int {
	switch waypointType {
	case typePlanet:
		return 0
	case typeMoon:
		return 1
	}
	return 2
}

// stackAnchor returns the parkable member of the innermost stack that matches, skipping stacks
// already claimed by an earlier anchor and stacks with no fuelled member. "" = no match: the
// era template changed, and the slot fails open to the central set.
func stackAnchor(stacks []centralStack, matches func(centralStack) bool, claimed map[string]bool) string {
	for _, stack := range stacks {
		if stack.holdsAny(claimed) || !matches(stack) {
			continue
		}
		if member := stack.parkable(); member != "" {
			return member
		}
	}
	return ""
}

// isHStack: one planet with exactly three co-located moons.
func isHStack(s centralStack) bool {
	return s.count(typePlanet) == 1 && s.count(typeMoon) == hStackMoonCount
}

// isEStack: a planet+moon stack with NO orbital station — the clause that excludes the A-stack
// (planet + 2 moons + station) and the F-stack (planet + station) in every measured era.
func isEStack(s centralStack) bool {
	return s.count(typePlanet) >= 1 && s.count(typeMoon) >= 1 && s.count(typeOrbitalStation) == 0
}

// farAnchor returns the NEAREST waypoint matching a far-anchor predicate (symbol-tiebroken).
// The template makes each far anchor unique, so nearest-first is a determinism rule rather than
// a preference; it also keeps the pick closest to home if a future era charts two.
func farAnchor(markets []WaypointMarket, matches func(WaypointMarket, float64) bool) string {
	best, bestRadius := "", 0.0
	for _, m := range markets {
		radius := math.Hypot(m.X, m.Y)
		if !matches(m, radius) {
			continue
		}
		if best == "" || radius < bestRadius || (radius == bestRadius && m.Symbol < best) {
			best, bestRadius = m.Symbol, radius
		}
	}
	return best
}

// isFarSink: the charted PIRATE_BASE marketplace in the far sink's sanity band, with fuel on
// site (a standby slot a hull can leave again).
func isFarSink(m WaypointMarket, radius float64) bool {
	return m.HasFuel && hasTrait(m, traitMarketplace) && hasTrait(m, traitPirateBase) &&
		radius >= farSinkMinRadius && radius <= farSinkMaxRadius
}

// isFarSourceBase: the OUTPOST ASTEROID_BASE marketplace beyond the central band and inside the
// source-base sanity ceiling, with fuel on site.
func isFarSourceBase(m WaypointMarket, radius float64) bool {
	return m.HasFuel && m.Type == typeAsteroidBase &&
		hasTrait(m, traitMarketplace) && hasTrait(m, traitOutpost) &&
		radius > centralBandRadius && radius <= farSourceBaseMaxRadius
}

func hasTrait(m WaypointMarket, trait string) bool {
	for _, charted := range m.Traits {
		if charted == trait {
			return true
		}
	}
	return false
}

func claimedSymbols(symbols ...string) map[string]bool {
	claimed := make(map[string]bool, len(symbols))
	for _, symbol := range symbols {
		if symbol != "" {
			claimed[symbol] = true
		}
	}
	return claimed
}

// pruneAnchorCoLocated drops every central park that shares a resolved anchor's coordinate,
// keeping the anchor itself. The standby set is ONE HULL PER LOCATION: without this, the
// demand-ranked fill would hand a second hull a different symbol at the SAME spot (era-5
// H49/H50/H51/H52 are four markets on one coordinate) and waste a placement slot.
func pruneAnchorCoLocated(parks []string, markets []WaypointMarket, anchors EraAnchors) []string {
	coordOf := make(map[string][2]float64, len(markets))
	for _, m := range markets {
		coordOf[m.Symbol] = [2]float64{m.X, m.Y}
	}

	anchored := map[[2]float64]string{}
	for _, anchor := range anchors.Ordered() {
		if anchor == "" {
			continue
		}
		anchored[coordOf[anchor]] = anchor
	}
	if len(anchored) == 0 {
		return parks
	}

	kept := make([]string, 0, len(parks))
	for _, park := range parks {
		if holder, occupied := anchored[coordOf[park]]; occupied && holder != park {
			continue
		}
		kept = append(kept, park)
	}
	return kept
}
