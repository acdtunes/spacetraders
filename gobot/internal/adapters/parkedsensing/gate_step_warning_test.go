package parkedsensing_test

// The gate walk's two failure exits must BOTH be named.
//
// Only the unroutable one ever was. A route that RESOLVED and then could not be
// walked propagated back through flyToSlot to dispatch, which held the slot for the
// next tick and logged nothing at all — so a hull could spend one placement attempt
// per tick indefinitely and produce no diagnostics. That is the state TORWIND-15F
// was found in: 22.5 hours BOUGHT for X1-K39-AC2B with ZERO lines in daemon.log
// naming it, which is why the freeze had to be diagnosed from the database rather
// than from the logs.

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	adapterSensing "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	shipNav "github.com/andrescamacho/spacetraders-go/internal/application/ship/commands/navigation"
	shipQueries "github.com/andrescamacho/spacetraders-go/internal/application/ship/queries"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// recordedLog is one captured log line, reduced to what the assertions care about:
// the action tag that makes it greppable and the metadata that makes it useful.
type recordedLog struct {
	level    string
	message  string
	metadata map[string]interface{}
}

type recordingLogger struct {
	mu    sync.Mutex
	lines []recordedLog
}

func (l *recordingLogger) Log(level, message string, metadata map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, recordedLog{level, message, metadata})
}

func (l *recordingLogger) withAction(action string) []recordedLog {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []recordedLog
	for _, line := range l.lines {
		if line.metadata["action"] == action {
			out = append(out, line)
		}
	}
	return out
}

// gateStepWorld answers the gate lookup with the waypoint the hull is already
// standing on — so the walk reaches step TWO, the jump — and then refuses the
// jump. That is the shape of a real refusal: the stored graph was right and the
// MOVE failed, on a cooldown, a fuel state, or a gate that will not take the hull.
type gateStepWorld struct {
	gate    string
	jumpErr error
}

func (w gateStepWorld) Send(_ context.Context, request mediator.Request) (mediator.Response, error) {
	switch request.(type) {
	case *shipQueries.FindNearestJumpGateQuery:
		gate, err := shared.NewWaypoint(w.gate, 0, 0)
		if err != nil {
			return nil, err
		}
		gate.Type = "JUMP_GATE"
		return &shipQueries.FindNearestJumpGateResponse{JumpGate: gate}, nil
	case *shipNav.JumpShipCommand:
		return nil, w.jumpErr
	}
	return nil, nil
}

func (gateStepWorld) Register(reflect.Type, mediator.RequestHandler) error { return nil }
func (gateStepWorld) RegisterMiddleware(mediator.Middleware)               {}

func TestRouteAcross_NamesARefusedGateStep(t *testing.T) {
	logger := &recordingLogger{}
	ctx := logging.WithLogger(context.Background(), logger)

	mover := adapterSensing.NewMoverPort(
		gateStepWorld{gate: "X1-AA-G1", jumpErr: errors.New("ship action is still on cooldown")},
		stubGateNeighbours{edges: map[string][]string{"X1-AA": {"X1-BB"}}},
	)

	err := mover.RouteAcross(ctx, testPlayerID, "TORWIND-15F", "X1-AA-G1", "X1-BB-M1")
	require.Error(t, err, "a refused jump must still be reported to the caller, which holds the slot and retries")

	named := logger.withAction("parked_sensing_gate_step_failed")
	require.Len(t, named, 1,
		"a refused gate step emitted no named warning: a hull in this state burns one placement attempt per tick "+
			"and stays invisible in the logs, which is exactly how 318 frozen slots went unnoticed for 22.5 hours")

	line := named[0]
	require.Equal(t, "WARNING", line.level)
	// The hull, where it stands, and where it was going: a warning naming only the
	// error leaves the reader grepping the ledger for the placement it belongs to.
	require.Equal(t, "TORWIND-15F", line.metadata["ship_symbol"])
	require.Equal(t, "X1-AA-G1", line.metadata["from_waypoint"])
	require.Equal(t, "X1-BB", line.metadata["next_system"])
	require.Equal(t, "X1-BB-M1", line.metadata["destination"])
	require.Contains(t, line.message, "cooldown", "the underlying refusal must survive into the message")

	// The two exits stay DISTINCT. Unroutable means the stored graph names no way
	// there and the fix is a gate re-probe; a refused step means the graph was right
	// and the move failed. Reading one as the other sends the next reader to the
	// wrong subsystem.
	require.Empty(t, logger.withAction("parked_sensing_gate_walk_unroutable"),
		"a refused step was reported as an unroutable graph")
}

// TestRouteAcross_StillNamesAnUnroutableWalk keeps the pre-existing warning under
// test too, so the pair cannot silently collapse into one.
func TestRouteAcross_StillNamesAnUnroutableWalk(t *testing.T) {
	logger := &recordingLogger{}
	ctx := logging.WithLogger(context.Background(), logger)

	mover := adapterSensing.NewMoverPort(
		gateStepWorld{gate: "X1-AA-G1"},
		// No adjacency at all: the BFS can name no next system.
		stubGateNeighbours{edges: map[string][]string{}},
	)

	err := mover.RouteAcross(ctx, testPlayerID, "PROBE-A", "X1-AA-G1", "X1-BB-M1")
	require.Error(t, err)

	named := logger.withAction("parked_sensing_gate_walk_unroutable")
	require.Len(t, named, 1, "an unroutable walk must still be named")
	require.Empty(t, logger.withAction("parked_sensing_gate_step_failed"),
		"an unroutable walk was reported as a refused step, though no step was ever attempted")

	// routerHopBound is the unbounded sentinel (0), not a real cap: printed as "within
	// 0 jumps" it reads as a hop limit that was hit, which is not what happened here.
	require.NotContains(t, named[0].message, "0 jumps",
		"the unbounded search sentinel was printed as if it were a real hop count")
	require.Contains(t, named[0].message, "unbounded",
		"the message must say the search was unbounded, not imply a numeric cap was reached")
}
