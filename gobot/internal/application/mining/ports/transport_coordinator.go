package ports

import "context"

// TransportCoordinator abstracts the coordination mechanism for mining transport operations.
// This interface decouples workers from the specific concurrency implementation (channels, queues, etc.)
//
// The transport coordinator manages:
// 1. Miners requesting transport ships
// 2. Assignment of available transports to miners
// 3. Notification of cargo transfer completion
// 4. Signaling transport availability
type TransportCoordinator interface {
	// RequestTransport is called by a mining worker to request a transport ship.
	// The minerSymbol identifies the requesting miner.
	// Blocks until a transport is assigned or context is cancelled.
	// Returns the assigned transport ship symbol.
	RequestTransport(ctx context.Context, minerSymbol string) (transportSymbol string, err error)

	// NotifyTransferComplete is called by a mining worker after completing cargo transfer.
	// The coordinator uses this to update transport cargo levels and notify the transport.
	NotifyTransferComplete(ctx context.Context, minerSymbol string, transportSymbol string) error

	// SignalAvailability is called by a transport worker to signal it is ready at the asteroid.
	// The transport will wait for cargo after signaling availability.
	// Blocks until cargo is received or context is cancelled.
	SignalAvailability(ctx context.Context, transportSymbol string) error

	// NotifyCargoReceived is called by the coordinator to notify a transport that cargo was transferred.
	// The transport can then check its cargo level and potentially trigger a selling route.
	NotifyCargoReceived(ctx context.Context, transportSymbol string) error

	// Shutdown gracefully stops the coordinator, closing all communication channels.
	Shutdown() error
}
