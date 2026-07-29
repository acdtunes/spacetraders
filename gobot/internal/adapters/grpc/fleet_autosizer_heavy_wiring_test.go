package grpc

// Tests for the heavy-money WIRING in NewFleetAutosizerCoordinatorHandler — the two capability
// assertions and the nil check that decide, at boot, whether heavy buying can happen at all.
//
// Each of them fails SILENTLY except for a log.Printf nobody reads: an unwired census makes the
// heavy_cap guard fail closed and no heavy is ever bought; an unwired heavy-yard reader leaves the
// reservation permanently 0 so expansion never holds credits back for a heavy; an unwired ranker
// means a known-but-unpriced yard is never priced, so no reservation can ever form. All three are
// the CORRECT fail-closed direction, which is exactly why nothing ever notices they happened.
//
// The assertions read the constructed handler's port fields by name. That coupling is deliberate:
// the wiring outcome IS the behaviour under test, and the ports are unexported in the coordinator's
// own package. A rename must update this test, never delete it.

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	shipyardQueries "github.com/andrescamacho/spacetraders-go/internal/application/shipyard/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeCensusShipRepo is a ship repository that DOES carry the tag-independent heavy census — the
// shape *persistence.GormShipRepository has in production.
type fakeCensusShipRepo struct {
	fakeHeavyShipRepo
	heavies int
}

func (r *fakeCensusShipRepo) CountHeavyHulls(_ context.Context, _ shared.PlayerID) (int, error) {
	return r.heavies, nil
}

// fakeFullYardFinder carries BOTH yard surfaces — the priced buy rank and the errand's rank that
// keeps availability-only rows — which is the shape *shipyardQueries.ReachableYardFinder has.
// fakeScannedYards deliberately carries only the first, and is the negative case.
type fakeFullYardFinder struct {
	fakeHeavyYardRanker
}

func (f *fakeFullYardFinder) NearestYardsSelling(_ context.Context, _ int, _, _ []string) ([]shipyardQueries.YardCandidate, error) {
	return nil, nil
}

// wiredPort reports whether a named port on the constructed coordinator was actually wired.
func wiredPort(t *testing.T, h *fleetCmd.RunFleetAutosizerCoordinatorHandler, field string) bool {
	t.Helper()
	v := reflect.ValueOf(h).Elem().FieldByName(field)
	require.True(t, v.IsValid(),
		"the autosizer coordinator no longer has a %q port; this test pins whether heavy buying is WIRED, so a rename must update it rather than remove it", field)
	require.Equal(t, reflect.Interface, v.Kind(), "%q is expected to be a port interface", field)
	return !v.IsNil()
}

// buildAutosizerHandler runs the REAL composition with only the heavy collaborators varied. Every
// other argument is nil on purpose: nothing here performs I/O, so a handler built this way exercises
// the wiring decisions and nothing else.
func buildAutosizerHandler(shipRepo navigation.ShipRepository, scannedYards scannedYardRanker, heavyYards heavyYardInventory) *fleetCmd.RunFleetAutosizerCoordinatorHandler {
	return NewFleetAutosizerCoordinatorHandler(
		&DaemonServer{}, nil, shipRepo, nil, nil, nil, nil, scannedYards, nil, heavyYards,
	)
}

// THE CENSUS ASSERTION, BOTH POLARITIES. A repository carrying CountHeavyHulls wires the census; one
// without it leaves the port unwired, which fails the heavy_cap guard CLOSED and stops heavy buying
// entirely — the state the loud WARN in the wiring exists to announce.
func TestAutosizerWiring_HeavyCensusFollowsTheRepositoryCapability(t *testing.T) {
	withCensus := buildAutosizerHandler(&fakeCensusShipRepo{heavies: 1}, nil, nil)
	require.True(t, wiredPort(t, withCensus, "heavyCensus"),
		"a repository carrying the tag-independent census must wire it — without it no heavy is ever bought")

	withoutCensus := buildAutosizerHandler(&fakeHeavyShipRepo{}, nil, nil)
	require.False(t, wiredPort(t, withoutCensus, "heavyCensus"),
		"a repository WITHOUT CountHeavyHulls must leave the census unwired (fail closed), never wire a port that silently counts zero")
}

// THE RANKER ASSERTION, BOTH POLARITIES. The errand's catalogue and its dispatch are wired together
// or not at all: a catalogue with no way to fly to what it finds, or a dispatcher with nothing to
// aim at, is not a half-working errand — it is a broken one.
func TestAutosizerWiring_PricingErrandFollowsTheAvailabilityOnlyRankCapability(t *testing.T) {
	full := buildAutosizerHandler(&fakeCensusShipRepo{}, &fakeFullYardFinder{}, nil)
	require.True(t, wiredPort(t, full, "heavyYardCatalog"),
		"a ranker that can list availability-only rows must wire the catalogue")
	require.True(t, wiredPort(t, full, "heavyErrand"),
		"the catalogue and the dispatch are wired together — a catalogue with no way to fly there prices nothing")

	pricedOnly := buildAutosizerHandler(&fakeCensusShipRepo{}, &fakeScannedYards{}, nil)
	require.False(t, wiredPort(t, pricedOnly, "heavyYardCatalog"),
		"a priced-only ranker cannot see unpriced yards, so the catalogue must stay unwired rather than report a catalogue that can never contain the row the errand acts on")
	require.False(t, wiredPort(t, pricedOnly, "heavyErrand"),
		"no catalogue means no errand: a dispatcher with nothing to aim at must not be wired")

	unwired := buildAutosizerHandler(&fakeCensusShipRepo{}, nil, nil)
	require.False(t, wiredPort(t, unwired, "heavyYardCatalog"))
	require.False(t, wiredPort(t, unwired, "heavyErrand"))
}

// THE SHARED-TARGET NIL CHECK, BOTH POLARITIES. Absent, the reservation is permanently 0 and
// expansion spending is never held back for a heavy — the fleet looks healthy and simply never
// accumulates one.
func TestAutosizerWiring_HeavyYardReaderFollowsTheSharedTarget(t *testing.T) {
	wired := buildAutosizerHandler(&fakeCensusShipRepo{}, nil, &fakeHeavyTargetSource{})
	require.True(t, wiredPort(t, wired, "heavyYard"),
		"a shared heavy target must be wired — it is the reservation's price term")

	absent := buildAutosizerHandler(&fakeCensusShipRepo{}, nil, nil)
	require.False(t, wiredPort(t, absent, "heavyYard"),
		"no shared target means no reservation port at all, never a port that reports a phantom 0 price")
}

// PROOF THE PROBE HAS TEETH: the port reader must be able to tell a wired port from an unwired one
// on the same handler. A reader that reported everything wired (or everything unwired) would make
// every assertion above vacuous while staying green.
func TestAutosizerWiring_PortProbeDistinguishesWiredFromUnwired(t *testing.T) {
	h := buildAutosizerHandler(&fakeCensusShipRepo{}, nil, nil)
	require.True(t, wiredPort(t, h, "heavyCensus"), "this handler HAS a census")
	require.False(t, wiredPort(t, h, "heavyYard"), "the same handler has NO shared heavy target")
}
