package navigation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// stubJumpTopologyStore answers jump-time topology from the persisted gate graph.
type stubJumpTopologyStore struct {
	waypoints      map[string]string // "from->to" -> destination gate waypoint
	built          map[string]bool
	waypointErr    error
	builtErr       error
	missValue      string // returned alongside ok=false, to prove the miss flag is honoured
	waypointLookup int
	builtLookup    int
}

func (s *stubJumpTopologyStore) StoredGateWaypoint(_ context.Context, fromSystem, toSystem string) (string, bool, error) {
	s.waypointLookup++
	if s.waypointErr != nil {
		// Adversarial: a failing store also returns a value, so the caller must
		// reject it on the error alone.
		return "X1-POISON-I00", true, s.waypointErr
	}
	if wp, ok := s.waypoints[fromSystem+"->"+toSystem]; ok {
		return wp, true, nil
	}
	// Adversarial: a MISS still carries a value, so the caller must reject it on
	// the miss flag alone rather than on the value being empty.
	return s.missValue, false, nil
}

func (s *stubJumpTopologyStore) RecordedBuiltGate(_ context.Context, gateWaypoint string) (bool, error) {
	s.builtLookup++
	if s.builtErr != nil {
		return true, s.builtErr
	}
	return s.built[gateWaypoint], nil
}

// countingJumpConstructionRepo counts the live construction reads a jump costs.
type countingJumpConstructionRepo struct {
	manufacturing.ConstructionSiteRepository

	site  *manufacturing.ConstructionSite
	err   error
	calls int
}

func (c *countingJumpConstructionRepo) FindByWaypoint(_ context.Context, _ string, _ int) (*manufacturing.ConstructionSite, error) {
	c.calls++
	return c.site, c.err
}

// newStoredTopologyJumpFixture wires a driveless hull in orbit on a complete gate,
// so the handler runs both the source-gate construction check and the destination
// gate resolution.
func newStoredTopologyJumpFixture(
	t *testing.T,
	client *stubSeqJumpAPIClient,
	constructionRepo manufacturing.ConstructionSiteRepository,
	store *stubJumpTopologyStore,
) (*JumpShipHandler, *JumpShipCommand) {
	t.Helper()
	gate := newJumpGateWaypoint(t, "X1-CA43-I56")
	ship := newDrivelessJumpTestShip(t, "TORWIND-56", gate)
	shipRepo := &stubJumpShipRepo{ship: ship}
	playerRepo := &stubJumpPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 25, 3, 0, 0, 0, time.UTC)}
	handler := NewJumpShipHandler(shipRepo, playerRepo, client, nil, &stubJumpContainerRepo{}, constructionRepo, clock)
	if store != nil {
		handler.SetJumpTopologyStore(store)
	}

	playerIDInt := 1
	cmd := &JumpShipCommand{
		ShipSymbol:        "TORWIND-56",
		DestinationSystem: "X1-XD86",
		PlayerID:          &playerIDInt,
		SkipClaim:         true,
	}
	return handler, cmd
}

func okJumpClient() *stubSeqJumpAPIClient {
	return &stubSeqJumpAPIClient{
		gateDataByCall: []*ports.JumpGateData{{Symbol: "X1-CA43-I56", Connections: realCA43Connections()}},
		result:         &ports.JumpResult{DestinationSystem: "X1-XD86", DestinationWaypoint: "X1-XD86-I54", CooldownSeconds: 60},
	}
}

// THE SAVING: with the hop already stored, a jump costs NO topology requests — the
// destination gate symbol and the source gate's build state both come from the record.
func TestJumpShip_StoredTopologyCostsNoTopologyRequests(t *testing.T) {
	client := okJumpClient()
	construction := &countingJumpConstructionRepo{}
	store := &stubJumpTopologyStore{
		waypoints: map[string]string{"X1-CA43->X1-XD86": "X1-XD86-I54"},
		built:     map[string]bool{"X1-CA43-I56": true},
	}
	handler, cmd := newStoredTopologyJumpFixture(t, client, construction, store)

	resp, err := handler.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if jumpResp, ok := resp.(*JumpShipResponse); !ok || !jumpResp.Success {
		t.Fatalf("expected a successful jump, got %+v", resp)
	}
	if client.getJumpGateCalls != 0 {
		t.Fatalf("expected 0 live gate reads, got %d", client.getJumpGateCalls)
	}
	if construction.calls != 0 {
		t.Fatalf("expected 0 live construction reads, got %d", construction.calls)
	}
	if client.jumpShipWaypointArg != "X1-XD86-I54" {
		t.Fatalf("the stored gate waypoint must be the one jumped to, got %q", client.jumpShipWaypointArg)
	}
}

// GUARD: an unstored hop still reads live — the store may only answer what it holds.
// The store reports the miss while still handing back a (wrong) waypoint, so the miss
// flag itself has to be honoured; a value-only check would jump to the wrong gate.
func TestJumpShip_UnstoredHopStillReadsLive(t *testing.T) {
	client := okJumpClient()
	store := &stubJumpTopologyStore{
		waypoints: map[string]string{},
		built:     map[string]bool{},
		missValue: "X1-WRONG-I99",
	}
	handler, cmd := newStoredTopologyJumpFixture(t, client, nil, store)

	if _, err := handler.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.getJumpGateCalls != 1 {
		t.Fatalf("an unstored hop must fall through to exactly one live read, got %d", client.getJumpGateCalls)
	}
	if client.jumpShipWaypointArg != "X1-XD86-I54" {
		t.Fatalf("expected the live-resolved waypoint, got %q", client.jumpShipWaypointArg)
	}
}

// GUARD: a store failure must never be trusted, even when it also returns a value.
func TestJumpShip_StoreErrorFallsThroughToLiveGateRead(t *testing.T) {
	client := okJumpClient()
	store := &stubJumpTopologyStore{waypointErr: errors.New("db down")}
	handler, cmd := newStoredTopologyJumpFixture(t, client, nil, store)

	if _, err := handler.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.getJumpGateCalls != 1 {
		t.Fatalf("a store error must fall through to the live read, got %d", client.getJumpGateCalls)
	}
	if client.jumpShipWaypointArg != "X1-XD86-I54" {
		t.Fatalf("the poisoned store value must never reach the jump, got %q", client.jumpShipWaypointArg)
	}
}

// GUARD: with no store wired the handler behaves exactly as before.
func TestJumpShip_NilStoreKeepsTheLiveReadBehaviour(t *testing.T) {
	client := okJumpClient()
	handler, cmd := newStoredTopologyJumpFixture(t, client, nil, nil)

	if _, err := handler.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.getJumpGateCalls != 1 {
		t.Fatalf("expected the unchanged single live read, got %d", client.getJumpGateCalls)
	}
}

// GUARD: a source gate NOT recorded built is still verified live, and a live
// under-construction verdict still REFUSES the jump. The store may suppress a
// redundant confirmation, never manufacture a permissive one.
func TestJumpShip_UnrecordedSourceGateIsVerifiedLiveAndCanRefuse(t *testing.T) {
	client := okJumpClient()
	construction := &countingJumpConstructionRepo{
		site: manufacturing.NewConstructionSite("X1-CA43-I56", "JUMP_GATE", []manufacturing.ConstructionMaterial{
			manufacturing.NewConstructionMaterial("FAB_MATS", 1000, 10),
		}, false),
	}
	store := &stubJumpTopologyStore{waypoints: map[string]string{}, built: map[string]bool{}}
	handler, cmd := newStoredTopologyJumpFixture(t, client, construction, store)

	_, err := handler.Handle(context.Background(), cmd)
	if err == nil {
		t.Fatal("an under-construction source gate must still refuse the jump")
	}
	if construction.calls != 1 {
		t.Fatalf("an unrecorded source gate must be verified live exactly once, got %d", construction.calls)
	}
}

// GUARD: a construction-store failure falls through to the live check rather than
// being read as "built".
func TestJumpShip_BuiltLookupErrorFallsThroughToLiveConstructionRead(t *testing.T) {
	client := okJumpClient()
	construction := &countingJumpConstructionRepo{
		site: manufacturing.NewConstructionSite("X1-CA43-I56", "JUMP_GATE", nil, true),
	}
	store := &stubJumpTopologyStore{
		waypoints: map[string]string{"X1-CA43->X1-XD86": "X1-XD86-I54"},
		builtErr:  errors.New("db down"),
	}
	handler, cmd := newStoredTopologyJumpFixture(t, client, construction, store)

	if _, err := handler.Handle(context.Background(), cmd); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if construction.calls != 1 {
		t.Fatalf("a built-lookup error must fall through to the live construction read, got %d", construction.calls)
	}
}
