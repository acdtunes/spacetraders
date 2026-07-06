package captainsup

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

var scopedBeadTypes = map[string]bool{
	"decision": true,
	"consult":  true,
	"handoff":  true,
}

var leadingLessonTag = regexp.MustCompile(`^L\d+\s*(\[[^\]]*\]\s*)?[—\-]*\s*`)
var waypointFullPattern = regexp.MustCompile(`\b[A-Z]\d{1,2}-[A-Z]{1,4}\d{1,4}(?:-[A-Z0-9]+)*\b`)
var waypointShortPattern = regexp.MustCompile(`\b[A-Z]\d{2,3}\b`)

type beadRecord struct {
	ID        string    `json:"id"`
	IssueType string    `json:"issue_type"`
	Labels    []string  `json:"labels"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type memoryRecord struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Text  string `json:"text"`
}

func (m memoryRecord) content() string {
	return firstNonEmpty(m.Value, m.Text)
}

// MemoryProposal is a KEEP/REWRITE/RETIRE suggestion for one bd memory, per
// spec §4.2. It is never auto-applied: the tool only proposes.
type MemoryProposal struct {
	Key    string
	Action string
	Reason string
}

// EraCloseReport is the plan (and, if applied, the outcome) of the beads
// era-close ritual (spec §6 phase 3) plus the read-only memory-review
// proposal table (spec §4.2, §6 phase 4).
type EraCloseReport struct {
	EraName         string
	Labeled         []string
	Closed          []string
	StrategyBead    string
	Commands        [][]string
	MemoryProposals []MemoryProposal
}

// EraClose plans (and, when apply is true, executes) the beads phase-3
// era-close sequence: date-window label sweep over scoped bead types, bulk
// close of open scoped beads, and strategy-bead demotion. It always reads
// bd list/memories JSON (non-destructive) to build a real plan, and always
// computes the phase-4 memory proposal table — but never executes a memory
// action (bd remember/forget): that boundary is Admiral-approval-only.
func EraClose(ctx context.Context, b *BeadsClient, eraName, resetDate string, windowStart, windowEnd time.Time, agentSymbol string, apply bool) (EraCloseReport, error) {
	rep := EraCloseReport{EraName: eraName}

	beads, err := listBeads(ctx, b)
	if err != nil {
		return rep, err
	}

	eraLabel := "era:" + eraName
	var scoped []beadRecord
	for _, bead := range beads {
		if scopedBeadTypes[bead.IssueType] && withinWindow(bead.CreatedAt, windowStart, windowEnd) {
			scoped = append(scoped, bead)
		}
	}
	sort.Slice(scoped, func(i, j int) bool { return scoped[i].ID < scoped[j].ID })

	var toLabel, openIDs []string
	for _, bead := range scoped {
		if !hasLabel(bead.Labels, eraLabel) {
			toLabel = append(toLabel, bead.ID)
		}
		if bead.Status == "open" {
			openIDs = append(openIDs, bead.ID)
		}
	}

	write := func(args ...string) error {
		rep.Commands = append(rep.Commands, append([]string{b.BDBin}, args...))
		if !apply {
			return nil
		}
		_, err := b.Exec(ctx, b.BDBin, args...)
		return err
	}

	if len(toLabel) > 0 {
		args := append([]string{"label", "add"}, toLabel...)
		args = append(args, eraLabel)
		if err := write(args...); err != nil {
			return rep, err
		}
		rep.Labeled = toLabel
	}

	if len(openIDs) > 0 {
		reason := fmt.Sprintf("era %s ended (universe reset %s)", eraName, resetDate)
		args := append([]string{"close"}, openIDs...)
		args = append(args, "--reason", reason)
		if err := write(args...); err != nil {
			return rep, err
		}
		rep.Closed = openIDs
	}

	for _, bead := range beads {
		if hasLabel(bead.Labels, "strategy") && bead.Status == "open" {
			if err := write("close", bead.ID, "--reason", "demoted to retrospective input"); err != nil {
				return rep, err
			}
			rep.StrategyBead = bead.ID
			break
		}
	}

	memories, err := listMemories(ctx, b)
	if err != nil {
		return rep, err
	}
	for _, m := range memories {
		action, reason := classifyMemory(m.content(), agentSymbol)
		rep.MemoryProposals = append(rep.MemoryProposals, MemoryProposal{Key: m.Key, Action: action, Reason: reason})
	}

	return rep, nil
}

func listBeads(ctx context.Context, b *BeadsClient) ([]beadRecord, error) {
	// --all + -n 0: the sweep must see the full corpus — closed beads still
	// need era labels, and bd's default limit (50) silently truncates.
	out, err := b.Exec(ctx, b.BDBin, "list", "--json", "--all", "-n", "0")
	if err != nil {
		return nil, err
	}
	var raw []beadRecord
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("parse bd list: %w", err)
	}
	return raw, nil
}

func listMemories(ctx context.Context, b *BeadsClient) ([]memoryRecord, error) {
	out, err := b.Exec(ctx, b.BDBin, "memories", "--json")
	if err != nil {
		return nil, err
	}
	// bd memories --json emits a flat {key: text} map.
	var raw map[string]string
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("parse bd memories: %w", err)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	records := make([]memoryRecord, 0, len(keys))
	for _, k := range keys {
		records = append(records, memoryRecord{Key: k, Text: raw[k]})
	}
	return records, nil
}

func hasLabel(labels []string, target string) bool {
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

// withinWindow reports whether t falls within [start, end], where end is the
// date-window end (inclusive per the --window-end flag contract): callers
// pass end as midnight of the end date, so the whole calendar day it names
// must be covered, not just its first instant.
func withinWindow(t, start, end time.Time) bool {
	return !t.Before(start) && t.Before(end.AddDate(0, 0, 1))
}

// classifyMemory applies the spec §4.2 heuristic: a memory with no
// universe-specific ship/waypoint marker is KEEP (universal as written); a
// memory that leads with such a marker is irreducibly instance-bound
// (RETIRE); a memory where the marker appears only as trailing evidence
// after a general clause is a universal rule wrapped in specific evidence
// (REWRITE — strip the instance, keep the rule). Final classification is
// always Admiral-approved before apply; this is a proposal only.
func classifyMemory(text, agentSymbol string) (action, reason string) {
	trimmed := strings.TrimSpace(leadingLessonTag.ReplaceAllString(strings.TrimSpace(text), ""))
	if trimmed == "" {
		return "KEEP", "empty text"
	}

	loc := markerIndex(trimmed, agentSymbol)
	if loc < 0 {
		return "KEEP", "no universe-specific ship/waypoint markers detected; treat as universal"
	}
	if loc == 0 {
		return "RETIRE", "leads with a universe-specific marker; irreducibly instance-bound"
	}
	return "REWRITE", "universal heuristic wrapped in universe-specific evidence; strip instance, keep rule"
}

func markerIndex(text, agentSymbol string) int {
	best := -1
	consider := func(loc []int) {
		if loc == nil {
			return
		}
		if best < 0 || loc[0] < best {
			best = loc[0]
		}
	}
	if strings.TrimSpace(agentSymbol) != "" {
		shipPattern := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(agentSymbol) + `-\d+\b`)
		consider(shipPattern.FindStringIndex(text))
	}
	consider(waypointFullPattern.FindStringIndex(text))
	consider(waypointShortPattern.FindStringIndex(text))
	return best
}
