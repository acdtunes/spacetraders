package contractscaler

import (
	"math"
	"reflect"
	"testing"
)

// Era-5 anchor symbols used as MUTATION HANDLES below. They are the fixture's expected
// OUTPUTS (asserted in TestResolveRoles_ResolvesTheFourEraInvariantAnchorsAcrossThreeRealEras),
// reused here to name which waypoint a negative case breaks. Production code never names one.
const (
	era5HAnchor          = "X1-KP23-H49"
	era5EAnchor          = "X1-KP23-E42"
	era5EAnchorMoon      = "X1-KP23-E43"
	era5FarSinkAnchor    = "X1-KP23-J56"
	era5FarSourceAnchor  = "X1-KP23-B7"
	era5NextEStackAnchor = "X1-KP23-G47" // the next-innermost planet+moon stack with no orbital station
)

// THE ACCEPTANCE PROOF (sp-9suun): the four standby anchors resolve identically across three
// eras with three DIFFERENT home systems and three DIFFERENT numberings, from durable charted
// facts only (type + traits + geometry) — never a waypoint symbol. The far sink's suffix moves
// J58→J59→J56 for one physical role; the H/E stacks renumber; the resolver does not care.
func TestResolveRoles_ResolvesTheFourEraInvariantAnchorsAcrossThreeRealEras(t *testing.T) {
	for _, tc := range []struct {
		era  string
		want EraAnchors
	}{
		{"era3", EraAnchors{HStack: "X1-VB74-H51", FarSink: "X1-VB74-J58", FarSourceBase: "X1-VB74-B7", EStack: "X1-VB74-E44"}},
		{"era4", EraAnchors{HStack: "X1-UM5-H52", FarSink: "X1-UM5-J59", FarSourceBase: "X1-UM5-B7", EStack: "X1-UM5-E42"}},
		{"era5", EraAnchors{HStack: "X1-KP23-H49", FarSink: "X1-KP23-J56", FarSourceBase: "X1-KP23-B7", EStack: "X1-KP23-E42"}},
	} {
		t.Run(tc.era, func(t *testing.T) {
			got := ResolveRoles(loadEraWaypoints(t, tc.era)).Anchors
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%s anchors = %+v, want %+v", tc.era, got, tc.want)
			}
		})
	}
}

// Every resolved anchor must be PARKABLE and SPREAD: on-site fuel (a hull that cannot refuel
// where it stands is stranded — era-5 E44 is the unfuelled member the E anchor must avoid) and
// four DISTINCT locations (one hull per park; two anchors on one coordinate waste a slot).
// Also pins the far anchors inside their sanity bands (600-850u sink, 300-450u source base).
func TestResolveRoles_EveryAnchorIsFuelledAndAtItsOwnLocationInEveryEra(t *testing.T) {
	for _, era := range []string{"era3", "era4", "era5"} {
		t.Run(era, func(t *testing.T) {
			markets := loadEraWaypoints(t, era)
			index := waypointBySymbol(markets)
			anchors := ResolveRoles(markets).Anchors

			seen := map[[2]float64]string{}
			for i, symbol := range anchors.Ordered() {
				waypoint, known := index[symbol]
				if !known {
					t.Fatalf("anchor %d = %q is not a waypoint of this era", i, symbol)
				}
				if !waypoint.HasFuel {
					t.Fatalf("anchor %d = %q has no on-site fuel — a hull parked there is stranded", i, symbol)
				}
				coord := [2]float64{waypoint.X, waypoint.Y}
				if other, dup := seen[coord]; dup {
					t.Fatalf("anchors %q and %q share location %v — two hulls on one park", other, symbol, coord)
				}
				seen[coord] = symbol
			}

			sinkDist := radiusOf(index[anchors.FarSink])
			if sinkDist < farSinkMinRadius || sinkDist > farSinkMaxRadius {
				t.Fatalf("far sink %q at %.1fu is outside the %g-%gu sanity band", anchors.FarSink, sinkDist, farSinkMinRadius, farSinkMaxRadius)
			}
			baseDist := radiusOf(index[anchors.FarSourceBase])
			if baseDist <= centralBandRadius || baseDist > farSourceBaseMaxRadius {
				t.Fatalf("far source base %q at %.1fu is outside the %g-%gu sanity band", anchors.FarSourceBase, baseDist, centralBandRadius, farSourceBaseMaxRadius)
			}
		})
	}
}

// FAIL-OPEN AT THE RESOLVER: break exactly ONE anchor's durable predicate and that anchor
// resolves EMPTY (named in Misses() so the analyst can re-rank from the corpus) while the other
// three are untouched. No panic, no partial garbage — an era whose generator template changed
// degrades one slot, not the whole set. (TopDeliverySlots then substitutes the demand-ranked
// central set for the empty slot; see TestTopDeliverySlots_AMissingAnchorDegradesToTheCentralSet.)
func TestResolveRoles_ABrokenPredicateEmptiesOnlyItsOwnAnchor(t *testing.T) {
	era5 := loadEraWaypoints(t, "era5")
	full := ResolveRoles(era5).Anchors

	for _, tc := range []struct {
		name    string
		broken  []WaypointMarket
		missing string
	}{
		{
			// The H-stack's three moons stop being moons → no planet+3-moon stack anywhere.
			name:    "h_stack composition changed",
			broken:  retypeCoLocated(era5, era5HAnchor, typeMoon, "ASTEROID"),
			missing: AnchorHStack,
		},
		{
			name:    "far sink lost its PIRATE_BASE trait",
			broken:  withoutTrait(era5, era5FarSinkAnchor, traitPirateBase),
			missing: AnchorFarSink,
		},
		{
			name:    "far source base lost its OUTPOST trait",
			broken:  withoutTrait(era5, era5FarSourceAnchor, traitOutpost),
			missing: AnchorFarSourceBase,
		},
		{
			// Every central moon OUTSIDE the H-stack stops being a moon → the H-stack is the
			// only planet+moon stack left, and it is already claimed by slot 1.
			name:    "no planet+moon stack left besides the H-stack",
			broken:  retypeAwayFrom(era5, era5HAnchor, typeMoon, "ASTEROID"),
			missing: AnchorEStack,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveRoles(tc.broken).Anchors

			if !reflect.DeepEqual(got.Misses(), []string{tc.missing}) {
				t.Fatalf("Misses() = %v, want exactly [%s] (the one broken slot)", got.Misses(), tc.missing)
			}
			for i, name := range []string{AnchorHStack, AnchorFarSink, AnchorFarSourceBase, AnchorEStack} {
				if name == tc.missing {
					continue
				}
				if got.Ordered()[i] != full.Ordered()[i] {
					t.Fatalf("breaking %s also moved %s: %q, want the unbroken %q", tc.missing, name, got.Ordered()[i], full.Ordered()[i])
				}
			}
		})
	}
}

// The far anchors' SANITY BANDS are load-bearing, not decoration: a trait match at the wrong
// radius is a broken template, not the anchor. A PIRATE_BASE marketplace parked at the jump-gate
// ring (450u) and an OUTPOST asteroid base out at the fuel-station ring (600u) are both REJECTED
// — without the bands the resolver would park hulls on the wrong side of the system.
func TestResolveRoles_AFarAnchorOutsideItsSanityBandIsRejected(t *testing.T) {
	era5 := loadEraWaypoints(t, "era5")

	nearSink := ResolveRoles(movedTo(era5, era5FarSinkAnchor, 450, 0)).Anchors
	if nearSink.FarSink != "" {
		t.Fatalf("far sink at 450u resolved to %q, want empty (below the %gu floor)", nearSink.FarSink, farSinkMinRadius)
	}
	farSink := ResolveRoles(movedTo(era5, era5FarSinkAnchor, 900, 0)).Anchors
	if farSink.FarSink != "" {
		t.Fatalf("far sink at 900u resolved to %q, want empty (above the %gu ceiling)", farSink.FarSink, farSinkMaxRadius)
	}
	farBase := ResolveRoles(movedTo(era5, era5FarSourceAnchor, 600, 0)).Anchors
	if farBase.FarSourceBase != "" {
		t.Fatalf("far source base at 600u resolved to %q, want empty (above the %gu ceiling)", farBase.FarSourceBase, farSourceBaseMaxRadius)
	}
	nearBase := ResolveRoles(movedTo(era5, era5FarSourceAnchor, 200, 0)).Anchors
	if nearBase.FarSourceBase != "" {
		t.Fatalf("far source base at 200u resolved to %q, want empty (inside the %gu central band)", nearBase.FarSourceBase, centralBandRadius)
	}
}

// The far anchors are not "whatever sits at the right radius": the template's shells carry other
// charted waypoints too, so each predicate's TYPE and TRAIT clauses have to be consulted. These
// decoys are placed NEARER than the real anchors, so nearest-first would hand them the slot if
// any clause were dropped — the source base must still be an ASTEROID_BASE, and the sink must
// still be a MARKETPLACE (a fuel station is not a contract delivery endpoint).
func TestResolveRoles_AFarAnchorDecoyAtTheRightRadiusDoesNotWinTheSlot(t *testing.T) {
	era5 := loadEraWaypoints(t, "era5")

	outpostDecoy := WaypointMarket{
		Symbol: "X1-SYN-OUTPOST-DECOY", X: 320, Y: 0, Type: typeOrbitalStation,
		Traits: []string{traitMarketplace, traitOutpost}, IsMarketplace: true, HasFuel: true,
	}
	withOutpostDecoy := ResolveRoles(append(append([]WaypointMarket(nil), era5...), outpostDecoy)).Anchors
	if withOutpostDecoy.FarSourceBase != era5FarSourceAnchor {
		t.Fatalf("far source base = %q, want the ASTEROID_BASE %q — a nearer non-base OUTPOST market must not win",
			withOutpostDecoy.FarSourceBase, era5FarSourceAnchor)
	}

	fuelStationDecoy := WaypointMarket{
		Symbol: "X1-SYN-PIRATE-FUEL-DECOY", X: 650, Y: 0, Type: "FUEL_STATION",
		Traits: []string{"FUEL_STATION", traitPirateBase}, HasFuel: true,
	}
	withFuelDecoy := ResolveRoles(append(append([]WaypointMarket(nil), era5...), fuelStationDecoy)).Anchors
	if withFuelDecoy.FarSink != era5FarSinkAnchor {
		t.Fatalf("far sink = %q, want the MARKETPLACE %q — a nearer non-market pirate base must not win",
			withFuelDecoy.FarSink, era5FarSinkAnchor)
	}
}

// "Planet + THREE moons" means EXACTLY three. A richer stack INSIDE the H-stack's ring would win
// an at-least test on nearest-first, moving the anchor off the location three eras of corpus
// ranked #1 for contract sourcing. (Era 4's real four-moon G-stack sits OUTSIDE the H-stack, so
// only an inner one can steal the slot — which is what this decoy is.)
func TestResolveRoles_HStackRejectsARicherInnerStack(t *testing.T) {
	at := func(symbol, waypointType string) WaypointMarket {
		return WaypointMarket{Symbol: symbol, X: 30, Y: 0, Type: waypointType, IsMarketplace: true,
			Traits: []string{traitMarketplace}, HasFuel: true}
	}
	// A planet + FOUR moons at 30u — inside the real H-stack's ~45u ring. The orbital station
	// keeps it out of the E-stack predicate, so this isolates the moon COUNT.
	richer := append(append([]WaypointMarket(nil), loadEraWaypoints(t, "era5")...),
		at("X1-SYN-RICH-P", typePlanet), at("X1-SYN-RICH-M1", typeMoon), at("X1-SYN-RICH-M2", typeMoon),
		at("X1-SYN-RICH-M3", typeMoon), at("X1-SYN-RICH-M4", typeMoon), at("X1-SYN-RICH-S", typeOrbitalStation))

	anchors := ResolveRoles(richer).Anchors

	if anchors.HStack != era5HAnchor {
		t.Fatalf("H anchor = %q, want the exactly-three-moon stack %q — a richer INNER stack must not win", anchors.HStack, era5HAnchor)
	}
	if anchors.EStack != era5EAnchor {
		t.Fatalf("E anchor = %q, want %q — the station-bearing decoy is not an E-stack either", anchors.EStack, era5EAnchor)
	}
}

// The central anchors' tie-breaks are a determinism rule: identical rows must resolve to the
// identical anchor on every pass, or a restart starts moving hulls. Two stacks at the SAME radius
// resolve by lowest member symbol, and within the chosen stack two fuelled members of the SAME
// rank (here two moons, the planet unfuelled) resolve by lowest symbol.
func TestResolveRoles_CentralAnchorTiesResolveByLowestSymbol(t *testing.T) {
	at := func(symbol, waypointType string, x, y float64, fuelled bool) WaypointMarket {
		return WaypointMarket{Symbol: symbol, X: x, Y: y, Type: waypointType, IsMarketplace: true,
			Traits: []string{traitMarketplace}, HasFuel: fuelled}
	}

	// Two DISTINCT locations at radius 50, both planet+moon stacks with no station.
	tiedStacks := ResolveRoles([]WaypointMarket{
		at("X1-SYN-BB-P", typePlanet, -40, -30, true), at("X1-SYN-BB-M", typeMoon, -40, -30, true),
		at("X1-SYN-AA-P", typePlanet, 30, 40, true), at("X1-SYN-AA-M", typeMoon, 30, 40, true),
	}).Anchors
	if tiedStacks.EStack != "X1-SYN-AA-P" {
		t.Fatalf("E anchor across two equal-radius stacks = %q, want the lowest-symbol stack's planet X1-SYN-AA-P", tiedStacks.EStack)
	}

	// One stack, an UNFUELLED planet and two fuelled moons — the moons tie on preference rank.
	tiedMembers := ResolveRoles([]WaypointMarket{
		at("X1-SYN-CC-P", typePlanet, 60, 0, false),
		at("X1-SYN-CC-M2", typeMoon, 60, 0, true),
		at("X1-SYN-CC-M1", typeMoon, 60, 0, true),
	}).Anchors
	if tiedMembers.EStack != "X1-SYN-CC-M1" {
		t.Fatalf("E anchor across two equally-ranked fuelled moons = %q, want the lowest symbol X1-SYN-CC-M1", tiedMembers.EStack)
	}
}

// The CENTRAL anchors must be CENTRAL and must have the composition their name claims. Two
// decoys that each win the slot the moment a clause is relaxed:
//   - a stack of three moons with NO planet, placed INNER of the real H-stack (nearest-first
//     would take it if the "one planet" clause went);
//   - a FAR-band planet+moon stack in an era whose central band has none left. The E slot must
//     stay EMPTY there and fail open to the central set — parking a "central" standby hull
//     hundreds of units out is worse than not resolving the anchor at all.
func TestResolveRoles_CentralAnchorsRejectAPlanetlessStackAndTheFarBand(t *testing.T) {
	moon := func(symbol string, x, y float64) WaypointMarket {
		return WaypointMarket{Symbol: symbol, X: x, Y: y, Type: typeMoon, IsMarketplace: true,
			Traits: []string{traitMarketplace}, HasFuel: true}
	}
	planet := func(symbol string, x, y float64) WaypointMarket {
		return WaypointMarket{Symbol: symbol, X: x, Y: y, Type: typePlanet, IsMarketplace: true,
			Traits: []string{traitMarketplace}, HasFuel: true}
	}

	era5 := loadEraWaypoints(t, "era5")
	planetless := append(append([]WaypointMarket(nil), era5...),
		moon("X1-SYN-MOONONLY-1", 30, 0), moon("X1-SYN-MOONONLY-2", 30, 0), moon("X1-SYN-MOONONLY-3", 30, 0))
	if got := ResolveRoles(planetless).Anchors.HStack; got != era5HAnchor {
		t.Fatalf("H anchor = %q, want the real planet+3-moon stack %q — a nearer planetless 3-moon stack must not win", got, era5HAnchor)
	}

	// No central moons left anywhere, plus a far-band planet+moon stack at 500u.
	noCentralStack := append(retypeAwayFrom(retypeCoLocated(era5, era5HAnchor, typeMoon, "ASTEROID"), era5HAnchor, typeMoon, "ASTEROID"),
		planet("X1-SYN-FARSTACK-P", 500, 0), moon("X1-SYN-FARSTACK-M", 500, 0))
	anchors := ResolveRoles(noCentralStack).Anchors
	if anchors.EStack != "" {
		t.Fatalf("E anchor = %q, want empty — a FAR-band planet+moon stack is not a central park", anchors.EStack)
	}
	if anchors.HStack != "" {
		t.Fatalf("H anchor = %q, want empty — the far-band stack has no three moons either", anchors.HStack)
	}
}

// The template makes each far anchor unique TODAY, so the tie-breaks are a determinism rule
// rather than a preference — and an untested determinism rule is how a restart starts moving
// hulls. With two valid matches the NEAREST wins (least travel from home), and at equal radius
// the lower symbol wins, so the same era rows always resolve to the same anchor.
func TestResolveRoles_TwoValidFarMatchesResolveNearestThenLowestSymbol(t *testing.T) {
	pirateBase := func(symbol string, x, y float64) WaypointMarket {
		return WaypointMarket{Symbol: symbol, X: x, Y: y, Type: typeAsteroidBase, IsMarketplace: true,
			Traits: []string{traitMarketplace, traitPirateBase}, HasFuel: true}
	}
	outpostBase := func(symbol string, x, y float64) WaypointMarket {
		return WaypointMarket{Symbol: symbol, X: x, Y: y, Type: typeAsteroidBase, IsMarketplace: true,
			Traits: []string{traitMarketplace, traitOutpost}, HasFuel: true}
	}

	nearer := ResolveRoles([]WaypointMarket{
		pirateBase("X1-SYN-SINK-FAR", 800, 0),
		pirateBase("X1-SYN-SINK-NEAR", 650, 0),
		outpostBase("X1-SYN-BASE-FAR", 440, 0),
		outpostBase("X1-SYN-BASE-NEAR", 320, 0),
	}).Anchors
	if nearer.FarSink != "X1-SYN-SINK-NEAR" {
		t.Fatalf("far sink with two in-band matches = %q, want the NEARER one", nearer.FarSink)
	}
	if nearer.FarSourceBase != "X1-SYN-BASE-NEAR" {
		t.Fatalf("far source base with two in-band matches = %q, want the NEARER one", nearer.FarSourceBase)
	}

	tied := ResolveRoles([]WaypointMarket{
		pirateBase("X1-SYN-SINK-B", 0, 700),
		pirateBase("X1-SYN-SINK-A", 700, 0), // same radius, lower symbol
	}).Anchors
	if tied.FarSink != "X1-SYN-SINK-A" {
		t.Fatalf("far sink at equal radius = %q, want the lowest symbol X1-SYN-SINK-A", tied.FarSink)
	}
}

// A stack is parked on a FUELLED member, planet-first: era-5 E42 (planet) and E43 (moon) sell
// fuel, E44 does not. Strip the planet's fuel and the anchor slides to the fuelled MOON of the
// same stack; strip both and the stack is unparkable, so the E slot moves to the NEXT innermost
// planet+moon stack rather than parking a hull where it cannot refuel.
func TestResolveRoles_StackAnchorParksOnAFuelledMemberOrMovesOn(t *testing.T) {
	era5 := loadEraWaypoints(t, "era5")

	dryPlanet := ResolveRoles(withoutFuel(era5, era5EAnchor)).Anchors
	if dryPlanet.EStack != era5EAnchorMoon {
		t.Fatalf("E anchor with an unfuelled planet = %q, want the fuelled moon %q", dryPlanet.EStack, era5EAnchorMoon)
	}

	dryStack := ResolveRoles(withoutFuel(withoutFuel(era5, era5EAnchor), era5EAnchorMoon)).Anchors
	if dryStack.EStack != era5NextEStackAnchor {
		t.Fatalf("E anchor with an entirely unfuelled stack = %q, want the next parkable stack %q", dryStack.EStack, era5NextEStackAnchor)
	}
}

// ONE HULL PER LOCATION: the stack members co-located with a resolved anchor are dropped from
// the central-park pool, so the demand-ranked fill can never hand a second hull the same
// coordinate the anchor already occupies (era-5 H49/H50/H51/H52 and E42/E43/E44 each share one
// coordinate). The anchor itself stays a park.
func TestResolveRoles_CentralParksDropTheWaypointsCoLocatedWithAnAnchor(t *testing.T) {
	markets := loadEraWaypoints(t, "era5")
	index := waypointBySymbol(markets)
	roles := ResolveRoles(markets)

	anchoredCoords := map[[2]float64]string{}
	for _, anchor := range roles.Anchors.Ordered() {
		anchoredCoords[[2]float64{index[anchor].X, index[anchor].Y}] = anchor
	}
	for _, park := range roles.CentralParks {
		coord := [2]float64{index[park].X, index[park].Y}
		if anchor, anchored := anchoredCoords[coord]; anchored && anchor != park {
			t.Fatalf("central park %q sits on anchor %q's location %v — a second hull on one park", park, anchor, coord)
		}
	}
	if !containsSymbol(roles.CentralParks, roles.Anchors.HStack) || !containsSymbol(roles.CentralParks, roles.Anchors.EStack) {
		t.Fatalf("the central-band anchors must stay in the park pool, got %v", roles.CentralParks)
	}
}

// EraRoles.FarSink is REUSED, not duplicated: it now resolves from the DURABLE template (the
// unique PIRATE_BASE marketplace) and keeps its original farthest-far-band-importer rule as the
// fallback for an era whose template has no pirate base at all.
func TestResolveRoles_FarSinkPrefersTheDurableTemplateAndKeepsTheImporterFallback(t *testing.T) {
	era5 := loadEraWaypoints(t, "era5")
	if got := ResolveRoles(era5).FarSink; got != era5FarSinkAnchor {
		t.Fatalf("FarSink = %q, want the durable PIRATE_BASE marketplace %q", got, era5FarSinkAnchor)
	}

	fallback := ResolveRoles([]WaypointMarket{
		{Symbol: "X1-SYN-CENTRAL", X: 40, Y: 0, IsMarketplace: true, Imports: []string{"FOOD"}},
		{Symbol: "X1-SYN-NEARIMPORT", X: 400, Y: 0, Imports: []string{"FOOD"}},
		{Symbol: "X1-SYN-FARIMPORT", X: 700, Y: 0, Imports: []string{"FOOD"}},
	})
	if fallback.FarSink != "X1-SYN-FARIMPORT" {
		t.Fatalf("FarSink without a pirate base = %q, want the farthest far-band importer", fallback.FarSink)
	}
	if fallback.Anchors.FarSink != "" {
		t.Fatalf("the far-sink ANCHOR must stay empty without the durable trait, got %q", fallback.Anchors.FarSink)
	}
}

// Misses() names the unresolved slots in PLACEMENT ORDER — the payload of the miss log the
// analyst re-ranks from. A fully resolved era reports nothing.
func TestEraAnchors_MissesNamesTheUnresolvedSlotsInPlacementOrder(t *testing.T) {
	if got := (EraAnchors{HStack: "A", FarSink: "B", FarSourceBase: "C", EStack: "D"}).Misses(); len(got) != 0 {
		t.Fatalf("Misses() on a fully resolved era = %v, want none", got)
	}
	got := (EraAnchors{FarSink: "B", EStack: "D"}).Misses()
	if !reflect.DeepEqual(got, []string{AnchorHStack, AnchorFarSourceBase}) {
		t.Fatalf("Misses() = %v, want [%s %s] in placement order", got, AnchorHStack, AnchorFarSourceBase)
	}
}

// Deterministic + restart-idempotent (#2): the anchors are a pure function of the era's charted
// rows, not of the order the waypoint store happens to return them in.
func TestResolveRoles_AnchorsAreIndependentOfWaypointInputOrder(t *testing.T) {
	markets := loadEraWaypoints(t, "era5")
	want := ResolveRoles(markets).Anchors

	reversed := make([]WaypointMarket, 0, len(markets))
	for i := len(markets) - 1; i >= 0; i-- {
		reversed = append(reversed, markets[i])
	}
	if got := ResolveRoles(reversed).Anchors; !reflect.DeepEqual(got, want) {
		t.Fatalf("reversed input resolved to %+v, want %+v", got, want)
	}
}

// --- fixture mutators: each breaks exactly ONE durable fact of ONE waypoint (or stack) ---

func radiusOf(m WaypointMarket) float64 { return math.Hypot(m.X, m.Y) }

func containsSymbol(symbols []string, want string) bool {
	for _, symbol := range symbols {
		if symbol == want {
			return true
		}
	}
	return false
}

func coordOfSymbol(markets []WaypointMarket, symbol string) [2]float64 {
	for _, m := range markets {
		if m.Symbol == symbol {
			return [2]float64{m.X, m.Y}
		}
	}
	return [2]float64{math.NaN(), math.NaN()}
}

// retypeCoLocated retypes every waypoint sharing anchor's coordinate (the anchor's STACK).
func retypeCoLocated(markets []WaypointMarket, anchor, fromType, toType string) []WaypointMarket {
	return retype(markets, anchor, fromType, toType, true)
}

// retypeAwayFrom retypes every waypoint NOT sharing anchor's coordinate.
func retypeAwayFrom(markets []WaypointMarket, anchor, fromType, toType string) []WaypointMarket {
	return retype(markets, anchor, fromType, toType, false)
}

func retype(markets []WaypointMarket, anchor, fromType, toType string, coLocated bool) []WaypointMarket {
	target := coordOfSymbol(markets, anchor)
	out := append([]WaypointMarket(nil), markets...)
	for i, m := range out {
		if m.Type != fromType || ([2]float64{m.X, m.Y} == target) != coLocated {
			continue
		}
		out[i].Type = toType
	}
	return out
}

func withoutTrait(markets []WaypointMarket, symbol, trait string) []WaypointMarket {
	out := append([]WaypointMarket(nil), markets...)
	for i, m := range out {
		if m.Symbol != symbol {
			continue
		}
		kept := make([]string, 0, len(m.Traits))
		for _, t := range m.Traits {
			if t != trait {
				kept = append(kept, t)
			}
		}
		out[i].Traits = kept
	}
	return out
}

func withoutFuel(markets []WaypointMarket, symbol string) []WaypointMarket {
	out := append([]WaypointMarket(nil), markets...)
	for i, m := range out {
		if m.Symbol == symbol {
			out[i].HasFuel = false
		}
	}
	return out
}

func movedTo(markets []WaypointMarket, symbol string, x, y float64) []WaypointMarket {
	out := append([]WaypointMarket(nil), markets...)
	for i, m := range out {
		if m.Symbol == symbol {
			out[i].X, out[i].Y = x, y
		}
	}
	return out
}
