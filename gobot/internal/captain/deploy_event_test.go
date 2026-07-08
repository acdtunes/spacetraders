package watchkeeper

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/buildinfo"
)

// recordingDeployStore is a deterministic in-memory stand-in for the narrow
// store RecordDeployIfChanged needs: record an event and read back the latest
// of a type, scoped by player. Mirrors recordingBurstStore's shape
// (burstgroup_test.go) — an append-only log where the LAST matching entry is
// authoritative, exactly like ORDER BY created_at DESC, id DESC in the real
// GORM repository.
type recordingDeployStore struct {
	events    []*captain.Event
	latestErr error
	recordErr error
}

func (s *recordingDeployStore) Record(_ context.Context, e *captain.Event) error {
	if s.recordErr != nil {
		return s.recordErr
	}
	cp := *e
	s.events = append(s.events, &cp)
	return nil
}

func (s *recordingDeployStore) LatestByType(_ context.Context, playerID int, t captain.EventType) (*captain.Event, error) {
	if s.latestErr != nil {
		return nil, s.latestErr
	}
	var latest *captain.Event
	for _, e := range s.events {
		if e.PlayerID == playerID && e.Type == t {
			latest = e
		}
	}
	return latest, nil
}

func seedDeployEvent(playerID int, commit string) *captain.Event {
	payload, _ := json.Marshal(deployEventPayload{Commit: commit, Version: "v-old", BuildTime: "old-time"})
	return &captain.Event{Type: captain.EventDeployCompleted, PlayerID: playerID, Payload: string(payload)}
}

func noBeadID() string { return "" }

// TestRecordDeployIfChangedEmitsOnFirstBoot: no prior deploy.completed event
// at all means this boot is, by definition, the first recorded deploy — emit
// once (team-lead's explicit first-boot/edge-case requirement).
func TestRecordDeployIfChangedEmitsOnFirstBoot(t *testing.T) {
	store := &recordingDeployStore{}
	info := buildinfo.Info{Commit: "abc123", Version: "v1.2.3", BuildTime: "2026-07-08T00:00:00Z"}

	err := RecordDeployIfChanged(context.Background(), store, 1, info, noBeadID)

	require.NoError(t, err)
	require.Len(t, store.events, 1, "first boot ever must emit exactly one deploy.completed")
	require.Equal(t, captain.EventDeployCompleted, store.events[0].Type)
	require.Equal(t, 1, store.events[0].PlayerID)

	var got deployEventPayload
	require.NoError(t, json.Unmarshal([]byte(store.events[0].Payload), &got))
	require.Equal(t, "abc123", got.Commit)
	require.Equal(t, "v1.2.3", got.Version)
}

// TestRecordDeployIfChangedNoOpWhenCommitUnchanged is the core regression this
// bead exists to prevent: an ordinary crash-restart of the SAME binary must
// NOT look like a fresh deploy, or the crash-loop-resumes-on-deploy doctrine
// would fire on every crash-bounce instead of only on a real redeploy.
func TestRecordDeployIfChangedNoOpWhenCommitUnchanged(t *testing.T) {
	store := &recordingDeployStore{events: []*captain.Event{seedDeployEvent(1, "abc123")}}
	info := buildinfo.Info{Commit: "abc123", Version: "v1.2.3", BuildTime: "now"}

	err := RecordDeployIfChanged(context.Background(), store, 1, info, noBeadID)

	require.NoError(t, err)
	require.Len(t, store.events, 1, "same commit as the last recorded deploy.completed is a crash-restart, not a deploy — must not emit")
}

// TestRecordDeployIfChangedEmitsWhenCommitChanged proves a genuinely new
// binary (different commit than the last recorded deploy.completed) emits,
// and the new event becomes the baseline for the next boot.
func TestRecordDeployIfChangedEmitsWhenCommitChanged(t *testing.T) {
	store := &recordingDeployStore{events: []*captain.Event{seedDeployEvent(1, "abc123")}}
	info := buildinfo.Info{Commit: "def456", Version: "v1.3.0", BuildTime: "later"}

	err := RecordDeployIfChanged(context.Background(), store, 1, info, noBeadID)

	require.NoError(t, err)
	require.Len(t, store.events, 2, "a different commit than the recorded baseline is a real deploy — must emit")
	var got deployEventPayload
	require.NoError(t, json.Unmarshal([]byte(store.events[1].Payload), &got))
	require.Equal(t, "def456", got.Commit)
}

// TestRecordDeployIfChangedFailsOpenWhenBaselineReadErrors mirrors
// BurstGroupingRecorder's own philosophy (burstgroup.go): this emission is
// one-shot at boot, so a transient failure to READ the baseline must not
// silently suppress the one signal this bead exists to provide.
func TestRecordDeployIfChangedFailsOpenWhenBaselineReadErrors(t *testing.T) {
	store := &recordingDeployStore{latestErr: errors.New("db down")}
	info := buildinfo.Info{Commit: "abc123"}

	err := RecordDeployIfChanged(context.Background(), store, 1, info, noBeadID)

	require.NoError(t, err)
	require.Len(t, store.events, 1, "a baseline-read error must fail OPEN (emit), not silently drop the boot's deploy signal")
}

// TestRecordDeployIfChangedIncludesBeadIDWhenLookupSucceeds proves the
// injected bead-id lookup's result lands in the payload as garnish alongside
// the commit, which remains the actual deploy signal.
func TestRecordDeployIfChangedIncludesBeadIDWhenLookupSucceeds(t *testing.T) {
	store := &recordingDeployStore{}
	info := buildinfo.Info{Commit: "abc123"}

	err := RecordDeployIfChanged(context.Background(), store, 1, info, func() string { return "sp-ess3" })

	require.NoError(t, err)
	require.Len(t, store.events, 1)
	var got deployEventPayload
	require.NoError(t, json.Unmarshal([]byte(store.events[0].Payload), &got))
	require.Equal(t, "sp-ess3", got.BeadID)
}

// TestRecordDeployIfChangedToleratesNilBeadIDLookup proves a nil beadID
// function (no lookup available at all) degrades to an empty bead-id and
// never panics or blocks the emit — bead-id is best-effort garnish, never a
// precondition for the actual deploy signal (the commit).
func TestRecordDeployIfChangedToleratesNilBeadIDLookup(t *testing.T) {
	store := &recordingDeployStore{}
	info := buildinfo.Info{Commit: "abc123"}

	require.NotPanics(t, func() {
		err := RecordDeployIfChanged(context.Background(), store, 1, info, nil)
		require.NoError(t, err)
	})
	require.Len(t, store.events, 1)
	var got deployEventPayload
	require.NoError(t, json.Unmarshal([]byte(store.events[0].Payload), &got))
	require.Empty(t, got.BeadID)
}

// TestRecordDeployIfChangedPropagatesRecordError proves a write failure
// surfaces to the caller (cmd/spacetraders-daemon/main.go) rather than being
// swallowed, so it can be logged — the "never block the boot" policy belongs
// at that call site, not here.
func TestRecordDeployIfChangedPropagatesRecordError(t *testing.T) {
	store := &recordingDeployStore{recordErr: errors.New("write failed")}
	info := buildinfo.Info{Commit: "abc123"}

	err := RecordDeployIfChanged(context.Background(), store, 1, info, noBeadID)

	require.Error(t, err)
}
