package watchkeeper

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

// WakePolicy is the captain-declared wake policy (spec: sp-sk68 wake model):
// when to interrupt the supervisor's deferred-event batching regardless of
// the default cadence. Every field is optional — nil/empty means "no
// override, use the default." Declared via `spacetraders captain wake set`
// and consumed fresh by the supervisor at the top of every Tick, so a
// declaration takes effect on the very next poll without a restart.
type WakePolicy struct {
	// NextWakeAt, if set, overrides the heartbeat cadence for the next wake.
	// Always capped at LastSession+MaxWakeIntervalMinutes: it can delay a
	// wake, never suppress one past the never-wake ceiling.
	NextWakeAt *time.Time `json:"next_wake_at,omitempty"`
	// CreditsAbove/CreditsBelow force a wake once CurrentCredits crosses
	// either bound.
	CreditsAbove *int `json:"credits_above,omitempty"`
	CreditsBelow *int `json:"credits_below,omitempty"`
	// InterruptTypes, if non-empty, REPLACES (not extends) the default
	// interrupt set for classifying which event types force a wake.
	InterruptTypes []string  `json:"interrupt_types,omitempty"`
	DeclaredAt     time.Time `json:"declared_at,omitempty"`
}

// RegimeTripwire is one captain-declared price tripwire (sp-zlfv): the
// watchkeeper's price-regime detector emits a deferred market.regime_shift
// event once a matching good's market price crosses Threshold — or, in
// relative mode, Multiplier times a recorded baseline price — in Direction.
// Exactly one of Threshold/Multiplier is set. Mechanizes the per-wake price
// sweep the captain used to hand-roll.
type RegimeTripwire struct {
	// Good is either a good-class keyword ("ORE", "GAS") or a comma-separated
	// literal symbol list (e.g. "IRON_ORE,COPPER_ORE").
	Good string `json:"good"`
	// Direction is "bid-above" or "bid-below".
	Direction string `json:"direction"`
	// Threshold is an absolute sell price. Mutually exclusive with Multiplier.
	Threshold *int `json:"threshold,omitempty"`
	// Multiplier expresses the threshold as Nx a recorded baseline price
	// (the oldest price-history sample within Window). Mutually exclusive
	// with Threshold.
	Multiplier *float64 `json:"multiplier,omitempty"`
	// Window is both the baseline lookback (multiplier mode) and the
	// edge-trigger cooldown: once a crossing fires, the same crossing does
	// not re-fire until Window elapses (sp-1hak HasSince lesson).
	Window    time.Duration `json:"window"`
	CreatedAt time.Time     `json:"created_at,omitempty"`
}

// RegimePolicy is the captain-declared set of price tripwires (sp-zlfv).
// Declared via `spacetraders captain regime set` and consumed fresh by the
// supervisor at the top of every Tick, so a declaration takes effect on the
// very next poll without a restart. An empty/nil Tripwires list disables the
// detector entirely — no config means no scan.
type RegimePolicy struct {
	Tripwires []RegimeTripwire `json:"tripwires,omitempty"`
}

// supervisorState is the durable subset of Supervisor's scheduling
// bookkeeping. Everything here must survive a process restart so a fresh
// process never re-treats an already-armed cadence as immediately due: a
// restart must never fire an immediate wake or survey nudge.
//
// The struct has four independent owners sharing one file: the supervisor
// writes the cadence fields (LastSession, LastSurveyorNudge, Renudges,
// Escalated, LastCredits) via saveCadenceState, the captain CLI writes the
// embedded WakePolicy via SaveWakePolicy, the captain CLI separately writes
// the embedded RegimePolicy via SaveRegimePolicy, and the supervisor+CLI
// share the embedded WatchPolicy via SaveWatchPolicy (sp-oyer one-shot
// watches). Each writer reads the current file, mutates only its own fields,
// and writes back atomically, so no writer clobbers another's most recent
// write.
type supervisorState struct {
	LastSession       time.Time      `json:"last_session"`
	LastSurveyorNudge time.Time      `json:"last_surveyor_nudge"`
	Renudges          map[int64]int  `json:"renudges,omitempty"`
	Escalated         map[int64]bool `json:"escalated,omitempty"`
	LastCredits       int            `json:"last_credits,omitempty"`
	// LastNudge persists the nudge-coalescing cooldown clock so a restart
	// mid-window does not reset it and re-storm (sp-o8wi). omitempty keeps it
	// absent from state written before this field existed — those files load
	// with LastNudge zero, which correctly means "fire the first wake at once".
	LastNudge time.Time `json:"last_nudge,omitempty"`

	// A captain-declared credits wake bound is ONE-SHOT (sp-wfut, revising
	// sp-l6pz): it is consumed — set to nil in the embedded WakePolicy — on the
	// delivered wake that services it, so the persisted policy itself IS the
	// durable one-shot state. No separate fired-flag/edge field is needed (the
	// sp-l6pz credits_above_fired/credits_below_fired keys are retired). An old
	// state file that still carries those keys loads fine: JSON ignores the now-
	// unknown fields, and any bound already consumed pre-upgrade is simply absent.
	WakePolicy
	RegimePolicy
	WatchPolicy
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
// directory on demand if this is the first write in a fresh workspace. This
// is a full overwrite (not a read-merge-write): callers that must preserve
// fields they do not own should use saveCadenceState or SaveWakePolicy
// instead.
func saveSupervisorState(path string, st supervisorState) error {
	return writeStateAtomic(path, st)
}

// writeStateAtomic serializes st as indented JSON and installs it at path
// via a temp file + rename, so a concurrent reader (or a crash mid-write)
// never observes a partially-written file.
func writeStateAtomic(path string, st supervisorState) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".supervisor-state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// atomicUpdateState reads the current state at path (the zero value if the
// file does not exist yet), applies mutate, and writes the result back
// atomically. This is the dual-writer-safety primitive shared by
// saveCadenceState and SaveWakePolicy: each supplies a mutate func that
// touches only the fields it owns, so the two writers never clobber each
// other even though both target the same file.
func atomicUpdateState(path string, mutate func(*supervisorState)) error {
	st, err := loadSupervisorState(path)
	if err != nil {
		return err
	}
	mutate(&st)
	return writeStateAtomic(path, st)
}

// saveCadenceState updates only the supervisor-owned cadence fields
// (LastSession, LastSurveyorNudge, Renudges, Escalated, LastCredits,
// LastNudge), preserving whatever wake policy the captain has separately
// declared.
func saveCadenceState(path string, cadence supervisorState) error {
	return atomicUpdateState(path, func(st *supervisorState) {
		st.LastSession = cadence.LastSession
		st.LastSurveyorNudge = cadence.LastSurveyorNudge
		st.Renudges = cadence.Renudges
		st.Escalated = cadence.Escalated
		st.LastCredits = cadence.LastCredits
		st.LastNudge = cadence.LastNudge
	})
}

// SaveWakePolicy updates only the captain-owned wake-policy fields,
// preserving the supervisor's cadence bookkeeping untouched. This is what
// `spacetraders captain wake set` calls.
func SaveWakePolicy(path string, policy WakePolicy) error {
	return atomicUpdateState(path, func(st *supervisorState) {
		st.WakePolicy = policy
	})
}

// LoadWakePolicy returns the captain-declared wake policy, or the zero
// value (no overrides — default cadence and default interrupt set apply) if
// none has been declared yet. This is what `spacetraders captain wake show`
// calls, and what the supervisor re-reads at the top of every Tick.
func LoadWakePolicy(path string) (WakePolicy, error) {
	st, err := loadSupervisorState(path)
	if err != nil {
		return WakePolicy{}, err
	}
	return st.WakePolicy, nil
}

// SaveRegimePolicy updates only the captain-owned regime-tripwire fields,
// preserving the supervisor's cadence bookkeeping and the independently
// declared WakePolicy untouched. This is what `spacetraders captain regime
// set`/`clear` call.
func SaveRegimePolicy(path string, policy RegimePolicy) error {
	return atomicUpdateState(path, func(st *supervisorState) {
		st.RegimePolicy = policy
	})
}

// LoadRegimePolicy returns the captain-declared price tripwires, or the zero
// value (no tripwires — the regime detector does not scan) if none has been
// declared yet. This is what `spacetraders captain regime list` calls, and
// what the supervisor re-reads at the top of every Tick (before running
// detectors, since tripwires configure detector behavior rather than just
// the wake gate).
func LoadRegimePolicy(path string) (RegimePolicy, error) {
	st, err := loadSupervisorState(path)
	if err != nil {
		return RegimePolicy{}, err
	}
	return st.RegimePolicy, nil
}

// SaveWatchPolicy updates only the one-shot wake-watch fields (sp-oyer),
// preserving the supervisor's cadence bookkeeping and the independently
// declared WakePolicy/RegimePolicy untouched. This is what `spacetraders
// captain wake watch add|clear` calls, and what the supervisor calls to
// persist the surviving (not-yet-fired) watches after a fire disarms one.
func SaveWatchPolicy(path string, policy WatchPolicy) error {
	return atomicUpdateState(path, func(st *supervisorState) {
		st.WatchPolicy = policy
	})
}

// LoadWatchPolicy returns the captain-declared one-shot wake watches, or the
// zero value (no watches armed) if none has been declared yet. This is what
// `spacetraders captain wake watch list` calls, and what the supervisor
// re-reads at the top of every Tick to evaluate armed watches against the
// event batch and the wall clock.
func LoadWatchPolicy(path string) (WatchPolicy, error) {
	st, err := loadSupervisorState(path)
	if err != nil {
		return WatchPolicy{}, err
	}
	return st.WatchPolicy, nil
}
