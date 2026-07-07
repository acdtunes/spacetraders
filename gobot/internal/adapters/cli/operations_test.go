package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The daemon persists container types from the domain constants, which are
// UPPERCASE (e.g. "MANUFACTURING_COORDINATOR", "GAS_COORDINATOR"). The
// operations verbs classify against these registered types; these tests pin the
// verbs to the types the daemon actually stores so `operations status`/`stop`
// can never again silently miss a running coordinator (sp-dpsq).

func TestClassifyOperationContainersMatchesRegisteredTypes(t *testing.T) {
	containers := []*ContainerInfo{
		{ContainerID: "gas_coordinator-X1-abc", ContainerType: "GAS_COORDINATOR"},
		{ContainerID: "gas_siphon-1", ContainerType: "GAS_SIPHON_WORKER"},
		{ContainerID: "parallel_manufacturing-X1-def", ContainerType: "MANUFACTURING_COORDINATOR"},
		{ContainerID: "mfg-worker-1", ContainerType: "MANUFACTURING_TASK_WORKER"},
		{ContainerID: "storage-1", ContainerType: "STORAGE_SHIP"},
	}

	groups := classifyOperationContainers(containers)

	require.Len(t, groups.gasCoordinators, 1)
	require.Equal(t, "gas_coordinator-X1-abc", groups.gasCoordinators[0].ContainerID)

	require.Len(t, groups.gasWorkers, 1)
	require.Equal(t, "gas_siphon-1", groups.gasWorkers[0].ContainerID)

	require.Len(t, groups.mfgCoordinators, 1,
		"a running MANUFACTURING_COORDINATOR must be tracked as a manufacturing coordinator, not lost to Other")
	require.Equal(t, "parallel_manufacturing-X1-def", groups.mfgCoordinators[0].ContainerID)

	require.Len(t, groups.mfgWorkers, 1)
	require.Equal(t, "mfg-worker-1", groups.mfgWorkers[0].ContainerID)

	require.Len(t, groups.other, 1)
	require.Equal(t, "storage-1", groups.other[0].ContainerID)
}

func TestClassifyOperationContainersTracksParallelManufacturingType(t *testing.T) {
	// PARALLEL_MANUFACTURING is the sibling constant for the parallel task-based
	// coordinator (container IDs are prefixed "parallel_manufacturing-"); it is a
	// manufacturing coordinator for the operator's purposes.
	containers := []*ContainerInfo{
		{ContainerID: "parallel_manufacturing-X1-ghi", ContainerType: "PARALLEL_MANUFACTURING"},
	}

	groups := classifyOperationContainers(containers)

	require.Len(t, groups.mfgCoordinators, 1)
	require.Empty(t, groups.other)
}

func TestSelectCoordinatorsToStopTracksManufacturingCoordinator(t *testing.T) {
	containers := []*ContainerInfo{
		{ContainerID: "parallel_manufacturing-X1-PZ28-f388df4b", ContainerType: "MANUFACTURING_COORDINATOR"},
	}

	toStop := selectCoordinatorsToStop(containers, false, true, "")

	require.Len(t, toStop, 1,
		"operations stop --manufacturing must match a running MANUFACTURING_COORDINATOR")
	require.Equal(t, "parallel_manufacturing-X1-PZ28-f388df4b", toStop[0].ContainerID)
}

func TestSelectCoordinatorsToStopRespectsTypeAndSystemFilters(t *testing.T) {
	gasCoord := &ContainerInfo{
		ContainerID:   "gas_coordinator-X1-AU21",
		ContainerType: "GAS_COORDINATOR",
		Metadata:      `{"system_symbol":"X1-AU21"}`,
	}
	mfgCoord := &ContainerInfo{
		ContainerID:   "parallel_manufacturing-X1-PZ28",
		ContainerType: "MANUFACTURING_COORDINATOR",
		Metadata:      `{"system_symbol":"X1-PZ28"}`,
	}
	mfgWorker := &ContainerInfo{
		ContainerID:   "mfg-worker-1",
		ContainerType: "MANUFACTURING_TASK_WORKER",
		Metadata:      `{"system_symbol":"X1-PZ28"}`,
	}
	containers := []*ContainerInfo{gasCoord, mfgCoord, mfgWorker}

	cases := []struct {
		name              string
		stopGas           bool
		stopManufacturing bool
		system            string
		wantIDs           []string
	}{
		{"manufacturing only", false, true, "", []string{"parallel_manufacturing-X1-PZ28"}},
		{"gas only", true, false, "", []string{"gas_coordinator-X1-AU21"}},
		{"both types", true, true, "", []string{"gas_coordinator-X1-AU21", "parallel_manufacturing-X1-PZ28"}},
		{"system filter narrows to one", true, true, "X1-PZ28", []string{"parallel_manufacturing-X1-PZ28"}},
		{"workers are never selected", false, true, "X1-PZ28", []string{"parallel_manufacturing-X1-PZ28"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			toStop := selectCoordinatorsToStop(containers, tc.stopGas, tc.stopManufacturing, tc.system)

			gotIDs := make([]string, len(toStop))
			for i, c := range toStop {
				gotIDs[i] = c.ContainerID
			}
			require.ElementsMatch(t, tc.wantIDs, gotIDs)
		})
	}
}
