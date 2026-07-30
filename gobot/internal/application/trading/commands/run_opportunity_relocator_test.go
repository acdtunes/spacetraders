package commands

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// run_opportunity_relocator_test.go — the opportunity relocator's driving port (sp-zvywu Part 2).
//
// Every test enters through Reconcile and asserts at the DRIVEN port boundary: which hulls the
// actuator was asked to move, and what the intent store holds. Nothing reaches inside the handler.

// ── driven-port fakes ───────────────────────────────────────────────────────────────────────────

type relocFakeFleet struct {
	hulls []RelocatorHull
	err   error
	// atActuation lets a test make the fleet answer DIFFERENTLY at the actuation re-read than it did
	// at observation — the only way to exercise the sp-x2jr6 race, where a hull is eligible when it is
	// scored and claimed by the time it would be moved. Absent for a ship ⇒ the re-read returns the
	// same hull the bulk observation did, so every pre-existing test is unaffected.
	atActuation map[string]RelocatorHull
	// reobserveErr makes the single-hull re-read fail, for the fail-closed case.
	reobserveErr error
	// reobserved records every ship symbol the handler re-read, so a test can assert the re-check
	// happens at all and happens once per commit.
	reobserved []string
}

func (f *relocFakeFleet) ObserveTradeHulls(context.Context, int) ([]RelocatorHull, error) {
	return f.hulls, f.err
}

func (f *relocFakeFleet) ObserveHull(_ context.Context, _ int, shipSymbol string) (RelocatorHull, error) {
	f.reobserved = append(f.reobserved, shipSymbol)
	if f.reobserveErr != nil {
		return RelocatorHull{}, f.reobserveErr
	}
	if hull, ok := f.atActuation[shipSymbol]; ok {
		return hull, nil
	}
	for _, hull := range f.hulls {
		if hull.ShipSymbol == shipSymbol {
			return hull, nil
		}
	}
	return RelocatorHull{}, fmt.Errorf("hull %s is no longer in the trade fleet", shipSymbol)
}

type relocFakeRegions struct {
	byOrigin map[string][]RelocatorRegion
	err      error
	radii    []int // every hopRadius the handler asked for, so a test can assert the knob is honoured
}

func (f *relocFakeRegions) ObserveRegions(_ context.Context, _ int, originSystem string, hopRadius int) ([]RelocatorRegion, error) {
	f.radii = append(f.radii, hopRadius)
	return f.byOrigin[originSystem], f.err
}

type relocFakeTelemetry struct {
	rows  []trading.TourLegTelemetry
	err   error
	since []time.Time
}

func (f *relocFakeTelemetry) ObserveTourTelemetry(_ context.Context, _ int, since time.Time) ([]trading.TourLegTelemetry, error) {
	f.since = append(f.since, since)
	return f.rows, f.err
}

type relocFakeEra struct {
	hours float64
	known bool
}

func (f *relocFakeEra) RemainingEraHours(context.Context, int) (float64, bool) {
	return f.hours, f.known
}

// relocMove is one actuator call, recorded so a test asserts the OBSERVABLE outcome: which hull was
// sent where.
type relocMove struct {
	ship     string
	waypoint string
}

type relocFakeActuator struct {
	moves    []relocMove
	failWith error
}

func (f *relocFakeActuator) RelocateHull(_ context.Context, _ int, shipSymbol, targetWaypoint string, _ int) error {
	f.moves = append(f.moves, relocMove{ship: shipSymbol, waypoint: targetWaypoint})
	return f.failWith
}

func (f *relocFakeActuator) movedShips() []string {
	ships := make([]string, 0, len(f.moves))
	for _, move := range f.moves {
		ships = append(ships, move.ship)
	}
	return ships
}

// relocFakeIntents is an in-memory intent store, one record per hull, exactly like the durable one.
type relocFakeIntents struct {
	records   map[string]RelocationIntent
	writes    []RelocationIntent
	loadErr   error
	recordErr error
}

func newRelocFakeIntents(seed ...RelocationIntent) *relocFakeIntents {
	store := &relocFakeIntents{records: map[string]RelocationIntent{}}
	for _, intent := range seed {
		store.records[intent.ShipSymbol] = intent
	}
	return store
}

func (f *relocFakeIntents) LoadRelocationIntents(context.Context, string, int) ([]RelocationIntent, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	out := make([]RelocationIntent, 0, len(f.records))
	for _, intent := range f.records {
		out = append(out, intent)
	}
	return out, nil
}

func (f *relocFakeIntents) RecordRelocationIntent(_ context.Context, _ string, _ int, intent RelocationIntent) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	f.writes = append(f.writes, intent)
	f.records[intent.ShipSymbol] = intent
	return nil
}

// ── fixture ─────────────────────────────────────────────────────────────────────────────────────

var relocNow = time.Date(2026, 7, 30, 20, 0, 0, 0, time.UTC)

// relocHarness wires the handler to fakes. Defaults describe a fleet where relocation SHOULD happen,
// so each test perturbs exactly one thing and the assertion isolates that one guard.
type relocHarness struct {
	fleet     *relocFakeFleet
	regions   *relocFakeRegions
	telemetry *relocFakeTelemetry
	era       *relocFakeEra
	actuator  *relocFakeActuator
	intents   *relocFakeIntents
	clock     *shared.MockClock
	handler   *RunOpportunityRelocatorHandler
	cmd       *RunOpportunityRelocatorCommand
}

// relocTelemetryFor emits one completed tour for a hull at the given realized rate: 100 units bought
// at 0 and sold at `ratePerHour/100`, over exactly one hour, so net/hours == ratePerHour.
func relocTelemetryFor(ship string, ratePerHour int, finishedAt time.Time) []trading.TourLegTelemetry {
	start := finishedAt.Add(-time.Hour)
	return []trading.TourLegTelemetry{
		{TourID: "tour-" + ship, ShipSymbol: ship, Good: "FUEL", IsBuy: true, RealizedUnits: 100, RealizedUnitPrice: 0, PlannedAt: start, RealizedAt: start},
		{TourID: "tour-" + ship, ShipSymbol: ship, Good: "FUEL", IsBuy: false, RealizedUnits: 100, RealizedUnitPrice: ratePerHour / 100, PlannedAt: start, RealizedAt: finishedAt},
	}
}

// newRelocHarness builds the baseline: one eligible hull HAULER-A at X1-HOME earning 100,000/hr, and
// one fresh region X1-RICH two gate hops away projecting 400,000/hr. 48 h of era remain.
func newRelocHarness(t *testing.T) *relocHarness {
	t.Helper()
	h := &relocHarness{
		fleet: &relocFakeFleet{hulls: []RelocatorHull{
			{ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME"},
		}},
		regions: &relocFakeRegions{byOrigin: map[string][]RelocatorRegion{
			"X1-HOME": {relocRegion("X1-RICH", 2, 400_000)},
		}},
		telemetry: &relocFakeTelemetry{rows: relocTelemetryFor("HAULER-A", 100_000, relocNow.Add(-30*time.Minute))},
		era:       &relocFakeEra{hours: 48, known: true},
		actuator:  &relocFakeActuator{},
		intents:   newRelocFakeIntents(),
		clock:     &shared.MockClock{CurrentTime: relocNow},
	}
	h.handler = NewRunOpportunityRelocatorHandler(h.fleet, h.regions, h.telemetry, h.era, h.actuator, h.intents, nil, h.clock)
	h.cmd = &RunOpportunityRelocatorCommand{PlayerID: 1, ContainerID: "relocator-1"}
	return h
}

// relocRegion is a FRESH, readable candidate region.
func relocRegion(system string, hops int, projectedRate float64) RelocatorRegion {
	return RelocatorRegion{
		AnchorSystem:    system,
		LandingWaypoint: system + "-MARKET",
		GateHops:        hops,
		ProjectedRate:   projectedRate,
		RateReadable:    true,
		SnapshotAge:     5 * time.Minute,
		Activity:        "STRONG", // the tightest freshness cap (30 min), so "fresh" is not an artefact of a loose cap
	}
}

func (h *relocHarness) reconcile(t *testing.T) *RelocatorTickResult {
	t.Helper()
	result, err := h.handler.Reconcile(context.Background(), h.cmd)
	if err != nil {
		t.Fatalf("Reconcile returned an error: %v", err)
	}
	return result
}

func relocRequireMoved(t *testing.T, actuator *relocFakeActuator, want ...string) {
	t.Helper()
	got := actuator.movedShips()
	if len(got) != len(want) {
		t.Fatalf("relocated %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("relocated %v, want %v", got, want)
		}
	}
}

func relocRequireNoMove(t *testing.T, actuator *relocFakeActuator, why string) {
	t.Helper()
	if len(actuator.moves) != 0 {
		t.Fatalf("%s but the relocator moved %v", why, actuator.movedShips())
	}
}

// ── THE REGRESSION PAIR ─────────────────────────────────────────────────────────────────────────

// A hull earning measurably below a reachable region's projected rate, at honest tour release, with
// positive NPV, GETS RELOCATED.
func TestOpportunityRelocatorShould_RelocateAHullToAReachableRegionProjectingMeasurablyMore(t *testing.T) {
	h := newRelocHarness(t)

	result := h.reconcile(t)

	relocRequireMoved(t, h.actuator, "HAULER-A")
	if h.actuator.moves[0].waypoint != "X1-RICH-MARKET" {
		t.Fatalf("relocated to waypoint %q, want X1-RICH-MARKET (the region's landing waypoint)", h.actuator.moves[0].waypoint)
	}
	if len(result.Relocated) != 1 || result.Relocated[0] != "HAULER-A" {
		t.Fatalf("tick reported Relocated %v, want [HAULER-A]", result.Relocated)
	}
}

// The SAME hull with NPV below the threshold does NOT get relocated.
func TestOpportunityRelocatorShould_NotRelocateAHullWhoseNPVIsBelowTheThreshold(t *testing.T) {
	h := newRelocHarness(t)
	// The baseline move's NPV: uplift 300,000/hr x 24 h window - 100,000 x travel - 100,000 risk.
	// A threshold of 100,000,000 is far above any 24 h uplift, so only the threshold can refuse it.
	h.cmd.NPVThresholdCredits = 100_000_000

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "the move's NPV is far below the configured threshold")
	if result.Skipped[string(trading.RelocationRefusedBelowThreshold)] != 1 {
		t.Fatalf("expected one below-threshold refusal, got skips %v", result.Skipped)
	}
	if len(h.intents.writes) != 0 {
		t.Fatalf("a refused relocation wrote %d intents; nothing may be persisted for a move that does not happen", len(h.intents.writes))
	}
}

// ── caps and cooldowns ──────────────────────────────────────────────────────────────────────────

// max_concurrent_relocations must BIND. The fixture supplies FOUR eligible hulls, each with its own
// strong region, against a cap of 2 — strictly more candidates than the cap, so the cap is genuinely
// consulted rather than passing vacuously.
func TestOpportunityRelocatorShould_RelocateNoMoreHullsThanTheConcurrencyCap(t *testing.T) {
	h := newRelocHarness(t)
	h.cmd.MaxConcurrentRelocations = 2

	var telemetry []trading.TourLegTelemetry
	h.fleet.hulls = nil
	h.regions.byOrigin = map[string][]RelocatorRegion{}
	for _, hull := range []struct {
		ship, system, region string
		projected            float64
	}{
		{"HAULER-A", "X1-A", "X1-RICH-A", 400_000},
		{"HAULER-B", "X1-B", "X1-RICH-B", 500_000},
		{"HAULER-C", "X1-C", "X1-RICH-C", 600_000},
		{"HAULER-D", "X1-D", "X1-RICH-D", 700_000},
	} {
		h.fleet.hulls = append(h.fleet.hulls, RelocatorHull{ShipSymbol: hull.ship, CurrentSystem: hull.system})
		h.regions.byOrigin[hull.system] = []RelocatorRegion{relocRegion(hull.region, 2, hull.projected)}
		telemetry = append(telemetry, relocTelemetryFor(hull.ship, 100_000, relocNow.Add(-30*time.Minute))...)
	}
	h.telemetry.rows = telemetry

	result := h.reconcile(t)

	if len(h.actuator.moves) != 2 {
		t.Fatalf("relocated %v (%d hulls) against a cap of 2 — four licensed candidates were available, so the cap must bind", h.actuator.movedShips(), len(h.actuator.moves))
	}
	// And it must spend the budget on the BEST candidates: D (700k) then C (600k).
	relocRequireMoved(t, h.actuator, "HAULER-D", "HAULER-C")
	if result.Skipped["at_concurrency_cap"] == 0 {
		t.Fatalf("the cap bound but no at_concurrency_cap skip was recorded; skips %v", result.Skipped)
	}
}

// ANTI-HERD per region, on the SAME knob the reposition-reach discovery uses. Three eligible hulls
// all rank the same region best, against a per-system cap of 1 that one landed hull already fills.
func TestOpportunityRelocatorShould_NotHerdMoreHullsOntoARegionThanItsPerSystemCap(t *testing.T) {
	h := newRelocHarness(t)
	h.cmd.RepositionReachMaxHullsPerSystem = 1
	h.cmd.MaxConcurrentRelocations = 5 // so only the anti-herd cap can refuse

	h.fleet.hulls = []RelocatorHull{
		{ShipSymbol: "HAULER-A", CurrentSystem: "X1-A"},
		{ShipSymbol: "HAULER-B", CurrentSystem: "X1-B"},
		{ShipSymbol: "HAULER-C", CurrentSystem: "X1-C"},
	}
	crowded := relocRegion("X1-RICH", 2, 400_000)
	h.regions.byOrigin = map[string][]RelocatorRegion{
		"X1-A": {crowded}, "X1-B": {crowded}, "X1-C": {crowded},
	}
	var telemetry []trading.TourLegTelemetry
	for _, ship := range []string{"HAULER-A", "HAULER-B", "HAULER-C"} {
		telemetry = append(telemetry, relocTelemetryFor(ship, 100_000, relocNow.Add(-30*time.Minute))...)
	}
	h.telemetry.rows = telemetry

	// With a cap of 1 and no hull yet in X1-RICH, exactly ONE of the three may go.
	result := h.reconcile(t)

	if len(h.actuator.moves) != 1 {
		t.Fatalf("relocated %v onto X1-RICH against a per-system cap of 1 — three hulls ranked it best, so the anti-herd cap must bind", h.actuator.movedShips())
	}
	if result.Skipped["region_herded"] == 0 {
		t.Fatalf("the anti-herd cap bound but no region_herded skip was recorded; skips %v", result.Skipped)
	}
}

// A region ALREADY at its cap must be excluded before it is ever scored.
func TestOpportunityRelocatorShould_ExcludeARegionAlreadyAtItsPerSystemCap(t *testing.T) {
	h := newRelocHarness(t)
	h.cmd.RepositionReachMaxHullsPerSystem = 1
	// A landed trade hull already occupies X1-RICH, filling the cap of 1.
	h.fleet.hulls = append(h.fleet.hulls, RelocatorHull{ShipSymbol: "HAULER-SITTING", CurrentSystem: "X1-RICH"})
	h.telemetry.rows = append(h.telemetry.rows, relocTelemetryFor("HAULER-SITTING", 900_000, relocNow.Add(-30*time.Minute))...)

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "X1-RICH already holds a trade hull at a per-system cap of 1")
	if result.Skipped["region_herded"] == 0 {
		t.Fatalf("expected a region_herded skip for the already-full region; skips %v", result.Skipped)
	}
}

// PER-HULL COOLDOWN, read from the persisted intent so it survives a restart.
func TestOpportunityRelocatorShould_NotRelocateAHullInsideItsCooldown(t *testing.T) {
	h := newRelocHarness(t)
	h.cmd.CooldownMinutes = 90
	h.intents = newRelocFakeIntents(RelocationIntent{
		ShipSymbol: "HAULER-A", FromSystem: "X1-OLD", TargetSystem: "X1-HOME", TargetWaypoint: "X1-HOME-MARKET",
		DecidedAt: relocNow.Add(-30 * time.Minute), Completed: true,
	})
	h.handler = NewRunOpportunityRelocatorHandler(h.fleet, h.regions, h.telemetry, h.era, h.actuator, h.intents, nil, h.clock)

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "HAULER-A relocated 30 minutes ago inside a 90-minute cooldown")
	if result.Skipped["within_cooldown"] != 1 {
		t.Fatalf("expected one within_cooldown skip; skips %v", result.Skipped)
	}

	// Past the cooldown the SAME hull moves — proof the cooldown is a clock, not a permanent block.
	h.clock.CurrentTime = relocNow.Add(2 * time.Hour)
	if got := h.reconcile(t); len(got.Relocated) != 1 {
		t.Fatalf("the same hull was still refused two hours later, past its 90-minute cooldown; skips %v", got.Skipped)
	}
}

// ── fail-closed exclusions ──────────────────────────────────────────────────────────────────────

// A STALE or UNREADABLE region is EXCLUDED, never scored optimistically. Staleness is measured
// per-activity through RankerAgeCaps: a STRONG market's 30-minute cap, not a flat threshold.
func TestOpportunityRelocatorShould_ExcludeAStaleOrUnreadableRegionRatherThanScoreItOptimistically(t *testing.T) {
	for name, region := range map[string]RelocatorRegion{
		"a region whose projection is unreadable": {
			AnchorSystem: "X1-RICH", LandingWaypoint: "X1-RICH-MARKET", GateHops: 2,
			ProjectedRate: 400_000, RateReadable: false, SnapshotAge: time.Minute, Activity: "STRONG",
		},
		"a STRONG-market region past its 30-minute freshness cap": {
			AnchorSystem: "X1-RICH", LandingWaypoint: "X1-RICH-MARKET", GateHops: 2,
			ProjectedRate: 400_000, RateReadable: true, SnapshotAge: 31 * time.Minute, Activity: "STRONG",
		},
	} {
		h := newRelocHarness(t)
		h.regions.byOrigin["X1-HOME"] = []RelocatorRegion{region}

		h.reconcile(t)

		relocRequireNoMove(t, h.actuator, name+" must be excluded, not scored at its last-seen prices")
	}
}

// The freshness cap is PER-ACTIVITY, not flat: the same 31-minute-old snapshot that disqualifies a
// STRONG market is perfectly rankable on a WEAK one (an 8 h cap). Otherwise a flat cap would be
// hiding behind the exclusion above.
func TestOpportunityRelocatorShould_AdmitAWeakMarketSnapshotThatWouldBeTooStaleForAStrongOne(t *testing.T) {
	h := newRelocHarness(t)
	weak := relocRegion("X1-RICH", 2, 400_000)
	weak.Activity = "WEAK"
	weak.SnapshotAge = 31 * time.Minute // past STRONG's 30 min cap, far inside WEAK's 8 h
	h.regions.byOrigin["X1-HOME"] = []RelocatorRegion{weak}

	if got := h.reconcile(t); len(got.Relocated) != 1 {
		t.Fatalf("a 31-minute-old WEAK-market region was excluded; the freshness cap is being applied flat instead of per-activity. skips %v", got.Skipped)
	}
}

// A hull whose own realized rate is UNREADABLE is never moved: the trip's opportunity cost and the
// uplift ratio are both undefined without it.
func TestOpportunityRelocatorShould_NotRelocateAHullWhoseRealizedRateIsUnreadable(t *testing.T) {
	h := newRelocHarness(t)
	h.telemetry.rows = nil // no completed tour for HAULER-A

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "HAULER-A has no computable realized rate")
	if result.Skipped["hull_rate_unreadable"] != 1 {
		t.Fatalf("expected one hull_rate_unreadable skip; skips %v", result.Skipped)
	}
}

// THE ENDGAME GUARD, through the driving port: too little era left for the move to pay for itself.
func TestOpportunityRelocatorShould_RefuseToRelocateWhenTooLittleEraRemainsToRepayTheMove(t *testing.T) {
	h := newRelocHarness(t)
	// travel for 2 gate hops = (750 + 650*2)/3600 = 0.5694 h. uplift 300,000/hr,
	// risk margin = 100,000 (one hour of the hull's own earnings).
	// payback = (100,000 x 0.5694 + 100,000) / 300,000 = 0.5231 h.
	// The guard needs 2 x 0.5694 + 0.5231 = 1.662 h of era.
	h.era = &relocFakeEra{hours: 1.5, known: true}
	h.handler = NewRunOpportunityRelocatorHandler(h.fleet, h.regions, h.telemetry, h.era, h.actuator, h.intents, nil, h.clock)

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "only 1.5 h of era remain against the 1.66 h the move needs to repay itself")
	if result.Skipped[string(trading.RelocationRefusedEndgame)] == 0 {
		t.Fatalf("expected an endgame refusal; skips %v", result.Skipped)
	}

	// With more era left the same move clears — the guard measures the era budget, it does not just
	// refuse everything.
	h.era.hours = 6
	if got := h.reconcile(t); len(got.Relocated) != 1 {
		t.Fatalf("the same move was refused with 6 h of era left; skips %v", got.Skipped)
	}
}

// ── protected and busy hulls ────────────────────────────────────────────────────────────────────

// NEVER MID-TOUR. A touring hull is skipped and reconsidered later; only honest release qualifies.
func TestOpportunityRelocatorShould_NeverRelocateAHullMidTour(t *testing.T) {
	h := newRelocHarness(t)
	h.fleet.hulls = []RelocatorHull{{ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME", OnTour: true}}

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "HAULER-A is mid-tour")
	if result.Skipped["mid_tour"] != 1 {
		t.Fatalf("expected one mid_tour skip; skips %v", result.Skipped)
	}

	// At honest release the same hull, unchanged in every other respect, DOES move.
	h.fleet.hulls[0].OnTour = false
	if got := h.reconcile(t); len(got.Relocated) != 1 {
		t.Fatalf("the same hull was refused at honest tour release; skips %v", got.Skipped)
	}
}

// RULINGS #7: "the ownership model is law. Pinned/dedicated hulls are never poached ...; the command
// frigate hauls only as last resort." Both classes are dropped at observation.
func TestOpportunityRelocatorShould_NeverRelocateTheCommandFrigateOrAPinnedHull(t *testing.T) {
	for name, hull := range map[string]RelocatorHull{
		"the command frigate": {ShipSymbol: "COMMAND-1", CurrentSystem: "X1-HOME", IsCommandFrigate: true},
		"a pinned hull":       {ShipSymbol: "PINNED-1", CurrentSystem: "X1-HOME", Pinned: true},
	} {
		h := newRelocHarness(t)
		h.fleet.hulls = []RelocatorHull{hull}
		// Give it the strongest possible economic case, so ONLY the protection can refuse it.
		h.telemetry.rows = relocTelemetryFor(hull.ShipSymbol, 100, relocNow.Add(-30*time.Minute))
		h.regions.byOrigin["X1-HOME"] = []RelocatorRegion{relocRegion("X1-RICH", 1, 5_000_000)}

		h.reconcile(t)

		relocRequireNoMove(t, h.actuator, name+" is protected by RULINGS #7 and must never be relocated, however good the ground")
	}
}

// The shared operator kill-switch halts this reconciler too: one stand-down stops ALL auto-relocation.
func TestOpportunityRelocatorShould_HaltEntirelyOnTheSharedRepositionKillSwitch(t *testing.T) {
	h := newRelocHarness(t)
	h.cmd.RepositionDisabled = true

	h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "the shared reposition kill-switch is engaged")
	if len(h.intents.writes) != 0 {
		t.Fatalf("the kill-switch still allowed %d intent writes", len(h.intents.writes))
	}
}

// ── restart safety ──────────────────────────────────────────────────────────────────────────────

// A RESTART RE-DERIVES INTENTS WITHOUT DOUBLE-RELOCATING. The hull is already AT its persisted
// target: the move landed before the crash, so the intent is completed, NOT re-decided — and the
// hull's cooldown starts from the original decision.
func TestOpportunityRelocatorShould_CompleteALandedIntentOnRestartWithoutRelocatingAgain(t *testing.T) {
	h := newRelocHarness(t)
	// Post-restart live state: HAULER-A sits in X1-RICH, exactly where the persisted intent sent it.
	h.fleet.hulls = []RelocatorHull{{ShipSymbol: "HAULER-A", CurrentSystem: "X1-RICH"}}
	h.regions.byOrigin["X1-RICH"] = []RelocatorRegion{relocRegion("X1-RICHER", 2, 900_000)}
	h.telemetry.rows = relocTelemetryFor("HAULER-A", 100_000, relocNow.Add(-30*time.Minute))
	h.intents = newRelocFakeIntents(RelocationIntent{
		ShipSymbol: "HAULER-A", FromSystem: "X1-HOME", TargetSystem: "X1-RICH", TargetWaypoint: "X1-RICH-MARKET",
		DecidedAt: relocNow.Add(-5 * time.Minute), Completed: false,
	})
	h.handler = NewRunOpportunityRelocatorHandler(h.fleet, h.regions, h.telemetry, h.era, h.actuator, h.intents, nil, h.clock)

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "the persisted intent had already landed, so nothing needed moving")
	if len(result.Relocated) != 0 {
		t.Fatalf("a restart re-relocated %v; a landed intent must be completed, never re-decided", result.Relocated)
	}
	if !h.intents.records["HAULER-A"].Completed {
		t.Fatal("the landed intent was not marked completed; the next tick would resume a move that already finished")
	}
	if got := h.intents.records["HAULER-A"].DecidedAt; !got.Equal(relocNow.Add(-5 * time.Minute)) {
		t.Fatalf("completing the intent moved DecidedAt to %v; the ORIGINAL decision time is the restart-durable cooldown clock", got)
	}
	if result.Skipped["already_relocating"] != 1 {
		t.Fatalf("expected the just-settled hull to be excluded from this tick's scoring; skips %v", result.Skipped)
	}
}

// A restart mid-FLIGHT resumes the SAME target rather than re-scoring the hull from wherever it
// happened to be interrupted — re-deciding there is the double-relocation bug.
func TestOpportunityRelocatorShould_ResumeAnInterruptedMoveTowardItsOriginalTarget(t *testing.T) {
	h := newRelocHarness(t)
	// The hull is still at home; the persisted intent aimed it at X1-RICH. A far better region is now
	// visible, which a re-DECISION would chase instead.
	h.regions.byOrigin["X1-HOME"] = []RelocatorRegion{relocRegion("X1-RICHER", 1, 5_000_000)}
	h.intents = newRelocFakeIntents(RelocationIntent{
		ShipSymbol: "HAULER-A", FromSystem: "X1-HOME", TargetSystem: "X1-RICH", TargetWaypoint: "X1-RICH-MARKET",
		DecidedAt: relocNow.Add(-5 * time.Minute), Completed: false,
	})
	h.handler = NewRunOpportunityRelocatorHandler(h.fleet, h.regions, h.telemetry, h.era, h.actuator, h.intents, nil, h.clock)

	result := h.reconcile(t)

	relocRequireMoved(t, h.actuator, "HAULER-A")
	if h.actuator.moves[0].waypoint != "X1-RICH-MARKET" {
		t.Fatalf("resumed toward %q, want the ORIGINAL target X1-RICH-MARKET; a restart must not re-decide against a map that has moved", h.actuator.moves[0].waypoint)
	}
	if len(result.Resumed) != 1 || result.Resumed[0] != "HAULER-A" {
		t.Fatalf("tick reported Resumed %v, want [HAULER-A]", result.Resumed)
	}
	if len(result.Relocated) != 0 {
		t.Fatalf("a resumed move was also counted as a new relocation %v — that is the double-relocation it exists to prevent", result.Relocated)
	}
}

// An in-flight intent consumes concurrency budget, so a restart cannot authorize MORE moves on top of
// the work already authorized.
func TestOpportunityRelocatorShould_CountInFlightIntentsAgainstTheConcurrencyCap(t *testing.T) {
	h := newRelocHarness(t)
	h.cmd.MaxConcurrentRelocations = 1
	h.fleet.hulls = []RelocatorHull{
		{ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME"},
		{ShipSymbol: "HAULER-B", CurrentSystem: "X1-B"},
	}
	h.regions.byOrigin["X1-B"] = []RelocatorRegion{relocRegion("X1-RICH-B", 2, 900_000)}
	h.telemetry.rows = append(h.telemetry.rows, relocTelemetryFor("HAULER-B", 100_000, relocNow.Add(-30*time.Minute))...)
	// HAULER-A's move is already in flight and lands this tick, consuming the only slot.
	h.intents = newRelocFakeIntents(RelocationIntent{
		ShipSymbol: "HAULER-A", FromSystem: "X1-OLD", TargetSystem: "X1-HOME", TargetWaypoint: "X1-HOME-MARKET",
		DecidedAt: relocNow.Add(-5 * time.Minute), Completed: false,
	})
	h.handler = NewRunOpportunityRelocatorHandler(h.fleet, h.regions, h.telemetry, h.era, h.actuator, h.intents, nil, h.clock)

	result := h.reconcile(t)

	if len(result.Relocated) != 0 {
		t.Fatalf("relocated %v while one move was already in flight against a cap of 1 — an in-flight intent must consume budget", result.Relocated)
	}
	if result.Skipped["at_concurrency_cap"] == 0 {
		t.Fatalf("expected an at_concurrency_cap skip; skips %v", result.Skipped)
	}
}

// PERSIST BEFORE MOVING. A hull is never sent anywhere the store could not record, or a crash
// mid-flight would leave an unattributed hull in transit and the next tick would re-decide it.
func TestOpportunityRelocatorShould_NotMoveAHullWhenTheIntentCannotBePersisted(t *testing.T) {
	h := newRelocHarness(t)
	h.intents.recordErr = errors.New("intent store unavailable")

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "the relocation intent could not be persisted")
	// COUNTED, not merely logged. A licensed relocation that did not happen is what the stall verdict
	// escalates on, so a commit failure that records nothing is invisible to the escalator and to the
	// heartbeat alike — which is how this whole class of loss stayed unmeasured (sp-j1i49).
	if result.Skipped[skipReasonIntentPersistFailed] != 1 {
		t.Fatalf("an unpersistable intent recorded no skip; skips %v", result.Skipped)
	}
}

// A failed move leaves the intent UNCOMPLETED so the next tick resumes it, and does not report a
// relocation that did not happen.
func TestOpportunityRelocatorShould_LeaveAFailedMovesIntentInFlightForTheNextTick(t *testing.T) {
	h := newRelocHarness(t)
	h.actuator.failWith = errors.New("jump failed")

	result := h.reconcile(t)

	if len(result.Relocated) != 0 {
		t.Fatalf("a failed move was reported as relocated %v", result.Relocated)
	}
	if result.Skipped[skipReasonRelocateFailed] != 1 {
		t.Fatalf("a failed jump recorded no skip, so the escalator and the heartbeat both stay blind to it; skips %v", result.Skipped)
	}
	intent, seen := h.intents.records["HAULER-A"]
	if !seen {
		t.Fatal("a failed move left no persisted intent; the hull's in-flight state would be lost")
	}
	if intent.Completed {
		t.Fatal("a failed move marked its intent COMPLETED; the next tick would never resume the interrupted jump")
	}
}

// ── knob wiring ─────────────────────────────────────────────────────────────────────────────────

// The region radius the bead specifies (system + 2-hop neighbours) must actually reach the observer.
func TestOpportunityRelocatorShould_AskForTheConfiguredRegionHopRadius(t *testing.T) {
	h := newRelocHarness(t)

	h.reconcile(t)
	if len(h.regions.radii) != 1 || h.regions.radii[0] != defaultRelocatorRegionHopRadius {
		t.Fatalf("asked for radii %v, want the default %d (the bead's system + 2-hop neighbourhood)", h.regions.radii, defaultRelocatorRegionHopRadius)
	}

	// A FRESH harness: the first reconcile above relocated HAULER-A, so reusing it would hit the
	// per-hull cooldown and never reach the region observer at all.
	configured := newRelocHarness(t)
	configured.cmd.RegionHopRadius = 4
	configured.reconcile(t)
	if len(configured.regions.radii) != 1 || configured.regions.radii[0] != 4 {
		t.Fatalf("asked for radii %v after configuring 4", configured.regions.radii)
	}
}

// The uplift bar clamps UP only: a weakened configuration is treated as the hard floor.
func TestOpportunityRelocatorShould_ClampAWeakenedUpliftBarUpToItsHardFloor(t *testing.T) {
	h := newRelocHarness(t)
	h.cmd.UpliftBarPct = 105 // an operator trying to weaken the ratchet to 1.05x
	// A region projecting 1.2x the hull's rate clears 1.05x but not the 1.5x floor.
	h.regions.byOrigin["X1-HOME"] = []RelocatorRegion{relocRegion("X1-RICH", 2, 120_000)}

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "a 1.2x region must not clear the clamped 1.5x uplift floor")
	if result.Skipped[string(trading.RelocationRefusedNoUplift)] == 0 {
		t.Fatalf("expected a no_uplift refusal under the clamped floor; skips %v", result.Skipped)
	}
}

// THE REFIT SEAM. Injecting a different TravelHopModel must change the priced travel — and therefore
// the decision — with no change to any scoring code.
func TestOpportunityRelocatorShould_PriceTravelThroughTheInjectedHopModel(t *testing.T) {
	// Two identical fixtures differing ONLY in the injected hop model, with 1.5 h of era left and the
	// NPV floor set aside so the travel model is the sole discriminator.
	//
	//	expensive (clamped to 1800 + 2x1800 s = 1.5 h/crossing): 2 x 1.5 h of travel alone
	//	  exceeds the 1.5 h of era -> the endgame guard refuses.
	//	cheap (300 + 2x300 s = 0.25 h/crossing): payback 0.4167 h, guard needs
	//	  2 x 0.25 + 0.4167 = 0.9167 h < 1.5 h -> licensed.
	withModel := func(t *testing.T, model trading.TravelHopModel) *relocHarness {
		t.Helper()
		h := newRelocHarness(t)
		h.era = &relocFakeEra{hours: 1.5, known: true}
		h.cmd.NPVThresholdCredits = 1
		h.handler = NewRunOpportunityRelocatorHandler(h.fleet, h.regions, h.telemetry, h.era, h.actuator, h.intents, model, h.clock)
		return h
	}

	expensive := withModel(t, trading.AffineHopModel{BaseSeconds: 1_000_000, PerHopSeconds: 1_000_000})
	expensive.reconcile(t)
	relocRequireNoMove(t, expensive.actuator, "an expensive refitted hop model makes the crossing unaffordable in the remaining era")

	cheap := withModel(t, trading.AffineHopModel{BaseSeconds: 300, PerHopSeconds: 300})
	if got := cheap.reconcile(t); len(got.Relocated) != 1 {
		t.Fatalf("a cheap refitted hop model did not license the same move; the injected model is not reaching the valuation. skips %v", got.Skipped)
	}
}

// The EWMA window the bead specifies must actually bound the telemetry read.
func TestOpportunityRelocatorShould_ReadTelemetryOverTheConfiguredWindow(t *testing.T) {
	h := newRelocHarness(t)
	h.cmd.RateWindowMinutes = 120

	h.reconcile(t)

	if len(h.telemetry.since) != 1 {
		t.Fatalf("telemetry read %d times, want exactly 1 per tick", len(h.telemetry.since))
	}
	if want := relocNow.Add(-120 * time.Minute); !h.telemetry.since[0].Equal(want) {
		t.Fatalf("telemetry window opened at %v, want %v (120 configured minutes before now)", h.telemetry.since[0], want)
	}
}

// ── unreadable collaborators ────────────────────────────────────────────────────────────────────

// An unreadable region set for a hull scores NOTHING for that hull rather than defaulting.
func TestOpportunityRelocatorShould_ScoreNothingForAHullWhoseRegionsAreUnreadable(t *testing.T) {
	h := newRelocHarness(t)
	h.regions.err = errors.New("gate graph unavailable")

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "the region set was unreadable")
	if result.Skipped["regions_unreadable"] != 1 {
		t.Fatalf("expected one regions_unreadable skip; skips %v", result.Skipped)
	}
}

// Unreadable telemetry makes every hull rate unprovable, so nothing moves — fail closed by
// construction rather than by a special case.
func TestOpportunityRelocatorShould_RelocateNothingWhenTelemetryIsUnreadable(t *testing.T) {
	h := newRelocHarness(t)
	h.telemetry.err = errors.New("telemetry unavailable")

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "tour telemetry was unreadable, so no hull's rate is provable")
	if result.Skipped["hull_rate_unreadable"] == 0 {
		t.Fatalf("expected a hull_rate_unreadable skip; skips %v", result.Skipped)
	}
}

// An unreadable FLEET is an operational error the runner should retry, not a silent no-op.
func TestOpportunityRelocatorShould_ReturnAnErrorWhenTheFleetCannotBeObserved(t *testing.T) {
	h := newRelocHarness(t)
	h.fleet.err = errors.New("ship repository unavailable")

	if _, err := h.handler.Reconcile(context.Background(), h.cmd); err == nil {
		t.Fatal("an unreadable fleet returned no error; the tick would silently no-op forever")
	}
	relocRequireNoMove(t, h.actuator, "the fleet could not be observed")
}

// An unreadable INTENT STORE must not proceed: without knowing what is in flight the tick could
// re-decide a move already authorized.
func TestOpportunityRelocatorShould_ReturnAnErrorWhenPersistedIntentsCannotBeRead(t *testing.T) {
	h := newRelocHarness(t)
	h.intents.loadErr = errors.New("intent store unavailable")

	if _, err := h.handler.Reconcile(context.Background(), h.cmd); err == nil {
		t.Fatal("unreadable persisted intents returned no error; the tick could re-decide an in-flight move")
	}
	relocRequireNoMove(t, h.actuator, "persisted intents could not be read")
}

// An UNKNOWN era horizon must not silence the reconciler — that would be a feature flag by accident.
func TestOpportunityRelocatorShould_StillRelocateWhenTheEraHorizonIsUnknown(t *testing.T) {
	h := newRelocHarness(t)
	h.era = &relocFakeEra{known: false}
	h.handler = NewRunOpportunityRelocatorHandler(h.fleet, h.regions, h.telemetry, h.era, h.actuator, h.intents, nil, h.clock)

	if got := h.reconcile(t); len(got.Relocated) != 1 {
		t.Fatalf("an unknown era horizon relocated nothing; the reconciler would be dormant on every unreadable era read. skips %v", got.Skipped)
	}
}

// A region that IS the hull's current system is never a relocation target.
func TestOpportunityRelocatorShould_NeverRelocateAHullToTheSystemItIsAlreadyIn(t *testing.T) {
	h := newRelocHarness(t)
	here := relocRegion("X1-HOME", 0, 5_000_000)
	h.regions.byOrigin["X1-HOME"] = []RelocatorRegion{here}

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "the only candidate region is the system the hull is already in")
	if result.Skipped["region_is_current_system"] != 1 {
		t.Fatalf("expected one region_is_current_system skip; skips %v", result.Skipped)
	}
}

// ── the actuation-time claim race (sp-x2jr6 slice 1) ────────────────────────────────────────────
//
// Observation and actuation are not atomic and the actuator holds no claim, so a hull that was
// genuinely eligible when it was scored can be claimed by a tour before it is moved. Measured live:
// 3 of the relocator's first 4 decisions were lost this way, the losing hull having been claimed
// FOUR SECONDS after the fleet snapshot and TEN SECONDS before the decision — the ~14s gap being the
// bounded pre-flight, which is real work and not latency to optimise away.
//
// EVERY fixture here makes the hull ELIGIBLE at observation and TAKEN at actuation. That divergence is
// the whole point: a fixture where the hull is already taken at observation never reaches the re-check,
// because hullEligibility drops it long before, and would pass against no re-check at all.

// THE REGRESSION TEST. A hull claimed between scoring and actuation is NOT moved.
func TestOpportunityRelocatorShould_NotMoveAHullClaimedBetweenObservationAndActuation(t *testing.T) {
	for name, taken := range map[string]RelocatorHull{
		"a tour claimed it while the pre-flight was running": {ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME", OnTour: true},
		"another operation claimed it":                       {ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME", Pinned: true},
		"the captain reserved it":                            {ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME", Pinned: true},
	} {
		t.Run(name, func(t *testing.T) {
			h := newRelocHarness(t)
			// Eligible at observation — the baseline harness already relocates this hull — and taken by
			// the time the actuator would be called.
			h.fleet.atActuation = map[string]RelocatorHull{"HAULER-A": taken}

			result := h.reconcile(t)

			relocRequireNoMove(t, h.actuator, name+", so the relocation must be abandoned")
			if len(result.Relocated) != 0 {
				t.Fatalf("reported Relocated %v for a hull it never moved", result.Relocated)
			}
			if result.Skipped["claimed_at_actuation"] != 1 {
				t.Fatalf("expected one claimed_at_actuation skip so the loss is countable in the heartbeat; skips %v", result.Skipped)
			}
			// NOTHING may be persisted. An intent written for a move that never happens would be
			// RESUMED on the next tick — which is how a refused relocation becomes one that happens
			// anyway, one tick later, without ever being re-checked.
			if len(h.intents.writes) != 0 {
				t.Fatalf("an abandoned relocation persisted %d intent(s); the next tick would resume the move this check just refused", len(h.intents.writes))
			}
		})
	}
}

// The re-check must actually RUN — and run for the hull about to be moved. Without this the tests above
// could pass on a fixture accident rather than on a real re-read.
func TestOpportunityRelocatorShould_ReReadTheChosenHullBeforeMovingIt(t *testing.T) {
	h := newRelocHarness(t)

	result := h.reconcile(t)

	relocRequireMoved(t, h.actuator, "HAULER-A")
	if len(result.Relocated) != 1 {
		t.Fatalf("the baseline move stopped happening; the re-check must let an unchanged hull through. skips %v", result.Skipped)
	}
	if len(h.fleet.reobserved) != 1 || h.fleet.reobserved[0] != "HAULER-A" {
		t.Fatalf("re-read %v immediately before moving, want exactly [HAULER-A]; a commit that never re-reads cannot see the race", h.fleet.reobserved)
	}
}

// FAIL CLOSED: an unreadable re-read cannot prove the hull is still free, so the hull is not moved. The
// asymmetry is deliberate — a lost relocation costs one tick, moving a hull another operation holds
// costs a hull.
func TestOpportunityRelocatorShould_NotMoveAHullWhoseActuationReReadIsUnreadable(t *testing.T) {
	h := newRelocHarness(t)
	h.fleet.reobserveErr = errors.New("ship repository blipped")

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "the actuation re-read failed, so the hull's ownership is unprovable")
	if result.Skipped["actuation_recheck_unreadable"] != 1 {
		t.Fatalf("expected one actuation_recheck_unreadable skip; skips %v", result.Skipped)
	}
	if len(h.intents.writes) != 0 {
		t.Fatalf("a fail-closed abort persisted %d intent(s)", len(h.intents.writes))
	}
}

// THE RESUME PATH IS THE SAME HAZARD. A restart resumes an in-flight intent toward its original target
// rather than re-deciding it — but if a tour has since claimed the hull, resuming poaches it just as a
// fresh commit would, and the resume path calls the very same actuator.
//
// An abandoned resume must mark the intent COMPLETED. Leaving it in flight would consume a concurrency
// slot forever: reconcileInFlight counts every uncompleted intent against
// max_concurrent_relocations, so two permanently-unresumable intents would deadlock the reconciler at
// its cap and it would stop relocating anything at all.
func TestOpportunityRelocatorShould_AbandonRatherThanResumeAnInterruptedMoveOntoAHullATourHasSinceClaimed(t *testing.T) {
	h := newRelocHarness(t)
	h.fleet.atActuation = map[string]RelocatorHull{
		"HAULER-A": {ShipSymbol: "HAULER-A", CurrentSystem: "X1-HOME", OnTour: true},
	}
	h.intents = newRelocFakeIntents(RelocationIntent{
		ShipSymbol: "HAULER-A", FromSystem: "X1-HOME", TargetSystem: "X1-RICH", TargetWaypoint: "X1-RICH-MARKET",
		DecidedAt: relocNow.Add(-5 * time.Minute), Completed: false,
	})
	h.handler = NewRunOpportunityRelocatorHandler(h.fleet, h.regions, h.telemetry, h.era, h.actuator, h.intents, nil, h.clock)

	result := h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "a tour has claimed the hull, so its interrupted relocation must be abandoned rather than resumed")
	if len(result.Resumed) != 0 {
		t.Fatalf("reported Resumed %v for a move it never made", result.Resumed)
	}
	if !h.intents.records["HAULER-A"].Completed {
		t.Fatal("the abandoned intent was left IN FLIGHT; it would consume a concurrency slot forever and deadlock the reconciler at its cap")
	}
	if got := h.intents.records["HAULER-A"].DecidedAt; !got.Equal(relocNow.Add(-5 * time.Minute)) {
		t.Fatalf("abandoning the intent moved DecidedAt to %v; the ORIGINAL decision time is the restart-durable cooldown clock", got)
	}
}

// An UNREADABLE re-read in the resume path is different from a hull that is definitely taken: it must
// leave the intent IN FLIGHT so the next tick retries, rather than abandon a relocation that may still
// be perfectly valid. Fail closed on the MOVE, not on the record.
func TestOpportunityRelocatorShould_LeaveAnInterruptedMoveInFlightWhenItsReReadIsUnreadable(t *testing.T) {
	h := newRelocHarness(t)
	h.fleet.reobserveErr = errors.New("ship repository blipped")
	h.intents = newRelocFakeIntents(RelocationIntent{
		ShipSymbol: "HAULER-A", FromSystem: "X1-HOME", TargetSystem: "X1-RICH", TargetWaypoint: "X1-RICH-MARKET",
		DecidedAt: relocNow.Add(-5 * time.Minute), Completed: false,
	})
	h.handler = NewRunOpportunityRelocatorHandler(h.fleet, h.regions, h.telemetry, h.era, h.actuator, h.intents, nil, h.clock)

	h.reconcile(t)

	relocRequireNoMove(t, h.actuator, "the re-read failed, so the resume cannot be proven safe this tick")
	if h.intents.records["HAULER-A"].Completed {
		t.Fatal("a transient re-read failure ABANDONED a valid in-flight relocation; only a hull that is definitely taken may be abandoned")
	}
}

// --- reconcile cadence (sp-fjvlm) ----------------------------------------------

// shortestMeasuredIdleWindowSeconds is the SHORTEST gap ever observed between one tour
// ending and the same hull's next tour starting, and it is the number the reconcile
// cadence has to fit inside.
//
// Measured over 12h of tour containers, 195 samples of
// lead(started_at) - stopped_at per hull:
//
//	under 3 min      0      min     183s
//	3-15 min       184      p05     185s
//	over 15 min     11      median  206s
//
// NO HULL IS EVER IDLE FOR LESS THAN THREE MINUTES. That is what makes a cadence fix
// sufficient and an event-driven redesign unnecessary: the windows are wide, and a tick
// that fits inside the narrowest of them catches essentially all of them.
const shortestMeasuredIdleWindowSeconds = 183

// TestResolveRelocatorTickSeconds_DefaultFitsInsideTheShortestIdleWindow pins the cadence
// to the measurement rather than to a number.
//
// A periodic tick of period T lands inside an idle window of length W whenever T <= W, so
// a cadence longer than the shortest observed window will sleep straight through some of
// the releases it exists to catch. At 900s against a 206s median it slept through ~86% of
// them: the live tick read `1 relocated, 0 resumed, 1 evaluated, skipped[mid_tour=32]` —
// one eligible hull out of 33, not because 32 were ineligible but because the tick landed
// mid-tour for all of them.
//
// Asserted as an INEQUALITY against the measurement, not an equality against 120, so the
// test states the constraint instead of restating the constant. Raising the cadence back
// above the shortest measured window fails here with the reason attached.
func TestResolveRelocatorTickSeconds_DefaultFitsInsideTheShortestIdleWindow(t *testing.T) {
	got := resolveRelocatorTickSeconds(0)

	if got > shortestMeasuredIdleWindowSeconds {
		t.Fatalf("default reconcile cadence is %ds, which is longer than the shortest idle window ever measured (%ds). "+
			"A tick period longer than the window sleeps through it: at 900s the relocator evaluated 1 of 33 hulls, "+
			"skipping 32 as mid_tour. The cadence must fit inside the window it is meant to land in.",
			got, shortestMeasuredIdleWindowSeconds)
	}
	if got <= 0 {
		t.Fatalf("default reconcile cadence resolved to %ds — a non-positive cadence would spin the reconcile loop", got)
	}
}

// TestResolveRelocatorTickSeconds_OperatorOverrideStillWins proves the faster default did
// not become a hard-coded cadence. TickSeconds remains the override; sp-fjvlm moved only
// what it falls back to.
func TestResolveRelocatorTickSeconds_OperatorOverrideStillWins(t *testing.T) {
	if got := resolveRelocatorTickSeconds(600); got != 600 {
		t.Fatalf("explicit TickSeconds=600 resolved to %d, want 600 — the override must outrank the default", got)
	}
	// The 0/absent -> documented default idiom (RULINGS #5), including the negative case a
	// malformed config can produce.
	for _, absent := range []int{0, -1} {
		if got := resolveRelocatorTickSeconds(absent); got != defaultRelocatorTickSeconds {
			t.Fatalf("TickSeconds=%d resolved to %d, want the default %d", absent, got, defaultRelocatorTickSeconds)
		}
	}
}
