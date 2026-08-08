package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The graduation edge for market tours. Refusing to START one past the gate is only half
// the rule — a tour already flying HOLDS A PROBE, and that probe is the supply parked
// sensing is about to place.

// fakeTourSweeper records what the sweep asked of the container registry.
type fakeTourSweeper struct {
	running  []string
	listErr  error
	stopErr  map[string]error
	stopped  []string
	listCall int
}

func (s *fakeTourSweeper) RunningTours(_ context.Context, _ shared.PlayerID) ([]string, error) {
	s.listCall++
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.running, nil
}

func (s *fakeTourSweeper) StopTourAndReleaseHull(_ context.Context, _ shared.PlayerID, containerID string) error {
	if err := s.stopErr[containerID]; err != nil {
		return err
	}
	s.stopped = append(s.stopped, containerID)
	return nil
}

func sweepTestCmd() *RunProbeSensingCoordinatorCommand {
	return &RunProbeSensingCoordinatorCommand{
		PlayerID:    shared.MustNewPlayerID(1),
		ContainerID: "sensing-1",
	}
}

// The edge STOPS work rather than merely declining to start it.
func TestLegacyTourSweep_StopsEveryRunningTour(t *testing.T) {
	h := &RunProbeSensingCoordinatorHandler{}
	sweeper := &fakeTourSweeper{running: []string{"tour-1", "tour-2"}}
	h.SetLegacyTourSweeper(sweeper)

	stopped := h.sweepLegacyTours(context.Background(), sweepTestCmd())

	require.Equal(t, 2, stopped)
	require.ElementsMatch(t, []string{"tour-1", "tour-2"}, sweeper.stopped,
		"a tour left flying past the gate holds a probe parked sensing needs to place")
}

// It runs on EVERY tick, not once at the edge. Container recovery re-adopts scout_tour
// workers across a daemon restart, so a boot that lands past the edge re-floats a tour
// nothing else would ever stop — a one-shot sweep would miss exactly that case.
func TestLegacyTourSweep_KeepsSweepingOnLaterTicks(t *testing.T) {
	h := &RunProbeSensingCoordinatorHandler{}
	sweeper := &fakeTourSweeper{running: []string{"tour-1"}}
	h.SetLegacyTourSweeper(sweeper)
	cmd := sweepTestCmd()

	require.Equal(t, 1, h.sweepLegacyTours(context.Background(), cmd))

	// A restart re-floated a tour; the next tick must still catch it.
	sweeper.running = []string{"tour-recovered"}
	require.Equal(t, 1, h.sweepLegacyTours(context.Background(), cmd))
	require.Contains(t, sweeper.stopped, "tour-recovered")
	require.Equal(t, 2, sweeper.listCall, "the sweep is per-tick, never latched")
}

// ANTI-VACUITY CONTROL: a quiet fleet costs one list and stops nothing. Without this, a
// sweep that stopped everything unconditionally would pass the tests above.
func TestLegacyTourSweep_NoToursIsAQuietNoOp(t *testing.T) {
	h := &RunProbeSensingCoordinatorHandler{}
	sweeper := &fakeTourSweeper{}
	h.SetLegacyTourSweeper(sweeper)

	require.Zero(t, h.sweepLegacyTours(context.Background(), sweepTestCmd()))
	require.Empty(t, sweeper.stopped)
}

// One tour that will not stop must not shield the others — the loop continues and reports
// honestly, because the alternative is a single stuck container holding the whole fleet's
// probes hostage.
func TestLegacyTourSweep_OneFailureDoesNotAbortTheRest(t *testing.T) {
	h := &RunProbeSensingCoordinatorHandler{}
	sweeper := &fakeTourSweeper{
		running: []string{"tour-stuck", "tour-2"},
		stopErr: map[string]error{"tour-stuck": errors.New("container gone")},
	}
	h.SetLegacyTourSweeper(sweeper)
	logger := &sweepCaptureLogger{}

	stopped := h.sweepLegacyTours(common.WithLogger(context.Background(), logger), sweepTestCmd())

	require.Equal(t, 1, stopped, "the count must report what actually stopped, not what was attempted")
	require.Equal(t, []string{"tour-2"}, sweeper.stopped)
	require.True(t, logger.has("tour-stuck"), "the tour that would not stop must be NAMED, never a silent skip")
}

// An unreadable container list is not evidence that no tour is flying, so nothing is
// claimed and the failure is loud.
func TestLegacyTourSweep_UnreadableListIsLoudAndStopsNothing(t *testing.T) {
	h := &RunProbeSensingCoordinatorHandler{}
	sweeper := &fakeTourSweeper{listErr: errors.New("db down")}
	h.SetLegacyTourSweeper(sweeper)
	logger := &sweepCaptureLogger{}

	require.Zero(t, h.sweepLegacyTours(common.WithLogger(context.Background(), logger), sweepTestCmd()))
	require.Empty(t, sweeper.stopped)
	require.True(t, logger.has("Graduation tour sweep skipped"))
}

// An unwired sweeper is inert rather than fatal: an unswept tour wastes a probe, while
// holding the whole sensing tick over one would trade a large harm for a small one.
func TestLegacyTourSweep_UnwiredIsInertNotFatal(t *testing.T) {
	h := &RunProbeSensingCoordinatorHandler{}
	require.Zero(t, h.sweepLegacyTours(context.Background(), sweepTestCmd()))
}

// sweepCaptureLogger records the sweep's lines.
type sweepCaptureLogger struct{ messages []string }

func (l *sweepCaptureLogger) Log(_, message string, _ map[string]interface{}) {
	l.messages = append(l.messages, message)
}

func (l *sweepCaptureLogger) has(sub string) bool {
	for _, m := range l.messages {
		if len(sub) > 0 && len(m) >= len(sub) && containsSub(m, sub) {
			return true
		}
	}
	return false
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
