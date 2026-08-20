package commands

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// These tests cover the bootstrap coordinator's place in the generic runtime tune mechanism
// (sp-r6yq): the coordinator snapshots its OWN persisted config column at each tick start
// (liveconfig.Reader) and a BARE tunable key overlays the launch value on the NEXT tick with
// no restart. Bootstrap's launch keys are the config.yaml-authoritative bootstrap_* family
// (prefixed, cleared+reinjected on every rebuild), so the tune key is deliberately a SEPARATE
// bare key — an untuned bare key is genuinely absent and MUST NOT zero the launch value.

// fakeLiveConfig is a settable liveconfig.Reader: a test flips the snapshot between ticks to
// prove a `tune` write lands on the NEXT reconcile with no restart. Concurrency-guarded
// because the singleton handler shape means many players' ticks could share one reader.
type fakeLiveConfig struct {
	mu   sync.Mutex
	snap liveconfig.Snapshot
	err  error
}

func (f *fakeLiveConfig) Snapshot(_ context.Context, _ string, _ int) (liveconfig.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap, f.err
}

func (f *fakeLiveConfig) set(s liveconfig.Snapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snap = s
}

// BootstrapTunableDefaults is the default-of-record the daemon tune bounds registry reads; its
// KEY SET is the contract for which bare keys resolveBootstrapConfig live-overlays. The cadence
// is the only runtime lever — the cold-start shape is fixed in the coordinator — and it is a
// whole number, so the int-only tune mechanism (liveconfig.PositiveInt) can carry it.
func TestBootstrapTunableDefaults_MirrorsCoordinatorConsts(t *testing.T) {
	got := BootstrapTunableDefaults()
	want := map[string]int{
		"tick_secs":                         defaultBootstrapTickSeconds,                // 45 (sp-lgo3: short cold-start cadence)
		"contract_start_treasury_threshold": int(defaultContractStartTreasuryThreshold), // 500000 (the contract operation's start bar)
	}
	if len(got) != len(want) {
		t.Fatalf("tunable defaults size: got %d want %d (%v)", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("default %q: got %d want %d", k, got[k], v)
		}
	}
}

// The live overlay wins for the tick when the bare tune key is present-and-positive. This is the
// per-tick re-read that makes `tune --operation bootstrap` land next tick with no restart.
func TestBootstrap_ResolveConfig_LiveOverlayOverridesLaunch(t *testing.T) {
	cmd := baseCmd()
	cmd.TickIntervalSecs = 30 // the launch value the overlay must beat

	cfg := resolveBootstrapConfig(cmd, liveconfig.Snapshot{"tick_secs": 120})
	if cfg.Tick != 120*time.Second {
		t.Errorf("tick: got %v want 120s", cfg.Tick)
	}
}

// Seam-inertness: a nil snapshot (reader unwired/unreadable), an empty snapshot, and a snapshot
// carrying only the prefixed launch key (no bare tune key) ALL resolve identically to the
// launch-frozen default. The live-read seam changes nothing until an operator tunes the bare key.
func TestBootstrap_ResolveConfig_NilAndEmptyAndNoiseLive_ByteIdentical(t *testing.T) {
	base := resolveBootstrapConfig(baseCmd(), nil)

	if got := resolveBootstrapConfig(baseCmd(), liveconfig.Snapshot{}); got != base {
		t.Fatalf("empty snapshot must equal nil-live (no overlay): %+v vs %+v", got, base)
	}
	// A snapshot with only the config.yaml-authoritative PREFIXED key + unrelated noise must not
	// overlay the cadence — the tune key is the separate BARE family, which is exactly why a tune
	// survives the launch-config rebuild that clears the prefixed one.
	noise := liveconfig.Snapshot{"bootstrap_tick_secs": 120, "unrelated": 7}
	if got := resolveBootstrapConfig(baseCmd(), noise); got != base {
		t.Fatalf("a snapshot without the bare tune key must be byte-identical to the default: %+v vs %+v", got, base)
	}
	// And the documented default is what a cold, all-zero launch resolves to.
	if base.Tick != defaultBootstrapTickSeconds*time.Second {
		t.Fatalf("nil-live resolve must be the documented default cadence, got %v", base.Tick)
	}
}

// A zeroed/absent bare key falls back to the LAUNCH value, never silently zeroing it — the
// sp-ggk2 only-when-present discipline.
func TestBootstrap_ResolveConfig_AbsentBareKey_KeepsLaunchValue(t *testing.T) {
	cmd := baseCmd()
	cmd.TickIntervalSecs = 90

	for name, live := range map[string]liveconfig.Snapshot{
		"absent": {},
		"zeroed": {"tick_secs": 0},
	} {
		if cfg := resolveBootstrapConfig(cmd, live); cfg.Tick != 90*time.Second {
			t.Errorf("%s bare key must leave the launch cadence at 90s, got %v", name, cfg.Tick)
		}
	}
}

// THE acceptance (coordinator side): a live retune lands on the NEXT tick with NO restart.
// Driven through the two calls reconcileOnce itself makes each tick — take the live snapshot, then
// resolve against it — so the assertion covers the real per-tick re-read, not a stored value. The
// launch command is untouched throughout, proving the coordinator acted on the LIVE column.
func TestBootstrap_LiveRetune_TickSecs_LandsNextTick_NoRestart(t *testing.T) {
	live := &fakeLiveConfig{snap: liveconfig.Snapshot{}} // armed (reader wired) but nothing tuned yet
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetLiveConfigReader(live)
	ctx := ctxWithLogger(&capturingLogger{})
	cmd := baseCmd() // all-zero → the cadence resolves to its default

	tickNow := func() time.Duration {
		return resolveBootstrapConfig(cmd, h.liveConfigSnapshot(ctx, cmd)).Tick
	}

	if got := tickNow(); got != defaultBootstrapTickSeconds*time.Second {
		t.Fatalf("tick1: an untuned column must run at the default cadence, got %v", got)
	}

	// LIVE RETUNE: 45s → 120s. No restart, no rebuild.
	live.set(liveconfig.Snapshot{"tick_secs": 120})

	if got := tickNow(); got != 120*time.Second {
		t.Fatalf("tick2: the live tune must govern the very next tick, got %v", got)
	}
	if cmd.TickIntervalSecs != 0 {
		t.Fatalf("no restart happened: launch cmd.TickIntervalSecs must remain 0, got %d", cmd.TickIntervalSecs)
	}
}

// The contract-start threshold follows the SAME two-family discipline as the cadence: an absent bare
// key keeps the launch value, a present one overlays it for the tick, and an all-zero launch resolves
// to the documented default.
func TestBootstrap_ResolveConfig_ContractStartThreshold_LaunchDefaultAndOverlay(t *testing.T) {
	if got := resolveBootstrapConfig(baseCmd(), nil).ContractStartTreasury; got != defaultContractStartTreasuryThreshold {
		t.Fatalf("an all-zero launch must resolve the documented default, got %d", got)
	}
	cmd := baseCmd()
	cmd.ContractStartTreasuryThreshold = 250_000
	for name, live := range map[string]liveconfig.Snapshot{
		"absent": {},
		"zeroed": {"contract_start_treasury_threshold": 0},
		"noise":  {"bootstrap_contract_start_treasury_threshold": 900_000},
	} {
		if got := resolveBootstrapConfig(cmd, live).ContractStartTreasury; got != 250_000 {
			t.Errorf("%s bare key must leave the launch threshold at 250000, got %d", name, got)
		}
	}
	if got := resolveBootstrapConfig(cmd, liveconfig.Snapshot{"contract_start_treasury_threshold": 900_000}).ContractStartTreasury; got != 900_000 {
		t.Fatalf("the bare tune must beat the launch value, got %d", got)
	}
}

// THE acceptance (coordinator side) for the new knob: a live retune lands on the NEXT tick with NO
// restart, driven through the two calls reconcileOnce itself makes each tick.
func TestBootstrap_LiveRetune_ContractStartThreshold_LandsNextTick_NoRestart(t *testing.T) {
	live := &fakeLiveConfig{snap: liveconfig.Snapshot{}}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetLiveConfigReader(live)
	ctx := ctxWithLogger(&capturingLogger{})
	cmd := baseCmd()

	thresholdNow := func() int64 {
		return resolveBootstrapConfig(cmd, h.liveConfigSnapshot(ctx, cmd)).ContractStartTreasury
	}

	if got := thresholdNow(); got != defaultContractStartTreasuryThreshold {
		t.Fatalf("tick1: an untuned column must run at the default threshold, got %d", got)
	}
	live.set(liveconfig.Snapshot{"contract_start_treasury_threshold": 1_250_000})
	if got := thresholdNow(); got != 1_250_000 {
		t.Fatalf("tick2: the live tune must govern the very next tick, got %d", got)
	}
	if cmd.ContractStartTreasuryThreshold != 0 {
		t.Fatalf("no restart happened: launch cmd.ContractStartTreasuryThreshold must remain 0, got %d", cmd.ContractStartTreasuryThreshold)
	}
}

// A live-config read error (row gone, transient DB gap) falls the tick back to the LAUNCH
// command's value — fail-safe, never a half-applied config. With the reader erroring, the
// tick_secs=120 tune is invisible and the launch cadence governs.
func TestBootstrap_LiveConfigUnreadable_FallsBackToLaunchValues(t *testing.T) {
	live := &fakeLiveConfig{snap: liveconfig.Snapshot{"tick_secs": 120}, err: context.DeadlineExceeded}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetLiveConfigReader(live)
	ctx := ctxWithLogger(&capturingLogger{})
	cmd := baseCmd()
	cmd.TickIntervalSecs = 30

	cfg := resolveBootstrapConfig(cmd, h.liveConfigSnapshot(ctx, cmd))
	if cfg.Tick != 30*time.Second {
		t.Fatalf("an unreadable live config must fall back to the launch cadence 30s, got %v", cfg.Tick)
	}
}
