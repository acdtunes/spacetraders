package grpc

import (
	"context"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"

	"github.com/stretchr/testify/require"
)

// growthPort reads a named port off the CONSTRUCTED coordinator. The coupling to the field name is
// deliberate: the wiring outcome IS the behaviour under test. A rename must update this, never
// delete it.
func growthPort(t *testing.T, h *fleetCmd.RunFleetGrowthCoordinatorHandler, field string) reflect.Value {
	t.Helper()
	v := reflect.ValueOf(h).Elem().FieldByName(field)
	require.True(t, v.IsValid(),
		"the growth coordinator no longer has a %q port; this test pins what is WIRED into it, so a rename must update it rather than remove it", field)
	require.Equal(t, reflect.Interface, v.Kind(), "%q is expected to be a port interface", field)
	return v
}

// THE DEMONSTRATED-CAPACITY PORT IS THE PEAK-OVER-WINDOW READ, AND NOTHING ELSE. A LIVE balance
// reaching the wave through this field flaps exactly as the live-treasury form did, and every test
// in the predicate's package still passes — it is a WIRING defect. So it is asserted on the handler
// the composition root actually builds: a handler built without the wiring call at all is the
// failure a test of the seam in isolation cannot see. The peak contract is pinned where implemented.
func TestGrowthHighWaterPort_IsWiredIntoTheCoordinator(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewGormTransactionRepository(db)

	// The REAL composition, with only the ledger varied: nothing here performs I/O.
	h := NewFleetGrowthCoordinatorHandler(&DaemonServer{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, repo)

	port := growthPort(t, h, "highWater")
	require.False(t, port.IsNil(),
		"the demonstrated-capacity port must be WIRED — unwired, the wave is permanently PROBE and no heavy is ever bought")
	require.Equal(t, reflect.ValueOf(repo).Pointer(), port.Elem().Pointer(),
		"the coordinator must read the ledger's own peak port, not an adapter over a point read")

	// And a ledger the composition never received must leave the port unwired rather than wire
	// something that silently reports a zero peak — proof this probe can tell the two apart.
	unwired := NewFleetGrowthCoordinatorHandler(&DaemonServer{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	require.True(t, growthPort(t, unwired, "highWater").IsNil(),
		"no ledger means no port at all: a phantom zero peak would read as a fleet that has never held any money")
}

// The seam's own contract, beside the wiring: the port type must remain the ledger's peak reader.
func TestGrowthHighWaterPort_IsTheLedgersPeakOverWindowReader(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewGormTransactionRepository(db)

	port := growthHighWaterPort(repo)

	require.Same(t, repo, port, "the coordinator must read the ledger's own peak port, not an adapter over a point read")
	var _ ledger.TreasuryHighWaterReader = port
	var _ fleetCmd.TreasuryHighWaterReader = port
}

// A LIVE-TREASURY READER MUST NOT FIT THE DEMONSTRATED-CAPACITY SLOT. The two questions are
// different — "how much may I withhold right now" versus "is this ask reachable by this fleet" —
// and the type system is what keeps the answer to one from being handed to the other.
func TestGrowthHighWaterPort_TheLiveTreasuryReaderDoesNotSatisfyIt(t *testing.T) {
	var live interface{} = &autosizerTreasuryReader{}
	if _, ok := live.(fleetCmd.TreasuryHighWaterReader); ok {
		t.Fatal("the LIVE treasury reader satisfies the demonstrated-capacity port — a live balance could be wired into the wave")
	}
}

// The growth coordinator's metrics sink must implement the whole sink: a partial implementation
// would compile behind an embedded interface and panic on the first un-overridden call, in the buy
// path, in production.
func TestGrowthMetricsSink_ImplementsTheWholeSink(t *testing.T) {
	var _ fleetCmd.GrowthMetricsSink = &growthMetricsSink{}
}

// buildGrowthHandlerOn runs the REAL composition with only the heavy collaborators varied. Every
// other argument is nil on purpose: nothing here performs I/O, so a handler built this way
// exercises the wiring decisions and nothing else.
func buildGrowthHandlerOn(server *DaemonServer, shipRepo navigation.ShipRepository, scannedYards scannedYardRanker, heavyYards heavyYardInventory) *fleetCmd.RunFleetGrowthCoordinatorHandler {
	return NewFleetGrowthCoordinatorHandler(server, nil, nil, shipRepo, nil, nil, nil, scannedYards, heavyYards, nil, nil)
}

func buildGrowthHandler(shipRepo navigation.ShipRepository, scannedYards scannedYardRanker, heavyYards heavyYardInventory) *fleetCmd.RunFleetGrowthCoordinatorHandler {
	return buildGrowthHandlerOn(&DaemonServer{}, shipRepo, scannedYards, heavyYards)
}

// THE RANKER ASSERTION, BOTH POLARITIES. The errand's catalogue and its dispatch are wired together
// or not at all: a catalogue with no way to fly to what it finds, or a dispatcher with nothing to
// aim at, is not a half-working errand — it is a broken one.
//
// It is asserted on the GROWTH coordinator because that is the fleet's heavy buyer, and the errand
// hangs off the buyer: a coordinator that never buys a heavy has no reason to pay a probe's time
// for a heavy's ask, and two coordinators holding the ports would each dispatch against the same
// one-hull-at-a-time bound.
func TestGrowthWiring_PricingErrandFollowsTheAvailabilityOnlyRankCapability(t *testing.T) {
	full := buildGrowthHandler(&fakeCensusShipRepo{}, &fakeFullYardFinder{}, nil)
	require.False(t, growthPort(t, full, "heavyYardCatalog").IsNil(),
		"a ranker that can list availability-only rows must wire the catalogue")
	require.False(t, growthPort(t, full, "heavyErrand").IsNil(),
		"the catalogue and the dispatch are wired together — a catalogue with no way to fly there prices nothing")

	pricedOnly := buildGrowthHandler(&fakeCensusShipRepo{}, &fakeScannedYards{}, nil)
	require.True(t, growthPort(t, pricedOnly, "heavyYardCatalog").IsNil(),
		"a priced-only ranker cannot see unpriced yards, so the catalogue must stay unwired rather than report a catalogue that can never contain the row the errand acts on")
	require.True(t, growthPort(t, pricedOnly, "heavyErrand").IsNil(),
		"no catalogue means no errand: a dispatcher with nothing to aim at must not be wired")

	unwired := buildGrowthHandler(&fakeCensusShipRepo{}, nil, nil)
	require.True(t, growthPort(t, unwired, "heavyYardCatalog").IsNil())
	require.True(t, growthPort(t, unwired, "heavyErrand").IsNil())
}

// THE SCOUT-POST ROSTER MUST COME OFF THE DAEMON'S CONNECTION.
//
// The errand draws its carrier from the parked-sensing pool, which it SHARES with the scout
// coordinator, so it can only tell a spare probe from a working one by reading the posts. Without
// the roster the errand refuses every tick — fail-closed and correct, and therefore invisible:
// exactly the class of permanently-silent stall this pins against.
//
// The composition cannot omit the roster because it never passes one: it passes the server, and
// newAutosizerPricingErrand takes the roster off it. This pins that constructor's contract in both
// polarities, which is what makes the omission unexpressible rather than merely unlikely.
func TestGrowthWiring_PricingErrandReadsTheScoutPostRoster(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	live := newAutosizerPricingErrand(&DaemonServer{db: db}, nil, &fakeCensusShipRepo{})
	require.NotNil(t, live.posts,
		"a daemon with a connection must yield an errand that can read the scout posts — without it every tick refuses, silently and forever")

	// PROOF THE ASSERTION HAS TEETH: no connection leaves the roster nil, and the resulting errand
	// is still a usable port — the refusal happens at read time, not at boot.
	for _, server := range []*DaemonServer{{}, nil} {
		dry := newAutosizerPricingErrand(server, nil, &fakeCensusShipRepo{})
		require.NotNil(t, dry, "a connectionless daemon must still yield a port, not a nil one")
		require.Nil(t, dry.posts)
		_, rerr := dry.ErrandHulls(context.Background(), 1)
		require.Error(t, rerr, "an errand with no roster must REFUSE the read rather than report every probe unclaimed")
	}

	// And the real composition still installs the port, so the two halves meet.
	installed := buildGrowthHandlerOn(&DaemonServer{db: db}, &fakeCensusShipRepo{}, &fakeFullYardFinder{}, nil)
	require.False(t, growthPort(t, installed, "heavyErrand").IsNil())
}

// THE ERRAND HAS EXACTLY ONE DRIVER, and the composition root is where a second one would appear.
// The port fields are unexported, so a handler that was never given them cannot dispatch at all —
// which is what makes the bound of one a property of the wiring rather than of the tick loops.
func TestGrowthWiring_TheAutosizerIsNoLongerAnErrandDriver(t *testing.T) {
	autosizer := buildAutosizerHandler(&fakeCensusShipRepo{}, &fakeFullYardFinder{}, nil)
	for _, port := range []string{"heavyYardCatalog", "heavyErrand"} {
		require.False(t, reflect.ValueOf(autosizer).Elem().FieldByName(port).IsValid(),
			"the autosizer must hold no %q port: two drivers each read the same in-flight bound and each conclude nothing is under way", port)
	}
}
