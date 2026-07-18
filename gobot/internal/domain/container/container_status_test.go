package container

import (
	"fmt"
	"testing"
)

// TestContainerStatusProjectsLifecycleState is a characterization test that
// pins the exact lifecycle-state -> ContainerStatus projection (and the
// Container-specific STOPPING extension that precedes it) BEFORE the projection
// is delegated to shared.ProjectStatus. Every row must remain identical across
// that refactor. INTERRUPTED and the switch default are intentionally absent:
// `interrupted` is never set true anywhere in the codebase (dormant recovery
// state) and the lifecycle machine is always in one of its five valid states,
// so neither is reachable through the public API. They are preserved verbatim
// in production and covered structurally by the shared primitive's own tests.
func TestContainerStatusProjectsLifecycleState(t *testing.T) {
	cases := []struct {
		name  string
		drive func(t *testing.T, c *Container)
		want  ContainerStatus
	}{
		{"pending on construction", func(t *testing.T, c *Container) {}, ContainerStatusPending},
		{"running after start", func(t *testing.T, c *Container) {
			mustDo(t, "Start", c.Start())
		}, ContainerStatusRunning},
		{"completed after complete", func(t *testing.T, c *Container) {
			mustDo(t, "Start", c.Start())
			mustDo(t, "Complete", c.Complete())
		}, ContainerStatusCompleted},
		{"failed after fail", func(t *testing.T, c *Container) {
			mustDo(t, "Start", c.Start())
			mustDo(t, "Fail", c.Fail(fmt.Errorf("boom")))
		}, ContainerStatusFailed},
		{"stopped after stop from pending", func(t *testing.T, c *Container) {
			mustDo(t, "Stop", c.Stop())
		}, ContainerStatusStopped},
		{"stopping extension after stop from running", func(t *testing.T, c *Container) {
			mustDo(t, "Start", c.Start())
			mustDo(t, "Stop", c.Stop())
		}, ContainerStatusStopping},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewContainer("c-1", ContainerTypeTrading, 1, -1, nil, nil, nil)
			tc.drive(t, c)
			if got := c.Status(); got != tc.want {
				t.Fatalf("Status() = %q, want %q", got, tc.want)
			}
		})
	}
}

func mustDo(t *testing.T, action string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
}
