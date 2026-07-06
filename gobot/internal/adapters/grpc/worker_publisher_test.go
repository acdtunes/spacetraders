package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

type fakePublisher struct {
	events []navigation.WorkerCompletedEvent
}

func (f *fakePublisher) PublishArrived(string, shared.PlayerID, string, navigation.NavStatus) {}
func (f *fakePublisher) PublishWorkerCompleted(e navigation.WorkerCompletedEvent) {
	f.events = append(f.events, e)
}
func (f *fakePublisher) PublishTasksBecameReady(navigation.TasksBecameReadyEvent)     {}
func (f *fakePublisher) PublishTransportRequested(navigation.TransportRequestedEvent) {}
func (f *fakePublisher) PublishTransferCompleted(navigation.TransferCompletedEvent)   {}

func TestResolveWorkerPublisherFallsBackToDefault(t *testing.T) {
	SetDefaultWorkerEventPublisher(nil)
	require.Nil(t, resolveWorkerPublisher(nil), "no instance, no default")

	def := &fakePublisher{}
	SetDefaultWorkerEventPublisher(def)
	defer SetDefaultWorkerEventPublisher(nil)
	require.Equal(t, navigation.ShipEventPublisher(def), resolveWorkerPublisher(nil),
		"nil instance falls back to package default")

	inst := &fakePublisher{}
	require.Equal(t, navigation.ShipEventPublisher(inst), resolveWorkerPublisher(inst),
		"instance publisher wins over default")
}
