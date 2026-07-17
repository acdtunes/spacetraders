// gobot/internal/adapters/flowfeed/registry.go

// Package flowfeed is an in-memory, read-only registry of active trading flows
// (tour / trade-route / arb) that the daemon exposes at GET /api/flows.
//
// RULINGS #4: this surface is pure state exposure. No trading logic reads it and
// no guard consults it; publishing is fire-and-forget and can never gate or relax
// a buy. The registry is process-memory and repopulates as executors re-adopt
// plans after a daemon restart (durable intent is phase 2).
package flowfeed

import (
	"sort"
	"sync"
	"time"
)

// Program identifies which trading executor published a flow.
const (
	ProgramTour       = "tour"
	ProgramTradeRoute = "trade-route"
	ProgramArb        = "arb"
)

// Tranche is one buy or sell intent at a hop.
type Tranche struct {
	Good              string `json:"good"`
	IsBuy             bool   `json:"isBuy"`
	Units             int    `json:"units"`
	ExpectedUnitPrice int    `json:"expectedUnitPrice"`
}

// Hop is a planned future stop with its intended tranches. System tags the
// stop's system so the galaxy view can chain cross-system glides;
// TravelSeconds is the planner's projected travel time from the previous stop
// (0 = no plan-time estimate — viewers fall back to nav-truth interpolation).
type Hop struct {
	Waypoint      string    `json:"waypoint"`
	System        string    `json:"system"`
	TravelSeconds int       `json:"travelSeconds"`
	Tranches      []Tranche `json:"tranches"`
}

// CargoItem is one good currently aboard the hull.
type CargoItem struct {
	Good  string `json:"good"`
	Units int    `json:"units"`
}

// Leg is the hull's current in-progress leg. Timestamps are best-effort from the
// executor's ship nav; the visualizer server overlays PG nav for position truth.
type Leg struct {
	From       string    `json:"from"`
	To         string    `json:"to"`
	DepartedAt time.Time `json:"departedAt"`
	ArrivesAt  time.Time `json:"arrivesAt"`
	// TravelSeconds is the planner's projected duration for THIS leg (0 = no
	// plan-time estimate). With PlannedAt (stamped at leg-start publish) it
	// anchors the galaxy view's schedule-drift glyph.
	TravelSeconds int `json:"travelSeconds"`
}

// Projection is the run's projected economics.
type Projection struct {
	Profit      int64   `json:"profit"`
	RatePerHour float64 `json:"ratePerHour"`
}

// Flow is one active trading run's published snapshot. Field order here IS the
// JSON field order of the payload contract.
type Flow struct {
	ContainerID   string      `json:"containerId"`
	Program       string      `json:"program"`
	Ship          string      `json:"ship"`
	TourID        *string     `json:"tourId"`
	Closed        bool        `json:"closed"` // closed-tour mode: the plan returns to its anchor (sp-im74)
	CurrentLeg    *Leg        `json:"currentLeg"`
	Cargo         []CargoItem `json:"cargo"`
	RemainingHops []Hop       `json:"remainingHops"`
	Projected     *Projection `json:"projected"`
	PlannedAt     time.Time   `json:"plannedAt"`
}

// Feed is the top-level GET /api/flows payload.
type Feed struct {
	Flows       []Flow    `json:"flows"`
	GeneratedAt time.Time `json:"generatedAt"`
}

// Registry is a concurrency-safe, in-memory set of active flows keyed by
// container id.
type Registry struct {
	mu    sync.RWMutex
	flows map[string]Flow
	now   func() time.Time // injectable for deterministic tests
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{flows: make(map[string]Flow), now: time.Now}
}

// Publish inserts or overwrites the flow for f.ContainerID. Overwrite is
// intentional: executors re-publish on every re-adoption and leg boundary, so the
// latest snapshot wins. A blank container id is ignored.
func (r *Registry) Publish(f Flow) {
	if f.ContainerID == "" {
		return
	}
	r.mu.Lock()
	r.flows[f.ContainerID] = f
	r.mu.Unlock()
}

// Remove drops the flow for the given container id (called at terminal exit).
func (r *Registry) Remove(containerID string) {
	r.mu.Lock()
	delete(r.flows, containerID)
	r.mu.Unlock()
}

// Snapshot returns the current feed. Flows are sorted by container id for a
// deterministic payload; an empty registry yields a non-nil empty slice so the
// JSON is {"flows":[]}, never {"flows":null}.
func (r *Registry) Snapshot() Feed {
	r.mu.RLock()
	flows := make([]Flow, 0, len(r.flows))
	for _, f := range r.flows {
		flows = append(flows, f)
	}
	r.mu.RUnlock()
	sort.Slice(flows, func(i, j int) bool { return flows[i].ContainerID < flows[j].ContainerID })
	return Feed{Flows: flows, GeneratedAt: r.now().UTC()}
}

// --- process-wide singleton (mirrors the metrics SetGlobal*Collector idiom) ---

var global *Registry

// SetGlobal installs the process-wide registry that the free functions delegate
// to. The daemon wires this once at construction.
func SetGlobal(r *Registry) { global = r }

// Publish is the nil-safe free function executors call. When no registry is
// installed (unit tests, or any path where the daemon did not wire one) it is a
// no-op — a missed publish can never touch the trade path.
func Publish(f Flow) {
	if global != nil {
		global.Publish(f)
	}
}

// Remove is the nil-safe free function the container runner calls at terminal exit.
func Remove(containerID string) {
	if global != nil {
		global.Remove(containerID)
	}
}
