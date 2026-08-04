package grpc

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	shipyardCmd "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing/gate"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The GATE phase buys 4 hulls in D/F/F/D order, and the role is derived from the LIVE
// fleet, not from a cursor: a restart mid-ramp must resume the order correctly.
func TestNextGateRole_DerivesTheOrderFromTheLiveFleet(t *testing.T) {
	cases := []struct {
		name  string
		fleet []*navigation.Ship
		want  gate.Role
	}{
		{"no gate hulls yet", nil, gate.RoleDelivery},
		{"one delivery", []*navigation.Ship{
			reclaimHull(t, "GATE-7", 40, gate.DeliveryFleetTag, navigation.NavStatusInOrbit),
		}, gate.RoleFactory},
		{"one of each", []*navigation.Ship{
			reclaimHull(t, "GATE-7", 40, gate.DeliveryFleetTag, navigation.NavStatusInOrbit),
			reclaimHull(t, "GATE-8", 40, gate.FactoryFleetTag, navigation.NavStatusInOrbit),
		}, gate.RoleFactory},
		{"one delivery two factory", []*navigation.Ship{
			reclaimHull(t, "GATE-7", 40, gate.DeliveryFleetTag, navigation.NavStatusInOrbit),
			reclaimHull(t, "GATE-8", 40, gate.FactoryFleetTag, navigation.NavStatusInOrbit),
			reclaimHull(t, "GATE-9", 40, gate.FactoryFleetTag, navigation.NavStatusInOrbit),
		}, gate.RoleDelivery},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeReclaimShipRepo{all: tc.fleet}
			got, err := nextGateRole(context.Background(), repo, shared.MustNewPlayerID(1))
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

// A LEGACY manufacturing hull carries no role, so it must not shift the D/F/F/D order:
// counting it as either role would skew every subsequent purchase.
func TestNextGateRole_LegacyHullsDoNotShiftTheOrder(t *testing.T) {
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{
		reclaimHull(t, "MFG-7", 40, gate.LegacyFleetTag, navigation.NavStatusInOrbit),
		reclaimHull(t, "MFG-8", 40, gate.LegacyFleetTag, navigation.NavStatusInOrbit),
	}}
	got, err := nextGateRole(context.Background(), repo, shared.MustNewPlayerID(1))
	require.NoError(t, err)
	require.Equal(t, gate.RoleDelivery, got, "the first ROLE-tagged hull is delivery regardless of legacy hulls")
}

// FAIL CLOSED on an unreadable fleet: a role derived from an unknown count would be a
// guess, and a mis-roled hull is a hull working the wrong half of the operation.
func TestNextGateRole_FailsClosedOnAFleetReadError(t *testing.T) {
	repo := &fakeReclaimShipRepo{findErr: context.DeadlineExceeded}
	_, err := nextGateRole(context.Background(), repo, shared.MustNewPlayerID(1))
	require.Error(t, err)
}

// THE OBSERVER. All three gate tags increment GateWorkers. A role-tagged hull counted as
// nothing would under-report the workforce and let the staged ramp buy past its target.
func TestObserveFleetShape_CountsEveryGateTagAsAGateWorker(t *testing.T) {
	ships := []*navigation.Ship{
		reclaimHull(t, "MFG-7", 40, gate.LegacyFleetTag, navigation.NavStatusInOrbit),
		reclaimHull(t, "GATE-8", 40, gate.DeliveryFleetTag, navigation.NavStatusInOrbit),
		reclaimHull(t, "GATE-9", 40, gate.FactoryFleetTag, navigation.NavStatusInOrbit),
		reclaimHull(t, "CONTRACT-4", 40, contractFleetTag, navigation.NavStatusInOrbit),
	}
	var obs bootstrapCmd.Observation
	observeFleetShape(ships, &obs)

	require.Equal(t, 3, obs.GateWorkers, "all three gate tags must count; an undercount lets the ramp over-buy")
	require.Len(t, obs.GateWorkerHulls, 3, "GateWorkerHulls must stay in lock-step with GateWorkers")
	require.Len(t, obs.Haulers, 1, "a contract hauler must not be absorbed into the gate count")
}

// THE RE-TAG GUARDS. Surplus release and the EXPANSION trade redirect must be able to
// touch a ROLE-tagged hull. Guarding on the legacy tag alone would strand every role-tagged
// hull dedicated forever, with no path back to the idle pool or the trade fleet.
func TestRetagGateWorkers_ReleasesRoleTaggedHullsNotOnlyLegacyOnes(t *testing.T) {
	delivery := reclaimHull(t, "GATE-7", 40, gate.DeliveryFleetTag, navigation.NavStatusInOrbit)
	factory := reclaimHull(t, "GATE-8", 40, gate.FactoryFleetTag, navigation.NavStatusInOrbit)
	legacy := reclaimHull(t, "MFG-9", 40, gate.LegacyFleetTag, navigation.NavStatusInOrbit)
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{delivery, factory, legacy}}
	r := &bootstrapGateSurplusReleaser{shipRepo: repo}

	released, err := r.ReleaseSurplusGateWorkers(context.Background(), 1, []string{"GATE-7", "GATE-8", "MFG-9"})
	require.NoError(t, err)
	require.Equal(t, 3, released, "every gate tag must be releasable, or a role-tagged hull is stranded dedicated")
	require.Equal(t, []assignFleetCall{
		{symbol: "GATE-7", fleet: ""},
		{symbol: "GATE-8", fleet: ""},
		{symbol: "MFG-9", fleet: ""},
	}, repo.assigned)
}

// Widening the guard must NOT widen it to foreign fleets: a contract or trade hull is
// never a gate hull, and re-tagging one would be a poach (RULINGS #7).
func TestRetagGateWorkers_StillRefusesForeignFleets(t *testing.T) {
	foreign := reclaimHull(t, "CONTRACT-4", 40, contractFleetTag, navigation.NavStatusInOrbit)
	traded := reclaimHull(t, "TRADE-5", 40, tradeFleetTag, navigation.NavStatusInOrbit)
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{foreign, traded}}
	r := &bootstrapGateSurplusReleaser{shipRepo: repo}

	released, err := r.ReleaseSurplusGateWorkers(context.Background(), 1, []string{"CONTRACT-4", "TRADE-5"})
	require.NoError(t, err)
	require.Equal(t, 0, released, "a foreign-fleet hull must never be re-tagged by a gate path")
	require.Nil(t, repo.assigned)
}

// The mid-task guard survives the widening: a role-tagged hull in transit is mid-haul and
// must never be yanked out from under its leg.
func TestRetagGateWorkers_StillSkipsARoleTaggedHullMidHaul(t *testing.T) {
	midHaul := reclaimHull(t, "GATE-7", 40, gate.DeliveryFleetTag, navigation.NavStatusInTransit)
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{midHaul}}
	r := &bootstrapGateSurplusReleaser{shipRepo: repo}

	released, err := r.ReleaseSurplusGateWorkers(context.Background(), 1, []string{"GATE-7"})
	require.NoError(t, err)
	require.Equal(t, 0, released)
	require.Nil(t, repo.assigned, "a hull mid-haul must finish its leg before any reassignment")
}

// --- THE WRITE PATH ITSELF (the plan changes BuyForConstruction but scripts no test for it) ---

// gateBuyMediator answers the money-integrity batch purchase with one freshly-bought hull per
// call, minting a DISTINCT symbol each time so a multi-hull ramp can be told apart hull by hull.
// It records every dispatched command, which is how a test proves what was NOT spent.
type gateBuyMediator struct {
	common.Mediator
	mu     sync.Mutex
	sent   []common.Request
	minted int
}

func (m *gateBuyMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	m.mu.Lock()
	m.sent = append(m.sent, request)
	isPurchase := false
	if _, ok := request.(*shipyardCmd.BatchPurchaseShipsCommand); ok {
		isPurchase = true
		m.minted++
	}
	minted := m.minted
	m.mu.Unlock()

	if !isPurchase {
		return nil, nil
	}
	bought, err := navigation.NewShip(
		gateBoughtSymbol(minted), shared.MustNewPlayerID(1),
		mustWaypoint("X1-HQ-YARD"), mustFuel(), 100, 40, mustCargo(), 30,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusDocked,
	)
	if err != nil {
		return nil, err
	}
	return &shipyardCmd.BatchPurchaseShipsResponse{
		PurchasedShips: []*navigation.Ship{bought}, TotalCost: 1000, ShipsPurchasedCount: 1,
	}, nil
}

func (m *gateBuyMediator) purchases() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.sent {
		if _, ok := r.(*shipyardCmd.BatchPurchaseShipsCommand); ok {
			n++
		}
	}
	return n
}

// gateBoughtSymbol names the nth bought hull (n starts at 1). It OFFSETS PAST 1 so no minted
// symbol ever ends in the "-1" suffix contract.IsCommandHullSymbolRole treats as the command
// frigate. That matters because retagGateWorkers consults IsCommandHull, and the ramp test
// appends these hulls into the fleet as ordinary members: a "-1" fixture routed through any
// release path would be silently SKIPPED, and a skipped hull still reads as a green result.
// (strconv, not rune arithmetic, so a ramp longer than 9 hulls keeps minting real numbers.)
func gateBoughtSymbol(n int) string {
	return "GATEBUY-" + strconv.Itoa(n+1)
}

func newGateWorkerAcquirerForTest(med common.Mediator, repo navigation.ShipRepository) *bootstrapGateWorkerAcquirer {
	return &bootstrapGateWorkerAcquirer{
		bootstrapAcquirer: &bootstrapAcquirer{med: med, shipRepo: repo, lastAsks: map[askKey]int64{}},
		shipRepo:          repo,
	}
}

// The tag written at purchase IS the ClaimShip operation for the role: ClaimShip authorizes a
// new claim only when tag == operation, so a hull tagged with anything else is rejected at the
// DB and silently never works. The FIRST gate hull is delivery.
func TestBuyForConstruction_DedicatesTheBoughtHullUnderItsRoleTag(t *testing.T) {
	purchaser := reclaimHull(t, "TORWIND-3", 40, navigation.PurchasingFleet, navigation.NavStatusInOrbit)
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{purchaser}}
	med := &gateBuyMediator{}
	acquirer := newGateWorkerAcquirerForTest(med, repo)

	bought, err := acquirer.BuyForConstruction(context.Background(), 1, "SHIP_LIGHT_HAULER", "X1-HQ-YARD")

	require.NoError(t, err)
	require.Equal(t, 1, med.purchases(), "the buy must still run through the money-integrity batch path")
	require.Equal(t, []assignFleetCall{{symbol: bought.ShipSymbol, fleet: gate.DeliveryFleetTag}}, repo.assigned,
		"AssignFleet is the single write path, and the value written is the role's tag")
	require.NotEqual(t, manufacturingFleetTag, repo.assigned[0].fleet,
		"a freshly-bought hull carries a ROLE now, not the pre-role legacy tag")
	// Non-tautological: the tag must be the exact string the role's drain claims under. Any other
	// value leaves the hull discoverable by tag but rejected by ClaimShip.
	require.Equal(t, "gate-delivery", gate.DeliveryFleetTag)
}

// THE PURCHASE ORDER, observable at the write path. The interleave is load-bearing at every
// PARTIAL state (the state that occurs when treasury is tight): first hull delivery so any
// accumulated factory stock starts moving, and stopping after two leaves one of each rather
// than two of the same. Each bought hull is persisted carrying its tag, which is exactly what
// the NEXT derivation reads — no cursor, so a restart mid-ramp resumes correctly.
func TestBuyForConstruction_RampWritesTheDeliveryFactoryFactoryDeliveryOrder(t *testing.T) {
	purchaser := reclaimHull(t, "TORWIND-3", 40, navigation.PurchasingFleet, navigation.NavStatusInOrbit)
	repo := &fakeReclaimShipRepo{all: []*navigation.Ship{purchaser}}
	med := &gateBuyMediator{}
	acquirer := newGateWorkerAcquirerForTest(med, repo)

	var written []string
	for hull := 0; hull < 4; hull++ {
		bought, err := acquirer.BuyForConstruction(context.Background(), 1, "SHIP_LIGHT_HAULER", "X1-HQ-YARD")
		require.NoError(t, err)
		require.Len(t, repo.assigned, hull+1, "every buy must dedicate exactly one hull")
		tag := repo.assigned[hull].fleet
		written = append(written, tag)
		// The bought hull is now in the fleet carrying its tag — the live count the next role reads.
		gateHull := reclaimHull(t, bought.ShipSymbol, 40, tag, navigation.NavStatusInOrbit)
		// The fixture must not mint the "-1" symbol the command-frigate heuristic reserves: these
		// hulls join the fleet as ordinary members, and retagGateWorkers would silently skip one.
		require.False(t, contract.IsCommandHull(gateHull),
			"a command-hull fixture would be skipped by every re-tag path and still report green")
		repo.all = append(repo.all, gateHull)
	}

	require.Equal(t, []string{
		gate.DeliveryFleetTag, gate.FactoryFleetTag, gate.FactoryFleetTag, gate.DeliveryFleetTag,
	}, written, "the 4-hull ramp must land 1D, then F, then F, then D — an interleave, not two of a kind first")
	require.Equal(t, 4, med.purchases())
}

// The role resolves BEFORE the spend, so an unreadable fleet costs a DEFERRED purchase rather
// than a bought hull tagged by guess (or left untagged, which strands it). Failing the fleet
// read on the SECOND call is what makes the ORDER observable: with the buy first, that read
// lands after the money is gone.
func TestBuyForConstruction_ResolvesTheRoleBeforeTheSpend(t *testing.T) {
	cases := []struct {
		name       string
		failOnCall int
	}{
		{"the role read itself fails -> propagate, spend nothing", 1},
		{"the read AFTER the role resolves fails -> the spend never happened", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			purchaser := reclaimHull(t, "TORWIND-3", 40, navigation.PurchasingFleet, navigation.NavStatusInOrbit)
			repo := &nthFleetReadFailsRepo{
				fakeReclaimShipRepo: &fakeReclaimShipRepo{all: []*navigation.Ship{purchaser}},
				failOnCall:          tc.failOnCall,
			}
			med := &gateBuyMediator{}
			acquirer := newGateWorkerAcquirerForTest(med, repo)

			_, err := acquirer.BuyForConstruction(context.Background(), 1, "SHIP_LIGHT_HAULER", "X1-HQ-YARD")

			require.Error(t, err)
			require.Equal(t, 0, med.purchases(), "an unreadable fleet must defer the buy, never spend against a guessed role")
			require.Nil(t, repo.fakeReclaimShipRepo.assigned, "nothing was bought, so nothing may be dedicated")
		})
	}
}

// nthFleetReadFailsRepo fails exactly the nth FindAllByPlayer and serves the fleet on every
// other call — the shape that distinguishes "role resolved before the spend" from "role
// resolved after it", which an always-failing repo cannot tell apart.
type nthFleetReadFailsRepo struct {
	*fakeReclaimShipRepo
	failOnCall int
	calls      int
}

func (r *nthFleetReadFailsRepo) FindAllByPlayer(ctx context.Context, playerID shared.PlayerID) ([]*navigation.Ship, error) {
	r.calls++
	if r.calls == r.failOnCall {
		return nil, errors.New("fleet store unreadable")
	}
	return r.fakeReclaimShipRepo.FindAllByPlayer(ctx, playerID)
}
