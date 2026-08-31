package grpc

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/api"
)

func TestHullRepairScheduler_SweepsOnItsCadence(t *testing.T) {
	var calls atomic.Int32
	fired := make(chan struct{}, 8)
	s := NewHullRepairScheduler(func(context.Context) error {
		calls.Add(1)
		select {
		case fired <- struct{}{}:
		default:
		}
		return nil
	}, 10*time.Millisecond)

	go func() { _ = s.Run(context.Background()) }()
	defer s.Stop()

	select {
	case <-fired:
	case <-time.After(2 * time.Second):
		t.Fatal("the repair sweep never ran")
	}
	require.GreaterOrEqual(t, calls.Load(), int32(1))
}

// A sweep that fails must not take the standing loop down with it: the fault it repairs
// appears with nobody watching, so the loop has to outlive every bad pass.
func TestHullRepairScheduler_SurvivesAFailingSweep(t *testing.T) {
	var calls atomic.Int32
	logged := make(chan string, 8)
	s := NewHullRepairScheduler(func(context.Context) error {
		calls.Add(1)
		return errors.New("probe refused")
	}, 5*time.Millisecond)
	s.logf = func(format string, args ...interface{}) {
		select {
		case logged <- format:
		default:
		}
	}

	go func() { _ = s.Run(context.Background()) }()
	defer s.Stop()

	select {
	case <-logged:
	case <-time.After(2 * time.Second):
		t.Fatal("a failing sweep must be reported, not swallowed")
	}
	require.Eventually(t, func() bool { return calls.Load() >= 2 }, 2*time.Second, 5*time.Millisecond,
		"the loop must keep sweeping after a failure")
}

func TestHullRepairScheduler_StopsOnContextCancel(t *testing.T) {
	s := NewHullRepairScheduler(func(context.Context) error { return nil }, 5*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "a cancelled loop is a clean stop, not a supervised crash")
	case <-time.After(2 * time.Second):
		t.Fatal("the loop did not stop on cancellation")
	}
}

// Ships ON with no config present (RULINGS #22): a non-positive interval is the default,
// never "disabled".
func TestHullRepairScheduler_DefaultsToItsStandingCadence(t *testing.T) {
	require.Equal(t, defaultHullRepairInterval, NewHullRepairScheduler(nil, 0).interval)
	require.Equal(t, defaultHullRepairInterval, NewHullRepairScheduler(nil, -time.Second).interval)
}

// A partial fleet read that could attribute the failure to no hull at all names a
// placeholder. It is not a ship symbol and must never open a repair episode against one.
func TestRecordUnreadableHullsSkipsTheUnattributedPlaceholder(t *testing.T) {
	server := &DaemonServer{}
	require.NotPanics(t, func() {
		server.RecordUnreadableHulls(context.Background(), 10, []string{api.UnidentifiedHull, ""})
	}, "a nil-db daemon must not be reachable through a placeholder name")
}
