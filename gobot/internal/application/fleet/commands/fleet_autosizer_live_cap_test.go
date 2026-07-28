package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

type fakeLiveConfig struct {
	snap liveconfig.Snapshot
	err  error
}

func (f *fakeLiveConfig) Snapshot(_ context.Context, _ string, _ int) (liveconfig.Snapshot, error) {
	return f.snap, f.err
}

// A tuned heavy_cap takes effect on the NEXT tick with no rebuild — the whole point of the
// liveconfig seam (reconcileOnce already re-resolves config every tick; only the SOURCE was static).
func TestLiveHeavyCap_TunedValueAppliesNextTick(t *testing.T) {
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	h.SetHeavyCapReader(&fakeLiveConfig{snap: liveconfig.Snapshot{heavyCapKey: 12}})
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyCap: intPtr(5)}

	if got := h.liveHeavyCap(context.Background(), cmd, 5); got != 12 {
		t.Fatalf("tuned heavy_cap = %d, want the live value 12", got)
	}
}

// A nil reader keeps LAUNCH behaviour — never an empty config.
func TestLiveHeavyCap_NilReaderKeepsLaunchValue(t *testing.T) {
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyCap: intPtr(7)}

	if got := h.liveHeavyCap(context.Background(), cmd, 7); got != 7 {
		t.Fatalf("nil reader heavy_cap = %d, want the launch value 7", got)
	}
}

// A snapshot ERROR falls back to the launch value rather than zeroing the cap. Zeroing would read
// as an operator hold and silently stop all heavy buying on a transient DB blip.
func TestLiveHeavyCap_SnapshotErrorFallsBackNotZero(t *testing.T) {
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	h.SetHeavyCapReader(&fakeLiveConfig{err: errors.New("db blip")})
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyCap: intPtr(5)}

	if got := h.liveHeavyCap(context.Background(), cmd, 5); got != 5 {
		t.Fatalf("errored snapshot heavy_cap = %d, want the launch value 5 (never 0 — that reads as a hold)", got)
	}
}

// An absent key is the `tune <key> 0` revert: the tune write DELETES the key, so absence means
// "the launch/documented value applies again".
func TestLiveHeavyCap_AbsentKeyRevertsToLaunchValue(t *testing.T) {
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	h.SetHeavyCapReader(&fakeLiveConfig{snap: liveconfig.Snapshot{}})
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyCap: intPtr(5)}

	if got := h.liveHeavyCap(context.Background(), cmd, 5); got != 5 {
		t.Fatalf("absent key heavy_cap = %d, want the launch value 5", got)
	}
}

// An operator HOLD set in config.yaml (explicit 0) survives the live read: with no tuned override
// the launch value wins, and 0 is a real value here rather than "unset".
func TestLiveHeavyCap_ConfiguredZeroHoldSurvives(t *testing.T) {
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	h.SetHeavyCapReader(&fakeLiveConfig{snap: liveconfig.Snapshot{}})
	cmd := &RunFleetAutosizerCoordinatorCommand{PlayerID: 1, ContainerID: "c1", HeavyCap: intPtr(0)}

	if got := h.liveHeavyCap(context.Background(), cmd, 0); got != 0 {
		t.Fatalf("configured hold heavy_cap = %d, want 0 (an explicit operator hold)", got)
	}
}

// The resolver's pointer semantics: nil ⇒ documented default, explicit 0 ⇒ a real hold.
func TestResolveHeavyCap_PointerSemantics(t *testing.T) {
	if got := resolveHeavyCap(nil); got != defaultHeavyCap {
		t.Fatalf("nil heavy_cap = %d, want the default %d", got, defaultHeavyCap)
	}
	if got := resolveHeavyCap(intPtr(0)); got != 0 {
		t.Fatalf("explicit 0 heavy_cap = %d, want 0 (a legitimate operator hold, not unset)", got)
	}
	if got := resolveHeavyCap(intPtr(9)); got != 9 {
		t.Fatalf("explicit heavy_cap = %d, want 9", got)
	}
	if got := resolveHeavyCap(intPtr(-3)); got != defaultHeavyCap {
		t.Fatalf("negative heavy_cap = %d, want the default %d (a typo must not read as a hold)", got, defaultHeavyCap)
	}
}
