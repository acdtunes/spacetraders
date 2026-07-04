package captainsup

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"gorm.io/gorm"
)

const metaReviewMarker = "last-meta-review"

func MetaReviewDue(ws Workspace, now time.Time) bool {
	data, err := os.ReadFile(ws.StatePath(metaReviewMarker))
	if err != nil {
		return true
	}
	last, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		return true
	}
	return now.Sub(last) >= 24*time.Hour
}

func MarkMetaReviewDone(ws Workspace, now time.Time) error {
	return os.WriteFile(ws.StatePath(metaReviewMarker), []byte(now.UTC().Format(time.RFC3339)), 0o644)
}

// frictionLines extracts `friction:` observations from the captain's log.
func frictionLines(log string) []string {
	var out []string
	for _, line := range strings.Split(log, "\n") {
		if idx := strings.Index(strings.ToLower(line), "friction:"); idx >= 0 {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// ComposeMetaReview builds the daily meta-game session prompt
// (spec: Meta-game improvement loop §2).
func ComposeMetaReview(ctx context.Context, db *gorm.DB, ws Workspace, playerID int, now time.Time) (string, error) {
	var b strings.Builder
	b.WriteString("# Meta-review: upgrade your own instrument panel\n")
	b.WriteString("Generated: " + now.UTC().Format(time.RFC3339) + "\n\n")

	credits, err := CurrentCredits(ctx, db, playerID)
	if err != nil {
		return "", err
	}
	b.WriteString(fmt.Sprintf("## KPI check\n- Current credits: %d\n", credits))

	b.WriteString("\n## Friction queue (state/friction.md — cleared after this review)\n")
	if fq := strings.TrimSpace(ws.ReadFull("friction.md")); fq != "" {
		b.WriteString(fq + "\n")
	} else {
		// Fallback for the transition + safety net: recent log tail only,
		// so resolved ancient friction cannot resurface forever.
		fl := frictionLines(ws.Tail("captain-log.md", 24*1024))
		if len(fl) == 0 {
			b.WriteString("(queue empty — if sessions hit friction, they are not recording it; fix that habit)\n")
		}
		for _, l := range fl {
			b.WriteString("- " + l + "\n")
		}
	}

	b.WriteString("\n## Lessons (state/lessons.md)\n")
	b.WriteString(ws.ReadFull("lessons.md") + "\n")

	b.WriteString("\n## Current improvement backlog (state/improvement-backlog.md)\n")
	b.WriteString(ws.ReadFull("improvement-backlog.md") + "\n")

	b.WriteString(`
## Your obligations this meta-review
1. Rewrite state/improvement-backlog.md: re-score existing proposals against
   the evidence above, prune obsolete ones, add new ones from friction. Each
   proposal needs: problem, evidence (decision/friction refs), sketch of the
   change, expected ROI (credits/hour or captain effectiveness).
2. Promote at most ONE proposal to ready by writing a feature report to
   reports/bugs/YYYY-MM-DD-<slug>.md with frontmatter kind: feature,
   status: new. Only promote when the top proposal's evidence is strong; an
   empty promotion round is a fine outcome.
3. Verify the last merged improvement (if any) actually moved the KPI it
   promised; record the verdict as a lesson in state/lessons.md.
4. BOLDNESS AUDIT: list the moves you did NOT take since the last review
   and why. Sort the reasons: EVIDENCE (genuinely insufficient data) vs
   PROCESS (review windows, formality, "validate first" on a foregone
   conclusion). Every PROCESS-reason hold is a failure pattern — record it
   as a lesson with the opportunity cost in credits/hours.
5. STRATEGIC HORIZON: your KPI measures the current income loop, but a
   fleet that only exploits its known loop plateaus. Review the frontier:
   CLI capabilities you have NEVER exercised, systems never visited,
   long-horizon structures the game rewards, and income streams that would
   run IN PARALLEL to the current one. Maintain a "Horizon" section in
   state/strategy.md ranking such objectives by step-change potential vs
   incremental gain, and either start a cheap probe toward the top one or
   record why not yet. Optimizing a local maximum forever is a failure mode.
6. Append a meta-review entry to state/captain-log.md.
`)
	return b.String(), nil
}
