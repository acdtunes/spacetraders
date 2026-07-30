package grpc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// opportunity_relocator_ports_test.go — the relocator's adapter boundary (sp-zvywu Part 2).
//
// Two things are pinned here because they are the two ways this wiring can be silently wrong:
//
//  1. THE FLEET OBSERVER'S PROTECTION FLAGS. RULINGS #7 is enforced by the reconciler, but only on the
//     facts THIS adapter reports; a hull whose Pinned flag reads false is poachable however careful
//     the reconciler is. And the failure is silent in BOTH directions — flags that are always true
//     leave the relocator permanently dormant while looking healthy, so the eligible case is pinned
//     alongside the protected ones.
//  2. THE INTENT STORE'S ROUND TRIP. DecidedAt is the restart-durable cooldown clock and the only
//     field a restart cannot re-derive from live state. A DecidedAt that drifts re-opens a hull early;
//     one that resets freezes it forever.

// --- fleet-observer fakes ---

type relocatorPortsShipRepo struct {
	ships []*navigation.Ship
	err   error
}

func (r *relocatorPortsShipRepo) FindAllByPlayer(context.Context, shared.PlayerID) ([]*navigation.Ship, error) {
	return r.ships, r.err
}

type relocatorPortsContainerRepo struct {
	byStatus map[container.ContainerStatus][]*persistence.ContainerModel
	err      error
}

func (r *relocatorPortsContainerRepo) ListByStatus(_ context.Context, status container.ContainerStatus, _ *int) ([]*persistence.ContainerModel, error) {
	if r.err != nil {
		return nil, r.err
	}
	return r.byStatus[status], nil
}

// relocatorPortsHull builds a trade-dedicated hull standing in X1-HOME. role is the registration role
// ("HAULER" for a worker, commandRole for the flagship).
func relocatorPortsHull(t *testing.T, symbol, role string) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	waypoint, err := shared.NewWaypoint("X1-HOME-A1", 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(1), waypoint, fuel, 100, 40, cargo, 30,
		"FRAME_LIGHT_FREIGHTER", role, nil, navigation.NavStatusDocked,
	)
	require.NoError(t, err)
	ship.SetDedicatedFleet(tradeFleetTag)
	return ship
}

// relocatorPortsObserved returns the observed hull for symbol, failing when it was not surfaced at all.
func relocatorPortsObserved(t *testing.T, hulls []tradingCmd.RelocatorHull, symbol string) tradingCmd.RelocatorHull {
	t.Helper()
	for _, hull := range hulls {
		if hull.ShipSymbol == symbol {
			return hull
		}
	}
	t.Fatalf("%s was not surfaced by the fleet observer at all; observed %v", symbol, hulls)
	return tradingCmd.RelocatorHull{}
}

// --- the protection flags (RULINGS #7) ---

// The command frigate and every exclusively-held hull must be reported PROTECTED, so the reconciler
// drops them at observation and no scoring path can reach them.
//
// The relocator's actuator does NOT claim the hull (RepositionToWaypointWithinJumps trusts a claim its
// caller already holds, and the relocator holds none), so a live container claim is the only thing
// standing between this reconciler and poaching-by-movement — which is why an assigned-elsewhere hull
// must read Pinned, not merely "busy".
func TestRelocatorFleetObserverShould_ReportTheCommandFrigateAndEveryExclusivelyHeldHullAsProtected(t *testing.T) {
	clock := shared.NewRealClock()

	frigate := relocatorPortsHull(t, "COMMAND-1", commandRole)

	reserved := relocatorPortsHull(t, "RESERVED-1", "HAULER")
	require.NoError(t, reserved.ReserveByCaptain("operator is flying it by hand", clock))

	claimedElsewhere := relocatorPortsHull(t, "CLAIMED-1", "HAULER")
	require.NoError(t, claimedElsewhere.AssignToContainer("longhaul_arb-OTHER", clock))

	observer := NewRelocatorFleetObserver(
		&relocatorPortsShipRepo{ships: []*navigation.Ship{frigate, reserved, claimedElsewhere}},
		// The claiming container is RUNNING and REAL — a long-haul arb worker, not a tour. Without this
		// row the tour-container set would be empty for the wrong reason and the command_type filter
		// would never be consulted, so an observer that counted ANY container as a tour would pass this
		// test while quietly reclassifying every pinned hull as merely mid-tour.
		&relocatorPortsContainerRepo{byStatus: map[container.ContainerStatus][]*persistence.ContainerModel{
			container.ContainerStatusRunning: {{ID: "longhaul_arb-OTHER", CommandType: "longhaul_arb"}},
		}},
	)

	hulls, err := observer.ObserveTradeHulls(context.Background(), 1)
	require.NoError(t, err)

	// The frigate is protected by its ROLE, which the reconciler checks first and separately.
	got := relocatorPortsObserved(t, hulls, "COMMAND-1")
	require.True(t, got.IsCommandFrigate, "the command frigate must be flagged as such; RULINGS #7 protects it and the reconciler drops it at observation")

	// A captain reservation's own contract is that it hides the hull from every coordinator's
	// discovery, so a coordinator that relocated one anyway would be defeating the reservation.
	got = relocatorPortsObserved(t, hulls, "RESERVED-1")
	require.True(t, got.Pinned, "a captain-reserved hull must read Pinned; the reservation exists to hide it from every coordinator")

	// Another operation holds this hull exclusively. Flying it away mid-operation is the poaching
	// RULINGS #7 forbids, and no claim check downstream would catch it.
	got = relocatorPortsObserved(t, hulls, "CLAIMED-1")
	require.True(t, got.Pinned, "a hull claimed by a NON-tour container must read Pinned; the relocator's actuator never claims, so this flag is the only guard")
	require.False(t, got.OnTour, "a hull held by a long-haul worker is not on a tour; conflating the two would make the mid_tour heartbeat count lie")
}

// THE FALSIFIER for the flags above: a genuinely idle, trade-dedicated, non-frigate hull must come
// back RELOCATABLE. Without this the protection assertions pass vacuously — an observer that flags
// every hull protected satisfies them all and leaves the reconciler permanently dormant while
// reporting a healthy tick.
//
// This is not a hypothetical failure mode. FindActiveByPlayer — the seam the anti-herd count uses —
// filters to ships where IsAssigned() is true, which is the exact complement of what this port needs:
// a trade hull between tours has been force-released by its finished tour container, so it is IDLE and
// absent from the "active" set. An observer built on it surfaces only hulls another operation holds,
// every one of which this adapter then flags OnTour or Pinned. Hence FindAllByPlayer.
func TestRelocatorFleetObserverShould_SurfaceAnIdleTradeHullAsRelocatable(t *testing.T) {
	idle := relocatorPortsHull(t, "HAULER-A", "HAULER")
	require.True(t, idle.IsIdle(), "fixture precondition: the hull is between tours, holding no claim")

	observer := NewRelocatorFleetObserver(
		&relocatorPortsShipRepo{ships: []*navigation.Ship{idle}},
		&relocatorPortsContainerRepo{},
	)

	hulls, err := observer.ObserveTradeHulls(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, hulls, 1, "an idle trade hull must be surfaced; an observer that surfaces nothing leaves the relocator dormant while looking healthy")

	got := hulls[0]
	require.Equal(t, "X1-HOME", got.CurrentSystem, "the hull's system is what the region observation is keyed on")
	require.False(t, got.IsCommandFrigate)
	require.False(t, got.Pinned, "an unassigned, un-reserved trade hull is not pinned; flagging it would make every relocation impossible")
	require.False(t, got.OnTour, "a released hull is at honest tour release, which is the only state the relocator will move")
}

// A hull out on a tour is ON TOUR, not pinned: expected, temporary, and counted as mid_tour rather
// than as a protection event.
//
// The hull IS claimed here, so this covers only the CLAIMED half. The unclaimed half — a tour row that
// exists but has not claimed its hull yet — is a separate window with its own test below, because it is
// reached by a different branch and an earlier version of this code got it wrong.
func TestRelocatorFleetObserverShould_ReportAClaimedTourHullAsOnTourRatherThanPinned(t *testing.T) {
	clock := shared.NewRealClock()
	touring := relocatorPortsHull(t, "HAULER-A", "HAULER")
	require.NoError(t, touring.AssignToContainer("tour_run-HAULER-A", clock))

	observer := NewRelocatorFleetObserver(
		&relocatorPortsShipRepo{ships: []*navigation.Ship{touring}},
		&relocatorPortsContainerRepo{byStatus: map[container.ContainerStatus][]*persistence.ContainerModel{
			container.ContainerStatusRunning: {{ID: "tour_run-HAULER-A", CommandType: tourRunCommandType}},
		}},
	)

	hulls, err := observer.ObserveTradeHulls(context.Background(), 1)
	require.NoError(t, err)
	got := relocatorPortsObserved(t, hulls, "HAULER-A")
	require.True(t, got.OnTour, "a claimed tour hull must read OnTour so the heartbeat counts it as mid_tour")
	require.False(t, got.Pinned, "a tour is not a protection event; reporting it as Pinned would hide the real mid_tour count")
}

// SLICE 2 (sp-x2jr6). A tour container that EXISTS but has not claimed its hull yet must still exclude
// that hull — and the hull must be UNASSIGNED here, because that is the only shape in which the window
// is real.
//
// The launch sequence is: containerRepo.Add INSERTs the row, THEN the runner's ClaimShip takes the
// hull. In between, `ship.ContainerID()` is empty and `IsAssigned()` is false. A predicate keyed on the
// hull's own claim therefore cannot see the row at all — and because IsAssigned() is also false the
// hull reads Pinned:false too, leaving it FULLY ELIGIBLE at the exact moment a tour is about to take
// it. The container has DECLARED its hull in config["ship_symbol"] the whole time
// (container_ops_tour.go), which is what must be matched.
//
// The previous version of this test assigned the hull to the PENDING container, which exercises the
// claimed branch above and never reaches the unclaimed one — so it passed while the PENDING entries in
// the id map were provably unreachable. That fixture was flattering the code; this one is the real shape.
func TestRelocatorFleetObserverShould_ExcludeAHullDeclaredByATourContainerThatHasNotClaimedItYet(t *testing.T) {
	for name, status := range map[string]container.ContainerStatus{
		"a PENDING tour that has not reached its ClaimShip yet":      container.ContainerStatusPending,
		"a RUNNING tour observed before its claim landed in the row": container.ContainerStatusRunning,
	} {
		t.Run(name, func(t *testing.T) {
			unclaimed := relocatorPortsHull(t, "HAULER-A", "HAULER")
			require.True(t, unclaimed.IsIdle(), "fixture precondition: the tour has NOT claimed the hull yet")
			require.False(t, unclaimed.IsAssigned(), "fixture precondition: an unassigned hull is what makes this window real")

			observer := NewRelocatorFleetObserver(
				&relocatorPortsShipRepo{ships: []*navigation.Ship{unclaimed}},
				&relocatorPortsContainerRepo{byStatus: map[container.ContainerStatus][]*persistence.ContainerModel{
					status: {{
						ID:          "tour_run-HAULER-A",
						CommandType: tourRunCommandType,
						Config:      `{"ship_symbol":"HAULER-A","max_iterations":-1}`,
					}},
				}},
			)

			hulls, err := observer.ObserveTradeHulls(context.Background(), 1)
			require.NoError(t, err)
			got := relocatorPortsObserved(t, hulls, "HAULER-A")
			require.True(t, got.OnTour,
				"%s declares HAULER-A but has not claimed it, so a claim-keyed predicate reads the hull as fully eligible — the relocator would fly it out from under a tour that is about to start", name)
		})
	}
}

// The declared-hull match must be scoped to TOUR rows. A non-tour container that declares a hull is a
// protection event (Pinned), not a tour — folding it into OnTour would make the mid_tour heartbeat
// count every long-haul worker and cargo liquidation as a tour.
func TestRelocatorFleetObserverShould_NotCountANonTourContainersDeclaredHullAsOnTour(t *testing.T) {
	clock := shared.NewRealClock()
	claimed := relocatorPortsHull(t, "HAULER-A", "HAULER")
	require.NoError(t, claimed.AssignToContainer("longhaul_arb-OTHER", clock))

	observer := NewRelocatorFleetObserver(
		&relocatorPortsShipRepo{ships: []*navigation.Ship{claimed}},
		&relocatorPortsContainerRepo{byStatus: map[container.ContainerStatus][]*persistence.ContainerModel{
			container.ContainerStatusRunning: {{
				ID:          "longhaul_arb-OTHER",
				CommandType: "longhaul_arb",
				Config:      `{"ship_symbol":"HAULER-A"}`,
			}},
		}},
	)

	hulls, err := observer.ObserveTradeHulls(context.Background(), 1)
	require.NoError(t, err)
	got := relocatorPortsObserved(t, hulls, "HAULER-A")
	require.False(t, got.OnTour, "a long-haul worker is not a tour; counting its hull as mid_tour would corrupt the heartbeat's tour count")
	require.True(t, got.Pinned, "a hull exclusively held by a non-tour operation is a protection event")
}

// A hull IN TRANSIT is not at honest tour release whatever put it in flight, so it must be reported
// busy rather than relocatable — otherwise the relocator would redirect a hull mid-crossing.
func TestRelocatorFleetObserverShould_ReportAnInTransitHullAsBusyRatherThanAtHonestRelease(t *testing.T) {
	flying := relocatorPortsHull(t, "HAULER-A", "HAULER")
	destination, err := shared.NewWaypoint("X1-FAR-B2", 10, 10)
	require.NoError(t, err)
	_, err = flying.EnsureInOrbit()
	require.NoError(t, err)
	require.NoError(t, flying.StartTransit(destination))
	require.True(t, flying.IsInTransit(), "fixture precondition: the hull is mid-crossing")

	observer := NewRelocatorFleetObserver(
		&relocatorPortsShipRepo{ships: []*navigation.Ship{flying}},
		&relocatorPortsContainerRepo{},
	)

	hulls, err := observer.ObserveTradeHulls(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, relocatorPortsObserved(t, hulls, "HAULER-A").OnTour,
		"an in-transit hull must not read as at honest release; the relocator would redirect it mid-crossing")
}

// The DEDICATION half of RULINGS #7, enforced by construction: a hull dedicated to another fleet is
// never observed at all, so no scoring path can reach it and no later guard can get it wrong.
func TestRelocatorFleetObserverShould_NeverObserveAHullDedicatedToAnotherFleet(t *testing.T) {
	contractHauler := relocatorPortsHull(t, "CONTRACT-1", "HAULER")
	contractHauler.SetDedicatedFleet(contractFleetTag)
	tradeHauler := relocatorPortsHull(t, "HAULER-A", "HAULER")

	observer := NewRelocatorFleetObserver(
		&relocatorPortsShipRepo{ships: []*navigation.Ship{contractHauler, tradeHauler}},
		&relocatorPortsContainerRepo{},
	)

	hulls, err := observer.ObserveTradeHulls(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, hulls, 1, "only the trade-dedicated hull may be observed; observed %v", hulls)
	require.Equal(t, "HAULER-A", hulls[0].ShipSymbol)
}

// FAIL CLOSED on an unreadable container surface. An empty container list would read as "nobody is
// touring" and promote every touring hull to eligible — the one mistake the tour-container lookup
// exists to prevent — so it must be an error the reconciler retries, never a silent all-clear.
func TestRelocatorFleetObserverShould_ErrorRatherThanReadEveryTouringHullAsIdleWhenContainersAreUnreadable(t *testing.T) {
	clock := shared.NewRealClock()
	touring := relocatorPortsHull(t, "HAULER-A", "HAULER")
	require.NoError(t, touring.AssignToContainer("tour_run-HAULER-A", clock))

	observer := NewRelocatorFleetObserver(
		&relocatorPortsShipRepo{ships: []*navigation.Ship{touring}},
		&relocatorPortsContainerRepo{err: errors.New("container repository unavailable")},
	)

	_, err := observer.ObserveTradeHulls(context.Background(), 1)
	require.Error(t, err, "an unreadable container surface must fail closed; an empty tour set would make every touring hull look relocatable")
}

// An unreadable FLEET is likewise an error the reconciler retries, not an empty fleet.
func TestRelocatorFleetObserverShould_ErrorWhenTheFleetCannotBeRead(t *testing.T) {
	observer := NewRelocatorFleetObserver(
		&relocatorPortsShipRepo{err: errors.New("ship repository unavailable")},
		&relocatorPortsContainerRepo{},
	)

	_, err := observer.ObserveTradeHulls(context.Background(), 1)
	require.Error(t, err, "an unreadable fleet must surface as an error, not as a fleet with no hulls")
}

// --- the intent store's restart contract (RULINGS #2) ---

// relocatorIntentStoreFixture wires the real config-backed store against a persisted relocator
// container, so the round trip crosses the actual JSON column rather than a fake.
func relocatorIntentStoreFixture(t *testing.T, launchConfig string) (*RelocationIntentConfigStore, string, int) {
	t.Helper()
	s, db, playerID := newRecoveryTestServer(t)
	containerID := "opportunity_relocator-player-1"
	insertRunningContainer(t, db, containerID, "opportunity_relocator", string(container.ContainerTypeOpportunityRelocator), launchConfig, playerID, nil)
	return NewRelocationIntentConfigStore(s.containerRepo), containerID, playerID
}

// DecidedAt AND Completed must survive a save/load cycle EXACTLY. DecidedAt is the per-hull cooldown
// clock and the only field a restart cannot re-derive from live state: drift re-opens a hull for
// relocation early, a reset freezes it forever. Completed is the in-flight/landed discriminator the
// restart contract branches on — a completed intent read back as in-flight would re-fly a move that
// already landed.
func TestRelocationIntentStoreShould_RoundTripDecidedAtAndCompletedExactly(t *testing.T) {
	store, containerID, playerID := relocatorIntentStoreFixture(t, `{"container_id":"opportunity_relocator-player-1"}`)
	ctx := context.Background()

	// A nanosecond-bearing instant, so a format that silently truncates to seconds fails here rather
	// than in production where the cooldown would be quietly off.
	decidedAt := time.Date(2026, 7, 30, 20, 14, 37, 123456789, time.UTC)
	written := tradingCmd.RelocationIntent{
		ShipSymbol:     "HAULER-A",
		FromSystem:     "X1-HOME",
		TargetSystem:   "X1-RICH",
		TargetWaypoint: "X1-RICH-MARKET",
		DecidedAt:      decidedAt,
		Completed:      false,
	}
	require.NoError(t, store.RecordRelocationIntent(ctx, containerID, playerID, written))

	loaded, err := store.LoadRelocationIntents(ctx, containerID, playerID)
	require.NoError(t, err)
	require.Len(t, loaded, 1)
	require.Equal(t, written.ShipSymbol, loaded[0].ShipSymbol)
	require.Equal(t, written.FromSystem, loaded[0].FromSystem)
	require.Equal(t, written.TargetSystem, loaded[0].TargetSystem)
	require.Equal(t, written.TargetWaypoint, loaded[0].TargetWaypoint)
	require.True(t, loaded[0].DecidedAt.Equal(decidedAt),
		"DecidedAt round-tripped as %v, want %v; it is the restart-durable cooldown clock, so any drift silently changes when a hull becomes relocatable again", loaded[0].DecidedAt, decidedAt)
	require.False(t, loaded[0].Completed, "an in-flight intent must load back in flight, or a restart would never resume the interrupted jump")

	// Completing REWRITES the same record in place, preserving the ORIGINAL decision time — the
	// cooldown starts when the move was decided, not when it landed.
	completed := loaded[0]
	completed.Completed = true
	require.NoError(t, store.RecordRelocationIntent(ctx, containerID, playerID, completed))

	reloaded, err := store.LoadRelocationIntents(ctx, containerID, playerID)
	require.NoError(t, err)
	require.Len(t, reloaded, 1, "completing an intent must REWRITE the hull's single record, not append a second one")
	require.True(t, reloaded[0].Completed, "the landed intent must load back completed, or the next tick would resume a move that already finished")
	require.True(t, reloaded[0].DecidedAt.Equal(decidedAt),
		"completing the intent moved DecidedAt to %v; the ORIGINAL decision time is the cooldown clock", reloaded[0].DecidedAt)
}

// The store is a read-modify-write of ONE config key, so it must preserve every other hull's intent AND
// the container's launch knobs — which the restart rebuild reads. Clobbering them would silently reset
// the relocator's configuration on its first write.
func TestRelocationIntentStoreShould_PreserveOtherHullsIntentsAndTheContainersLaunchKnobs(t *testing.T) {
	store, containerID, playerID := relocatorIntentStoreFixture(t,
		`{"container_id":"opportunity_relocator-player-1","relocator_uplift_bar_pct":250,"reposition_disabled":true}`)
	ctx := context.Background()

	first := tradingCmd.RelocationIntent{
		ShipSymbol: "HAULER-A", FromSystem: "X1-HOME", TargetSystem: "X1-RICH", TargetWaypoint: "X1-RICH-MARKET",
		DecidedAt: time.Date(2026, 7, 30, 19, 0, 0, 0, time.UTC), Completed: true,
	}
	second := tradingCmd.RelocationIntent{
		ShipSymbol: "HAULER-B", FromSystem: "X1-B", TargetSystem: "X1-RICH-B", TargetWaypoint: "X1-RICH-B-MARKET",
		DecidedAt: time.Date(2026, 7, 30, 20, 0, 0, 0, time.UTC), Completed: false,
	}
	require.NoError(t, store.RecordRelocationIntent(ctx, containerID, playerID, first))
	require.NoError(t, store.RecordRelocationIntent(ctx, containerID, playerID, second))

	loaded, err := store.LoadRelocationIntents(ctx, containerID, playerID)
	require.NoError(t, err)
	require.Len(t, loaded, 2, "one record per hull, both retained; writing B must not drop A's cooldown clock")
	byShip := map[string]tradingCmd.RelocationIntent{}
	for _, intent := range loaded {
		byShip[intent.ShipSymbol] = intent
	}
	require.True(t, byShip["HAULER-A"].DecidedAt.Equal(first.DecidedAt))
	require.True(t, byShip["HAULER-A"].Completed)
	require.True(t, byShip["HAULER-B"].DecidedAt.Equal(second.DecidedAt))
	require.False(t, byShip["HAULER-B"].Completed)

	// The launch knobs the restart rebuild reads must still be there.
	config := relocatorIntentStoreConfig(t, store, containerID, playerID)
	require.EqualValues(t, 250, config["relocator_uplift_bar_pct"], "the uplift-bar knob was clobbered by an intent write; a restart would silently drop back to the default")
	require.Equal(t, true, config["reposition_disabled"], "the shared kill-switch was clobbered by an intent write; a stood-down relocator would come back live on restart")
}

// FAIL CLOSED on a corrupt payload. Reading unparseable intents as "none" is exactly how an in-flight
// move gets re-decided: the hull would be scored again from wherever it happened to be interrupted, and
// every cooldown would read as "never relocated".
func TestRelocationIntentStoreShould_ErrorRatherThanReportNoIntentsWhenThePersistedPayloadIsCorrupt(t *testing.T) {
	store, containerID, playerID := relocatorIntentStoreFixture(t,
		`{"container_id":"opportunity_relocator-player-1","relocation_intents":"not-an-object"}`)

	_, err := store.LoadRelocationIntents(context.Background(), containerID, playerID)
	require.Error(t, err, "a corrupt intent payload must fail closed; reading it as no intents would re-decide an in-flight move")
}

// An ABSENT key is not corruption — it is a relocator that has decided nothing yet, and it must load
// cleanly or the very first tick would error forever.
func TestRelocationIntentStoreShould_LoadCleanlyWhenNoIntentHasEverBeenRecorded(t *testing.T) {
	store, containerID, playerID := relocatorIntentStoreFixture(t, `{"container_id":"opportunity_relocator-player-1"}`)

	loaded, err := store.LoadRelocationIntents(context.Background(), containerID, playerID)
	require.NoError(t, err, "a relocator that has decided nothing must load cleanly, or its first tick errors forever")
	require.Empty(t, loaded)
}

// relocatorIntentStoreConfig reads the container's persisted config back as a map, so a test can assert
// on the keys the intent write had to leave alone.
func relocatorIntentStoreConfig(t *testing.T, store *RelocationIntentConfigStore, containerID string, playerID int) map[string]interface{} {
	t.Helper()
	repo, ok := store.containers.(*persistence.ContainerRepositoryGORM)
	require.True(t, ok, "fixture wires the real container repository")
	model, err := repo.Get(context.Background(), containerID, playerID)
	require.NoError(t, err)
	require.NotNil(t, model)
	config := map[string]interface{}{}
	require.NoError(t, json.Unmarshal([]byte(model.Config), &config))
	return config
}

// --- the era horizon ---

// The era horizon is honestly UNKNOWN, and that must stay a deliberate report rather than drift into an
// invented number. There is no era-END date in the schema (persistence.EraModel carries the era's start
// and a ClosedAt written after the fact), and an invented horizon is the input the endgame guard is most
// sensitive to: too long licenses a move the era cannot repay, too short refuses every move.
//
// Unknown is SAFE, not dormant: trading.ValueRelocation bounds an unknown horizon to the reconciler's
// own horizon knob and keeps valuing moves against it.
func TestRelocatorEraHorizonShould_ReportUnknownRatherThanInventAnEraLength(t *testing.T) {
	hours, known := NewRelocatorEraHorizon().RemainingEraHours(context.Background(), 1)

	require.False(t, known, "the schema holds no era-end date, so any horizon reported as KNOWN would be invented")
	require.Zero(t, hours, "an unknown horizon must carry no number at all; a non-zero value alongside known=false invites a caller to use it")
}

// --- the actuation-time re-read (sp-x2jr6 slice 1, adapter side) ---

// THE RACE, at the adapter boundary: the same observer that reported a hull relocatable must report it
// TAKEN once a tour row appears, without anything else changing. This is the live TORWIND-5F shape — the
// tour container was created four seconds AFTER the fleet snapshot and ten seconds before the decision.
func TestRelocatorFleetObserverShould_SeeATourThatAppearedAfterTheBulkObservation(t *testing.T) {
	hull := relocatorPortsHull(t, "HAULER-A", "HAULER")
	containers := &relocatorPortsContainerRepo{}
	observer := NewRelocatorFleetObserver(&relocatorPortsShipRepo{ships: []*navigation.Ship{hull}}, containers)
	ctx := context.Background()

	// t0: no tour anywhere. The hull is relocatable, which is what makes it a candidate.
	bulk, err := observer.ObserveTradeHulls(ctx, 1)
	require.NoError(t, err)
	require.False(t, relocatorPortsObserved(t, bulk, "HAULER-A").OnTour, "fixture precondition: the hull is eligible at observation")

	// t0 + the pre-flight: a tour container is created for the hull and has not claimed it yet.
	containers.byStatus = map[container.ContainerStatus][]*persistence.ContainerModel{
		container.ContainerStatusRunning: {{
			ID: "tour_run-HAULER-A", CommandType: tourRunCommandType, Config: `{"ship_symbol":"HAULER-A"}`,
		}},
	}

	reread, err := observer.ObserveHull(ctx, 1, "HAULER-A")
	require.NoError(t, err)
	require.True(t, reread.OnTour,
		"the re-read did not see a tour created since the snapshot; it must re-read the container rows, not reuse the occupancy map the snapshot was built from — otherwise it answers the stale question it exists to re-ask")
}

// The re-read must derive its facts EXACTLY as the bulk observation does, or the commit gate and the
// scoring gate disagree about what a protected hull is and the guard is bypassable through the gap.
func TestRelocatorFleetObserverShould_DeriveTheReReadFactsIdenticallyToTheBulkObservation(t *testing.T) {
	clock := shared.NewRealClock()
	frigate := relocatorPortsHull(t, "COMMAND-1", commandRole)
	reserved := relocatorPortsHull(t, "RESERVED-1", "HAULER")
	require.NoError(t, reserved.ReserveByCaptain("operator is flying it by hand", clock))
	claimed := relocatorPortsHull(t, "CLAIMED-1", "HAULER")
	require.NoError(t, claimed.AssignToContainer("longhaul_arb-OTHER", clock))
	idle := relocatorPortsHull(t, "HAULER-A", "HAULER")
	// IN TRANSIT is in this fixture deliberately: it is the shape the live loss actually took. The
	// TORWIND-5F jump failed with API 4214 "Ship is currently in-transit" — the tour was already flying
	// the hull — which is a claim-race failure, NOT the unroutable-gate class that sp-ky85o cut by 97%.
	// So the re-read must agree with the bulk observation on THIS fact above all others.
	flying := relocatorPortsHull(t, "FLYING-1", "HAULER")
	destination, err := shared.NewWaypoint("X1-FAR-B2", 10, 10)
	require.NoError(t, err)
	_, err = flying.EnsureInOrbit()
	require.NoError(t, err)
	require.NoError(t, flying.StartTransit(destination))

	observer := NewRelocatorFleetObserver(
		&relocatorPortsShipRepo{ships: []*navigation.Ship{frigate, reserved, claimed, idle, flying}},
		&relocatorPortsContainerRepo{byStatus: map[container.ContainerStatus][]*persistence.ContainerModel{
			container.ContainerStatusRunning: {{ID: "longhaul_arb-OTHER", CommandType: "longhaul_arb"}},
		}},
	)
	ctx := context.Background()

	bulk, err := observer.ObserveTradeHulls(ctx, 1)
	require.NoError(t, err)
	require.Len(t, bulk, 5)
	require.True(t, relocatorPortsObserved(t, bulk, "FLYING-1").OnTour, "fixture precondition: the in-transit hull reads busy")

	for _, want := range bulk {
		got, rerr := observer.ObserveHull(ctx, 1, want.ShipSymbol)
		require.NoError(t, rerr, "re-reading %s", want.ShipSymbol)
		require.Equal(t, want, got,
			"%s reads differently at the actuation re-check than at observation; the two gates must share one derivation or the guard is bypassable through the gap", want.ShipSymbol)
	}
}

// A hull that is no longer a trade hull is an ERROR, not a relocatable hull with default facts. The
// caller fails the move closed on it, which is right: a re-dedicated, sold, or vanished hull is
// emphatically not one this reconciler may move.
func TestRelocatorFleetObserverShould_ErrorWhenTheReReadHullIsNoLongerRelocatable(t *testing.T) {
	reDedicated := relocatorPortsHull(t, "GONE-1", "HAULER")
	reDedicated.SetDedicatedFleet(contractFleetTag)
	observer := NewRelocatorFleetObserver(
		&relocatorPortsShipRepo{ships: []*navigation.Ship{reDedicated}},
		&relocatorPortsContainerRepo{},
	)
	ctx := context.Background()

	_, err := observer.ObserveHull(ctx, 1, "GONE-1")
	require.Error(t, err, "a hull re-dedicated away from the trade fleet must fail the re-read, not come back with zero-valued (unprotected) facts")

	_, err = observer.ObserveHull(ctx, 1, "NEVER-EXISTED")
	require.Error(t, err, "a hull absent from the fleet must fail the re-read; zero-valued facts would read as fully relocatable")
}

// A tour row whose launch config cannot be parsed must degrade in the SAFE direction, in both halves:
// it still guards the CLAIMED window through its container id, and it does not take the whole
// observation down with it.
//
// Erroring instead would let ONE malformed row hold the entire reconciler dormant — the
// silent-dormancy failure this observer has already been bitten by — while dropping the row entirely
// would un-guard a hull a tour is actively driving. So the row contributes its id but no declared hull,
// and the only thing lost is the pre-claim window for a tour whose config is already corrupt.
func TestRelocatorFleetObserverShould_DegradeSafelyOnATourRowWhoseConfigCannotBeParsed(t *testing.T) {
	clock := shared.NewRealClock()
	claimed := relocatorPortsHull(t, "HAULER-CLAIMED", "HAULER")
	require.NoError(t, claimed.AssignToContainer("tour_run-BROKEN", clock))
	idle := relocatorPortsHull(t, "HAULER-IDLE", "HAULER")

	observer := NewRelocatorFleetObserver(
		&relocatorPortsShipRepo{ships: []*navigation.Ship{claimed, idle}},
		&relocatorPortsContainerRepo{byStatus: map[container.ContainerStatus][]*persistence.ContainerModel{
			container.ContainerStatusRunning: {{
				ID: "tour_run-BROKEN", CommandType: tourRunCommandType, Config: `{not valid json at all`,
			}},
		}},
	)

	hulls, err := observer.ObserveTradeHulls(context.Background(), 1)
	require.NoError(t, err, "one malformed tour row must not fail the whole observation; that would hold the reconciler dormant until someone fixed a row")

	require.True(t, relocatorPortsObserved(t, hulls, "HAULER-CLAIMED").OnTour,
		"the hull CLAIMED by the malformed tour row must still read OnTour — the row's container id is enough for the claimed window, and dropping the row entirely would un-guard a hull a tour is driving")
	require.False(t, relocatorPortsObserved(t, hulls, "HAULER-IDLE").OnTour,
		"an unrelated idle hull must not be excluded by someone else's malformed row")
	require.False(t, relocatorPortsObserved(t, hulls, "HAULER-IDLE").Pinned,
		"an unrelated idle hull must not be pinned by someone else's malformed row")
}
