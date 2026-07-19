package captain

import "testing"

// TestDefaultInterruptTypesIsExactlyTheApprovedSet locks the default interrupt
// set to exactly the eleven approved event types: everything else
// (workflow.finished, contract.completed, credits.threshold, ship.idle, and the
// self-healing single container.crashed) is deferred and rides the next wake's
// batch instead of forcing one. The actionable crash signal is the crash LOOP
// (container.crashloop), not the single death.
func TestDefaultInterruptTypesIsExactlyTheApprovedSet(t *testing.T) {
	want := map[EventType]bool{
		EventWorkflowFailed:           true,
		EventContainerCrashLoop:       true,
		EventContainerLost:            true,
		EventPinnedHullContainerless:  true,
		EventHeartbeatLost:            true,
		EventContractFailed:           true,
		EventIncomeStalled:            true,
		EventStreamDown:               true,
		EventCoordinatorErrorLoop:     true,
		EventDaemonComponentCrashLoop: true,
		EventPrometheusAlertFiring:    true,
	}

	got := DefaultInterruptTypes()
	if len(got) != len(want) {
		t.Fatalf("DefaultInterruptTypes() = %v (len %d), want len %d", got, len(got), len(want))
	}
	for _, typ := range got {
		if !want[typ] {
			t.Errorf("DefaultInterruptTypes() contains unexpected type %s", typ)
		}
		delete(want, typ)
	}
	if len(want) != 0 {
		t.Errorf("DefaultInterruptTypes() missing types: %v", want)
	}
}

func TestIsInterruptUnderDefaultSet(t *testing.T) {
	tests := []struct {
		name string
		typ  EventType
		want bool
	}{
		{"workflow.failed interrupts", EventWorkflowFailed, true},
		{"container.crashloop interrupts", EventContainerCrashLoop, true},
		{"container.lost interrupts (recovery-lost does not self-heal, sp-tit8)", EventContainerLost, true},
		{"hull.containerless interrupts (stranded pinned hull, sp-v63s)", EventPinnedHullContainerless, true},
		{"container.heartbeat_lost interrupts", EventHeartbeatLost, true},
		{"contract.failed interrupts", EventContractFailed, true},
		{"income.stalled interrupts", EventIncomeStalled, true},
		{"stream.down interrupts", EventStreamDown, true},
		{"prometheus.alert_firing interrupts (revenue/capacity-critical page, sp-y0f6)", EventPrometheusAlertFiring, true},
		{"container.crashed defers (self-healing single, sp-no9i)", EventContainerCrashed, false},
		{"workflow.finished defers", EventWorkflowFinished, false},
		{"contract.completed defers", EventContractCompleted, false},
		{"credits.threshold defers", EventCreditsThreshold, false},
		{"ship.idle defers", EventShipIdle, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsInterrupt(tt.typ, nil); got != tt.want {
				t.Errorf("IsInterrupt(%s, nil) = %v, want %v", tt.typ, got, tt.want)
			}
			// An explicitly empty (non-nil) override must fall back to the
			// default set exactly like nil does.
			if got := IsInterrupt(tt.typ, []EventType{}); got != tt.want {
				t.Errorf("IsInterrupt(%s, []) = %v, want %v", tt.typ, got, tt.want)
			}
		})
	}
}

// TestIsInterruptCaptainOverrideReplacesDefaultSet verifies a captain-
// declared InterruptTypes policy REPLACES the default set rather than
// extending it: a type outside the override no longer interrupts, even if
// it is one of the built-in defaults.
func TestIsInterruptCaptainOverrideReplacesDefaultSet(t *testing.T) {
	override := []EventType{EventShipIdle}

	if !IsInterrupt(EventShipIdle, override) {
		t.Error("IsInterrupt(EventShipIdle, [EventShipIdle]) = false, want true")
	}
	if IsInterrupt(EventWorkflowFailed, override) {
		t.Error("IsInterrupt(EventWorkflowFailed, [EventShipIdle]) = true, want false: override replaces the default set")
	}
}
