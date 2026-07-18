package grpc

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
)

// ClockDriftBuffer accounts for slight time differences between API server and local clock.
// Ensures we never act before the API considers the ship arrived.
const ClockDriftBuffer = 1 * time.Second

// SweeperInterval is how often the background sweeper checks for stuck ships.
// This catches ships that slip through due to failed saves, timeouts, or clock drift.
// With event-based arrival handling, the sweeper is a safety net, not the primary mechanism.
const SweeperInterval = 60 * time.Second

// shipStateWriteTimeout bounds a single ship-state transition write (arrival or
// cooldown clear) performed under CAS-retry.
const shipStateWriteTimeout = 10 * time.Second

// stuckSweepTimeout bounds one full stuck-ship sweeper pass (a batch that may
// transition many ships in sequence).
const stuckSweepTimeout = 30 * time.Second

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
		supervise.Guard("ship-arrival-timer", func() {
			s.handleArrival(symbol, playerID)
		})
	})

	fmt.Printf("Scheduled arrival for %s in %v\n", symbol, delay)
}

// handleArrival processes a ship arrival
func (s *ShipStateScheduler) handleArrival(symbol string, playerID shared.PlayerID) {
	ctx, cancel := context.WithTimeout(context.Background(), shipStateWriteTimeout)
	defer cancel()

	// Re-find + arrive + save under CAS-retry (sp-01wc): on a concurrent-writer
	// version conflict the arrival is re-applied on the fresh row instead of
	// last-write-wins clobbering the other writer. The in-transit guard lives
	// inside the mutation so it is re-checked on every re-find — if another
	// writer already transitioned the hull, changed=false and we skip the write.
	freshShip, saved, err := s.shipRepo.SaveWithRetry(ctx, symbol, playerID, arriveIfInTransit)
	if err != nil {
		fmt.Printf("Warning: Failed to transition ship %s to orbit: %v\n", symbol, err)
	} else if saved {
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
		supervise.Guard("ship-cooldown-timer", func() {
			s.handleCooldownClear(symbol, playerID, timerKey)
		})
	})

	fmt.Printf("Scheduled cooldown clear for %s in %v\n", symbol, delay)
}

// handleCooldownClear processes a cooldown expiration
func (s *ShipStateScheduler) handleCooldownClear(symbol string, playerID shared.PlayerID, timerKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), shipStateWriteTimeout)
	defer cancel()

	// Re-find + clear-cooldown + save under CAS-retry (sp-01wc): a concurrent
	// writer's mutation is re-applied on fresh state instead of clobbered. The
	// still-set guard lives inside the mutation so it is re-checked on every
	// re-find (another operation may have cleared it first → changed=false).
	_, saved, err := s.shipRepo.SaveWithRetry(ctx, symbol, playerID, clearCooldownIfSet)
	if err != nil {
		fmt.Printf("Warning: Failed to save ship %s after cooldown clear: %v\n", symbol, err)
	} else if saved {
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

// RunSweeper blocks, checking for stuck ships every SweeperInterval, until
// ctx is canceled or Stop() is called. It runs under the daemon Supervisor
// (sp-i01z): a panic inside a sweep pass is captured there and the sweeper
// restarts with backoff instead of dying silently — before this, a dead
// sweeper meant arrivals stopped being swept for the rest of the daemon's
// life with zero signal. Replaces StartBackgroundSweeper.
func (s *ShipStateScheduler) RunSweeper(ctx context.Context) error {
	fmt.Printf("Background sweeper started (interval: %v)\n", SweeperInterval)
	ticker := time.NewTicker(SweeperInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.stopCh:
			return nil
		case <-ticker.C:
			s.sweepStuckShips()
		}
	}
}

// sweepStuckShips finds and transitions ships that are stuck in IN_TRANSIT with past arrival times.
// This catches ships that slipped through due to failed saves, timeouts, or other errors.
func (s *ShipStateScheduler) sweepStuckShips() {
	ctx, cancel := context.WithTimeout(context.Background(), stuckSweepTimeout)
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
		symbol := ship.ShipSymbol()
		playerID := ship.PlayerID()
		// Re-find + arrive + save under CAS-retry (sp-01wc), same as the
		// event-driven handler: the batch row may be stale by the time we write.
		freshShip, saved, err := s.shipRepo.SaveWithRetry(ctx, symbol, playerID, arriveIfInTransit)
		if err != nil {
			fmt.Printf("Sweeper: Failed to transition %s: %v\n", symbol, err)
			continue
		}
		if saved {
			fmt.Printf("Sweeper: Unstuck %s → IN_ORBIT at %s\n", symbol, freshShip.CurrentLocation().Symbol)

			// Publish ARRIVED event for unstuck ships too
			if s.eventPublisher != nil {
				s.eventPublisher.PublishArrived(
					symbol,
					playerID,
					freshShip.CurrentLocation().Symbol,
					freshShip.NavStatus(),
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
		symbol := ship.ShipSymbol()
		playerID := ship.PlayerID()
		_, saved, err := s.shipRepo.SaveWithRetry(ctx, symbol, playerID, clearCooldownIfSet)
		if err != nil {
			fmt.Printf("Sweeper: Failed to clear cooldown for %s: %v\n", symbol, err)
		} else if saved {
			fmt.Printf("Sweeper: Cleared stuck cooldown for %s\n", symbol)
		}
	}
}

// Stop stops the background sweeper and cancels all timers
func (s *ShipStateScheduler) Stop() {
	close(s.stopCh)
	s.CancelAll()
}

// arriveIfInTransit is the SaveWithRetry mutation that transitions a still-in-transit
// hull to orbit and clears its arrival time. It reports changed=false (skipping the
// write) when the hull is no longer in transit, so a concurrent writer that already
// transitioned it is never clobbered. Shared by the event-driven handler and the sweeper.
func arriveIfInTransit(sh *navigation.Ship) (bool, error) {
	if !sh.IsInTransit() {
		return false, nil
	}
	if err := sh.Arrive(); err != nil {
		return false, err
	}
	sh.ClearArrivalTime()
	return true, nil
}

// clearCooldownIfSet is the SaveWithRetry mutation that clears a still-set cooldown. It
// reports changed=false (skipping the write) when the cooldown is already clear, so a
// concurrent writer that cleared it first is never clobbered. Shared by the event-driven
// handler and the sweeper.
func clearCooldownIfSet(sh *navigation.Ship) (bool, error) {
	if sh.CooldownExpiration() == nil {
		return false, nil
	}
	sh.ClearCooldown()
	return true, nil
}
