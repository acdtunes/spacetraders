package commands

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// ---- tests: sp-5les manning watchdog ---------------------------------------
//
// The manning-loop analogue of the sp-iupr sizing fix: a standing market-freshness post
// can read IsFullyManned() yet produce ZERO new scan telemetry for many cycles (its tour
// container reads RUNNING while its hull no longer scans — invisible to pass 1, whose only
// health signal is container liveness). The sizer (sp-iupr) stopped HOARDING probes for
// such a silent post; this watchdog RE-MANS it. Detection is the SystemsFreshness census's
// OldestAgeSeconds (worst-case market staleness): a fully-manned standing post whose
// worst-case age breaches its OWN freshness target WITHOUT improving for a whole CIRCUIT
// PERIOD is re-manned (its wedged tour torn down so the passes re-man it fresh — the
// ensureSingleHullFreshness teardown seam). All tests drive the real reconcileOnce
// tick seam with doubles only at the port boundaries (freshness census, ship repo, daemon
// client, event store).
//
// The fixtures that exercise the manning_stall_cycles DEBOUNCE give their post a short
// freshness target and a short avgHop, so its circuit period lands at or under the configured
// cycle count and the knob is the binding window. A post with a longer circuit is paced by that
// circuit instead — TestScoutPost_ManningWatchdog_ColdStartCircuit_* covers that side.

func countManningStalled(store *fakeScoutEventStore) int {
	n := 0
	for _, e := range store.recorded {
		if e.Type == captain.EventScoutPostManningStalled {
			n++
		}
	}
	return n
}

func lastManningStalled(store *fakeScoutEventStore) *captain.Event {
	var last *captain.Event
	for _, e := range store.recorded {
		if e.Type == captain.EventScoutPostManningStalled {
			last = e
		}
	}
	return last
}

func distinctStopped(daemonClient *fakeScoutDaemonClient) int {
	seen := map[string]bool{}
	for _, s := range daemonClient.stopped {
		seen[s] = true
	}
	return len(seen)
}

// manningWatchdogHandler wires a coordinator with the sp-5les census + event ports plus the
// partition/reposition scaffolding every reconcileOnce path touches, so tests exercise the
// watchdog through the real tick seam.
func manningWatchdogHandler(
	postRepo *fakeScoutPostRepo,
	shipRepo *fakeScoutShipRepo,
	daemonClient *fakeScoutDaemonClient,
	cq *fakeContainerStatusQuery,
	mp *fakeMultiMarketProvider,
	fr *fakeFreshnessReader,
	store *fakeScoutEventStore,
	clock shared.Clock,
) *RunScoutPostCoordinatorHandler {
	return &RunScoutPostCoordinatorHandler{
		postRepo:               postRepo,
		shipRepo:               shipRepo,
		daemonClient:           daemonClient,
		containerQuery:         cq,
		marketProvider:         mp,
		clock:                  clock,
		routingClient:          &fakeScoutRoutingClient{},
		repositionBackoffUntil: map[string]time.Time{},
		systemFreshnessReader:  fr,
		eventStore:             store,
	}
}

// Test (1): a fully-manned standing post whose worst-case market age has breached its
// freshness target without improving for N consecutive cycles triggers exactly ONE
// corrective re-man (the wedged tour torn down, the post re-manned this same tick) and
// exactly ONE deferred captain event.
func TestScoutPost_ManningWatchdog_StaleForNCycles_RemansOnceAndEmitsEvent(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding,
		Hulls: 1, FreshnessTarget: 2 * time.Minute, AssignedHull: "SAT-1", TourContainerID: "tour-1",
	}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	require.NoError(t, sat.AssignToContainer("tour-1", clock))
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{
		"RUNNING": {{ID: "tour-1", Status: "RUNNING"}},
	}}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{"X1-GZ7": {"X1-GZ7-M1", "X1-GZ7-M2", "X1-GZ7-M3"}}}
	// Worst-case age 4000s breaches the 120s target and never improves — a silent post.
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{snap("X1-GZ7", 3, 4000, 300, 2)}}
	store := &fakeScoutEventStore{}
	handler := manningWatchdogHandler(postRepo, shipRepo, daemonClient, cq, mp, fr, store, clock)
	cmd := scoutPostTestCmd()
	cmd.ManningStallCycles = 4
	cmd.ManningStallCorrectionCap = 3
	cmd.UndersizedAvgHopSecs = 20 // 3 markets × 20s = a 60s circuit, inside the 4-cycle debounce

	// Ticks 1..3 sit below the 4-cycle threshold — the manned tour is left completely alone.
	for i := 0; i < 3; i++ {
		require.NoError(t, handler.reconcileOnce(context.Background(), cmd))
	}
	require.Empty(t, daemonClient.stopped, "below the stall threshold the manned tour is never torn down")
	require.Zero(t, countManningStalled(store), "no stall event before the threshold")

	// Tick 4: the 4th consecutive stale-without-improvement cycle fires the watchdog once.
	require.NoError(t, handler.reconcileOnce(context.Background(), cmd))

	require.Contains(t, daemonClient.stopped, "tour-1", "the wedged tour is torn down at the stall threshold")
	require.Equal(t, 1, countManningStalled(store), "exactly one manning-stalled event fires")
	ev := lastManningStalled(store)
	require.Equal(t, "X1-GZ7", ev.Ship, "the event is scoped to the stalled post's system")
	require.Contains(t, ev.Payload, `"markets":3`)
	require.Contains(t, ev.Payload, `"stall_cycles":4`)
	require.Contains(t, ev.Payload, `"cycle_samples":2`, "the census cycle-sample count rides the event for diagnosis")

	got := postRepo.find("X1-GZ7")
	require.Equal(t, "SAT-1", got.AssignedHull, "the post is re-manned the same tick — the reclaimed hull picks it back up")
	require.NotEqual(t, "tour-1", got.TourContainerID, "onto a FRESH tour, not the wedged one")
}

// coldStartPost builds the production shape the watchdog is judged against: a fully-manned
// multi-probe standing post over a large market set, every slot RUNNING on its own disjoint
// partition, in a system whose markets are ALL stale — the cold-start census, where the
// worst-case age breaches the target from the very first tick.
func coldStartPost(t *testing.T, clock shared.Clock, marketCount, hulls int) (
	*domainScouting.ScoutPost, []string, *fakeScoutShipRepo, *fakeContainerStatusQuery,
) {
	t.Helper()
	markets := make([]string, 0, marketCount)
	for i := 0; i < marketCount; i++ {
		markets = append(markets, fmt.Sprintf("X1-KP23-M%02d", i))
	}
	partitions := make([][]string, hulls)
	for i, m := range markets {
		partitions[i%hulls] = append(partitions[i%hulls], m)
	}

	post := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-KP23", Kind: domainScouting.PostKindStanding,
		Hulls: hulls, FreshnessTarget: time.Hour,
		AssignedHull: "SAT-0", TourContainerID: "tour-0", PrimaryPartition: partitions[0],
	}
	sats := []*navigation.Ship{newScoutTestSatellite(t, "SAT-0", "X1-KP23-A0")}
	require.NoError(t, sats[0].AssignToContainer("tour-0", clock))
	running := []persistence.ContainerSummary{{ID: "tour-0", Status: "RUNNING"}}
	for i := 1; i < hulls; i++ {
		hull, tour := fmt.Sprintf("SAT-%d", i), fmt.Sprintf("tour-%d", i)
		post.ExtraSlots = append(post.ExtraSlots, domainScouting.ScoutPostSlot{
			AssignedHull: hull, TourContainerID: tour, Partition: partitions[i],
		})
		sat := newScoutTestSatellite(t, hull, fmt.Sprintf("X1-KP23-A%d", i))
		require.NoError(t, sat.AssignToContainer(tour, clock))
		sats = append(sats, sat)
		running = append(running, persistence.ContainerSummary{ID: tour, Status: "RUNNING"})
	}

	return post, markets,
		&fakeScoutShipRepo{ships: sats, clock: clock},
		&fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{"RUNNING": running}}
}

// Test (1b): the cold-start livelock. A freshly-manned post over 26 markets cannot improve its
// WORST-case age until a probe closes a full circuit back to the market it scanned first —
// every market ahead of the probe only ages meanwhile, and at cold start they are all stale
// before the first probe has even arrived anywhere. So for the whole circuit period the post
// is "fully manned, breaching, not improving": the watchdog must hold its hand, or it destroys
// and recreates the tours faster than they could ever report. Past that period, with still no
// improvement, it re-mans exactly as before.
func TestScoutPost_ManningWatchdog_ColdStartCircuit_HeldUntilTheTourCouldReport(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post, markets, shipRepo, cq := coldStartPost(t, clock, 26, 3)
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	daemonClient := &fakeScoutDaemonClient{}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{"X1-KP23": markets}}
	fr := &fakeFreshnessReader{}
	store := &fakeScoutEventStore{}
	handler := manningWatchdogHandler(postRepo, shipRepo, daemonClient, cq, mp, fr, store, clock)
	cmd := scoutPostTestCmd()
	cmd.ManningStallCycles = 4
	cmd.ManningStallCorrectionCap = 3

	// The observed trace: worst-case age already 4403s past the 1h target on the first tick,
	// and one tick staler every tick because no probe has come back round to the oldest market.
	age := 4403.0
	tick := func() {
		fr.snapshots = []domainScouting.SystemFreshnessSnapshot{snap("X1-KP23", 26, age, 0, 0)}
		require.NoError(t, handler.reconcileOnce(context.Background(), cmd))
		age += float64(defaultScoutPostTickSeconds)
	}

	// 40 ticks — 20 minutes, well inside the post's 1h circuit period.
	for i := 0; i < 40; i++ {
		tick()
	}
	require.Empty(t, daemonClient.stopped, "a tour that has not yet had time to close one circuit is never torn down")
	require.Zero(t, countManningStalled(store), "and nothing is reported stalled — silence this short is travel, not a wedge")
	got := postRepo.find("X1-KP23")
	require.True(t, got.IsFullyManned(), "the post keeps its three probes")
	require.Equal(t, "tour-0", got.TourContainerID, "on their original tours")
	require.Equal(t, markets[0], got.PrimaryPartition[0], "over their original partitions")

	// Past the circuit period the same frozen age IS a wedge: the watchdog fires once.
	for i := 0; i < 90; i++ {
		tick()
	}
	require.Equal(t, 3, distinctStopped(daemonClient), "all three wedged tours are torn down for a fresh manning")
	require.Equal(t, 1, countManningStalled(store), "exactly one stall report — the circuit window paces corrections")
}

// Test (2) + mutation target: a stall lasting FEWER than N cycles is debounced — no
// teardown, no event. Removing the consecutive-cycle guard in remanStalledPosts (firing on
// the first stale cycle) must break this test.
func TestScoutPost_ManningWatchdog_BelowThreshold_Debounced(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding,
		Hulls: 1, FreshnessTarget: 2 * time.Minute, AssignedHull: "SAT-1", TourContainerID: "tour-1",
	}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	require.NoError(t, sat.AssignToContainer("tour-1", clock))
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{
		"RUNNING": {{ID: "tour-1", Status: "RUNNING"}},
	}}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{"X1-GZ7": {"X1-GZ7-M1", "X1-GZ7-M2", "X1-GZ7-M3"}}}
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{snap("X1-GZ7", 3, 4000, 300, 2)}}
	store := &fakeScoutEventStore{}
	handler := manningWatchdogHandler(postRepo, shipRepo, daemonClient, cq, mp, fr, store, clock)
	cmd := scoutPostTestCmd()
	cmd.ManningStallCycles = 4
	cmd.UndersizedAvgHopSecs = 20

	// Exactly N-1 stale cycles.
	for i := 0; i < 3; i++ {
		require.NoError(t, handler.reconcileOnce(context.Background(), cmd))
	}

	require.Empty(t, daemonClient.stopped, "a stall below the N-cycle threshold must never tear down the tour (debounce)")
	require.Zero(t, countManningStalled(store), "and must never emit — the debounce holds")
	require.Equal(t, "tour-1", postRepo.find("X1-GZ7").TourContainerID, "the manned tour is untouched")
}

// Test (3): a post whose telemetry IS advancing — its worst-case age dropping each cycle,
// even while still nominally above the SLA — is never touched, because a re-scan pulling the
// age back means the probe is working. The improvement check spares a recovering post.
func TestScoutPost_ManningWatchdog_AdvancingTelemetry_NeverTouched(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding,
		Hulls: 1, FreshnessTarget: time.Minute, AssignedHull: "SAT-1", TourContainerID: "tour-1",
	}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	require.NoError(t, sat.AssignToContainer("tour-1", clock))
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{
		"RUNNING": {{ID: "tour-1", Status: "RUNNING"}},
	}}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{"X1-GZ7": {"X1-GZ7-M1", "X1-GZ7-M2", "X1-GZ7-M3"}}}
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{snap("X1-GZ7", 3, 5000, 300, 2)}}
	store := &fakeScoutEventStore{}
	handler := manningWatchdogHandler(postRepo, shipRepo, daemonClient, cq, mp, fr, store, clock)
	cmd := scoutPostTestCmd()
	cmd.ManningStallCycles = 2 // a tight debounce, to prove even it never fires on an advancing post
	cmd.UndersizedAvgHopSecs = 20

	// Each cycle the worst-case age DROPS — telemetry is advancing — though still over the SLA.
	for _, ageSeconds := range []float64{5000, 4000, 3000, 2500, 2000} {
		fr.snapshots = []domainScouting.SystemFreshnessSnapshot{snap("X1-GZ7", 3, ageSeconds, 300, 2)}
		require.NoError(t, handler.reconcileOnce(context.Background(), cmd))
	}

	require.Empty(t, daemonClient.stopped, "a post whose worst-case age is dropping (telemetry advancing) is never torn down")
	require.Zero(t, countManningStalled(store), "and never emits, even across multiple cycles above the SLA")
	require.Equal(t, "tour-1", postRepo.find("X1-GZ7").TourContainerID, "its tour keeps running untouched")
}

// Test (4): after K corrective re-mans that do NOT restore telemetry (the market is
// genuinely unreachable), the watchdog BACKS OFF — it stops tearing the tour down but keeps
// emitting the deferred event so the stuck post stays visible to the captain.
func TestScoutPost_ManningWatchdog_BacksOffAfterCorrectionCap_EventPersists(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	post := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding,
		Hulls: 1, FreshnessTarget: time.Minute, AssignedHull: "SAT-1", TourContainerID: "tour-1",
	}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	sat := newScoutTestSatellite(t, "SAT-1", "X1-GZ7-A1")
	require.NoError(t, sat.AssignToContainer("tour-1", clock))
	shipRepo := &fakeScoutShipRepo{ships: []*navigation.Ship{sat}, clock: clock}
	daemonClient := &fakeScoutDaemonClient{}
	cq := &fakeContainerStatusQuery{byStatus: map[string][]persistence.ContainerSummary{
		"RUNNING": {{ID: "tour-1", Status: "RUNNING"}},
	}}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{"X1-GZ7": {"X1-GZ7-M1", "X1-GZ7-M2", "X1-GZ7-M3"}}}
	// The census never recovers, no matter how many times the post is re-manned.
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{snap("X1-GZ7", 3, 4000, 300, 2)}}
	store := &fakeScoutEventStore{}
	handler := manningWatchdogHandler(postRepo, shipRepo, daemonClient, cq, mp, fr, store, clock)
	cmd := scoutPostTestCmd()
	cmd.ManningStallCycles = 2
	cmd.ManningStallCorrectionCap = 2
	cmd.UndersizedAvgHopSecs = 20

	// keepFreshestTourRunning simulates a re-manned tour coming up RUNNING (so pass 1 leaves it
	// and the post stays fully manned) while its hull still cannot scan.
	keepFreshestTourRunning := func() {
		if len(daemonClient.started) > 0 {
			latest := daemonClient.started[len(daemonClient.started)-1]
			cq.byStatus["RUNNING"] = []persistence.ContainerSummary{{ID: latest, Status: "RUNNING"}}
		}
	}

	// Three fire-windows (N=2 each) fire at ticks 2/4/6. K=2 re-mans, then backoff on the 3rd.
	for tick := 0; tick < 6; tick++ {
		require.NoError(t, handler.reconcileOnce(context.Background(), cmd))
		keepFreshestTourRunning()
	}
	require.Equal(t, 2, distinctStopped(daemonClient), "the watchdog re-mans at most correction-cap (2) times")
	require.Equal(t, 3, countManningStalled(store), "the event fires every window — the stuck post is visible even as corrections run")

	// Two more windows: no further teardown, but the deferred event keeps carrying it.
	for tick := 0; tick < 2; tick++ {
		require.NoError(t, handler.reconcileOnce(context.Background(), cmd))
		keepFreshestTourRunning()
	}
	require.Equal(t, 2, distinctStopped(daemonClient), "past the correction cap the watchdog never re-relays again — no infinite churn")
	require.Equal(t, 4, countManningStalled(store), "the deferred event persists in backoff, carrying the post to the captain")
}

// Test (5): an under-manned / unmanned post is out of scope — normal manning (the sizer and
// pass 2) own it. Even wildly over its SLA, a post that is not fully manned is never a
// watchdog target.
func TestScoutPost_ManningWatchdog_UnmannedPost_OutOfScope(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Now()}
	// Unmanned (AssignedHull empty) and no satellite anywhere to man it → never fully manned.
	post := &domainScouting.ScoutPost{
		PlayerID: 1, SystemSymbol: "X1-GZ7", Kind: domainScouting.PostKindStanding,
		Hulls: 1, FreshnessTarget: time.Minute,
	}
	postRepo := &fakeScoutPostRepo{posts: []*domainScouting.ScoutPost{post}}
	shipRepo := &fakeScoutShipRepo{clock: clock} // no hulls
	daemonClient := &fakeScoutDaemonClient{}
	cq := &fakeContainerStatusQuery{}
	mp := &fakeMultiMarketProvider{markets: map[string][]string{"X1-GZ7": {"X1-GZ7-M1", "X1-GZ7-M2", "X1-GZ7-M3"}}}
	fr := &fakeFreshnessReader{snapshots: []domainScouting.SystemFreshnessSnapshot{snap("X1-GZ7", 3, 9000, 300, 2)}}
	store := &fakeScoutEventStore{}
	handler := manningWatchdogHandler(postRepo, shipRepo, daemonClient, cq, mp, fr, store, clock)
	cmd := scoutPostTestCmd()
	cmd.ManningStallCycles = 2
	cmd.UndersizedAvgHopSecs = 20

	for i := 0; i < 5; i++ {
		require.NoError(t, handler.reconcileOnce(context.Background(), cmd))
	}

	require.Zero(t, countManningStalled(store), "an unmanned/under-manned post is out of scope — normal manning owns it")
	require.Empty(t, daemonClient.stopped, "the watchdog never tears down a post it does not fully own")
}

// Knob resolution: launch value → live-tuned override → documented default, mirroring the
// freshness sizer's resolveSizerConfig live-overlay semantics (a zeroed/absent live key
// reverts to the default — the `tune <key> 0` behavior).
func TestScoutPost_ManningStallConfig_ResolvesLiveOverLaunchOverDefault(t *testing.T) {
	cmd := &RunScoutPostCoordinatorCommand{ManningStallCycles: 6, ManningStallCorrectionCap: 0}

	cycles, correctionCap := resolveManningStallConfig(cmd, nil)
	require.Equal(t, 6, cycles, "a positive launch value is honored when no live config is wired")
	require.Equal(t, defaultManningStallCorrectionCap, correctionCap, "an unset knob falls back to its documented default")

	live := liveconfig.Snapshot{"manning_stall_cycles": 9}
	cycles, correctionCap = resolveManningStallConfig(cmd, live)
	require.Equal(t, 9, cycles, "a live-tuned value overrides the launch value on the next tick")
	require.Equal(t, defaultManningStallCorrectionCap, correctionCap, "a key absent from the live snapshot resolves to the documented default")
}
