package grpc

// A PANICKED HEARTBEAT MUST NOT SILENCE A LIVE CONTAINER (sp-739gf item 3).
//
// The heartbeat runs on its own goroutine, independent of the handler, so long work has never been
// able to starve it — that part of the bead's framing describes a mechanism that already exists.
// What CAN produce the reported reading (status RUNNING, heartbeat hours stale, the container still
// logging) is a panic inside the heartbeat write: supervise.Guard logs and suppresses it without
// restarting, so the beat stops for the rest of the container's life while nothing else changes.

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

var errWriteFailed = errors.New("heartbeat write failed")

// heartbeatProbe records beats and panics on the ones a test names.
type heartbeatProbe struct {
	mu       sync.Mutex
	beats    int
	panicOn  map[int]bool
	panicked int
}

func (p *heartbeatProbe) beat() error {
	p.mu.Lock()
	p.beats++
	n := p.beats
	shouldPanic := p.panicOn[n]
	if shouldPanic {
		p.panicked++
	}
	p.mu.Unlock()
	if shouldPanic {
		panic("heartbeat write blew up")
	}
	return nil
}

func (p *heartbeatProbe) counts() (beats, panicked int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.beats, p.panicked
}

// heartbeatRunner is a runner wired for the beat and nothing else — no repository, no mediator, no
// ship assignments: this test is about the goroutine, not about the container's work.
func heartbeatRunner(probe *heartbeatProbe) *ContainerRunner {
	entity := container.NewContainer("hb-1", container.ContainerTypeFleetGrowth, 1, -1, nil, map[string]interface{}{}, nil)
	r := NewContainerRunner(entity, nil, nil, &capturingLogRepo{}, nil, nil, nil)
	r.beat = probe.beat
	r.heartbeatEvery = 2 * time.Millisecond
	return r
}

// THE REGRESSION. Beat 2 panics; beats must keep landing afterwards. Before the fix the goroutine
// died there and the container's heartbeat froze while the container itself ran on.
func TestHeartbeat_ResumesAfterAPanickedWrite(t *testing.T) {
	probe := &heartbeatProbe{panicOn: map[int]bool{2: true}}
	r := heartbeatRunner(probe)
	r.heartbeatStarted.Store(true)
	go r.runHeartbeat()
	defer r.stopHeartbeat()

	require.Eventually(t, func() bool {
		beats, panicked := probe.counts()
		return panicked == 1 && beats >= 5
	}, 2*time.Second, 5*time.Millisecond,
		"the beat must resume after a panicked write — a RUNNING container with a frozen heartbeat is the reading nothing can interpret")
}

// A REPEATEDLY PANICKING WRITE STILL BEATS, and still stops on request. Resuming must not turn one
// broken write into a wedged goroutine that shutdown then waits out.
func TestHeartbeat_KeepsResumingAndStillStops(t *testing.T) {
	probe := &heartbeatProbe{panicOn: map[int]bool{1: true, 2: true, 3: true}}
	r := heartbeatRunner(probe)
	r.heartbeatStarted.Store(true)
	go r.runHeartbeat()

	require.Eventually(t, func() bool {
		_, panicked := probe.counts()
		return panicked >= 3
	}, 2*time.Second, 5*time.Millisecond, "every panicked write must be followed by another attempt")

	done := make(chan struct{})
	go func() { r.stopHeartbeat(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stopHeartbeat did not return — the resumed beat must still honour the stop signal")
	}
}

// AN ORDINARY WRITE ERROR IS NOT A PANIC and was never the bug: it is logged and the beat continues.
// Pinned so a future "simplification" cannot collapse the two paths into one that stops on either.
func TestHeartbeat_ContinuesThroughAFailedWrite(t *testing.T) {
	var beats int
	var mu sync.Mutex
	r := heartbeatRunner(&heartbeatProbe{})
	r.beat = func() error {
		mu.Lock()
		beats++
		mu.Unlock()
		return errWriteFailed
	}
	r.heartbeatStarted.Store(true)
	go r.runHeartbeat()
	defer r.stopHeartbeat()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return beats >= 4
	}, 2*time.Second, 5*time.Millisecond)
}
