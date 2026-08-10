package commands

// A HULL ON A CHARTING ERRAND HOLDS NO SLOT ROW, BY DESIGN.
//
// claimSpares stamps the errand into sensing_systems and DELETES the hull's
// placement row, so between steps a seed hull is idle, untagged where the tag
// write lagged, and named by nothing in sensing_slots. To every pass that indexes
// hulls by slot row alone it is indistinguishable from a stranded orphan — so
// adoption re-parks it every tick, and the orphan dispatch would FLY it off its
// mission. Both are two writers on one hull (RULINGS #3).

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// chartingSystem is a frontier row with a hull out on it — the ledger's record of
// an errand, and the only record there is.
func chartingSystem(system, hull string) parkedsensing.ExpandSystem {
	return parkedsensing.ExpandSystem{
		System: system, Verdict: parkedsensing.VerdictPending, CatalogKnown: true,
		UnchartedCount: 4, SeedShip: hull, SeedState: parkedsensing.SeedStateCharting,
	}
}

// Adoption's own re-park is what makes the ghost row PERMANENT: expansion may
// release it, but a pass that writes it back next tick turns the repair into one
// write and one delete every tick, forever.
func TestAdoption_NeverRecordsAHullAlreadyOnAChartingErrand(t *testing.T) {
	world := steadyWorld(t, map[string]string{"X1-IN1": parkedsensing.VerdictInScope})
	world.posts.posts = nil
	world.fleet.ships = []*navigation.Ship{scoutProbe(t, "PROBE-SEED", "X1-FAR9-A1")}
	world.ledger.systems["X1-DARK"] = chartingSystem("X1-DARK", "PROBE-SEED")
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	_, recorded := slotFor(world, "PROBE-SEED")
	require.False(t, recorded,
		"a hull the system row already has out charting must get no slot row — that row is the ghost expansion then has to release")
	require.NotContains(t, world.tagger.tagged, "PROBE-SEED")
	require.Equal(t, 0, logger.payload("parked_sensing_cycle")["adopted_stranded"],
		"and the heartbeat must not report an adoption that would have to be undone")
}

// The sharper half: this pass issues a FLIGHT. Sending a mid-errand hull to a
// placement hands one hull to two drivers and strands the charting it was doing.
func TestOrphanDispatch_NeverFliesAHullAlreadyOnAChartingErrand(t *testing.T) {
	world := liveFleetWorld(t)
	world.ledger.systems["X1-DARK"] = chartingSystem("X1-DARK", "TORWIND-14")
	// The ships table cannot place it this tick, so the seed's own tour stays
	// still and what remains is purely this pass's decision.
	delete(world.shipPos.at, "TORWIND-14")
	logger := &capturingLogger{}

	require.NoError(t, world.handler.ReconcileOnce(common.WithLogger(world.ctx, logger), world.cmd))

	require.Empty(t, dispatchTargetOf(world, "TORWIND-14"),
		"no placement may name a hull that is already charting X1-DARK")
	require.NotContains(t, world.mover.moves, "TORWIND-14",
		"and it is never commanded off its mission")
	require.Equal(t, 3, dispatchedCount(t, logger),
		"the other three stacked orphans are still put to work — the errand guard must not stall the pass")
}

// expansionCycleLine renders one heartbeat for the given expansion report.
func expansionCycleLine(t *testing.T, rep parkedsensing.ExpandReport) (string, map[string]interface{}) {
	t.Helper()
	log := &messageLogger{}
	h := &RunProbeSensingCoordinatorHandler{}
	h.heartbeat(common.WithLogger(t.Context(), log),
		&RunProbeSensingCoordinatorCommand{ContainerID: "probe_sensing_coordinator-player-1-24f32043"},
		sensingConfig{ProbeCap: 800},
		heartbeat{expand: rep})

	if len(log.messages) != 1 {
		t.Fatalf("want exactly one cycle line, got %d", len(log.messages))
	}
	return log.messages[0], log.fields[0]
}

// A repaired invariant nobody can see recurring is one that recurs unwatched: the
// live symptom was hours of "0 seeds requested" with nothing in the line saying why.
func TestHeartbeat_ReleasedSpareGhostsAreReportedWhenTheyBind(t *testing.T) {
	msg, fields := expansionCycleLine(t, parkedsensing.ExpandReport{SeedsRequested: 3, SpareGhostsReleased: 2})

	if !strings.Contains(msg, "2 ghost") {
		t.Fatalf("cycle line does not report the released ghost rows: %q", msg)
	}
	if got := fields["spare_ghosts_released"]; got != 2 {
		t.Fatalf("payload spare_ghosts_released = %v, want 2 — a standing non-zero value is the invariant being broken repeatedly", got)
	}
}

// And the ordinary line is unchanged, so the count reads as an exception rather
// than as noise an operator learns to skip.
func TestHeartbeat_NoGhostReleaseLeavesTheExpansionLineAlone(t *testing.T) {
	msg, fields := expansionCycleLine(t, parkedsensing.ExpandReport{SeedsRequested: 3})

	if strings.Contains(msg, "ghost") {
		t.Fatalf("a tick that released nothing still mentions ghosts: %q", msg)
	}
	if got := fields["spare_ghosts_released"]; got != 0 {
		t.Fatalf("payload spare_ghosts_released = %v, want 0", got)
	}
}
