package api

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

// navOpsFakeAPIClient answers the five state-changing verbs the operation methods
// call, standing in for the live API. Every verb succeeds: these tests are about
// what reaches the ROW afterwards, not about API failure handling.
type navOpsFakeAPIClient struct {
	domainPorts.APIClient
	navResult    *navigation.Result
	refuelResult *navigation.RefuelResult
}

func (f *navOpsFakeAPIClient) NavigateShip(_ context.Context, _, _, _ string) (*navigation.Result, error) {
	return f.navResult, nil
}
func (f *navOpsFakeAPIClient) DockShip(_ context.Context, _, _ string) error  { return nil }
func (f *navOpsFakeAPIClient) OrbitShip(_ context.Context, _, _ string) error { return nil }
func (f *navOpsFakeAPIClient) RefuelShip(_ context.Context, _, _ string, _ *int) (*navigation.RefuelResult, error) {
	return f.refuelResult, nil
}
func (f *navOpsFakeAPIClient) SetFlightMode(_ context.Context, _, _, _ string) error { return nil }

// navOpsArrival is the arrival clock the fake navigate response reports.
var navOpsArrival = time.Date(2031, 3, 4, 5, 6, 7, 0, time.UTC)

func newNavOpsTestRepo(t *testing.T) (*ShipRepository, *gorm.DB, shared.PlayerID) {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	row := persistence.PlayerModel{AgentSymbol: "TORWIND", Token: "tok", CreatedAt: time.Now()}
	require.NoError(t, db.Create(&row).Error)
	playerID := shared.MustNewPlayerID(row.ID)

	apiClient := &navOpsFakeAPIClient{
		navResult: &navigation.Result{
			Destination:    "X1-KN67-B2",
			ArrivalTimeStr: navOpsArrival.Format(time.RFC3339),
			FuelConsumed:   12,
			FlightMode:     "CRUISE",
		},
		refuelResult: &navigation.RefuelResult{
			FuelCurrent: 950, FuelCapacity: 1000, CreditsCost: 200,
			AgentCredits: intPtr(71_500),
		},
	}
	repo := NewShipRepository(
		apiClient,
		&refuelFakePlayerRepo{p: &player.Player{ID: playerID, Token: "tok"}},
		nil, stubWaypoints{}, db, nil,
	)
	return repo, db, playerID
}

// staleArrivalClock is the arrival time of a transit that has already landed —
// seeded so an operation that owns the clock is seen to CLEAR it, and one that
// does not is seen to leave it alone.
var staleArrivalClock = time.Date(2029, 1, 2, 3, 4, 5, 0, time.UTC)

// seedLoadedHull seeds a hull carrying the state a long-held nav entity picks up
// at load time: a hold with cargo in it, a tank, an arrival clock, and a nav
// status.
func seedLoadedHull(t *testing.T, db *gorm.DB, playerID int, symbol, navStatus string) {
	t.Helper()
	arrival := staleArrivalClock
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:       symbol,
		PlayerID:         playerID,
		AssignmentStatus: "idle",
		NavStatus:        navStatus,
		FlightMode:       "CRUISE",
		ArrivalTime:      &arrival,
		LocationSymbol:   "X1-KN67-A1",
		SystemSymbol:     "X1-KN67",
		FuelCurrent:      400,
		FuelCapacity:     1000,
		CargoCapacity:    40,
		CargoUnits:       30,
		CargoInventory:   `[{"symbol":"IRON_ORE","name":"Iron Ore","description":"ore","units":30}]`,
		EngineSpeed:      10,
		Version:          1,
	}).Error)
}

// deliverCargoOnRow is another writer emptying the hold — the sell/deliver
// write-back that made the entity's cargo snapshot obsolete.
func deliverCargoOnRow(t *testing.T, db *gorm.DB, symbol string) {
	t.Helper()
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("ship_symbol = ?", symbol).
		Updates(map[string]interface{}{
			"cargo_units":     0,
			"cargo_inventory": "[]",
			"version":         gorm.Expr("version + 1"),
		}).Error)
}

// refuelOnRow is another writer filling the tank behind the entity's back.
func refuelOnRow(t *testing.T, db *gorm.DB, symbol string) {
	t.Helper()
	require.NoError(t, db.Model(&persistence.ShipModel{}).
		Where("ship_symbol = ?", symbol).
		Updates(map[string]interface{}{
			"fuel_current": 900,
			"version":      gorm.Expr("version + 1"),
		}).Error)
}

func loadShipRow(t *testing.T, db *gorm.DB, symbol string) persistence.ShipModel {
	t.Helper()
	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", symbol).First(&row).Error)
	return row
}

// navOperation is one of the five ShipRepository methods that call the API,
// mutate one or two fields of the caller's ship entity, and persist.
type navOperation struct {
	name        string
	startNav    string
	run         func(t *testing.T, repo *ShipRepository, ship *navigation.Ship, playerID shared.PlayerID)
	assertOwned func(t *testing.T, row persistence.ShipModel)
}

func navOperations() []navOperation {
	return []navOperation{
		{
			name:     "navigate",
			startNav: "IN_ORBIT",
			run: func(t *testing.T, repo *ShipRepository, ship *navigation.Ship, playerID shared.PlayerID) {
				destination, err := shared.NewWaypoint("X1-KN67-B2", 5, 5)
				require.NoError(t, err)
				_, err = repo.Navigate(context.Background(), ship, destination, playerID)
				require.NoError(t, err)
			},
			assertOwned: func(t *testing.T, row persistence.ShipModel) {
				assert.Equal(t, "IN_TRANSIT", row.NavStatus, "navigate owns nav_status")
				assert.Equal(t, "X1-KN67-B2", row.LocationSymbol, "navigate owns the location")
				assert.Equal(t, 5.0, row.LocationX)
				assert.Equal(t, 5.0, row.LocationY)
				assert.Equal(t, "X1-KN67", row.SystemSymbol)
				require.NotNil(t, row.ArrivalTime, "navigate owns the arrival clock")
				require.True(t, navOpsArrival.Equal(*row.ArrivalTime))
				assert.Equal(t, "CRUISE", row.FlightMode, "navigate owns the flight mode it flew")
				// Navigate owns fuel: it just burnt some, and the API told it how
				// much. It writes its own arithmetic (400 - 12), not the 900 a
				// concurrent refuel left on the row.
				assert.Equal(t, 388, row.FuelCurrent, "navigate owns fuel — the burn lands")
			},
		},
		{
			name:     "dock",
			startNav: "IN_ORBIT",
			run: func(t *testing.T, repo *ShipRepository, ship *navigation.Ship, playerID shared.PlayerID) {
				require.NoError(t, repo.Dock(context.Background(), ship, playerID))
			},
			assertOwned: func(t *testing.T, row persistence.ShipModel) {
				assert.Equal(t, "DOCKED", row.NavStatus, "dock owns nav_status")
				assert.Equal(t, 900, row.FuelCurrent, "dock changes no fuel at the server, so it writes none")
				assert.NotNil(t, row.ArrivalTime, "dock does not own the arrival clock")
			},
		},
		{
			name:     "orbit",
			startNav: "DOCKED",
			run: func(t *testing.T, repo *ShipRepository, ship *navigation.Ship, playerID shared.PlayerID) {
				require.NoError(t, repo.Orbit(context.Background(), ship, playerID))
			},
			assertOwned: func(t *testing.T, row persistence.ShipModel) {
				assert.Equal(t, "IN_ORBIT", row.NavStatus, "orbit owns nav_status")
				assert.Nil(t, row.ArrivalTime, "orbit clears the landed transit's clock, so it owns that column too")
				assert.Equal(t, 900, row.FuelCurrent, "orbit changes no fuel at the server, so it writes none")
			},
		},
		{
			name:     "refuel",
			startNav: "DOCKED",
			run: func(t *testing.T, repo *ShipRepository, ship *navigation.Ship, playerID shared.PlayerID) {
				result, err := repo.Refuel(context.Background(), ship, playerID, nil)
				require.NoError(t, err)
				// The handler bills the ledger from this result — the cost and the
				// authoritative post-transaction balance must both survive the seam.
				require.Equal(t, 200, result.CreditsCost)
				require.NotNil(t, result.AgentCredits)
				require.Equal(t, 71_500, *result.AgentCredits)
			},
			assertOwned: func(t *testing.T, row persistence.ShipModel) {
				assert.Equal(t, 950, row.FuelCurrent, "refuel owns fuel — the tank the API reported lands")
				assert.Equal(t, 1000, row.FuelCapacity)
				assert.Equal(t, "DOCKED", row.NavStatus, "refuel changes no nav state")
				assert.NotNil(t, row.ArrivalTime, "refuel does not own the arrival clock")
			},
		},
		{
			name:     "set flight mode",
			startNav: "IN_ORBIT",
			run: func(t *testing.T, repo *ShipRepository, ship *navigation.Ship, playerID shared.PlayerID) {
				require.NoError(t, repo.SetFlightMode(context.Background(), ship, playerID, "BURN"))
			},
			assertOwned: func(t *testing.T, row persistence.ShipModel) {
				assert.Equal(t, "BURN", row.FlightMode, "set flight mode owns flight_mode")
				assert.Equal(t, "IN_ORBIT", row.NavStatus, "set flight mode changes no nav state")
				assert.Equal(t, 900, row.FuelCurrent, "set flight mode changes no fuel at the server, so it writes none")
				assert.NotNil(t, row.ArrivalTime, "set flight mode does not own the arrival clock")
			},
		},
	}
}

// TestNavOperations_PersistOnlyTheColumnsTheyOwn is the root-cause regression.
//
// Each of these five methods calls the API, mutates one or two fields of a ship
// entity the caller may have held for an entire flight, and persists. Persisting
// the WHOLE row from that snapshot rewrites cargo, ownership and dedication from
// a view the operation has no reason to believe is current — the mechanism behind
// both a resurrected claim and a restored-then-liquidated hold.
//
// The world moves on behind the entity exactly as it does in production: the
// claim is released, the hull is re-dedicated, the hold is emptied, the tank is
// filled. Every one of those columns must survive, and the operation's own
// columns must still land.
func TestNavOperations_PersistOnlyTheColumnsTheyOwn(t *testing.T) {
	for _, operation := range navOperations() {
		t.Run(operation.name, func(t *testing.T) {
			repo, db, playerID := newNavOpsTestRepo(t)
			seedLoadedHull(t, db, playerID.Value(), "TORWIND-40", operation.startNav)
			seedContainerParent(t, db, "contract-worker-9", playerID.Value())
			require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-40", "contract-worker-9", playerID, "contract"))

			// The nav stack loads the hull ONCE and carries this entity for the
			// whole flight.
			inFlight, err := repo.FindBySymbol(context.Background(), "TORWIND-40", playerID)
			require.NoError(t, err)
			require.Equal(t, "contract-worker-9", inFlight.ContainerID(), "precondition: the snapshot holds the claim")
			require.Equal(t, 30, inFlight.Cargo().Units, "precondition: the snapshot holds a full hold")
			require.Equal(t, 400, inFlight.Fuel().Current, "precondition: the snapshot holds the old tank")

			// Everything the entity cannot know about, while it is in the air.
			released, err := repo.ReleaseContainerClaim(context.Background(), "TORWIND-40", playerID, "container_stopped")
			require.NoError(t, err)
			require.Equal(t, "contract-worker-9", released, "the break names the container that lost the hull (sp-h8mbb)")
			require.NoError(t, repo.AssignFleet(context.Background(), "TORWIND-40", "manufacturing", playerID))
			deliverCargoOnRow(t, db, "TORWIND-40")
			refuelOnRow(t, db, "TORWIND-40")

			conflicts := shipVersionConflicts.Load()
			assignmentClobbers := assignmentClobbersPrevented.Load()
			dedicationClobbers := dedicatedFleetClobbersPrevented.Load()

			operation.run(t, repo, inFlight, playerID)

			row := loadShipRow(t, db, "TORWIND-40")
			operation.assertOwned(t, row)

			// Independent facts about one write — asserted, not required, so a
			// regression reports every invariant it broke rather than just the first.
			assert.Equal(t, "idle", row.AssignmentStatus, "the operation does not own ownership and must not write it")
			assert.Nil(t, row.ContainerID, "a released hull must stay free, not orphaned under a dead container")
			assert.Equal(t, "manufacturing", row.DedicatedFleet, "the operation does not own dedication and must not write it")
			assert.Equal(t, 0, row.CargoUnits, "the operation does not own cargo and must not restore an emptied hold")
			assert.Equal(t, "[]", row.CargoInventory, "the inventory must stay emptied too, not just the count")

			// The last-resort guards exist for whole-row writers. A write scoped to
			// what the operation owns never puts these columns at risk, so the
			// guards are never consulted and no conflict is logged.
			assert.Equal(t, conflicts, shipVersionConflicts.Load(),
				"a scoped write is not a whole-row race and must not be reported as one")
			assert.Equal(t, assignmentClobbers, assignmentClobbersPrevented.Load(),
				"ownership is never carried, so the ownership guard has nothing to prevent")
			assert.Equal(t, dedicationClobbers, dedicatedFleetClobbersPrevented.Load(),
				"dedication is never carried, so the dedication guard has nothing to prevent")
		})
	}
}

// The observed sp-7oxeo sequence end to end, on one continuously-running daemon:
// a coordinator claims the hull, the nav stack loads that entity and flies the
// whole leg on it, the coordinator exits and its release lands — and the flight's
// arrival write-back puts the dead claim back on the row. The hull is then
// permanently orphaned under a container that no longer exists, so no release
// path will ever free it. It happened five times on one hull in one day.
func TestNavOperations_AReleasedHullStaysReleasedThroughAnArrival(t *testing.T) {
	repo, db, playerID := newNavOpsTestRepo(t)
	seedLoadedHull(t, db, playerID.Value(), "TORWIND-41", "DOCKED")
	seedContainerParent(t, db, "cargo-liquidation-48887734", playerID.Value())
	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-41", "cargo-liquidation-48887734", playerID, "contract"))

	inFlight, err := repo.FindBySymbol(context.Background(), "TORWIND-41", playerID)
	require.NoError(t, err)
	require.Equal(t, "cargo-liquidation-48887734", inFlight.ContainerID(), "precondition: the snapshot holds the claim")

	released, err := repo.ReleaseContainerClaim(context.Background(), "TORWIND-41", playerID, "container_stopped")
	require.NoError(t, err)
	require.Equal(t, "cargo-liquidation-48887734", released,
		"precondition: the release freed the hull AND named the container that lost it (sp-h8mbb)")

	prevented := assignmentClobbersPrevented.Load()

	// The flight arrives: orbit, then a routine top-up.
	require.NoError(t, repo.Orbit(context.Background(), inFlight, playerID))
	_, err = repo.Refuel(context.Background(), inFlight, playerID, nil)
	require.NoError(t, err)

	row := loadShipRow(t, db, "TORWIND-41")
	require.Equal(t, "idle", row.AssignmentStatus, "a released hull must stay released")
	require.Nil(t, row.ContainerID, "the hull must stay free, not orphaned under a container that no longer exists")
	require.Equal(t, "IN_ORBIT", row.NavStatus, "the arrival still lands")
	require.Equal(t, 950, row.FuelCurrent, "the refuel still lands")
	require.Equal(t, prevented, assignmentClobbersPrevented.Load(),
		"the claim is defended by never being carried, not by a guard catching it on the way out")
}

// The observed sp-in36a sequence end to end. A hull delivers its hold; the
// delivery is persisted by its own writer. The nav operations still holding the
// pre-delivery snapshot then put the cargo back on the row, and auto-liquidation
// — which reads that column to decide there is something to sell — dispatches
// against an empty hull. Six spawns in eighty minutes sold nothing, each one
// stealing the hull off contract work.
func TestNavOperations_ADeliveredHoldIsNotRestoredByAFlight(t *testing.T) {
	repo, db, playerID := newNavOpsTestRepo(t)
	seedLoadedHull(t, db, playerID.Value(), "TORWIND-42", "IN_ORBIT")

	inFlight, err := repo.FindBySymbol(context.Background(), "TORWIND-42", playerID)
	require.NoError(t, err)
	require.Equal(t, 30, inFlight.Cargo().Units, "precondition: the snapshot predates the delivery")

	deliverCargoOnRow(t, db, "TORWIND-42")

	// The rest of the flight, on the pre-delivery entity.
	require.NoError(t, repo.Dock(context.Background(), inFlight, playerID))
	_, err = repo.Refuel(context.Background(), inFlight, playerID, nil)
	require.NoError(t, err)
	require.NoError(t, repo.Orbit(context.Background(), inFlight, playerID))
	destination, err := shared.NewWaypoint("X1-KN67-B2", 5, 5)
	require.NoError(t, err)
	_, err = repo.Navigate(context.Background(), inFlight, destination, playerID)
	require.NoError(t, err)

	row := loadShipRow(t, db, "TORWIND-42")
	require.Equal(t, 0, row.CargoUnits, "a delivered hold must stay empty — liquidation reads this column")
	require.Equal(t, "[]", row.CargoInventory)
	require.Equal(t, "IN_TRANSIT", row.NavStatus, "the flight still lands its own state")
}

// A hull the daemon has never persisted has no row to scope a write to, and no
// other writer's columns to protect — the whole-row insert is the only way to
// create it, and it must still happen. This is the path a freshly-purchased hull
// takes when its first operation lands before its first sync.
func TestNavOperations_AHullWithNoRowYetIsStillInserted(t *testing.T) {
	repo, db, playerID := newNavOpsTestRepo(t)

	location, err := shared.NewWaypoint("X1-KN67-A1", 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 1000)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	unpersisted, err := navigation.NewShip(
		"TORWIND-44", playerID, location, fuel, 1000, 40, cargo,
		10, "FRAME_HAULER", "HAULER", nil, navigation.NavStatusInOrbit,
	)
	require.NoError(t, err)

	require.NoError(t, repo.Dock(context.Background(), unpersisted, playerID))

	row := loadShipRow(t, db, "TORWIND-44")
	require.Equal(t, "DOCKED", row.NavStatus, "the first operation on an unpersisted hull must create its row")
	require.Equal(t, 400, row.FuelCurrent)
}

// Entity coherence after a scoped write, pinned so it cannot be regressed
// silently.
//
// A scoped write advances the ROW version — nav state is exactly what a
// concurrent whole-row writer would otherwise clobber, so a snapshot older than
// this write must lose its next CAS. It deliberately does NOT advance the
// ENTITY's version: the entity is still not a trustworthy source for a whole-row
// write (its cargo and ownership have moved on), and advancing it would make its
// next Save win the CAS outright and silently bypass the ownership guard that
// catches exactly that write.
func TestNavOperations_ScopedWriteLeavesTheEntityUntrustedForAWholeRowSave(t *testing.T) {
	repo, db, playerID := newNavOpsTestRepo(t)
	seedLoadedHull(t, db, playerID.Value(), "TORWIND-43", "IN_ORBIT")
	seedContainerParent(t, db, "contract-worker-10", playerID.Value())
	require.NoError(t, repo.ClaimShip(context.Background(), "TORWIND-43", "contract-worker-10", playerID, "contract"))

	inFlight, err := repo.FindBySymbol(context.Background(), "TORWIND-43", playerID)
	require.NoError(t, err)
	loadedVersion := inFlight.PersistedVersion()

	require.NoError(t, repo.Dock(context.Background(), inFlight, playerID))

	require.Equal(t, loadedVersion, inFlight.PersistedVersion(),
		"a scoped write must not certify the entity as current for columns it did not write")
	require.Greater(t, loadShipRow(t, db, "TORWIND-43").Version, loadedVersion,
		"the row moves, so a snapshot older than this write loses its next CAS")

	// The container exits, and something later saves the whole row from this same
	// stale entity. The ownership guard must still be the one that catches it.
	released, err := repo.ReleaseContainerClaim(context.Background(), "TORWIND-43", playerID, "container_stopped")
	require.NoError(t, err)
	require.Equal(t, "contract-worker-10", released, "the break names the container that lost the hull (sp-h8mbb)")

	prevented := assignmentClobbersPrevented.Load()
	require.NoError(t, repo.Save(context.Background(), inFlight))

	row := loadShipRow(t, db, "TORWIND-43")
	require.Equal(t, "idle", row.AssignmentStatus, "the whole-row save must not resurrect the released claim")
	require.Nil(t, row.ContainerID)
	require.Equal(t, prevented+1, assignmentClobbersPrevented.Load(),
		"the guard stays armed for whole-row writers — the scoped write must not have disarmed it")
}
