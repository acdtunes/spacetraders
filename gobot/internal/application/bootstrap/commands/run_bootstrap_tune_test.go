package commands

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/liveconfig"
)

// These tests cover the bootstrap coordinator's migration into the generic runtime tune
// mechanism (sp-r6yq): the coordinator snapshots its OWN persisted config column at each
// tick start (liveconfig.Reader) and a BARE tunable key overlays the launch value on the
// NEXT tick with no restart. Bootstrap's launch keys are the config.yaml-authoritative
// bootstrap_* family (prefixed, cleared+reinjected on every rebuild), so the tune keys are
// deliberately SEPARATE bare keys — an untuned bare key is genuinely absent and MUST NOT
// zero the launch value (byte-identical when nothing is tuned). The int-only registry
// expresses income_bar as whole credits.

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

// BootstrapTunableDefaults is the defaults-of-record the daemon tune bounds registry reads;
// its KEY SET is the contract for which bare keys resolveBootstrapConfig live-overlays. Every
// value is a whole number (income_bar is whole credits), so the int-only tune mechanism
// (liveconfig.PositiveInt) can carry every bootstrap knob.
func TestBootstrapTunableDefaults_MirrorsCoordinatorConsts(t *testing.T) {
	got := BootstrapTunableDefaults()
	want := map[string]int{
		"probe_target":                  defaultProbeTarget,            // 3
		"hauler_target":                 defaultHaulerTarget,           // 4
		"income_bar":                    10000,                         // defaultIncomeBar 10000.0 → 10000 credits
		"gate_worker_target":            defaultGateWorkerTarget,       // 6
		"tick_secs":                     defaultBootstrapTickSeconds,   // 45 (sp-lgo3: short cold-start cadence)
		"gate_min_haulers":              defaultGateMinHaulers,         // 2 — sp-gm7r escape-hatch starved-earner floor (GATE ENTRY uses the scaler target)
		"gate_surplus_floor":            int(defaultGateSurplusFloor),  // 500000 — sp-gm7r GATE-entry treasury surplus war chest
		"gate_reentry_construction_pct": 5,                             // defaultGateReentryConstructionPct 5.0 → 5 percent
		"gate_reentry_streak_ticks":     defaultGateReentryStreakTicks, // 3 — sp-gm7r escape-hatch anti-thrash streak
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

// The live overlay wins for the tick when a bare tune key is present-and-positive, with
// income_bar reading as whole credits. This is the per-tick re-read that makes
// `tune --operation bootstrap` land next tick with no restart.
func TestBootstrap_ResolveConfig_LiveOverlayOverridesLaunch(t *testing.T) {
	live := liveconfig.Snapshot{
		"probe_target":       5,
		"hauler_target":      2,
		"income_bar":         20000,
		"gate_worker_target": 8,
		"tick_secs":          120,
	}
	cfg := resolveBootstrapConfig(baseCmd(), live)
	if cfg.ProbeTarget != 5 {
		t.Errorf("probe_target: got %d want 5", cfg.ProbeTarget)
	}
	if cfg.HaulerTarget != 2 {
		t.Errorf("hauler_target: got %d want 2", cfg.HaulerTarget)
	}
	if cfg.IncomeBar != 20000 {
		t.Errorf("income_bar: got %v want 20000", cfg.IncomeBar)
	}
	if cfg.GateWorkerTarget != 8 {
		t.Errorf("gate_worker_target: got %d want 8", cfg.GateWorkerTarget)
	}
	if cfg.Tick != 120*time.Second {
		t.Errorf("tick: got %v want 120s", cfg.Tick)
	}
}

// Seam-inertness: a nil snapshot (reader unwired/unreadable), an empty snapshot, and a snapshot
// carrying only the prefixed launch keys (no bare tune key) ALL resolve identically to the
// launch-frozen defaults. The live-read seam changes nothing until an operator tunes a bare key.
func TestBootstrap_ResolveConfig_NilAndEmptyAndNoiseLive_ByteIdentical(t *testing.T) {
	base := resolveBootstrapConfig(baseCmd(), nil)

	if got := resolveBootstrapConfig(baseCmd(), liveconfig.Snapshot{}); got != base {
		t.Fatalf("empty snapshot must equal nil-live (no overlay): %+v vs %+v", got, base)
	}
	// A snapshot with only the config.yaml-authoritative prefixed keys + unrelated noise must
	// not overlay any tunable knob (the tune keys are the separate BARE family).
	noise := liveconfig.Snapshot{"bootstrap_probe_target": 3, "bootstrap_hauler_target": 4, "unrelated": 7}
	if got := resolveBootstrapConfig(baseCmd(), noise); got != base {
		t.Fatalf("a snapshot without bare tune keys must be byte-identical to defaults: %+v vs %+v", got, base)
	}
	// And the documented defaults are what a cold, all-zero launch resolves to.
	if base.ProbeTarget != defaultProbeTarget ||
		base.IncomeBar != defaultIncomeBar ||
		base.HaulerTarget != defaultHaulerTarget || base.GateWorkerTarget != defaultGateWorkerTarget ||
		base.Tick != defaultBootstrapTickSeconds*time.Second {
		t.Fatalf("nil-live resolve must be the documented defaults, got %+v", base)
	}
}

// A zeroed/absent bare key falls back to the LAUNCH value (only-when-present overlay), never
// silently zeroing a knob — the sp-ggk2 discipline. A partial snapshot (one knob tuned)
// overlays only that knob and leaves the rest at their launch/default values.
func TestBootstrap_ResolveConfig_PartialOverlay_LeavesOthersAtLaunch(t *testing.T) {
	cfg := resolveBootstrapConfig(baseCmd(), liveconfig.Snapshot{"probe_target": 9})
	if cfg.ProbeTarget != 9 {
		t.Fatalf("probe_target must overlay to 9, got %d", cfg.ProbeTarget)
	}
	if cfg.HaulerTarget != defaultHaulerTarget || cfg.GateWorkerTarget != defaultGateWorkerTarget {
		t.Fatalf("untuned knobs must stay at their launch/default values, got %+v", cfg)
	}
}

// THE sp-r6yq acceptance (coordinator side): a live retune of probe_target lands on the NEXT
// reconcile tick with NO restart. Tick 1 at the launch target 3 (with 3 probes) buys nothing;
// after a bare-key tune to 5 the very next tick's buy gate opens and the probe buy fires — and
// the launch command is untouched, proving the coordinator acted on the LIVE column.
func TestBootstrap_LiveRetune_ProbeTarget_LandsNextTick_NoRestart(t *testing.T) {
	obs := Observation{
		HomeSystem: "X1-HQ", ProbeCount: 3, ProbesScouting: 3, HasIdlePurchaser: true,
		MarketsTotal: 10, MarketsCovered: 0, Treasury: 1_000_000, Readable: true,
	}
	acq := &fakeAcquirer{price: 40000, yard: "X1-HQ-YARD", readable: true}
	live := &fakeLiveConfig{snap: liveconfig.Snapshot{}} // armed (reader wired) but nothing tuned yet
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(acq)
	h.SetScoutPostDeclarer(&fakeDeclarer{})
	h.SetLiveConfigReader(live)

	cmd := baseCmd() // all-zero → probe_target resolves to default 3

	res1, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
	if err != nil {
		t.Fatalf("tick1: %v", err)
	}
	if res1.Purchased != 0 || acq.buys != 0 {
		t.Fatalf("tick1: 3 probes at target 3 must not buy (purchased=%d buys=%d)", res1.Purchased, acq.buys)
	}

	// LIVE RETUNE: probe_target 3 → 5. No restart, no rebuild.
	live.set(liveconfig.Snapshot{"probe_target": 5})

	res2, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), cmd)
	if err != nil {
		t.Fatalf("tick2: %v", err)
	}
	// buy-to-target (sp-hh0h): 3 probes, live target 5 → the 2-probe remainder buys this tick.
	if res2.Purchased != 2 || acq.buys != 2 {
		t.Fatalf("tick2: live probe_target=5 must open the buy gate to target (purchased=%d buys=%d)", res2.Purchased, acq.buys)
	}

	if cmd.ProbeTarget != 0 {
		t.Fatalf("no restart happened: launch cmd.ProbeTarget must remain 0, got %d", cmd.ProbeTarget)
	}
}

// A live-config read error (row gone, transient DB gap) falls the tick back to the LAUNCH
// command's values — fail-safe, never a half-applied config. With the reader erroring, the
// probe_target=5 tune is invisible and the launch target 3 governs (no buy at 3 probes).
func TestBootstrap_LiveConfigUnreadable_FallsBackToLaunchValues(t *testing.T) {
	obs := Observation{
		HomeSystem: "X1-HQ", ProbeCount: 3, ProbesScouting: 3, HasIdlePurchaser: true,
		MarketsTotal: 10, MarketsCovered: 0, Treasury: 1_000_000, Readable: true,
	}
	acq := &fakeAcquirer{price: 40000, yard: "X1-HQ-YARD", readable: true}
	live := &fakeLiveConfig{snap: liveconfig.Snapshot{"probe_target": 5}, err: context.DeadlineExceeded}
	h := NewRunBootstrapCoordinatorHandler(nil)
	h.SetShipRefresher(&fakeRefresher{})
	h.SetWorldObserver(&fakeObserver{obs: obs})
	h.SetProbeAcquirer(acq)
	h.SetScoutPostDeclarer(&fakeDeclarer{})
	h.SetLiveConfigReader(live)

	res, err := h.reconcileOnce(ctxWithLogger(&capturingLogger{}), baseCmd())
	if err != nil {
		t.Fatalf("reconcileOnce: %v", err)
	}
	if res.Purchased != 0 || acq.buys != 0 {
		t.Fatalf("an unreadable live config must fall back to the launch target 3 (no buy), got purchased=%d buys=%d", res.Purchased, acq.buys)
	}
}
