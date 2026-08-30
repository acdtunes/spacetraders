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
// KEY SET is the contract for which bare keys resolveBootstrapConfig live-overlays. Every entry is a
// whole number, so the int-only tune mechanism (liveconfig.PositiveInt) can carry it.
func TestBootstrapTunableDefaults_MirrorsCoordinatorConsts(t *testing.T) {
	got := BootstrapTunableDefaults()
	want := map[string]int{
		"tick_secs":                         defaultBootstrapTickSeconds,                // 45 (sp-lgo3: short cold-start cadence)
		"contract_start_treasury_threshold": int(defaultContractStartTreasuryThreshold), // 500000 (the contract operation's start bar)
		"hauler_target":                     defaultHaulerTarget,                        // the cold-start contract-hull ramp cap
		"gate_worker_target":                defaultGateWorkerTarget,                    // the GATE construction workforce
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

// --- the two FLEET-SIZE bounds (sp-58pmg): config.yaml key + live tune, same two-family discipline ---

// Both targets follow the contract-start threshold's shape exactly: an absent or zeroed bare key keeps
// the LAUNCH value (which is where a config.yaml [bootstrap] setting arrives), a present one overlays it
// for the tick, and an all-zero launch with no live column resolves the documented default. The `noise`
// row is the load-bearing one: the prefixed bootstrap_* launch keys are cleared and re-injected from
// config.yaml on every rebuild, so a tune MUST be the separate bare key to survive a daemon bounce.
func TestBootstrap_ResolveConfig_FleetTargets_LaunchDefaultAndOverlay(t *testing.T) {
	if got := resolveBootstrapConfig(baseCmd(), nil); got.HaulerTarget != defaultHaulerTarget || got.GateWorkerTarget != defaultGateWorkerTarget {
		t.Fatalf("an all-zero launch must resolve the documented defaults (%d/%d), got %d/%d",
			defaultHaulerTarget, defaultGateWorkerTarget, got.HaulerTarget, got.GateWorkerTarget)
	}

	cmd := baseCmd()
	cmd.HaulerTarget = 6     // as if config.yaml [bootstrap].hauler_target: 6
	cmd.GateWorkerTarget = 5 // as if config.yaml [bootstrap].gate_worker_target: 5
	for name, live := range map[string]liveconfig.Snapshot{
		"absent": {},
		"zeroed": {"hauler_target": 0, "gate_worker_target": 0},
		"noise":  {"bootstrap_hauler_target": 2, "bootstrap_gate_worker_target": 9},
	} {
		got := resolveBootstrapConfig(cmd, live)
		if got.HaulerTarget != 6 || got.GateWorkerTarget != 5 {
			t.Errorf("%s bare keys must leave the launch targets at 6/5, got %d/%d", name, got.HaulerTarget, got.GateWorkerTarget)
		}
	}

	got := resolveBootstrapConfig(cmd, liveconfig.Snapshot{"hauler_target": 3, "gate_worker_target": 2})
	if got.HaulerTarget != 3 || got.GateWorkerTarget != 2 {
		t.Fatalf("the bare tunes must beat the launch values, got %d/%d", got.HaulerTarget, got.GateWorkerTarget)
	}
}

// THE ACCEPTANCE for this bead: the two orders that each cost an edit/compile/test/restart cycle on
// 2026-08-30 — cut the gate workforce, then put a feeder back — are now one knob write each, landing on
// the NEXT tick. Driven through the two calls reconcileOnce itself makes (snapshot, then resolve), and
// the launch command is untouched throughout, so nothing here is a restart in disguise.
func TestBootstrap_LiveRetune_FleetTargets_LandNextTick_NoRestart(t *testing.T) {
	live := &fakeLiveConfig{snap: liveconfig.Snapshot{}} // armed, nothing tuned yet
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetLiveConfigReader(live)
	ctx := ctxWithLogger(&capturingLogger{})
	cmd := baseCmd()

	cfgNow := func() bootstrapRunConfig {
		return resolveBootstrapConfig(cmd, h.liveConfigSnapshot(ctx, cmd))
	}

	if got := cfgNow(); got.GateWorkerTarget != defaultGateWorkerTarget || got.HaulerTarget != defaultHaulerTarget {
		t.Fatalf("tick1: an untuned column must run the documented defaults, got %d/%d", got.HaulerTarget, got.GateWorkerTarget)
	}

	// The 4 -> 2 cut, as a knob write.
	live.set(liveconfig.Snapshot{"gate_worker_target": 2})
	if got := cfgNow().GateWorkerTarget; got != 2 {
		t.Fatalf("tick2: the cut must govern the very next tick, got %d", got)
	}
	// ...and the 2 -> 3 restore an hour later, the same way.
	live.set(liveconfig.Snapshot{"gate_worker_target": 3, "hauler_target": 5})
	got := cfgNow()
	if got.GateWorkerTarget != 3 || got.HaulerTarget != 5 {
		t.Fatalf("tick3: the restore must govern the next tick, got %d/%d", got.HaulerTarget, got.GateWorkerTarget)
	}

	// REVERT: deleting the keys (what `tune <key> 0` does to the column) puts the defaults back.
	live.set(liveconfig.Snapshot{})
	if got := cfgNow(); got.GateWorkerTarget != defaultGateWorkerTarget || got.HaulerTarget != defaultHaulerTarget {
		t.Fatalf("revert: a cleared column must fall back to the documented defaults, got %d/%d", got.HaulerTarget, got.GateWorkerTarget)
	}

	if cmd.GateWorkerTarget != 0 || cmd.HaulerTarget != 0 {
		t.Fatalf("no restart happened: the launch cmd targets must remain 0, got %d/%d", cmd.HaulerTarget, cmd.GateWorkerTarget)
	}
}

// A retune DOWN sheds the idle overage and buys nothing; a retune UP stages exactly one buy per tick and
// releases nothing. Neither direction can strand a hull mid-task — the surplus is drawn from idle hulls
// only — and the buy and release paths stay mutually exclusive across the whole range.
func TestBootstrap_LiveRetune_GateWorkerTarget_ShedsDownStagesUp(t *testing.T) {
	obs := Observation{
		Haulers:     nHaulers(4),
		GateWorkers: 4,
		GateWorkerHulls: []GateWorkerSnapshot{
			{Symbol: "M1", Idle: true}, {Symbol: "M2", Idle: false}, // M2 is mid-construction
			{Symbol: "M3", Idle: true}, {Symbol: "M4", Idle: true},
		},
	}

	down := planGateWorkers(obs, 2)
	if down.Buy != 0 {
		t.Fatalf("a tune DOWN must never buy, got %d", down.Buy)
	}
	if want := []string{"M1", "M3"}; !equalStrings(down.SurplusToUndedicate, want) {
		t.Fatalf("a tune DOWN sheds the IDLE overage lowest-symbol first: got %v want %v", down.SurplusToUndedicate, want)
	}

	up := planGateWorkers(obs, 6)
	if up.Buy != 1 {
		t.Fatalf("a tune UP stages exactly one buy per tick, got %d", up.Buy)
	}
	if len(up.SurplusToUndedicate) != 0 {
		t.Fatalf("a tune UP must release nothing, got %v", up.SurplusToUndedicate)
	}

	at := planGateWorkers(obs, 4)
	if at.Buy != 0 || len(at.SurplusToUndedicate) != 0 {
		t.Fatalf("at the target the plan is inert, got buy=%d release=%v", at.Buy, at.SurplusToUndedicate)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
