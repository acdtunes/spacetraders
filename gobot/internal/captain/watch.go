package watchkeeper

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// Watch subjects and predicates. A watch spec is "<subject>:<id>:<predicate>":
// ship:<SYMBOL>:arrival or container:<ID>:terminal. The subject selects the
// match field (ship symbol vs payload container_id); the predicate is the
// fixed keyword paired with that subject.
const (
	WatchSubjectShip      = "ship"
	WatchSubjectContainer = "container"

	WatchPredicateArrival  = "arrival"
	WatchPredicateTerminal = "terminal"
)

// DefaultWatchDeadline is the fallback deadline applied when `wake watch add`
// omits --by. Every evented wait is deadline'd at the sensing layer (sp-oyer
// design note), so a watch without an explicit deadline still auto-fires — and
// auto-disarms — rather than arming forever if its match event is lost. The
// CLI exposes --default-deadline to override it per RULINGS #5 (parametrize).
const DefaultWatchDeadline = 30 * time.Minute

// Watch is a captain-declared one-shot wake predicate (sp-oyer): fire a single
// wake the FIRST time a specific ship arrives or a specific container reaches a
// terminal state, OR once Deadline passes, whichever comes first — then
// auto-disarm. Watches are operational ephemera, independent of the standing
// WakePolicy: multiple coexist, and `wake set`'s replace semantics never touch
// them.
type Watch struct {
	Subject   string    `json:"subject"`   // WatchSubjectShip | WatchSubjectContainer
	ID        string    `json:"id"`        // ship symbol or container id
	Predicate string    `json:"predicate"` // WatchPredicateArrival | WatchPredicateTerminal
	Deadline  time.Time `json:"deadline"`  // wall-clock; deadline-fires if reached before a match
	ArmedAt   time.Time `json:"armed_at,omitempty"`
}

// Spec renders the watch back to its "<subject>:<id>:<predicate>" form for
// display and for tagging the fire mail.
func (w Watch) Spec() string {
	return fmt.Sprintf("%s:%s:%s", w.Subject, w.ID, w.Predicate)
}

// WatchPolicy is the captain-declared set of one-shot wake watches (sp-oyer).
// It is a SEPARATE store from WakePolicy/RegimePolicy — persisted as its own
// field of supervisorState, loaded fresh every Tick — so the standing wake
// policy is completely untouched by watch add/fire. An empty list means no
// watches are armed and the evaluator is a no-op.
type WatchPolicy struct {
	Watches []Watch `json:"watches,omitempty"`
}

// ParseWatchSpec parses "ship:<SYMBOL>:arrival" or "container:<ID>:terminal"
// into a Watch (Deadline/ArmedAt left unset — the caller stamps those). The id
// segment may itself contain colons: the subject is the first ":"-segment and
// the predicate is the last, so everything between is rejoined as the id. The
// predicate must be the one paired with the subject (ship→arrival,
// container→terminal) so a typo fails loudly at add time instead of arming a
// watch that can never match.
func ParseWatchSpec(spec string) (Watch, error) {
	parts := strings.Split(strings.TrimSpace(spec), ":")
	if len(parts) < 3 {
		return Watch{}, fmt.Errorf("watch spec %q must be \"ship:<SYMBOL>:arrival\" or \"container:<ID>:terminal\"", spec)
	}
	subject := parts[0]
	predicate := parts[len(parts)-1]
	id := strings.Join(parts[1:len(parts)-1], ":")
	if strings.TrimSpace(id) == "" {
		return Watch{}, fmt.Errorf("watch spec %q has an empty id", spec)
	}

	var want string
	switch subject {
	case WatchSubjectShip:
		want = WatchPredicateArrival
	case WatchSubjectContainer:
		want = WatchPredicateTerminal
	default:
		return Watch{}, fmt.Errorf("watch subject %q must be %q or %q", subject, WatchSubjectShip, WatchSubjectContainer)
	}
	if predicate != want {
		return Watch{}, fmt.Errorf("watch predicate for %s must be %q, got %q", subject, want, predicate)
	}
	return Watch{Subject: subject, ID: id, Predicate: predicate}, nil
}

// terminalWorkflowType reports whether t is a captain-outbox event that signals
// a ship's in-flight workflow (navigation/contract/park/jump) reached a
// terminal state — the honest "the ship arrived / the container is done"
// signal both watch predicates key off. container_runner's
// signalCompletionWithStatus emits exactly these two, carrying the ship symbol
// (Event.Ship) and a container_id in the JSON payload, so ship:arrival and
// container:terminal differ only in the match field, not the event type. A
// failed terminal state still fires the watch: the wait is over either way, and
// the fire mail tags the underlying event type so the captain sees which.
func terminalWorkflowType(t captain.EventType) bool {
	return t == captain.EventWorkflowFinished || t == captain.EventWorkflowFailed
}

// matches reports whether event e satisfies watch w.
func (w Watch) matches(e *captain.Event) bool {
	if !terminalWorkflowType(e.Type) {
		return false
	}
	switch w.Subject {
	case WatchSubjectShip:
		return e.Ship == w.ID
	case WatchSubjectContainer:
		return watchEventContainerID(e) == w.ID
	}
	return false
}

// firstMatch returns the first event in the batch that satisfies w, or nil.
func firstMatch(w Watch, events []*captain.Event) *captain.Event {
	for _, e := range events {
		if w.matches(e) {
			return e
		}
	}
	return nil
}

// watchEventContainerID pulls the container_id out of a terminal-workflow
// event's JSON payload (recorded by container_runner). An unparseable or
// absent payload yields "", which never equals a real container id.
func watchEventContainerID(e *captain.Event) string {
	var p struct {
		ContainerID string `json:"container_id"`
	}
	if json.Unmarshal([]byte(e.Payload), &p) != nil {
		return ""
	}
	return p.ContainerID
}

// Fire tags: which arm of a one-shot watch tripped. Recorded on the wake.watch
// event payload and rendered into the fire mail so acceptance (3) — a lost
// match event must still fire, tagged deadline-fired — is observable.
const (
	watchFireMatched  = "matched"
	watchFireDeadline = "deadline-fired"
)

// watchFirePayload is the JSON recorded on a wake.watch event: which watch
// fired and how. MatchedEventID/Type are populated only for a matched fire.
type watchFirePayload struct {
	Subject          string `json:"subject"`
	ID               string `json:"id"`
	Predicate        string `json:"predicate"`
	Tag              string `json:"tag"` // watchFireMatched | watchFireDeadline
	MatchedEventID   int64  `json:"matched_event_id,omitempty"`
	MatchedEventType string `json:"matched_event_type,omitempty"`
}

// describeWatchFire renders a wake.watch event's payload into a one-line
// human-readable descriptor for the fire mail (e.g.
// "ship:TORWIND-E:arrival matched (event 42 workflow.finished)" or
// "container:c-9:terminal deadline-fired"). An unparseable payload yields ""
// so composeWakeMail simply omits the annotation.
func describeWatchFire(payload string) string {
	var p watchFirePayload
	if json.Unmarshal([]byte(payload), &p) != nil {
		return ""
	}
	spec := fmt.Sprintf("%s:%s:%s", p.Subject, p.ID, p.Predicate)
	if p.Tag == watchFireMatched && p.MatchedEventID != 0 {
		return fmt.Sprintf("%s %s (event %d %s)", spec, p.Tag, p.MatchedEventID, p.MatchedEventType)
	}
	return fmt.Sprintf("%s %s", spec, p.Tag)
}

// evaluateWatches checks every armed one-shot watch (sp-oyer) against this
// tick's unprocessed events and the wall clock. A watch fires — recording a
// synthetic, always-interrupt wake.watch event tagged matched or
// deadline-fired — on the FIRST matching event OR once its deadline has passed,
// whichever comes first, then auto-disarms. Surviving watches stay armed.
// Returns true if any watch fired, so the caller re-reads the event batch to
// pick up the new wake.watch event(s) for this same tick's wake decision.
//
// This runs BEFORE the wake gate's type filtering: a watched ship's arrival is
// a deferred workflow.finished fleet-wide, but here — for a WATCHED ship — it
// elevates to an interrupt. Recording the marker (rather than forcing the wake
// inline) reuses the whole existing interrupt path: cooldown bypass (o8wi),
// delivery backoff bypass, mail, re-nudge, and ack.
//
// Ordering is record-then-save (at-least-once): the fire event is recorded
// before the surviving-watch list is persisted, so a crash in the tiny window
// between them re-arms the fired watch (a possible duplicate wake next tick)
// rather than losing the wake entirely — the failure mode the deadline exists
// to prevent. Deadlines are absolute wall-clock instants, so a restart
// evaluates them against the real now and cannot strand or double-fire beyond
// that one at-least-once window.
func (s *Supervisor) evaluateWatches(ctx context.Context, now time.Time, events []*captain.Event) bool {
	wp, err := LoadWatchPolicy(s.statePath)
	if err != nil {
		fmt.Printf("watchkeeper: watch policy unreadable, skipping watch evaluation: %v\n", err)
		return false
	}
	if len(wp.Watches) == 0 {
		return false
	}

	remaining := make([]Watch, 0, len(wp.Watches))
	fired := false
	for _, w := range wp.Watches {
		if hit := firstMatch(w, events); hit != nil {
			s.fireWatch(ctx, w, watchFireMatched, hit)
			fired = true
			continue
		}
		if !now.Before(w.Deadline) {
			s.fireWatch(ctx, w, watchFireDeadline, nil)
			fired = true
			continue
		}
		remaining = append(remaining, w)
	}
	if fired {
		if err := SaveWatchPolicy(s.statePath, WatchPolicy{Watches: remaining}); err != nil {
			fmt.Printf("watchkeeper: watch policy persist failed after fire: %v\n", err)
		}
	}
	return fired
}

// fireWatch records the synthetic wake.watch interrupt event for a fired watch.
// matched is the event that satisfied the watch (nil for a deadline fire). The
// event's Ship is set for a ship watch so it renders in the wake mail's SHIP
// column; the full watch identity and fire tag live in the payload.
func (s *Supervisor) fireWatch(ctx context.Context, w Watch, tag string, matched *captain.Event) {
	payload := watchFirePayload{Subject: w.Subject, ID: w.ID, Predicate: w.Predicate, Tag: tag}
	if matched != nil {
		payload.MatchedEventID = matched.ID
		payload.MatchedEventType = string(matched.Type)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte("{}")
	}
	ship := ""
	if w.Subject == WatchSubjectShip {
		ship = w.ID
	}
	if err := s.store.Record(ctx, &captain.Event{
		Type:     captain.EventWakeWatch,
		Ship:     ship,
		PlayerID: s.cfg.PlayerID,
		Payload:  string(raw),
	}); err != nil {
		fmt.Printf("watchkeeper: failed to record wake.watch fire (%s %s): %v\n", w.Spec(), tag, err)
	}
}
