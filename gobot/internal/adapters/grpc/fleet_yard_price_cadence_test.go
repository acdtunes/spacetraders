package grpc

// THE DECISION MUST NOT PAY FOR DISCOVERY. A hull-buy decision issues Earning-class GET /shipyard
// reads — no trait filter, no rescan window, never declined, blocking on a contended PriorityLow
// token behind every trade call — so which yards it reads is a cost the whole fleet pays.
//
// These tests pin the read set to the yards a hull of ours occupies, which is where a shipyard will
// quote a price at all, and pin that the premium reference the guard judges is unmoved by that.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/hullbuy"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// countingYardAskMediator is fakeYardAskMediator plus the RECORD of every waypoint it was asked
// about — the whole subject here is which yards produce a live API read, not what they answer. It
// answers through the embedded fake so the presence rule is enforced in one place.
type countingYardAskMediator struct {
	fakeYardAskMediator
	read    []string
	classes []marketscan.Class
}

func countingAsks(fleet []*navigation.Ship, asks map[string]int) *countingYardAskMediator {
	return &countingYardAskMediator{fakeYardAskMediator: yardAsks(fleet, asks)}
}

func countingAsksByType(fleet []*navigation.Ship, asks map[string]map[string]int) *countingYardAskMediator {
	return &countingYardAskMediator{fakeYardAskMediator: yardAsksByType(fleet, asks)}
}

func (m *countingYardAskMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	if q, ok := request.(*shipyardQueries.GetShipyardListingsQuery); ok {
		m.read = append(m.read, q.WaypointSymbol)
		m.classes = append(m.classes, q.Class)
	}
	return m.fakeYardAskMediator.Send(ctx, request)
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
	fleet := []*navigation.Ship{pairingHull(t, "BUYER-1", "X1-HOME-Y3", "")}
	med := countingAsks(fleet, map[string]int{
		"X1-HOME-Y1": 1_100_000,
		"X1-HOME-Y2": 1_200_000,
		"X1-HOME-Y3": 1_300_000, // the buyer's counter
		"X1-HOME-Y4": 1_400_000,
		"X1-HOME-Y5": 1_500_000,
	})
	r := &fleetYardPriceReader{
		med:      med,
		shipRepo: &fakeHeavyShipRepo{all: fleet},
		waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{
			"X1-HOME": yardsAt(t, "X1-HOME-Y1", "X1-HOME-Y2", "X1-HOME-Y3", "X1-HOME-Y4", "X1-HOME-Y5"),
		}},
		// Every one of those yards has been read before and persisted — the live fleet's state.
		scannedYards: scannedAt(map[string]int{
			"X1-HOME-Y1": 1_100_000, "X1-HOME-Y2": 1_200_000, "X1-HOME-Y3": 1_300_000,
			"X1-HOME-Y4": 1_400_000, "X1-HOME-Y5": 1_500_000,
		}),
	}

	price, cheapest, yard, readable, err := priceOne(context.Background(), r, 1, hullbuy.HullClassHeavy, pairingHeavyType, true)
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
	fleet := []*navigation.Ship{
		pairingHull(t, "BUYER-1", "X1-HOME-Y1", ""),
		pairingHull(t, "BUYER-2", "X1-HOME-Y2", ""),
	}
	med := countingAsks(fleet, map[string]int{
		"X1-HOME-Y1": 1_900_000,
		"X1-HOME-Y2": 1_400_000, // occupied AND cheaper
		"X1-HOME-Y3": 1_000_000, // cheapest on the map, nobody standing
	})
	r := &fleetYardPriceReader{
		med:      med,
		shipRepo: &fakeHeavyShipRepo{all: fleet},
		waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{
			"X1-HOME": yardsAt(t, "X1-HOME-Y1", "X1-HOME-Y2", "X1-HOME-Y3"),
		}},
		scannedYards: scannedAt(map[string]int{"X1-HOME-Y1": 1_900_000, "X1-HOME-Y2": 1_400_000, "X1-HOME-Y3": 1_000_000}),
	}

	price, cheapest, yard, readable, err := priceOne(context.Background(), r, 1, hullbuy.HullClassHeavy, pairingHeavyType, true)
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
	fleet := []*navigation.Ship{pairingHull(t, "BUYER-1", "X1-HOME-Y1", "")}
	med := countingAsks(fleet, map[string]int{"X1-HOME-Y1": 1_600_000})
	scanned := &fakeScannedYards{candidates: []shipyardQueries.YardCandidate{
		{SystemSymbol: "X1-FAR", WaypointSymbol: "X1-FAR-Y9", ShipType: pairingHeavyType, PurchasePrice: 900_000, Hops: 4},
	}}
	r := &fleetYardPriceReader{
		med:      med,
		shipRepo: &fakeHeavyShipRepo{all: fleet},
		waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{
			"X1-HOME": yardsAt(t, "X1-HOME-Y1"),
		}},
		scannedYards: scanned,
	}

	price, cheapest, yard, readable, err := priceOne(context.Background(), r, 1, hullbuy.HullClassHeavy, pairingHeavyType, true)
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
	fleet := []*navigation.Ship{pairingHull(t, "BUYER-1", "X1-HOME-Y1", "")}
	med := countingAsks(fleet, map[string]int{"X1-HOME-Y1": 800_000})
	scanned := &fakeScannedYards{candidates: []shipyardQueries.YardCandidate{
		{SystemSymbol: "X1-FAR", WaypointSymbol: "X1-FAR-Y9", ShipType: pairingHeavyType, PurchasePrice: 2_000_000, Hops: 4},
	}}
	r := &fleetYardPriceReader{
		med:          med,
		shipRepo:     &fakeHeavyShipRepo{all: fleet},
		waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{"X1-HOME": yardsAt(t, "X1-HOME-Y1")}},
		scannedYards: scanned,
	}

	_, cheapest, _, readable, err := priceOne(context.Background(), r, 1, hullbuy.HullClassHeavy, pairingHeavyType, true)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, int64(800_000), cheapest, "the live ask is cheaper, so it stays the reference")
}

// NO BUYER STANDING ANYWHERE ⇒ NO LIVE READ AT ALL, and the class stays fail-closed off the live
// walk (the heavy fallback then has its own separate say). A tick with nothing to transact must
// cost the shipyard allowance nothing.
func TestYardPriceReader_NoBuyerStanding_SpendsNoShipyardRead(t *testing.T) {
	fleet := []*navigation.Ship{tradeShipAt(t, "TR-1", 1, "X1-HOME-A1")}
	med := countingAsks(fleet, map[string]int{
		"X1-HOME-Y1": 1_100_000,
		"X1-HOME-Y2": 1_200_000,
	})
	r := &fleetYardPriceReader{
		med:          med,
		shipRepo:     &fakeHeavyShipRepo{all: fleet},
		waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{"X1-HOME": yardsAt(t, "X1-HOME-Y1", "X1-HOME-Y2")}},
		scannedYards: scannedAt(map[string]int{"X1-HOME-Y1": 1_100_000, "X1-HOME-Y2": 1_200_000}),
	}

	_, _, _, readable, err := priceOne(context.Background(), r, 1, hullbuy.HullClassHeavy, pairingHeavyType, true)
	require.NoError(t, err)
	require.False(t, readable, "no hull can sign here, so there is no price to spend against")
	require.Empty(t, med.read, "a decision with no transactable yard must issue no live shipyard read")
}

// THE ANTI-VACUITY CONTROL, and the gate's RULINGS #4 witness. With NO surface — unwired ranker,
// cold store, unreadable store — the walk earns the premium reference entirely on its own, so this
// is the case where a dropped read could genuinely raise the ceiling. The reference is the same
// number the full three-yard walk produced; only the request count moved.
func TestYardPriceReader_NoScanSurface_ReferenceSurvivesTheGate(t *testing.T) {
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
			med := countingAsks(fleet, asks)
			r := &fleetYardPriceReader{med: med, shipRepo: &fakeHeavyShipRepo{all: fleet}, waypointRepo: yards, scannedYards: surface}

			price, cheapest, yard, readable, err := priceOne(context.Background(), r, 1, hullbuy.HullClassHeavy, pairingHeavyType, true)
			require.NoError(t, err)
			require.True(t, readable)
			require.Equal(t, "X1-HOME-Y3", yard)
			require.Equal(t, int64(1_300_000), price)
			require.Equal(t, int64(1_300_000), cheapest,
				"only the occupied counter can be priced at all, so it alone earns the reference")
			require.Equal(t, []string{"X1-HOME-Y3"}, med.read,
				"the two unoccupied counters had no ask to give, so dropping their reads costs the reference nothing")
		})
	}
}

// THE READ COUNT IS A FUNCTION OF PRESENCE, NOT OF THE MAP. A counter no hull of ours stands on
// answers a live read with its catalogue and no ask, so the SHIPYARD-trait universe may grow without
// limit and the tick's cost must not follow it.
//
// The unoccupied yards carry the LOWEST asks in the fixture, so a gate that wrongly dropped a
// contributing read would move cheapest and this would fail on the money guard rather than on cost.
func TestYardPriceReader_LiveReadsScaleWithPresenceNotYardCount(t *testing.T) {
	const dockedAsk, inboundAsk, unoccupiedAsk = 1_500_000, 1_200_000, 500_000

	for _, yardCount := range []int{4, 16, 128, 512} {
		t.Run(fmt.Sprintf("%d yards charted", yardCount), func(t *testing.T) {
			symbols := make([]string, 0, yardCount)
			asks := make(map[string]int, yardCount)
			for i := 0; i < yardCount; i++ {
				symbol := fmt.Sprintf("X1-HOME-Y%03d", i)
				symbols = append(symbols, symbol)
				asks[symbol] = unoccupiedAsk
			}
			docked, inbound := symbols[0], symbols[1]
			asks[docked], asks[inbound] = dockedAsk, inboundAsk

			fleet := []*navigation.Ship{
				pairingHull(t, "BUYER-1", docked, ""),
				pairingHullWithStatus(t, "INBOUND-1", inbound, "", navigation.NavStatusInTransit),
			}
			med := countingAsks(fleet, asks)
			r := &fleetYardPriceReader{
				med:          med,
				shipRepo:     &fakeHeavyShipRepo{all: fleet},
				waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{"X1-HOME": yardsAt(t, symbols...)}},
			}

			price, cheapest, yard, readable, err := priceOne(context.Background(), r, 1, hullbuy.HullClassHeavy, pairingHeavyType, true)
			require.NoError(t, err)
			require.True(t, readable)
			require.Equal(t, docked, yard, "an inbound hull cannot sign, so the docked counter is the target")
			require.Equal(t, int64(dockedAsk), price)
			require.ElementsMatch(t, []string{docked, inbound}, med.read,
				"the live reads must be the presence set and nothing else, however many yards are charted")
			require.Equal(t, int64(inboundAsk), cheapest,
				"an inbound hull's counter is priceable on arrival, so its ask still feeds the premium reference")
		})
	}
}

// A COUNTER IS READ ONCE PER DECISION, NOT ONCE PER CANDIDATE HULL. The response carries the whole
// listing array, so pricing three types off one walk is free where three walks pay for the same rows
// three times — and each type must still come away with its OWN ask and its own target.
func TestYardPriceReader_PricesEveryCandidateTypeFromOneReadPerYard(t *testing.T) {
	candidates := []string{"SHIP_HEAVY_FREIGHTER", "SHIP_BULK_FREIGHTER", "SHIP_LIGHT_HAULER"}
	fleet := []*navigation.Ship{
		pairingHull(t, "BUYER-1", "X1-HOME-Y1", ""),
		pairingHull(t, "BUYER-2", "X1-HOME-Y2", ""),
	}
	med := countingAsksByType(fleet, map[string]map[string]int{
		"X1-HOME-Y1": {"SHIP_HEAVY_FREIGHTER": 1_900_000, "SHIP_LIGHT_HAULER": 400_000},
		"X1-HOME-Y2": {"SHIP_BULK_FREIGHTER": 2_400_000, "SHIP_LIGHT_HAULER": 370_000},
	})
	r := &fleetYardPriceReader{
		med:          med,
		shipRepo:     &fakeHeavyShipRepo{all: fleet},
		waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{"X1-HOME": yardsAt(t, "X1-HOME-Y1", "X1-HOME-Y2")}},
	}

	asks, err := r.PriceFor(context.Background(), 1, hullbuy.HullClassHeavy, candidates, true)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"X1-HOME-Y1", "X1-HOME-Y2"}, med.read,
		"each occupied counter answers for every candidate at once, so it is read exactly once")
	require.Equal(t, hullbuy.YardAsk{Price: 1_900_000, Cheapest: 1_900_000, Yard: "X1-HOME-Y1", Readable: true}, asks["SHIP_HEAVY_FREIGHTER"])
	require.Equal(t, hullbuy.YardAsk{Price: 2_400_000, Cheapest: 2_400_000, Yard: "X1-HOME-Y2", Readable: true}, asks["SHIP_BULK_FREIGHTER"])
	require.Equal(t, hullbuy.YardAsk{Price: 370_000, Cheapest: 370_000, Yard: "X1-HOME-Y2", Readable: true}, asks["SHIP_LIGHT_HAULER"],
		"the shared walk must not let one type's ask stand in for another's")
}

// THE NON-HEAVY CLASSES EARN THEIR OWN REFERENCE. The scanned surface is a heavy-class signal (a
// light buys in-system) and is never consulted here, so a non-heavy reference comes off the live
// walk alone — and the presence rule that gates that walk is the API's, not the class's.
func TestYardPriceReader_NonHeavyClass_EarnsItsReferenceLive(t *testing.T) {
	fleet := []*navigation.Ship{pairingHull(t, "BUYER-1", "X1-HOME-Y2", "")}
	med := countingAsks(fleet, map[string]int{
		"X1-HOME-Y1": 300_000, "X1-HOME-Y2": 400_000,
	})
	scanned := scannedAt(map[string]int{"X1-HOME-Y1": 300_000, "X1-HOME-Y2": 400_000})
	r := &fleetYardPriceReader{
		med:          med,
		shipRepo:     &fakeHeavyShipRepo{all: fleet},
		waypointRepo: &fakeSystemYardLister{bySystem: map[string][]*shared.Waypoint{"X1-HOME": yardsAt(t, "X1-HOME-Y1", "X1-HOME-Y2")}},
		scannedYards: scanned,
	}

	_, cheapest, yard, readable, err := priceOne(context.Background(), r, 1, hullbuy.HullClassContractDelivery, pairingHeavyType, true)
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, "X1-HOME-Y2", yard)
	require.Equal(t, int64(400_000), cheapest, "the live walk still earns the reference for a non-heavy class")
	require.Equal(t, []string{"X1-HOME-Y2"}, med.read, "the unoccupied counter could not have been priced for any class")
	require.Zero(t, scanned.calls, "and the scanned store is never consulted for a non-heavy class")
}
