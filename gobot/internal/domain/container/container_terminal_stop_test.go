package container_test

// sp-vz8hj. A stop refused because the container is ALREADY TERMINAL and a stop that failed against
// a LIVE container are opposite conditions demanding opposite responses, and until now they were
// the same untyped error. ContainerRunner.Stop discriminates on the sentinel below to decide
// whether to hand the hull back or to keep it, so the sentinel must appear on exactly the terminal
// states and on nothing else.

import (
	"errors"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

func newTestContainer(t *testing.T) *container.Container {
	t.Helper()
	return container.NewContainer("tour-run-TORWIND-F-52ea6fe2", container.ContainerType("tour_run"), 4, 1, nil, nil, nil)
}

func TestStop_OnACompletedContainerCarriesTheTerminalSentinel(t *testing.T) {
	c := newTestContainer(t)
	if err := c.Start(); err != nil {
		t.Fatalf("precondition: %v", err)
	}
	if err := c.Complete(); err != nil {
		t.Fatalf("precondition: %v", err)
	}

	err := c.Stop()
	if err == nil {
		t.Fatal("stopping a COMPLETED container must still report the refusal to its immediate caller")
	}
	if !errors.Is(err, container.ErrContainerAlreadyTerminal) {
		t.Fatalf("the refusal must be identifiable as terminal-state, or callers can only string-match it: %v", err)
	}
}

func TestStop_OnAStoppedContainerCarriesTheTerminalSentinel(t *testing.T) {
	c := newTestContainer(t)
	if err := c.Start(); err != nil {
		t.Fatalf("precondition: %v", err)
	}
	if err := c.Stop(); err != nil { // arms STOPPING
		t.Fatalf("precondition: %v", err)
	}
	if err := c.MarkStopped(); err != nil {
		t.Fatalf("precondition: %v", err)
	}

	if err := c.Stop(); !errors.Is(err, container.ErrContainerAlreadyTerminal) {
		t.Fatalf("an already-STOPPED container's refusal must carry the terminal sentinel: %v", err)
	}
}

// THE DISCRIMINATION, from the other side. A RUNNING container is the case the whole guard exists
// to protect: stopping it is a legitimate transition, and if its result ever carried the terminal
// sentinel the runner would hand back a hull that is still being flown.
func TestStop_OnARunningContainerNeitherFailsNorLooksTerminal(t *testing.T) {
	c := newTestContainer(t)
	if err := c.Start(); err != nil {
		t.Fatalf("precondition: %v", err)
	}

	err := c.Stop()
	if err != nil {
		t.Fatalf("stopping a RUNNING container is a legitimate transition, got: %v", err)
	}
	if errors.Is(err, container.ErrContainerAlreadyTerminal) {
		t.Fatal("a RUNNING container must never be reported terminal")
	}
}

// THE MESSAGE IS A CONTRACT, not just prose. adapters/cli/universe_transition.go decides whether a
// stop failure was benign by testing `strings.Contains(lower(err), "cannot stop container in")`.
// Wrapping the error changed its text, and a change that dropped or reworded that prefix would
// silently break that caller while every typed assertion above still passed.
func TestStop_TerminalRefusalKeepsThePrefixTheCLIMatchesOn(t *testing.T) {
	c := newTestContainer(t)
	if err := c.Start(); err != nil {
		t.Fatalf("precondition: %v", err)
	}
	if err := c.Complete(); err != nil {
		t.Fatalf("precondition: %v", err)
	}

	err := c.Stop()
	if err == nil {
		t.Fatal("precondition: the stop must be refused")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "cannot stop container in") {
		t.Fatalf("the CLI's benign-failure check matches on this prefix; the message must keep it: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "COMPLETED") {
		t.Errorf("the refusal must still name the state it refused from: %q", err.Error())
	}
}
