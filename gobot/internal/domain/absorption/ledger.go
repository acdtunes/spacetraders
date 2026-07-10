// Package absorption defines the cross-engine market-absorption ledger port and its
// value types (sp-78ai). Five engines — tours, arb-run, idle-arb, trade-route
// circuits, pre-positioning — absorb the SAME market depth with no shared signal but
// the market cache, which reflects only EXECUTED trades seconds late. The ledger is
// the substrate that carries the two invisible windows: in-flight PLANNED intent and
// the decaying EXECUTED recovery shadow (design §0). These types live in the domain
// so every engine (application layer) and the DB-backed implementation (adapter
// layer) share one vocabulary without the engines depending on persistence.
package absorption

import (
	"context"
	"time"
)

// Depth sides. A leg dumping into a sink occupies the SELL side; a buy leg occupies
// the ask (BUY) side.
const (
	SideSell = "sell"
	SideBuy  = "buy"
)

// LaneKey identifies one absorption pool: a market's (waypoint, good, side). Depth
// is a property of the market, not of any one engine's leg, so every engine's rows
// on the same key net against the same pool.
type LaneKey struct {
	Waypoint string
	Good     string
	Side     string
}

// KeyOccupancy is one pool's outstanding absorption as a reader sees it: PLANNED
// in-flight units (full, undecayed) plus the recovering EXECUTED residual (decayed
// on the fitted curve and counted only while still above the recovery floor).
type KeyOccupancy struct {
	PlannedUnits       int
	RecoveringResidual float64
}

// ReserveEntry is one sink a plan wants to absorb into. CapUnits is the fleet-wide
// ceiling for the key: idle-arb passes its own Units for BINARY exclusion (any other
// outstanding leg breaches), a tour passes A-cap × trade_volume so tranches may
// lawfully stack up to the depth cap. TTL bounds the PLANNED row's life (2× projected
// flight + slack) so a wedged container cannot hold depth forever.
type ReserveEntry struct {
	Waypoint    string
	Good        string
	Side        string
	Units       int
	CapUnits    int
	Tier        string
	QuotedPrice int
	TTL         time.Duration
}

// Ledger is the driven port the engines consult and write (implemented by the
// DB-backed adapter). A nil Ledger disables an engine's ledger integration — the
// same optional-port contract the other engine guards use for missing wiring.
type Ledger interface {
	// Reserve records a plan's PLANNED absorption all-or-nothing and reports whether
	// every sink still clears its cap (fail closed: a breach parks the plan with
	// ok=false, err=nil). The returned reservationIDs (one per entry, in order)
	// identify the rows the caller must Release or Convert.
	Reserve(ctx context.Context, playerID int, containerID, engine string, entries []ReserveEntry) (reservationIDs []string, ok bool, err error)
	// RecordPlanned writes ONE PLANNED row unconditionally (with the same self-cleaning
	// sweep as Reserve) and returns its reservationID. It is the launch-record path for
	// a leg whose sink the consult READ already cleared (idle-arb, arb-run): the leg has
	// committed, so this publishes its in-flight occupancy for other engines to consult
	// rather than gating it again. A write failure is the caller's to log — it must not
	// strand a launched leg (fail-open on the write, the mirror of the consult's
	// fail-closed read).
	RecordPlanned(ctx context.Context, playerID int, containerID, engine string, entry ReserveEntry) (reservationID string, err error)
	// Outstanding returns the player's non-expired absorption pools decayed to now —
	// the single batched read a consult pass nets against market depth.
	Outstanding(ctx context.Context, playerID int) (map[LaneKey]KeyOccupancy, error)
	// ConvertByContainer turns a container's PLANNED hold into an EXECUTED recovery
	// shadow at sale (untagged sinks / zero-unit sales leave none — Q2). Idempotent.
	ConvertByContainer(ctx context.Context, containerID string, playerID int, key LaneKey, realizedUnits int, liveTier string, trancheSize int) error
	// Release consumes a PLANNED reservation on an exit without a sale (no-op if gone).
	Release(ctx context.Context, reservationID string) error
	// ReleaseByContainer drops ALL of a container's still-PLANNED reservations in one
	// statement — the tour writer's re-plan/restart de-dup seam (sp-78ai L3): before a
	// (re)plan it clears this container's stale in-flight intent so the fresh plan nets
	// against OTHERS' depth and Reserve cannot double-count the container's own prior
	// rows. EXECUTED recovery shadows are LEFT untouched (real market damage still
	// recovering, which the container's own next plan must also avoid). Returns the
	// number of PLANNED rows dropped. No-op (0) when the container holds none.
	ReleaseByContainer(ctx context.Context, containerID string, playerID int) (int, error)
}
