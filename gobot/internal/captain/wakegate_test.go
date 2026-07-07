package captainsup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

func evt(typ captain.EventType) *captain.Event {
	return &captain.Event{Type: typ}
}

func intPtr(n int) *int              { return &n }
func timePtr(t time.Time) *time.Time { return &t }

// TestEvaluateWakeGate is the pure-function decision table for the sp-sk68
// wake model: evaluateWakeGate takes now/events/policy/credits/cadence as
// explicit inputs and returns whether to wake, independent of the database,
// event store, or city gateway. It never decides HOW to wake (bridgeWake,
// unchanged, still owns delivery) — only whether this tick's unprocessed
// batch, cadence, or credits cross an interrupt threshold.
func TestEvaluateWakeGate(t *testing.T) {
	base := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		in   wakeGateInput
		want bool
	}{
		{
			name: "deferred-only event before cadence due does not wake",
			in: wakeGateInput{
				Now:              base,
				Events:           []*captain.Event{evt(captain.EventShipIdle)},
				LastSession:      base.Add(-10 * time.Minute),
				HeartbeatMinutes: 45,
			},
			want: false,
		},
		{
			name: "deferred event plus one interrupt event wakes",
			in: wakeGateInput{
				Now:              base,
				Events:           []*captain.Event{evt(captain.EventShipIdle), evt(captain.EventWorkflowFailed)},
				LastSession:      base.Add(-10 * time.Minute),
				HeartbeatMinutes: 45,
			},
			want: true,
		},
		{
			name: "no events and cadence not yet due does not wake",
			in: wakeGateInput{
				Now:              base,
				LastSession:      base.Add(-10 * time.Minute),
				HeartbeatMinutes: 45,
			},
			want: false,
		},
		{
			name: "now past the default heartbeat cadence wakes",
			in: wakeGateInput{
				Now:              base,
				LastSession:      base.Add(-46 * time.Minute),
				HeartbeatMinutes: 45,
			},
			want: true,
		},
		{
			name: "credits at or above CreditsAbove wakes",
			in: wakeGateInput{
				Now:              base,
				LastSession:      base.Add(-10 * time.Minute),
				HeartbeatMinutes: 45,
				Credits:          500000,
				Policy:           WakePolicy{CreditsAbove: intPtr(500000)},
			},
			want: true,
		},
		{
			name: "credits below CreditsAbove threshold does not wake",
			in: wakeGateInput{
				Now:              base,
				LastSession:      base.Add(-10 * time.Minute),
				HeartbeatMinutes: 45,
				Credits:          499999,
				Policy:           WakePolicy{CreditsAbove: intPtr(500000)},
			},
			want: false,
		},
		{
			name: "credits at or below CreditsBelow wakes",
			in: wakeGateInput{
				Now:              base,
				LastSession:      base.Add(-10 * time.Minute),
				HeartbeatMinutes: 45,
				Credits:          900,
				Policy:           WakePolicy{CreditsBelow: intPtr(1000)},
			},
			want: true,
		},
		{
			name: "credits above CreditsBelow threshold does not wake",
			in: wakeGateInput{
				Now:              base,
				LastSession:      base.Add(-10 * time.Minute),
				HeartbeatMinutes: 45,
				Credits:          1500,
				Policy:           WakePolicy{CreditsBelow: intPtr(1000)},
			},
			want: false,
		},
		{
			name: "captain-declared NextWakeAt overrides the default cadence",
			in: wakeGateInput{
				Now:              base,
				LastSession:      base.Add(-10 * time.Minute), // default cadence would not be due for 35m more
				HeartbeatMinutes: 45,
				Policy:           WakePolicy{NextWakeAt: timePtr(base)}, // declared due exactly now
			},
			want: true,
		},
		{
			name: "captain-declared NextWakeAt not yet due does not wake",
			in: wakeGateInput{
				Now:              base,
				LastSession:      base.Add(-10 * time.Minute),
				HeartbeatMinutes: 45,
				Policy:           WakePolicy{NextWakeAt: timePtr(base.Add(time.Minute))},
			},
			want: false,
		},
		{
			name: "NextWakeAt beyond the never-wake ceiling does not wake before the cap",
			in: wakeGateInput{
				Now:                    base.Add(179 * time.Minute),
				LastSession:            base,
				HeartbeatMinutes:       45,
				MaxWakeIntervalMinutes: 180,
				Policy:                 WakePolicy{NextWakeAt: timePtr(base.Add(10 * 24 * time.Hour))},
			},
			want: false,
		},
		{
			name: "NextWakeAt beyond the never-wake ceiling wakes once the capped time is reached",
			in: wakeGateInput{
				Now:                    base.Add(180 * time.Minute),
				LastSession:            base,
				HeartbeatMinutes:       45,
				MaxWakeIntervalMinutes: 180,
				Policy:                 WakePolicy{NextWakeAt: timePtr(base.Add(10 * 24 * time.Hour))},
			},
			want: true,
		},
		{
			name: "MaxWakeIntervalMinutes unset falls back to the 180-minute default ceiling",
			in: wakeGateInput{
				Now:              base.Add(180 * time.Minute),
				LastSession:      base,
				HeartbeatMinutes: 45,
				Policy:           WakePolicy{NextWakeAt: timePtr(base.Add(10 * 24 * time.Hour))},
				// MaxWakeIntervalMinutes intentionally left zero.
			},
			want: true,
		},
		{
			name: "undeclared policy: default-set interrupt event still wakes",
			in: wakeGateInput{
				Now:              base,
				Events:           []*captain.Event{evt(captain.EventContainerCrashed)},
				LastSession:      base.Add(-time.Minute),
				HeartbeatMinutes: 45,
			},
			want: true,
		},
		{
			name: "undeclared policy: 45-minute heartbeat cadence applies",
			in: wakeGateInput{
				Now:              base,
				LastSession:      base.Add(-45 * time.Minute),
				HeartbeatMinutes: 45,
			},
			want: true,
		},
		{
			name: "captain-declared InterruptTypes override: default interrupt type no longer wakes alone",
			in: wakeGateInput{
				Now:              base,
				Events:           []*captain.Event{evt(captain.EventWorkflowFailed)},
				LastSession:      base.Add(-time.Minute),
				HeartbeatMinutes: 45,
				Policy:           WakePolicy{InterruptTypes: []string{"ship.idle"}},
			},
			want: false,
		},
		{
			name: "captain-declared InterruptTypes override: newly-declared type wakes",
			in: wakeGateInput{
				Now:              base,
				Events:           []*captain.Event{evt(captain.EventShipIdle)},
				LastSession:      base.Add(-time.Minute),
				HeartbeatMinutes: 45,
				Policy:           WakePolicy{InterruptTypes: []string{"ship.idle"}},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evaluateWakeGate(tt.in)
			require.Equal(t, tt.want, got.ShouldWake, "reason: %s", got.Reason)
		})
	}
}
