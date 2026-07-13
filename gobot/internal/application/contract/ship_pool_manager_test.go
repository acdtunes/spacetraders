package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
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

// sp-sqq5 (supersedes sp-4a4e's first-class treatment; RULINGS #7): while ANY
// regular hauler is idle, the command frigate must be HELD OUT of the candidate
// pool - it hauls only as a last resort. sp-4a4e had made the command ship a
// first-class candidate that entered the pool alongside idle haulers; the flaw
// that surfaced (sp-sqq5) is that an undedicated command frigate the captain had
// deliberately retired via `fleet unassign` was then re-swept back onto
// contracts by the running coordinator - once stranding a mid-delivery contract.
// RULINGS #7 ("the command frigate hauls only as last resort") governs: with an
// idle hauler present, the command ship is excluded; the busy-hauler /
// no-hauler last-resort cases below prove it is still usable when it is the only
// option, so this is a CONDITIONAL exclusion, never an absolute ban.
func TestFindIdleLightHaulers_ExcludesIdleCommandShipWhenHaulerAvailable(t *testing.T) {
	hauler := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)   // idle, far
	command := newCandidateShip(t, "TORWIND-1", "COMMAND", 115, 50, 0) // idle, close, command
	repo := &stubShipRepo{ships: []*navigation.Ship{hauler, command}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if containsSymbol(symbols, "TORWIND-1") {
		t.Fatalf("command ship TORWIND-1 must be held out of the pool %v while idle hauler TORWIND-3 is available - RULINGS #7 makes it last-resort only, not a first-class candidate", symbols)
	}
	if !containsSymbol(symbols, "TORWIND-3") {
		t.Fatalf("hauler TORWIND-3 missing from candidate pool %v", symbols)
	}
}

// Acceptance (sp-4a4e, preserved by sp-sqq5's conditional exclusion): with the
// only hauler busy and the command ship idle, the coordinator must still be able
// to dispatch the command ship - not fall through to an empty pool and wait 5h+
// while an idle hull sits docked. No REGULAR hauler is idle here (the only one
// is busy), so the command frigate is the last resort and enters the pool.
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

// ============================================================================
// RETIRED COMMAND FRIGATE STAYS OUT OF THE RUNNING COORDINATOR POOL (sp-sqq5)
//
// A command frigate deliberately retired via `fleet unassign` has its
// DedicatedFleet tag cleared to "" - it is pinned to no fleet. Before this fix
// IncludeCommandShip re-admitted it to the contract candidate pool purely by
// role/symbol (IsCommandHull never consults the tag), so the RUNNING
// coordinator re-claimed a hull the captain had just pulled off contracts -
// once stranding a mid-delivery contract into a re-source double-buy, and
// putting the low-cargo/low-fuel command hull back on contracts against
// RULINGS #7. The fix keeps the undedicated command frigate OUT of discovery
// while any regular hauler is idle, yet leaves it usable as the last resort.
// ============================================================================

// The re-claim vector: an idle, undedicated (fleet-unassign'd) command frigate
// must NOT re-enter the candidate pool while a regular hauler is idle, and the
// exclusion must hold across several reconcile ticks - not just the first pass.
// PROVES RED before the fix (the frigate is re-admitted today).
func TestCommandHull_FleetUnassigned_NotReclaimedWhileHaulersExist(t *testing.T) {
	hauler := newCandidateShip(t, "TORWIND-3", "HAULER", 80, 700, 0) // idle regular hauler
	// Undedicated command frigate (tag cleared by `fleet unassign`), sized to
	// match the era-2 upgraded frigate's real cargo capacity. FindIdleLightHaulers
	// applies no cargo-baseline screen to any role - only the generic
	// CargoCapacity()==0 probe check; the sp-uj6a baseline is a separate,
	// later, opt-in filter the caller applies AFTER this function returns, so
	// it is not in play here. This proves the DEDICATION/last-resort path
	// cleanly, regardless of cargo size.
	retired := newCandidateShip(t, "TORWIND-1", "COMMAND", 115, 50, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{hauler, retired}}

	for tick := 1; tick <= 3; tick++ {
		_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip)
		if err != nil {
			t.Fatalf("tick %d: FindIdleLightHaulers: %v", tick, err)
		}
		if containsSymbol(symbols, "TORWIND-1") {
			t.Fatalf("tick %d: retired command frigate TORWIND-1 must stay OUT of the candidate pool %v while hauler TORWIND-3 is idle (RULINGS #7 last-resort) - a `fleet unassign`'d frigate must not be re-swept onto contracts", tick, symbols)
		}
		if !containsSymbol(symbols, "TORWIND-3") {
			t.Fatalf("tick %d: idle hauler TORWIND-3 must remain a candidate, got %v", tick, symbols)
		}
	}
}

// The preserved last-resort path (RULINGS #7: the command frigate CAN haul when
// it is the ONLY option): with no regular hauler available at all, the command
// frigate must remain the candidate - benching the only hull would strand the
// contract. Guards the regression so the fix stays a CONDITIONAL exclusion, not
// an absolute ban. Stays GREEN across the change.
func TestCommandHull_LastResort_StillUsableWhenNoOtherHull(t *testing.T) {
	command := newCandidateShip(t, "TORWIND-1", "COMMAND", 115, 50, 0) // idle, undedicated, and the only hull
	repo := &stubShipRepo{ships: []*navigation.Ship{command}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}
	if len(symbols) != 1 || symbols[0] != "TORWIND-1" {
		t.Fatalf("with no regular hauler available the command frigate must remain the last-resort candidate [TORWIND-1], got %v", symbols)
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

// sp-m92a: the "stocker" fleet is a durable hauler dedication like "contract".
// The unconditional dedication exclude (DedicatedFleet != "") is what keeps a
// stocker hull invisible to the factory/contract pool "for free" — so a hull the
// captain pins `fleet assign --fleet stocker` is never poached between the
// stocker container's legs. Locks the stocker case explicitly so a future
// fleet-name special-case cannot silently regress continuous stocking.
func TestFindIdleLightHaulers_ExcludesStockerDedicatedShips(t *testing.T) {
	dedicated := newCandidateShip(t, "TORWIND-38", "HAULER", 30, 10, 0)
	dedicated.SetDedicatedFleet("stocker")
	general := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{dedicated, general}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if containsSymbol(symbols, "TORWIND-38") {
		t.Fatalf("stocker-dedicated hull TORWIND-38 must be excluded from the general pool %v - the factory/contract pool must never poach a continuous-stocking hull", symbols)
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
// 0-CARGO EXCLUSION (sp-lybx)
//
// A probe/satellite mispinned into the contract fleet (TORWIND-24: 0/0 cargo)
// can never carry a delivery, so claiming it just spawns a worker that dies
// instantly on 'deliveries not complete' - the 4-spawns-in-9s storm. Both
// contract-worker claim sites must make such a hull UNSELECTABLE at discovery:
// the general pool (FindIdleLightHaulers, already) and the dedicated pool
// (FindIdleShipsByFleet, opt-in via RequireCargoCapacity).
// ============================================================================

// The dedicated-fleet claim site, opted in with RequireCargoCapacity, must drop
// a 0-cargo hull while keeping cargo-carrying members (including non-hauler
// roles - role is still not the qualification here).
func TestFindIdleShipsByFleet_RequireCargoCapacity_ExcludesZeroCargoProbe(t *testing.T) {
	probe := newCandidateShip(t, "TORWIND-24", "HAULER", 0, 10, 0) // 0-cargo, tagged as a hauler (the sp-lybx mispin)
	probe.SetDedicatedFleet("contract")
	realHauler := newCandidateShip(t, "TORWIND-29", "HAULER", 30, 10, 0)
	realHauler.SetDedicatedFleet("contract")
	repo := &stubShipRepo{ships: []*navigation.Ship{probe, realHauler}}

	_, symbols, err := FindIdleShipsByFleet(context.Background(), shared.MustNewPlayerID(1), repo, "contract", RequireCargoCapacity)
	if err != nil {
		t.Fatalf("FindIdleShipsByFleet: %v", err)
	}

	if containsSymbol(symbols, "TORWIND-24") {
		t.Fatalf("0-cargo probe TORWIND-24 must be UNSELECTABLE for contract work, got %v", symbols)
	}
	if len(symbols) != 1 || symbols[0] != "TORWIND-29" {
		t.Fatalf("expected only the cargo-carrying hull [TORWIND-29], got %v", symbols)
	}
}

// Regression: the DEFAULT policy (no CargoCapacityPolicy passed) keeps every
// tagged member regardless of cargo - the idle-arb dispatcher's own calls omit
// the policy, so its pool and reserve accounting are byte-identical to before.
func TestFindIdleShipsByFleet_DefaultPolicy_KeepsZeroCargoHull(t *testing.T) {
	probe := newCandidateShip(t, "TORWIND-24", "HAULER", 0, 10, 0)
	probe.SetDedicatedFleet("contract")
	repo := &stubShipRepo{ships: []*navigation.Ship{probe}}

	_, symbols, err := FindIdleShipsByFleet(context.Background(), shared.MustNewPlayerID(1), repo, "contract")
	if err != nil {
		t.Fatalf("FindIdleShipsByFleet: %v", err)
	}

	if len(symbols) != 1 || symbols[0] != "TORWIND-24" {
		t.Fatalf("the default (AnyCargoCapacity) policy must keep the 0-cargo hull for callers that never opted in, got %v", symbols)
	}
}

// The general claim site already excludes a 0-cargo hull tagged as a hauler -
// pinned here explicitly so the "both claim sites covered" guarantee has a test
// on each side, not just the dedicated one.
func TestFindIdleLightHaulers_ExcludesZeroCargoHauler(t *testing.T) {
	probe := newCandidateShip(t, "TORWIND-24", "HAULER", 0, 10, 0) // 0-cargo, role HAULER
	realHauler := newCandidateShip(t, "TORWIND-29", "HAULER", 30, 700, 0)
	repo := &stubShipRepo{ships: []*navigation.Ship{probe, realHauler}}

	_, symbols, err := FindIdleLightHaulers(context.Background(), shared.MustNewPlayerID(1), repo, "", IncludeCommandShip)
	if err != nil {
		t.Fatalf("FindIdleLightHaulers: %v", err)
	}

	if containsSymbol(symbols, "TORWIND-24") {
		t.Fatalf("0-cargo hauler TORWIND-24 must be excluded from the general pool, got %v", symbols)
	}
	if len(symbols) != 1 || symbols[0] != "TORWIND-29" {
		t.Fatalf("expected only the cargo-carrying hauler [TORWIND-29], got %v", symbols)
	}
}

// ============================================================================
// COMMAND CARGO BASELINE (sp-uj6a)
//
// FindIdleLightHaulers' generic cargo check (CargoCapacity() == 0) only
// screens out probes/satellites - it does not stop a stock 40-cargo command
// frigate from competing for contract legs an 80-cargo light hauler would
// single-trip. A stock frigate double-trips that load, spending its whole
// speed advantage on the extra leg for a net loss versus just dispatching
// the light hauler. FilterCommandCargoBaseline is a SEPARATE, later step the
// caller runs after FindIdleLightHaulers returns - it does not touch
// FindIdleLightHaulers itself, the r6f1 dedication-write floor, or the
// sp-4a4e last-resort ranking in SelectHullForCargo (domain/contract).
// ============================================================================

// capturedLogEntry/capturingLogger mirror the fake in
// contract/services/insufficient_credits_test.go: the container-log renderer
// prints only level+message and DROPS the metadata map, so a cause hidden
// only in metadata never reaches an operator. Assertions below check the
// rendered MESSAGE TEXT, not the metadata, to prove the ship/capacity/
// baseline actually surface.
type capturedLogEntry struct {
	level   string
	message string
}

type capturingLogger struct {
	entries []capturedLogEntry
}

func (l *capturingLogger) Log(level, message string, _ map[string]interface{}) {
	l.entries = append(l.entries, capturedLogEntry{level: level, message: message})
}

// A stock 40-cargo command frigate is below the default 80 baseline (the
// light-hauler standard, RULINGS #5) and must be excluded, with a log
// message naming the ship, its capacity, and the baseline so the cause
// actually reaches an operator.
func TestFilterCommandCargoBaseline_StockCommandShip_ExcludedAndLogged(t *testing.T) {
	command := newCandidateShip(t, "TORWIND-1", "COMMAND", 40, 50, 0)
	hauler := newCandidateShip(t, "TORWIND-3", "HAULER", 30, 700, 0)
	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	symbols := FilterCommandCargoBaseline(ctx, []*navigation.Ship{command, hauler}, 0)

	if containsSymbol(symbols, "TORWIND-1") {
		t.Fatalf("stock 40-cargo command ship TORWIND-1 must be excluded below the default baseline, got %v", symbols)
	}
	if !containsSymbol(symbols, "TORWIND-3") {
		t.Fatalf("hauler TORWIND-3 missing from filtered pool %v", symbols)
	}

	found := false
	for _, e := range logger.entries {
		if strings.Contains(e.message, "TORWIND-1") && strings.Contains(e.message, "40") && strings.Contains(e.message, "80") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a log message naming the ship (TORWIND-1), its capacity (40), and the baseline (80), got entries=%v", logger.entries)
	}
}

// era-2's upgraded frigate (115 cargo) clears the default 80 baseline and
// must remain a candidate.
func TestFilterCommandCargoBaseline_UpgradedCommandShip_Included(t *testing.T) {
	command := newCandidateShip(t, "TORWIND-1", "COMMAND", 115, 50, 0)

	symbols := FilterCommandCargoBaseline(context.Background(), []*navigation.Ship{command}, 0)

	if !containsSymbol(symbols, "TORWIND-1") {
		t.Fatalf("upgraded 115-cargo command ship TORWIND-1 must remain a candidate, got %v", symbols)
	}
}

// RULINGS #5 (parametrize, don't hardcode): a caller-supplied baseline
// overrides the package default in both directions - a custom value can
// exclude a hull the default would have allowed, or admit one the default
// would have excluded.
func TestFilterCommandCargoBaseline_CustomBaseline_Respected(t *testing.T) {
	command := newCandidateShip(t, "TORWIND-1", "COMMAND", 90, 50, 0)

	excluded := FilterCommandCargoBaseline(context.Background(), []*navigation.Ship{command}, 100)
	if containsSymbol(excluded, "TORWIND-1") {
		t.Fatalf("a 90-cargo command ship must be excluded under a custom 100 baseline, got %v", excluded)
	}

	included := FilterCommandCargoBaseline(context.Background(), []*navigation.Ship{command}, 50)
	if !containsSymbol(included, "TORWIND-1") {
		t.Fatalf("a 90-cargo command ship must be included under a custom 50 baseline, got %v", included)
	}
}

// Regression: non-command hulls are never subject to the baseline, however
// small their hold - the gate is command-ship-specific.
func TestFilterCommandCargoBaseline_NonCommandHulls_Unaffected(t *testing.T) {
	tinyHauler := newCandidateShip(t, "TORWIND-3", "HAULER", 10, 700, 0)

	symbols := FilterCommandCargoBaseline(context.Background(), []*navigation.Ship{tinyHauler}, 0)

	if !containsSymbol(symbols, "TORWIND-3") {
		t.Fatalf("non-command hauler TORWIND-3 must be unaffected by the command cargo baseline regardless of its own cargo capacity, got %v", symbols)
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
