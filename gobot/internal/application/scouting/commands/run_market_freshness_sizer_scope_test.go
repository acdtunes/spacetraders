package commands

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

// ---- scope fixtures --------------------------------------------------------

// staticLiveConfig serves a fixed snapshot of the container's persisted config, so a test can
// pin a tunable-only knob the launch command has no field for.
type staticLiveConfig liveconfig.Snapshot

func (s staticLiveConfig) Snapshot(_ context.Context, _ string, _ int) (liveconfig.Snapshot, error) {
	return liveconfig.Snapshot(s), nil
}

// oneDiscoverySlot pins the allowance to a single slot so a fixture can show which systems the
// footprint cut releases without the default 8 slots absorbing them all.
func oneDiscoverySlot() staticLiveConfig {
	return staticLiveConfig{"scan_discovery_allowance": 1}
}

// newHaulerAt builds a NON-scout hull parked in a system — the fleet-presence signal that
// anchors a system into the footprint even when it carries no trade telemetry (contract hubs,
// the gate construction site, factory feed systems).
func newHaulerAt(t *testing.T, symbol, waypoint string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(80, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 400, 0, cargo, 80,
		"FRAME_LIGHT_FREIGHTER", "HAULER", nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	return ship
}

// newCommandFrigateAt builds the COMMAND frigate — the hull every era opens with. It is NOT
// scout-type, so it anchors its system into the footprint from the first tick, which is what
// makes a real opening fleet a NARROWED one however little it has traded.
func newCommandFrigateAt(t *testing.T, symbol, waypoint string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(400, 400)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(40, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 400, 0, cargo, 40,
		"FRAME_FRIGATE", "COMMAND", nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	return ship
}

// newScoutAt is newScout at an explicit waypoint — used to prove a PROBE's position does not
// anchor its own system into the footprint.
func newScoutAt(t *testing.T, symbol, waypoint string) *navigation.Ship {
	t.Helper()
	loc, err := shared.NewWaypoint(waypoint, 0, 0)
	require.NoError(t, err)
	fuel, err := shared.NewFuel(100, 100)
	require.NoError(t, err)
	cargo, err := shared.NewCargo(0, 0, nil)
	require.NoError(t, err)
	ship, err := navigation.NewShip(symbol, shared.MustNewPlayerID(1), loc, fuel, 100, 0, cargo, 30,
		"FRAME_PROBE", "SATELLITE", nil, navigation.NavStatusInOrbit)
	require.NoError(t, err)
	return ship
}

// scopeSnap is a self-consistent, TRUSTED census entry whose markets carry the system's own
// waypoint prefix (the production shape) at a given age and intrinsic value weight.
func scopeSnap(system string, markets int, ageSecs, weight float64) domainScouting.SystemFreshnessSnapshot {
	samples := make([]domainScouting.MarketFreshnessSample, 0, markets)
	for i := 0; i < markets; i++ {
		samples = append(samples, domainScouting.MarketFreshnessSample{
			Waypoint: fmt.Sprintf("%s-M%d", system, i), AgeSeconds: ageSecs, Weight: weight,
		})
	}
	return domainScouting.SystemFreshnessSnapshot{
		SystemSymbol: system, MarketCount: markets, OldestAgeSeconds: ageSecs,
		MeasuredCycleSeconds: 120, CycleSamples: 25, Markets: samples,
	}
}

// tradeLegAgo builds a realized SELL leg in a system, `ago` before now.
func tradeLegAgo(system string, ago time.Duration, now time.Time) trading.TourLegTelemetry {
	at := now.Add(-ago)
	return trading.TourLegTelemetry{
		Waypoint: system + "-M0", Good: "PRECIOUS_STONES", IsBuy: false,
		RealizedUnits: 100, RealizedUnitPrice: 500, RealizedAt: at, PlannedAt: at, PlayerID: 1,
	}
}

// declaredSystems is the desired-state the tick wrote: system → hull budget, across both the
// declare (Upsert) and resize (UpdateHulls) seams.
func declaredSystems(pr *fakeSizerPostRepo) map[string]int {
	out := map[string]int{}
	for _, p := range pr.upserts {
		out[p.SystemSymbol] = p.HullBudget()
	}
	for system, hulls := range pr.hullUpdates {
		out[system] = hulls
	}
	return out
}

// ---- scope behaviour -------------------------------------------------------

// HEADLINE: sensing is scoped to the systems the fleet OPERATES in, not to the whole charted
// map. A system the fleet has never traded in and holds no hull in earns no standing post, and
// an existing post for one is released so its probes return to the pool. The single discovery
// slot in this fixture goes to the RICHER of the two untraded systems.
func TestSizer_NarrowsScanScopeToTheTradingFootprint(t *testing.T) {
	now := time.Now()
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		scopeSnap("X1-TRADED", 12, 600, 1),
		scopeSnap("X1-NEVER-RICH", 12, 600, 900),
		scopeSnap("X1-NEVER-POOR", 12, 600, 1),
	}}
	pr := newSizerPostRepo(
		standingSizerPost("X1-TRADED", 2, "PROBE-A"),
		standingSizerPost("X1-NEVER-RICH", 2, "PROBE-B"),
		standingSizerPost("X1-NEVER-POOR", 2, "PROBE-C"),
	)
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
	h, clock := newSizerWithClock(fr, pr, fl)
	clock.CurrentTime = now
	h.SetTourTelemetryReader(&fakeTourTelemetryReader{legs: []trading.TourLegTelemetry{
		tradeLegAgo("X1-TRADED", time.Hour, now),
	}})
	h.SetLiveConfigReader(oneDiscoverySlot())

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Contains(t, declaredSystems(pr), "X1-TRADED", "the traded system stays sensed")
	require.Contains(t, pr.removed, "X1-NEVER-POOR",
		"a never-traded, unoccupied system beyond the allowance is released so its probes return to the pool")
	require.NotContains(t, pr.removed, "X1-TRADED", "the traded system's post is never released")
	require.NotContains(t, pr.removed, "X1-NEVER-RICH", "the discovery slot holds the richest untraded system")
}

// THE INVARIANT THAT MUST NOT BREAK. Market prices are volatile and non-monotone: a market the
// fleet crushed by dumping recovers on its own, and the ONLY way to learn it recovered is to
// keep a probe looking. The fleet stops trading a crushed lane precisely BECAUSE it is crushed,
// so a footprint keyed on recent trades alone would evict it exactly while it reverts — and it
// could never return, because nothing would scan it, so nothing would trade it.
//
// Retention therefore outlasts the reversion window (priors: full reversion ~8-9h, a crushed
// lane dead 12-24h). A system last traded 20h ago is still sensed and still manned.
func TestSizer_RescansACrushedTradedMarketSoRecoveryIsDetected(t *testing.T) {
	now := time.Now()
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		scopeSnap("X1-CRUSHED", 12, 600, 1),
	}}
	pr := newSizerPostRepo(standingSizerPost("X1-CRUSHED", 2, "PROBE-A"))
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
	h, clock := newSizerWithClock(fr, pr, fl)
	clock.CurrentTime = now
	// Crushed and quiet for 20h — no trade since, because the bids collapsed.
	h.SetTourTelemetryReader(&fakeTourTelemetryReader{legs: []trading.TourLegTelemetry{
		tradeLegAgo("X1-CRUSHED", 20*time.Hour, now),
	}})

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.NotContains(t, pr.removed, "X1-CRUSHED",
		"a crushed market keeps its post through the whole reversion window or its recovery is never observed")
	require.GreaterOrEqual(t, declaredSystems(pr)["X1-CRUSHED"], 1,
		"it stays manned — the probe watching it IS the recovery sensor")
}

// The footprint does decay — just slower than a market recovers. A system untraded for longer
// than the retention window is released.
func TestSizer_ReleasesASystemOnlyAfterTheRetentionWindowElapses(t *testing.T) {
	now := time.Now()
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		scopeSnap("X1-ANCHOR", 12, 600, 1),
		scopeSnap("X1-LAPSED", 12, 600, 1),
		scopeSnap("X1-DECOY", 12, 600, 900), // richest untraded — takes the single discovery slot
	}}
	pr := newSizerPostRepo(
		standingSizerPost("X1-ANCHOR", 2, "PROBE-A"),
		standingSizerPost("X1-LAPSED", 2, "PROBE-B"),
		standingSizerPost("X1-DECOY", 2, "PROBE-C"),
	)
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
	h, clock := newSizerWithClock(fr, pr, fl)
	clock.CurrentTime = now
	h.SetTourTelemetryReader(&fakeTourTelemetryReader{legs: []trading.TourLegTelemetry{
		tradeLegAgo("X1-ANCHOR", time.Hour, now),
		tradeLegAgo("X1-LAPSED", 40*time.Hour, now),
	}})
	h.SetLiveConfigReader(oneDiscoverySlot())

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Contains(t, pr.removed, "X1-LAPSED", "a system untraded past the retention window is released")
	require.NotContains(t, pr.removed, "X1-ANCHOR")
}

// COLD START must not starve, and this is the fleet an era actually opens with: a COMMAND
// frigate and some probes, having traded nowhere. That fleet's scope is NARROWED from the first
// tick — footprint is traded UNION occupied, and the frigate is not scout-type, so it anchors
// its system with no trade history at all. The empty-footprint escape does not apply to any
// fleet that owns a hull.
//
// What protects a cold start is therefore not an un-narrowed scope but the DISCOVERY ALLOWANCE
// out-sizing a young census: while the untraded systems fit in the slots, every one of them
// keeps a standing watch and nothing is released. The default allowance is what an opening
// fleet runs, so this fixture pins the default rather than a smaller one.
func TestSizer_ColdStartWithACommandFrigateIsNarrowedYetSensesEveryMarketSystem(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		scopeSnap("X1-AA", 12, 600, 1),
		scopeSnap("X1-BB", 12, 600, 1),
		scopeSnap("X1-CC", 12, 600, 1),
	}}
	pr := newSizerPostRepo()
	fl := &fakeSizerFleetRepo{all: append(scouts(t, 20), newCommandFrigateAt(t, "FRIGATE-1", "X1-HOME-A1"))}
	h := newSizer(fr, pr, fl)
	h.SetTourTelemetryReader(&fakeTourTelemetryReader{legs: nil}) // no trade history at all

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	declared := declaredSystems(pr)
	for _, system := range []string{"X1-AA", "X1-BB", "X1-CC"} {
		require.Contains(t, declared, system,
			"a young census fits inside the discovery allowance, so every market-bearing system stays sensed")
	}
	require.Empty(t, pr.removed, "cold start releases nothing")
}

// The cold-start protection above is FINITE, and naming its real mechanism is the point of this
// test: it is the allowance, not an absent footprint. Hold the same opening fleet and shrink the
// allowance below the census, and the scope narrows for real — only the slotted system keeps a
// watch. A fleet that owns a hull is always narrowed; the only question is whether the allowance
// still covers what it knows.
func TestSizer_ColdStartProtectionIsTheAllowanceNotAnAbsentFootprint(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		scopeSnap("X1-AA", 12, 600, 1),
		scopeSnap("X1-BB", 12, 600, 1),
		scopeSnap("X1-CC", 12, 600, 1),
	}}
	pr := newSizerPostRepo()
	fl := &fakeSizerFleetRepo{all: append(scouts(t, 20), newCommandFrigateAt(t, "FRIGATE-1", "X1-HOME-A1"))}
	h := newSizer(fr, pr, fl)
	h.SetTourTelemetryReader(&fakeTourTelemetryReader{legs: nil})
	h.SetLiveConfigReader(oneDiscoverySlot())

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Len(t, declaredSystems(pr), 1,
		"with the allowance below the census the scope genuinely narrows — an opening fleet is not exempt from narrowing, it is only covered by the slots")
}

// An EMPTY census releases NOTHING, even while the scope is narrowed. A census that surfaced
// zero market-bearing systems is absent evidence, not evidence of absence — a truncated cache
// after an era reset, or one transient read failure. The scope built from it is equally empty
// of discovery slots, so acting on it would release every standing post outside the footprint
// in a single tick and un-man the fleet's whole sensing frontier.
//
// The fleet here is a REAL opening fleet: a COMMAND frigate anchors its system, so the scope IS
// narrowed and the empty-footprint escape does not apply. That combination — narrowed scope,
// empty census — is the one the fail-safe exists for.
func TestSizer_EmptyCensusReleasesNothingEvenWhileNarrowed(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: nil} // census truncated / unreadable this tick
	pr := newSizerPostRepo(
		standingSizerPost("X1-AA", 2, "PROBE-A"),
		standingSizerPost("X1-BB", 2, "PROBE-B"),
		standingSizerPost("X1-CC", 2, "PROBE-C"),
	)
	fl := &fakeSizerFleetRepo{all: append(scouts(t, 20), newCommandFrigateAt(t, "FRIGATE-1", "X1-HOME-A1"))}
	h := newSizer(fr, pr, fl)
	h.SetTourTelemetryReader(&fakeTourTelemetryReader{legs: nil}) // read succeeds, no trades yet

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Empty(t, pr.removed,
		"an empty census must release NO post — the scope derived from it carries no discovery tier, so acting on it un-mans the frontier in one tick")
}

// An unreadable telemetry read must NOT narrow the scope. Narrowing is the risky act — a system
// dropped from scope goes dark — so absent evidence the sizer keeps sensing everything.
func TestSizer_UnreadableTelemetryDoesNotNarrowScope(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		scopeSnap("X1-AA", 12, 600, 1),
		scopeSnap("X1-BB", 12, 600, 1),
		scopeSnap("X1-CC", 12, 600, 1),
	}}
	pr := newSizerPostRepo(
		standingSizerPost("X1-AA", 2, "PROBE-A"),
		standingSizerPost("X1-BB", 2, "PROBE-B"),
		standingSizerPost("X1-CC", 2, "PROBE-C"),
	)
	fl := &fakeSizerFleetRepo{all: append(scouts(t, 20), newHaulerAt(t, "HAULER-1", "X1-AA-M0"))}
	h := newSizer(fr, pr, fl)
	h.SetTourTelemetryReader(&fakeTourTelemetryReader{err: errors.New("telemetry unreadable")})
	h.SetLiveConfigReader(oneDiscoverySlot())

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Empty(t, pr.removed, "an unreadable telemetry read must never release a post")
	require.Contains(t, declaredSystems(pr), "X1-CC", "every system stays sensed while the evidence is missing")
}

// A system holding one of the fleet's NON-scout hulls is in the footprint even with no trade
// telemetry: contract hubs, the gate construction site, and factory feed systems all show up as
// hull presence long before — or without ever — producing a tour leg.
func TestSizer_OccupiedSystemAnchorsTheFootprintWithoutATradeRecord(t *testing.T) {
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		scopeSnap("X1-GATE", 12, 600, 1),
		scopeSnap("X1-NOWHERE", 12, 600, 1),
		scopeSnap("X1-DECOY", 12, 600, 900),
	}}
	pr := newSizerPostRepo(
		standingSizerPost("X1-GATE", 2, "PROBE-A"),
		standingSizerPost("X1-NOWHERE", 2, "PROBE-B"),
		standingSizerPost("X1-DECOY", 2, "PROBE-C"),
	)
	fl := &fakeSizerFleetRepo{all: append(scouts(t, 20), newHaulerAt(t, "HAULER-1", "X1-GATE-A1"))}
	h := newSizer(fr, pr, fl)
	h.SetTourTelemetryReader(&fakeTourTelemetryReader{legs: nil})
	h.SetLiveConfigReader(oneDiscoverySlot())

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Contains(t, declaredSystems(pr), "X1-GATE", "a system our hulls occupy is sensed")
	require.NotContains(t, pr.removed, "X1-GATE")
	require.Contains(t, pr.removed, "X1-NOWHERE", "a system with neither trades nor hulls is released")
}

// CIRCULARITY GUARD: a PROBE's own position must not anchor its system. Probes are the sensor,
// not the reason to sense — counting them would let every scanned system justify its own
// scanning and the scope could never narrow at all.
func TestSizer_ProbePositionDoesNotAnchorTheFootprint(t *testing.T) {
	now := time.Now()
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		scopeSnap("X1-REAL", 12, 600, 1),
		scopeSnap("X1-PROBEONLY", 12, 600, 1),
		scopeSnap("X1-DECOY", 12, 600, 900),
	}}
	pr := newSizerPostRepo(
		standingSizerPost("X1-REAL", 2, "PROBE-A"),
		standingSizerPost("X1-PROBEONLY", 2, "PROBE-Z"),
		standingSizerPost("X1-DECOY", 2, "PROBE-C"),
	)
	fl := &fakeSizerFleetRepo{all: append(scouts(t, 10), newScoutAt(t, "PROBE-Z", "X1-PROBEONLY-M0"))}
	h, clock := newSizerWithClock(fr, pr, fl)
	clock.CurrentTime = now
	h.SetTourTelemetryReader(&fakeTourTelemetryReader{legs: []trading.TourLegTelemetry{
		tradeLegAgo("X1-REAL", time.Hour, now),
	}})
	h.SetLiveConfigReader(oneDiscoverySlot())

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Contains(t, pr.removed, "X1-PROBEONLY",
		"a system whose only occupant is a probe is not part of the trading footprint")
}

// The DISCOVERY ALLOWANCE is bounded and non-zero: the fleet keeps watching a fixed number of
// the richest markets it does not yet trade in, so a narrowed scope can still grow. Each slot
// costs exactly one probe, never the full SLA model.
//
// The candidates carry 90 markets each, so the full model would size them to
// ceil(90 markets × 120s cycle / 3600s SLA) = 3 probes apiece. The one-probe assertion below is
// therefore load-bearing: it fails if the discovery tier is ever sized like a traded system.
func TestSizer_DiscoveryAllowanceIsBoundedAndNonZero(t *testing.T) {
	now := time.Now()
	snapshots := []domainScouting.SystemFreshnessSnapshot{scopeSnap("X1-TRADED", 12, 600, 1)}
	for i := 0; i < 10; i++ {
		snapshots = append(snapshots, scopeSnap(fmt.Sprintf("X1-CAND%d", i), 90, 600, float64(100+i)))
	}
	fr := &fakeFreshnessReader{snapshots: snapshots}
	pr := newSizerPostRepo()
	fl := &fakeSizerFleetRepo{all: scouts(t, 40)}
	h, clock := newSizerWithClock(fr, pr, fl)
	clock.CurrentTime = now
	h.SetTourTelemetryReader(&fakeTourTelemetryReader{legs: []trading.TourLegTelemetry{
		tradeLegAgo("X1-TRADED", time.Hour, now),
	}})
	h.SetLiveConfigReader(staticLiveConfig{"scan_discovery_allowance": 3})

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	discovery := map[string]int{}
	for system, hulls := range declaredSystems(pr) {
		if system != "X1-TRADED" {
			discovery[system] = hulls
		}
	}
	require.Len(t, discovery, 3, "exactly the allowance — bounded")
	require.NotEmpty(t, discovery, "and non-zero: narrowing never kills discovery outright")
	for _, want := range []string{"X1-CAND9", "X1-CAND8", "X1-CAND7"} {
		require.Contains(t, discovery, want, "discovery targets the richest untraded markets, not random ones")
	}
	for system, hulls := range discovery {
		require.Equal(t, 1, hulls,
			"a discovery slot costs exactly one probe (%s) — the full model would have asked for 3", system)
	}
}

// A discovery post carries the RELAXED discovery freshness target, not the system's trading SLA.
// A one-probe post stamped with a tight SLA would read as permanently breaching, and the scout
// reconciler's manning watchdog would tear its tour down on repeat.
func TestSizer_DiscoveryPostCarriesTheRelaxedDiscoveryTarget(t *testing.T) {
	now := time.Now()
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		scopeSnap("X1-TRADED", 12, 600, 1),
		scopeSnap("X1-RICH", 30, 600, 900),
	}}
	pr := newSizerPostRepo()
	fl := &fakeSizerFleetRepo{all: scouts(t, 40)}
	h, clock := newSizerWithClock(fr, pr, fl)
	clock.CurrentTime = now
	h.SetTourTelemetryReader(&fakeTourTelemetryReader{legs: []trading.TourLegTelemetry{
		tradeLegAgo("X1-TRADED", time.Hour, now),
	}})

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	var discovery *domainScouting.ScoutPost
	for _, p := range pr.upserts {
		if p.SystemSymbol == "X1-RICH" {
			discovery = p
		}
	}
	require.NotNil(t, discovery, "the richest untraded system holds a discovery slot")
	require.Equal(t, time.Duration(defaultScanDiscoverySLASeconds)*time.Second, discovery.FreshnessTarget,
		"a discovery post is stamped with the relaxed watch target, not the system's trading SLA")
}

// ACCEPTANCE: scoping brings the sizer's aggregate demand back inside its probe supply, so the
// coordinator stops buying probes to chase a map it never trades in.
func TestSizer_ScopedDemandFallsWithinSupplyAndStopsBuying(t *testing.T) {
	now := time.Now()
	snapshots := []domainScouting.SystemFreshnessSnapshot{scopeSnap("X1-TRADED", 6, 600, 1)}
	for i := 0; i < 12; i++ {
		snapshots = append(snapshots, scopeSnap(fmt.Sprintf("X1-COLD%d", i), 30, 600, 1))
	}
	run := func(legs []trading.TourLegTelemetry) int {
		fr := &fakeFreshnessReader{snapshots: snapshots}
		pr := newSizerPostRepo()
		fl := &fakeSizerFleetRepo{all: scouts(t, 8)}
		h, clock := newSizerWithClock(fr, pr, fl)
		clock.CurrentTime = now
		pu := &fakePurchaser{quotePrice: 5000, buySymbol: "PROBE-NEW"}
		h.SetProbePurchaser(pu)
		h.SetTourTelemetryReader(&fakeTourTelemetryReader{legs: legs})
		h.SetLiveConfigReader(oneDiscoverySlot())
		require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))
		return pu.buyCalls
	}

	require.Equal(t, 1, run(nil), "un-scoped, demand across the whole census outruns supply and buys")
	require.Equal(t, 0, run([]trading.TourLegTelemetry{tradeLegAgo("X1-TRADED", time.Hour, now)}),
		"scoped to the footprint, demand fits inside the existing probes — no buy")
}

// A post carrying a MANNING FLOOR (the home post, floored to the probes bootstrap bought for it)
// is never released by the scope cut — releasing it would strand the floor and the probes behind
// it, re-opening the bug the floor exists to prevent.
func TestSizer_NeverReleasesAFlooredPostOutOfScope(t *testing.T) {
	now := time.Now()
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{
		scopeSnap("X1-TRADED", 12, 600, 1),
		scopeSnap("X1-HOME", 12, 600, 1),
		scopeSnap("X1-DECOY", 12, 600, 900),
	}}
	floored := standingSizerPost("X1-HOME", 2, "PROBE-H")
	floored.MinHulls = 3
	pr := newSizerPostRepo(
		standingSizerPost("X1-TRADED", 2, "PROBE-A"),
		floored,
		standingSizerPost("X1-DECOY", 2, "PROBE-C"),
	)
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
	h, clock := newSizerWithClock(fr, pr, fl)
	clock.CurrentTime = now
	h.SetTourTelemetryReader(&fakeTourTelemetryReader{legs: []trading.TourLegTelemetry{
		tradeLegAgo("X1-TRADED", time.Hour, now),
	}})
	h.SetLiveConfigReader(oneDiscoverySlot())

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.NotContains(t, pr.removed, "X1-HOME", "a floored post survives the scope cut")
}

// The telemetry read serves BOTH the demand weighting and the footprint, and is issued once per
// tick — scoping must not double the coordinator's I/O — and it reaches back far enough that a
// system inside the retention window cannot read as untraded.
func TestSizer_ReadsTelemetryOncePerTickForBothDemandAndScope(t *testing.T) {
	now := time.Now()
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{scopeSnap("X1-TRADED", 12, 600, 1)}}
	pr := newSizerPostRepo(standingSizerPost("X1-TRADED", 2, "PROBE-A"))
	fl := &fakeSizerFleetRepo{all: scouts(t, 20)}
	h, clock := newSizerWithClock(fr, pr, fl)
	clock.CurrentTime = now
	reader := &fakeTourTelemetryReader{legs: []trading.TourLegTelemetry{tradeLegAgo("X1-TRADED", time.Hour, now)}}
	h.SetTourTelemetryReader(reader)

	require.NoError(t, h.ReconcileOnce(context.Background(), sizerCmd()))

	require.Equal(t, 1, reader.calls, "one telemetry read serves both the demand weight and the scope")
	require.False(t, reader.sinceArg.IsZero(), "the read stays bounded to a rolling window")
	require.False(t, reader.sinceArg.After(now.Add(-time.Duration(defaultScanFootprintRetentionSecs)*time.Second)),
		"the window reaches back at least the footprint retention, or a retained system reads as untraded")
}

// KNOB WIRING: the scope knobs are tunable-only (no launch-command field), resolving live >
// documented default, mirroring the demand half-life knob.
func TestResolveSizerConfig_ReadsScanScopeKnobsLiveWithDefaultFallback(t *testing.T) {
	def := resolveSizerConfig(sizerCmd(), nil)
	require.Equal(t, time.Duration(defaultScanFootprintRetentionSecs)*time.Second, def.FootprintRetention)
	require.Equal(t, defaultScanDiscoveryAllowance, def.DiscoveryAllowance)
	require.Equal(t, time.Duration(defaultScanDiscoverySLASeconds)*time.Second, def.DiscoverySLA)

	live := resolveSizerConfig(sizerCmd(), liveconfig.Snapshot{
		"scan_footprint_retention_secs": 43200,
		"scan_discovery_allowance":      4,
		"scan_discovery_sla_seconds":    7200,
	})
	require.Equal(t, 12*time.Hour, live.FootprintRetention)
	require.Equal(t, 4, live.DiscoveryAllowance)
	require.Equal(t, 2*time.Hour, live.DiscoverySLA)

	reverted := resolveSizerConfig(sizerCmd(), liveconfig.Snapshot{
		"scan_footprint_retention_secs": 0,
		"scan_discovery_allowance":      0,
		"scan_discovery_sla_seconds":    0,
	})
	require.Equal(t, time.Duration(defaultScanFootprintRetentionSecs)*time.Second, reverted.FootprintRetention,
		"`tune scan_footprint_retention_secs 0` reverts to the default")
	require.Equal(t, defaultScanDiscoveryAllowance, reverted.DiscoveryAllowance)
	require.Equal(t, time.Duration(defaultScanDiscoverySLASeconds)*time.Second, reverted.DiscoverySLA)
}

// The retention default must outlast the market-reversion window it exists to cover, and the
// allowance must be bounded but never zero.
func TestScanScopeDefaults_RetentionOutlivesMarketReversion(t *testing.T) {
	require.GreaterOrEqual(t, defaultScanFootprintRetentionSecs, 24*3600,
		"retention must outlast the 12-24h dead window of a crushed lane, or recovery is never observed")
	require.Positive(t, defaultScanDiscoveryAllowance, "the discovery allowance is bounded but never zero")
	require.Contains(t, SizerTunableDefaults(), "scan_footprint_retention_secs")
	require.Contains(t, SizerTunableDefaults(), "scan_discovery_allowance")
	require.Contains(t, SizerTunableDefaults(), "scan_discovery_sla_seconds")
}
