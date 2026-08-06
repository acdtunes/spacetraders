package grpc

import (
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	fleetCmd "github.com/andrescamacho/spacetraders-go/internal/application/fleet/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ledger"
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
