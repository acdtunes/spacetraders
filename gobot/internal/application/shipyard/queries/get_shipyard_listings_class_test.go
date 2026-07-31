package queries

// Tests for the CLASS SEAM (sp-lr27k): that this query can be asked for a
// DENIABLE read, that forgetting to say so is the safe direction, and that a
// deniable read no longer inflates the demand signal the budget allocates
// attention with.
//
// The sibling file get_shipyard_listings_budget_test.go covers the other half —
// that an Earning-stamped read is still undeniable (RULINGS #4). Both halves are
// needed: a change that made every read deniable would pass the tests here and
// fail those, and a change that made every read undeniable does the reverse.
// That is the pair, and it is what the class exists to sit between.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/ship"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	domainShipyard "github.com/andrescamacho/spacetraders-go/internal/domain/shipyard"
)

// notingScanner records what the handler told the budget about demand, so the
// NoteTarget gate can be observed rather than inferred. It wraps the REAL
// scanner so every other behaviour under test is the production one.
type notingScanner struct {
	inner   *ship.ShipyardScanner
	targets []string
}

func (n *notingScanner) ReadShipyard(ctx context.Context, playerID uint, waypointSymbol string, class marketscan.Class) (*domainPorts.ShipyardData, error) {
	return n.inner.ReadShipyard(ctx, playerID, waypointSymbol, class)
}

func (n *notingScanner) NoteTarget(waypoint string) {
	n.targets = append(n.targets, waypoint)
	n.inner.NoteTarget(waypoint)
}

// THE SEAM EXISTS: A CALLER CAN NOW ASK FOR A DENIABLE READ.
//
// This is the defect stated as a test. Before the Class field, this query
// carried only SystemSymbol/WaypointSymbol/PlayerID and hardcoded the read at
// Earning, so there was NO WAY for a discovery caller to be paced — every
// consumer skipped the trait filter, the rescan floor and the allowance
// together, which is how shipyard reads reached 3.2x their configured budget.
//
// The control is the same fixture the Earning tests use, and it is load-bearing:
// those prove this exact scanner DOES reach the API for an Earning read, so a
// failure here cannot be a broken fixture.
//
// THIS IS THE TEST THE CLASS-DEFAULT MUTATION MUST BREAK.
func TestGetShipyardListings_ADiscretionaryReadIsDeclinedByTheBudget(t *testing.T) {
	scanner, api := contendedScanner(t, true)

	h := NewGetShipyardListingsHandler(scanner, nil)
	_, err := h.Handle(guardCtx(), &GetShipyardListingsQuery{
		SystemSymbol:   "X1-GUARD",
		WaypointSymbol: guardYard,
		PlayerID:       shared.MustNewPlayerID(1),
		Class:          marketscan.Discretionary,
	})

	require.Error(t, err,
		"a declined read must surface as a REFUSAL TO PRICE, never as a remembered price")
	require.Equal(t, 0, api.gets,
		"a discretionary read on a drained budget at a just-scanned yard must not reach the API")
}

// FORGETTING TO CLASSIFY IS THE SAFE DIRECTION.
//
// The zero value of marketscan.Class is Discretionary by deliberate design
// (domain/marketscan/budget.go), and the whole fail-safe direction of this fix
// rests on it: a call site added tomorrow, or one nobody remembered to stamp, is
// PACED rather than unmetered. Written as its own case with no Class field at
// all, because a keyed struct literal takes the zero value silently — which is
// exactly how an unclassified caller will look in real code.
//
// THIS IS THE TEST THE CLASS-DEFAULT MUTATION MUST BREAK.
func TestGetShipyardListings_AnUnstampedQueryIsDeniableNotUndeniable(t *testing.T) {
	scanner, api := contendedScanner(t, true)

	h := NewGetShipyardListingsHandler(scanner, nil)
	_, err := h.Handle(guardCtx(), &GetShipyardListingsQuery{
		SystemSymbol:   "X1-GUARD",
		WaypointSymbol: guardYard,
		PlayerID:       shared.MustNewPlayerID(1),
		// Class deliberately omitted — this is the unclassified call site.
	})

	require.Error(t, err, "an unstamped read must be budgeted, not exempt")
	require.Equal(t, 0, api.gets,
		"the zero value must mean PACED; if it ever means Earning, every unclassified caller silently bypasses the allowance")
}

// A DISCRETIONARY READ MUST NOT SAY "THE FLEET IS BUYING HERE".
//
// NoteTarget is the strongest demand signal the budget has, and it used to fire
// on EVERY consumer of this query. That made the loop compounding rather than
// merely additive: a discovery read raised the yard's demand weight, which
// shortened its interval, which kept it hot in the rotation — sensing inflating
// the very signal the budget uses to allocate attention.
//
// THIS IS THE TEST THE NoteTarget-GATE MUTATION MUST BREAK.
func TestGetShipyardListings_OnlyAGuardReadRaisesTheYardsDemandWeight(t *testing.T) {
	scanner, _ := contendedScanner(t, true)
	noting := &notingScanner{inner: scanner}
	h := NewGetShipyardListingsHandler(noting, nil)

	// Discovery read: paced, and must leave the demand signal alone.
	_, _ = h.Handle(guardCtx(), &GetShipyardListingsQuery{
		SystemSymbol:   "X1-GUARD",
		WaypointSymbol: guardYard,
		PlayerID:       shared.MustNewPlayerID(1),
		Class:          marketscan.Discretionary,
	})
	require.Empty(t, noting.targets,
		"a discovery read must not declare buy intent — that is the feedback loop that kept sensed yards hot")

	// The control, and it is what makes the assertion above mean something: the
	// signal is NOT simply dead. A real guard read still marks the counter.
	_, err := h.Handle(guardCtx(), &GetShipyardListingsQuery{
		SystemSymbol:   "X1-GUARD",
		WaypointSymbol: guardYard,
		PlayerID:       shared.MustNewPlayerID(1),
		Class:          marketscan.Earning,
	})
	require.NoError(t, err)
	require.Equal(t, []string{guardYard}, noting.targets,
		"a money guard pricing a hull here IS buy intent and must still keep the yard warm")
}

// The rescan-window floor applies to a deniable read and not to a guard read.
// This is the OTHER filter Earning was skipping, and it is the one that matters
// most for volume: a loop re-reading the same yard every tick is exactly what an
// undeniable class turns into unbounded traffic.
func TestGetShipyardListings_TheRescanFloorAppliesOnlyToDeniableReads(t *testing.T) {
	api := &countingYardAPI{}
	heavy := domainShipyard.NewHeavyShipTypeSet([]string{guardHullType})
	// A GENEROUS rescan window and an UNDRAINED budget, so the recency floor is
	// the only thing that can decline: this isolates the TTL from the allowance.
	scanner := ship.NewShipyardScanner(api, freshInventory{}, yardTraits{isYard: true}, nil, heavy, time.Hour)
	scanner.SetScanBudget(ship.NewYardScanBudget(0.12, 8, heavy))

	h := NewGetShipyardListingsHandler(scanner, nil)

	_, err := h.Handle(guardCtx(), &GetShipyardListingsQuery{
		SystemSymbol:   "X1-GUARD",
		WaypointSymbol: guardYard,
		PlayerID:       shared.MustNewPlayerID(1),
		Class:          marketscan.Discretionary,
	})
	require.Error(t, err, "a yard read inside the rescan window must be declined for a deniable read")
	require.Equal(t, 0, api.gets, "the recency floor must cost no API request")

	_, err = h.Handle(guardCtx(), &GetShipyardListingsQuery{
		SystemSymbol:   "X1-GUARD",
		WaypointSymbol: guardYard,
		PlayerID:       shared.MustNewPlayerID(1),
		Class:          marketscan.Earning,
	})
	require.NoError(t, err, "a money guard is never held back by the recency window")
	require.Equal(t, 1, api.gets)
}
