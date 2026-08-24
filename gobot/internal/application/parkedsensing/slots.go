package parkedsensing

import (
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// ShipPos is where one hull is, read from the ships table.
type ShipPos struct {
	Waypoint  string
	NavStatus navigation.NavStatus
	// Found reports whether the ships table knows this hull at all. A hull we
	// cannot locate is never acted on.
	Found bool
	// FuelCapacity and EngineSpeed are the hull's flight characteristics, carried
	// so a charting crew's partition can be priced on the walk each hull actually
	// faces (chartshare.go). Zero means the ships table has not recorded them; no
	// decision here turns on that, and the solver prices such a hull on defaults.
	FuelCapacity int
	EngineSpeed  int
}

// QueuedSlot is one placement row as the buy queue and the placement machine
// see it: the ledger's own columns, with the nullable ones flattened to empty
// strings. Nothing distinguishes NULL from empty, which keeps every "is a hull
// recorded?" check a simple != "".
type QueuedSlot struct {
	Waypoint     string
	System       string
	Kind         string
	State        string
	AssignedShip string
	PurchaseYard string
	DepthCredits int64
	// WhitelistGoods is the whitelisted goods this placement was recorded as
	// watching — what lets the foothold path prove that releasing a hull leaves
	// its system's goods coverage intact (see coveredAfterMove). EMPTY MEANS
	// UNKNOWN, NEVER "WATCHES NOTHING": the adapter yields an empty list both for
	// a row that records no goods and for one whose goods column will not decode,
	// so it can only ever make a hull LESS eligible to be moved, never more.
	WhitelistGoods []string
}

// ScreenedSystem is one screened system's identity and size.
type ScreenedSystem struct {
	System       string
	DepthCredits int64
	// ScreenedAt is when this system was last looked at, or NIL for one that
	// never has been. It is what lets the sweep rotate least-recently-screened
	// first instead of re-screening a fixed alphabetical head forever. NIL IS
	// MEANINGFUL AND MUST NOT COLLAPSE TO THE ZERO TIME: a never-screened system
	// is the newly-discovered frontier, the case the sweep most needs to reach,
	// and the zero time would make it sort first only by accident.
	ScreenedAt *time.Time
}

// SlotFields carries the field writes a transition applies ATOMICALLY with the
// state flip. A nil pointer leaves the stored value alone; a pointer to the
// empty string CLEARS the column. Clearing matters: releasing a spare hull must
// drop its ship reference, or the ledger keeps counting a hull that now belongs
// to another slot.
type SlotFields struct {
	AssignedShip *string
	PurchaseYard *string
}

// SlotTransition addresses one placement row (Waypoint+Kind) and names the edge:
// From is the state the write is guarded on, To is what it becomes.
type SlotTransition struct {
	Waypoint string
	Kind     string
	From     string
	To       string
}
