package ship

import (
	"strconv"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ShipEventBus provides pub/sub for ship and container events.
// Implements both ShipEventPublisher and ShipEventSubscriber from domain ports.
// Thread-safe, supports multiple subscribers per topic.
// Uses buffered channels to prevent blocking publishers.
type ShipEventBus struct {
	mu sync.RWMutex
	// arrivedSubscribers[shipSymbol] = []channels
	arrivedSubscribers map[string][]chan navigation.ShipArrivedEvent

	// workerCompletedSubscribers[coordinatorID] = []channels
	workerCompletedSubscribers map[string][]chan navigation.WorkerCompletedEvent

	// tasksBecameReadySubscribers[playerID as string] = []channels
	tasksBecameReadySubscribers map[string][]chan navigation.TasksBecameReadyEvent

	// transportRequestedSubscribers[playerID as string] = []channels
	transportRequestedSubscribers map[string][]chan navigation.TransportRequestedEvent

	// transferCompletedSubscribers[playerID as string] = []channels
	transferCompletedSubscribers map[string][]chan navigation.TransferCompletedEvent
}

// Compile-time interface checks
var (
	_ navigation.ShipEventPublisher  = (*ShipEventBus)(nil)
	_ navigation.ShipEventSubscriber = (*ShipEventBus)(nil)
)

// NewShipEventBus creates a new event bus for ship and container events
func NewShipEventBus() *ShipEventBus {
	return &ShipEventBus{
		arrivedSubscribers:            make(map[string][]chan navigation.ShipArrivedEvent),
		workerCompletedSubscribers:    make(map[string][]chan navigation.WorkerCompletedEvent),
		tasksBecameReadySubscribers:   make(map[string][]chan navigation.TasksBecameReadyEvent),
		transportRequestedSubscribers: make(map[string][]chan navigation.TransportRequestedEvent),
		transferCompletedSubscribers:  make(map[string][]chan navigation.TransferCompletedEvent),
	}
}

// PublishArrived publishes an ARRIVED event when a ship transitions out of IN_TRANSIT.
// Implements ShipEventPublisher interface.
func (b *ShipEventBus) PublishArrived(shipSymbol string, playerID shared.PlayerID, location string, status navigation.NavStatus) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	event := navigation.ShipArrivedEvent{
		ShipSymbol: shipSymbol,
		PlayerID:   playerID,
		Location:   location,
		Status:     status,
	}

	channels := b.arrivedSubscribers[shipSymbol]
	for _, ch := range channels {
		// Non-blocking send - skip if channel buffer is full
		select {
		case ch <- event:
			// Event delivered
		default:
			// Channel full, subscriber is slow - skip to prevent blocking
		}
	}
}

// SubscribeArrived subscribes to ARRIVED events for a specific ship.
// Returns a channel that receives events. Caller must UnsubscribeArrived when done.
// Implements ShipEventSubscriber interface.
func (b *ShipEventBus) SubscribeArrived(shipSymbol string) <-chan navigation.ShipArrivedEvent {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Create buffered channel (size 1 prevents blocking on publish)
	ch := make(chan navigation.ShipArrivedEvent, 1)
	b.arrivedSubscribers[shipSymbol] = append(b.arrivedSubscribers[shipSymbol], ch)

	return ch
}

// UnsubscribeArrived removes a subscription. Closes the channel.
// Implements ShipEventSubscriber interface.
func (b *ShipEventBus) UnsubscribeArrived(shipSymbol string, ch <-chan navigation.ShipArrivedEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	channels := b.arrivedSubscribers[shipSymbol]
	for i, c := range channels {
		// Compare channel pointers
		if c == ch {
			// Close the channel
			close(c)
			// Remove from slice (order doesn't matter, so swap with last)
			channels[i] = channels[len(channels)-1]
			b.arrivedSubscribers[shipSymbol] = channels[:len(channels)-1]
			break
		}
	}

	// Cleanup empty maps
	if len(b.arrivedSubscribers[shipSymbol]) == 0 {
		delete(b.arrivedSubscribers, shipSymbol)
	}
}

// SubscriberCount returns the number of subscribers for a specific ship.
// Useful for testing and monitoring.
func (b *ShipEventBus) SubscriberCount(shipSymbol string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.arrivedSubscribers[shipSymbol])
}

// TotalSubscriberCount returns the total number of active subscriptions.
// Useful for monitoring.
func (b *ShipEventBus) TotalSubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()

	total := 0
	for _, channels := range b.arrivedSubscribers {
		total += len(channels)
	}
	for _, channels := range b.workerCompletedSubscribers {
		total += len(channels)
	}
	for _, channels := range b.tasksBecameReadySubscribers {
		total += len(channels)
	}
	for _, channels := range b.transportRequestedSubscribers {
		total += len(channels)
	}
	for _, channels := range b.transferCompletedSubscribers {
		total += len(channels)
	}
	return total
}

// ============================================================================
// Worker Completion Events
// ============================================================================

// PublishWorkerCompleted publishes a worker completion event.
// Coordinators subscribe by their container ID to receive completion signals.
// Implements ShipEventPublisher interface.
func (b *ShipEventBus) PublishWorkerCompleted(event navigation.WorkerCompletedEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	channels := b.workerCompletedSubscribers[event.CoordinatorID]
	for _, ch := range channels {
		// Non-blocking send - skip if channel buffer is full
		select {
		case ch <- event:
			// Event delivered
		default:
			// Channel full, subscriber is slow - skip to prevent blocking
		}
	}
}

// SubscribeWorkerCompleted subscribes to worker completion events for a coordinator.
// Returns a channel that receives events. Caller must UnsubscribeWorkerCompleted when done.
// Implements ShipEventSubscriber interface.
func (b *ShipEventBus) SubscribeWorkerCompleted(coordinatorID string) <-chan navigation.WorkerCompletedEvent {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Create buffered channel (size 10 allows multiple completions without blocking)
	ch := make(chan navigation.WorkerCompletedEvent, 10)
	b.workerCompletedSubscribers[coordinatorID] = append(b.workerCompletedSubscribers[coordinatorID], ch)

	return ch
}

// UnsubscribeWorkerCompleted removes a worker completion subscription. Closes the channel.
// Implements ShipEventSubscriber interface.
func (b *ShipEventBus) UnsubscribeWorkerCompleted(coordinatorID string, ch <-chan navigation.WorkerCompletedEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	channels := b.workerCompletedSubscribers[coordinatorID]
	for i, c := range channels {
		if c == ch {
			close(c)
			channels[i] = channels[len(channels)-1]
			b.workerCompletedSubscribers[coordinatorID] = channels[:len(channels)-1]
			break
		}
	}

	if len(b.workerCompletedSubscribers[coordinatorID]) == 0 {
		delete(b.workerCompletedSubscribers, coordinatorID)
	}
}

// ============================================================================
// Tasks Became Ready Events
// ============================================================================

// PublishTasksBecameReady publishes a tasks ready event.
// Used by SupplyMonitor to notify coordinators when tasks are ready for assignment.
// Implements ShipEventPublisher interface.
func (b *ShipEventBus) PublishTasksBecameReady(event navigation.TasksBecameReadyEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	key := strconv.Itoa(event.PlayerID)
	channels := b.tasksBecameReadySubscribers[key]
	for _, ch := range channels {
		select {
		case ch <- event:
		default:
		}
	}
}

// SubscribeTasksBecameReady subscribes to task ready events for a player.
// Returns a channel that receives events. Caller must UnsubscribeTasksBecameReady when done.
// Implements ShipEventSubscriber interface.
func (b *ShipEventBus) SubscribeTasksBecameReady(playerID int) <-chan navigation.TasksBecameReadyEvent {
	b.mu.Lock()
	defer b.mu.Unlock()

	key := strconv.Itoa(playerID)
	ch := make(chan navigation.TasksBecameReadyEvent, 10)
	b.tasksBecameReadySubscribers[key] = append(b.tasksBecameReadySubscribers[key], ch)

	return ch
}

// UnsubscribeTasksBecameReady removes a task ready subscription. Closes the channel.
// Implements ShipEventSubscriber interface.
func (b *ShipEventBus) UnsubscribeTasksBecameReady(playerID int, ch <-chan navigation.TasksBecameReadyEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	key := strconv.Itoa(playerID)
	channels := b.tasksBecameReadySubscribers[key]
	for i, c := range channels {
		if c == ch {
			close(c)
			channels[i] = channels[len(channels)-1]
			b.tasksBecameReadySubscribers[key] = channels[:len(channels)-1]
			break
		}
	}

	if len(b.tasksBecameReadySubscribers[key]) == 0 {
		delete(b.tasksBecameReadySubscribers, key)
	}
}

// ============================================================================
// Transport Requested Events
// ============================================================================

// PublishTransportRequested publishes a transport request event.
// Used by siphon workers to request transport assignment.
// Implements ShipEventPublisher interface.
func (b *ShipEventBus) PublishTransportRequested(event navigation.TransportRequestedEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	key := strconv.Itoa(event.PlayerID)
	channels := b.transportRequestedSubscribers[key]
	for _, ch := range channels {
		select {
		case ch <- event:
		default:
		}
	}
}

// SubscribeTransportRequested subscribes to transport request events for a player.
// Returns a channel that receives events. Caller must UnsubscribeTransportRequested when done.
// Implements ShipEventSubscriber interface.
func (b *ShipEventBus) SubscribeTransportRequested(playerID int) <-chan navigation.TransportRequestedEvent {
	b.mu.Lock()
	defer b.mu.Unlock()

	key := strconv.Itoa(playerID)
	ch := make(chan navigation.TransportRequestedEvent, 10)
	b.transportRequestedSubscribers[key] = append(b.transportRequestedSubscribers[key], ch)

	return ch
}

// UnsubscribeTransportRequested removes a transport request subscription. Closes the channel.
// Implements ShipEventSubscriber interface.
func (b *ShipEventBus) UnsubscribeTransportRequested(playerID int, ch <-chan navigation.TransportRequestedEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	key := strconv.Itoa(playerID)
	channels := b.transportRequestedSubscribers[key]
	for i, c := range channels {
		if c == ch {
			close(c)
			channels[i] = channels[len(channels)-1]
			b.transportRequestedSubscribers[key] = channels[:len(channels)-1]
			break
		}
	}

	if len(b.transportRequestedSubscribers[key]) == 0 {
		delete(b.transportRequestedSubscribers, key)
	}
}

// ============================================================================
// Transfer Completed Events
// ============================================================================

// PublishTransferCompleted publishes a transfer completion event.
// Used by transport workers to notify when cargo transfer is done.
// Implements ShipEventPublisher interface.
func (b *ShipEventBus) PublishTransferCompleted(event navigation.TransferCompletedEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	key := strconv.Itoa(event.PlayerID)
	channels := b.transferCompletedSubscribers[key]
	for _, ch := range channels {
		select {
		case ch <- event:
		default:
		}
	}
}

// SubscribeTransferCompleted subscribes to transfer completion events for a player.
// Returns a channel that receives events. Caller must UnsubscribeTransferCompleted when done.
// Implements ShipEventSubscriber interface.
func (b *ShipEventBus) SubscribeTransferCompleted(playerID int) <-chan navigation.TransferCompletedEvent {
	b.mu.Lock()
	defer b.mu.Unlock()

	key := strconv.Itoa(playerID)
	ch := make(chan navigation.TransferCompletedEvent, 10)
	b.transferCompletedSubscribers[key] = append(b.transferCompletedSubscribers[key], ch)

	return ch
}

// UnsubscribeTransferCompleted removes a transfer completion subscription. Closes the channel.
// Implements ShipEventSubscriber interface.
func (b *ShipEventBus) UnsubscribeTransferCompleted(playerID int, ch <-chan navigation.TransferCompletedEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	key := strconv.Itoa(playerID)
	channels := b.transferCompletedSubscribers[key]
	for i, c := range channels {
		if c == ch {
			close(c)
			channels[i] = channels[len(channels)-1]
			b.transferCompletedSubscribers[key] = channels[:len(channels)-1]
			break
		}
	}

	if len(b.transferCompletedSubscribers[key]) == 0 {
		delete(b.transferCompletedSubscribers, key)
	}
}
