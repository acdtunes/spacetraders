package grpc

// THE DECISION MUST NOT PAY FOR DISCOVERY (sp-739gf).
//
// PriceFor used to issue a LIVE Earning-class GET /shipyard at EVERY SHIPYARD-trait waypoint in
// every system the fleet occupies, and only then ask which of them a buyer stands on. Earning skips
// the trait filter, skips the rescan window and cannot be declined by the shipyard allowance, and
// Get Shipyard is PriorityLow — so each read blocks on a contended rate-limit token behind every
// trade-critical call. Fifty-five yards became fifty-five blocking reads inside one tick, which is
// the ~100-minute decision the Admiral called out.
//
// The target has been "a yard a claimable hull already stands on" since sp-w3mcc. These tests pin
// that the READS are now narrowed to that same set — same answer, a fraction of the calls — and
// that the premium reference the guard judges got WIDER rather than narrower.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainShipyard "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// countingYardAskMediator is fakeYardAskMediator plus the RECORD of every waypoint it was asked
// about — the whole subject here is which yards produce a live API read, not what they answer.
type countingYardAskMediator struct {
	common.Mediator
	askByWaypoint map[string]int
	read          []string
	classes       []marketscan.Class
}

func (m *countingYardAskMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	q, ok := request.(*shipyardQueries.GetShipyardListingsQuery)
	if !ok {
		return nil, errors.New("unexpected request")
	}
	m.read = append(m.read, q.WaypointSymbol)
	m.classes = append(m.classes, q.Class)
	ask, found := m.askByWaypoint[q.WaypointSymbol]
	if !found {
		return nil, errors.New("no priced listing at " + q.WaypointSymbol)
	}
	return &shipyardQueries.GetShipyardListingsResponse{
		Shipyard: domainShipyard.NewShipyard(q.WaypointSymbol, []string{pairingHeavyType},
			[]domainShipyard.ShipListing{{ShipType: pairingHeavyType, PurchasePrice: ask}}, 0),
	}, nil
}

// yardsAt builds the SHIPYARD-trait waypoint rows for one system.
func yardsAt(t *testing.T, symbols ...string) []*shared.Waypoint {
	t.Helper()
	out := make([]*shared.Waypoint, 0, len(symbols))
	for _, s := range symbols {
		wp, err := shared.NewWaypoint(s, 0, 0)
		require.NoError(t, err)
		out = append(out, wp)
	}
	return out
}

// scannedAt is the persisted scan surface a live fleet always has: the yards it has read, priced.
func scannedAt(prices map[string]int) *fakeScannedYards {
	out := &fakeScannedYards{}
	for wp, ask := range prices {
		out.candidates = append(out.candidates, shipyardQueries.YardCandidate{
			SystemSymbol: shared.ExtractSystemSymbol(wp), WaypointSymbol: wp, ShipType: pairingHeavyType, PurchasePrice: ask,
		})
	}
	return out
}

// THE CADENCE TEST. One system, five shipyards, a buyer standing on exactly one of them: the
// decision must read THAT ONE and leave the other four alone. Before sp-739gf all five were read
// live — the shape that turned 55 yards into a 100-minute tick.
func TestYardPriceReader_ReadsLiveOnlyWhereABuyerStands(t *testing.T) {
	med := &countingYardAskMediator{askByWaypoint: map[string]int{
		"X1-HOME-Y1": 1_100_000,
		"X1-HOME-Y2": 1_200_000,
		"X1-HOME-Y3": 1_300_000, // the buyer's counter
		"X1-HOME-Y4": 1_400_000,
		"X1-HOME-Y5": 1_500_000,
	}}
	r := &fleetYardPriceReader{
		med: med,
		shipRepo: &fakeHeavyShipRepo{all: []*navigation.Ship{
			pairingHull(t, "BUYER-1", "X1-HOME-Y3", ""),
		}},
		waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{
			"X1-HOME": yardsAt(t, "X1-HOME-Y1", "X1-HOME-Y2", "X1-HOME-Y3", "X1-HOME-Y4", "X1-HOME-Y5"),
		}},
		// Every one of those yards has been read before and persisted — the live fleet's state.
		scannedYards: scannedAt(map[string]int{
			"X1-HOME-Y1": 1_100_000, "X1-HOME-Y2": 1_200_000, "X1-HOME-Y3": 1_300_000,
			"X1-HOME-Y4": 1_400_000, "X1-HOME-Y5": 1_500_000,
		}),
	}

	price, cheapest, yard, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, pairingHeavyType, true)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, "X1-HOME-Y3", yard, "the target is the yard a claimable hull stands on")
	require.Equal(t, int64(1_300_000), price, "the target's ask is still a LIVE read (RULINGS #4)")
	require.Equal(t, []string{"X1-HOME-Y3"}, med.read,
		"the decision must read ONLY the yard it can transact at — every other live read is discovery charged to the tick")
	require.Equal(t, []marketscan.Class{marketscan.Earning}, med.classes,
		"the surviving read is the money guard's, so it stays Earning: metered, never denied")
	require.Equal(t, int64(1_100_000), cheapest,
		"and the premium reference still names the cheapest yard on the map, off the store rather than off four live reads")
}

// THE TARGET IS UNCHANGED. Narrowing the reads must not narrow the ANSWER: with buyers on two
// yards, the cheaper of the two is still the target, and both are still read live.
func TestYardPriceReader_TargetStillTheCheapestOccupiedYard(t *testing.T) {
	med := &countingYardAskMediator{askByWaypoint: map[string]int{
		"X1-HOME-Y1": 1_900_000,
		"X1-HOME-Y2": 1_400_000, // occupied AND cheaper
		"X1-HOME-Y3": 1_000_000, // cheapest on the map, nobody standing
	}}
	r := &fleetYardPriceReader{
		med: med,
		shipRepo: &fakeHeavyShipRepo{all: []*navigation.Ship{
			pairingHull(t, "BUYER-1", "X1-HOME-Y1", ""),
			pairingHull(t, "BUYER-2", "X1-HOME-Y2", ""),
		}},
		waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{
			"X1-HOME": yardsAt(t, "X1-HOME-Y1", "X1-HOME-Y2", "X1-HOME-Y3"),
		}},
		scannedYards: scannedAt(map[string]int{"X1-HOME-Y1": 1_900_000, "X1-HOME-Y2": 1_400_000, "X1-HOME-Y3": 1_000_000}),
	}

	price, cheapest, yard, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, pairingHeavyType, true)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, "X1-HOME-Y2", yard)
	require.Equal(t, int64(1_400_000), price)
	require.Equal(t, int64(1_000_000), cheapest, "the unoccupied yard is still the cheapest KNOWN ask")
	require.ElementsMatch(t, []string{"X1-HOME-Y1", "X1-HOME-Y2"}, med.read,
		"every occupied yard is still priced live; the unoccupied one is not")
}

// THE PREMIUM REFERENCE REACHES PAST THE WALK. cheapest is the guard's DENOMINATOR — a higher one
// raises the ceiling — and the scanned surface spans yards in systems the live walk never enters, so
// the reference is if anything WIDER than the one 55 live reads produced.
func TestYardPriceReader_CheapestFoldsTheScannedSurface(t *testing.T) {
	med := &countingYardAskMediator{askByWaypoint: map[string]int{"X1-HOME-Y1": 1_600_000}}
	scanned := &fakeScannedYards{candidates: []shipyardQueries.YardCandidate{
		{SystemSymbol: "X1-FAR", WaypointSymbol: "X1-FAR-Y9", ShipType: pairingHeavyType, PurchasePrice: 900_000, Hops: 4},
	}}
	r := &fleetYardPriceReader{
		med: med,
		shipRepo: &fakeHeavyShipRepo{all: []*navigation.Ship{
			pairingHull(t, "BUYER-1", "X1-HOME-Y1", ""),
		}},
		waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{
			"X1-HOME": yardsAt(t, "X1-HOME-Y1"),
		}},
		scannedYards: scanned,
	}

	price, cheapest, yard, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, pairingHeavyType, true)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, "X1-HOME-Y1", yard)
	require.Equal(t, int64(1_600_000), price)
	require.Equal(t, int64(900_000), cheapest,
		"the premium reference must name the cheapest ask KNOWN, including yards the live read never touched")
	require.Equal(t, 1, scanned.calls, "the scan surface is consulted ONCE per decision, off the store")
}

// A LOWER LIVE ASK STILL WINS THE REFERENCE. The fold is a minimum, not a replacement: a stale
// store row that is DEARER than what we just read live must never raise the ceiling.
func TestYardPriceReader_CheapestKeepsTheLowerOfLiveAndScanned(t *testing.T) {
	med := &countingYardAskMediator{askByWaypoint: map[string]int{"X1-HOME-Y1": 800_000}}
	scanned := &fakeScannedYards{candidates: []shipyardQueries.YardCandidate{
		{SystemSymbol: "X1-FAR", WaypointSymbol: "X1-FAR-Y9", ShipType: pairingHeavyType, PurchasePrice: 2_000_000, Hops: 4},
	}}
	r := &fleetYardPriceReader{
		med:          med,
		shipRepo:     &fakeHeavyShipRepo{all: []*navigation.Ship{pairingHull(t, "BUYER-1", "X1-HOME-Y1", "")}},
		waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{"X1-HOME": yardsAt(t, "X1-HOME-Y1")}},
		scannedYards: scanned,
	}

	_, cheapest, _, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, pairingHeavyType, true)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, int64(800_000), cheapest, "the live ask is cheaper, so it stays the reference")
}

// NO BUYER STANDING ANYWHERE ⇒ NO LIVE READ AT ALL, and the class stays fail-closed off the live
// walk (the heavy fallback then has its own separate say). A tick with nothing to transact must
// cost the shipyard allowance nothing.
func TestYardPriceReader_NoBuyerStanding_SpendsNoShipyardRead(t *testing.T) {
	med := &countingYardAskMediator{askByWaypoint: map[string]int{
		"X1-HOME-Y1": 1_100_000,
		"X1-HOME-Y2": 1_200_000,
	}}
	r := &fleetYardPriceReader{
		med:          med,
		shipRepo:     &fakeHeavyShipRepo{all: []*navigation.Ship{tradeShipAt(t, "TR-1", 1, "X1-HOME-A1")}},
		waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{"X1-HOME": yardsAt(t, "X1-HOME-Y1", "X1-HOME-Y2")}},
		scannedYards: scannedAt(map[string]int{"X1-HOME-Y1": 1_100_000, "X1-HOME-Y2": 1_200_000}),
	}

	_, _, _, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, pairingHeavyType, true)
	require.NoError(t, err)
	require.False(t, readable, "no hull can sign here, so there is no price to spend against")
	require.Empty(t, med.read, "a decision with no transactable yard must issue no live shipyard read")
}

// THE ANTI-VACUITY CONTROL, and the whole safety argument for the narrowing: the reads are dropped
// ONLY because the scan surface can supply the premium reference in their place. With NO surface —
// unwired ranker, cold store, unreadable store — the walk must go back to pricing every yard live and
// earn that reference itself, because the alternative is a reference that names only the yards we
// occupy, which RAISES the ceiling and weakens guardPrice (RULINGS #4).
func TestYardPriceReader_NoScanSurface_KeepsTheFullLiveWalk(t *testing.T) {
	fleet := []*navigation.Ship{pairingHull(t, "BUYER-1", "X1-HOME-Y3", "")}
	yards := &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{
		"X1-HOME": yardsAt(t, "X1-HOME-Y1", "X1-HOME-Y2", "X1-HOME-Y3"),
	}}
	asks := map[string]int{"X1-HOME-Y1": 900_000, "X1-HOME-Y2": 1_200_000, "X1-HOME-Y3": 1_300_000}

	for name, surface := range map[string]scannedYardRanker{
		"unwired ranker":   nil,
		"cold store":       &fakeScannedYards{},
		"unreadable store": &fakeScannedYards{err: errors.New("store down")},
	} {
		t.Run(name, func(t *testing.T) {
			med := &countingYardAskMediator{askByWaypoint: asks}
			r := &fleetYardPriceReader{med: med, shipRepo: &fakeHeavyShipRepo{all: fleet}, waypointRepo: yards, scannedYards: surface}

			price, cheapest, yard, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, pairingHeavyType, true)
			require.NoError(t, err)
			require.True(t, readable)
			require.Equal(t, "X1-HOME-Y3", yard)
			require.Equal(t, int64(1_300_000), price)
			require.Equal(t, int64(900_000), cheapest,
				"with nothing to replace it, the reference is still EARNED live — the ceiling never rises")
			require.Len(t, med.read, 3, "no surface ⇒ the full walk, exactly as before sp-739gf")
		})
	}
}

// THE NON-HEAVY CLASSES ARE UNTOUCHED. The scanned surface is a heavy-class signal (a light buys
// in-system), so it can never license a narrowing for them: the contract-delivery walk is byte-for-
// byte what it always was.
func TestYardPriceReader_NonHeavyClass_KeepsTheFullLiveWalk(t *testing.T) {
	med := &countingYardAskMediator{askByWaypoint: map[string]int{
		"X1-HOME-Y1": 300_000, "X1-HOME-Y2": 400_000,
	}}
	scanned := scannedAt(map[string]int{"X1-HOME-Y1": 300_000, "X1-HOME-Y2": 400_000})
	r := &fleetYardPriceReader{
		med:          med,
		shipRepo:     &fakeHeavyShipRepo{all: []*navigation.Ship{pairingHull(t, "BUYER-1", "X1-HOME-Y2", "")}},
		waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{"X1-HOME": yardsAt(t, "X1-HOME-Y1", "X1-HOME-Y2")}},
		scannedYards: scanned,
	}

	_, cheapest, yard, readable, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassContractDelivery, pairingHeavyType, true)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, "X1-HOME-Y2", yard)
	require.Equal(t, int64(300_000), cheapest, "the live walk still earns the reference for a non-heavy class")
	require.Len(t, med.read, 2, "both yards read live — the heavy-only surface may not narrow this walk")
	require.Zero(t, scanned.calls, "and the scanned store is never consulted for a non-heavy class")
}
