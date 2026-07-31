package navigation

// jump_ship_stranded_container_test.go — sp-rqhzh.
//
// The falsifier for the stranded-jump-container wedge is NOT "a jump succeeds": every
// pre-existing jump test already asserts that, and every one of them passes with the fix
// deleted. It is that a hull WHICH ALREADY OWNS A STRANDED CONTAINER ROW can jump. So each
// test here pre-seeds that row, and asserts it is present at exactly the read the new code
// performs before trusting the fixture to mean anything.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/metrics"
	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// fakeNavMetrics captures the stranded-container counter so a reap that silently does
// nothing cannot look identical to a fleet that had nothing to reap.
type fakeNavMetrics struct {
	stranded map[string]int
}

func (f *fakeNavMetrics) RecordRouteCompletion(int, domainNavigation.RouteStatus, float64, int, int) {
}
func (f *fakeNavMetrics) RecordSegmentCompletion(int, int, int)             {}
func (f *fakeNavMetrics) RecordFuelPurchase(int, string, int)               {}
func (f *fakeNavMetrics) RecordFuelConsumption(int, shared.FlightMode, int) {}
func (f *fakeNavMetrics) RecordStrandedJumpContainer(_ int, outcome string) {
	if f.stranded == nil {
		f.stranded = map[string]int{}
	}
	f.stranded[outcome]++
}

// installFakeNavMetrics points the process-wide navigation collector at a fake for the
// duration of one test and restores it afterwards.
func installFakeNavMetrics(t *testing.T) *fakeNavMetrics {
	t.Helper()
	fake := &fakeNavMetrics{}
	metrics.SetGlobalNavigationCollector(fake)
	t.Cleanup(func() { metrics.SetGlobalNavigationCollector(nil) })
	return fake
}

// jumpFixture builds the common happy-path jump: a hull at a gate connected to the
// destination, with an API client that reports a successful jump.
func jumpFixture(t *testing.T, shipSymbol string) (*stubJumpShipRepo, *stubJumpPlayerRepo, *stubJumpAPIClient, *shared.MockClock) {
	t.Helper()
	gate := newJumpGateWaypoint(t, "X1-AB12-GATE")
	ship := newJumpTestShip(t, shipSymbol, gate)
	return &stubJumpShipRepo{ship: ship},
		&stubJumpPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")},
		&stubJumpAPIClient{
			gateData: &ports.JumpGateData{Symbol: "X1-AB12-GATE", Connections: []string{"X1-CD34-GATE"}},
			result: &ports.JumpResult{
				DestinationSystem:   "X1-CD34",
				DestinationWaypoint: "X1-CD34-GATE",
				CooldownSeconds:     60,
			},
		},
		&shared.MockClock{CurrentTime: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)}
}

// THE FALSIFIER. A hull carrying the exact stranded row measured on the live fleet —
// legacy deterministic ID "ship-jump-<symbol>", no hull claimed under it — must be able to
// jump. Before the fix this hull was wedged permanently: the insert collided on
// containers_pkey and every subsequent jump failed the same way, forever.
func TestJumpShip_HullWithStrandedContainerRow_CanStillJump(t *testing.T) {
	shipRepo, playerRepo, apiClient, clock := jumpFixture(t, "TORWIND-235")
	containerRepo := &stubJumpContainerRepo{}

	// The row a crashed earlier jump left behind, exactly as observed in production:
	// ship-jump-TORWIND-235, PENDING, owning no hull.
	containerRepo.seedStrandedJumpContainer("ship-jump-TORWIND-235", "TORWIND-235")

	// FIXTURE ASSERTION, at exactly the read the new code performs. Without this, a
	// mis-keyed seed would make every assertion below vacuous - the test would pass
	// because nothing was stranded, not because the wedge was fixed.
	seeded, err := containerRepo.ListJumpContainersForShip(context.Background(), "TORWIND-235", 1)
	if err != nil {
		t.Fatalf("fixture read failed: %v", err)
	}
	if len(seeded) != 1 || seeded[0] != "ship-jump-TORWIND-235" {
		t.Fatalf("fixture is not seeded where the handler reads it: got %v, want [ship-jump-TORWIND-235]", seeded)
	}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, nil, clock)
	playerIDInt := 1
	resp, err := handler.Handle(context.Background(), &JumpShipCommand{
		ShipSymbol:        "TORWIND-235",
		DestinationSystem: "X1-CD34",
		PlayerID:          &playerIDInt,
	})
	if err != nil {
		t.Fatalf("a hull with a stranded jump container row must still be able to jump, got: %v", err)
	}
	jumpResp, ok := resp.(*JumpShipResponse)
	if !ok || !jumpResp.Success {
		t.Fatalf("expected a successful jump, got %#v", resp)
	}

	// The claim record this attempt created must not be the stranded one, or the
	// collision was merely renamed rather than removed.
	if len(containerRepo.added) != 1 {
		t.Fatalf("expected exactly 1 container record added, got %d", len(containerRepo.added))
	}
	if got := containerRepo.added[0].ID(); got == "ship-jump-TORWIND-235" {
		t.Fatalf("the jump reused the stranded deterministic ID %q - the wedge is intact", got)
	}
}

// The stranded row must actually be CLEARED, and counted. A fix that dodged the collision
// but left the row behind would leak one dead row per crashed jump forever.
func TestJumpShip_StrandedContainerRow_IsClearedAndCounted(t *testing.T) {
	fake := installFakeNavMetrics(t)
	shipRepo, playerRepo, apiClient, clock := jumpFixture(t, "TORWIND-241")
	containerRepo := &stubJumpContainerRepo{}
	containerRepo.seedStrandedJumpContainer("ship-jump-TORWIND-241", "TORWIND-241")

	seeded, err := containerRepo.ListJumpContainersForShip(context.Background(), "TORWIND-241", 1)
	if err != nil || len(seeded) != 1 {
		t.Fatalf("fixture is not seeded where the handler reads it: %v (err %v)", seeded, err)
	}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, nil, clock)
	playerIDInt := 1
	if _, err := handler.Handle(context.Background(), &JumpShipCommand{
		ShipSymbol:        "TORWIND-241",
		DestinationSystem: "X1-CD34",
		PlayerID:          &playerIDInt,
	}); err != nil {
		t.Fatalf("jump failed: %v", err)
	}

	left, err := containerRepo.ListJumpContainersForShip(context.Background(), "TORWIND-241", 1)
	if err != nil {
		t.Fatalf("post-jump read failed: %v", err)
	}
	for _, id := range left {
		if id == "ship-jump-TORWIND-241" {
			t.Fatalf("the stranded row survived the jump: %v", left)
		}
	}

	if fake.stranded["cleared"] != 1 {
		t.Fatalf("expected exactly 1 stranded row counted as cleared, got %v", fake.stranded)
	}
}

// A jump that is GENUINELY IN FLIGHT must not be clobbered. In flight is represented the
// only way it exists in this system: the hull is claimed under that jump's container. Our
// handler must lose at the claim and leave the other jump's row untouched.
//
// This is the test that pins the reap AFTER the claim. Move the reap before
// AssignToContainer - the "obvious" placement - and the in-flight row is deleted here,
// which under the live schema's ON DELETE SET NULL silently unclaims a hull mid-jump.
func TestJumpShip_InFlightJumpForSameHull_IsNotClobbered(t *testing.T) {
	fake := installFakeNavMetrics(t)
	shipRepo, playerRepo, apiClient, clock := jumpFixture(t, "TORWIND-235")
	containerRepo := &stubJumpContainerRepo{}

	// Another jump for this hull is mid-flight: its row exists AND it holds the claim.
	const inFlightID = "ship-jump-TORWIND-235-1753800000000000000"
	containerRepo.seedStrandedJumpContainer(inFlightID, "TORWIND-235")
	if err := shipRepo.ship.AssignToContainer(inFlightID, clock); err != nil {
		t.Fatalf("fixture: could not put the hull in flight: %v", err)
	}

	// FIXTURE ASSERTION on both halves of "in flight" - the row the reap would read, and
	// the claim that proves it is live. A fixture missing either half would let the reap
	// delete the row for the right reason and pass for the wrong one.
	seeded, err := containerRepo.ListJumpContainersForShip(context.Background(), "TORWIND-235", 1)
	if err != nil || len(seeded) != 1 || seeded[0] != inFlightID {
		t.Fatalf("fixture is not seeded where the handler reads it: %v (err %v)", seeded, err)
	}
	if !shipRepo.ship.IsAssigned() || shipRepo.ship.ContainerID() != inFlightID {
		t.Fatalf("fixture: hull is not claimed under the in-flight container")
	}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, nil, clock)
	playerIDInt := 1
	_, err = handler.Handle(context.Background(), &JumpShipCommand{
		ShipSymbol:        "TORWIND-235",
		DestinationSystem: "X1-CD34",
		PlayerID:          &playerIDInt,
	})
	if err == nil {
		t.Fatalf("a second jump must not proceed while the hull is claimed by a live jump")
	}

	// The in-flight jump's claim record must be exactly as it was.
	after, listErr := containerRepo.ListJumpContainersForShip(context.Background(), "TORWIND-235", 1)
	if listErr != nil {
		t.Fatalf("post-attempt read failed: %v", listErr)
	}
	found := false
	for _, id := range after {
		if id == inFlightID {
			found = true
		}
	}
	if !found {
		t.Fatalf("the in-flight jump's container record was clobbered: %v", after)
	}
	if fake.stranded["cleared"] != 0 {
		t.Fatalf("nothing was stranded, but the reap counted %v", fake.stranded)
	}
}

// The reap is hygiene, not the fix: if clearing a stranded row fails, the jump must still
// go, and the failure must be counted distinctly. A reaper that fires and fails must never
// emit the same signal as one that had nothing to do.
func TestJumpShip_StrandedRowClearFailure_CountsSeparatelyAndDoesNotFailTheJump(t *testing.T) {
	fake := installFakeNavMetrics(t)
	shipRepo, playerRepo, apiClient, clock := jumpFixture(t, "TORWIND-235")
	containerRepo := &stubJumpContainerRepo{}
	containerRepo.seedStrandedJumpContainer("ship-jump-TORWIND-235", "TORWIND-235")
	containerRepo.removeErr = errors.New("transient database failure")

	seeded, err := containerRepo.ListJumpContainersForShip(context.Background(), "TORWIND-235", 1)
	if err != nil || len(seeded) != 1 {
		t.Fatalf("fixture is not seeded where the handler reads it: %v (err %v)", seeded, err)
	}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, nil, clock)
	playerIDInt := 1
	if _, err := handler.Handle(context.Background(), &JumpShipCommand{
		ShipSymbol:        "TORWIND-235",
		DestinationSystem: "X1-CD34",
		PlayerID:          &playerIDInt,
	}); err != nil {
		t.Fatalf("a failed reap must not fail the jump, got: %v", err)
	}

	if fake.stranded["clear_failed"] != 1 {
		t.Fatalf("expected exactly 1 clear_failed, got %v", fake.stranded)
	}
	if fake.stranded["cleared"] != 0 {
		t.Fatalf("a failed clear must not be counted as cleared, got %v", fake.stranded)
	}
}

// Another hull's stranded row must never be reaped. Hull symbols are not prefix-free
// ("TORWIND-2" is a prefix of "TORWIND-23"), so a reap keyed on the container ID rather
// than on the hull its config names would delete a neighbour's live claim record.
func TestJumpShip_ReapIsScopedToItsOwnHull_NotIDPrefixNeighbours(t *testing.T) {
	shipRepo, playerRepo, apiClient, clock := jumpFixture(t, "TORWIND-2")
	containerRepo := &stubJumpContainerRepo{}
	containerRepo.seedStrandedJumpContainer("ship-jump-TORWIND-2", "TORWIND-2")
	containerRepo.seedStrandedJumpContainer("ship-jump-TORWIND-23", "TORWIND-23")

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, nil, clock)
	playerIDInt := 1
	if _, err := handler.Handle(context.Background(), &JumpShipCommand{
		ShipSymbol:        "TORWIND-2",
		DestinationSystem: "X1-CD34",
		PlayerID:          &playerIDInt,
	}); err != nil {
		t.Fatalf("jump failed: %v", err)
	}

	neighbour, err := containerRepo.ListJumpContainersForShip(context.Background(), "TORWIND-23", 1)
	if err != nil {
		t.Fatalf("post-jump read failed: %v", err)
	}
	if len(neighbour) != 1 || neighbour[0] != "ship-jump-TORWIND-23" {
		t.Fatalf("TORWIND-23's container record was reaped by TORWIND-2's jump: %v", neighbour)
	}
}

// Two successive jumps by the same hull must not collide with each other. This is the
// regression the deterministic ID caused in the first place, in its mildest form: with a
// per-attempt ID the second jump gets a fresh row even if the first one's cleanup was lost.
func TestJumpShip_SecondJumpAfterLostCleanup_DoesNotCollide(t *testing.T) {
	shipRepo, playerRepo, apiClient, clock := jumpFixture(t, "TORWIND-235")
	containerRepo := &stubJumpContainerRepo{}

	handler := NewJumpShipHandler(shipRepo, playerRepo, apiClient, nil, containerRepo, nil, clock)
	playerIDInt := 1
	cmd := &JumpShipCommand{ShipSymbol: "TORWIND-235", DestinationSystem: "X1-CD34", PlayerID: &playerIDInt}

	if _, err := handler.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("first jump failed: %v", err)
	}

	// Simulate the cleanup being lost the way a daemon death loses it: put the first
	// attempt's row back, unclaimed.
	firstID := containerRepo.added[0].ID()
	containerRepo.seedStrandedJumpContainer(firstID, "TORWIND-235")

	// The hull is back at the gate for a second jump.
	shipRepo.ship.SetLocation(newJumpGateWaypoint(t, "X1-AB12-GATE"))
	clock.CurrentTime = clock.CurrentTime.Add(time.Hour)

	if _, err := handler.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("second jump collided with the first jump's leftover row: %v", err)
	}
}
