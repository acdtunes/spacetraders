package shared

import (
	"fmt"
	"testing"
)

// TestProjectStatusMapsPresentStateToMappedValue pins the first behavior of the
// shared projection primitive: for every lifecycle state present in the table,
// ProjectStatus returns that state's mapped domain value. A generic local
// target type proves the primitive projects onto any string-backed status enum,
// exactly what Container/StorageOperation/Route each need.
func TestProjectStatusMapsPresentStateToMappedValue(t *testing.T) {
	type projected string
	const fallback projected = "FALLBACK"
	table := map[LifecycleStatus]projected{
		LifecycleStatusPending:   "P",
		LifecycleStatusRunning:   "R",
		LifecycleStatusCompleted: "C",
		LifecycleStatusFailed:    "F",
		LifecycleStatusStopped:   "S",
	}

	cases := []struct {
		name  string
		drive func(t *testing.T, sm *LifecycleStateMachine)
		want  projected
	}{
		{"pending", func(t *testing.T, sm *LifecycleStateMachine) {}, "P"},
		{"running", func(t *testing.T, sm *LifecycleStateMachine) {
			if err := sm.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
		}, "R"},
		{"completed", func(t *testing.T, sm *LifecycleStateMachine) {
			if err := sm.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if err := sm.Complete(); err != nil {
				t.Fatalf("Complete: %v", err)
			}
		}, "C"},
		{"failed", func(t *testing.T, sm *LifecycleStateMachine) {
			if err := sm.Start(); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if err := sm.Fail(fmt.Errorf("boom")); err != nil {
				t.Fatalf("Fail: %v", err)
			}
		}, "F"},
		{"stopped", func(t *testing.T, sm *LifecycleStateMachine) {
			if err := sm.Stop(); err != nil {
				t.Fatalf("Stop: %v", err)
			}
		}, "S"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sm := NewLifecycleStateMachine(nil)
			tc.drive(t, sm)
			if got := ProjectStatus(sm, table, fallback); got != tc.want {
				t.Fatalf("ProjectStatus in %s state = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

// TestProjectStatusReturnsFallbackWhenStateAbsentFromTable pins the second
// behavior: a lifecycle state with no table entry takes the fallback — the
// exact semantics of the switch `default:` arm the three aggregates relied on.
func TestProjectStatusReturnsFallbackWhenStateAbsentFromTable(t *testing.T) {
	type projected string
	const fallback projected = "FALLBACK"
	// Table intentionally omits RUNNING; the machine is driven INTO running, so
	// the lookup misses and must fall back.
	table := map[LifecycleStatus]projected{
		LifecycleStatusPending: "P",
	}

	sm := NewLifecycleStateMachine(nil)
	if err := sm.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if got := ProjectStatus(sm, table, fallback); got != fallback {
		t.Fatalf("ProjectStatus with state absent from table = %q, want fallback %q", got, fallback)
	}
}
