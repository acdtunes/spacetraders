package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/dutycycle"
)

// ShipEarningStatus is one hull's earning/idle status at the moment it was
// sampled (duty-cycle KPI). Earning means the hull was actively
// assigned to a working container at the moment of the sample; idle covers
// both genuinely unassigned hulls and captain reservations — a
// captain-reserved hull isn't earning either.
type ShipEarningStatus struct {
	Hull    string
	Earning bool
}

// DutyCycleSampler periodically samples every hull's earning/idle status on
// a ticker and accumulates dutycycle.Samples, computing the ship-hours
// earning/day KPI on demand via Report(). The
// lifecycle mirrors ShipStateScheduler's StartBackgroundSweeper/Stop
// ticker-goroutine pattern (internal/adapters/grpc/ship_state_scheduler.go).
//
// The sampling source is a plain func, not a named interface, matching this
// package's existing ContainerMetricsCollector.getContainers convention
// rather than inventing a new provider-interface shape.
type DutyCycleSampler struct {
	source   func(ctx context.Context) ([]ShipEarningStatus, error)
	interval time.Duration

	mu      sync.Mutex
	samples []dutycycle.Sample

	stopCh chan struct{}
}

// NewDutyCycleSampler builds a sampler that calls source every interval to
// snapshot each hull's earning status. A nil source makes Sample a safe
// no-op instead of panicking, mirroring the nil-tolerant contract shared by
// every metrics adapter in this package (e.g. APIBudgetTracker).
func NewDutyCycleSampler(source func(ctx context.Context) ([]ShipEarningStatus, error), interval time.Duration) *DutyCycleSampler {
	return &DutyCycleSampler{
		source:   source,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Sample takes one immediate snapshot from source and records it, appending
// one dutycycle.Sample per hull returned. A source error (e.g. the DB is
// temporarily unavailable) is swallowed — the same best-effort contract as
// the Prometheus collectors: a failed tick simply contributes no samples
// rather than taking down the caller.
func (s *DutyCycleSampler) Sample(ctx context.Context) {
	if s == nil || s.source == nil {
		return
	}
	statuses, err := s.source(ctx)
	if err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range statuses {
		s.samples = append(s.samples, dutycycle.Sample{Hull: st.Hull, Earning: st.Earning})
	}
}

// Report computes the duty-cycle report from every sample collected so far.
// Safe to call on a nil receiver (returns a zero-value report), so a
// possibly-nil global sampler (see GetGlobalDutyCycleSampler) can always be
// read without a caller-side nil check.
func (s *DutyCycleSampler) Report() dutycycle.Report {
	if s == nil {
		return dutycycle.Report{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return dutycycle.ComputeReport(s.samples, s.interval)
}

// Start begins the background sampling ticker in a new goroutine. Call Stop
// to end it.
func (s *DutyCycleSampler) Start() {
	go func() {
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				s.Sample(context.Background())
			}
		}
	}()
}

// Stop stops the background sampling ticker.
func (s *DutyCycleSampler) Stop() {
	close(s.stopCh)
}
