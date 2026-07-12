package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// fakeStatusLister is a black-box double for the container repo's status scan: it answers
// ListByStatusSimple from a per-status map so the singleton-admission guard can be driven without a DB.
type fakeStatusLister struct {
	byStatus map[string][]persistence.ContainerSummary
	err      error
	seen     []string // the statuses queried, in order (pins the RUNNING/PENDING/INTERRUPTED scan)
}

func (f *fakeStatusLister) ListByStatusSimple(ctx context.Context, status string, playerID *int) ([]persistence.ContainerSummary, error) {
	f.seen = append(f.seen, status)
	if f.err != nil {
		return nil, f.err
	}
	return f.byStatus[status], nil
}

func lister(byStatus map[string][]persistence.ContainerSummary) *fakeStatusLister {
	return &fakeStatusLister{byStatus: byStatus}
}

func bootstrapRow(id string) persistence.ContainerSummary {
	return persistence.ContainerSummary{ID: id, ContainerType: string(container.ContainerTypeBootstrapCoordinator), Status: "RUNNING"}
}

// st-drm.14 — a `workflow bootstrap` launch for a player whose coordinator is ALREADY active must be
// rejected naming the existing container. "Active" spans running / starting / pending-recovery so the
// guard also rejects a fresh launch that races the daemon's restart-recovery window (the live double-
// spend: a RECOVERED coordinator + a fresh one, two brains draining one treasury).

func TestActiveBootstrapContainerID_RunningRecoveredCountsAsActive(t *testing.T) {
	// A recovered coordinator is RUNNING — it must count as active and be named.
	l := lister(map[string][]persistence.ContainerSummary{
		string(container.ContainerStatusRunning): {bootstrapRow("bootstrap-player-1-53a85cc8")},
	})
	id, err := activeBootstrapContainerID(context.Background(), l, 1)
	require.NoError(t, err)
	require.Equal(t, "bootstrap-player-1-53a85cc8", id)
}

func TestActiveBootstrapContainerID_PendingRecoveryCountsAsActive(t *testing.T) {
	// INTERRUPTED = running-when-daemon-stopped, pending recovery. A fresh launch racing that window
	// must still be rejected.
	l := lister(map[string][]persistence.ContainerSummary{
		string(container.ContainerStatusInterrupted): {bootstrapRow("bootstrap-player-1-interrupted")},
	})
	id, err := activeBootstrapContainerID(context.Background(), l, 1)
	require.NoError(t, err)
	require.Equal(t, "bootstrap-player-1-interrupted", id)
}

func TestActiveBootstrapContainerID_PendingCountsAsActive(t *testing.T) {
	l := lister(map[string][]persistence.ContainerSummary{
		string(container.ContainerStatusPending): {bootstrapRow("bootstrap-player-1-pending")},
	})
	id, err := activeBootstrapContainerID(context.Background(), l, 1)
	require.NoError(t, err)
	require.Equal(t, "bootstrap-player-1-pending", id)
}

func TestActiveBootstrapContainerID_NoActiveBootstrap_AllowsLaunch(t *testing.T) {
	// A different container type, and a COMPLETED/STOPPED bootstrap (absent from the active-status
	// maps), must NOT block a fresh launch — the coordinator finished, re-launch is legitimate.
	l := lister(map[string][]persistence.ContainerSummary{
		string(container.ContainerStatusRunning): {
			{ID: "fleet-autosizer-1", ContainerType: string(container.ContainerTypeFleetAutosizer), Status: "RUNNING"},
		},
	})
	id, err := activeBootstrapContainerID(context.Background(), l, 1)
	require.NoError(t, err)
	require.Equal(t, "", id)
	// It must scan all three active statuses before concluding "none".
	require.Equal(t, []string{
		string(container.ContainerStatusRunning),
		string(container.ContainerStatusPending),
		string(container.ContainerStatusInterrupted),
	}, l.seen)
}

func TestActiveBootstrapContainerID_RepoError_FailsClosed(t *testing.T) {
	// A repo read miss must surface as an error (the caller fails CLOSED — rejects the launch rather
	// than risk a second concurrent coordinator).
	l := &fakeStatusLister{err: errors.New("db down")}
	_, err := activeBootstrapContainerID(context.Background(), l, 1)
	require.Error(t, err)
}
