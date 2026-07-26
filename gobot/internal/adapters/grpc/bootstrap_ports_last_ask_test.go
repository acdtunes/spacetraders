package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainShipyard "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// A yard prices its hulls only while a ship is standing at it, so the price-check reads UNREADABLE for
// most of cold start and reports the last ask on record instead. That reading is the sole evidence the
// first-hauler pivot has when it decides whether to free the command frigate — the only earner — from its
// contract loop to go buy, and a 0 tells the pivot no yard has ever priced the hull, so there is nothing
// to weigh and the free proceeds. The reading must therefore mean the same thing in a process that has
// been running for hours and in one started thirty seconds ago (RULINGS #2): a 0 that merely means "this
// process has not read a yard yet" would free the sole earner against evidence that says it cannot afford
// the hull, and a frigate dedicated to purchasing with nothing to buy stops earning.

// --- fakes ------------------------------------------------------------------------------------

type fakeAskShipRepo struct {
	navigation.ShipRepository
	all []*navigation.Ship
	err error
}

func (r *fakeAskShipRepo) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	return r.all, r.err
}

type fakeYardWaypoints struct{ yards []*shared.Waypoint }

func (w *fakeYardWaypoints) ListBySystemWithTrait(ctx context.Context, systemSymbol, trait string) ([]*shared.Waypoint, error) {
	return w.yards, nil
}

// fakeYardMediator answers the shipyard-listings query. listings==nil is a COLD yard — the live read
// fails exactly as it does when no hull is standing there.
type fakeYardMediator struct {
	common.Mediator
	listings []domainShipyard.ShipListing
}

func (m *fakeYardMediator) Send(ctx context.Context, request common.Request) (common.Response, error) {
	if m.listings == nil {
		return nil, errors.New("shipyard unreadable: no hull at the yard")
	}
	return &shipyardQueries.GetShipyardListingsResponse{
		Shipyard: domainShipyard.Shipyard{Symbol: "X1-HQ-YARD", Listings: m.listings},
	}, nil
}

type fakeSavedYards struct {
	rows          []domainShipyard.ShipTypeAvailability
	err           error
	askedForTypes []string
}

func (s *fakeSavedYards) ListSavedYards(ctx context.Context, playerID int, shipTypes []string) ([]domainShipyard.ShipTypeAvailability, error) {
	s.askedForTypes = shipTypes
	return s.rows, s.err
}

// coldYardAcquirer builds an acquirer whose home system holds one shipyard that prices nothing, with an
// EMPTY in-memory cache — a daemon that has just restarted into a cold yard.
func coldYardAcquirer(t *testing.T, saved savedYardReader) *bootstrapAcquirer {
	t.Helper()
	yard, err := shared.NewWaypoint("X1-HQ-YARD", 0, 0)
	require.NoError(t, err)
	return &bootstrapAcquirer{
		med:          &fakeYardMediator{},
		shipRepo:     &fakeAskShipRepo{all: []*navigation.Ship{shipyardHull(t, "FRIGATE-1", "X1-HQ-A1", "", commandRole, navigation.NavStatusDocked)}},
		waypointRepo: &fakeYardWaypoints{yards: []*shared.Waypoint{yard}},
		savedYards:   saved,
	}
}

// --- the durable ask --------------------------------------------------------------------------

// A cold yard is answered from the persisted inventory, so what the yard charged survives the process
// that read it. Absence of a record still reads as 0 — a first-ever cold start has genuinely never priced
// the hull, and the pivot must be free to send the frigate to find out.
func TestPriceCheck_ColdYard_ReportsTheAskOnRecordAcrossARestart(t *testing.T) {
	cases := []struct {
		name  string
		saved *fakeSavedYards
		want  int64
	}{
		{
			name:  "a yard priced the hull before the restart: that ask is reported",
			saved: &fakeSavedYards{rows: []domainShipyard.ShipTypeAvailability{{WaypointSymbol: "X1-HQ-YARD", ShipType: "SHIP_LIGHT_HAULER", PurchasePrice: 320_835}}},
			want:  320_835,
		},
		{
			name: "several yards on record: the cheapest, matching the live cheapest-yard read",
			saved: &fakeSavedYards{rows: []domainShipyard.ShipTypeAvailability{
				{WaypointSymbol: "X1-HQ-YARD", ShipType: "SHIP_LIGHT_HAULER", PurchasePrice: 320_835},
				{WaypointSymbol: "X1-FAR-YARD", ShipType: "SHIP_LIGHT_HAULER", PurchasePrice: 401_000},
			}},
			want: 320_835,
		},
		{
			name: "a listed-but-unpriced row is availability, not an ask: skipped for the real price",
			saved: &fakeSavedYards{rows: []domainShipyard.ShipTypeAvailability{
				{WaypointSymbol: "X1-HQ-YARD", ShipType: "SHIP_LIGHT_HAULER", PurchasePrice: 0},
				{WaypointSymbol: "X1-FAR-YARD", ShipType: "SHIP_LIGHT_HAULER", PurchasePrice: 401_000},
			}},
			want: 401_000,
		},
		{
			name:  "no yard has ever priced the hull: no evidence",
			saved: &fakeSavedYards{},
			want:  0,
		},
		{
			name:  "the store is unreadable: no evidence, and the tick says so",
			saved: &fakeSavedYards{err: errors.New("db down")},
			want:  0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acq := coldYardAcquirer(t, tc.saved)

			price, yard, readable, err := acq.PriceCheck(context.Background(), 1, "SHIP_LIGHT_HAULER")

			require.NoError(t, err)
			require.False(t, readable, "a cold yard is never readable — the buy paths must stay failed closed")
			require.Empty(t, yard, "an unreadable read names no yard to buy at")
			require.Equal(t, tc.want, price)
			require.Equal(t, []string{"SHIP_LIGHT_HAULER"}, tc.saved.askedForTypes, "the record is read for the hull being priced, not for whatever is cheapest on the lot")
		})
	}
}

// A live read is the freshest evidence there is and outranks the record: a yard that has just raised its
// price must not be weighed at the cheaper price it charged an hour ago.
func TestPriceCheck_LiveRead_OutranksTheAskOnRecord(t *testing.T) {
	stale := &fakeSavedYards{rows: []domainShipyard.ShipTypeAvailability{{WaypointSymbol: "X1-HQ-YARD", ShipType: "SHIP_LIGHT_HAULER", PurchasePrice: 320_835}}}
	acq := coldYardAcquirer(t, stale)
	acq.med = &fakeYardMediator{listings: []domainShipyard.ShipListing{{ShipType: "SHIP_LIGHT_HAULER", PurchasePrice: 401_000}}}

	price, yard, readable, err := acq.PriceCheck(context.Background(), 1, "SHIP_LIGHT_HAULER")
	require.NoError(t, err)
	require.True(t, readable)
	require.Equal(t, "X1-HQ-YARD", yard)
	require.Equal(t, int64(401_000), price)

	// The yard goes cold again (the hull that warmed it leaves): the just-read price is what is reported,
	// not the older one on record.
	acq.med = &fakeYardMediator{}
	price, _, readable, err = acq.PriceCheck(context.Background(), 1, "SHIP_LIGHT_HAULER")
	require.NoError(t, err)
	require.False(t, readable)
	require.Equal(t, int64(401_000), price)
}
