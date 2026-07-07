package captain

import "testing"

// TestDefaultInterruptTypesIsExactlyTheApprovedSet locks the default
// interrupt set to the six event types the design approved: everything else
// (workflow.finished, contract.completed, credits.threshold, ship.idle) is
// deferred and rides the next wake's batch instead of forcing one.
func TestDefaultInterruptTypesIsExactlyTheApprovedSet(t *testing.T) {
	want := map[EventType]bool{
		EventWorkflowFailed:   true,
		EventContainerCrashed: true,
		EventHeartbeatLost:    true,
		EventContractFailed:   true,
		EventIncomeStalled:    true,
		EventStreamDown:       true,
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
		{"container.crashed interrupts", EventContainerCrashed, true},
		{"container.heartbeat_lost interrupts", EventHeartbeatLost, true},
		{"contract.failed interrupts", EventContractFailed, true},
		{"income.stalled interrupts", EventIncomeStalled, true},
		{"stream.down interrupts", EventStreamDown, true},
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
