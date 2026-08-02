// gobot/internal/adapters/grpc/daemon_flows_live_test.go
//
// GET /api/flows reported 5 flows while `spacetraders container list`
// reported 13 RUNNING tour containers. Both now read the same runner map, so they
// cannot disagree — and a hull that joined mid-era or survived a daemon restart
// can no longer fall out of the feed while it is between publish points.
package grpc

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
	tradingCmd "github.com/andrescamacho/spacetraders-go/internal/application/trading/commands"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// runningRunner builds a registered runner in the RUNNING state around cmd,
// without touching a DB — liveTradingRuns only reads the entity and the command.
func runningRunner(t *testing.T, id string, cmd interface{}) *ContainerRunner {
	t.Helper()
	entity := container.NewContainer(id, container.ContainerTypeTrading, 5, 1, nil,
		map[string]interface{}{"operation": "trade"}, nil)
	require.NoError(t, entity.Start(), "container must be RUNNING for the feed to claim it")
	return NewContainerRunner(entity, nil, cmd, nil, nil, nil, nil)
}

func pendingRunner(t *testing.T, id string, cmd interface{}) *ContainerRunner {
	t.Helper()
	entity := container.NewContainer(id, container.ContainerTypeTrading, 5, 1, nil, nil, nil)
	return NewContainerRunner(entity, nil, cmd, nil, nil, nil, nil)
}

func serverWith(runners map[string]*ContainerRunner) *DaemonServer {
	s := &DaemonServer{containers: make(map[string]*ContainerRunner, len(runners))}
	for id, r := range runners {
		s.containers[id] = r
	}
	return s
}

func TestLiveTradingRuns_EnumeratesEveryRunningTradingContainer(t *testing.T) {
	s := serverWith(map[string]*ContainerRunner{
		// The original hulls and the ones the captain bought mid-era are
		// indistinguishable here — that is the point.
		"tour-run-TORWIND-A-aaa": runningRunner(t, "tour-run-TORWIND-A-aaa",
			&tradingCmd.RunTourCoordinatorCommand{ShipSymbol: "TORWIND-A", ContainerID: "tour-run-TORWIND-A-aaa"}),
		"tour-run-TORWIND-D8-ddd": runningRunner(t, "tour-run-TORWIND-D8-ddd",
			&tradingCmd.RunTourCoordinatorCommand{ShipSymbol: "TORWIND-D8", ContainerID: "tour-run-TORWIND-D8-ddd", ClosedTours: true}),
		"trade-route-TORWIND-6-bbb": runningRunner(t, "trade-route-TORWIND-6-bbb",
			&tradingCmd.RunTradeRouteCoordinatorCommand{ShipSymbol: "TORWIND-6"}),
		"arb-run-TORWIND-9-ccc": runningRunner(t, "arb-run-TORWIND-9-ccc",
			&tradingCmd.RunArbCoordinatorCommand{ShipSymbol: "TORWIND-9"}),
		// Not RUNNING (PENDING): claimed by nothing yet, so not a live flow.
		"tour-run-TORWIND-Z-zzz": pendingRunner(t, "tour-run-TORWIND-Z-zzz",
			&tradingCmd.RunTourCoordinatorCommand{ShipSymbol: "TORWIND-Z"}),
		// A RUNNING non-trading container publishes no flows and must be skipped.
		"mine-TORWIND-M-mmm": runningRunner(t, "mine-TORWIND-M-mmm", struct{ Name string }{"mining"}),
	})

	byID := map[string]flowfeed.LiveRun{}
	for _, run := range s.liveTradingRuns() {
		byID[run.ContainerID] = run
	}

	require.Len(t, byID, 4, "want every RUNNING trading container and nothing else: %v", byID)

	require.Equal(t, flowfeed.ProgramTour, byID["tour-run-TORWIND-A-aaa"].Program)
	require.Equal(t, "TORWIND-A", byID["tour-run-TORWIND-A-aaa"].Ship)
	require.False(t, byID["tour-run-TORWIND-A-aaa"].Closed)
	require.True(t, byID["tour-run-TORWIND-D8-ddd"].Closed, "closed-tour mode must ride through")
	require.Equal(t, flowfeed.ProgramTradeRoute, byID["trade-route-TORWIND-6-bbb"].Program)
	require.Equal(t, flowfeed.ProgramArb, byID["arb-run-TORWIND-9-ccc"].Program)

	require.NotContains(t, byID, "tour-run-TORWIND-Z-zzz", "a non-RUNNING container is not a live flow")
	require.NotContains(t, byID, "mine-TORWIND-M-mmm", "a non-trading container publishes no flows")
}

// The defect, end to end: 13 RUNNING tour containers, 5 of which have published.
// Before the fix the route served 5.
func TestFlowsRoute_ServesEveryRunningTourIncludingUnpublishedOnes(t *testing.T) {
	const total, published = 13, 5

	runners := map[string]*ContainerRunner{}
	ids := make([]string, 0, total)
	for i := 0; i < total; i++ {
		id := "tour-run-HULL-" + string(rune('A'+i)) + "-xyz"
		ids = append(ids, id)
		runners[id] = runningRunner(t, id,
			&tradingCmd.RunTourCoordinatorCommand{ShipSymbol: "HULL-" + string(rune('A'+i)), ContainerID: id})
	}
	s := serverWith(runners)

	// A restarted daemon: the registry NewDaemonServer builds, on a runner map
	// restart recovery has just rebuilt, with nothing published yet.
	reg := newFlowRegistry(s)
	for i := 0; i < published; i++ {
		reg.Publish(flowfeed.Flow{ContainerID: ids[i], Program: flowfeed.ProgramTour, Ship: "HULL-" + string(rune('A'+i))})
	}

	mux := http.NewServeMux()
	registerFlowsRoute(mux, reg)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/flows")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var feed struct {
		Flows []struct {
			ContainerID string `json:"containerId"`
			Ship        string `json:"ship"`
			Program     string `json:"program"`
		} `json:"flows"`
	}
	require.NoError(t, json.Unmarshal(body, &feed))

	require.Len(t, feed.Flows, total,
		"feed must match the RUNNING container count at request time (this is the 5-of-13 defect)")

	served := map[string]bool{}
	for _, f := range feed.Flows {
		served[f.ContainerID] = true
		require.Equal(t, flowfeed.ProgramTour, f.Program)
		require.NotEmpty(t, f.Ship, "every flow must name its hull, published or not")
	}
	for _, id := range ids {
		require.True(t, served[id], "RUNNING container %s missing from the feed", id)
	}
}

// A container that STOPS between requests leaves the feed once its executor's
// terminal exit removes the published flow — the live view must not resurrect it.
func TestFlowsRoute_StoppedContainerDropsOutAtNextRequest(t *testing.T) {
	id := "tour-run-HULL-A-xyz"
	runner := runningRunner(t, id, &tradingCmd.RunTourCoordinatorCommand{ShipSymbol: "HULL-A", ContainerID: id})
	s := serverWith(map[string]*ContainerRunner{id: runner})

	reg := newFlowRegistry(s)
	require.Len(t, reg.Snapshot().Flows, 1, "a RUNNING container is in the feed")

	require.NoError(t, runner.Container().Stop())
	require.NoError(t, runner.Container().MarkStopped())
	require.Empty(t, reg.Snapshot().Flows, "a stopped container must not be resurrected by the live view")
}

// Snapshot reaches into the runner map while executors publish concurrently; the
// feed lock must never be held across the container lock.
func TestFlowsRegistry_ConcurrentSnapshotAndPublish(t *testing.T) {
	id := "tour-run-HULL-A-xyz"
	s := serverWith(map[string]*ContainerRunner{
		id: runningRunner(t, id, &tradingCmd.RunTourCoordinatorCommand{ShipSymbol: "HULL-A", ContainerID: id}),
	})
	reg := newFlowRegistry(s)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); reg.Publish(flowfeed.Flow{ContainerID: id, Ship: "HULL-A"}) }()
		go func() { defer wg.Done(); _ = reg.Snapshot() }()
	}
	wg.Wait()

	require.Len(t, reg.Snapshot().Flows, 1)
}
