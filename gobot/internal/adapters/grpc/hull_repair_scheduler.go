package grpc

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/hullrepair"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
)

const (
	// defaultHullRepairInterval is how often the open unreadable-hull episodes are swept.
	// Short because the sweep makes no API call at all while nothing is open, and because
	// the cost of the fault it clears is measured in hull-hours.
	defaultHullRepairInterval = 60 * time.Second

	// hullRepairSweepTimeout bounds one sweep so a stuck probe cannot wedge the loop.
	hullRepairSweepTimeout = 3 * time.Minute
)

// hullRepairLogf is the repair's log sink.
var hullRepairLogf = log.Printf

// HullRepairScheduler runs the unreadable-hull repair sweep on a fixed cadence. It mirrors
// the other standing daemon loops: a timer whose body is panic-isolated, halting promptly
// on ctx cancellation or Stop().
//
// It is unconditional. The fault it repairs appears with no operator present and does not
// clear on its own, so an arming step would be a switch nobody is there to throw.
type HullRepairScheduler struct {
	sweep    func(context.Context) error
	interval time.Duration
	logf     func(format string, args ...interface{})
	stopCh   chan struct{}
}

// NewHullRepairScheduler builds the standing sweep. A non-positive interval selects the
// default.
func NewHullRepairScheduler(sweep func(context.Context) error, interval time.Duration) *HullRepairScheduler {
	if interval <= 0 {
		interval = defaultHullRepairInterval
	}
	return &HullRepairScheduler{
		sweep:    sweep,
		interval: interval,
		logf:     log.Printf,
		stopCh:   make(chan struct{}),
	}
}

// Run blocks, sweeping every interval, until ctx is canceled or Stop() is called. The
// timer is reset only after a pass returns, so a long sweep cannot stack passes on itself.
func (s *HullRepairScheduler) Run(ctx context.Context) error {
	timer := time.NewTimer(s.interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.stopCh:
			return nil
		case <-timer.C:
			supervise.Guard("hull-repair", func() {
				if err := s.sweep(ctx); err != nil && !errors.Is(err, context.Canceled) {
					s.logf("Unreadable-hull repair sweep failed: %v", err)
				}
			})
			timer.Reset(s.interval)
		}
	}
}

// Stop halts Run. The daemon stops the loop via runCtx cancellation, so this is primarily
// the explicit test seam.
func (s *HullRepairScheduler) Stop() {
	close(s.stopCh)
}

// hullRepairLedger builds the durable episode store.
func (s *DaemonServer) hullRepairLedger() *persistence.UnreadableHullLedger {
	return persistence.NewUnreadableHullLedger(s.db)
}

// hullRepairSweeper assembles the repair for one player over the daemon's live
// collaborators. It errors when the API client cannot bisect a hull — without that read
// there is no confirmation, and an unconfirmed repair is exactly what must not happen.
func (s *DaemonServer) hullRepairSweeper(playerID int) (*hullrepair.Sweeper, error) {
	parts, ok := s.apiClient.(hullPartsReader)
	if !ok {
		return nil, errors.New("the wired API client cannot probe a hull's sub-resources, so the repair signature cannot be confirmed")
	}
	if s.db == nil {
		return nil, errors.New("no database wired for the unreadable-hull repair")
	}
	tokens := hullTokens{playerRepo: s.playerRepo}
	repairer := hullrepair.NewRepairer(
		&hullRepairProbe{parts: parts, tokens: tokens, playerID: playerID},
		&hullRepairWriter{api: s.apiClient, tokens: tokens, mediator: s.mediator, playerID: playerID},
		&hullFuelMarket{db: s.db},
		&hullTreasury{
			inner:  persistence.NewLedgerTreasury(s.db, &liveAgentCredits{api: s.apiClient}, s.clock, 0),
			tokens: tokens,
		},
		&hullTankSize{db: s.db},
		&hullRowRefresher{shipRepo: s.shipRepo},
		hullRepairReporter{},
	)
	return hullrepair.NewSweeper(s.hullRepairLedger(), repairer, hullRepairReporter{}, s.clock.Now, hullRepairLogf), nil
}

// sweepUnreadableHulls is the scheduler's tick: work every open episode for the open-era
// player. It is a no-op, and free of API calls, while no hull is unreadable.
func (s *DaemonServer) sweepUnreadableHulls(parent context.Context) error {
	if s.db == nil || s.apiClient == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, hullRepairSweepTimeout)
	defer cancel()

	playerID := s.primaryPlayerID(ctx)
	if playerID == 0 {
		return nil
	}
	sweeper, err := s.hullRepairSweeper(playerID)
	if err != nil {
		return err
	}
	return sweeper.Sweep(ctx, playerID)
}

// RecordUnreadableHulls opens a repair episode for every hull a fleet read could not
// deliver. It only writes rows, so a caller that has just learned a hull is unreadable
// pays nothing to report it, and re-reporting an open episode never resets its bounds.
func (s *DaemonServer) RecordUnreadableHulls(ctx context.Context, playerID int, symbols []string) {
	if s.db == nil || playerID == 0 || len(symbols) == 0 {
		return
	}
	ledger := s.hullRepairLedger()
	now := s.clock.Now()
	for _, symbol := range symbols {
		// The fleet read names a hull it could not deliver only when the payload gave up
		// a symbol or our own rows could supply one; the placeholder for neither is not a
		// hull and cannot be repaired.
		if symbol == "" || symbol == api.UnidentifiedHull {
			continue
		}
		if err := ledger.Observe(ctx, playerID, symbol, now); err != nil {
			hullRepairLogf("WARNING [hull_repair_ledger] ship=%s: could not open a repair episode: %v", symbol, err)
		}
	}
}
