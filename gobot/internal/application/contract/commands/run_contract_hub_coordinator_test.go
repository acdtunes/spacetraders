package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Fakes for the contract-hub coordinator ports (shared across the suite) ---
//
// These mirror the siting-coordinator fakes: each port is a narrow interface the
// daemon wires to a concrete market/contract/fleet adapter (M-wiring, follow-on)
// and the tests inject deterministic fakes. Every fake asserts on OBSERVABLE
// output (the assigned home hub), never on internal calls.

type fakeHubCandidateSource struct {
	scan       HubScan
	err        error
	calls      int
	lastPlayer int
}

func (f *fakeHubCandidateSource) ScanHubs(ctx context.Context, playerID int) (HubScan, error) {
	f.calls++
	f.lastPlayer = playerID
	return f.scan, f.err
}

type fakeContractDemandSource struct {
	contracts []ContractDemandRecord
	err       error
	calls     int
}

func (f *fakeContractDemandSource) RecentContracts(ctx context.Context, playerID int) ([]ContractDemandRecord, error) {
	f.calls++
	return f.contracts, f.err
}

type fakeHaulerHomeSource struct {
	haulers []HaulerHome
	err     error
	calls   int
}

func (f *fakeHaulerHomeSource) Haulers(ctx context.Context, playerID int) ([]HaulerHome, error) {
	f.calls++
	return f.haulers, f.err
}

type fakeHomeAssigner struct {
	assigned map[string]string // ship symbol -> hub waypoint
	err      error
}

func (f *fakeHomeAssigner) AssignHome(ctx context.Context, playerID int, shipSymbol, hubWaypoint string) error {
	if f.err != nil {
		return f.err
	}
	if f.assigned == nil {
		f.assigned = make(map[string]string)
	}
	f.assigned[shipSymbol] = hubWaypoint
	return nil
}

// newHubTestHandler wires a coordinator from the given ports, filling any nil port
// with an inert fake (the siting newTestHandler idiom).
func newHubTestHandler(
	cand HubCandidateSource,
	demand ContractDemandSource,
	homes HaulerHomeSource,
	assigner HomeAssigner,
) *RunContractHubCoordinatorHandler {
	if cand == nil {
		cand = &fakeHubCandidateSource{}
	}
	if demand == nil {
		demand = &fakeContractDemandSource{}
	}
	if homes == nil {
		homes = &fakeHaulerHomeSource{}
	}
	if assigner == nil {
		assigner = &fakeHomeAssigner{}
	}
	return NewRunContractHubCoordinatorHandler(cand, demand, homes, assigner, nil)
}

// --- Config resolution (RULINGS #5: every knob is a config key with a documented default) ---

func TestResolveContractHubConfig_DefaultsWhenUnset(t *testing.T) {
	// An all-zero command is the "absent config" case: LIVE BY DEFAULT (not disabled)
	// and every knob resolves to its documented protective default.
	cfg := resolveContractHubConfig(&RunContractHubCoordinatorCommand{})

	assert.False(t, cfg.Disabled, "absent config must resolve ACTIVE (Disabled=false) — LIVE BY DEFAULT")
	assert.Equal(t, defaultContractHubTickSeconds*time.Second, cfg.Tick)
	assert.Equal(t, defaultContractHubEWMAHalfLife, cfg.EWMAHalfLife)
	assert.Equal(t, defaultContractHubMaxHaulersPerHub, cfg.MaxHaulersPerHub)
	assert.Equal(t, defaultContractHubBaselineCoverage, cfg.BaselineCoverage)
	assert.Equal(t, defaultContractHubRehomeHysteresis, cfg.RehomeHysteresisMargin)
	assert.Equal(t, defaultContractHubExpectedRemainingContracts, cfg.ExpectedRemainingContracts)
}

func TestResolveContractHubConfig_OverridesRespected(t *testing.T) {
	cmd := &RunContractHubCoordinatorCommand{
		Disabled:                   true,
		DryRun:                     true,
		TickIntervalSecs:           120,
		EWMAHalfLifeContracts:      10,
		MaxHaulersPerHub:           5,
		BaselineCoverage:           2000,
		RehomeHysteresisMargin:     42,
		ExpectedRemainingContracts: 7,
	}
	cfg := resolveContractHubConfig(cmd)

	assert.True(t, cfg.Disabled)
	assert.True(t, cfg.DryRun)
	assert.Equal(t, 120*time.Second, cfg.Tick)
	assert.Equal(t, 10.0, cfg.EWMAHalfLife)
	assert.Equal(t, 5, cfg.MaxHaulersPerHub)
	assert.Equal(t, 2000.0, cfg.BaselineCoverage)
	assert.Equal(t, 42.0, cfg.RehomeHysteresisMargin)
	assert.Equal(t, 7.0, cfg.ExpectedRemainingContracts)
}

// --- reconcile boot-gate ---

func TestReconcileOnce_DisabledGate_DoesNotScan(t *testing.T) {
	cand := &fakeHubCandidateSource{scan: HubScan{Candidates: []HubCandidate{{Waypoint: "X1-AA-C1"}}}}
	assigner := &fakeHomeAssigner{}
	h := newHubTestHandler(cand, nil, nil, assigner)

	res, err := h.reconcileOnce(context.Background(), &RunContractHubCoordinatorCommand{Disabled: true, ContainerID: "c1"})
	require.NoError(t, err)
	assert.Equal(t, 0, cand.calls, "disabled coordinator must not SCAN")
	assert.Equal(t, 0, res.Placed)
	assert.Empty(t, assigner.assigned, "disabled coordinator must assign no homes")
}

// --- FAIL-SAFE: a transient read failure leaves existing homes untouched (acceptance #4) ---

func TestReconcileOnce_ScanErrorLeavesHomesUntouched(t *testing.T) {
	cand := &fakeHubCandidateSource{err: context.DeadlineExceeded}
	homes := &fakeHaulerHomeSource{haulers: []HaulerHome{{ShipSymbol: "H1", Idle: true}}}
	assigner := &fakeHomeAssigner{}
	h := newHubTestHandler(cand, nil, homes, assigner)

	_, err := h.reconcileOnce(context.Background(), &RunContractHubCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	require.Error(t, err, "a market-read failure must surface, not be silently swallowed")
	assert.Empty(t, assigner.assigned, "market-read failure must leave homes untouched (no churn on transient error)")
}

func TestReconcileOnce_DemandErrorLeavesHomesUntouched(t *testing.T) {
	cand := &fakeHubCandidateSource{scan: HubScan{Candidates: []HubCandidate{{Waypoint: "C1"}}}}
	demand := &fakeContractDemandSource{err: context.DeadlineExceeded}
	homes := &fakeHaulerHomeSource{haulers: []HaulerHome{{ShipSymbol: "H1", Idle: true}}}
	assigner := &fakeHomeAssigner{}
	h := newHubTestHandler(cand, demand, homes, assigner)

	_, err := h.reconcileOnce(context.Background(), &RunContractHubCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	require.Error(t, err)
	assert.Empty(t, assigner.assigned, "contract-read failure must leave homes untouched")
}

func TestReconcileOnce_HaulerReadErrorLeavesHomesUntouched(t *testing.T) {
	cand := &fakeHubCandidateSource{scan: HubScan{Candidates: []HubCandidate{{Waypoint: "C1"}}}}
	homes := &fakeHaulerHomeSource{err: context.DeadlineExceeded}
	assigner := &fakeHomeAssigner{}
	h := newHubTestHandler(cand, nil, homes, assigner)

	_, err := h.reconcileOnce(context.Background(), &RunContractHubCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	require.Error(t, err)
	assert.Empty(t, assigner.assigned, "fleet-read failure must leave homes untouched")
}

// --- Acceptance #1: a newly-added light hauler is auto-homed to the computed best-open
// hub off LIVE market+contract data (not a config constant). Proven by SWAPPING the live
// source geometry and confirming the placement follows the data. ---

func TestReconcileOnce_AutoHomesNewHaulerToLiveBestHub(t *testing.T) {
	demand := &fakeContractDemandSource{contracts: []ContractDemandRecord{
		{Goods: []string{"FUEL"}, PaymentOnFulfilled: 1000},
		{Goods: []string{"FUEL"}, PaymentOnFulfilled: 1000},
	}}
	newHauler := []HaulerHome{{ShipSymbol: "H1", HomeWaypoint: "", Idle: true}}

	// Data A: FUEL's cheapest source sits on C_NEAR → the higher-marginal (closer) hub wins.
	scanNear := HubScan{
		Candidates: []HubCandidate{{Waypoint: "C_NEAR", X: 0, Y: 0}, {Waypoint: "C_FAR", X: 50, Y: 0}},
		Sources:    []GoodSource{{Good: "FUEL", Waypoint: "S", X: 0, Y: 0}},
	}
	assignerA := &fakeHomeAssigner{}
	hA := newHubTestHandler(&fakeHubCandidateSource{scan: scanNear}, demand, &fakeHaulerHomeSource{haulers: newHauler}, assignerA)
	_, err := hA.reconcileOnce(context.Background(), &RunContractHubCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	require.NoError(t, err)
	assert.Equal(t, "C_NEAR", assignerA.assigned["H1"], "hauler must home to the hub nearest the live cheapest source")

	// Data B: the SAME good's source moves onto C_FAR (a live price tick). The placement must
	// follow the data to C_FAR — proving the home is computed live, not a config constant.
	scanFar := HubScan{
		Candidates: []HubCandidate{{Waypoint: "C_NEAR", X: 0, Y: 0}, {Waypoint: "C_FAR", X: 50, Y: 0}},
		Sources:    []GoodSource{{Good: "FUEL", Waypoint: "S", X: 50, Y: 0}},
	}
	assignerB := &fakeHomeAssigner{}
	hB := newHubTestHandler(&fakeHubCandidateSource{scan: scanFar}, demand, &fakeHaulerHomeSource{haulers: newHauler}, assignerB)
	_, err = hB.reconcileOnce(context.Background(), &RunContractHubCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	require.NoError(t, err)
	assert.Equal(t, "C_FAR", assignerB.assigned["H1"], "placement must follow the LIVE source, not a fixed hub")
}

// --- Phase-1 scope: already-homed haulers are NEVER re-homed (that is Phase 2). ---

func TestReconcileOnce_DoesNotRehomeAlreadyHomedHauler(t *testing.T) {
	scan := HubScan{
		Candidates: []HubCandidate{{Waypoint: "C1", X: 0, Y: 0}, {Waypoint: "C2", X: 100, Y: 0}},
		Sources:    []GoodSource{{Good: "FUEL", Waypoint: "S", X: 0, Y: 0}},
	}
	demand := &fakeContractDemandSource{contracts: []ContractDemandRecord{{Goods: []string{"FUEL"}, PaymentOnFulfilled: 1000}}}
	// H0 is already homed (to C2) and idle — Phase 1 must leave it alone even though C1 is a
	// better hub for the live demand.
	homes := &fakeHaulerHomeSource{haulers: []HaulerHome{
		{ShipSymbol: "H0", HomeWaypoint: "C2", HomeX: 100, HomeY: 0, Idle: true},
	}}
	assigner := &fakeHomeAssigner{}
	h := newHubTestHandler(&fakeHubCandidateSource{scan: scan}, demand, homes, assigner)

	res, err := h.reconcileOnce(context.Background(), &RunContractHubCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	require.NoError(t, err)
	assert.Equal(t, 0, res.Placed)
	assert.NotContains(t, assigner.assigned, "H0", "an already-homed hauler must never be re-homed in Phase 1")
}

// --- Guardrail: idle-only. A non-idle unhomed hauler is never placed (never strand mid-contract). ---

func TestReconcileOnce_SkipsNonIdleUnhomedHauler(t *testing.T) {
	scan := HubScan{
		Candidates: []HubCandidate{{Waypoint: "C1", X: 0, Y: 0}},
		Sources:    []GoodSource{{Good: "FUEL", Waypoint: "S", X: 0, Y: 0}},
	}
	demand := &fakeContractDemandSource{contracts: []ContractDemandRecord{{Goods: []string{"FUEL"}, PaymentOnFulfilled: 1000}}}
	homes := &fakeHaulerHomeSource{haulers: []HaulerHome{{ShipSymbol: "BUSY", HomeWaypoint: "", Idle: false}}}
	assigner := &fakeHomeAssigner{}
	h := newHubTestHandler(&fakeHubCandidateSource{scan: scan}, demand, homes, assigner)

	res, err := h.reconcileOnce(context.Background(), &RunContractHubCoordinatorCommand{PlayerID: 1, ContainerID: "c1"})
	require.NoError(t, err)
	assert.Equal(t, 0, res.Placed)
	assert.Empty(t, assigner.assigned, "a busy (non-idle) hauler must not be homed mid-work")
}

// --- DryRun evaluates + logs but assigns nothing (watch mode). ---

func TestReconcileOnce_DryRunDoesNotAssign(t *testing.T) {
	scan := HubScan{
		Candidates: []HubCandidate{{Waypoint: "C1", X: 0, Y: 0}},
		Sources:    []GoodSource{{Good: "FUEL", Waypoint: "S", X: 0, Y: 0}},
	}
	demand := &fakeContractDemandSource{contracts: []ContractDemandRecord{{Goods: []string{"FUEL"}, PaymentOnFulfilled: 1000}}}
	homes := &fakeHaulerHomeSource{haulers: []HaulerHome{{ShipSymbol: "H1", Idle: true}}}
	assigner := &fakeHomeAssigner{}
	h := newHubTestHandler(&fakeHubCandidateSource{scan: scan}, demand, homes, assigner)

	res, err := h.reconcileOnce(context.Background(), &RunContractHubCoordinatorCommand{PlayerID: 1, ContainerID: "c1", DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Planned, "dry-run still computes the placement it WOULD make")
	assert.Empty(t, assigner.assigned, "dry-run must not persist any home")
}
