package storage

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStorageOperationStatusProjectsLifecycleState pins every lifecycle-state ->
// OperationStatus row of Status(). All five states are reachable through the
// aggregate's own transitions, so the full projection table is exercised
// end-to-end.
func TestStorageOperationStatusProjectsLifecycleState(t *testing.T) {
	cases := []struct {
		name  string
		drive func(t *testing.T, op *StorageOperation)
		want  OperationStatus
	}{
		{"pending on construction", func(t *testing.T, op *StorageOperation) {}, OperationStatusPending},
		{"running after start", func(t *testing.T, op *StorageOperation) {
			require.NoError(t, op.Start())
		}, OperationStatusRunning},
		{"completed after complete", func(t *testing.T, op *StorageOperation) {
			require.NoError(t, op.Start())
			require.NoError(t, op.Complete())
		}, OperationStatusCompleted},
		{"stopped after stop", func(t *testing.T, op *StorageOperation) {
			require.NoError(t, op.Start())
			require.NoError(t, op.Stop())
		}, OperationStatusStopped},
		{"failed after fail", func(t *testing.T, op *StorageOperation) {
			require.NoError(t, op.Start())
			require.NoError(t, op.Fail(fmt.Errorf("boom")))
		}, OperationStatusFailed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op, err := NewStorageOperation(
				"op-1", 42, "X1-A1", OperationTypeMining,
				[]string{"EXT-1"}, []string{"STORE-1"}, []string{"IRON_ORE"}, nil,
			)
			require.NoError(t, err)
			tc.drive(t, op)
			require.Equal(t, tc.want, op.Status())
		})
	}
}
