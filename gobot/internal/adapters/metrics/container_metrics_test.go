package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// fakeContainerInfo is a minimal ContainerInfo implementation for exercising
// RecordContainerExit without a real container_runner.
type fakeContainerInfo struct {
	playerID         int
	containerType    container.ContainerType
	status           container.ContainerStatus
	restartCount     int
	currentIteration int
	runtimeDuration  time.Duration
}

func (f fakeContainerInfo) PlayerID() int                     { return f.playerID }
func (f fakeContainerInfo) Type() container.ContainerType     { return f.containerType }
func (f fakeContainerInfo) Status() container.ContainerStatus { return f.status }
func (f fakeContainerInfo) RestartCount() int                 { return f.restartCount }
func (f fakeContainerInfo) CurrentIteration() int             { return f.currentIteration }
func (f fakeContainerInfo) RuntimeDuration() time.Duration    { return f.runtimeDuration }

// TestContainerMetrics_RegisterAndExport proves container_exit_total (sp-dp92 P9)
// REGISTERS on the daemon's registry AND actually appears by name once observed —
// the bopj P10 trap where a family was "registered" yet never showed on /metrics.
func TestContainerMetrics_RegisterAndExport(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewContainerMetricsCollector(nil, nil)
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordContainerExit(fakeContainerInfo{
		playerID:      1,
		containerType: container.ContainerTypeTrading,
		status:        container.ContainerStatusCompleted,
	})

	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error: %v", err)
	}
	got := map[string]bool{}
	for _, f := range families {
		got[f.GetName()] = true
	}
	const want = "spacetraders_daemon_container_exit_total"
	if !got[want] {
		t.Errorf("metric %q registered but not exported on the registry", want)
	}
}

// TestContainerMetrics_LabelsAndValues pins container_exit_total's label set
// (player_id, command_type, status) and that repeat records accumulate on the
// right series.
func TestContainerMetrics_LabelsAndValues(t *testing.T) {
	prev := Registry
	t.Cleanup(func() { Registry = prev })
	Registry = prometheus.NewRegistry()

	c := NewContainerMetricsCollector(nil, nil)
	if err := c.Register(); err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	c.RecordContainerExit(fakeContainerInfo{playerID: 7, containerType: container.ContainerTypeTrading, status: container.ContainerStatusCompleted})
	c.RecordContainerExit(fakeContainerInfo{playerID: 7, containerType: container.ContainerTypeTrading, status: container.ContainerStatusCompleted})
	c.RecordContainerExit(fakeContainerInfo{playerID: 7, containerType: container.ContainerTypeTrading, status: container.ContainerStatusFailed})

	const name = "spacetraders_daemon_container_exit_total"

	cases := []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"trading completed", map[string]string{"player_id": "7", "command_type": "TRADING", "status": "COMPLETED"}, 2},
		{"trading failed", map[string]string{"player_id": "7", "command_type": "TRADING", "status": "FAILED"}, 1},
	}
	for _, tc := range cases {
		got, ok := gatherCounter(t, Registry, name, tc.labels)
		if !ok {
			t.Errorf("%s: series %s%v not found", tc.name, name, tc.labels)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: %s%v = %v, want %v", tc.name, name, tc.labels, got, tc.want)
		}
	}
}

// TestContainerMetrics_NilSafe mirrors the sibling metric families' guarantee:
// a recording miss on a typed-nil receiver or an uninitialized collector must
// degrade to a no-op, never a SIGSEGV that would take down container_runner's
// terminal exit path (RULINGS #4 — observation only).
func TestContainerMetrics_NilSafe(t *testing.T) {
	var nilC *ContainerMetricsCollector
	nilC.RecordContainerExit(fakeContainerInfo{playerID: 1, containerType: container.ContainerTypeTrading, status: container.ContainerStatusCompleted})

	empty := &ContainerMetricsCollector{}
	empty.RecordContainerExit(fakeContainerInfo{playerID: 1, containerType: container.ContainerTypeTrading, status: container.ContainerStatusFailed})
}
