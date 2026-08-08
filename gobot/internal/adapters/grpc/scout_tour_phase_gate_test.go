package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	domainScouting "github.com/andrescamacho/spacetraders-go/internal/domain/scouting"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The bootstrap-phase gate on market tours (Admiral 2026-08-08). The standing scout-post
// coordinator is deleted; what is left is an operator-started tour, and this is the rule
// that keeps it inside the bootstrap phase.

// stubPhaseGate scripts the shared EXPANSION verdict.
type stubPhaseGate struct {
	inExpansion bool
	err         error
}

func (g *stubPhaseGate) InExpansion(_ context.Context, _ shared.PlayerID) (bool, error) {
	if g.err != nil {
		return false, g.err
	}
	return g.inExpansion, nil
}

// ANTI-VACUITY CONTROL, first: the ONE window the ruling leaves open must actually be
// open. A refusal test alone would pass on a gate that refused everything.
func TestTourGate_PreExpansion_AdmitsTheOperatorsTour(t *testing.T) {
	s := &DaemonServer{}
	s.SetBootstrapPhaseGate(&stubPhaseGate{inExpansion: false})

	require.NoError(t, s.refuseTourOutsideBootstrap(context.Background(), 1),
		"a manual tour during bootstrap is exactly what the ruling keeps")
}

// Past the gate the tour is refused, and the message must carry the reason and the
// replacement — an operator who is told only "no" reaches for a workaround.
func TestTourGate_Expansion_RefusesAndNamesTheReplacement(t *testing.T) {
	s := &DaemonServer{}
	s.SetBootstrapPhaseGate(&stubPhaseGate{inExpansion: true})

	err := s.refuseTourOutsideBootstrap(context.Background(), 1)

	require.Error(t, err)
	require.Contains(t, err.Error(), "jump gate")
	require.Contains(t, err.Error(), "parked-sensing", "the refusal must name what owns freshness now")
}

// An unreadable phase is not a licence. Refusing an operator's non-spending action costs
// seconds; admitting a circulating hull into the era where nothing is left to notice it
// does not self-correct.
func TestTourGate_UnreadablePhase_FailsClosed(t *testing.T) {
	s := &DaemonServer{}
	s.SetBootstrapPhaseGate(&stubPhaseGate{err: errors.New("construction site unreadable")})

	err := s.refuseTourOutsideBootstrap(context.Background(), 1)

	require.Error(t, err)
	require.Contains(t, err.Error(), "unreadable")
}

// A forgotten wiring must refuse too, and say so as a daemon fault rather than blaming
// the operator — a gate that defaults open is the defect this whole change removes.
func TestTourGate_Unwired_FailsClosed(t *testing.T) {
	s := &DaemonServer{}

	err := s.refuseTourOutsideBootstrap(context.Background(), 1)

	require.Error(t, err)
	require.Contains(t, err.Error(), "not wired")
}

// Both tour-start verbs pass the gate — the single-hull one and the multi-hull VRP one.
// ScoutMarkets spawns tours too, so a gate on only one of them leaves the rule half-made.
func TestTourGate_BothStartVerbsRefusePastTheGate(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)
	s.SetBootstrapPhaseGate(&stubPhaseGate{inExpansion: true})
	// A mediator that ANSWERS. The harness default blocks forever, which would turn an
	// ungated ScoutMarkets into a hung test rather than a failing one — and a test that
	// hangs on regression is a test that gets killed and skipped, not one that reports.
	s.mediator = &refusingMediator{}
	ctx := context.Background()

	_, err := s.ScoutTour(ctx, "tour-1", "PROBE-1", []string{"X1-HQ-A1"}, 1, playerID)
	require.Error(t, err, "`scout tour` must refuse past the gate")
	require.Contains(t, err.Error(), "market tour refused")

	_, _, _, err = s.ScoutMarkets(ctx, []string{"PROBE-1"}, "X1-HQ", []string{"X1-HQ-A1"}, 1, playerID)
	require.Error(t, err, "`scout markets` spawns tours as well and must refuse identically")
	require.Contains(t, err.Error(), "market tour refused")

	require.Zero(t, countContainersOfType(t, db, playerID, container.ContainerTypeScout),
		"a refused tour must persist no container")
}

// --- the retired declare surface -------------------------------------------

// Declaring a post asked a reconciler to man a system. That reconciler is gone, so a post
// written now is desired state nothing satisfies — which reads as a working CLI and is not
// one. The verb refuses and names what replaced it.
func TestAddScoutPost_IsRetiredAndSaysWhatReplacedIt(t *testing.T) {
	s, db, playerID := newRecoveryTestServer(t)

	post, err := s.AddScoutPost(context.Background(), playerID, "X1-FAR", time.Hour, domainScouting.PostKindStanding, 1)

	require.Error(t, err)
	require.Nil(t, post)
	require.Contains(t, err.Error(), "retired")
	require.Contains(t, err.Error(), "scout tour", "the refusal must name the surviving manual verb")

	var rows int64
	require.NoError(t, db.Table("scout_posts").Where("system_symbol = ?", "X1-FAR").Count(&rows).Error)
	require.Zero(t, rows, "a refused declaration must write NOTHING")
}

// The retirement is data-driven and BOTH halves must hold: recovery skips a persisted row
// of a retired type, and the registry has no builder to rebuild one into a ghost engine.
// Either half alone leaves the row able to come back.
func TestRetiredCommandTypes_CoverTheDeletedTouringEngine(t *testing.T) {
	for _, commandType := range []string{"scout_post_coordinator", "shipyard_backfill_coordinator", "scout_reposition"} {
		t.Run(commandType, func(t *testing.T) {
			require.True(t, retiredCommandTypes[commandType],
				"recovery must skip a persisted row of this type rather than report an unexplained loss")
			for _, spec := range containerSpecList() {
				require.NotEqual(t, commandType, spec.CommandType,
					"a retired type with a live builder can be rebuilt into a ghost engine")
			}
		})
	}
}

// ANTI-VACUITY CONTROL for the check above: scout_tour is the survivor, so it must NOT be
// retired and MUST still have a builder. Without this, deleting the whole registry would
// pass every assertion in the retirement test.
func TestRetiredCommandTypes_LeaveTheManualTourAlone(t *testing.T) {
	require.False(t, retiredCommandTypes["scout_tour"],
		"the operator's tour is the one thing the ruling keeps")
	built := false
	for _, spec := range containerSpecList() {
		if spec.CommandType == "scout_tour" {
			built = true
		}
	}
	require.True(t, built, "a manual tour must still be rebuildable across a daemon restart")
}

// refusingMediator answers instantly so a gate regression surfaces as a failed assertion.
type refusingMediator struct{ common.Mediator }

func (m *refusingMediator) Send(context.Context, common.Request) (common.Response, error) {
	return nil, errors.New("mediator not exercised by this test")
}
