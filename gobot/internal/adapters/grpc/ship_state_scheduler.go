package grpc

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ClockDriftBuffer accounts for slight time differences between API server and local clock.
// Ensures we never act before the API considers the ship arrived.
const ClockDriftBuffer = 1 * time.Second

// SweeperInterval is how often the background sweeper checks for stuck ships.
// This catches ships that slip through due to failed saves, timeouts, or clock drift.
// With event-based arrival handling, the sweeper is a safety net, not the primary mechanism.
const SweeperInterval = 60 * time.Second

// ShipStateScheduler manages timers for ship state transitions.
// Uses time.AfterFunc to schedule precise transitions at exact API-provided timestamps.
// Zero CPU usage between events (no polling).
// Also runs a background sweeper to catch any ships that slip through due to failures.
// Publishes events via ShipEventPublisher when state transitions occur.
type ShipStateScheduler struct {
	shipRepo       navigation.ShipRepository
	clock          shared.Clock
	eventPublisher navigation.ShipEventPublisher
	timers         map[string]*time.Timer // key: shipSymbol or shipSymbol:cooldown
	mu             sync.Mutex
	stopCh         chan struct{} // signals sweeper goroutine to stop
}

// NewShipStateScheduler creates a new scheduler for ship state transitions.
// eventPublisher is optional - if nil, no events will be published.
func NewShipStateScheduler(shipRepo navigation.ShipRepository, clock shared.Clock, eventPublisher navigation.ShipEventPublisher) *ShipStateScheduler {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &ShipStateScheduler{
		shipRepo:       shipRepo,
		clock:          clock,
		eventPublisher: eventPublisher,
		timers:         make(map[string]*time.Timer),
		stopCh:         make(chan struct{}),
	}
}

// ScheduleArrival schedules a timer to transition ship from IN_TRANSIT to IN_ORBIT
func (s *ShipStateScheduler) ScheduleArrival(ship *navigation.Ship) {
	if ship.ArrivalTime() == nil {
		return
	}

	delay := time.Until(*ship.ArrivalTime())
	if delay < 0 {
		delay = 0 // Already past, execute immediately
	}
	delay += ClockDriftBuffer // Buffer for clock drift between API server and local

	s.mu.Lock()
	defer s.mu.Unlock()

	// Cancel existing timer if any
	timerKey := ship.ShipSymbol()
	if existing, ok := s.timers[timerKey]; ok {
		existing.Stop()
	}

	symbol := ship.ShipSymbol()
	playerID := ship.PlayerID()

	s.timers[timerKey] = time.AfterFunc(delay, func() {
		s.handleArrival(symbol, playerID)
	})

	fmt.Printf("Scheduled arrival for %s in %v\n", symbol, delay)
}

// handleArrival processes a ship arrival
func (s *ShipStateScheduler) handleArrival(symbol string, playerID shared.PlayerID) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Re-fetch ship to get latest state
	freshShip, err := s.shipRepo.FindBySymbol(ctx, symbol, playerID)
	if err != nil {
		fmt.Printf("Warning: Failed to fetch ship %s for arrival: %v\n", symbol, err)
		return
	}

	// Only transition if still in transit
	if !freshShip.IsInTransit() {
		return
	}

	if err := freshShip.Arrive(); err != nil {
		fmt.Printf("Warning: Failed to transition ship %s to orbit: %v\n", symbol, err)
		return
	}

	freshShip.ClearArrivalTime()

	if err := s.shipRepo.Save(ctx, freshShip); err != nil {
		fmt.Printf("Warning: Failed to save ship %s after arrival: %v\n", symbol, err)
	} else {
		fmt.Printf("Ship %s arrived at %s\n", symbol, freshShip.CurrentLocation().Symbol)

		// Publish ARRIVED event to notify waiting containers
		if s.eventPublisher != nil {
			s.eventPublisher.PublishArrived(
				symbol,
				playerID,
				freshShip.CurrentLocation().Symbol,
				freshShip.NavStatus(),
			)
		}
	}

	// Cleanup timer reference
	s.mu.Lock()
	delete(s.timers, symbol)
	s.mu.Unlock()
}

// ScheduleCooldownClear schedules a timer to clear cooldown
func (s *ShipStateScheduler) ScheduleCooldownClear(ship *navigation.Ship) {
	if ship.CooldownExpiration() == nil {
		return
	}

	delay := time.Until(*ship.CooldownExpiration())
	if delay < 0 {
		delay = 0
	}
	delay += ClockDriftBuffer // Buffer for clock drift

	s.mu.Lock()
	defer s.mu.Unlock()

	timerKey := ship.ShipSymbol() + ":cooldown"
	if existing, ok := s.timers[timerKey]; ok {
		existing.Stop()
	}

	symbol := ship.ShipSymbol()
	playerID := ship.PlayerID()

	s.timers[timerKey] = time.AfterFunc(delay, func() {
		s.handleCooldownClear(symbol, playerID, timerKey)
	})

	fmt.Printf("Scheduled cooldown clear for %s in %v\n", symbol, delay)
}

// handleCooldownClear processes a cooldown expiration
func (s *ShipStateScheduler) handleCooldownClear(symbol string, playerID shared.PlayerID, timerKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	freshShip, err := s.shipRepo.FindBySymbol(ctx, symbol, playerID)
	if err != nil {
		fmt.Printf("Warning: Failed to fetch ship %s for cooldown clear: %v\n", symbol, err)
		return
	}

	// Only clear if cooldown is still set (might have been cleared by another operation)
	if freshShip.CooldownExpiration() == nil {
		return
	}

	freshShip.ClearCooldown()

	if err := s.shipRepo.Save(ctx, freshShip); err != nil {
		fmt.Printf("Warning: Failed to save ship %s after cooldown clear: %v\n", symbol, err)
	} else {
		fmt.Printf("Cooldown cleared for ship %s\n", symbol)
	}

	// Cleanup timer reference
	s.mu.Lock()
	delete(s.timers, timerKey)
	s.mu.Unlock()
}

// ScheduleAllPending schedules timers for all ships with pending arrivals/cooldowns
// Called on daemon startup after syncing ships from API
func (s *ShipStateScheduler) ScheduleAllPending(ctx context.Context) error {
	// Schedule arrivals for in-transit ships with future arrival times
	inTransitShips, err := s.shipRepo.FindInTransitWithFutureArrival(ctx)
	if err != nil {
		return fmt.Errorf("failed to find in-transit ships: %w", err)
	}
	for _, ship := range inTransitShips {
		s.ScheduleArrival(ship)
	}

	// Schedule cooldown clears for ships with future cooldowns
	shipsWithCooldown, err := s.shipRepo.FindWithFutureCooldown(ctx)
	if err != nil {
		return fmt.Errorf("failed to find ships with cooldown: %w", err)
	}
	for _, ship := range shipsWithCooldown {
		s.ScheduleCooldownClear(ship)
	}

	// Also handle any ships that should have already arrived/cleared (past times)
	// These will execute immediately due to delay=0
	pastArrivalShips, err := s.shipRepo.FindInTransitWithPastArrival(ctx)
	if err != nil {
		return fmt.Errorf("failed to find past-arrival ships: %w", err)
	}
	for _, ship := range pastArrivalShips {
		s.ScheduleArrival(ship) // Will execute immediately
	}

	pastCooldownShips, err := s.shipRepo.FindWithExpiredCooldown(ctx)
	if err != nil {
		return fmt.Errorf("failed to find expired-cooldown ships: %w", err)
	}
	for _, ship := range pastCooldownShips {
		s.ScheduleCooldownClear(ship) // Will execute immediately
	}

	arrivals := len(inTransitShips) + len(pastArrivalShips)
	cooldowns := len(shipsWithCooldown) + len(pastCooldownShips)
	if arrivals > 0 || cooldowns > 0 {
		fmt.Printf("Scheduled %d arrival(s) and %d cooldown clear(s)\n", arrivals, cooldowns)
	}

	return nil
}

// CancelAll cancels all pending timers (for graceful shutdown)
func (s *ShipStateScheduler) CancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key, timer := range s.timers {
		timer.Stop()
		delete(s.timers, key)
	}
}

// PendingCount returns the number of pending timers (for testing/monitoring)
func (s *ShipStateScheduler) PendingCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.timers)
}

// StartBackgroundSweeper starts a goroutine that periodically checks for stuck ships.
// This provides resilience against failed timer callbacks (DB timeouts, save failures, etc.)
func (s *ShipStateScheduler) StartBackgroundSweeper() {
	go func() {
		ticker := time.NewTicker(SweeperInterval)
		defer ticker.Stop()

		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				s.sweepStuckShips()
			}
		}
	}()
	fmt.Printf("Background sweeper started (interval: %v)\n", SweeperInterval)
}

// sweepStuckShips finds and transitions ships that are stuck in IN_TRANSIT with past arrival times.
// This catches ships that slipped through due to failed saves, timeouts, or other errors.
func (s *ShipStateScheduler) sweepStuckShips() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Find ships that should have arrived but are still IN_TRANSIT
	stuckShips, err := s.shipRepo.FindInTransitWithPastArrival(ctx)
	if err != nil {
		fmt.Printf("Sweeper: Failed to find stuck ships: %v\n", err)
		return
	}

	if len(stuckShips) == 0 {
		return
	}

	fmt.Printf("Sweeper: Found %d stuck ship(s), transitioning...\n", len(stuckShips))

	for _, ship := range stuckShips {
		if err := ship.Arrive(); err != nil {
			fmt.Printf("Sweeper: Failed to transition %s: %v\n", ship.ShipSymbol(), err)
			continue
		}

		ship.ClearArrivalTime()

		if err := s.shipRepo.Save(ctx, ship); err != nil {
			fmt.Printf("Sweeper: Failed to save %s: %v\n", ship.ShipSymbol(), err)
		} else {
			fmt.Printf("Sweeper: Unstuck %s → IN_ORBIT at %s\n", ship.ShipSymbol(), ship.CurrentLocation().Symbol)

			// Publish ARRIVED event for unstuck ships too
			if s.eventPublisher != nil {
				s.eventPublisher.PublishArrived(
					ship.ShipSymbol(),
					ship.PlayerID(),
					ship.CurrentLocation().Symbol,
					ship.NavStatus(),
				)
			}
		}
	}

	// Also sweep stuck cooldowns
	stuckCooldowns, err := s.shipRepo.FindWithExpiredCooldown(ctx)
	if err != nil {
		fmt.Printf("Sweeper: Failed to find stuck cooldowns: %v\n", err)
		return
	}

	for _, ship := range stuckCooldowns {
		ship.ClearCooldown()
		if err := s.shipRepo.Save(ctx, ship); err != nil {
			fmt.Printf("Sweeper: Failed to clear cooldown for %s: %v\n", ship.ShipSymbol(), err)
		} else {
			fmt.Printf("Sweeper: Cleared stuck cooldown for %s\n", ship.ShipSymbol())
		}
	}
}

// Stop stops the background sweeper and cancels all timers
func (s *ShipStateScheduler) Stop() {
	close(s.stopCh)
	s.CancelAll()
}
