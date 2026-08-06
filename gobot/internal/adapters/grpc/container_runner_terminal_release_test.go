package grpc

// sp-vz8hj. A hung tour container was killed by the watchdog every ~31 seconds for nineteen
// minutes — 38 identical "cannot stop container in COMPLETED state" lines against ONE container —
// while the hull it had claimed sat laden and undispatched.
//
// The livelock is a symptom. The root is here: ContainerRunner.Stop returns the terminal-state
// error from its FIRST statement, and releaseShipAssignments is its LAST — so the one cleanup a
// terminal container still owes is exactly the one an early return skips. The hull stays pinned to
// a dead container, the trade coordinator skips it (assignment_status is still 'active'), and every
// subsequent kill re-hits the same refusal.
//
// The safety property this must NOT break: a stop that fails against a LIVE container still fails,
// and its hull is still NOT released — that container may genuinely still be flying the hull.

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// releaseSpyShipRepo records whether the runner asked for this container's hulls at all.
// FindByContainer is the first thing releaseShipAssignments does after its nil guard, so it is the
// narrowest observable that distinguishes "the release ran" from "the release was skipped".
type releaseSpyShipRepo struct {
	navigation.ShipRepository
	askedFor []string
}

func (r *releaseSpyShipRepo) FindByContainer(_ context.Context, containerID string, _ shared.PlayerID) ([]*navigation.Ship, error) {
	r.askedFor = append(r.askedFor, containerID)
	return nil, nil
}

func terminalRunner(t *testing.T, drive func(*container.Container)) (*ContainerRunner, *releaseSpyShipRepo) {
	t.Helper()
	entity := container.NewContainer("tour-run-TORWIND-F-52ea6fe2", container.ContainerType("tour_run"), 4, 1, nil, nil, nil)
	drive(entity)
	spy := &releaseSpyShipRepo{}
	return &ContainerRunner{
		containerEntity: entity,
		logRepo:         silentLogRepo{},
		shipRepo:        spy,
	}, spy
}

// THE DEFECT. Stopping a container that has already COMPLETED must hand its hull back, because a
// terminal container is not flying anything. Against the unfixed runner this returns the
// terminal-state error with the hull still pinned, which is what left TORWIND-F undispatchable.
func TestRunnerStop_ReleasesTheHullOfAnAlreadyCompletedContainer(t *testing.T) {
	runner, spy := terminalRunner(t, func(c *container.Container) {
		if err := c.Start(); err != nil {
			t.Fatalf("precondition: %v", err)
		}
		if err := c.Complete(); err != nil {
			t.Fatalf("precondition: %v", err)
		}
	})

	if err := runner.Stop(); err != nil {
		t.Fatalf("stopping an already-COMPLETED container must succeed — it is already stopped — got: %v", err)
	}
	if len(spy.askedFor) != 1 {
		t.Fatalf("the hull was never released: releaseShipAssignments did not run (%d lookups), so the hull stays pinned to a dead container and the coordinator keeps skipping it", len(spy.askedFor))
	}
	if spy.askedFor[0] != "tour-run-TORWIND-F-52ea6fe2" {
		t.Errorf("released the wrong container's hulls: %q", spy.askedFor[0])
	}
}

// The same must hold for a container already STOPPED — the other terminal state, and the one a
// second kill attempt lands on after the first succeeded. Without it the watchdog's retry is still
// an error every time.
func TestRunnerStop_ReleasesTheHullOfAnAlreadyStoppedContainer(t *testing.T) {
	runner, spy := terminalRunner(t, func(c *container.Container) {
		// STOPPED is reachable only through STOPPING: start, stop (which arms STOPPING), finalize.
		if err := c.Start(); err != nil {
			t.Fatalf("precondition: %v", err)
		}
		if err := c.Stop(); err != nil {
			t.Fatalf("precondition: %v", err)
		}
		if err := c.MarkStopped(); err != nil {
			t.Fatalf("precondition: %v", err)
		}
	})

	if err := runner.Stop(); err != nil {
		t.Fatalf("stopping an already-STOPPED container must succeed, got: %v", err)
	}
	if len(spy.askedFor) != 1 {
		t.Fatalf("the hull was never released for an already-STOPPED container (%d lookups)", len(spy.askedFor))
	}
}

// Idempotent: a second stop of the same terminal container must stay successful and must not error
// back into the caller. This is the watchdog's actual shape — it re-attempts on a fixed cadence.
func TestRunnerStop_IsIdempotentOnATerminalContainer(t *testing.T) {
	runner, spy := terminalRunner(t, func(c *container.Container) {
		// STOPPED is reachable only through STOPPING: start, stop (which arms STOPPING), finalize.
		if err := c.Start(); err != nil {
			t.Fatalf("precondition: %v", err)
		}
		if err := c.Stop(); err != nil {
			t.Fatalf("precondition: %v", err)
		}
		if err := c.MarkStopped(); err != nil {
			t.Fatalf("precondition: %v", err)
		}
	})

	for attempt := 1; attempt <= 3; attempt++ {
		if err := runner.Stop(); err != nil {
			t.Fatalf("attempt %d must succeed, got: %v", attempt, err)
		}
	}
	if len(spy.askedFor) != 3 {
		t.Errorf("each stop must re-attempt the release (it no-ops when nothing is pinned), got %d", len(spy.askedFor))
	}
}
