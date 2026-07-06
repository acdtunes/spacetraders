package captainsup

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
	SpawnSession(ctx context.Context, agent, alias string) error
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
// clock, exactly as a legacy session start did.
func (s *Supervisor) recordWake(now time.Time) {
	s.sessionStarts = append(s.sessionStarts, now)
	s.lastSession = now
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
