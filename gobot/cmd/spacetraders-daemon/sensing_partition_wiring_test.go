package main

// THE CHARTING CREW'S SOLVER, pinned at the composition root. A crew missing its
// partitioner keeps working — it falls back to angular sectors — which is why
// nothing else can see this: the engine's own tests supply their own partitioner.

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/routing"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

func TestSensingChartPartitionerIsTheFleetsOwnRoutingClient(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)

	client, err := routing.NewGRPCRoutingClient("127.0.0.1:0")
	require.NoError(t, err, "the client constructs lazily, so no service need be listening")
	t.Cleanup(func() { _ = client.Close() })

	// The REAL composition, with only the client varied: nothing here does I/O.
	ports := sensingWiring{db: db, routingClient: client}.enginePorts(nil, nil, 1)
	require.NotNil(t, ports.Partitioner,
		"the charting crew must be handed the fleet's partitioner; unwired, every crew silently "+
			"partitions by angular sector and the solver is never asked")
	require.Equal(t, reflect.ValueOf(client).Pointer(), reflect.ValueOf(ports.Partitioner).Pointer(),
		"the crew must partition through the SAME client the scout reset does, or two solvers cut one fleet")

	// A composition that never received a client must leave the field a GENUINE nil:
	// a typed nil is not nil to an interface, so it would sail past the fallback's
	// own guard and panic mid-tour instead.
	unwired := sensingWiring{db: db}.enginePorts(nil, nil, 1)
	require.Nil(t, unwired.Partitioner, "no client means no port at all")
}
