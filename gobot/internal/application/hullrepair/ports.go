// Package hullrepair turns a hull the API refuses to serialise back into a readable one.
//
// The fault is server-side and permanent: the composite ship record 500s forever while
// every sub-resource still answers, which narrows the corruption to one field the parts do
// not cover. Fuel is the only such field a client can WRITE, and writing it re-serialises
// the record. Everything here is built around that asymmetry — the read cannot be trusted,
// so the write is confirmed against the parts before it is spent.
package hullrepair

import (
	"context"
	"time"
)

// Verdict is what one read of a resource established.
type Verdict int

const (
	// ReadOK — the resource served.
	ReadOK Verdict = iota
	// ReadRefusedServer — the server could not produce it (5xx): the corruption shape.
	ReadRefusedServer
	// ReadRefusedClient — refused on the request's own merits (4xx). The hull is gone or
	// not ours, and neither is repairable.
	ReadRefusedClient
	// ReadUnavailable — rate limiting or a transport failure: nothing was established, so
	// nothing may be concluded and nothing may be written.
	ReadUnavailable
)

// NavReading is the live position a repair needs. It comes from the /nav sub-resource
// rather than our own row: acting on a stale row is what the unreadable hull already is.
type NavReading struct {
	WaypointSymbol string
	Status         string
	ArrivalAt      time.Time
}

// Nav statuses the repair distinguishes.
const (
	NavDocked    = "DOCKED"
	NavInOrbit   = "IN_ORBIT"
	NavInTransit = "IN_TRANSIT"
)

// Subresources is the part-by-part reading of a hull whose composite record refused.
// Answered naming at least one resource is the whole confirmation: parts that serve while
// the whole does not is the corruption, and parts that also refuse is the API being down.
type Subresources struct {
	Nav      *NavReading
	Answered []string
	Refused  []string
}

// AnyAnswered reports whether the composite's failure is local to this hull.
func (s Subresources) AnyAnswered() bool { return len(s.Answered) > 0 }

// HullProbe reads a hull without spending anything.
type HullProbe interface {
	// ReadComposite is GET /my/ships/<symbol>; only the verdict matters here.
	ReadComposite(ctx context.Context, symbol string) (Verdict, error)
	// ProbeSubresources reads the parts, stopping at the first that answers. It must
	// return a Nav reading whenever /nav is among them.
	ProbeSubresources(ctx context.Context, symbol string) (Subresources, error)
}

// RefuelReceipt is what the fuel write reported.
type RefuelReceipt struct {
	FuelCurrent  int
	FuelCapacity int
	CreditsCost  int
}

// HullWriter performs the repair's game actions. Writes reach a hull whose reads refuse,
// which is the only reason any of this works.
type HullWriter interface {
	Dock(ctx context.Context, symbol string) error
	Orbit(ctx context.Context, symbol string) error
	Refuel(ctx context.Context, symbol string) (RefuelReceipt, error)
}

// FuelMarket answers both questions the spend needs: whether fuel can be bought here at
// all, and what a unit costs. An unpriceable waypoint fails the guard closed.
type FuelMarket interface {
	FuelAsk(ctx context.Context, playerID int, waypoint string) (price int, sells bool, err error)
}

// Treasury is the live balance. It must return an error rather than a stale or zero
// balance when it cannot read.
type Treasury interface {
	Credits(ctx context.Context, playerID int) (int64, error)
}

// TankSize reports a hull's fuel capacity, which bounds the worst-case spend. Capacity is
// a composite-only field, so the last good read is the only source; it changes only on a
// refit, which makes a stale value safe here where a stale position would not be.
type TankSize interface {
	FuelCapacity(ctx context.Context, playerID int, symbol string) (int, error)
}

// HullRefresher re-reads a repaired hull into its row so coordinators stop working from
// the snapshot taken before the hull went dark.
type HullRefresher interface {
	Refresh(ctx context.Context, playerID int, symbol string) error
}

// Record is one open unreadable episode. It is persisted: the attempt bound is the thing
// that stops a doomed repair looping, and a bound that resets on restart is not a bound.
type Record struct {
	PlayerID      int
	ShipSymbol    string
	FirstSeenAt   time.Time
	Attempts      int
	NextAttemptAt time.Time
	EscalatedAt   *time.Time
	LastOutcome   string
	LastReason    string
}

// Ledger persists the open episodes.
type Ledger interface {
	// Observe opens an episode, or refreshes an open one without touching its attempts.
	Observe(ctx context.Context, playerID int, symbol string, at time.Time) error
	// Due lists the open, non-escalated episodes whose backoff has expired.
	Due(ctx context.Context, playerID int, at time.Time) ([]Record, error)
	// Save persists an episode's attempt count, backoff, outcome and escalation.
	Save(ctx context.Context, rec Record) error
	// Clear removes an episode; the hull reads again, or is gone.
	Clear(ctx context.Context, playerID int, symbol string) error
	// Find returns one episode if it is open.
	Find(ctx context.Context, playerID int, symbol string) (Record, bool, error)
}

// Reporter surfaces what an operator has to know. Every method is best-effort: a
// reporting miss must never change what the repair does.
type Reporter interface {
	Attempted(symbol string, outcome Outcome)
	Escalated(symbol, reason string)
}
