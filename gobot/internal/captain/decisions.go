package captainsup

import (
	"bufio"
	"encoding/json"
	"os"
	"time"
)

// Decision mirrors one line of state/decisions.jsonl (spec: Learning loop §1).
type Decision struct {
	ID          string    `json:"id"`
	TS          string    `json:"ts,omitempty"`
	Action      string    `json:"action"`
	Rationale   string    `json:"rationale,omitempty"`
	Expectation string    `json:"expectation"`
	ReviewAfter time.Time `json:"review_after"`
	Outcome     *string   `json:"outcome,omitempty"`
	Verdict     string    `json:"verdict,omitempty"`
	Lesson      string    `json:"lesson,omitempty"`
}

// ReadDecisions parses decisions.jsonl, skipping malformed lines (the file is
// LLM-written; one bad line must not poison the ledger). Missing file = empty.
func ReadDecisions(path string) ([]Decision, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Decision
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		var d Decision
		if err := json.Unmarshal(scanner.Bytes(), &d); err != nil || d.ID == "" {
			continue
		}
		out = append(out, d)
	}
	return out, scanner.Err()
}

// DueForReview: review_after has passed and no outcome recorded yet.
func DueForReview(ds []Decision, now time.Time) []Decision {
	var due []Decision
	for _, d := range ds {
		if d.Outcome == nil && !d.ReviewAfter.IsZero() && d.ReviewAfter.Before(now) {
			due = append(due, d)
		}
	}
	return due
}
