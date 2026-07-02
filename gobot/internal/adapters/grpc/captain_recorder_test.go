package grpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

type fakeRecorder struct{ events []*captain.Event }

func (f *fakeRecorder) Record(_ context.Context, e *captain.Event) error {
	f.events = append(f.events, e)
	return nil
}

func TestRecordCaptainEventNoopWhenUnset(t *testing.T) {
	SetCaptainEventRecorder(nil)
	// must not panic
	recordCaptainEvent(captain.EventWorkflowFailed, "SHIP-1", 1, map[string]any{"error": "x"})
}

func TestRecordCaptainEventForwards(t *testing.T) {
	f := &fakeRecorder{}
	SetCaptainEventRecorder(f)
	defer SetCaptainEventRecorder(nil)

	recordCaptainEvent(captain.EventWorkflowFinished, "SHIP-2", 7, map[string]any{"container_id": "c-1"})

	require.Len(t, f.events, 1)
	require.Equal(t, captain.EventWorkflowFinished, f.events[0].Type)
	require.Equal(t, "SHIP-2", f.events[0].Ship)
	require.Equal(t, 7, f.events[0].PlayerID)
	require.Contains(t, f.events[0].Payload, "c-1")
}
