package grpc

// Tests for the three remaining heavy-money adapters: the pricing errand (who could go, and flying
// them), the SHARED heavy-target transport, and the tag-independent heavy census.
//
// Every one of them feeds a money guard, so the shape they all share is the one under test: a read
// that cannot be made REFUSES. A silent zero from the census reads as "we own no heavies" and
// authorises re-buying a 1.5–2.9M hull we already have; a phantom target reads as "there is a priced
// yard" and forms a reservation against a number nobody quoted.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	navCmd "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/application/system/gategraph"
	"github.com/andrescamacho/spacetraders-go/internal/domain/marketscan"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// --- fakes -------------------------------------------------------------------------------------

// recordingErrandMediator captures every dispatch so a test can assert BOTH what was sent and that
// nothing else was: the errand's contract is that it navigates and does not dock, quote or spend.
type recordingErrandMediator struct {
	common.Mediator
	sent []common.Request
	err  error
}

func (m *recordingErrandMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	m.sent = append(m.sent, request)
	if m.err != nil {
		return nil, m.err
	}
	return &navCmd.NavigateRouteResponse{Status: "completed"}, nil
}

type fakeHeavyTargetSource struct {
	target shipyardQueries.HeavyTarget
	err    error
}

func (f *fakeHeavyTargetSource) HeavyTarget(_ context.Context, _ int) (shipyardQueries.HeavyTarget, error) {
	return f.target, f.err
}

type fakeHeavyHullCounter struct {
	count     int
	err       error
	gotPlayer shared.PlayerID
}

func (f *fakeHeavyHullCounter) CountHeavyHulls(_ context.Context, playerID shared.PlayerID) (int, error) {
	f.gotPlayer = playerID
	return f.count, f.err
}

// errandHull builds a hull with the raw facts the errand policy judges. navStatus drives both
// Idle-vs-busy reporting and the in-transit case, where the domain model sets currentLocation to the
// DESTINATION — which is what makes "an errand is already under way" a pure read of durable rows.
func errandHull(t *testing.T, symbol string, playerID int, waypoint, fleet string, cargoCap int) *navigation.Ship {
	t.Helper()
	return errandHullAtSpeed(t, symbol, playerID, waypoint, fleet, cargoCap, 30)
}

// errandHullAtSpeed names the engine rating too — the fact the carrier choice ranks on, and the one
// that separates a satellite from a hauler standing in the same pool.
func errandHullAtSpeed(t *testing.T, symbol string, playerID int, waypoint, fleet string, cargoCap, speed int) *navigation.Ship {
	t.Helper()
	cargo, err := shared.NewCargo(cargoCap, 0, nil)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	wp, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	ship, err := navigation.NewShip(
		symbol, shared.MustNewPlayerID(playerID), wp, fuel, 100, cargoCap, cargo, speed,
		"FRAME_FRIGATE", "HAULER", nil, navigation.NavStatusDocked,
	)
	require.NoError(t, err)
	ship.SetDedicatedFleet(fleet)
	return ship
}

// fakeScoutPostRoster is the live scout-post read the errand consults so it never takes a probe a
// post already owns.
type fakeScoutPostRoster struct {
	posts []*domainScouting.ScoutPost
	err   error
}

func (f *fakeScoutPostRoster) ListActive(_ context.Context, _ int) ([]*domainScouting.ScoutPost, error) {
	return f.posts, f.err
}

// emptyRoster is the "no post mans anything" case, stated explicitly rather than left nil — a nil
// roster REFUSES, and a test that leaned on nil would be testing the refusal by accident.
func emptyRoster() *fakeScoutPostRoster { return &fakeScoutPostRoster{} }

// --- ErrandHulls -------------------------------------------------------------------------------

// THE ADAPTER FILTERS NOTHING, ON PURPOSE. Eligibility — a spare parked probe, unclaimed by any
// scout post, idle, not already flying — is the application layer's rule. Pre-filtering here would
// move that rule out of reach of the tests pinning it, so every hull must appear in this list
// carrying the raw facts that get it chosen or refused.
func TestErrandHulls_ReportsRawFactsForEveryHullAndPreFiltersNothing(t *testing.T) {
	inTransit := errandHull(t, "TR-2", 1, "X1-HOME-A1", "trade", 40)
	inTransit.SetNavStatus(navigation.NavStatusInOrbit)
	destination, err := shared.NewWaypoint("X1-YARD-Y1", 0, 0)
	require.NoError(t, err)
	require.NoError(t, inTransit.StartTransit(destination))

	e := &heavyYardPricingErrand{
		shipRepo: &fakeHeavyShipRepo{all: []*navigation.Ship{
			errandHull(t, "TR-1", 1, "X1-HOME-A1", "trade", 40),
			inTransit,
			errandHullAtSpeed(t, "PROBE-9", 1, "X1-DARK-A1", "sensing_parked", 0, 9),
		}},
		posts: emptyRoster(),
	}

	hulls, err := e.ErrandHulls(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, hulls, 3, "every hull is reported; the allowlist belongs to the policy, not to this read")

	require.Equal(t, "TR-1", hulls[0].Symbol)
	require.Equal(t, "trade", hulls[0].Fleet)
	require.Equal(t, "X1-HOME-A1", hulls[0].Location)
	require.True(t, hulls[0].Idle)
	require.False(t, hulls[0].InTransit)
	require.Equal(t, 40, hulls[0].CargoCapacity)
	require.Equal(t, 30, hulls[0].EngineSpeed)
	require.False(t, hulls[0].MannedScoutPost)

	require.Equal(t, "TR-2", hulls[1].Symbol)
	require.True(t, hulls[1].InTransit)
	require.Equal(t, "X1-YARD-Y1", hulls[1].Location,
		"a hull in transit reports its DESTINATION — that is what lets 'an errand is already under way' be a pure read")

	require.Equal(t, "PROBE-9", hulls[2].Symbol)
	require.Equal(t, "sensing_parked", hulls[2].Fleet)
	require.Zero(t, hulls[2].CargoCapacity,
		"a probe's zero hold must still reach the policy verbatim — the read reports facts, it does not judge them")
	require.Equal(t, 9, hulls[2].EngineSpeed,
		"the engine rating is the fact the carrier choice ranks on, so each hull must carry its own")
}

// THE STANDING OWNER RULE'S EVIDENCE (sp-gmfvw). The errand now draws from the SAME probe pool the
// scout coordinator mans its posts from, so the fleet tag no longer separates a spare hull from a
// working one. A hull a live post names — in its primary slot OR in any extra slot of a multi-hull
// post — must arrive at the policy marked, or the errand will take a working scout off station in
// the idle gap between two of its tours.
func TestErrandHulls_MarksEveryHullALiveScoutPostNames(t *testing.T) {
	e := &heavyYardPricingErrand{
		shipRepo: &fakeHeavyShipRepo{all: []*navigation.Ship{
			errandHull(t, "PROBE-PRIMARY", 1, "X1-AA11-A1", "sensing_parked", 0),
			errandHull(t, "PROBE-EXTRA", 1, "X1-AA11-B2", "sensing_parked", 0),
			errandHull(t, "PROBE-FREE", 1, "X1-BB22-C3", "sensing_parked", 0),
		}},
		posts: &fakeScoutPostRoster{posts: []*domainScouting.ScoutPost{
			{
				SystemSymbol: "X1-AA11",
				Hulls:        2,
				AssignedHull: "PROBE-PRIMARY",
				ExtraSlots:   []domainScouting.ScoutPostSlot{{AssignedHull: "PROBE-EXTRA"}},
			},
			{SystemSymbol: "X1-CC33"}, // a declared post with nobody on it reserves no hull
			nil,                       // a torn row must not panic the read
		}},
	}

	hulls, err := e.ErrandHulls(context.Background(), 1)
	require.NoError(t, err)
	require.Len(t, hulls, 3)

	require.True(t, hulls[0].MannedScoutPost, "the hull in the post's PRIMARY slot is that post's")
	require.True(t, hulls[1].MannedScoutPost,
		"a multi-hull post's EXTRA slot owns its probe just as much — reading only the scalar column leaves every extra slot exposed")
	require.False(t, hulls[2].MannedScoutPost, "a probe no post names is genuinely spare")
}

// An unreadable fleet REFUSES. An empty hull list is not the same fact: it reads as "no carrier is
// free", which the coordinator logs as a benign wait and retries forever without ever saying the
// fleet could not be read.
func TestErrandHulls_AnUnreadableFleetRefusesInsteadOfReportingNoCarriers(t *testing.T) {
	broken := &heavyYardPricingErrand{shipRepo: &fakeHeavyShipRepo{err: errors.New("db down")}, posts: emptyRoster()}
	hulls, err := broken.ErrandHulls(context.Background(), 1)
	require.Error(t, err)
	require.Nil(t, hulls)

	valid := &heavyYardPricingErrand{
		shipRepo: &fakeHeavyShipRepo{all: []*navigation.Ship{errandHull(t, "TR-1", 1, "X1-HOME-A1", "trade", 40)}},
		posts:    emptyRoster(),
	}
	hulls, err = valid.ErrandHulls(context.Background(), 0) // an invalid player is not a player with no hulls
	require.Error(t, err)
	require.Nil(t, hulls)
}

// AN UNREADABLE — OR UNWIRED — SCOUT-POST ROSTER REFUSES THE WHOLE READ, and that direction is the
// point. Reporting the fleet with MannedScoutPost false everywhere reads to the policy as "no post
// mans anything", which is the single reading that authorises taking a working scout off station.
// A refusal costs one tick of waiting and says so in the log.
func TestErrandHulls_AnUnreadableScoutPostRosterRefusesInsteadOfReportingEveryHullFree(t *testing.T) {
	fleet := &fakeHeavyShipRepo{all: []*navigation.Ship{
		errandHull(t, "PROBE-1", 1, "X1-AA11-A1", "sensing_parked", 0),
	}}

	unreadable := &heavyYardPricingErrand{shipRepo: fleet, posts: &fakeScoutPostRoster{err: errors.New("db down")}}
	hulls, err := unreadable.ErrandHulls(context.Background(), 1)
	require.Error(t, err, "a roster that cannot be read must refuse, never report every probe unclaimed")
	require.Nil(t, hulls)

	unwired := &heavyYardPricingErrand{shipRepo: fleet}
	hulls, err = unwired.ErrandHulls(context.Background(), 1)
	require.Error(t, err, "an unwired roster is not evidence that no post mans anything")
	require.Nil(t, hulls)
}

// --- SendToYard --------------------------------------------------------------------------------

// The errand NAVIGATES and does nothing else. Presence in orbit is enough for a shipyard listing to
// price and the purchase path docks on its own account, so exactly one command goes out and it is a
// route — no dock, no quote, no purchase (RULINGS #4: this tick spends nothing).
func TestSendToYard_NavigatesTheHullAndIssuesNoOtherCommand(t *testing.T) {
	med := &recordingErrandMediator{}
	e := &heavyYardPricingErrand{med: med}

	require.NoError(t, e.SendToYard(context.Background(), 7, "TR-1", "X1-YARD-Y1"))

	require.Len(t, med.sent, 1, "the errand must issue exactly one command — it never docks, quotes or buys")
	nav, ok := med.sent[0].(*navCmd.NavigateRouteCommand)
	require.True(t, ok, "the errand must reuse the shared route+refuel command, got %T", med.sent[0])
	require.Equal(t, "TR-1", nav.ShipSymbol)
	require.Equal(t, "X1-YARD-Y1", nav.Destination)
	require.Equal(t, shared.MustNewPlayerID(7), nav.PlayerID)
}

// A dispatch that fails surfaces as an error naming the hull and the yard, so the coordinator logs a
// retryable failure rather than recording a flight that never happened — a phantom errand would make
// the next tick count one "in flight" and dispatch nobody, forever.
func TestSendToYard_ADispatchFailureSurfacesInsteadOfReportingAFlight(t *testing.T) {
	med := &recordingErrandMediator{err: errors.New("no route")}
	e := &heavyYardPricingErrand{med: med}

	err := e.SendToYard(context.Background(), 7, "TR-1", "X1-YARD-Y1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "TR-1")
	require.Contains(t, err.Error(), "X1-YARD-Y1")

	// An invalid player never reaches the mediator: nothing is flown on a request we cannot resolve.
	clean := &recordingErrandMediator{}
	e = &heavyYardPricingErrand{med: clean}
	require.Error(t, e.SendToYard(context.Background(), 0, "TR-1", "X1-YARD-Y1"))
	require.Empty(t, clean.sent, "an unresolvable player must dispatch nothing")
}

// --- the SHARED heavy target transport ---------------------------------------------------------

// The reader TRANSPORTS and re-derives nothing — a second opinion on which yard we are saving toward
// is precisely how a reservation drifts away from the yard the buy targets. All four fields carry
// through unchanged, including the state that matters most: capability open with NOTHING priced,
// which must never collapse into "priced at 0".
func TestAutosizerHeavyYardReader_TransportsTheSharedTargetVerbatim(t *testing.T) {
	cases := []struct {
		name   string
		target shipyardQueries.HeavyTarget
	}{
		{"a priced target", shipyardQueries.HeavyTarget{
			CapabilityOpen: true, Priced: true, WaypointSymbol: "X1-PR33-CC3C", PurchasePrice: 2_931_905,
		}},
		{"capability open, nothing priced — the state the errand exists to resolve", shipyardQueries.HeavyTarget{
			CapabilityOpen: true, Priced: false, WaypointSymbol: "", PurchasePrice: 0,
		}},
		{"no heavy yard known at all", shipyardQueries.HeavyTarget{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fleetHeavyYardReader{yards: &fakeHeavyTargetSource{target: tc.target}}

			got, err := r.HeavyTarget(context.Background(), 1)
			require.NoError(t, err)
			require.Equal(t, tc.target.CapabilityOpen, got.CapabilityOpen)
			require.Equal(t, tc.target.Priced, got.Priced)
			require.Equal(t, tc.target.WaypointSymbol, got.WaypointSymbol)
			require.Equal(t, tc.target.PurchasePrice, got.PurchasePrice)
		})
	}
}

// RULINGS #4: an unreadable target REFUSES with a zero value. "We could not look" must never arrive
// at the reservation as "there is a priced yard": a phantom CapabilityOpen or a phantom price would
// hold treasury back against a yard nobody found.
func TestAutosizerHeavyYardReader_AReadFailureRefusesWithNoPhantomTarget(t *testing.T) {
	r := &fleetHeavyYardReader{yards: &fakeHeavyTargetSource{
		target: shipyardQueries.HeavyTarget{CapabilityOpen: true, Priced: true, WaypointSymbol: "X1-PR33-CC3C", PurchasePrice: 2_931_905},
		err:    errors.New("db down"),
	}}

	got, err := r.HeavyTarget(context.Background(), 1)
	require.Error(t, err)
	require.False(t, got.CapabilityOpen, "an unreadable target must not open the capability")
	require.False(t, got.Priced)
	require.Zero(t, got.PurchasePrice, "an unreadable target must carry no price at all")
	require.Empty(t, got.WaypointSymbol)
}

// --- the tag-independent heavy census -----------------------------------------------------------

// The census counts through the asserted repository capability, scoped to the player. It is
// deliberately NOT the trade-pool count: a heavy tagged elsewhere would be invisible to that, leaving
// the reservation open and authorising a re-buy of a hull we already own.
func TestAutosizerHeavyCensus_CountsOwnedHeaviesForThePlayer(t *testing.T) {
	counter := &fakeHeavyHullCounter{count: 2}
	c := &fleetHeavyCensus{counter: counter}

	owned, err := c.HeaviesOwned(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, 2, owned)
	require.Equal(t, shared.MustNewPlayerID(7), counter.gotPlayer, "the census must be scoped to the asking player")
}

// A census that cannot be read REFUSES. A silent 0 reads as "we own no heavies", which opens the
// heavy_cap guard and authorises buying a hull we may already have (RULINGS #4).
func TestAutosizerHeavyCensus_AnUnreadableCensusRefusesInsteadOfCountingZero(t *testing.T) {
	broken := &fleetHeavyCensus{counter: &fakeHeavyHullCounter{err: errors.New("db down")}}
	_, err := broken.HeaviesOwned(context.Background(), 7)
	require.Error(t, err)

	invalidPlayer := &fleetHeavyCensus{counter: &fakeHeavyHullCounter{count: 3}}
	_, err = invalidPlayer.HeaviesOwned(context.Background(), 0)
	require.Error(t, err, "an unresolvable player is not a player owning zero heavies")
}

// --- the navigator-truth reach, and the free read (sp-37alr) -------------------------------------

// recordingHopSource captures the reach question so a test can assert the errand asks it FROM THE
// CARRIER'S SYSTEM and AT THE EXECUTOR'S BOUND — the two things the yard catalogue gets wrong.
type recordingHopSource struct {
	distances map[string]int
	err       error
	gotFrom   string
	gotTo     []string
	gotBound  int
	calls     int
}

func (f *recordingHopSource) StoredHopDistances(_ context.Context, fromSystem string, targets []string, maxJumps int) (map[string]int, error) {
	f.calls++
	f.gotFrom, f.gotTo, f.gotBound = fromSystem, targets, maxJumps
	return f.distances, f.err
}

// Routable is the depotHomeRouter half the server's gateGraph field is typed on. The errand reaches
// its own capability by asserting past that narrower type, exactly as the live wiring does, so this
// fake must carry both faces or the wiring test would exercise a path production never takes.
func (f *recordingHopSource) Routable(_ context.Context, _, _ string, _ int) (bool, error) {
	return true, nil
}

// THE FIX'S LOAD-BEARING ADAPTER FACT: the reach question is asked from ONE named origin and capped
// at the SAME bound the navigator enforces.
//
// The catalogue's Reachable is a multi-source distance from every system the FLEET stands in, which
// is a lower bound on the distance from the hull that will actually fly. Asking from the carrier's
// own system is the entire correction: live, the catalogue called X1-FH57 two hops away while the
// navigator refused "no jump-gate route from X1-AM71 to X1-FH57 within 5 jumps" 61 times.
func TestHopsFrom_AsksFromTheCarriersOwnSystemAtTheNavigatorsBound(t *testing.T) {
	hops := &recordingHopSource{distances: map[string]int{"X1-KP46": 3}}
	e := &heavyYardPricingErrand{hops: hops}

	got, err := e.HopsFrom(context.Background(), "X1-AM71", []string{"X1-FH57", "X1-KP46"})
	require.NoError(t, err)
	require.Equal(t, map[string]int{"X1-KP46": 3}, got)
	require.Equal(t, "X1-AM71", hops.gotFrom, "the reach must be measured from the carrier's system, not from the fleet's nearest")
	require.Equal(t, []string{"X1-FH57", "X1-KP46"}, hops.gotTo)
	require.Equal(t, gategraph.MaxJumpPath, hops.gotBound,
		"the errand must be held to the bound the EXECUTOR enforces — a looser copy is how a hull gets dispatched to a yard the flight can never route to")
	require.Equal(t, heavyYardReachBoundHops, hops.gotBound, "and that bound is the one the catalogue is capped at, not a second opinion")
}

// An unwired or unreadable gate graph REFUSES. An empty map would reach the policy as a genuine
// "no route exists to any yard" — which retires the dispatching errand silently and permanently,
// the exact failure class this whole mechanism keeps being bitten by.
func TestHopsFrom_UnwiredOrUnreadableRefusesInsteadOfReportingNothingReachable(t *testing.T) {
	unwired := &heavyYardPricingErrand{}
	_, err := unwired.HopsFrom(context.Background(), "X1-AM71", []string{"X1-FH57"})
	require.Error(t, err, "an unwired gate graph is not a fleet that can reach nowhere")

	broken := &heavyYardPricingErrand{hops: &recordingHopSource{err: errors.New("adjacency store down")}}
	_, err = broken.HopsFrom(context.Background(), "X1-AM71", []string{"X1-FH57"})
	require.Error(t, err)
}

// THE FREE ERRAND'S ONE OUTBOUND ACT: read the yard, at the DENIABLE class, and issue nothing else.
//
// The class is the safety argument and therefore the assertion. This read fills a row that says 0;
// no money guard consumes it, and every spend guard downstream still takes its own live Earning read
// before a credit moves. Discretionary keeps the trait filter, the rescan-window floor and the
// budget in force, so a yard that keeps reporting no ask is retried at the window's pace rather than
// re-read into a request storm. Stamping this Earning would exempt it from all three.
func TestPriceYardInPlace_ReadsTheYardDeniablyAndIssuesNoOtherCommand(t *testing.T) {
	med := &recordingErrandMediator{}
	e := &heavyYardPricingErrand{med: med}

	require.NoError(t, e.PriceYardInPlace(context.Background(), 7, "X1-AS98-X23E"))

	require.Len(t, med.sent, 1, "the free errand reads the yard and does nothing else — it never docks, quotes or spends")
	q, ok := med.sent[0].(*shipyardQueries.GetShipyardListingsQuery)
	require.True(t, ok, "the read must go through the fleet's ONE metered shipyard query, got %T", med.sent[0])
	require.Equal(t, "X1-AS98-X23E", q.WaypointSymbol)
	require.Equal(t, "X1-AS98", q.SystemSymbol)
	require.Equal(t, shared.MustNewPlayerID(7), q.PlayerID, "the mediator resolves the token from PlayerID — an unset one reads no yard")
	require.NotEqual(t, marketscan.Earning, q.Class,
		"this is DISCOVERY, not a pre-commit price: Earning would exempt it from the trait filter, the rescan window and the budget all at once")
	require.Equal(t, marketscan.Discretionary, q.Class, "and the deniable class is the zero value, so forgetting to stamp it stays the safe direction")
}

// A refused read SURFACES. The errand logs it as a wait and retries; swallowing it would report a
// price landed when the budget served nothing, and the yard would stay at 0 with nobody looking.
func TestPriceYardInPlace_ADeclinedReadSurfacesInsteadOfReportingSuccess(t *testing.T) {
	med := &recordingErrandMediator{err: errors.New("shipyard listings for X1-AS98-X23E were not read live")}
	e := &heavyYardPricingErrand{med: med}

	require.Error(t, e.PriceYardInPlace(context.Background(), 7, "X1-AS98-X23E"))

	invalidPlayer := &heavyYardPricingErrand{med: &recordingErrandMediator{}}
	require.Error(t, invalidPlayer.PriceYardInPlace(context.Background(), 0, "X1-AS98-X23E"))
}

// THE WIRING IS THE FEATURE. newHeavyYardPricingErrand takes the reach off the server's already-
// injected gate graph, so an errand built from a server that HAS one must be able to answer — and
// one built from a server that does not must refuse rather than silently report nothing reachable.
//
// This is the "closed is not armed" shape: both failure modes are quiet. A nil graph makes the
// dispatching errand decline forever with a correct-looking reason, which is indistinguishable from
// a fleet whose map has not grown — the state this bead spent 25 hours in.
func TestNewAutosizerPricingErrand_TakesTheReachOffTheServersGateGraph(t *testing.T) {
	wired := newHeavyYardPricingErrand(&DaemonServer{gateGraph: &recordingHopSource{distances: map[string]int{"X1-KP46": 2}}}, nil, nil)
	got, err := wired.HopsFrom(context.Background(), "X1-AM71", []string{"X1-KP46"})
	require.NoError(t, err, "a server holding a gate graph must yield an errand that can prove reach")
	require.Equal(t, map[string]int{"X1-KP46": 2}, got)

	for name, server := range map[string]*DaemonServer{"no server": nil, "no gate graph": {}} {
		t.Run(name, func(t *testing.T) {
			_, err := newHeavyYardPricingErrand(server, nil, nil).HopsFrom(context.Background(), "X1-AM71", []string{"X1-KP46"})
			require.Error(t, err, "an unwired reach must refuse, not report a fleet that can reach nowhere")
		})
	}
}
