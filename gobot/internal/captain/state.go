package captainsup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// supervisorStateFile is the on-disk name for the supervisor's durable
// scheduling state, kept alongside the captain's other bookkeeping
// artifacts (strategy.md, decisions.jsonl, lessons.md, ...) under the
// workspace's state/ subdirectory.
const supervisorStateFile = "supervisor-state.json"

// supervisorState is the durable subset of Supervisor's scheduling
// bookkeeping. Everything here must survive a process restart so a fresh
// process never re-treats an already-armed cadence as immediately due: a
// restart must never fire an immediate wake or survey nudge.
type supervisorState struct {
	LastSession       time.Time      `json:"last_session"`
	LastSurveyorNudge time.Time      `json:"last_surveyor_nudge"`
	Renudges          map[int64]int  `json:"renudges,omitempty"`
	Escalated         map[int64]bool `json:"escalated,omitempty"`
}

// StatePath returns where the supervisor's durable scheduling state lives
// for this workspace.
func (w Workspace) StatePath() string {
	return filepath.Join(w.dir, "state", supervisorStateFile)
}

// loadSupervisorState reads persisted scheduling state. A missing file is
// not an error — it returns the zero value so the caller can arm cadences
// fresh (one full interval out, never immediately due). A present but
// corrupt file is reported as an error so the caller can decide how to
// degrade.
func loadSupervisorState(path string) (supervisorState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return supervisorState{}, nil
		}
		return supervisorState{}, err
	}
	var st supervisorState
	if err := json.Unmarshal(data, &st); err != nil {
		return supervisorState{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return st, nil
}

// saveSupervisorState writes scheduling state, creating the state
// directory on demand if this is the first write in a fresh workspace.
func saveSupervisorState(path string, st supervisorState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
