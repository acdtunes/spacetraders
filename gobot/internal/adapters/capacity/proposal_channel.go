package capacity

// The capacity tiered-autonomy PROPOSAL CHANNEL: the
// REAL domainCapacity.ProposalChannel that replaces the fail-loud NoOp in the
// daemon wiring. When the governor mints a tier-4 capital proposal (autobuy a
// hull / stand up a cluster — a treasury-moving add that NEVER auto-executes
// under v1 tiered autonomy), the loop hands it here to file for human approval.
//
// It files the proposal as a DEFERRED captain event carrying the full ROI
// evidence — the capacity analogue of EventScoutPostProposal ("a PROPOSAL only;
// the captain decides and declares"). This REUSES the existing captain-outbox
// seam (HasSince dedup + Record, the exact idiom sitingScoutDemandEmitter uses);
// it invents no new captain plumbing. Critically, filing a proposal SPENDS
// NOTHING: it moves no credits, buys no hull. Capital executes only later, via
// the approval-execution path (proposal_approval_execution.go), past the
// invariant-4 capital gate — so wiring this in is deploy-inert.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	domainCapacity "github.com/andrescamacho/spacetraders-go/internal/domain/capacity"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// defaultProposalNudgeCooldown bounds how often ONE open capital gap re-nudges
// the captain. The governor re-mints a proposal for an unclosed gap every
// reconcile tick (~300s), but a capital add is a strategic, hours-scale
// decision — so the same gap (keyed on the stable Proposal.ID) files ONE nudge
// per cooldown, not one per tick (event-spam doctrine).
const defaultProposalNudgeCooldown = time.Hour

// capexProposalEventStore is the captain-outbox seam the channel files on: the
// SAME deferred-event mechanism EventScoutPostProposal rides — HasSince to
// dedup, Record to file. GormCaptainEventRepository satisfies it verbatim.
type capexProposalEventStore interface {
	HasSince(ctx context.Context, playerID int, t captain.EventType, ship string, since time.Time) (bool, error)
	Record(ctx context.Context, e *captain.Event) error
}

// ProposalChannel is the real domainCapacity.ProposalChannel.
type ProposalChannel struct {
	store    capexProposalEventStore
	clock    shared.Clock
	cooldown time.Duration
}

var _ domainCapacity.ProposalChannel = (*ProposalChannel)(nil)

// NewProposalChannel wires the channel to the captain outbox. A nil clock
// defaults to the production real clock.
func NewProposalChannel(store capexProposalEventStore, clock shared.Clock) *ProposalChannel {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &ProposalChannel{store: store, clock: clock, cooldown: defaultProposalNudgeCooldown}
}

// Submit files (or, for an already-open gap, leaves standing) a capital
// proposal for approval.
//
// It fails CLOSED on a malformed proposal FIRST — a zero PlayerID (the loop
// stamps the reconciling player before Submit, so zero is a wiring bug) or an
// action that is not a canonical tier-4 capital verb — so treasury-moving
// garbage never reaches a human's approve button.
//
// It is idempotent on the stable Proposal.ID: if a nudge for this gap was filed
// within the cooldown, the open proposal stands and Submit is a no-op — the
// same gap re-proposed every tick never spawns a duplicate.
func (c *ProposalChannel) Submit(ctx context.Context, proposal domainCapacity.Proposal) error {
	if err := validateCapitalProposal(proposal); err != nil {
		return err
	}
	since := c.clock.Now().Add(-c.cooldown)
	recent, err := c.store.HasSince(ctx, proposal.PlayerID, captain.EventCapacityCapexProposal, proposal.ID, since)
	if err != nil {
		return fmt.Errorf("capex proposal %s: dedup check: %w", proposal.ID, err)
	}
	if recent {
		return nil // an open proposal for this gap already stands — no duplicate nudge
	}
	return c.store.Record(ctx, buildCapexProposalEvent(proposal))
}

// validateCapitalProposal is the fail-closed money-boundary guard: a proposal
// MUST carry a real player and a canonical capital (tier-4) action. Anything
// else is a wiring or governor bug and is refused, never filed.
func validateCapitalProposal(p domainCapacity.Proposal) error {
	if p.PlayerID == 0 {
		return fmt.Errorf("capex proposal %s: refusing to file with zero PlayerID (the loop stamps the reconciling player before Submit)", p.ID)
	}
	if !p.Action.Tier.RequiresApproval() {
		return fmt.Errorf("capex proposal %s: refusing to file a non-capital action (tier %s) — only tier-4 capital requires approval", p.ID, p.Action.Tier)
	}
	switch p.Action.Verb {
	case domainCapacity.VerbAddCluster, domainCapacity.VerbBuyHull:
		return nil
	default:
		return fmt.Errorf("capex proposal %s: refusing to file unknown capital verb %q (fail-closed)", p.ID, p.Action.Verb)
	}
}

// buildCapexProposalEvent renders the proposal as its deferred captain event.
// Ship carries the stable Proposal.ID (the dedup + attribution key); the JSON
// payload carries the full ROI evidence so the approver judges from evidence.
func buildCapexProposalEvent(p domainCapacity.Proposal) *captain.Event {
	payload, _ := json.Marshal(map[string]any{
		"proposal_id":                 p.ID,
		"verb":                        string(p.Action.Verb),
		"hub":                         p.Action.HubSymbol,
		"ship":                        p.Action.ShipSymbol,
		"good":                        p.Action.Good,
		"gap_kind":                    string(p.Action.GapKind),
		"hull_delta":                  p.Action.HullDelta,
		"estimated_cost_credits":      p.Action.EstimatedCostCredits,
		"cost_credits":                p.Evidence.CostCredits,
		"projected_gain_per_hour":     p.Evidence.ProjectedGainPerHour,
		"payback_horizon":             p.Evidence.PaybackHorizon.String(),
		"projected_payback_hours":     p.Evidence.ProjectedPaybackHours,
		"fleet_per_hull_cr_hr_before": p.Evidence.FleetPerHullCrHrBefore,
		"fleet_per_hull_cr_hr_after":  p.Evidence.FleetPerHullCrHrAfter,
		"narrative":                   p.Evidence.Narrative,
	})
	return &captain.Event{
		Type:     captain.EventCapacityCapexProposal,
		Ship:     p.ID,
		PlayerID: p.PlayerID,
		Payload:  string(payload),
	}
}
