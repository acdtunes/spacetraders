package watchkeeper

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/buildinfo"
)

// deployEventStore is the narrow slice of captain.EventStore RecordDeployIfChanged
// needs: record an event and read back the most recent one of a type. Kept
// narrow so tests can substitute a recording fake (mirrors burstCooldownStore,
// burstgroup.go); *persistence.GormCaptainEventRepository satisfies it.
type deployEventStore interface {
	Record(ctx context.Context, e *captain.Event) error
	LatestByType(ctx context.Context, playerID int, t captain.EventType) (*captain.Event, error)
}

// deployEventPayload is the JSON shape recorded on captain.Event.Payload for a
// deploy.completed event. Commit is the actual deploy signal (compared boot to
// boot); Version/BuildTime ride along as the rest of the build stamp. BeadID is
// best-effort garnish (see RecordDeployIfChanged) — empty, never a blocking
// error, when it cannot be determined.
type deployEventPayload struct {
	Commit    string `json:"commit"`
	Version   string `json:"version"`
	BuildTime string `json:"build_time"`
	BeadID    string `json:"bead_id,omitempty"`
}

// RecordDeployIfChanged is the daemon's own deploy signal (sp-ess3). There is
// no distinct Go "merge-deploy" path anywhere in this codebase: the gate
// (cmd/captain-gate, worktree.go SquashMerge) only gates and squash-merges;
// the actual rebuild+restart happens out-of-process, driven by the shipwright
// agent's own shell commands. The one honest, in-process signal that a deploy
// actually happened is: THIS process just booted running a different commit
// than the last deploy.completed event on record. That is exactly what this
// function checks — call it once, at daemon startup.
//
// The baseline is the captain_events store itself, not a new table: it reads
// the most recent deploy.completed event for the player and compares its
// recorded commit to info.Commit.
//
//   - No prior deploy.completed event at all (first boot ever, or a fresh
//     database) -> treated as changed -> emit once (that boot did ship this
//     build).
//   - Same commit as the last recorded deploy.completed -> this boot is an
//     ordinary crash-restart of the SAME binary, not a deploy -> do NOT emit.
//     Firing here would be actively harmful: the crash-loop-resumes-on-deploy
//     doctrine (bd memory crashloop-resumes-on-deploy-sp-ess3) resumes a
//     crash-looping job on deploy.completed, so a spurious emit on every
//     crash-bounce would make the captain re-roll the exact same bad binary
//     immediately, over and over — the opposite of this bead's purpose.
//   - Different commit -> a fresh binary is running -> emit, and the new
//     event becomes the baseline for the next boot (self-correcting, no
//     separate "last deploy" storage needed).
//
// A failure to READ the baseline (a transient DB hiccup) fails OPEN (treated
// as changed -> emits), mirroring BurstGroupingRecorder's own cooldown-check
// philosophy (burstgroup.go): this emission is one-shot at boot, so silently
// skipping it on a transient error risks permanently losing the one signal
// this bead exists to provide.
//
// beadID is an injected best-effort lookup (e.g. BeadIDFromHEAD) so this
// function is fully testable without invoking real git; it may be nil, and
// any empty result just means the emitted event carries no bead id. Bead id
// is never a precondition for emitting — the commit is the deploy signal.
//
// The event is deferred class: EventDeployCompleted is deliberately NOT in
// DefaultInterruptTypes, so this rides the next wake for any reason rather
// than forcing an immediate one.
func RecordDeployIfChanged(ctx context.Context, store deployEventStore, playerID int, info buildinfo.Info, beadID func() string) error {
	changed := true
	if last, err := store.LatestByType(ctx, playerID, captain.EventDeployCompleted); err == nil && last != nil {
		var prev deployEventPayload
		if jsonErr := json.Unmarshal([]byte(last.Payload), &prev); jsonErr == nil {
			changed = prev.Commit != info.Commit
		}
		// A malformed prior payload leaves changed at its zero-value default
		// (true) — fail open, same as a read error.
	}
	// err != nil from LatestByType: changed stays true — fail open (see doc).
	if !changed {
		return nil
	}

	var bead string
	if beadID != nil {
		bead = beadID()
	}
	payload, err := json.Marshal(deployEventPayload{
		Commit:    info.Commit,
		Version:   info.Version,
		BuildTime: info.BuildTime,
		BeadID:    bead,
	})
	if err != nil {
		return fmt.Errorf("watchkeeper: marshal deploy.completed payload: %w", err)
	}
	return store.Record(ctx, &captain.Event{
		Type:     captain.EventDeployCompleted,
		PlayerID: playerID,
		Payload:  string(payload),
	})
}
