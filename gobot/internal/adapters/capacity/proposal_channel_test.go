package capacity_test

// Unit tests for the capacity tiered-autonomy PROPOSAL CHANNEL (bead st-0h8,
// epic st-7zk).
//
// The ProposalChannel is the REAL domainCapacity.ProposalChannel: it files a
// tier-4 capital proposal for human approval as a DEFERRED captain event
// carrying the full ROI evidence — the capacity analogue of the scout-post
// proposal ("a PROPOSAL only; the captain decides and declares"). It reuses the
// existing captain-outbox seam (HasSince dedup + Record) and spends NOTHING.
// Every test drives Submit (the driving port) and asserts at the captain-outbox
// boundary (the fake store) — the only double, at the port boundary.
//
// Test budget: 3 distinct behaviors × 2 = 6 max. 3 written (one parametrized):
//  1. files the proposal — a captain nudge carrying the ROI evidence
//  2. idempotent on the stable Proposal.ID — a re-proposed gap never duplicates
//  3. fails CLOSED on a malformed proposal (zero player / non-capital / bad verb)

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	capacityAdapters "github.com/andrescamacho/spacetraders-go/internal/adapters/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeCapexEventStore is the captain-outbox port double. It behaves like the
// real GormCaptainEventRepository: Record stamps CreatedAt off the shared clock
// and appends; HasSince answers from what was actually recorded (not a canned
// return) — so the idempotency test exercises the real dedup arithmetic, never
// a stubbed yes/no.
type fakeCapexEventStore struct {
	clock       *shared.MockClock
	recorded    []*captain.Event
	recordErr   error
	hasSinceErr error
}

func (f *fakeCapexEventStore) HasSince(_ context.Context, playerID int, t captain.EventType, ship string, since time.Time) (bool, error) {
	if f.hasSinceErr != nil {
		return false, f.hasSinceErr
	}
	for _, e := range f.recorded {
		if e.PlayerID == playerID && e.Type == t && e.Ship == ship && e.CreatedAt.After(since) {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeCapexEventStore) Record(_ context.Context, e *captain.Event) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	if f.clock != nil {
		e.CreatedAt = f.clock.Now()
	}
	f.recorded = append(f.recorded, e)
	return nil
}

func validCapexProposal() capacity.Proposal {
	return capacity.Proposal{
		ID:       "capex-X1-HUB-B-buy_hull",
		PlayerID: 7,
		Action: capacity.Action{
			Tier:                 capacity.TierCapital,
			Verb:                 capacity.VerbBuyHull,
			HubSymbol:            "X1-HUB-B",
			GapKind:              capacity.GapWorkerShort,
			EstimatedCostCredits: 120000,
			HullDelta:            1,
			ProjectedPerHullCrHr: 3100,
		},
		Evidence: capacity.ROIEvidence{
			CostCredits:            120000,
			ProjectedGainPerHour:   4200,
			PaybackHorizon:         24 * time.Hour,
			ProjectedPaybackHours:  28.6,
			FleetPerHullCrHrBefore: 3000,
			FleetPerHullCrHrAfter:  3100,
			Narrative:              "buy a worker hull for X1-HUB-B: +4200 cr/hr, payback ~28.6h, fleet per-hull 3000->3100",
		},
		Status: capacity.ProposalPending,
	}
}

// Behavior: Submit files the capital proposal as a deferred captain nudge that
// carries the full ROI evidence a human approves from — attributed to the
// player and keyed on the stable Proposal.ID.
func TestCapexProposalChannel_SubmitFilesCaptainNudgeWithROIEvidence(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)}
	store := &fakeCapexEventStore{clock: clock}
	channel := capacityAdapters.NewProposalChannel(store, clock)

	err := channel.Submit(context.Background(), validCapexProposal())
	require.NoError(t, err)

	require.Len(t, store.recorded, 1, "Submit must file exactly one proposal nudge")
	event := store.recorded[0]
	require.Equal(t, captain.EventCapacityCapexProposal, event.Type)
	require.Equal(t, 7, event.PlayerID, "the nudge is attributed to the reconciling player")
	require.Equal(t, "capex-X1-HUB-B-buy_hull", event.Ship,
		"the stable Proposal.ID keys dedup + attribution (the scout-post Ship-overload idiom)")

	// The payload carries the ROI evidence — decode it and assert the numbers a
	// human judges from actually reached the captain (not just 'some payload').
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(event.Payload), &payload))
	require.Equal(t, "buy_hull", payload["verb"])
	require.Equal(t, "X1-HUB-B", payload["hub"])
	require.EqualValues(t, 120000, payload["estimated_cost_credits"])
	require.EqualValues(t, 4200, payload["projected_gain_per_hour"])
	require.EqualValues(t, 28.6, payload["projected_payback_hours"])
	require.EqualValues(t, 3000, payload["fleet_per_hull_cr_hr_before"])
	require.EqualValues(t, 3100, payload["fleet_per_hull_cr_hr_after"])
	require.Contains(t, payload["narrative"], "payback ~28.6h",
		"the human-readable narrative must survive to the nudge")
}

// Behavior: Submit is idempotent on the stable Proposal.ID — the same gap
// re-proposed on a later tick (the governor re-mints it every ~300s) must NOT
// spawn a duplicate proposal; the open one stands.
func TestCapexProposalChannel_SubmitIsIdempotentOnProposalID(t *testing.T) {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)}
	store := &fakeCapexEventStore{clock: clock}
	channel := capacityAdapters.NewProposalChannel(store, clock)
	proposal := validCapexProposal()

	require.NoError(t, channel.Submit(context.Background(), proposal))
	// A few ticks later, same open gap, same stable ID (evidence may drift).
	clock.CurrentTime = clock.CurrentTime.Add(5 * time.Minute)
	require.NoError(t, channel.Submit(context.Background(), proposal))

	require.Len(t, store.recorded, 1,
		"the same gap re-proposed within the cooldown must keep the open proposal, never file a duplicate nudge")
}

// Behavior: Submit fails CLOSED on a malformed proposal — treasury-moving
// garbage (no player, a non-capital action, an unknown capital verb) must never
// reach a human's approve button, and nothing is filed.
func TestCapexProposalChannel_SubmitFailsClosedOnMalformedProposal(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(p *capacity.Proposal)
		wantSub string
	}{
		{
			name:    "zero PlayerID (the loop stamps it before Submit, so zero is a wiring bug)",
			mutate:  func(p *capacity.Proposal) { p.PlayerID = 0 },
			wantSub: "player",
		},
		{
			name: "non-capital action masquerading as a proposal",
			mutate: func(p *capacity.Proposal) {
				p.Action.Tier = capacity.TierReuseIdle
				p.Action.Verb = capacity.VerbReassignHull
			},
			wantSub: "capital",
		},
		{
			name:    "unknown capital verb",
			mutate:  func(p *capacity.Proposal) { p.Action.Verb = capacity.ActionVerb("detonate_treasury") },
			wantSub: "verb",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)}
			store := &fakeCapexEventStore{clock: clock}
			channel := capacityAdapters.NewProposalChannel(store, clock)
			proposal := validCapexProposal()
			tc.mutate(&proposal)

			err := channel.Submit(context.Background(), proposal)

			require.Error(t, err)
			require.Contains(t, strings.ToLower(err.Error()), tc.wantSub)
			require.Empty(t, store.recorded, "a malformed capital proposal must never be filed for approval")
		})
	}
}
