package parkedsensing_test

// The regression proof for the sensing probe-buy claim's foreign-key violation.
//
// THE DEFECT. ProbePurchasePort.Buy holds an exclusive single-writer claim on the
// purchasing hull for the length of the purchase, and that claim is written to
// ships.container_id — a column carrying a composite foreign key to
// containers(id, player_id). The adapter passed a descriptive LABEL
// ("parked_sensing_buy") into ClaimShip's containerID parameter. No such
// container row exists, Postgres refused the write with fk_ships_container
// (23503), and because the claim fails CLOSED the purchase never happened. The
// engine never completed a single probe buy in its existence: no probe, no spare
// hull, no charting seed, and the fleet stopped discovering new systems.
//
// WHY IT REACHED PRODUCTION, AND WHAT THESE TESTS DO DIFFERENTLY. Every existing
// test of this path used a fake ship repository whose ClaimShip accepts any
// string it is handed (see probeFakeShipRepo in the sibling expansion adapter's
// tests, which records containerID in a map). A fake that ignores the argument
// cannot distinguish a real container id from a label, so it passes either way —
// which is exactly how the label shipped.
//
// These tests use the REAL api.ShipRepository against the REAL schema with SQLite
// foreign keys ENFORCED (NewTestConnection turns PRAGMA foreign_keys ON, sp-55aa),
// so the claim is checked against the same constraint production Postgres applies.
// Restore the label and TestProbeBuy_ClaimsBuyerToTheDrivingContainer fails on the
// claim, exactly as the live fleet did.

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sensingBuyContainerID is a realistic sensing-coordinator container id, matching
// the shape the daemon mints for this operation on the live fleet.
const sensingBuyContainerID = "probe_sensing_coordinator-player-1-24f32043"

// buyYard and buyerHull mirror the live refusal this fixes:
// "refused: buy at X1-KP23-C38 via TORWIND-11".
const (
	buyYard   = "X1-KP23-C38"
	buyerHull = "TORWIND-11"
)

// sensingBuyMediator answers the one command Buy dispatches, and — the load-bearing
// part — reads the ships table back AT THE MOMENT the purchase is issued. That is
// the only window in which the claim is observable: Buy releases it on return, so a
// post-hoc read would show an idle hull whether or not the claim ever landed.
//
// Embeds common.Mediator so any OTHER request nil-panics, keeping the fake honest
// about what the adapter actually sends.
type sensingBuyMediator struct {
	common.Mediator

	db *gorm.DB

	// sawPurchase reports whether the purchase was reached at all. It is what
	// separates "the claim failed and no money moved" from "the buy went through".
	sawPurchase bool
	// ownerMidBuy is ships.container_id as it stood while the claim was live.
	ownerMidBuy *string
	// statusMidBuy is the assignment_status observed in the same read.
	statusMidBuy string
}

func (m *sensingBuyMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	cmd, ok := request.(*shipyardCmd.PurchaseShipCommand)
	if !ok {
		return nil, fmt.Errorf("sensingBuyMediator: unexpected request %T", request)
	}
	m.sawPurchase = true

	var row persistence.ShipModel
	if err := m.db.Where("ship_symbol = ? AND player_id = ?", cmd.PurchasingShipSymbol, testPlayerID).
		First(&row).Error; err == nil {
		m.ownerMidBuy = row.ContainerID
		m.statusMidBuy = row.AssignmentStatus
	}

	return &shipyardCmd.PurchaseShipResponse{
		Ship:          boughtProbeShip(t0Symbol, buyYard),
		PurchasePrice: 50_000,
		ShipType:      "SHIP_PROBE",
		AgentCredits:  900_000,
	}, nil
}

// t0Symbol is the hull the yard delivers.
const t0Symbol = "TORWIND-PROBE-NEW"

// boughtProbeShip builds the delivered hull the purchase response carries. Only
// the symbol is read by the adapter, but the domain constructor validates the
// rest, so the values are plausible ones.
func boughtProbeShip(symbol, waypoint string) *navigation.Ship {
	loc, err := shared.NewWaypoint(waypoint, 0, 0)
	if err != nil {
		panic(err)
	}
	fuel, err := shared.NewFuel(100, 100)
	if err != nil {
		panic(err)
	}
	cargo, err := shared.NewCargo(0, 0, nil)
	if err != nil {
		panic(err)
	}
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(testPlayerID), loc, fuel, 100, 0, cargo,
		30, "FRAME_PROBE", "SATELLITE", nil, navigation.NavStatusDocked)
	if err != nil {
		panic(err)
	}
	return ship
}

// newSensingBuyWorld builds the FK-enforcing fixture: the real ship repository over
// the real schema, the driving coordinator's container row (the FK parent the claim
// must reference), and an idle sensing-tagged probe standing at the yard.
func newSensingBuyWorld(t *testing.T) (*adapterSensing.ProbePurchasePort, *sensingBuyMediator, *gorm.DB) {
	t.Helper()
	db := newShipPortsDB(t)

	// The driving coordinator's row. Without it there is no legal claim owner at
	// all — which is the whole point of the constraint.
	require.NoError(t, db.Create(&persistence.ContainerModel{
		ID:            sensingBuyContainerID,
		PlayerID:      testPlayerID,
		ContainerType: "coordinator",
		CommandType:   "probe_sensing_coordinator",
		Status:        "RUNNING",
	}).Error)

	// The purchasing hull: docked at the yard, tagged to this engine's own fleet
	// (the case ClaimShip's dedication guard must ACCEPT rather than reject).
	require.NoError(t, db.Create(&persistence.ShipModel{
		ShipSymbol:     buyerHull,
		PlayerID:       testPlayerID,
		NavStatus:      string(navigation.NavStatusDocked),
		LocationSymbol: buyYard,
		Role:           "SATELLITE",
		DedicatedFleet: appSensing.SensingParkedFleetTag,
	}).Error)

	med := &sensingBuyMediator{db: db}
	shipRepo := api.NewShipRepository(nil, nil, nil, nil, db, shared.NewRealClock())
	// nil listing store: this fixture is about the claim foreign key, and an
	// unwired persister leaves Quote byte-identical to its pre-memo behaviour.
	return adapterSensing.NewProbePurchasePort(med, shipRepo, nil), med, db
}

// THE REGRESSION PROOF. The buy must claim its purchasing hull to the DRIVING
// COORDINATOR'S CONTAINER ID, which is the only value ships.container_id will
// accept. With the old label in place, ClaimShip's write violates
// fk_ships_container and Buy returns the fail-closed claim error instead of ever
// reaching the counter.
func TestProbeBuy_ClaimsBuyerToTheDrivingContainer(t *testing.T) {
	port, med, db := newSensingBuyWorld(t)

	bought, err := port.Buy(context.Background(), testPlayerID, buyerHull, buyYard, sensingBuyContainerID)
	require.NoError(t, err,
		"the probe buy must survive the ships.container_id foreign key: the claim owner has to be the driving coordinator's real container id, never a descriptive label")
	require.Equal(t, t0Symbol, bought.ShipSymbol)

	// The claim genuinely LANDED against the FK-enforcing store — asserted mid-buy,
	// while it was held, and naming the container that owns it.
	require.True(t, med.sawPurchase, "the purchase was never reached, so the claim failed closed before any money could move")
	require.Equal(t, "active", med.statusMidBuy, "the purchasing hull was not claimed while the buy was in flight")
	require.NotNil(t, med.ownerMidBuy, "the hull carried no container id mid-buy, so no claim was actually written")
	require.Equal(t, sensingBuyContainerID, *med.ownerMidBuy,
		"the claim must be owned by the driving coordinator's container id")

	// And the claim is released on the way out, stamping the audit reason that had
	// never once appeared on a hull in production.
	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", buyerHull).First(&row).Error)
	require.Equal(t, "idle", row.AssignmentStatus, "the buy claim must not outlive the purchase")
	require.Nil(t, row.ContainerID)
	require.Equal(t, "parked_sensing_buy_complete", row.ReleaseReason)
}

// The independent proof that the constraint is real IN THIS HARNESS, and the guard
// against the subtler regression: if foreign-key enforcement were ever switched off
// in the test connection, the test above would keep passing with the label restored
// and quietly stop testing anything. This pins the mechanism itself — a claim owner
// that is not a container row is REFUSED — so the harness cannot silently defang.
func TestProbeBuy_ClaimOwnerThatIsNotAContainerRowIsRefusedByTheForeignKey(t *testing.T) {
	_, _, db := newSensingBuyWorld(t)
	shipRepo := api.NewShipRepository(nil, nil, nil, nil, db, shared.NewRealClock())

	err := shipRepo.ClaimShip(context.Background(), buyerHull, "parked_sensing_buy",
		shared.MustNewPlayerID(testPlayerID), appSensing.SensingParkedFleetTag)
	require.Error(t, err,
		"a descriptive label must not be writable as a claim owner: ships.container_id carries a foreign key to containers(id), and this harness must enforce it")

	// Nothing was written: the hull is still idle and claimable.
	var row persistence.ShipModel
	require.NoError(t, db.Where("ship_symbol = ?", buyerHull).First(&row).Error)
	require.Nil(t, row.ContainerID)
}

// An unnamed owner fails the buy CLOSED, before any claim is attempted and before
// any money moves. There is deliberately no default to fall back to — every
// candidate would be a label, and a label is precisely what the foreign key
// rejects — so refusing is the only safe direction (RULINGS #4). The placement is
// simply retried by the next tick.
func TestProbeBuy_RefusesWhenNoOwningContainerIsNamed(t *testing.T) {
	for _, owner := range []string{"", "   "} {
		t.Run(fmt.Sprintf("owner=%q", owner), func(t *testing.T) {
			port, med, db := newSensingBuyWorld(t)

			_, err := port.Buy(context.Background(), testPlayerID, buyerHull, buyYard, owner)
			require.Error(t, err, "a buy with no owning container id must fail closed")
			require.Contains(t, err.Error(), "no owning container id")

			require.False(t, med.sawPurchase, "the purchase must not be reached: no money may move behind an unclaimable hull")

			var row persistence.ShipModel
			require.NoError(t, db.Where("ship_symbol = ?", buyerHull).First(&row).Error)
			require.Nil(t, row.ContainerID, "no claim may be written for an unnamed owner")
		})
	}
}
