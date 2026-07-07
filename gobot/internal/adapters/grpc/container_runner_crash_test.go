package grpc

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// noopLogRepo is a no-op ContainerLogRepository so ContainerRunner.Log's async
// persistence goroutine has a non-nil target during tests.
type noopLogRepo struct{}

func (noopLogRepo) Log(_ context.Context, _ string, _ int, _, _ string, _ map[string]interface{}) error {
	return nil
}

func (noopLogRepo) GetLogs(_ context.Context, _ string, _ int, _ int, _ *string, _ *time.Time) ([]persistence.ContainerLogEntry, error) {
	return nil, nil
}

func (noopLogRepo) GetLogsWithOffset(_ context.Context, _ string, _ int, _, _ int, _ *string, _ *time.Time) ([]persistence.ContainerLogEntry, error) {
	return nil, nil
}

// newCrashTestRunner builds a minimal running ContainerRunner suitable for
// exercising the error/crash paths. The container is Started so Fail() is valid.
func newCrashTestRunner(t *testing.T, containerID string) *ContainerRunner {
	t.Helper()
	entity := container.NewContainer(containerID, container.ContainerTypeContractWorkflow, 2, -1, nil, nil, nil)
	require.NoError(t, entity.Start())
	return NewContainerRunner(entity, nil, nil, noopLogRepo{}, nil, nil, nil)
}

func countEvents(events []*captain.Event, target captain.EventType) int {
	n := 0
	for _, e := range events {
		if e.Type == target {
			n++
		}
	}
	return n
}

// A retryable iteration error must NOT be counted as a container crash.
// handleError runs on every failed iteration (before the restart decision), so
// emitting container.crashed there over-counts crashes: a worker that retries and
// recovers is reported as crashed. The strategic crash event belongs on the true
// (unrecoverable) exit path only.
func TestHandleErrorDoesNotEmitContainerCrashed(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	r := newCrashTestRunner(t, "contract-work-TORWIND-3-abc")
	r.handleError(fmt.Errorf("route segment failed: API 4203 insufficient fuel"))

	require.Equal(t, 0, countEvents(rec.events, captain.EventContainerCrashed),
		"handleError must not emit container.crashed for a retryable error")
}

// A true (unrecoverable) crash must (a) record exactly one container.crashed event
// whose payload carries the container id and underlying error, and (b) log exactly
// one ERROR line carrying the same signature. This is the observability guarantee
// the bug demands: a crashing container surfaces its exit cause above INFO.
func TestRecordCrashSurfacesSignatureAndEmitsOneEvent(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	const cid = "contract-work-TORWIND-3-abc"
	r := newCrashTestRunner(t, cid)
	crashErr := fmt.Errorf("route segment failed: API 4203 insufficient fuel")

	r.recordCrash(crashErr)

	// (a) exactly one crash event, payload carries id + error signature
	require.Equal(t, 1, countEvents(rec.events, captain.EventContainerCrashed))
	var crash *captain.Event
	for _, e := range rec.events {
		if e.Type == captain.EventContainerCrashed {
			crash = e
		}
	}
	require.NotNil(t, crash)
	require.Contains(t, crash.Payload, cid)
	require.Contains(t, crash.Payload, "4203")

	// (b) exactly one ERROR log line carrying id + error + a greppable crash marker
	var errLines []string
	for _, l := range r.GetLogs(nil, nil) {
		if l.Level == "ERROR" {
			errLines = append(errLines, l.Message)
		}
	}
	require.Len(t, errLines, 1, "recordCrash must log exactly one ERROR line")
	require.Contains(t, errLines[0], cid)
	require.Contains(t, errLines[0], "4203")
	require.Contains(t, strings.ToLower(errLines[0]), "crash")
}
