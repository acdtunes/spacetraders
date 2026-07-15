// Package capacity holds the ADAPTER side of the capacity reconciler (epic
// st-7zk): implementations of the ports declared in
// internal/domain/capacity, wired in cmd/spacetraders-daemon/main.go. This
// file is the SENSE lane (bead st-7ee): a read-only signal collector over the
// daemon database plus the live-API treasury boundary.
//
// Sibling lanes (actuator st-5ig, proposal channel st-0h8) add their adapters
// to this same package — keep exported names narrow and lane-scoped.
package capacity

import (
	"context"
	"fmt"
	"log"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	domainCapacity "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/dutycycle"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// TreasuryReader is the sensor's ONLY live-API boundary: the current agent
// credit balance. Production satisfies it with
// internal/adapters/expansion.TreasuryReader; tests double it.
type TreasuryReader interface {
	LiveCredits(ctx context.Context, playerID shared.PlayerID) (int, error)
}

// DutyCycleReportFunc supplies the in-memory duty-cycle KPI snapshot (the
// global sampler accumulates only while the daemon runs — there is no
// persisted history to read, see internal/domain/dutycycle).
type DutyCycleReportFunc func() dutycycle.Report

const (
	// DefaultIncomeWindow is the trailing ledger window income velocity is
	// measured over — the same 1h the bootstrap harness's income-per-hour
	// probe uses (internal/adapters/grpc/bootstrap_ports.go).
	DefaultIncomeWindow = time.Hour

	// DefaultCrossSystemSourceDistance is the coarse distance tier assigned to
	// a good whose only known source market is outside the hub's system. In-
	// system waypoint coordinates span roughly ±800, so no real in-system pair
	// exceeds ~2263 — the tier deliberately dominates every in-system distance,
	// mirroring the warehousecap adapter's in-system/cross-system residual
	// tiering (a cross-system restock costs gate hops, not Euclidean units).
	DefaultCrossSystemSourceDistance = 2500.0
)

// Sensor implements domain capacity.Sensor: one read-only Signals snapshot per
// call. Collection is best-effort PER FAMILY — a source that fails or does not
// exist yet yields that family empty with a logged note, never a blocked tick
// (partial real signal beats a blocked engine). The only hard error is a
// mis-wired (nil) database.
type Sensor struct {
	db                  *gorm.DB
	treasury            TreasuryReader
	clock               shared.Clock
	dutyCycleReport     DutyCycleReportFunc
	incomeWindow        time.Duration
	crossSystemDistance float64
}

var _ domainCapacity.Sensor = (*Sensor)(nil)

// SensorOption customizes a Sensor (test seams + calibration knobs).
type SensorOption func(*Sensor)

// WithSensorClock injects the time source (tests freeze it). Sensor-prefixed:
// sibling reconciler lanes add adapters to this same package, and a bare
// WithClock invites a merge-time name collision.
func WithSensorClock(clock shared.Clock) SensorOption {
	return func(s *Sensor) { s.clock = clock }
}

// WithDutyCycleReport injects the duty-cycle KPI source. Default reads the
// global in-memory sampler (nil-safe: zero report before the daemon starts it).
func WithDutyCycleReport(report DutyCycleReportFunc) SensorOption {
	return func(s *Sensor) { s.dutyCycleReport = report }
}

// WithIncomeWindow overrides the trailing income-velocity window.
func WithIncomeWindow(window time.Duration) SensorOption {
	return func(s *Sensor) {
		if window > 0 {
			s.incomeWindow = window
		}
	}
}

// WithCrossSystemSourceDistance overrides the cross-system distance tier.
func WithCrossSystemSourceDistance(distance float64) SensorOption {
	return func(s *Sensor) {
		if distance > 0 {
			s.crossSystemDistance = distance
		}
	}
}

// NewSensor builds the production SENSE collector over the daemon database and
// the live-API treasury reader.
func NewSensor(db *gorm.DB, treasury TreasuryReader, opts ...SensorOption) *Sensor {
	s := &Sensor{
		db:                  db,
		treasury:            treasury,
		clock:               shared.NewRealClock(),
		dutyCycleReport:     func() dutycycle.Report { return metrics.GetGlobalDutyCycleSampler().Report() },
		incomeWindow:        DefaultIncomeWindow,
		crossSystemDistance: DefaultCrossSystemSourceDistance,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Sense collects one read-only Signals snapshot for the player. Family
// collection is best-effort: a failed/absent source logs a note and leaves
// that family empty — Sense errors only on a mis-wired (nil) database.
func (s *Sensor) Sense(ctx context.Context, playerID int) (domainCapacity.Signals, error) {
	if s.db == nil {
		return domainCapacity.Signals{}, fmt.Errorf("capacity sensor: database not wired")
	}
	now := s.clock.Now()

	contracts, err := s.loadContracts(ctx, playerID)
	if err != nil {
		s.note("demand", err)
		contracts = nil
	}
	demand := s.senseDemand(contracts, now)
	topology := s.senseTopology(ctx, playerID)
	utilization := s.senseUtilization(ctx, playerID)

	return domainCapacity.Signals{
		PlayerID:    playerID,
		CollectedAt: now,
		Demand:      demand,
		Performance: s.sensePerformance(ctx, playerID, contracts),
		Topology:    topology,
		Utilization: utilization,
		// FleetHullCount derives from the SAME hull set Utilization carries —
		// the len(Utilization.Hulls) consistency the contract requires is
		// structural, not coincidental.
		Economics: s.senseEconomics(ctx, playerID, demand, topology, len(utilization.Hulls)),
	}, nil
}

// note logs one family's degradation — a partial real signal beats a blocked
// engine, but the gap must be visible, never silent.
func (s *Sensor) note(family string, err error) {
	log.Printf("capacity sensor: %s signals degraded (family left empty): %v", family, err)
}
