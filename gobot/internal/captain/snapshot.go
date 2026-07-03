package captainsup

import (
	"os"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

const (
	logTailBytes    = 8 * 1024
	maxDueDecisions = 20
)

// ComposeSnapshot builds the full prompt for a strategy session: fleet state,
// KPIs, pending events, due decisions, and memory. The captain spends its
// turns deciding, not fetching (spec: Component 3).
func ComposeSnapshot(ctx context.Context, db *gorm.DB, ws Workspace, playerID int, events []*captain.Event, now time.Time) (string, error) {
	var b strings.Builder

	b.WriteString("# Fleet situation report\n")
	b.WriteString("Generated: " + now.UTC().Format(time.RFC3339) + "\n\n")

	// Pending events
	b.WriteString("## Pending events\n")
	if len(events) == 0 {
		b.WriteString("(none — heartbeat review)\n")
	}
	for _, e := range events {
		b.WriteString(fmt.Sprintf("- [%d] %s ship=%s at %s payload=%s\n",
			e.ID, e.Type, e.Ship, e.CreatedAt.UTC().Format(time.RFC3339), e.Payload))
	}

	// Fleet
	var ships []persistence.ShipModel
	if err := db.WithContext(ctx).Where("player_id = ?", playerID).Find(&ships).Error; err != nil {
		return "", err
	}
	b.WriteString("\n## Fleet\n")
	for _, s := range ships {
		b.WriteString(fmt.Sprintf("- %s: %s at %s fuel=%d/%d cargo=%d/%d\n",
			s.ShipSymbol, s.NavStatus, s.LocationSymbol,
			s.FuelCurrent, s.FuelCapacity, s.CargoUnits, s.CargoCapacity))
	}

	// Containers
	var containers []persistence.ContainerModel
	if err := db.WithContext(ctx).
		Where("player_id = ? AND status = ?", playerID, "RUNNING").
		Find(&containers).Error; err != nil {
		return "", err
	}
	b.WriteString("\n## Active containers\n")
	if len(containers) == 0 {
		b.WriteString("(none running)\n")
	}
	for _, c := range containers {
		age := "?"
		if c.StartedAt != nil {
			age = now.Sub(*c.StartedAt).Round(time.Minute).String()
		}
		b.WriteString(fmt.Sprintf("- %s: %s running for %s\n", c.ID, c.CommandType, age))
	}

	// Treasury / KPIs from ledger
	credits, err := CurrentCredits(ctx, db, playerID)
	if err != nil {
		return "", err
	}
	var dayAgoTx persistence.TransactionModel
	_ = db.WithContext(ctx).
		Where("player_id = ? AND timestamp >= ?", playerID, now.Add(-24*time.Hour)).
		Order("timestamp ASC").Limit(1).Find(&dayAgoTx).Error
	b.WriteString("\n## Treasury\n")
	b.WriteString(fmt.Sprintf("- Credits: %d\n", credits))
	if dayAgoTx.ID != "" {
		delta := credits - dayAgoTx.BalanceBefore
		b.WriteString(fmt.Sprintf("- 24h delta: %+d (≈ %+d credits/hour)\n", delta, delta/24))
	} else {
		b.WriteString("- 24h delta: no transactions in window\n")
	}

	// Decisions due for review (Learning loop §2 — forced outcome review)
	decisions, err := ReadDecisions(ws.StatePath("decisions.jsonl"))
	if err != nil {
		return "", err
	}
	due := DueForReview(decisions, now)
	if len(due) > maxDueDecisions {
		due = due[:maxDueDecisions]
	}
	b.WriteString("\n## Decisions due for review\n")
	if len(due) == 0 {
		b.WriteString("(none)\n")
	}
	for _, d := range due {
		raw, _ := json.Marshal(d)
		b.WriteString("- " + string(raw) + "\n")
	}

	// Memory
	b.WriteString("\n## Standing strategy (state/strategy.md)\n")
	b.WriteString(ws.ReadFull("strategy.md") + "\n")
	b.WriteString("\n## Lessons (state/lessons.md)\n")
	b.WriteString(ws.ReadFull("lessons.md") + "\n")
	b.WriteString("\n## Recent log tail (state/captain-log.md)\n")
	b.WriteString(ws.Tail("captain-log.md", logTailBytes) + "\n")

	// Capability coverage: verbs the fleet owns but the captain has never run.
	if verbs := UnexercisedVerbs(ws); len(verbs) > 0 {
		b.WriteString("\n## Capability coverage — NEVER-EXERCISED verbs\n")
		b.WriteString(strings.Join(verbs, ", ") + "\n")
		b.WriteString("These are fleet capabilities you have never once invoked. An\n")
		b.WriteString("unexplored capability is a standing liability (see obligations).\n")
	}

	// Admiral inbox: challenges from the human, cleared after the session.
	if msg, err := os.ReadFile(ws.InboxPath()); err == nil && len(strings.TrimSpace(string(msg))) > 0 {
		b.WriteString("\n## Message from the Admiral\n")
		b.Write(msg)
		b.WriteString("\nYou MUST address it with evidence in your log this session — agree,")
		b.WriteString("\nrebut, or design the cheap experiment that would settle it. The message")
		b.WriteString("\nclears automatically; record what matters in your own memory files.\n")
	}

	// Session contract (details live in the workspace CLAUDE.md; this is the reminder)
	b.WriteString(`
## Your obligations this session
1. Close every decision listed under "Decisions due for review": append an
   updated JSONL line to state/decisions.jsonl with outcome (worked|failed|inconclusive),
   verdict notes, and a lesson for failures/surprises.
2. Assess the pending events and fleet state; act via the spacetraders CLI.
3. Record every non-trivial action as a new decision line with a measurable
   expectation and review_after time.
4. Append a dated entry to state/captain-log.md (decisions + rationale + friction: tags).
5. Revise state/strategy.md if KPIs disagree with its targets; curate state/lessons.md.
6. STRATEGY DUTY — EVERY SESSION, NOT JUST QUIET ONES: events are triage,
   the mission is the job. After handling events, you MUST advance the top
   strategic priority with a CONCRETE step this session: standing Admiral
   directives first, then your Horizon plan, then never-exercised
   capability study (--help end to end + a read-only/--dry-run execution).
   "Deferred until idle" is forbidden — a healthy fleet is never idle, so
   idle-gated strategy is starvation. If you truly cannot act, record
   exactly what blocks you as a decision with a review time.
7. At heartbeat reviews (no pending events): identify the single BINDING
   CONSTRAINT on credits/hour growth — capital, fleet capacity, market
   intelligence, tooling, or something else. State the evidence, then either
   record a decision that attacks the constraint or record why attacking it
   now is wrong. Busy fleets emit no events; constraints must be hunted, not
   waited for. Your tools can tell you what your assets can do — read their
   help before assuming your current usage is optimal.
`)
	return b.String(), nil
}
