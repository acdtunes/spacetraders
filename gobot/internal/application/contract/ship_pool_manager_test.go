package contract

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// stubShipRepo serves a fixed fleet from FindAllByPlayer and leaves every other
// ShipRepository method embedded (nil), so a test panics loudly if candidate
// discovery ever reaches for something other than the full fleet snapshot.
type stubShipRepo struct {
	navigation.ShipRepository
	ships []*navigation.Ship
}

func (r *stubShipRepo) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	return r.ships, nil
}

// newCandidateShip builds an idle, docked ship at (x,y) with the given symbol,
// role and cargo capacity - the minimum surface a coordinator inspects when
// deciding whether a hull is a haul candidate.
func newCandidateShip(t *testing.T, symbol, role string, cargoCap int, x, y float64) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(cargoCap, 0, nil)
	if err != nil {
		t.Fatalf("build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("build fuel: %v", err)
	}
	wp, err := shared.NewWaypoint("X1-TW-A2", x, y)
	if err != nil {
		t.Fatalf("build waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		wp,
		fuel,
		100,
		cargoCap,
		cargo,
		30,
		"FRAME_FRIGATE",
		role,
		nil,
		navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("build ship: %v", err)
	}
	return ship
}

// newCandidateShipWithCargo builds an idle, docked ship already carrying the
// given inventory - newCandidateShip always builds an empty hold, which
// can't exercise the NO-CARGO-DUMP CLAIM GUARD (sp-wq7r).
func newCandidateShipWithCargo(t *testing.T, symbol, role string, cargoCap int, x, y float64, inventory []*shared.CargoItem) *navigation.Ship {
	t.Helper()
	units := 0
	for _, item := range inventory {
		units += item.Units
	}
	cargo, err := shared.NewCargo(cargoCap, units, inventory)
	if err != nil {
		t.Fatalf("build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("build fuel: %v", err)
	}
	wp, err := shared.NewWaypoint("X1-TW-A2", x, y)
	if err != nil {
		t.Fatalf("build waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol,
		shared.MustNewPlayerID(1),
		wp,
		fuel,
		100,
		cargoCap,
		cargo,
		30,
		"FRAME_FRIGATE",
		role,
		nil,
		navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("build ship: %v", err)
	}
	return ship
}

func containsSymbol(symbols []string, want string) bool {
	for _, s := range symbols {
		if s == want {
			return true
		}
	}
	return false
}

// The contract coordinator must treat the command ship as a first-class haul
// candidate, not a zero-hauler fallback. Before sp-4a4e a 40-cargo COMMAND
// frigate sat benched for hours whenever any hauler existed, because it only
// entered the pool when NO haulers existed at all - so a free, fast hull
// contributed nothing while a light hauler flew oversized legs.
func TestFindIdleLightHaulers_IncludesIdleCommandShipAlongsideHaulers(t *testing.T) {
	hauler := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)  // idle, far
	command := newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 50, 0) // idle, close, command
	repo := &stubShipRepo{ships: []*navigation.Ship{hauler, command}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if !containsSymbol(symbols, "TORWIND-1") {
		t.Fatalf("command ship TORWIND-1 excluded from candidate pool %v - it is idle and must be a first-class haul candidate, not a benched fallback", symbols)
	}
	if !containsSymbol(symbols, "TORWIND-3") {
		t.Fatalf("hauler TORWIND-3 missing from candidate pool %v", symbols)
	}
}

// Acceptance (sp-4a4e): with the only hauler busy and the command ship idle, the
// coordinator must be able to dispatch the command ship - not fall through to an
// empty pool and wait 5h+ while a 40-cargo hull sits docked. The fallback-only
// design returned nothing here because a (busy) hauler existed.
func TestFindIdleLightHaulers_BusyHauler_IdleCommandShip_CommandIsCandidate(t *testing.T) {
	hauler := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)
	if err := hauler.AssignToContainer("contract-worker-TORWIND-3", shared.NewRealClock()); err != nil {
		t.Fatalf("assign hauler busy: %v", err)
	}
	command := newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 50, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{hauler, command}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if len(symbols) != 1 || symbols[0] != "TORWIND-1" {
		t.Fatalf("expected only the idle command ship [TORWIND-1] as candidate while the hauler is busy, got %v", symbols)
	}
}

// Scope guard: manufacturing/factory coordinators call FindIdleLightHaulers
// without opting in (ExcludeCommandShip default), and must never draft the
// command ship - it stays reserved for contracts and manual operations. Only
// haulers return.
func TestFindIdleLightHaulers_ExcludesCommandShip_WhenNotOptedIn(t *testing.T) {
	hauler := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)
	command := newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 50, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{hauler, command}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "")
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if containsSymbol(symbols, "TORWIND-1") {
		t.Fatalf("command ship TORWIND-1 must stay out of the manufacturing pool %v", symbols)
	}
	if !containsSymbol(symbols, "TORWIND-3") {
		t.Fatalf("hauler TORWIND-3 missing from manufacturing pool %v", symbols)
	}
}

// Claim-filter (sp-snmb): a ship marked DedicatedFleet is reserved exclusively
// for its own coordinator's direct lookup (FindIdleShipsByFleet) - every
// other coordinator (manufacturing, factory, gas, balance-handler) shares
// this same discovery function, so excluding dedicated ships here, unconditionally,
// is what makes them invisible fleet-wide "for free" without touching every
// caller individually.
func TestFindIdleLightHaulers_ExcludesDedicatedShips(t *testing.T) {
	dedicated := newCandidateShip(t, "TORWIND-4", "HAULER", 30, 10, 0)
	dedicated.SetDedicatedFleet("contract")
	general := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{dedicated, general}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if containsSymbol(symbols, "TORWIND-4") {
		t.Fatalf("dedicated ship TORWIND-4 must be excluded from the general pool %v - it is reserved for the contract coordinator's own dedicated lookup", symbols)
	}
	if !containsSymbol(symbols, "TORWIND-3") {
		t.Fatalf("non-dedicated hauler TORWIND-3 missing from candidate pool %v", symbols)
	}
}

// newCandidateShipAt builds an idle, docked hauler located at the given waypoint
// symbol, so a test can place hulls in different systems and exercise the
// single-system pool filter (sp-qr3v). Its system is derived from the waypoint
// symbol exactly as production does (shared.ExtractSystemSymbol).
func newCandidateShipAt(t *testing.T, symbol, waypointSymbol string) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(30, 0, nil)
	if err != nil {
		t.Fatalf("build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("build fuel: %v", err)
	}
	wp, err := shared.NewWaypoint(waypointSymbol, 0, 0)
	if err != nil {
		t.Fatalf("build waypoint: %v", err)
	}
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), wp, fuel, 100, 30, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked,
	)
	if err != nil {
		t.Fatalf("build ship: %v", err)
	}
	return ship
}

// Single-system filter (sp-qr3v): a factory operating in system X1-JP61 must see
// ONLY hulls currently in that system. An idle hauler sitting in the home system
// (X1-KA42) is unselectable - the factory can never navigate it home to work, so
// claiming it just fails the worker on every pass. This is the incident:
// goods_factory-LAB_INSTRUMENTS claimed TORWIND-1F at KA42-E42 (home) and churned
// "Worker failed" every ~90s. With the systemFilter set, that out-of-system hull
// never enters the pool, so it is never claimed.
func TestFindIdleLightHaulers_SystemFilter_ExcludesOutOfSystemHulls(t *testing.T) {
	inSystem := newCandidateShipAt(t, "JP61-2", "X1-JP61-E42")        // idle hauler in the factory's system
	outOfSystem := newCandidateShipAt(t, "TORWIND-1F", "X1-KA42-E42") // idle hauler in the home system
	repo := &stubShipRepo{ships: []*navigation.Ship{outOfSystem, inSystem}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "X1-JP61")
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if containsSymbol(symbols, "TORWIND-1F") {
		t.Fatalf("out-of-system hull TORWIND-1F (X1-KA42) must be unselectable for an X1-JP61 factory, got %v", symbols)
	}
	if len(symbols) != 1 || symbols[0] != "JP61-2" {
		t.Fatalf("expected only the in-system hauler [JP61-2], got %v", symbols)
	}
}

// Contract compatibility: an empty systemFilter preserves the original
// fleet-wide behavior - every idle hauler qualifies regardless of which system
// it sits in. Contract callers (run_fleet_coordinator, balance_ship_position)
// pass "" and must be entirely unaffected by the sp-qr3v filter.
func TestFindIdleLightHaulers_EmptySystemFilter_ReturnsAllSystems(t *testing.T) {
	home := newCandidateShipAt(t, "TORWIND-1F", "X1-KA42-E42")
	away := newCandidateShipAt(t, "JP61-2", "X1-JP61-E42")
	repo := &stubShipRepo{ships: []*navigation.Ship{home, away}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "")
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if !containsSymbol(symbols, "TORWIND-1F") || !containsSymbol(symbols, "JP61-2") {
		t.Fatalf("an empty system filter must return haulers from every system (fleet-wide), got %v", symbols)
	}
}

// FindIdleShipsByFleet is a coordinator's direct lookup for its own dedicated
// fleet (sp-l7h2, replacing sp-snmb's symbol-list FindIdleDedicatedShips): it
// returns only currently-idle ships whose persisted DedicatedFleet tag equals
// the fleet name. Busy members are silently skipped rather than erroring,
// members of OTHER fleets and untagged ships never appear, and - unlike
// FindIdleLightHaulers - role does not matter: carrying the tag is the whole
// qualification.
func TestFindIdleShipsByFleet_ReturnsOnlyIdleMembersOfNamedFleet(t *testing.T) {
	idle := newCandidateShip(t, "TORWIND-4", "EXCAVATOR", 30, 10, 0) // non-hauler role: tag is the only qualification
	idle.SetDedicatedFleet("contract")
	busy := newCandidateShip(t, "TORWIND-5", "HAULER", 30, 10, 0)
	busy.SetDedicatedFleet("contract")
	if err := busy.AssignToContainer("contract-worker-TORWIND-5", shared.NewRealClock()); err != nil {
		t.Fatalf("assign busy dedicated ship: %v", err)
	}
	otherFleet := newCandidateShip(t, "TORWIND-19", "HAULER", 30, 10, 0)
	otherFleet.SetDedicatedFleet("bulk_circuit")
	untagged := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{idle, busy, otherFleet, untagged}}

	_, symbols, err := FindIdleShipsByFleet(context.Background(), shared.MustNewPlayerID(1), repo, "contract")
	if err != nil {
		t.Fatalf("FindIdleShipsByFleet: %v", err)
	}

	if len(symbols) != 1 || symbols[0] != "TORWIND-4" {
		t.Fatalf("expected only the idle contract-fleet ship [TORWIND-4], got %v", symbols)
	}
}

// A fleet member mid-flight is not dispatchable even without an active
// assignment - mirroring FindIdleLightHaulers' in-transit exclusion.
func TestFindIdleShipsByFleet_SkipsInTransitMembers(t *testing.T) {
	cargo, err := shared.NewCargo(30, 0, nil)
	if err != nil {
		t.Fatalf("build cargo: %v", err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		t.Fatalf("build fuel: %v", err)
	}
	wp, err := shared.NewWaypoint("X1-TW-A2", 10, 0)
	if err != nil {
		t.Fatalf("build waypoint: %v", err)
	}
	inTransit, err := navigation.NewShip(
		"TORWIND-4", shared.MustNewPlayerID(1), wp, fuel, 100, 30, cargo, 30,
		"FRAME_FRIGATE", "HAULER", nil, navigation.NavStatusInTransit,
	)
	if err != nil {
		t.Fatalf("build in-transit ship: %v", err)
	}
	inTransit.SetDedicatedFleet("contract")
	repo := &stubShipRepo{ships: []*navigation.Ship{inTransit}}

	_, symbols, err := FindIdleShipsByFleet(context.Background(), shared.MustNewPlayerID(1), repo, "contract")
	if err != nil {
		t.Fatalf("FindIdleShipsByFleet: %v", err)
	}

	if len(symbols) != 0 {
		t.Fatalf("expected no dispatchable ships while the only member is in transit, got %v", symbols)
	}
}

// An empty fleet name means "general pool", never a fleet of its own: the
// lookup must return nothing rather than every untagged ship - otherwise a
// coordinator started without a dedicated fleet would treat the whole navy
// as its exclusive property.
func TestFindIdleShipsByFleet_EmptyFleetName_ReturnsNothing(t *testing.T) {
	untagged := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{untagged}}

	_, symbols, err := FindIdleShipsByFleet(context.Background(), shared.MustNewPlayerID(1), repo, "")
	if err != nil {
		t.Fatalf("FindIdleShipsByFleet: %v", err)
	}

	if len(symbols) != 0 {
		t.Fatalf("expected an empty fleet name to return nothing, got %v", symbols)
	}
}

// ============================================================================
// EXCLUSIVE MODE (sp-wq7r)
//
// Bug: with a dedicated fleet configured, the coordinator still combined
// FindIdleLightHaulers' general pool with FindIdleShipsByFleet's dedicated
// pool unconditionally (availableShips := append(generalShips,
// dedicatedIdleShips...)), so it drafted idle non-dedicated hulls by
// distance - the "dedicated" fleet was never actually exclusive. Fixed by
// FleetHasMembers (does an exclusive fleet exist at all right now, busy
// members included) gating SelectAvailableShips (seals the pool to
// dedicated-only candidates when a fleet is active).
// ============================================================================

// FleetHasMembers answers "does this coordinator have an exclusive fleet at
// all right now" - a broader question than FindIdleShipsByFleet's
// dispatchable-only view. An idle tagged member is the simplest case.
func TestFleetHasMembers_IdleMember_ReturnsTrue(t *testing.T) {
	member := newCandidateShip(t, "TORWIND-4", "HAULER", 30, 10, 0)
	member.SetDedicatedFleet("contract")
	repo := &stubShipRepo{ships: []*navigation.Ship{member}}

	hasMembers, err := FleetHasMembers(context.Background(), shared.MustNewPlayerID(1), repo, "contract")
	if err != nil {
		t.Fatalf("FleetHasMembers: %v", err)
	}
	if !hasMembers {
		t.Fatalf("expected an idle tagged member to count as a fleet member")
	}
}

// The critical case: a dedicated fleet that is entirely busy must still
// report having members, so EXCLUSIVE MODE keeps the coordinator sealed to
// its own fleet instead of falling back to the general pool the moment
// every dedicated hull happens to be mid-delivery.
func TestFleetHasMembers_OnlyBusyMember_ReturnsTrue(t *testing.T) {
	busy := newCandidateShip(t, "TORWIND-5", "HAULER", 30, 10, 0)
	busy.SetDedicatedFleet("contract")
	if err := busy.AssignToContainer("contract-worker-TORWIND-5", shared.NewRealClock()); err != nil {
		t.Fatalf("assign busy dedicated ship: %v", err)
	}
	repo := &stubShipRepo{ships: []*navigation.Ship{busy}}

	hasMembers, err := FleetHasMembers(context.Background(), shared.MustNewPlayerID(1), repo, "contract")
	if err != nil {
		t.Fatalf("FleetHasMembers: %v", err)
	}
	if !hasMembers {
		t.Fatalf("a fully-busy dedicated fleet must still report having members - otherwise the coordinator falls back to the general pool the moment every dedicated hull is working")
	}
}

func TestFleetHasMembers_NoTaggedShips_ReturnsFalse(t *testing.T) {
	untagged := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{untagged}}

	hasMembers, err := FleetHasMembers(context.Background(), shared.MustNewPlayerID(1), repo, "contract")
	if err != nil {
		t.Fatalf("FleetHasMembers: %v", err)
	}
	if hasMembers {
		t.Fatalf("expected no dedicated fleet members when no ship carries the tag")
	}
}

func TestFleetHasMembers_EmptyFleetName_ReturnsFalse(t *testing.T) {
	untagged := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{untagged}}

	hasMembers, err := FleetHasMembers(context.Background(), shared.MustNewPlayerID(1), repo, "")
	if err != nil {
		t.Fatalf("FleetHasMembers: %v", err)
	}
	if hasMembers {
		t.Fatalf("an empty fleet name must never report members - it means 'no dedicated fleet', mirroring FindIdleShipsByFleet")
	}
}

// SelectAvailableShips is the exact combine point of the original bug: with
// a dedicated fleet active, the general pool - including the command ship,
// which IncludeCommandShip makes a first-class hauler candidate - must
// never appear in the result, even when mixed in alongside dedicated ships.
func TestSelectAvailableShips(t *testing.T) {
	tests := []struct {
		name                 string
		generalShips         []string
		dedicatedIdleShips   []string
		dedicatedFleetActive bool
		want                 []string
	}{
		{
			name:                 "exclusive mode returns only dedicated ships - general pool and command ship excluded",
			generalShips:         []string{"TORWIND-1", "TORWIND-3"}, // TORWIND-1 follows the *-1 command ship convention
			dedicatedIdleShips:   []string{"TORWIND-4", "TORWIND-8"},
			dedicatedFleetActive: true,
			want:                 []string{"TORWIND-4", "TORWIND-8"},
		},
		{
			name:                 "exclusive mode with every dedicated ship busy returns empty - never falls back to the general pool",
			generalShips:         []string{"TORWIND-1", "TORWIND-3"},
			dedicatedIdleShips:   nil,
			dedicatedFleetActive: true,
			want:                 nil,
		},
		{
			name:                 "no dedicated fleet combines both pools same as before the fix",
			generalShips:         []string{"TORWIND-1", "TORWIND-3"},
			dedicatedIdleShips:   nil,
			dedicatedFleetActive: false,
			want:                 []string{"TORWIND-1", "TORWIND-3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SelectAvailableShips(tt.generalShips, tt.dedicatedIdleShips, tt.dedicatedFleetActive)
			if len(got) != len(tt.want) {
				t.Fatalf("SelectAvailableShips() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("SelectAvailableShips() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// ============================================================================
// NO-CARGO-DUMP CLAIM GUARD (sp-wq7r)
//
// Bug: the coordinator selected candidates by distance alone, leaving the
// worker's jettison step (CargoManager.JettisonWrongCargoIfNeeded) to
// silently dump whatever a claimed hull was carrying to make room - in
// production this destroyed 43 units of EQUIPMENT the captain was
// mid-liquidating on a borrowed pool hull, for a LIQUID_NITROGEN contract.
// FilterUnrelatedCargo runs at selection time, before a hull is ever
// claimed, so cargo unrelated to the delivery is never at risk.
// ============================================================================

func TestFilterUnrelatedCargo_EmptyCargo_Claimable(t *testing.T) {
	empty := newCandidateShip(t, "TORWIND-3", "HAULER", 40, 10, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{empty}}

	claimable, parked, err := FilterUnrelatedCargo(context.Background(), shared.MustNewPlayerID(1), repo, []string{"TORWIND-3"}, "LIQUID_NITROGEN")
	if err != nil {
		t.Fatalf("FilterUnrelatedCargo: %v", err)
	}
	if !containsSymbol(claimable, "TORWIND-3") {
		t.Fatalf("expected empty-hold ship to be claimable, got claimable=%v parked=%v", claimable, parked)
	}
	if len(parked) != 0 {
		t.Fatalf("expected nothing parked, got %v", parked)
	}
}

func TestFilterUnrelatedCargo_OnlyRequiredCargoAlreadyAboard_Claimable(t *testing.T) {
	item, err := shared.NewCargoItem("LIQUID_NITROGEN", "Liquid Nitrogen", "", 10)
	if err != nil {
		t.Fatalf("build cargo item: %v", err)
	}
	partial := newCandidateShipWithCargo(t, "TORWIND-3", "HAULER", 40, 10, 0, []*shared.CargoItem{item})
	repo := &stubShipRepo{ships: []*navigation.Ship{partial}}

	claimable, parked, err := FilterUnrelatedCargo(context.Background(), shared.MustNewPlayerID(1), repo, []string{"TORWIND-3"}, "LIQUID_NITROGEN")
	if err != nil {
		t.Fatalf("FilterUnrelatedCargo: %v", err)
	}
	if !containsSymbol(claimable, "TORWIND-3") {
		t.Fatalf("a hull already holding only the required cargo (resumed partial delivery) must be claimable, got claimable=%v parked=%v", claimable, parked)
	}
	if len(parked) != 0 {
		t.Fatalf("expected nothing parked, got %v", parked)
	}
}

// The regression case: a pool hull mid-liquidation of unrelated cargo must
// never be claimed for an unrelated contract - not claimed and silently
// jettisoned by the worker, which is what happened before this fix.
func TestFilterUnrelatedCargo_UnrelatedCargoAboard_Parked_NotClaimable(t *testing.T) {
	item, err := shared.NewCargoItem("EQUIPMENT", "Equipment", "", 43)
	if err != nil {
		t.Fatalf("build cargo item: %v", err)
	}
	loaded := newCandidateShipWithCargo(t, "TORWIND-B", "HAULER", 60, 10, 0, []*shared.CargoItem{item})
	repo := &stubShipRepo{ships: []*navigation.Ship{loaded}}

	claimable, parked, err := FilterUnrelatedCargo(context.Background(), shared.MustNewPlayerID(1), repo, []string{"TORWIND-B"}, "LIQUID_NITROGEN")
	if err != nil {
		t.Fatalf("FilterUnrelatedCargo: %v", err)
	}
	if containsSymbol(claimable, "TORWIND-B") {
		t.Fatalf("a hull holding unrelated EQUIPMENT cargo must never be claimable for a LIQUID_NITROGEN contract, got claimable=%v", claimable)
	}
	if !containsSymbol(parked, "TORWIND-B") {
		t.Fatalf("expected TORWIND-B parked for holding unrelated cargo, got parked=%v", parked)
	}
}

func TestFilterUnrelatedCargo_MixedCandidates_SplitsCleanAndParked(t *testing.T) {
	clean := newCandidateShip(t, "TORWIND-3", "HAULER", 40, 10, 0)
	item, err := shared.NewCargoItem("EQUIPMENT", "Equipment", "", 43)
	if err != nil {
		t.Fatalf("build cargo item: %v", err)
	}
	loaded := newCandidateShipWithCargo(t, "TORWIND-B", "HAULER", 60, 10, 0, []*shared.CargoItem{item})
	repo := &stubShipRepo{ships: []*navigation.Ship{clean, loaded}}

	claimable, parked, err := FilterUnrelatedCargo(context.Background(), shared.MustNewPlayerID(1), repo, []string{"TORWIND-3", "TORWIND-B"}, "LIQUID_NITROGEN")
	if err != nil {
		t.Fatalf("FilterUnrelatedCargo: %v", err)
	}
	if !containsSymbol(claimable, "TORWIND-3") || containsSymbol(claimable, "TORWIND-B") {
		t.Fatalf("expected only TORWIND-3 claimable, got %v", claimable)
	}
	if !containsSymbol(parked, "TORWIND-B") || containsSymbol(parked, "TORWIND-3") {
		t.Fatalf("expected only TORWIND-B parked, got %v", parked)
	}
}

func TestFilterUnrelatedCargo_SymbolNotInFleetSnapshot_SkippedSilently(t *testing.T) {
	repo := &stubShipRepo{ships: []*navigation.Ship{}}

	claimable, parked, err := FilterUnrelatedCargo(context.Background(), shared.MustNewPlayerID(1), repo, []string{"TORWIND-GONE"}, "LIQUID_NITROGEN")
	if err != nil {
		t.Fatalf("FilterUnrelatedCargo: %v", err)
	}
	if len(claimable) != 0 || len(parked) != 0 {
		t.Fatalf("a candidate missing from the fleet snapshot must appear in neither list, got claimable=%v parked=%v", claimable, parked)
	}
}
