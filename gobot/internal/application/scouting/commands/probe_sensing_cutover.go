package commands

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
)

// screenSweep re-screens the PENDING systems, bounded to screenSweepBatch per
// tick.
//
// PENDING is the ONLY verdict re-screened, and that is the whole cost model: a
// system judged IN_SCOPE has its placements and never needs judging again, and
// NO_WHITELIST is durable by design. Re-screening either would put the sweep's
// cost on the size of the known map rather than on the size of the frontier.
//
// A PENDING system whose waypoint CATALOG has never been swept is swept first,
// in-band. Without it the screen reads an unswept system's empty waypoint list
// as a fully-examined barren one — the same reading, opposite meaning — and the
// verdict it would record is durable AND makes the system a frontier propagation
// origin, so one wrong write-off walks outward across the map. The sweep is a
// paginated multi-call read metered under the charting envelope; it is bounded
// here by the batch above and happens once per system, and the pacer concedes
// charting's measured rate out of its own share (budgetInputs), so the spend is
// accounted rather than merely tolerated.
func (h *RunProbeSensingCoordinatorHandler) screenSweep(ctx context.Context, cyc sensingCycle) (int, error) {
	playerID := cyc.cmd.PlayerID.Value()
	pending, err := cyc.ports.Ledger.SystemsByVerdict(ctx, playerID, parkedsensing.VerdictPending)
	if err != nil {
		return 0, fmt.Errorf("failed to list systems awaiting screening: %w", err)
	}
	// LEAST-RECENTLY-SCREENED FIRST. This ordering is the whole fairness
	// property, because the batch below is a fixed cap over a queue that DOES
	// NOT DRAIN: a system still being charted screens back to PENDING, so it
	// stays in this list. Any order that is stable across ticks therefore gives
	// the list a PERMANENT head — the same batch re-screened forever, and
	// everything behind it never screened at all. Sorting by symbol did exactly
	// that: measured live, the five alphabetically-first PENDING systems were
	// the five most-recently-screened, 18 seconds old, while the alphabetic tail
	// had gone 12.2 hours untouched. An unscreened system is never judged.
	//
	// Because screened_at is restamped on EVERY verdict write (PENDING
	// included), screening a system moves it to the back, so this rotates: N
	// systems are fully covered in ceil(N/batch) ticks at unchanged cost per
	// tick.
	//
	// A NEVER-screened system sorts before every screened one. That is the
	// newly-discovered frontier, the case this sweep most needs to reach, and it
	// is why ScreenedAt is a pointer: NULL is answered here explicitly rather
	// than collapsing to the zero time and being ordered correctly by accident.
	//
	// The symbol tie-break makes the order TOTAL. sort.Slice is not stable, and
	// equal timestamps are readily produced, so without it the sweep's pick
	// would vary between runs on the same data — untestable, and starvation
	// creeps back the moment a group of systems shares a stamp.
	sort.Slice(pending, func(i, j int) bool {
		a, b := pending[i], pending[j]
		if (a.ScreenedAt == nil) != (b.ScreenedAt == nil) {
			return a.ScreenedAt == nil
		}
		if a.ScreenedAt != nil && !a.ScreenedAt.Equal(*b.ScreenedAt) {
			return a.ScreenedAt.Before(*b.ScreenedAt)
		}
		return a.System < b.System
	})

	screen := cyc.ports.screenPorts()
	screened := 0
	var failures []error
	for _, system := range pending {
		if screened >= screenSweepBatch {
			break
		}
		screened++

		known, kerr := cyc.ports.Waypoints.CatalogKnown(ctx, system.System)
		if kerr != nil {
			failures = append(failures, fmt.Errorf("failed to read whether the waypoint catalog of %q is known: %w", system.System, kerr))
			continue
		}
		if !known {
			if serr := cyc.ports.SeedShip.SyncWaypoints(ctx, playerID, system.System); serr != nil {
				// Named, not silent: a repeating sweep failure is otherwise
				// invisible and holds the system PENDING forever.
				common.LoggerFromContext(ctx).Log("WARNING", fmt.Sprintf("Failed to sweep the waypoint catalog of %s; it stays PENDING: %v", system.System, serr), map[string]interface{}{
					"action":        "parked_sensing_catalog_sweep_failed",
					"system_symbol": system.System,
				})
				continue
			}
		}

		if _, serr := parkedsensing.ScreenSystem(ctx, screen, playerID, system.System, cyc.cfg.Whitelist); serr != nil {
			failures = append(failures, serr)
		}
	}
	return screened, errors.Join(failures...)
}

// cutover retires the touring sensing model, once, on the first reconcile that
// finds an empty ledger. It reports how many scout posts it removed.
//
// THE ORDER IS THE WHOLE SAFETY PROPERTY, and it is driven by one fact: the
// re-fire trigger is an EMPTY sensing_systems table, and screening WRITES that
// table — every verdict, PENDING included. So the screen is the step that
// disarms the retry, and it therefore runs LAST, after everything irreversible
// has already committed:
//
//  1. Retire every scout post EXCEPT the home one. Home is kept because the
//     scout-post coordinator still mans it and bootstrap floor-protects it; the
//     rest are the touring model's and have no owner once this engine runs.
//  2. Adopt the orphaned probes those posts were manning as parked SPARE hulls
//     where they stand. Second, so "orphaned" is read against the posts that
//     actually survive rather than the ones about to be removed.
//  3. Screen every system the fleet already has market rows for, OFFLINE. The
//     map is already in the database; paying the API to rediscover it would cost
//     a full sweep's worth of calls for information we already hold.
//
// Screening first — the obvious reading order — is the bug this ordering exists
// to prevent. It commits the trigger-disarming write BEFORE the hulls are
// accounted for, so ANY failure after it (an unlistable posts table, a partial
// removal, an unreadable fleet, a crash) leaves scout-tagged hulls belonging to
// no post and no slot, with the retry already dead. Those hulls are invisible to
// the probe-cap count, so the engine buys replacements for probes it already
// owns — permanently, with no path back (see adoptOrphanProbes).
//
// Both steps that now run first are IDEMPOTENT, which is what makes the retry
// safe rather than merely possible: ListActive after a partial removal returns
// what is left (and removing an absent post is explicitly not an error), and
// adoption skips any hull already carrying the sensing tag.
func (h *RunProbeSensingCoordinatorHandler) cutover(ctx context.Context, cyc sensingCycle) (int, error) {
	logger := common.LoggerFromContext(ctx)
	playerID := cyc.cmd.PlayerID.Value()

	home, err := cyc.ports.Home.HomeSystem(ctx, playerID)
	if err != nil {
		// Without the home system every post looks retirable, including the one
		// the scout-post coordinator is actively manning. Refuse the cutover
		// rather than guess: nothing has been written, the ledger stays empty,
		// and the next tick retries from the top.
		return 0, fmt.Errorf("cutover refused: the home system is unreadable, so no scout post can be safely retired: %w", err)
	}

	var failures []error

	posts, err := h.postRepo.ListActive(ctx, playerID)
	if err != nil {
		// Nothing has been committed yet, so this really is a refusal: the
		// sensing ledger is still empty and the next tick re-enters here.
		return 0, fmt.Errorf("cutover refused: failed to list scout posts: %w", err)
	}
	removed := 0
	for _, post := range posts {
		if post.SystemSymbol == home {
			continue
		}
		if rerr := h.postRepo.Remove(ctx, playerID, post.SystemSymbol); rerr != nil {
			failures = append(failures, fmt.Errorf("failed to retire scout post %s: %w", post.SystemSymbol, rerr))
			continue
		}
		removed++
	}

	adopted := h.adoptOrphanProbes(ctx, cyc, &failures)

	// The trigger-disarming write, last. A failure anywhere above has left the
	// ledger empty, so the whole cutover is re-run next tick over the idempotent
	// remains of this one.
	if len(failures) > 0 {
		return removed, errors.Join(append(failures,
			errors.New("cutover incomplete: the offline screen was held back so the empty sensing ledger re-triggers it next tick"))...)
	}
	screened := h.cutoverScreen(ctx, cyc, &failures)
	if len(failures) > 0 {
		// The screen itself failed. The done-mark must NOT latch: it is in
		// memory, so latching it over an EMPTY ledger strands the engine until a
		// daemon restart — nothing else recovers it, because the steady-state
		// sweep re-screens only systems already carrying a PENDING row, and a
		// census that failed OUTRIGHT wrote none. That case is real and is what
		// this gate is for: a MarketDepthRows failure returns before a single
		// system is iterated, so nothing is written at all.
		//
		// A PARTIALLY screened ledger MOSTLY no longer needs the gate. It already
		// holds rows, so the cutover's own trigger will not re-enter whatever this
		// gate does — and usually it need not, because a system whose screen failed
		// carries a bare PENDING row of its own (markAwaitingScreening),
		// which is exactly what the steady-state sweep re-screens. Recovery is
		// SAME-TICK, not next-tick: the sweep runs later in this very reconcile,
		// so a system marked PENDING here is re-screened before the tick ends.
		//
		// The one system that stays uncovered is the one whose FALLBACK write also
		// failed — it has no row, so the sweep cannot see it either, and only
		// frontier propagation will reach it. That is rare, but not impossible.
		//
		// A legitimately EMPTY census is not a failure and does latch: screened
		// == 0 with no errors is a fleet that has simply not scanned anything
		// yet, and the expansion frontier is what grows the map from there.
		logger.Log("WARNING", fmt.Sprintf(
			"Parked-probe cutover screened %d system(s) before failing; the done-mark is withheld. A system whose screen failed carries a PENDING row the sweep picks up later in this same tick — except where the census read itself failed (nothing was written at all, so the empty ledger re-triggers the cutover next tick) or the fallback write ALSO failed (that system is named in its own error above and carries no row, so only frontier propagation reaches it)",
			screened), map[string]interface{}{
			"action":           "parked_sensing_cutover_screen_failed",
			"systems_screened": screened,
		})
		return removed, errors.Join(append(failures,
			errors.New("cutover incomplete: the offline screen did not finish"))...)
	}

	h.markCutoverDone(cyc.cmd.ContainerID)
	logger.Log("INFO", fmt.Sprintf(
		"Parked-probe cutover: screened %d system(s) offline, retired %d scout post(s) (home %s kept), adopted %d orphaned probe(s) as spares",
		screened, removed, home, adopted), map[string]interface{}{
		"action":           "parked_sensing_cutover",
		"systems_screened": screened,
		"posts_removed":    removed,
		"home_system":      home,
		"probes_adopted":   adopted,
	})
	// Every failure path returned above, so `failures` is necessarily empty here
	// and there is nothing to join — returning nil says that outright rather than
	// leaving a reader to check.
	return removed, nil
}

// cutoverScreen screens every system the market cache already knows about,
// without touching the API.
//
// The offline guarantee is enforced by SUBSTITUTION, not by convention: the
// screen's remote gap-fill port is replaced with one that refuses, so a market
// the cache cannot answer for leaves its system PENDING (verdictFor requires
// every market to resolve before recording the durable rejection) instead of
// firing a fetch. The steady-state sweep re-screens those PENDING systems with
// the real fetcher wired.
func (h *RunProbeSensingCoordinatorHandler) cutoverScreen(ctx context.Context, cyc sensingCycle, failures *[]error) int {
	playerID := cyc.cmd.PlayerID.Value()
	rows, err := h.depthReader.MarketDepthRows(ctx, playerID)
	if err != nil {
		*failures = append(*failures, fmt.Errorf("failed to read the market census for the cutover screen: %w", err))
		return 0
	}

	seen := make(map[string]bool, len(rows))
	systems := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.System == "" || seen[row.System] {
			continue
		}
		seen[row.System] = true
		systems = append(systems, row.System)
	}
	sort.Strings(systems)

	offline := cyc.ports.screenPorts()
	offline.RemoteMarket = offlineMarketFetcher{}

	screened := 0
	for _, system := range systems {
		if _, serr := parkedsensing.ScreenSystem(ctx, offline, playerID, system, cyc.cfg.Whitelist); serr != nil {
			*failures = append(*failures, serr)
			markAwaitingScreening(ctx, cyc.ports, playerID, system, failures)
			continue
		}
		screened++
	}
	return screened
}

// markAwaitingScreening leaves a bare PENDING row for a system whose screen
// failed, so the steady-state sweep adopts it.
//
// WITHOUT THIS ROW THE SYSTEM FALLS THROUGH EVERY RECOVERY PATH. ScreenSystem
// writes its sensing_systems row LAST and only on full success, so a failure
// mid-census leaves that system with no row while its siblings have theirs. The
// cutover's own re-fire trigger is an EMPTY ledger, which the siblings' rows
// disarm; and the steady-state sweep re-screens only systems that ALREADY carry
// a PENDING row, which this one does not. What is left is frontier propagation
// happening to reach it, which depends on the map's topology rather than on
// anything we control.
//
// THE ROW ASSERTS ONLY WHAT WE KNOW, WHICH IS NOTHING. Symbol and verdict, and
// no third thing: a depth or an uncharted count invented here would be a
// measurement we never took, and PENDING already means exactly "nobody has
// judged this yet". It is the same shape the expansion engine writes for a
// newly discovered frontier neighbour, and it is read the same way.
//
// It goes through UpsertSystem, the verdict-scoped writer, whose column list
// structurally excludes seed_ship and seed_state — so this can never clear a
// charting errand, however it is called (sensingSystemUpdateColumns).
//
// A fallback that ITSELF fails is collected rather than swallowed and rather
// than fatal: there is nothing further to try for this system, and the systems
// after it in the census are still perfectly screenable.
func markAwaitingScreening(ctx context.Context, ports SensingEnginePorts, playerID int, system string, failures *[]error) {
	if uerr := ports.Ledger.UpsertSystem(ctx, playerID, parkedsensing.SystemRecord{
		System:  system,
		Verdict: parkedsensing.VerdictPending,
	}); uerr != nil {
		*failures = append(*failures, fmt.Errorf(
			"failed to record %q as awaiting screening after its cutover screen failed (it now carries no row, so only frontier propagation can reach it): %w",
			system, uerr))
	}
}

// offlineMarketFetcher is the cutover's stand-in for the API gap fill: it
// refuses every call, which screenMarkets reads as "this market did not
// resolve" and which leaves the system PENDING rather than rejected.
type offlineMarketFetcher struct{}

// errOfflineScreen names the refusal in the one place it could otherwise look
// like a real outage.
var errOfflineScreen = errors.New("cutover screens from the local market cache only; no remote market fetch is made")

func (offlineMarketFetcher) FetchGoods(context.Context, int, string, string) ([]string, error) {
	return nil, errOfflineScreen
}

// cutoverAlreadyDone reports whether this container has already cut over.
//
// Named for what it RETURNS. It gates an irreversible bulk delete, and the
// previous name said "pending" while returning "done" — so the call site read as
// the opposite of what it did.
func (h *RunProbeSensingCoordinatorHandler) cutoverAlreadyDone(containerID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cutoverDone[containerID]
}

func (h *RunProbeSensingCoordinatorHandler) markCutoverDone(containerID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cutoverDone[containerID] = true
}
