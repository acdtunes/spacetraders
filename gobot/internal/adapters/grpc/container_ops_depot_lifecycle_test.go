package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/contract/depotstore"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract/depot"
)

// fakeDepotRepo is an in-memory depotstore.Repository — the durable driven port's test
// double at the hexagonal boundary, so the start/stop lifecycle orchestration is proven
// without a DB. Persistence is observable through a fresh LoadRegistry (the same seam a
// restarted daemon rebuilds from).
type fakeDepotRepo struct {
	byID map[string]*depot.ContractDepot
}

func newFakeDepotRepo() *fakeDepotRepo {
	return &fakeDepotRepo{byID: map[string]*depot.ContractDepot{}}
}

func (f *fakeDepotRepo) List(context.Context) ([]*depot.ContractDepot, error) {
	out := make([]*depot.ContractDepot, 0, len(f.byID))
	for _, c := range f.byID {
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeDepotRepo) Get(_ context.Context, id string) (*depot.ContractDepot, bool, error) {
	c, ok := f.byID[id]
	return c, ok, nil
}

func (f *fakeDepotRepo) Save(_ context.Context, c *depot.ContractDepot) error {
	f.byID[c.ID()] = c
	return nil
}

func (f *fakeDepotRepo) Delete(_ context.Context, id string) error {
	delete(f.byID, id)
	return nil
}

// spyDepotStopSink records the container ids depot teardown stops, standing in for the
// live container registry (ListContainers + StopContainer) so the SELECTION logic is
// proven without spawning containers.
type spyDepotStopSink struct {
	refs    []depotContainerRef
	stopped []string
}

func (s *spyDepotStopSink) listDepotContainers(int) []depotContainerRef { return s.refs }

func (s *spyDepotStopSink) stopContainer(id string) error {
	s.stopped = append(s.stopped, id)
	return nil
}

// `depot start <name> <spec>` PERSISTS the named depot's topology AND launches its
// coordinators in one shot (live activation, no restart). Persistence is observable via a
// fresh LoadRegistry; the launch via the spy sink.
func TestStartDepot_PersistsTopologyAndLaunchesItsCoordinators(t *testing.T) {
	store := depotstore.New(newFakeDepotRepo())
	sink := &spyDepotSink{}
	c, err := depot.NewContractDepot("j58",
		[]depot.Element{{Waypoint: "X1-J58-WH", ShipSymbol: "WH-1"}},
		[]depot.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-1"}},
		nil, nil,
	)
	require.NoError(t, err)

	launched, err := startDepot(context.Background(), store, sink, 7, c)
	require.NoError(t, err)

	reg, err := store.LoadRegistry(context.Background())
	require.NoError(t, err)
	require.Len(t, reg.Depots(), 1, "start persists the depot into the durable set (restart-safe)")
	require.Equal(t, "j58", reg.Depots()[0].ID())

	require.Len(t, sink.warehouses, 1, "start launches the depot's warehouse coordinator")
	require.Equal(t, "WH-1", sink.warehouses[0].ship)
	require.Len(t, sink.stockers, 1, "start launches the depot's stocker coordinator")
	require.Equal(t, "ST-1", sink.stockers[0].ship)
	require.Equal(t, 2, launched, "one warehouse + one stocker coordinator were launched")
}

// `depot start <name>` is scoped to the ONE named depot: it launches only that depot's
// coordinators, even when the durable set already holds OTHER depots. It is a single-depot
// activation, never a whole-topology relaunch.
func TestStartDepot_LaunchesOnlyTheNamedDepot(t *testing.T) {
	store := depotstore.New(newFakeDepotRepo())
	other, err := depot.NewContractDepot("other",
		[]depot.Element{{Waypoint: "X1-OTHER-WH", ShipSymbol: "OTHER-WH"}},
		[]depot.Element{{Waypoint: "X1-OTHER-SRC", ShipSymbol: "OTHER-ST"}},
		nil, nil,
	)
	require.NoError(t, err)
	require.NoError(t, store.AddDepot(context.Background(), other))

	sink := &spyDepotSink{}
	target, err := depot.NewContractDepot("j58",
		[]depot.Element{{Waypoint: "X1-J58-WH", ShipSymbol: "WH-1"}},
		nil, nil, nil,
	)
	require.NoError(t, err)

	_, err = startDepot(context.Background(), store, sink, 7, target)
	require.NoError(t, err)

	require.Len(t, sink.warehouses, 1, "only the named depot's warehouse launches")
	require.Equal(t, "WH-1", sink.warehouses[0].ship,
		"the pre-existing OTHER depot's coordinator is NOT launched by `start j58`")
	require.Empty(t, sink.stockers, "the named depot declares no stocker, so none launches")
}

// `depot start <name>` is idempotent at the durable layer: re-running upserts the same
// depot rather than duplicating it (launch-idempotency is the sink's idle-gap discipline,
// relied on per sp-cftm — a hull already flying its coordinator is skipped). After two
// starts the registry holds exactly one j58.
func TestStartDepot_IsIdempotent_UpsertsWithoutDuplicating(t *testing.T) {
	store := depotstore.New(newFakeDepotRepo())
	sink := &spyDepotSink{}
	build := func() *depot.ContractDepot {
		c, err := depot.NewContractDepot("j58",
			[]depot.Element{{Waypoint: "X1-J58-WH", ShipSymbol: "WH-1"}},
			nil, nil, nil,
		)
		require.NoError(t, err)
		return c
	}

	_, err := startDepot(context.Background(), store, sink, 7, build())
	require.NoError(t, err)
	_, err = startDepot(context.Background(), store, sink, 7, build())
	require.NoError(t, err)

	reg, err := store.LoadRegistry(context.Background())
	require.NoError(t, err)
	require.Len(t, reg.Depots(), 1, "re-running start upserts the depot; it is never duplicated")
}

// `depot stop <name>` terminates exactly the named depot's warehouse + stocker coordinator
// containers (joined by the crewing ship), leaving containers the depot does not own
// running. It is the precise inverse of the coordinators `start` launches.
func TestStopDepot_StopsOnlyTheDepotsCoordinatorContainers(t *testing.T) {
	store := depotstore.New(newFakeDepotRepo())
	c, err := depot.NewContractDepot("j58",
		[]depot.Element{{Waypoint: "X1-J58-WH", ShipSymbol: "WH-1"}},
		[]depot.Element{{Waypoint: "X1-SRC-1", ShipSymbol: "ST-1"}},
		nil, nil,
	)
	require.NoError(t, err)
	require.NoError(t, store.AddDepot(context.Background(), c))

	sink := &spyDepotStopSink{refs: []depotContainerRef{
		{containerID: "cont-wh", shipSymbol: "WH-1"},    // depot warehouse coordinator
		{containerID: "cont-st", shipSymbol: "ST-1"},    // depot stocker coordinator
		{containerID: "cont-x", shipSymbol: "STRANGER"}, // unrelated coordinator
	}}

	stopped, err := stopDepot(context.Background(), store, sink, 7, "j58")
	require.NoError(t, err)

	require.ElementsMatch(t, []string{"cont-wh", "cont-st"}, sink.stopped,
		"stop terminates exactly the depot's warehouse + stocker coordinators")
	require.NotContains(t, sink.stopped, "cont-x", "a container the depot does not own is left running")
	require.Equal(t, 2, stopped)
}
