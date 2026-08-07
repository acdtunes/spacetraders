package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// RelocatorHull is one live trade hull as the relocator observes it.
type RelocatorHull struct {
	ShipSymbol    string
	CurrentSystem string
	// IsCommandFrigate marks the command frigate. RULINGS #7 protects it; it is dropped at
	// observation and can never be scored.
	IsCommandFrigate bool
	// Pinned marks a hull under a fleet pin or exclusive dedication. RULINGS #7 — never poached.
	Pinned bool
	// OnTour is true while the hull is executing a tour. Only a hull at honest release is a
	// candidate; a touring hull is skipped and reconsidered on a later tick.
	OnTour bool
	// InTransit is true while the hull is physically flying. It IMPLIES OnTour but is separate: an
	// unfinished intent abandons a tour-claimed hull, and leaves a flying one alone to arrive.
	InTransit bool
	// Offered marks a hull whose tour has reached a boundary and has DURABLY offered it for
	// relocation until a deadline (sp-e8d92 first refusal). Its tour container is still RUNNING — it is
	// waiting — so the hull reads OnTour and would otherwise be excluded as mid_tour, which is exactly
	// the 38..40 exclusions the fleet reports while occupying 23 of 373 tradeable systems.
	//
	// AN OFFER IS PERMISSION, NOT OWNERSHIP. It says "this hull's tour will wait for you until T", never
	// "ownership is waived": a protected hull stays protected, and an offer that lapses before the move
	// commits is abandoned through the counted actuation re-check like any other lost hull.
	Offered bool
}

// RelocatorRegion is one candidate region: an anchor system plus the neighbourhood a tour planned
// from its landing waypoint can reach, carrying the rate the planner projects there.
type RelocatorRegion struct {
	AnchorSystem string
	// LandingWaypoint is where the hull lands and the planner prices the candidate tour from.
	LandingWaypoint string
	// GateHops is the gate-hop distance from the hull's current system, priced through the
	// TravelHopModel.
	GateHops int
	// ProjectedRate is the planner-projected credits/hour for this hull on the region's snapshot.
	ProjectedRate float64
	// RateReadable is false when the region carries no usable projection. Such a region is EXCLUDED,
	// never scored at an assumed rate (fail closed).
	RateReadable bool
	// SnapshotAge is how old the region's market snapshot is, measured against the per-activity
	// RankerAgeCaps cap for Activity.
	SnapshotAge time.Duration
	// Activity is the region's market activity level, selecting its freshness cap.
	Activity string
}

// RelocationIntent is the durable record of one relocation decision. ONE record per hull, rewritten
// in place, and it serves two restart duties at once (RULINGS #2): while Completed is false it is an
// in-flight move a restart must finish rather than re-decide, and once Completed it is the per-hull
// COOLDOWN clock, which therefore survives a restart instead of resetting to "never relocated".
type RelocationIntent struct {
	ShipSymbol     string
	FromSystem     string
	TargetSystem   string
	TargetWaypoint string
	// DecidedAt is when the relocation was decided — the cooldown clock's origin.
	DecidedAt time.Time
	// Completed marks the move as landed.
	Completed bool
}

// RelocatorFleetObserver lists the live trade hulls with the position and protection facts the
// relocator filters on.
type RelocatorFleetObserver interface {
	ObserveTradeHulls(ctx context.Context, playerID int) ([]RelocatorHull, error)
	// ObserveHull re-reads ONE hull's live protection facts, for the actuation-time re-check
	// (sp-x2jr6 slice 1). It must derive Pinned/OnTour/IsCommandFrigate exactly as ObserveTradeHulls
	// does, or the commit gate and the scoring gate will disagree about what a protected hull is.
	// An error means the hull's ownership is UNPROVABLE, which fails the move closed.
	ObserveHull(ctx context.Context, playerID int, shipSymbol string) (RelocatorHull, error)
}

// RelocatorRegionObserver produces the candidate regions reachable from originSystem within
// hopRadius gate hops, each with a FRESH snapshot and the rate a planner projects on it.
type RelocatorRegionObserver interface {
	ObserveRegions(ctx context.Context, playerID int, originSystem string, hopRadius int) ([]RelocatorRegion, error)
}

// RelocatorTelemetryObserver reads the per-leg tour telemetry the per-hull EWMA rate is computed
// from — realized TRANSACTIONS, which is what makes the rate per-hull rather than per-lane.
type RelocatorTelemetryObserver interface {
	ObserveTourTelemetry(ctx context.Context, playerID int, since time.Time) ([]trading.TourLegTelemetry, error)
}

// RelocatorEraHorizon reports how much era is left, and whether that is a real reading. An
// unreadable horizon bounds the valuation to the horizon knob rather than silencing the reconciler
// (see trading.ValueRelocation).
type RelocatorEraHorizon interface {
	RemainingEraHours(ctx context.Context, playerID int) (float64, bool)
}

// RelocatorActuator moves a hull through the existing occupancy/reposition primitives. NO SPEND: it
// is travel and a claim, never a purchase.
type RelocatorActuator interface {
	RelocateHull(ctx context.Context, playerID int, shipSymbol, targetWaypoint string, jumpBound int) error
}

// RelocationIntentStore persists relocation intents so a restart finishes rather than re-decides,
// and so the per-hull cooldown survives a restart (RULINGS #2).
type RelocationIntentStore interface {
	// LoadRelocationIntents returns every persisted intent for the container, completed and not.
	LoadRelocationIntents(ctx context.Context, containerID string, playerID int) ([]RelocationIntent, error)
	// RecordRelocationIntent writes (or rewrites) the single record for intent.ShipSymbol.
	RecordRelocationIntent(ctx context.Context, containerID string, playerID int, intent RelocationIntent) error
}

// readTelemetry reads the EWMA window. An unreadable repository yields no rows, which makes every
// hull rate unreadable and therefore relocates nothing — fail closed by construction.
func (h *RunOpportunityRelocatorHandler) readTelemetry(ctx context.Context, cmd *RunOpportunityRelocatorCommand) []trading.TourLegTelemetry {
	window := cmd.rateWindow()
	rows, err := h.telemetry.ObserveTourTelemetry(ctx, cmd.PlayerID, h.clock.Now().Add(-window))
	if err != nil {
		common.LoggerFromContext(ctx).Log("WARN", fmt.Sprintf("Opportunity relocator: tour telemetry unreadable, no hull has a provable rate this tick: %v", err), map[string]interface{}{
			"reason": "telemetry_unreadable",
		})
		return nil
	}
	return rows
}

// readEraHorizon reads the remaining era. Unknown is honest and bounded, not fatal — see
// trading.ValueRelocation on an unknown horizon.
func (h *RunOpportunityRelocatorHandler) readEraHorizon(ctx context.Context, cmd *RunOpportunityRelocatorCommand) (float64, bool) {
	if h.era == nil {
		return 0, false
	}
	return h.era.RemainingEraHours(ctx, cmd.PlayerID)
}
