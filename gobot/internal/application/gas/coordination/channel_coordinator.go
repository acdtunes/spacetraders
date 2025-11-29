package coordination

import (
	"context"
	"fmt"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/application/gas/ports"
)

// ChannelTransportCoordinator implements TransportCoordinator using Go channels.
// This is the default in-process implementation for coordinating siphon and transport workers.
type ChannelTransportCoordinator struct {
	// Siphon → Coordinator: Siphon ship sends its symbol to request transport
	siphonRequestChan chan string

	// Coordinator → Siphon: Coordinator sends assigned transport symbol (per-siphon channels)
	siphonAssignChans map[string]chan string

	// Transport → Coordinator: Transport sends its symbol to signal availability
	transportAvailabilityChan chan string

	// Coordinator → Transport: Coordinator signals cargo was transferred (per-transport channels)
	transportCargoReceivedChans map[string]chan struct{}

	// Siphon → Coordinator: Siphon signals transfer completion
	transferCompleteChan chan TransferComplete

	mu       sync.RWMutex
	shutdown bool
}

// TransferComplete signals that a siphon ship has completed transferring cargo to a transport
type TransferComplete struct {
	SiphonSymbol    string
	TransportSymbol string
}

// NewChannelTransportCoordinator creates a new channel-based transport coordinator.
// siphonSymbols: List of siphon ship symbols participating in the operation
// transportSymbols: List of transport ship symbols participating in the operation
func NewChannelTransportCoordinator(siphonSymbols []string, transportSymbols []string) *ChannelTransportCoordinator {
	// Create per-siphon assignment channels
	siphonAssignChans := make(map[string]chan string)
	for _, siphon := range siphonSymbols {
		siphonAssignChans[siphon] = make(chan string, 1) // Buffered to prevent blocking coordinator
	}

	// Create per-transport cargo notification channels
	transportCargoReceivedChans := make(map[string]chan struct{})
	for _, transport := range transportSymbols {
		transportCargoReceivedChans[transport] = make(chan struct{}, 1) // Buffered to prevent blocking coordinator
	}

	return &ChannelTransportCoordinator{
		siphonRequestChan:           make(chan string, len(siphonSymbols)),
		siphonAssignChans:           siphonAssignChans,
		transportAvailabilityChan:   make(chan string, len(transportSymbols)),
		transportCargoReceivedChans: transportCargoReceivedChans,
		transferCompleteChan:        make(chan TransferComplete, len(siphonSymbols)),
		shutdown:                    false,
	}
}

// RequestTransport is called by a siphon worker to request a transport ship.
// Blocks until a transport is assigned or context is cancelled.
func (c *ChannelTransportCoordinator) RequestTransport(ctx context.Context, siphonSymbol string) (string, error) {
	c.mu.RLock()
	if c.shutdown {
		c.mu.RUnlock()
		return "", fmt.Errorf("coordinator is shutdown")
	}
	assignChan := c.siphonAssignChans[siphonSymbol]
	c.mu.RUnlock()

	if assignChan == nil {
		return "", fmt.Errorf("siphon %s not registered with coordinator", siphonSymbol)
	}

	// Send request
	select {
	case c.siphonRequestChan <- siphonSymbol:
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

// NotifyTransferComplete is called by a siphon worker after completing cargo transfer.
func (c *ChannelTransportCoordinator) NotifyTransferComplete(ctx context.Context, siphonSymbol string, transportSymbol string) error {
	c.mu.RLock()
	if c.shutdown {
		c.mu.RUnlock()
		return fmt.Errorf("coordinator is shutdown")
	}
	c.mu.RUnlock()

	select {
	case c.transferCompleteChan <- TransferComplete{
		SiphonSymbol:    siphonSymbol,
		TransportSymbol: transportSymbol,
	}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SignalAvailability is called by a transport worker to signal it is ready at the gas giant.
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
	close(c.siphonRequestChan)
	close(c.transportAvailabilityChan)
	close(c.transferCompleteChan)

	for _, ch := range c.siphonAssignChans {
		close(ch)
	}

	for _, ch := range c.transportCargoReceivedChans {
		close(ch)
	}

	return nil
}

// GetChannels returns the raw channels for coordinator consumption.
// This method is used by the RunGasCoordinatorHandler to get channels for its main loop.
// IMPORTANT: This is only for the coordinator's internal use - workers should use the interface methods.
func (c *ChannelTransportCoordinator) GetChannels() (
	siphonRequestChan <-chan string,
	transportAvailabilityChan <-chan string,
	transferCompleteChan <-chan TransferComplete,
	siphonAssignChans map[string]chan<- string,
) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Convert siphonAssignChans to send-only for external use
	sendOnlySiphonChans := make(map[string]chan<- string)
	for k, v := range c.siphonAssignChans {
		sendOnlySiphonChans[k] = v
	}

	return c.siphonRequestChan,
		c.transportAvailabilityChan,
		c.transferCompleteChan,
		sendOnlySiphonChans
}

// Ensure ChannelTransportCoordinator implements TransportCoordinator
var _ ports.TransportCoordinator = (*ChannelTransportCoordinator)(nil)
