package watchkeeper

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// standDownIfGagged re-reads the live gag switch fresh (never cached at
// construction) and reports whether the supervisor must stand down from all
// wake-eval actions this tick. Because it is read at the top of every Tick, a
// `captain gag on|off` toggle takes effect on the very next poll without a
// process restart — the same live-config idiom the wake/regime/watch policies
// use. An unreadable state file degrades to ungagged (fail-open): the gag is a
// soft operator pause, so a corrupt file must never silently suppress the whole
// fleet the way a fail-closed default would.
//
// It sits AFTER the DISABLED and universe-reset checks in Tick by design: the
// gag is subordinate to the hard kill-switch rail, so a universe reset still
// halts the fleet even while gagged. The edge transition (enter/exit) is logged
// and audited here, exactly once, before the boolean is returned.
func (s *Supervisor) standDownIfGagged(ctx context.Context) bool {
	policy, err := LoadGagPolicy(s.statePath)
	if err != nil {
		fmt.Printf("watchkeeper: gag policy unreadable, treating as ungagged: %v\n", err)
		policy = GagPolicy{}
	}
	s.noteGagEdge(ctx, policy)
	return policy.Gagged
}

// noteGagEdge emits the operator-visible log line and the audit event ONCE per
// gag transition (sp-6g96 edge-triggered doctrine): a supervisor that stays
// gagged for hours must not re-log every 30s poll. s.gagged is the in-memory
// previous state; it starts false, so a process that boots into an already-
// gagged config announces the stand-down on its first tick — desirable, a fresh
// process SHOULD re-state that it is standing down.
func (s *Supervisor) noteGagEdge(ctx context.Context, policy GagPolicy) {
	if policy.Gagged == s.gagged {
		return
	}
	s.gagged = policy.Gagged
	if policy.Gagged {
		fmt.Printf("watchkeeper: GAG ENGAGED — standing down from wake-eval (no session spawn, no corrective action); process, heartbeat, and universe-reset rail stay live. reason: %s\n",
			gagReasonOrNone(policy.GagReason))
		s.recordGagEvent(ctx, "gagged", policy.GagReason)
		return
	}
	fmt.Printf("watchkeeper: GAG CLEARED — resuming normal wake-eval\n")
	s.recordGagEvent(ctx, "ungagged", policy.GagReason)
}

// recordGagEvent writes the deferred captain.supervisor_gagged audit event so
// the captain and Admiral can see, on the next wake, that the supervisor stood
// down (or resumed) and why. Best-effort: a store error must not stop the
// supervisor from standing down — the log line above already carries the signal.
func (s *Supervisor) recordGagEvent(ctx context.Context, state, reason string) {
	payload, err := json.Marshal(struct {
		State  string `json:"state"`
		Reason string `json:"reason,omitempty"`
	}{State: state, Reason: reason})
	if err != nil {
		payload = []byte("{}")
	}
	if err := s.store.Record(ctx, &captain.Event{
		Type:     captain.EventSupervisorGagged,
		PlayerID: s.cfg.PlayerID,
		Payload:  string(payload),
	}); err != nil {
		fmt.Printf("watchkeeper: failed to record supervisor-gag event (%s): %v\n", state, err)
	}
}

func gagReasonOrNone(reason string) string {
	if reason == "" {
		return "(none given)"
	}
	return reason
}
