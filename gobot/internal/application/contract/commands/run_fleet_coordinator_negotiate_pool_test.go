package commands

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractServices "github.com/andrescamacho/spacetraders-go/internal/application/contract/services"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// discoverShipPool opts the dedicated-fleet lookup into AdmitUnclaimedInTransit so an unclaimed,
// mid-HOMING hull can compete in SelectClosestShip's route-ETA ranking. But pool.available is
// ALSO consumed by contract NEGOTIATION, and the SpaceTraders negotiate-contract endpoint
// rejects an in-transit ship outright. The tests below pin the fix: the pool is split into
// `available` (the WIDENED set, unchanged, still feeding the "anything to do this pass" gate and
// SelectClosestShip) and `dockable` (a strictly non-in-transit subset), with negotiation reading
// dockable-first via shipPool.negotiationCandidate() - and waiting rather than negotiating at all
// when dockable is empty.

// TestDiscoverShipPool_DockableExcludesUnclaimedInTransitDedicatedMember is the discovery-side
// half: an unclaimed in-transit dedicated member must still be admitted into the WIDENED
// `available` pool (calibration - the widening must actually be armed, or the rest of this
// test proves nothing about the fix), but must NEVER appear in `dockable`.
func TestDiscoverShipPool_DockableExcludesUnclaimedInTransitDedicatedMember(t *testing.T) {
	dockableMember := pinnedHauler(t, "TORWIND-6", dedicatedFleetContract)
	inTransitMember := pinnedHauler(t, "TORWIND-7", dedicatedFleetContract)
	inTransitMember.SetNavStatus(navigation.NavStatusInTransit)
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{dockableMember, inTransitMember}}

	pool, ok := newBaselinePass(repo).discoverShipPool(context.Background())
	if !ok {
		t.Fatal("discoverShipPool must succeed on a healthy repo")
	}
	if !pool.dedicatedFleetActive {
		t.Fatal("a contract-tagged hull must set EXCLUSIVE MODE active")
	}

	// Calibration: the widening must actually be armed here, or the assertion below proves nothing.
	if !containsShipSymbol(pool.available, "TORWIND-7") {
		t.Fatalf("expected the unclaimed in-transit member admitted into the WIDENED pool, got %v", pool.available)
	}

	if containsShipSymbol(pool.dockable, "TORWIND-7") {
		t.Fatalf("the in-transit member must NEVER appear in the dockable pool - a non-selection "+
			"consumer (negotiation) would receive an undockable ship, got %v", pool.dockable)
	}
	if !containsShipSymbol(pool.dockable, "TORWIND-6") {
		t.Fatalf("expected the genuinely idle dedicated member in the dockable pool, got %v", pool.dockable)
	}
}

// TestDiscoverShipPool_AvailableAndDockableDoNotAliasBackingArray: available and dockable are
// each built by calling SelectAvailableShips against the SAME generalShips slice. In the
// non-EXCLUSIVE branch (no dedicated fleet tagged - the common case here, one plain general
// hauler) that function returns append(generalShips, dedicatedIdleShips...); with both dedicated
// lists empty, append with nothing to add hands back generalShips itself UNCHANGED - so available
// and dockable end up as the literal same slice, backed by the literal same array. Mutating one
// through its own index must never be visible through the other.
func TestDiscoverShipPool_AvailableAndDockableDoNotAliasBackingArray(t *testing.T) {
	hauler := baselineHauler(t, "TORWIND-7")
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{hauler}}

	pool, ok := newBaselinePass(repo).discoverShipPool(context.Background())
	if !ok {
		t.Fatal("discoverShipPool must succeed on a healthy repo")
	}
	// Calibration: both must be non-empty and hold the same content, or the mutation below
	// proves nothing about aliasing.
	if len(pool.available) == 0 || len(pool.dockable) == 0 {
		t.Fatalf("expected a non-empty available and dockable pool, got available=%v dockable=%v", pool.available, pool.dockable)
	}

	original := pool.dockable[0]
	pool.available[0] = "MUTATED-SENTINEL"
	if pool.dockable[0] != original {
		t.Fatalf("available and dockable share a backing array: mutating available[0] changed dockable[0] from %q to %q",
			original, pool.dockable[0])
	}
}

// TestShipPool_NegotiationCandidatePrefersDockableOverWidened is the consumption-side half: the
// negotiate call site must pick from `dockable` first, even though `available[0]` (arbitrary
// claim order) could just as easily be the in-transit member.
func TestShipPool_NegotiationCandidatePrefersDockableOverWidened(t *testing.T) {
	pool := shipPool{
		available: []string{"TORWIND-7", "TORWIND-6"}, // widened: index 0 is the in-transit member
		dockable:  []string{"TORWIND-6"},
	}

	if got := pool.negotiationCandidate(); got != "TORWIND-6" {
		t.Fatalf("expected the dockable member TORWIND-6, got %q", got)
	}
}

// TestShipPool_NegotiationCandidateReturnsEmptyWhenEveryMemberInTransit is the corrected
// corner: when every candidate is in transit, negotiationCandidate must return no candidate at
// all rather than fall back to the widened available[0]. A negotiate attempt on an in-transit
// hull is rejected by the API (4214), and the coordinator's error handling for that treats it as
// a cache-desync signature and pays for a fleet-wide resync before retrying - strictly worse than
// today's main, which has nothing to attempt in this state and waits quietly instead. The caller
// (run_fleet_coordinator.go) reads an empty candidate as "wait for one to arrive," not "give up."
func TestShipPool_NegotiationCandidateReturnsEmptyWhenEveryMemberInTransit(t *testing.T) {
	pool := shipPool{
		available: []string{"TORWIND-7"},
		dockable:  nil,
	}

	if got := pool.negotiationCandidate(); got != "" {
		t.Fatalf("expected no candidate (caller must wait) when every pool member is in transit, got %q", got)
	}
}

// TestShipPool_NegotiationCandidateEmptyPoolReturnsEmpty documents the vacuous case: an empty
// pool (the "no ships available, waiting" branch already guards this in production) must not
// panic - it returns "", not index out of range.
func TestShipPool_NegotiationCandidateEmptyPoolReturnsEmpty(t *testing.T) {
	pool := shipPool{}

	if got := pool.negotiationCandidate(); got != "" {
		t.Fatalf("expected an empty pool to yield \"\", got %q", got)
	}
}

// negotiateCallRecordingContractRepo records whether FindActiveContracts was ever called - the
// very first thing NegotiateContract does - so a test can assert negotiation was never attempted
// at all, not merely that some later step didn't run.
type negotiateCallRecordingContractRepo struct {
	domainContract.ContractRepository
	called bool
}

func (r *negotiateCallRecordingContractRepo) FindActiveContracts(_ context.Context, _ int) ([]*domainContract.Contract, error) {
	r.called = true
	return nil, nil
}

// actionSignalLogger streams every logged action over a channel, so a test can wait,
// synchronized, for one specific action to fire from a concurrently-running coordinator goroutine
// without racing a shared slice the way a plain capturing logger would.
type actionSignalLogger struct {
	actions chan string
}

func newActionSignalLogger() *actionSignalLogger {
	return &actionSignalLogger{actions: make(chan string, 64)}
}

func (l *actionSignalLogger) Log(_, _ string, fields map[string]interface{}) {
	action, _ := fields["action"].(string)
	l.actions <- action
}

func (l *actionSignalLogger) waitForAction(t *testing.T, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case action := <-l.actions:
			if action == want {
				return
			}
		case <-deadline:
			t.Fatalf("timed out after %s waiting for action %q to be logged", timeout, want)
		}
	}
}

// TestFleetCoordinator_AllDedicatedMembersInTransit_WaitsInsteadOfNegotiating drives the real
// Handle() loop with a single dedicated hull that is unclaimed but IN_TRANSIT - the pool widening
// admits it into `available` (so the pass clears the ":415 no ships available" gate) but it can
// never appear in `dockable`. The coordinator must wait for it to arrive rather than negotiate
// with it: a negotiate call on an in-transit ship is rejected by the API and drives the fleet-wide
// resync + 30s ERROR retry loop the whole fix exists to prevent.
func TestFleetCoordinator_AllDedicatedMembersInTransit_WaitsInsteadOfNegotiating(t *testing.T) {
	inTransit := pinnedHauler(t, "TORWIND-7", dedicatedFleetContract)
	inTransit.SetNavStatus(navigation.NavStatusInTransit)
	repo := &singleHullFakeShipRepo{ships: []*navigation.Ship{inTransit}}
	daemonClient := newSettleWindowDaemonClient()
	containerRepo := &reclaimFakeContainerRepo{}
	workerCh := make(chan navigation.WorkerCompletedEvent)
	mockClock := &shared.MockClock{CurrentTime: time.Now()}
	negotiateRepo := &negotiateCallRecordingContractRepo{}
	logger := newActionSignalLogger()

	handler := &RunFleetCoordinatorHandler{
		workerLifecycleManager: contractServices.NewWorkerLifecycleManager(daemonClient, containerRepo, repo),
		contractMarketService:  contractServices.NewContractMarketService(nil, negotiateRepo),
		shipRepo:               repo,
		daemonClient:           daemonClient,
		graphProvider:          &placementStubGraphProvider{graph: settleWindowGraph(t)},
		clock:                  mockClock,
		eventSubscriber:        &reclaimFakeSubscriber{workerCompleted: workerCh},
	}

	ctx, cancel := context.WithCancel(common.WithLogger(context.Background(), logger))
	done := make(chan struct{})
	go func() {
		_, _ = handler.Handle(ctx, contractSpawnCommand())
		close(done)
	}()

	// The coordinator must log that it is waiting for a dockable ship instead of attempting a
	// doomed negotiate call - wait for that specific, synchronized signal rather than a raw sleep.
	logger.waitForAction(t, "await_dockable_for_negotiate", 2*time.Second)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Handle did not return after ctx cancellation")
	}

	if negotiateRepo.called {
		t.Fatal("NegotiateContract must never be attempted while every dedicated member is in transit - " +
			"it would be rejected by the API (4214) and trigger a spurious fleet-wide resync")
	}
}
