package watchkeeper

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

// cityGateway is the driven port the bridge wake path uses; *CityGateway
// satisfies it. Kept narrow so tests can substitute a recording fake.
type cityGateway interface {
	SendMail(ctx context.Context, to, subject, body string) error
	Nudge(ctx context.Context, alias, text string) error
	SessionAlive(ctx context.Context, alias string) (bool, error)
}

// SetCity wires the city adapters for bridge mode without changing the
// constructor signature legacy tests depend on.
func (s *Supervisor) SetCity(gw *CityGateway, bc *BeadsClient) {
	s.gw = gw
	s.bc = bc
}

// bridgeWake replaces the legacy prompt+runner session with visible city
// signals: one event mail + nudge, re-nudges for unacked events, and an
// Admiral escalation when the captain stays unresponsive.
func (s *Supervisor) bridgeWake(ctx context.Context, now time.Time, events []*captain.Event) (bool, error) {
	if s.renudges == nil {
		s.renudges = map[int64]int{}
	}
	if s.escalated == nil {
		s.escalated = map[int64]bool{}
	}
	s.pruneWakeState(events)

	agent := s.cfg.CaptainAgent
	if len(events) == 0 {
		if err := s.gw.Nudge(ctx, agent, "heartbeat — no events"); err != nil {
			return true, err
		}
		s.recordWake(now)
		return true, nil
	}

	if s.hasUnmailedEvents(events) {
		return s.firstWake(ctx, now, agent, events)
	}

	ackTimeout := time.Duration(s.cfg.AckTimeoutMinutes) * time.Minute
	if now.Sub(s.lastSession) < ackTimeout {
		return false, nil
	}
	return s.renudge(ctx, now, events)
}

func (s *Supervisor) firstWake(ctx context.Context, now time.Time, agent string, events []*captain.Event) (bool, error) {
	subject, body := composeWakeMail(s.cfg.PlayerID, events, now)
	if err := s.gw.SendMail(ctx, agent, subject, body); err != nil {
		return true, err
	}
	nudge := fmt.Sprintf("wake: %d events + heartbeat — check mail", len(events))
	if err := s.gw.Nudge(ctx, agent, nudge); err != nil {
		return true, err
	}
	for _, e := range events {
		if _, ok := s.renudges[e.ID]; !ok {
			s.renudges[e.ID] = 0
		}
	}
	s.recordWake(now)
	return true, nil
}

func (s *Supervisor) renudge(ctx context.Context, now time.Time, events []*captain.Event) (bool, error) {
	needNudge := false
	for _, e := range events {
		if s.escalated[e.ID] {
			continue
		}
		s.renudges[e.ID]++
		if s.renudges[e.ID] > s.cfg.EscalateAfterRenudges {
			if err := s.escalate(ctx, e); err != nil {
				return true, err
			}
			continue
		}
		needNudge = true
	}
	if needNudge {
		nudge := fmt.Sprintf("wake: %d events still unacked — check mail", len(events))
		if err := s.gw.Nudge(ctx, s.cfg.CaptainAgent, nudge); err != nil {
			return true, err
		}
	}
	s.recordWake(now)
	return true, nil
}

func (s *Supervisor) escalate(ctx context.Context, e *captain.Event) error {
	body := fmt.Sprintf("event %d (%s) unacked after %d re-nudges", e.ID, e.Type, s.cfg.EscalateAfterRenudges)
	if err := s.gw.SendMail(ctx, s.cfg.AdmiralAlias, "captain unresponsive", body); err != nil {
		return err
	}
	s.escalated[e.ID] = true
	return nil
}

// pruneWakeState drops bookkeeping for events the captain has since acked, so
// re-arming after an ack starts from a clean slate.
func (s *Supervisor) pruneWakeState(events []*captain.Event) {
	live := make(map[int64]bool, len(events))
	for _, e := range events {
		live[e.ID] = true
	}
	for id := range s.renudges {
		if !live[id] {
			delete(s.renudges, id)
		}
	}
	for id := range s.escalated {
		if !live[id] {
			delete(s.escalated, id)
		}
	}
}

func (s *Supervisor) hasUnmailedEvents(events []*captain.Event) bool {
	for _, e := range events {
		if _, ok := s.renudges[e.ID]; !ok {
			return true
		}
	}
	return false
}

// recordWake counts the wake against the hourly cap and resets the heartbeat
// clock, exactly as a legacy session start did. It also persists scheduling
// state so a process restart picks up where this one left off instead of
// treating the heartbeat/renudge/escalation clocks as freshly zeroed.
//
// recordWake is the ONLY success signal for wake delivery, so it also clears
// the delivery-failure backoff (sp-sk68 D1): a delivered wake proves the
// channel is healthy again.
func (s *Supervisor) recordWake(now time.Time) {
	s.sessionStarts = append(s.sessionStarts, now)
	s.lastSession = now
	s.deliveryFailures = 0
	s.firstDeliveryFailure = time.Time{}
	s.lastDeliveryAttempt = time.Time{}
	s.lastAttemptInterrupts = nil
	s.lastAttemptCreditsAbove = false
	s.lastAttemptCreditsBelow = false
	s.saveState()
}

// deliveryThrottled reports whether this tick's wake attempt should be skipped
// because a prior delivery failed and the exponential backoff window has not
// yet elapsed (sp-sk68 D1). It never throttles when there have been zero
// consecutive failures (the healthy path), and it always lets a brand-new
// interrupt-class event through so interrupt delivery is never regressed.
func (s *Supervisor) deliveryThrottled(now time.Time, events []*captain.Event, policy WakePolicy) bool {
	if s.deliveryFailures == 0 {
		return false
	}
	if s.hasNewInterrupt(events, policy) {
		return false
	}
	// A CreditsAbove/Below bound newly satisfied since the last attempt is a
	// genuine edge-triggered crossing (not a standing level), so it bypasses the
	// backoff without letting a permanently-satisfied threshold hammer a dead
	// channel every tick (sp-sk68 D4).
	if s.creditsGateNewlyCrossed(policy) {
		return false
	}
	delay := backoffDelay(s.pollInterval(), s.deliveryFailures)
	// An interrupt-class event that has never been successfully mailed inherits
	// the same deliveryFailures counter that failed heartbeat nudges accumulate;
	// without a cap its one bypassed attempt would be followed by up to the full
	// 15m heartbeat backoff before the next retry (sp-sk68 D1 follow-up). Keep an
	// undelivered interrupt prompt at 2x poll; pure-heartbeat and
	// already-delivered (renudge-only) batches keep the full ceiling.
	if s.hasUnmailedInterrupt(events, policy) {
		if capped := 2 * s.pollInterval(); delay > capped {
			delay = capped
		}
	}
	return now.Before(s.lastDeliveryAttempt.Add(delay))
}

// hasNewInterrupt reports whether the batch contains an interrupt-class event
// (classified under the current policy) whose id was not present at the last
// delivery attempt. Such an event must bypass the backoff. Comparing ids is
// policy-independent, so a mid-outage interrupt-type redeclaration cannot mask
// a genuinely new event.
func (s *Supervisor) hasNewInterrupt(events []*captain.Event, policy WakePolicy) bool {
	interrupts, _ := partitionEvents(events, policy.InterruptTypes)
	for _, e := range interrupts {
		if !s.lastAttemptInterrupts[e.ID] {
			return true
		}
	}
	return false
}

// hasUnmailedInterrupt reports whether the batch contains an interrupt-class
// event that has not yet been successfully mailed. s.renudges gains an entry
// for an event only after firstWake's mail+nudge both succeed, so an interrupt
// with no renudges entry has never reached the captain and must not sit out the
// full delivery backoff (sp-sk68 D1 follow-up).
func (s *Supervisor) hasUnmailedInterrupt(events []*captain.Event, policy WakePolicy) bool {
	interrupts, _ := partitionEvents(events, policy.InterruptTypes)
	for _, e := range interrupts {
		if _, mailed := s.renudges[e.ID]; !mailed {
			return true
		}
	}
	return false
}

// creditsGateNewlyCrossed reports whether a CreditsAbove/Below wake condition is
// satisfied now but was NOT at the last delivery attempt — a true edge-triggered
// crossing. A still-satisfied (level) bound returns false so a standing
// threshold cannot defeat the delivery backoff on every tick (sp-sk68 D4). The
// snapshot is refreshed on every actual attempt (rememberAttempt), so whenever
// deliveryFailures>0 it reflects the credit state at the most recent attempt.
func (s *Supervisor) creditsGateNewlyCrossed(policy WakePolicy) bool {
	aboveNow := policy.CreditsAbove != nil && s.lastCredits >= *policy.CreditsAbove
	belowNow := policy.CreditsBelow != nil && s.lastCredits <= *policy.CreditsBelow
	return (aboveNow && !s.lastAttemptCreditsAbove) || (belowNow && !s.lastAttemptCreditsBelow)
}

// rememberAttempt snapshots the interrupt-event ids present at this delivery
// attempt, so the NEXT tick can tell a genuinely new interrupt (which bypasses
// the backoff) from the same unchanged interrupt still failing to deliver.
func (s *Supervisor) rememberAttempt(events []*captain.Event, policy WakePolicy) {
	interrupts, _ := partitionEvents(events, policy.InterruptTypes)
	set := make(map[int64]bool, len(interrupts))
	for _, e := range interrupts {
		set[e.ID] = true
	}
	s.lastAttemptInterrupts = set
	s.lastAttemptCreditsAbove = policy.CreditsAbove != nil && s.lastCredits >= *policy.CreditsAbove
	s.lastAttemptCreditsBelow = policy.CreditsBelow != nil && s.lastCredits <= *policy.CreditsBelow
}

// noteDeliveryFailure records one failed wake delivery: it advances the
// consecutive-failure counter, stamps the attempt/first-failure times that
// drive the backoff, and emits ONE distinct, grep-able line per failed attempt
// so a persistent outage is unmistakable and separable from generic tick
// errors (sp-sk68 D1). It deliberately does NOT touch last_session — a failed
// wake is not a session.
func (s *Supervisor) noteDeliveryFailure(now time.Time, err error) {
	if s.deliveryFailures == 0 {
		s.firstDeliveryFailure = now
	}
	s.deliveryFailures++
	s.lastDeliveryAttempt = now
	fmt.Printf("watchkeeper: WAKE DELIVERY FAILING (%d consecutive since %s): %v\n",
		s.deliveryFailures, s.firstDeliveryFailure.Format(time.RFC3339), err)
}

func (s *Supervisor) pollInterval() time.Duration {
	return time.Duration(s.cfg.PollIntervalSeconds) * time.Second
}

// backoffDelay is the wait before the next delivery retry: pollInterval*2^n
// capped at 15 minutes, where n is the count of consecutive failures so far.
func backoffDelay(poll time.Duration, failures int) time.Duration {
	const ceiling = 15 * time.Minute
	if poll <= 0 {
		poll = 30 * time.Second
	}
	d := poll
	for i := 0; i < failures; i++ {
		d *= 2
		if d >= ceiling {
			return ceiling
		}
	}
	if d > ceiling {
		return ceiling
	}
	return d
}

func composeWakeMail(playerID int, events []*captain.Event, now time.Time) (subject, body string) {
	subject = fmt.Sprintf("wake: %d events", len(events))
	var b strings.Builder
	ids := make([]string, 0, len(events))
	for _, e := range events {
		age := now.Sub(e.CreatedAt).Round(time.Minute)
		fmt.Fprintf(&b, "%d\t%s\t%s\t%s\n", e.ID, e.Type, e.Ship, age)
		ids = append(ids, strconv.FormatInt(e.ID, 10))
	}
	fmt.Fprintf(&b, "\nack: spacetraders captain events ack --player-id %d --ids %s\n",
		playerID, strings.Join(ids, ","))
	return subject, b.String()
}
