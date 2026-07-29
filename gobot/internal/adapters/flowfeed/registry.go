// Package flowfeed is a read-only feed of active trading flows (tour /
// trade-route / arb) that the daemon exposes at GET /api/flows.
//
// RULINGS #4: this surface is pure state exposure. No trading logic reads it and
// no guard consults it; publishing is fire-and-forget and can never gate or relax
// a buy.
//
// The feed has TWO inputs and is assembled at request time (sp-2uvec):
//
//  1. the published snapshots executors write at plan adoption and leg arrival —
//     process-memory, so empty right after a daemon restart, and
//  2. the LIVE set of RUNNING trading containers, read from the daemon's runner
//     map on every Snapshot.
//
// Input 2 is what makes the feed honest. Publishing is event-sparse: a hull is
// silent while it repositions, replans, or flies a long first leg, and every hull
// is silent after a restart until it next adopts a plan — gaps of tens of minutes
// were routine. A published-only feed reported 5 of 13 running tours. Every
// RUNNING trading container now appears immediately (unenriched until its
// executor publishes), so neither a hull that joined mid-era nor a daemon restart
// can drop a live tour (RULINGS #2).
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
	From string `json:"from"`
	To   string `json:"to"`
	// DepartedAt is the TRUE leg-start timestamp (captured before travel began),
	// not publish time — the tour publisher runs after arrival, so a publish
	// stamp would sit at/after ArrivesAt.
	DepartedAt time.Time `json:"departedAt"`
	ArrivesAt  time.Time `json:"arrivesAt"`
	// TravelSeconds is the planner's projected duration for THIS leg (0 = no
	// plan-time estimate). With DepartedAt it anchors the galaxy view's
	// schedule-drift glyph: drift = ArrivesAt − (DepartedAt + TravelSeconds).
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
	Closed        bool        `json:"closed"` // closed-tour mode: the plan returns to its anchor
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

// LiveRun is the identity of one trading container that is RUNNING right now, as
// the daemon's live runner map sees it. It carries only what a flow needs before
// its executor has published a plan snapshot — the plan itself, the legs and the
// economics arrive with the first Publish.
type LiveRun struct {
	ContainerID string
	Program     string // ProgramTour | ProgramTradeRoute | ProgramArb
	Ship        string
	Closed      bool
}

// LiveSource enumerates the trading containers that are RUNNING at call time. The
// daemon wires this to the same in-memory runner map `spacetraders container list`
// reads, which restart recovery rebuilds — so it is restart-resilient by
// construction and can never disagree with the container list.
type LiveSource func() []LiveRun

// Registry is a concurrency-safe, in-memory set of active flows keyed by
// container id, assembled against a live view of RUNNING trading containers.
type Registry struct {
	mu    sync.RWMutex
	flows map[string]Flow
	live  LiveSource
	now   func() time.Time // injectable for deterministic tests
}

// New returns an empty registry with no live source. Snapshot then reports only
// what was published — the shape unit tests want. The daemon always installs a
// live source (SetLiveSource) at construction.
func New() *Registry {
	return &Registry{flows: make(map[string]Flow), now: time.Now}
}

// SetLiveSource installs the enumerator of RUNNING trading containers that
// Snapshot reconciles published flows against.
func (r *Registry) SetLiveSource(src LiveSource) {
	r.mu.Lock()
	r.live = src
	r.mu.Unlock()
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

// Snapshot returns the current feed: every published flow, plus a pending
// placeholder for every RUNNING trading container that has not published one yet.
// A published snapshot always wins over its placeholder — the placeholder exists
// only to stop a live tour vanishing while its executor is between publish
// points. Flows are sorted by container id for a deterministic payload; an empty
// feed yields a non-nil empty slice so the JSON is {"flows":[]}, never
// {"flows":null}.
func (r *Registry) Snapshot() Feed {
	r.mu.RLock()
	live := r.live
	flows := make([]Flow, 0, len(r.flows))
	seen := make(map[string]bool, len(r.flows))
	for id, f := range r.flows {
		flows = append(flows, f)
		seen[id] = true
	}
	r.mu.RUnlock()

	// live() reaches into the daemon's runner map, so it is called with r.mu
	// released — the feed lock must never be held across another subsystem's.
	if live != nil {
		for _, run := range live() {
			if run.ContainerID == "" || seen[run.ContainerID] {
				continue
			}
			seen[run.ContainerID] = true
			flows = append(flows, pendingFlow(run))
		}
	}

	sort.Slice(flows, func(i, j int) bool { return flows[i].ContainerID < flows[j].ContainerID })
	return Feed{Flows: flows, GeneratedAt: r.now().UTC()}
}

// pendingFlow is the placeholder for a RUNNING trading container whose executor
// has not published a plan yet. It reports the run's identity and nothing it
// cannot know: no leg, no hops, no projection. PlannedAt stays ZERO on purpose —
// stamping now would claim a plan snapshot that does not exist, and a zero time
// sorts below every real one so it cannot poison the viewer's lastPlanAt max.
func pendingFlow(run LiveRun) Flow {
	f := Flow{
		ContainerID:   run.ContainerID,
		Program:       run.Program,
		Ship:          run.Ship,
		Closed:        run.Closed,
		Cargo:         []CargoItem{},
		RemainingHops: []Hop{},
	}
	if run.Program == ProgramTour {
		// A tour's id IS its container id (buildTourFlow); arb and trade-route
		// carry no tour id.
		tourID := run.ContainerID
		f.TourID = &tourID
	}
	return f
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
