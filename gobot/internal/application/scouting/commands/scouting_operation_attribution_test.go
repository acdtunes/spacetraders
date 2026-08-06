package commands

// scouting_operation_attribution_test.go is the acceptance gate for sp-wxgd2: every
// coordinator in this package that can reach a SPEND must stamp an operation context,
// because the leaf handlers that record the spend (jump gate fee, refuel, cargo,
// outfitting) read it off ctx and label the row "manual" when they find none.
//
// "manual" is not a category — it is the else branch. It reads to an operator as "a
// human did this" and means "attribution was lost". Sensing and scouting were the only
// spending subsystems that never stamped one, which is why 686,731 credits of probe
// spend (113 unattributed jump fees, all naming hulls this package flies) sat in that
// bucket.
//
// THE ASSERTIONS ARE TAKEN AT THE PORT THAT ISSUES THE SPEND, not at the coordinator
// that stamps it: the ctx the mover/repositioner/mediator actually receives is the ctx
// the leaf recorder will read, so a stamp applied to a context that never reaches them
// would pass a "the coordinator calls WithOperationContext" test and still book
// everything 'manual'. The leaf's own half of the contract — that this exact string
// lands in operation_type rather than 'manual' — is pinned end-to-end against a real
// ledger in ship/commands/navigation/jump_ship_fee_operation_type_test.go.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- the sensing engine ---------------------------------------------------------

// opCtxSpyMover wraps the real fake mover and records the OPERATION CONTEXT visible on
// the ctx of every movement verb. It records the derived context rather than the ctx
// itself so the assertion reads the same value jump_ship_fee.go would.
type opCtxSpyMover struct {
	inner     parkedsensing.ShipMover
	routes    []*shared.OperationContext
	navigates []*shared.OperationContext
	docks     []*shared.OperationContext
}

func (m *opCtxSpyMover) NavigateWithin(ctx context.Context, playerID int, shipSymbol, destination string) error {
	m.navigates = append(m.navigates, shared.OperationContextFromContext(ctx))
	return m.inner.NavigateWithin(ctx, playerID, shipSymbol, destination)
}

func (m *opCtxSpyMover) RouteAcross(ctx context.Context, playerID int, shipSymbol, fromWaypoint, destination string) error {
	m.routes = append(m.routes, shared.OperationContextFromContext(ctx))
	return m.inner.RouteAcross(ctx, playerID, shipSymbol, fromWaypoint, destination)
}

func (m *opCtxSpyMover) Dock(ctx context.Context, playerID int, shipSymbol string) error {
	m.docks = append(m.docks, shared.OperationContextFromContext(ctx))
	return m.inner.Dock(ctx, playerID, shipSymbol)
}

// requireSensingAttribution asserts one captured context is the sensing engine's.
func requireSensingAttribution(t *testing.T, opCtx *shared.OperationContext, containerID, where string) {
	t.Helper()
	require.NotNil(t, opCtx, "%s ran on an UNSTAMPED context: its spend books as 'manual'", where)
	require.True(t, opCtx.IsValid(), "%s carried an incomplete context, which the leaf treats exactly like none", where)
	require.Equal(t, parkedsensing.SensingCoverageOperationType, opCtx.NormalizedOperationType(),
		"%s must book under the engine's own name", where)
	require.NotEqual(t, "manual", opCtx.NormalizedOperationType(),
		"%s must never fall through to the unattributed bucket", where)
	require.Equal(t, containerID, opCtx.ContainerID,
		"%s must name the container that owns the work, so the row joins back to the coordinator", where)
}

// THE HEADLINE. A gate crossing driven by a sensing tick reaches the mover — the port
// that issues JumpShipCommand, and therefore the port whose ctx the jump-gate fee is
// recorded against — carrying the sensing operation context.
//
// The crossing is the right fixture rather than a same-system hop because the crossing
// is what COSTS: a jump charges a gate fee, and those fees were 579,597 of the 686,731
// credits that landed in 'manual'.
func TestSensingTick_AGateCrossingCarriesTheSensingOperationContext(t *testing.T) {
	world := crossingWorld(t, "X1-GF41")
	world.gates.link("X1-KP23", "X1-GF41")
	world.ledger.slots[psSlotKey{"X1-GF41-M7", parkedsensing.SlotKindMarket}] = wantedAt("X1-GF41-M7")

	spy := &opCtxSpyMover{inner: world.mover}
	ports := world.ports
	ports.Mover = spy
	world.handler.SetEnginePortsFactory(func(int) SensingEnginePorts { return ports })

	runTicks(t, world, 4)

	// Non-vacuity: a fixture that never crossed a gate would pass every loop below by
	// iterating nothing, which is the exact shape of a test that proves nothing.
	require.NotEmpty(t, spy.routes,
		"precondition: the tick must actually have issued a gate-walk step — with none, the "+
			"assertions below iterate an empty slice and pass whatever the code does")
	require.Equal(t, "X1-GF41-M7", world.shipPos.at["TORWIND-14"].Waypoint,
		"precondition: the hull really crossed and berthed, so the whole spending path ran")

	for _, opCtx := range spy.routes {
		requireSensingAttribution(t, opCtx, world.cmd.ContainerID, "the gate-walk step (which issues the JUMP)")
	}
	for _, opCtx := range spy.navigates {
		requireSensingAttribution(t, opCtx, world.cmd.ContainerID, "the in-system hop (which can book a refuel)")
	}
	for _, opCtx := range spy.docks {
		requireSensingAttribution(t, opCtx, world.cmd.ContainerID, "the berth")
	}
}

// THE LABEL IS THE ONE ALREADY IN THE LEDGER, and this pins the literal.
//
// The engine's probe PURCHASES have always been booked "sensing coverage", and the
// historical unattributed jump fees are being backfilled to that same string. A rename
// here — even to something more descriptive — silently splits one engine's costs across
// two operation_types and makes every per-operation breakdown wrong on the seam, which
// is not a failure any behavioural test would catch.
func TestSensingAttribution_ReusesTheExistingLedgerLabel(t *testing.T) {
	require.Equal(t, "sensing coverage", parkedsensing.SensingCoverageOperationType,
		"the sensing engine's buys, its jump fees and the historical backfill must all carry ONE string")
	require.Equal(t, "sensing coverage",
		shared.NewOperationContext("c", parkedsensing.SensingCoverageOperationType).NormalizedOperationType(),
		"and NormalizedOperationType must pass it through unchanged — a mapping added for it would "+
			"put the buys and the fees in different rows")
}

// --- the scout-post relay -------------------------------------------------------

// opCtxSpyRepositioner records the operation context visible to the shared travel
// machinery, which is what actually crosses the gates and pays their fees.
type opCtxSpyRepositioner struct {
	plain []*shared.OperationContext
	chart []*shared.OperationContext
}

func (f *opCtxSpyRepositioner) RepositionToWaypointWithinJumps(ctx context.Context, _, _ string, _, _ int) error {
	f.plain = append(f.plain, shared.OperationContextFromContext(ctx))
	return nil
}

func (f *opCtxSpyRepositioner) RepositionToSystemGateAndChart(ctx context.Context, _ string, _, _ int) error {
	f.chart = append(f.chart, shared.OperationContextFromContext(ctx))
	return nil
}

// The cross-gate relay is the scouting subsystem's JUMPING path — crossing gates is all
// it does — so its gate fees are the ones most worth attributing. Both relay shapes are
// covered: a mis-stamp on the gate-charting branch would leave exactly the reconcile
// traffic unattributed.
func TestScoutReposition_StampsItsOwnOperationContext(t *testing.T) {
	const coordinator = "scout_post_coordinator-player-7"

	for name, chartOnArrival := range map[string]bool{
		"the plain market relay":        false,
		"the gate-charting 0-hop relay": true,
	} {
		t.Run(name, func(t *testing.T) {
			spy := &opCtxSpyRepositioner{}
			_, err := NewScoutRepositionHandler(spy).Handle(context.Background(), &ScoutRepositionCommand{
				PlayerID:            shared.MustNewPlayerID(testPlayerID),
				ShipSymbol:          "SAT-1",
				DestinationWaypoint: "X1-FAR-A1",
				CoordinatorID:       coordinator,
				ChartGateOnArrival:  chartOnArrival,
			})
			require.NoError(t, err)

			seen := append(append([]*shared.OperationContext{}, spy.plain...), spy.chart...)
			require.Len(t, seen, 1, "precondition: exactly one move is delegated, and it is the one under test")
			require.NotNil(t, seen[0], "the relay flew on an UNSTAMPED context: its gate fees book as 'manual'")
			require.Equal(t, ScoutRepositionOperationType, seen[0].NormalizedOperationType())
			require.NotEqual(t, "manual", seen[0].NormalizedOperationType())
			require.Equal(t, coordinator, seen[0].ContainerID)
		})
	}
}

// A relay with no spawning coordinator is a DIRECT/CLI dispatch, and it stays 'manual'
// on purpose. Labelling an operator's own action as automated is the same defect as
// losing attribution, pointing the other way — so the absence is asserted, not tolerated.
func TestScoutReposition_WithoutACoordinator_StaysHonestlyUnattributed(t *testing.T) {
	spy := &opCtxSpyRepositioner{}
	_, err := NewScoutRepositionHandler(spy).Handle(context.Background(), &ScoutRepositionCommand{
		PlayerID:            shared.MustNewPlayerID(testPlayerID),
		ShipSymbol:          "SAT-1",
		DestinationWaypoint: "X1-FAR-A1",
	})
	require.NoError(t, err)

	require.Len(t, spy.plain, 1)
	require.Nil(t, spy.plain[0],
		"a CLI-launched relay must NOT be dressed up as coordinator work — 'manual' is correct there")
}

// --- the scout tour -------------------------------------------------------------

// opCtxSpyTourMediator answers a navigation instantly and records the operation context
// the leg would have booked its refuel and any gate fee against.
type opCtxSpyTourMediator struct {
	common.Mediator
	seen []*shared.OperationContext
}

func (m *opCtxSpyTourMediator) Send(ctx context.Context, _ common.Request) (common.Response, error) {
	m.seen = append(m.seen, shared.OperationContextFromContext(ctx))
	return &shipNav.NavigateRouteResponse{Status: "completed"}, nil
}

// A tour NAVIGATES, and the route executor behind that navigation books the refuels (and
// any gate fee a cross-system leg costs) off whatever context rides the ctx. Entered
// through Handle rather than executeMultiMarketTour, because Handle is where the stamp
// lives and a test that entered below it could not see it at all.
func TestScoutTour_StampsItsOperationContextOnEveryLeg(t *testing.T) {
	const coordinator = "scout_post_coordinator-player-7"
	med := &opCtxSpyTourMediator{}
	handler := &ScoutTourHandler{
		shipRepo: &fakeTourShipRepo{ship: scoutProbe(t, "PROBE-1", "X1-AA1-M1")},
		mediator: med,
		clock:    &shared.MockClock{CurrentTime: time.Now()},
	}

	_, err := handler.Handle(context.Background(), &ScoutTourCommand{
		PlayerID:      shared.MustNewPlayerID(testPlayerID),
		ShipSymbol:    "PROBE-1",
		Markets:       []string{"X1-AA1-M1", "X1-AA1-M2"},
		Iterations:    1,
		CoordinatorID: coordinator,
		ScanInterval:  30 * time.Minute,
	})
	require.NoError(t, err)

	require.NotEmpty(t, med.seen, "precondition: the tour must actually have navigated")
	for _, opCtx := range med.seen {
		require.NotNil(t, opCtx, "a tour leg flew UNSTAMPED: its refuels book as 'manual'")
		require.Equal(t, ScoutTourOperationType, opCtx.NormalizedOperationType())
		require.NotEqual(t, "manual", opCtx.NormalizedOperationType())
		require.Equal(t, coordinator, opCtx.ContainerID)
	}
}

// The three coordinators that fly probes carry three DISTINCT names. One shared label
// would leave the ledger unable to answer "which of them spent this" — the same reason
// the sensing engine was given its own name instead of sharing "fleet expansion" with
// the frontier engine, where a shared label made a live money leak indistinguishable
// from an engine the operator had already switched off.
func TestScoutingOperationTypes_AreDistinctAndHumanReadable(t *testing.T) {
	names := []string{
		parkedsensing.SensingCoverageOperationType,
		ScoutTourOperationType,
		ScoutRepositionOperationType,
	}
	seen := map[string]bool{}
	for _, name := range names {
		require.NotEmpty(t, name)
		require.NotEqual(t, "manual", name, "no coordinator may claim the unattributed bucket's label")
		require.False(t, seen[name], "two probe-flying coordinators share the operation_type %q", name)
		seen[name] = true
	}
	require.Equal(t, "scout tour", ScoutTourOperationType)
	require.Equal(t, "scout reposition", ScoutRepositionOperationType)
}
