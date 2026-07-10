package trading

import (
	"context"
	"time"
)

// TourLegTelemetry is one planned-vs-realized record for a single trade at a single
// leg of a multi-hop trade tour (sp-1ek0 P1b). The tour_run executor emits one per
// executed (or explicitly skipped) trade so the graduation-gate report can measure
// the median |planned−realized|/planned price error — the gate metric that proves
// the market model, not merely that the tour turned a profit. A skipped/degraded
// trade records RealizedUnits=0 with a zero RealizedAt.
//
// This is the domain-level DTO the executor and report speak; the persistence layer
// maps it to its own row model, keeping the application decoupled from GORM (the same
// dependency-inversion the coordinators already use for ship/market repositories).
type TourLegTelemetry struct {
	TourID            string // the tour_run container id — groups a tour's legs
	ShipSymbol        string
	LegIndex          int
	Waypoint          string
	Good              string
	IsBuy             bool
	PlannedUnits      int
	RealizedUnits     int
	PlannedUnitPrice  int
	RealizedUnitPrice int
	PlannedAt         time.Time
	RealizedAt        time.Time
	PlayerID          int
}

// TourTelemetryRepository persists per-leg tour telemetry and reads it back for the
// graduation report. Implemented by the persistence layer.
type TourTelemetryRepository interface {
	// RecordLeg persists one planned-vs-realized trade record.
	RecordLeg(ctx context.Context, leg TourLegTelemetry) error
	// ListByPlayer returns playerID's telemetry rows whose PlannedAt is at or after
	// since, in execution order (a zero since returns the full history).
	ListByPlayer(ctx context.Context, playerID int, since time.Time) ([]TourLegTelemetry, error)
}
