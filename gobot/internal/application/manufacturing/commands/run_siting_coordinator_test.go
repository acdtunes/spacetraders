package commands

import (
	"context"
	"testing"
	"time"
)

// --- Fakes for the siting coordinator ports (shared across M1–M6 tests) ---

type fakeSitingScanner struct {
	candidates []SitingCandidate
	err        error
	calls      int
	lastParams SitingScanParams
	lastPlayer int
}

func (f *fakeSitingScanner) ScanCandidates(ctx context.Context, playerID int, params SitingScanParams) ([]SitingCandidate, error) {
	f.calls++
	f.lastParams = params
	f.lastPlayer = playerID
	return f.candidates, f.err
}

type fakeChainProjector struct {
	// byKey maps "good@system" → projection; missing keys default to Proceed=true, PL=0.
	byKey map[string]ChainProjection
	err   error
	calls int
}

func (f *fakeChainProjector) Project(ctx context.Context, good, system string, playerID int) (ChainProjection, error) {
	f.calls++
	if f.err != nil {
		return ChainProjection{}, f.err
	}
	if p, ok := f.byKey[good+"@"+system]; ok {
		return p, nil
	}
	return ChainProjection{ProjectedPL: 0, Proceed: true}, nil
}

type fakeChainController struct {
	running    []RunningChain
	launched   []string // "good@system"
	retired    []string // factoryID
	launchErr  error
	retireErr  error
	runningErr error
	nextID     int
}

func (f *fakeChainController) RunningChains(ctx context.Context, playerID int) ([]RunningChain, error) {
	return f.running, f.runningErr
}

func (f *fakeChainController) Launch(ctx context.Context, good, system string, playerID int) (string, error) {
	if f.launchErr != nil {
		return "", f.launchErr
	}
	f.launched = append(f.launched, good+"@"+system)
	f.nextID++
	return "container-" + good, nil
}

func (f *fakeChainController) Retire(ctx context.Context, factoryID string, playerID int) error {
	if f.retireErr != nil {
		return f.retireErr
	}
	f.retired = append(f.retired, factoryID)
	return nil
}

type fakeAlignmentProvider struct {
	byKey map[string]float64
	err   error
}

func (f *fakeAlignmentProvider) Alignment(ctx context.Context, playerID int, good, system string) (float64, error) {
	if f.err != nil {
		return 0, f.err
	}
	if v, ok := f.byKey[good+"@"+system]; ok {
		return v, nil
	}
	return 0, nil // no entry → neutral signal (score falls back to branchPL alone)
}

type fakeWorkerCounter struct {
	count int
	err   error
}

func (f *fakeWorkerCounter) CountWorkers(ctx context.Context, playerID int) (int, error) {
	return f.count, f.err
}

type fakeWorkerReachabilityProvider struct {
	// bySystem maps a candidate system → staffing-reachability signal ∈ [0,1] (1 = an idle
	// worker is in-system, 0 = no worker can reach it). A missing system defaults to 1
	// (fully reachable / neutral), mirroring the production nil/error fallback.
	bySystem map[string]float64
	err      error
}

func (f *fakeWorkerReachabilityProvider) Reachability(ctx context.Context, playerID int, system string) (float64, error) {
	if f.err != nil {
		return 0, f.err
	}
	if v, ok := f.bySystem[system]; ok {
		return v, nil
	}
	return 1, nil // no entry → fully reachable (neutral, no penalty)
}

type fakeScoutDemandEmitter struct {
	emitted    []string // systems
	suppressed map[string]bool
	err        error
}

func (f *fakeScoutDemandEmitter) EmitScoutDemand(ctx context.Context, playerID int, system string, cooldown time.Duration, payload string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if f.suppressed[system] {
		return false, nil
	}
	f.emitted = append(f.emitted, system)
	return true, nil
}

func newTestHandler(scanner SitingScanner, projector ChainProjector, controller ChainController) *RunSitingCoordinatorHandler {
	if scanner == nil {
		scanner = &fakeSitingScanner{}
	}
	if projector == nil {
		projector = &fakeChainProjector{}
	}
	if controller == nil {
		controller = &fakeChainController{}
	}
	return NewRunSitingCoordinatorHandler(scanner, projector, controller, nil)
}

// --- M1: config resolution ---

func TestResolveSitingConfig_DefaultsWhenUnset(t *testing.T) {
	// An all-zero command is the "absent config" case: LIVE BY DEFAULT (not disabled) and
	// every knob resolves to its documented protective default.
	cfg := resolveSitingConfig(&RunSitingCoordinatorCommand{})

	if cfg.Disabled {
		t.Fatal("absent config must resolve ACTIVE (Disabled=false) — LIVE BY DEFAULT")
	}
	if cfg.Tick != defaultSitingTickSeconds*time.Second {
		t.Errorf("Tick = %s, want %ds", cfg.Tick, defaultSitingTickSeconds)
	}
	if cfg.WorkersPerChain != defaultSitingWorkersPerChain {
		t.Errorf("WorkersPerChain = %v, want %v", cfg.WorkersPerChain, defaultSitingWorkersPerChain)
	}
	if cfg.FreshnessMax != defaultSitingFreshnessMaxSecs*time.Second {
		t.Errorf("FreshnessMax = %s, want %ds", cfg.FreshnessMax, defaultSitingFreshnessMaxSecs)
	}
	if cfg.EmitStaleness != defaultSitingEmitStalenessSecs*time.Second {
		t.Errorf("EmitStaleness = %s, want %ds", cfg.EmitStaleness, defaultSitingEmitStalenessSecs)
	}
	if cfg.WeightAlignment != defaultSitingWeightAlignment {
		t.Errorf("WeightAlignment = %v, want %v", cfg.WeightAlignment, defaultSitingWeightAlignment)
	}
	if cfg.WeightCompetition != defaultSitingWeightCompetition {
		t.Errorf("WeightCompetition = %v, want %v", cfg.WeightCompetition, defaultSitingWeightCompetition)
	}
	if cfg.WeightStaleness != defaultSitingWeightStaleness {
		t.Errorf("WeightStaleness = %v, want %v", cfg.WeightStaleness, defaultSitingWeightStaleness)
	}
	if cfg.MaxChainsPerSystem != defaultSitingMaxChainsSystem {
		t.Errorf("MaxChainsPerSystem = %d, want %d", cfg.MaxChainsPerSystem, defaultSitingMaxChainsSystem)
	}
	if cfg.MaxChainsPerInputMarket != defaultSitingMaxChainsInput {
		t.Errorf("MaxChainsPerInputMarket = %d, want %d", cfg.MaxChainsPerInputMarket, defaultSitingMaxChainsInput)
	}
	if cfg.RetireHysteresisTicks != defaultSitingRetireHysteresis {
		t.Errorf("RetireHysteresisTicks = %d, want %d", cfg.RetireHysteresisTicks, defaultSitingRetireHysteresis)
	}
	if cfg.EffectSelfcheckTicks != defaultSitingEffectSelfcheck {
		t.Errorf("EffectSelfcheckTicks = %d, want %d", cfg.EffectSelfcheckTicks, defaultSitingEffectSelfcheck)
	}
	if cfg.ScoutCooldown != defaultSitingScoutCooldownSecs*time.Second {
		t.Errorf("ScoutCooldown = %s, want %ds", cfg.ScoutCooldown, defaultSitingScoutCooldownSecs)
	}
}

func TestResolveSitingConfig_OverridesRespected(t *testing.T) {
	cmd := &RunSitingCoordinatorCommand{
		Disabled:                true,
		DryRun:                  true,
		TickIntervalSecs:        120,
		TopK:                    7,
		WorkersPerChain:         4.0,
		FreshnessMaxSecs:        600,
		EmitStalenessSecs:       300,
		WeightTourAlignment:     2.5,
		WeightInputCompetition:  3.0,
		WeightStaleness:         0.5,
		MaxChainsPerSystem:      5,
		MaxChainsPerInputMarket: 4,
		RetireHysteresisTicks:   6,
		EffectSelfcheckTicks:    9,
		ScoutDemandCooldownSecs: 60,
	}
	cfg := resolveSitingConfig(cmd)

	if !cfg.Disabled || !cfg.DryRun {
		t.Error("Disabled/DryRun overrides not respected")
	}
	if cfg.Tick != 120*time.Second || cfg.TopK != 7 || cfg.WorkersPerChain != 4.0 {
		t.Errorf("tick/topk/workers overrides not respected: %+v", cfg)
	}
	if cfg.FreshnessMax != 600*time.Second || cfg.EmitStaleness != 300*time.Second {
		t.Errorf("freshness/emit overrides not respected: %+v", cfg)
	}
	if cfg.WeightAlignment != 2.5 || cfg.WeightCompetition != 3.0 || cfg.WeightStaleness != 0.5 {
		t.Errorf("weight overrides not respected: %+v", cfg)
	}
	if cfg.MaxChainsPerSystem != 5 || cfg.MaxChainsPerInputMarket != 4 {
		t.Errorf("cap overrides not respected: %+v", cfg)
	}
	if cfg.RetireHysteresisTicks != 6 || cfg.EffectSelfcheckTicks != 9 || cfg.ScoutCooldown != 60*time.Second {
		t.Errorf("hysteresis/selfcheck/cooldown overrides not respected: %+v", cfg)
	}
}

// --- M1: reconcile boot-gate ---

func TestReconcileOnce_DisabledGate_DoesNotScan(t *testing.T) {
	scanner := &fakeSitingScanner{candidates: []SitingCandidate{{Good: "IRON", System: "X1-AA"}}}
	h := newTestHandler(scanner, nil, nil)

	res, err := h.reconcileOnce(context.Background(), &RunSitingCoordinatorCommand{Disabled: true, ContainerID: "c1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scanner.calls != 0 {
		t.Errorf("disabled coordinator must not SCAN; scanner called %d times", scanner.calls)
	}
	if res.Candidates != 0 || res.Actions() != 0 {
		t.Errorf("disabled coordinator must report zero effect, got %+v", res)
	}
}

func TestReconcileOnce_EnabledScansWithFreshnessGate(t *testing.T) {
	scanner := &fakeSitingScanner{candidates: []SitingCandidate{{Good: "IRON", System: "X1-AA"}}}
	h := newTestHandler(scanner, nil, nil)

	// All-zero knobs = absent config = ACTIVE. Proves absent→ACTIVE end to end.
	res, err := h.reconcileOnce(context.Background(), &RunSitingCoordinatorCommand{PlayerID: 42, ContainerID: "c1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scanner.calls != 1 {
		t.Fatalf("enabled coordinator must SCAN once; scanner called %d times", scanner.calls)
	}
	if scanner.lastPlayer != 42 {
		t.Errorf("scanner playerID = %d, want 42", scanner.lastPlayer)
	}
	if scanner.lastParams.FreshnessMaxSecs != float64(defaultSitingFreshnessMaxSecs) {
		t.Errorf("scanner freshness = %v, want default %d", scanner.lastParams.FreshnessMaxSecs, defaultSitingFreshnessMaxSecs)
	}
	if res.Candidates != 1 {
		t.Errorf("expected 1 candidate reported, got %d", res.Candidates)
	}
}

func TestReconcileOnce_ScanErrorPropagates(t *testing.T) {
	scanner := &fakeSitingScanner{err: context.DeadlineExceeded}
	h := newTestHandler(scanner, nil, nil)

	if _, err := h.reconcileOnce(context.Background(), &RunSitingCoordinatorCommand{ContainerID: "c1"}); err == nil {
		t.Fatal("expected scan error to propagate")
	}
}
