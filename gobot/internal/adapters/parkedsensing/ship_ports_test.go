package parkedsensing_test

// Integration tests (real GORM/sqlite, no mocks) for the ships-table reads
// behind the parked-probe sensing engine. The interesting behaviour is not
// round-tripping rows — it is the SELECTION PREDICATE that decides which hull
// the buy queue is handed as a purchasing ship, because a hull selected here
// that cannot actually be claimed becomes a permanent, per-tick API drain
// rather than a one-off failure.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

const testPlayerID = 1

// newShipPortsDB returns a test DB with the two player rows the ships table's
// foreign key requires — the player under test, and a second one used to prove
// cross-player isolation.
func newShipPortsDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	for _, id := range []int{testPlayerID, testPlayerID + 1} {
		require.NoError(t, db.Create(&persistence.PlayerModel{
			ID: id, AgentSymbol: fmt.Sprintf("AGENT-%d", id), Token: "t", CreatedAt: time.Now().UTC(),
		}).Error)
	}
	return db
}

// probeRow builds a docked SATELLITE at a waypoint, carrying a fleet tag.
func probeRow(symbol, waypoint, fleet string) persistence.ShipModel {
	return persistence.ShipModel{
		ShipSymbol:     symbol,
		PlayerID:       testPlayerID,
		NavStatus:      string(navigation.NavStatusDocked),
		LocationSymbol: waypoint,
		Role:           "SATELLITE",
		DedicatedFleet: fleet,
	}
}

func TestDockedProbeAt_SelectsUndedicatedAndOwnFleetHulls(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fleet string
	}{
		{"undedicated", ""},
		{"our own sensing fleet", appSensing.SensingParkedFleetTag},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newShipPortsDB(t)
			require.NoError(t, db.Create(&persistence.ShipModel{
				ShipSymbol: "PROBE-1", PlayerID: testPlayerID,
				NavStatus: string(navigation.NavStatusDocked), LocationSymbol: "X1-AA-Y1",
				Role: "SATELLITE", DedicatedFleet: tc.fleet,
			}).Error)

			port := adapterSensing.NewShipPositionPort(db)
			ship, found, err := port.DockedProbeAt(context.Background(), testPlayerID, "X1-AA-Y1")
			require.NoError(t, err)
			require.True(t, found, "a hull we may drive was not offered as a purchasing ship")
			require.Equal(t, "PROBE-1", ship)
		})
	}
}

// TestDockedProbeAt_SkipsForeignDedicatedHulls is the load-bearing one. A hull
// tagged to another fleet is rejected by the claim path PERMANENTLY, so offering
// it would make the buy queue select it, burn a live shipyard price read, fail,
// and select the identical hull again next tick — forever.
func TestDockedProbeAt_SkipsForeignDedicatedHulls(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "PROBE-CONTRACT", PlayerID: testPlayerID,
		NavStatus: string(navigation.NavStatusDocked), LocationSymbol: "X1-AA-Y1",
		Role: "SATELLITE", DedicatedFleet: "contract",
	}).Error)

	port := adapterSensing.NewShipPositionPort(db)
	_, found, err := port.DockedProbeAt(context.Background(), testPlayerID, "X1-AA-Y1")
	require.NoError(t, err)
	require.False(t, found, "offered a hull dedicated to another fleet, which can never be claimed")
}

// TestDockedProbeAt_PrefersADrivableHullOverAForeignOne pins that one
// unusable hull at a yard does not mask a usable one standing beside it.
func TestDockedProbeAt_PrefersADrivableHullOverAForeignOne(t *testing.T) {
	db := newShipPortsDB(t)
	// "AAA" sorts first, so a naive query ordered by symbol would return it.
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "PROBE-AAA", PlayerID: testPlayerID,
		NavStatus: string(navigation.NavStatusDocked), LocationSymbol: "X1-AA-Y1",
		Role: "SATELLITE", DedicatedFleet: "trade",
	}).Error)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "PROBE-ZZZ", PlayerID: testPlayerID,
		NavStatus: string(navigation.NavStatusDocked), LocationSymbol: "X1-AA-Y1",
		Role: "SATELLITE", DedicatedFleet: "",
	}).Error)

	port := adapterSensing.NewShipPositionPort(db)
	ship, found, err := port.DockedProbeAt(context.Background(), testPlayerID, "X1-AA-Y1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "PROBE-ZZZ", ship, "a foreign hull masked the drivable one standing beside it")
}

func TestDockedProbeAt_SkipsHullsThatCannotBuy(t *testing.T) {
	db := newShipPortsDB(t)
	// In orbit, not at the counter.
	orbiting := probeRow("PROBE-ORBIT", "X1-AA-Y1", "")
	orbiting.NavStatus = string(navigation.NavStatusInOrbit)
	require.NoError(t, db.Create(&orbiting).Error)
	// Docked, but not a probe.
	hauler := probeRow("HAULER-1", "X1-AA-Y1", "")
	hauler.Role = "HAULER"
	require.NoError(t, db.Create(&hauler).Error)
	// A probe of ours, but at another waypoint.
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "PROBE-ELSEWHERE", PlayerID: testPlayerID,
		NavStatus: string(navigation.NavStatusDocked), LocationSymbol: "X1-AA-Y2",
		Role: "SATELLITE",
	}).Error)
	// Another player's probe standing right here.
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "PROBE-THEIRS", PlayerID: testPlayerID + 1,
		NavStatus: string(navigation.NavStatusDocked), LocationSymbol: "X1-AA-Y1",
		Role: "SATELLITE",
	}).Error)

	port := adapterSensing.NewShipPositionPort(db)
	_, found, err := port.DockedProbeAt(context.Background(), testPlayerID, "X1-AA-Y1")
	require.NoError(t, err)
	require.False(t, found, "offered a hull that cannot buy at this counter")
}

func TestShipAt_ReportsPositionAndAbsence(t *testing.T) {
	db := newShipPortsDB(t)
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol: "PROBE-1", PlayerID: testPlayerID,
		NavStatus: string(navigation.NavStatusInOrbit), LocationSymbol: "X1-AA-M1",
		Role: "SATELLITE",
	}).Error)

	port := adapterSensing.NewShipPositionPort(db)

	pos, err := port.ShipAt(context.Background(), testPlayerID, "PROBE-1")
	require.NoError(t, err)
	require.True(t, pos.Found)
	require.Equal(t, "X1-AA-M1", pos.Waypoint)
	require.Equal(t, navigation.NavStatusInOrbit, pos.NavStatus)

	// A hull the table does not know is an ANSWER, not an error — the caller
	// leaves the placement alone either way.
	missing, err := port.ShipAt(context.Background(), testPlayerID, "PROBE-GHOST")
	require.NoError(t, err)
	require.False(t, missing.Found)
}
