package grpc

import (
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

var (
	workerPublisherMu      sync.RWMutex
	defaultWorkerPublisher navigation.ShipEventPublisher
)

// SetDefaultWorkerEventPublisher wires the ship event bus for worker-completed
// events. Called once from the daemon main. Individual runners may still carry
// an instance publisher via SetEventPublisher; this default covers the ~17
// construction sites that never wired one — without it, coordinators wait on
// a channel nobody publishes to and fall back to a ~53-minute timeout.
func SetDefaultWorkerEventPublisher(p navigation.ShipEventPublisher) {
	workerPublisherMu.Lock()
	defer workerPublisherMu.Unlock()
	defaultWorkerPublisher = p
}

// resolveWorkerPublisher prefers the runner's instance publisher, falling back
// to the package default.
func resolveWorkerPublisher(instance navigation.ShipEventPublisher) navigation.ShipEventPublisher {
	if instance != nil {
		return instance
	}
	workerPublisherMu.RLock()
	defer workerPublisherMu.RUnlock()
	return defaultWorkerPublisher
}
