package commands

import "sync"

// supplyWorkers is the drain's registry of live supply workers.
//
// A supply worker OUTLIVES the tick that dispatched it — that is what keeps activation, the retry
// sweep and hull discovery on the tick's cadence instead of a haul's — so the bookkeeping a joined
// errgroup used to give for free has to live here: how many workers a container has out (the
// max_workers bound), which hulls they hold (so no later tick re-dispatches one), and how many
// supplies have landed since that container's last tick reported.
//
// Everything is keyed by container so two coordinator containers sharing this one registered handler
// never spend each other's worker budget, harvest each other's completions, or block each other's
// stop. Hull ownership is deliberately global: no two workers may hold one hull, whoever dispatched
// them.
//
// In-process only. A restart loses the workers and this registry together, and the daemon's
// boot-time ReleaseAllActive returns their hulls to the idle pool.
type supplyWorkers struct {
	mu   sync.Mutex
	idle *sync.Cond
	// byHull maps a hull a worker is currently supplying with to the container that dispatched it.
	byHull map[string]string
	// reserved is the units of each material (pipeline+good) that in-flight workers are still
	// authorized to BUY. A delivered counter only moves once the server accepts a supply, so a load
	// already paid for but still in the air is invisible to it — and a later tick sizing its buys off
	// that counter would buy the same units again, against a bill the first load is about to meet.
	// Netting these out is fail-closed: a worker that has already delivered still holds its
	// reservation until it retires, so the drain can only ever under-plan by one tick, never over-buy.
	reserved map[string]int
	// completed counts, per container, the supplies that finished since that container last reported.
	completed map[string]int
}

// admit registers hull under containerID, reserving units of material against the material's bill,
// and reports whether this worker took the hull. A hull another worker already holds is REFUSED:
// this is the atomic backstop behind the pool-level exclusion, and it matters because a re-claim by
// the SAME container is idempotent at the DB — ClaimShip would hand a second worker the same hull
// without complaint.
func (w *supplyWorkers) admit(hull, containerID, material string, units int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.byHull == nil {
		w.byHull = make(map[string]string)
	}
	if _, held := w.byHull[hull]; held {
		return false
	}
	w.byHull[hull] = containerID
	if units > 0 {
		if w.reserved == nil {
			w.reserved = make(map[string]int)
		}
		w.reserved[material] += units
	}
	return true
}

// retire hands the hull and its buy reservation back, and records whether the supply delivered.
func (w *supplyWorkers) retire(hull, containerID, material string, units int, delivered bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.byHull, hull)
	if units > 0 {
		if w.reserved[material] -= units; w.reserved[material] <= 0 {
			delete(w.reserved, material)
		}
	}
	if delivered {
		if w.completed == nil {
			w.completed = make(map[string]int)
		}
		w.completed[containerID]++
	}
	w.signal().Broadcast()
}

// reserved is how many units of material in-flight workers may still buy — what a tick must net out
// of the material's outstanding bill before sizing its own buys.
func (w *supplyWorkers) reservedUnits(material string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reserved[material]
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
	for _, owner := range w.byHull {
		if owner == containerID {
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
