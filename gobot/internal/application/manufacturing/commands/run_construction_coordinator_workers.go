package commands

import (
	"sync"
	"time"
)

// supplyWorkers is the drain's registry of live supply workers.
//
// A supply worker OUTLIVES the tick that dispatched it — that is what keeps activation, the retry
// sweep and hull discovery on the tick's cadence rather than a haul's — so the bookkeeping a joined
// errgroup would give for free lives here instead: how many workers a container has out (the
// max_workers bound), which hulls they hold (so no later tick re-dispatches one), and how many
// supplies have landed since that container's last tick reported.
//
// Everything is keyed by container, so two coordinator containers sharing this one registered
// handler never spend each other's worker budget, harvest each other's completions, or block each
// other's stop. Hull ownership is deliberately GLOBAL: no two workers may hold one hull, whoever
// dispatched them.
//
// A registration is BOUNDED (supplyHold.expiresAt) because nothing else bounds it: this registry
// only knows what a worker tells it, so a worker that stops running without deregistering would
// retire its hull for the life of the process.
//
// In-process only. A restart loses the workers and this registry together, and the daemon's
// boot-time ReleaseAllActive returns their hulls to the idle pool.
type supplyWorkers struct {
	mu   sync.Mutex
	idle *sync.Cond
	// byHull maps a hull a worker is currently supplying with to that worker's registration.
	byHull map[string]supplyHold
	// reserved is the units of each material (pipeline+good) that in-flight workers are still
	// authorized to BUY. A delivered counter only moves once the server accepts a supply, so a load
	// already paid for but still in the air is invisible to it — and a later tick sizing its buys off
	// that counter would buy the same units again, against a bill the first load is about to meet.
	// Netting these out is fail-closed: a worker that has already delivered still holds its
	// reservation until it retires, so the drain can only ever under-plan by one tick, never over-buy.
	reserved map[string]int
	// completed counts, per container, the supplies that finished since that container last reported.
	completed map[string]int
	// admissions stamps each registration with a generation, so a hull's history is ordered.
	admissions uint64
}

// supplyHold is one worker's registration: the hull it holds, whose budget it spends, what it may
// buy, and how long the registry keeps believing the worker exists.
type supplyHold struct {
	hull        string
	containerID string
	material    string
	reserved    int
	// expiresAt is when the registry gives the worker up for dead and reclaims its hull. Set from the
	// worker's OWN task deadline plus a cleanup grace, so a haul still inside the deadline that bounds
	// it is never reaped — only a worker that has already blown its own contract.
	expiresAt time.Time
	// seq separates this registration from a later one on the SAME hull. A worker the reap gave up on
	// can still come back, and without a generation its cleanup would hand back a hull that by then
	// belongs to someone else.
	seq uint64
}

// admit registers hull under containerID until expiresAt, reserving units of material against the
// material's bill, and reports the registration's generation. A hull another worker already holds is
// REFUSED: the atomic backstop behind the pool-level exclusion, which matters because a re-claim by
// the SAME container is idempotent at the DB — ClaimShip hands a second worker the same hull.
func (w *supplyWorkers) admit(hull, containerID, material string, units int, expiresAt time.Time) (uint64, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.byHull == nil {
		w.byHull = make(map[string]supplyHold)
	}
	if _, held := w.byHull[hull]; held {
		return 0, false
	}
	w.admissions++
	w.byHull[hull] = supplyHold{
		hull:        hull,
		containerID: containerID,
		material:    material,
		reserved:    units,
		expiresAt:   expiresAt,
		seq:         w.admissions,
	}
	if units > 0 {
		if w.reserved == nil {
			w.reserved = make(map[string]int)
		}
		w.reserved[material] += units
	}
	return w.admissions, true
}

// retire hands the hull and its buy reservation back, and records whether the supply delivered. A
// registration the reap already took is NOT this worker's to hand back — the hull may be hauling for
// someone else by now — but a delivery it did land is real, and is still counted.
func (w *supplyWorkers) retire(hull, containerID string, seq uint64, delivered bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if hold, held := w.byHull[hull]; held && hold.seq == seq {
		w.releaseLocked(hold)
	}
	if delivered {
		if w.completed == nil {
			w.completed = make(map[string]int)
		}
		w.completed[containerID]++
	}
	w.signal().Broadcast()
}

// reap drops every registration whose worker has outlived its bound and returns them, so the caller
// can return their hulls to the idle pool. This is the registry's ONLY liveness guarantee: a worker
// can stop running without ever reaching its deregistration — wedged in a call that ignores ctx, or
// unwound past it — and an unbounded entry would retire its hull permanently while the drain kept
// reporting RUNNING on ever less capacity.
func (w *supplyWorkers) reap(now time.Time) []supplyHold {
	w.mu.Lock()
	defer w.mu.Unlock()
	var expired []supplyHold
	for _, hold := range w.byHull {
		if now.Before(hold.expiresAt) {
			continue
		}
		w.releaseLocked(hold)
		expired = append(expired, hold)
	}
	if len(expired) > 0 {
		w.signal().Broadcast()
	}
	return expired
}

// owns reports whether hull is still registered to the seq registration — false once the reap has
// given that worker up. A worker asks before it SPENDS: its buy reservation went back to the pool
// when it was reaped, and a later tick has re-planned against a bill that no longer nets it out.
func (w *supplyWorkers) owns(hull string, seq uint64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	hold, held := w.byHull[hull]
	return held && hold.seq == seq
}

// releasable reports whether the seq registration may release hull's claim: while the registration
// is still its own, and still once the reap has left the hull unregistered, an orphaned claim
// belonging back in the idle pool. False ONLY when a DIFFERENT worker now holds the hull, whose live
// haul must not be un-claimed from under it.
func (w *supplyWorkers) releasable(hull string, seq uint64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	hold, held := w.byHull[hull]
	return !held || hold.seq == seq
}

// releaseLocked drops a registration and the buy reservation it held. Callers hold w.mu.
func (w *supplyWorkers) releaseLocked(hold supplyHold) {
	delete(w.byHull, hold.hull)
	if hold.reserved > 0 {
		if w.reserved[hold.material] -= hold.reserved; w.reserved[hold.material] <= 0 {
			delete(w.reserved, hold.material)
		}
	}
}

// reserved is how many units of material in-flight workers may still buy — what a tick must net out
// of the material's outstanding bill before sizing its own buys.
func (w *supplyWorkers) reservedUnits(material string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reserved[material]
}

// reservationFor reports what hull's live worker is authorized to buy, and for which material.
// (0, "") once no worker holds the hull.
//
// The PER-HULL grain is what lets the commitment fold combine a reservation with the hull's actual
// cargo by MAX rather than by sum. The aggregate reservedUnits above cannot say which hull a
// reservation belongs to, so it cannot tell "reserved but not yet spent" from "reserved and already
// in that same hold" — and summing those starves the fleet of dispatch while its holds are full.
func (w *supplyWorkers) reservationFor(hull string) (int, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	hold, held := w.byHull[hull]
	if !held {
		return 0, ""
	}
	return hold.reserved, hold.material
}

// reservationsExcept totals, per material, the reservations of every worker whose hull is NOT in
// counted — the workers the caller's own hull-by-hull fold never saw.
//
// It is the fail-CLOSED floor under a cargo-derived commitment. Deriving from holds is what makes
// the count survive a restart and a lapsed registration, but it sees only hulls the ship
// repository returns: a row a read dropped, or a worker of another container holding a hull this
// drain's query does not surface. Those reservations are still real spend authority. Hulls the
// caller DELIBERATELY skipped must be passed in as counted, or a leg's exclusion of its own hull
// is undone by that hull's own reservation.
func (w *supplyWorkers) reservationsExcept(counted map[string]bool) map[string]int {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string]int)
	for hull, hold := range w.byHull {
		if counted[hull] || hold.reserved <= 0 {
			continue
		}
		out[hold.material] += hold.reserved
	}
	return out
}

// inFlight is how many workers containerID still has out — what the tick's max_workers budget is
// measured against.
func (w *supplyWorkers) inFlight(containerID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.countLocked(containerID)
}

// holds reports whether ANY worker is currently supplying with hull.
func (w *supplyWorkers) holds(hull string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, held := w.byHull[hull]
	return held
}

// harvest takes containerID's finished supplies and resets the counter, so each delivery is reported
// by exactly one tick and none is double-counted.
func (w *supplyWorkers) harvest(containerID string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	done := w.completed[containerID]
	delete(w.completed, containerID)
	return done
}

// wait blocks until containerID has no worker in flight.
func (w *supplyWorkers) wait(containerID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for w.countLocked(containerID) > 0 {
		w.signal().Wait()
	}
}

func (w *supplyWorkers) countLocked(containerID string) int {
	out := 0
	for _, hold := range w.byHull {
		if hold.containerID == containerID {
			out++
		}
	}
	return out
}

// signal lazily builds the retirement signal, so a zero-value registry needs no constructor.
// Callers hold w.mu.
func (w *supplyWorkers) signal() *sync.Cond {
	if w.idle == nil {
		w.idle = sync.NewCond(&w.mu)
	}
	return w.idle
}
