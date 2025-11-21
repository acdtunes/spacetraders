package coordination

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/application/mining/ports"
)

// ChannelTransportCoordinator implements TransportCoordinator using Go channels.
// This is the default in-process implementation for coordinating mining and transport workers.
type ChannelTransportCoordinator struct {
	// Miner → Coordinator: Miner sends its symbol to request transport
	minerRequestChan chan string

	// Coordinator → Miner: Coordinator sends assigned transport symbol (per-miner channels)
	minerAssignChans map[string]chan string

	// Transport → Coordinator: Transport sends its symbol to signal availability
	transportAvailabilityChan chan string

	// Coordinator → Transport: Coordinator signals cargo was transferred (per-transport channels)
	transportCargoReceivedChans map[string]chan struct{}

	// Miner → Coordinator: Miner signals transfer completion
	transferCompleteChan chan TransferComplete

	mu       sync.RWMutex
	shutdown bool
}

// TransferComplete signals that a miner has completed transferring cargo to a transport
type TransferComplete struct {
	MinerSymbol     string
	TransportSymbol string
}

// NewChannelTransportCoordinator creates a new channel-based transport coordinator.
// minerSymbols: List of miner ship symbols participating in the operation
// transportSymbols: List of transport ship symbols participating in the operation
func NewChannelTransportCoordinator(minerSymbols []string, transportSymbols []string) *ChannelTransportCoordinator {
	// Create per-miner assignment channels
	minerAssignChans := make(map[string]chan string)
	for _, miner := range minerSymbols {
		minerAssignChans[miner] = make(chan string, 1) // Buffered to prevent blocking coordinator
	}

	// Create per-transport cargo notification channels
	transportCargoReceivedChans := make(map[string]chan struct{})
	for _, transport := range transportSymbols {
		transportCargoReceivedChans[transport] = make(chan struct{}, 1) // Buffered to prevent blocking coordinator
	}

	return &ChannelTransportCoordinator{
		minerRequestChan:            make(chan string, len(minerSymbols)),
		minerAssignChans:            minerAssignChans,
		transportAvailabilityChan:   make(chan string, len(transportSymbols)),
		transportCargoReceivedChans: transportCargoReceivedChans,
		transferCompleteChan:        make(chan TransferComplete, len(minerSymbols)),
		shutdown:                    false,
	}
}

// RequestTransport is called by a mining worker to request a transport ship.
// Blocks until a transport is assigned or context is cancelled.
func (c *ChannelTransportCoordinator) RequestTransport(ctx context.Context, minerSymbol string) (string, error) {
	c.mu.RLock()
	if c.shutdown {
		c.mu.RUnlock()
		return "", fmt.Errorf("coordinator is shutdown")
	}
	assignChan := c.minerAssignChans[minerSymbol]
	c.mu.RUnlock()

	if assignChan == nil {
		return "", fmt.Errorf("miner %s not registered with coordinator", minerSymbol)
	}

	// Send request
	select {
	case c.minerRequestChan <- minerSymbol:
		// Request sent
	case <-ctx.Done():
		return "", ctx.Err()
	}

	// Wait for assignment
	select {
	case transportSymbol := <-assignChan:
		return transportSymbol, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// NotifyTransferComplete is called by a mining worker after completing cargo transfer.
func (c *ChannelTransportCoordinator) NotifyTransferComplete(ctx context.Context, minerSymbol string, transportSymbol string) error {
	c.mu.RLock()
	if c.shutdown {
		c.mu.RUnlock()
		return fmt.Errorf("coordinator is shutdown")
	}
	c.mu.RUnlock()

	select {
	case c.transferCompleteChan <- TransferComplete{
		MinerSymbol:     minerSymbol,
		TransportSymbol: transportSymbol,
	}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SignalAvailability is called by a transport worker to signal it is ready at the asteroid.
// This method also blocks waiting for cargo to be received.
func (c *ChannelTransportCoordinator) SignalAvailability(ctx context.Context, transportSymbol string) error {
	c.mu.RLock()
	if c.shutdown {
		c.mu.RUnlock()
		return fmt.Errorf("coordinator is shutdown")
	}
	cargoReceivedChan := c.transportCargoReceivedChans[transportSymbol]
	c.mu.RUnlock()

	if cargoReceivedChan == nil {
		return fmt.Errorf("transport %s not registered with coordinator", transportSymbol)
	}

	// Signal availability
	select {
	case c.transportAvailabilityChan <- transportSymbol:
		// Availability signaled
	case <-ctx.Done():
		return ctx.Err()
	}

	// Wait for cargo received notification
	select {
	case <-cargoReceivedChan:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// NotifyCargoReceived is called by the coordinator to notify a transport that cargo was transferred.
func (c *ChannelTransportCoordinator) NotifyCargoReceived(ctx context.Context, transportSymbol string) error {
	c.mu.RLock()
	cargoReceivedChan := c.transportCargoReceivedChans[transportSymbol]
	c.mu.RUnlock()

	if cargoReceivedChan == nil {
		return fmt.Errorf("transport %s not registered with coordinator", transportSymbol)
	}

	select {
	case cargoReceivedChan <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Shutdown gracefully stops the coordinator, closing all communication channels.
func (c *ChannelTransportCoordinator) Shutdown() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.shutdown {
		return nil
	}

	c.shutdown = true

	// Close all channels
	close(c.minerRequestChan)
	close(c.transportAvailabilityChan)
	close(c.transferCompleteChan)

	for _, ch := range c.minerAssignChans {
		close(ch)
	}

	for _, ch := range c.transportCargoReceivedChans {
		close(ch)
	}

	return nil
}

// GetChannels returns the raw channels for coordinator consumption.
// This method is used by the RunCoordinatorHandler to get channels for its main loop.
// IMPORTANT: This is only for the coordinator's internal use - workers should use the interface methods.
func (c *ChannelTransportCoordinator) GetChannels() (
	minerRequestChan <-chan string,
	transportAvailabilityChan <-chan string,
	transferCompleteChan <-chan TransferComplete,
	minerAssignChans map[string]chan<- string,
) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Convert minerAssignChans to send-only for external use
	sendOnlyMinerChans := make(map[string]chan<- string)
	for k, v := range c.minerAssignChans {
		sendOnlyMinerChans[k] = v
	}

	return c.minerRequestChan,
		c.transportAvailabilityChan,
		c.transferCompleteChan,
		sendOnlyMinerChans
}

// Ensure ChannelTransportCoordinator implements TransportCoordinator
var _ ports.TransportCoordinator = (*ChannelTransportCoordinator)(nil)
