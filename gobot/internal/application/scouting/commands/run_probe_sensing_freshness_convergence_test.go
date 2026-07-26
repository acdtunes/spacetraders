package commands

// The live-outage regression: the sensing coordinator ADOPTED 231
// era-4 standing posts carrying freshness_target_seconds=10800 (3h), and the T6
// deferral ("resize writes never refresh FreshnessTarget on existing posts")
// left them stamped that way forever. The scout tour deliberately paces each
// circuit to its post's target, so every adopted system's markets aged past the
// trade planner's 75-min firm-sink freshness cap; the cap (fail-closed, CORRECT
// — RULINGS #4, untouched) refused every buy, the stall watchdog start/kill-
// looped the trade fleet, and trading flatlined (live: X1-ND33 hulls=2
// target=10800, 19/20 markets stale; 4,507 waypoints stale fleet-wide).
//
// These tests pin the convergence half of the fix: a live STANDING post whose
// FreshnessTarget differs from the resolved config target is refreshed through
// the SAME narrow UpdateSensingState seam as every other live-post delta — and
// a converged post writes nothing (the steady-state zero-write guard holds, so
// the refresh can never become per-tick write amplification).

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
)

// adoptedEraFourPost is the live outage fixture: an adopted standing post —
// budget already resized by sensing, hot set census-true, awake — still
// carrying the dead era's 3h pacing target as its ONLY divergence, so these
// tests isolate the freshness-refresh trigger from every other delta.
func adoptedEraFourPost(system string, hulls, hotN int) *domainScouting.ScoutPost {
	post := hotSensingPost(system, hulls, hotN)
	post.FreshnessTarget = 3 * time.Hour // the era-4 10800s shape
	return post
}

func TestSensing_AdoptedPostFreshnessConvergesToConfigTarget(t *testing.T) {
	// X1-ND33, the measured outage system: 13 hot markets (past the
	// second-probe threshold ⇒ the plan wants exactly its current 2 hulls).
	rows := richRows("X1-ND33", 13)
	pr := newSensingPostRepo(adoptedEraFourPost("X1-ND33", 2, 13))
	h, _ := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	require.Empty(t, pr.upserts, "a freshness refresh is a narrow delta, never a full-row Upsert")
	writes := pr.stateWritesFor("X1-ND33")
	require.Len(t, writes, 1,
		"a post differing ONLY in freshness target is still a live-post delta — leaving it unwritten is the T6 deferral that became the outage")
	require.Equal(t, time.Hour, writes[0].freshnessTarget, "the refresh carries the resolved CONFIG target")
	require.Equal(t, 2, writes[0].hulls, "the refresh never perturbs the hull budget")
	require.False(t, writes[0].dormant, "the refresh never perturbs the dormancy bit")
	require.Equal(t, hotWaypointsFor("X1-ND33", 13), writes[0].hotWaypoints, "the refresh never perturbs the hot set")
	require.Equal(t, time.Hour, pr.find("X1-ND33").FreshnessTarget)

	// Tick 2: converged ⇒ zero writes. The refresh is a one-shot convergence,
	// never a per-tick rewrite (the write-amplification guard).
	pr.stateWrites = nil
	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))
	require.Empty(t, pr.stateWrites, "a converged post stays zero-write in steady state")
	require.Empty(t, pr.upserts)
}

// The target is CONFIG-owned (RULINGS #5): an explicit launch/tune value — not
// the 3600 code default — is what live posts converge to. A refresh hardcoding
// the default would silently fight every operator retune.
func TestSensing_FreshnessConvergesToExplicitConfigTarget(t *testing.T) {
	rows := richRows("X1-ND33", 3)
	pr := newSensingPostRepo(adoptedEraFourPost("X1-ND33", 1, 3))
	h, _ := newSensingHandler(&fakeDepthReader{rows: rows}, pr, calmFleet(t), &fakePressure{})

	cmd := sensingCmd()
	cmd.FreshnessTargetSecs = 5400 // 90m — an explicit config target outranks the code default
	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))

	writes := pr.stateWritesFor("X1-ND33")
	require.Len(t, writes, 1)
	require.Equal(t, 90*time.Minute, writes[0].freshnessTarget,
		"the refresh converges to the RESOLVED config target, never a hardcoded default")

	pr.stateWrites = nil
	require.NoError(t, h.ReconcileOnce(context.Background(), cmd))
	require.Empty(t, pr.stateWrites, "a post already at the explicit target writes nothing")
}

// The floored-keep path (an out-of-scope MinHulls post — the home story) is a
// live standing post too: its stale era-4 target paces the HOME markets stale
// just the same, so the wake/refresh write converges it — while still never
// shrinking the post and never removing it.
func TestSensing_FlooredKeepPathConvergesFreshness(t *testing.T) {
	home := sensingPost("X1-HOME", 2)
	home.MinHulls = 2
	home.HotWaypoints = []string{"X1-HOME-W0"} // census-true under thinRows
	home.FreshnessTarget = 3 * time.Hour       // the adopted era-4 shape
	// HOME is census-visible but below the depth floor ⇒ out of scope ⇒ kept.
	pr := newSensingPostRepo(home)
	h, _ := newSensingHandler(&fakeDepthReader{rows: thinRows("X1-HOME")}, pr, calmFleet(t), &fakePressure{})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	require.Empty(t, pr.removed, "the floored post is kept")
	require.Empty(t, pr.upsertsFor("X1-HOME"), "the kept-post refresh is a narrow delta, never a full-row Upsert")
	writes := pr.stateWritesFor("X1-HOME")
	require.Len(t, writes, 1, "an awake, hot-converged floored post still refreshes its stale target")
	require.Equal(t, time.Hour, writes[0].freshnessTarget)
	require.Equal(t, 2, writes[0].hulls, "the refresh never shrinks the kept post")
	require.False(t, writes[0].dormant)
	require.Equal(t, time.Hour, pr.find("X1-HOME").FreshnessTarget)

	pr.stateWrites = nil
	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))
	require.Empty(t, pr.stateWrites, "the converged home post stays zero-write")
}

// Sweep-once posts are the frontier's: the standing diff skips their systems
// entirely, so a stale target on an open sweep is NEVER "converged" — a
// standing-shaped write against a sweep system would clobber the frontier row
// (Upsert and the delta seam are both keyed by (player, system)).
func TestSensing_SweepOncePostFreshnessNeverConverged(t *testing.T) {
	sweep := sensingSweepPost("X1-AA1")
	sweep.FreshnessTarget = 3 * time.Hour
	pr := newSensingPostRepo(sweep)
	h, _ := newSensingHandler(&fakeDepthReader{rows: richRows("X1-AA1", 3)}, pr, calmFleet(t), &fakePressure{})

	require.NoError(t, h.ReconcileOnce(context.Background(), sensingCmd()))

	require.Empty(t, pr.stateWritesFor("X1-AA1"), "a sweep-once post is the frontier's — sensing never rewrites it")
	require.Empty(t, pr.upsertsFor("X1-AA1"))
	require.Equal(t, 3*time.Hour, pr.find("X1-AA1").FreshnessTarget)
}
