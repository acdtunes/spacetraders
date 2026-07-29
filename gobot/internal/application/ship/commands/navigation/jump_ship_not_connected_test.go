package navigation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	domainNavigation "github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// The live TORWIND-41 incident (agent TORWIND, player 5). Four crashes in 27 minutes, plus
// TORWIND-A and TORWIND-6, all on API code 4255 "… is not connected to the current location".
//
// ESTABLISHED ROOT CAUSE (from the daemon's own logs + the gate_edges table, not a guess):
// the hull's PERSISTED row said X1-GF41 while the hull physically stood on X1-KC84's gate.
// The API said so twice, in writing, inside one container run:
//
//	4202 {"destinationSymbol":"X1-GF41-I57","currentSystemSymbol":"X1-KC84"}
//	4255 {"connections":["X1-VV36-I60","X1-RX9-XZ2B","X1-GF41-I56"]}   ← KC84's real gate set
//
// Everything downstream was FAITHFULLY wrong from that one stale fact: candidate discovery
// ("Reposition reach from X1-GF41"), the BFS (keyed adjacency["X1-GF41"]), and finally
// StoredGateWaypoint("X1-GF41","X1-KP23") = "X1-KP23-I53" — the exact waypoint the API
// refused. The adjacency lookup was never wrong; its KEY, the origin system, was.
//
// That is also why the single-hop case fails identically: no hop indexing is involved at all.
//
// Nothing ever reconciled that belief against the server, so the next tick re-derived the
// SAME impossible jump from the SAME stale row — the recurrence loop. These tests pin the
// re-anchor that breaks it: a 4255 refusal is first-hand proof that our believed position is
// wrong, so the hull is re-read from the server and written through to durable state.

// realKC84GateConnections is the authoritative connection set the live API returned in the
// 4255 refusals — the gate TORWIND-41 was really standing on (X1-KC84's). Used verbatim as a
// golden master so the fixture reproduces the incident rather than an invented shape.
func realKC84GateConnections() []string {
	return []string{"X1-VV36-I60", "X1-RX9-XZ2B", "X1-GF41-I56"}
}

// notConnectedRefusal builds the production 4255 wire form, %w-wrapped exactly as the API
// adapter wraps it ("failed to jump ship: %w" around a *ports.APIError), so the classifier
// under test faces the same error value production hands it.
func notConnectedRefusal(refusedWaypoint string, connections []string) error {
	quoted := make([]string, 0, len(connections))
	for _, c := range connections {
		quoted = append(quoted, fmt.Sprintf("%q", c))
	}
	body := fmt.Sprintf(
		`{"error":{"code":4255,"message":"Failed to execute jump. Waypoint %s is not connected to the current location.","data":{"connections":[%s]},"requestId":"019fab3b-2620-773c-9a33-c81e3d5340fd"}}`,
		refusedWaypoint, strings.Join(quoted, ","),
	)
	return fmt.Errorf("failed to jump ship: %w", &ports.APIError{StatusCode: 400, Body: body})
}

// stubStaleShipRepo models the defect exactly: the PERSISTED row (FindBySymbol — what the
// router plans from) disagrees with the SERVER (SyncShipFromAPI — where the hull really is).
// It counts the write-through re-anchor so a test can prove the correction actually reached
// durable state rather than being computed and dropped.
type stubStaleShipRepo struct {
	domainNavigation.ShipRepository

	persisted *domainNavigation.Ship
	live      *domainNavigation.Ship
	liveErr   error
	syncCalls int
}

func (s *stubStaleShipRepo) FindBySymbol(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.Ship, error) {
	return s.persisted, nil
}

func (s *stubStaleShipRepo) SyncShipFromAPI(_ context.Context, _ string, _ shared.PlayerID) (*domainNavigation.Ship, error) {
	s.syncCalls++
	if s.liveErr != nil {
		return nil, s.liveErr
	}
	return s.live, nil
}

func (s *stubStaleShipRepo) Save(_ context.Context, _ *domainNavigation.Ship) error { return nil }

func (s *stubStaleShipRepo) SaveWithRetry(_ context.Context, _ string, _ shared.PlayerID, mutate domainNavigation.ShipMutation) (*domainNavigation.Ship, bool, error) {
	changed, err := mutate(s.persisted)
	return s.persisted, changed, err
}

// prunedAdjacency is one recorded reconcile of a system's stored edges against the server's
// authoritative connection set.
type prunedAdjacency struct {
	system        string
	authoritative []string
}

// stubReconcilingTopologyStore answers the jump's topology questions and records every
// adjacency reconcile, so a test can assert WHICH system the correction was attributed to —
// the single fact the whole incident turns on.
type stubReconcilingTopologyStore struct {
	waypoints map[string]string
	built     map[string]bool
	pruneErr  error
	pruned    []prunedAdjacency
}

func (s *stubReconcilingTopologyStore) StoredGateWaypoint(_ context.Context, fromSystem, toSystem string) (string, bool, error) {
	if wp, ok := s.waypoints[fromSystem+"->"+toSystem]; ok {
		return wp, true, nil
	}
	return "", false, nil
}

func (s *stubReconcilingTopologyStore) RecordedBuiltGate(_ context.Context, gateWaypoint string) (bool, error) {
	return s.built[gateWaypoint], nil
}

func (s *stubReconcilingTopologyStore) PruneContradictedEdges(_ context.Context, systemSymbol string, authoritativeConnections []string) (int, error) {
	s.pruned = append(s.pruned, prunedAdjacency{system: systemSymbol, authoritative: authoritativeConnections})
	if s.pruneErr != nil {
		return 0, s.pruneErr
	}
	return 0, nil
}

// stubRefusingJumpAPIClient refuses every jump with a fixed error and records the waypoints
// it was asked to jump to — the values that actually cross the port into the live request body.
type stubRefusingJumpAPIClient struct {
	ports.APIClient

	jumpErr      error
	jumpResult   *ports.JumpResult
	gateData     *ports.JumpGateData
	jumpedTo     []string
	gateReads    int
	jumpAttempts int
}

func (s *stubRefusingJumpAPIClient) JumpShip(_ context.Context, _ string, waypointSymbol string, _ string) (*ports.JumpResult, error) {
	s.jumpAttempts++
	s.jumpedTo = append(s.jumpedTo, waypointSymbol)
	if s.jumpErr != nil {
		return nil, s.jumpErr
	}
	return s.jumpResult, nil
}

func (s *stubRefusingJumpAPIClient) GetJumpGate(_ context.Context, _, _, _ string) (*ports.JumpGateData, error) {
	s.gateReads++
	if s.gateData != nil {
		return s.gateData, nil
	}
	return &ports.JumpGateData{}, nil
}

// newStaleOriginJumpFixture wires the incident: a driveless hull whose PERSISTED location is
// X1-GF41's gate (the stale belief the router planned from) while the SERVER puts it on
// X1-KC84's gate (X1-KC84-A13E — the real waypoint, taken from the live gate_edges table).
// The stored topology is the real GF41 row set, so the handler resolves and posts exactly the
// waypoint production posted.
func newStaleOriginJumpFixture(
	t *testing.T,
	client *stubRefusingJumpAPIClient,
	store *stubReconcilingTopologyStore,
	destinationSystem string,
	liveErr error,
) (*JumpShipHandler, *JumpShipCommand, *stubStaleShipRepo) {
	t.Helper()
	believedGate := newJumpGateWaypoint(t, "X1-GF41-I56")
	trueGate := newJumpGateWaypoint(t, "X1-KC84-A13E")
	shipRepo := &stubStaleShipRepo{
		persisted: newDrivelessJumpTestShip(t, "TORWIND-41", believedGate),
		live:      newDrivelessJumpTestShip(t, "TORWIND-41", trueGate),
		liveErr:   liveErr,
	}
	playerRepo := &stubJumpPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(5), "TORWIND", "test-token")}
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 29, 0, 7, 0, 0, time.UTC)}
	handler := NewJumpShipHandler(shipRepo, playerRepo, client, nil, &stubJumpContainerRepo{}, nil, clock)
	if store != nil {
		handler.SetJumpTopologyStore(store)
	}

	playerIDInt := 5
	cmd := &JumpShipCommand{
		ShipSymbol:        "TORWIND-41",
		DestinationSystem: destinationSystem,
		PlayerID:          &playerIDInt,
		SkipClaim:         true,
	}
	return handler, cmd, shipRepo
}

// realGF41StoredTopology is X1-GF41's stored edge set from the live gate_edges table — the
// rows that supplied the two refused waypoints. The source gate is recorded built so the
// driveless precondition passes without a construction read.
func realGF41StoredTopology() *stubReconcilingTopologyStore {
	return &stubReconcilingTopologyStore{
		waypoints: map[string]string{
			"X1-GF41->X1-AJ10": "X1-AJ10-F24Z",
			"X1-GF41->X1-KP23": "X1-KP23-I53",
			"X1-GF41->X1-KC84": "X1-KC84-A13E",
		},
		built: map[string]bool{"X1-GF41-I56": true, "X1-KC84-A13E": true},
	}
}

// THE REGRESSION. Both production failures, which differ ONLY in the destination: the first
// hop of the 3-hop route toward X1-MC90, and the standalone single-hop route to X1-KP23. The
// single-hop case is what rules out any multi-hop-indexing explanation — same defect, no hops
// to mis-index.
//
// A 4255 refusal is the server telling us our believed position is wrong. The hull must be
// RE-ANCHORED on the server's truth and that correction written through to durable state, so
// the next tick re-derives the route from where the hull actually is (RULINGS #2) instead of
// replanning the identical impossible jump forever.
func TestJumpShip_NotConnectedRefusal_ReAnchorsTheHullOnTheServersTruth(t *testing.T) {
	cases := []struct {
		name              string
		destinationSystem string
		refusedWaypoint   string
	}{
		{
			name:              "first hop of the 3-hop route toward X1-MC90",
			destinationSystem: "X1-AJ10",
			refusedWaypoint:   "X1-AJ10-F24Z",
		},
		{
			name:              "single-hop route to X1-KP23 (hop 1 of 1 — no hop index to blame)",
			destinationSystem: "X1-KP23",
			refusedWaypoint:   "X1-KP23-I53",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &stubRefusingJumpAPIClient{
				jumpErr: notConnectedRefusal(tc.refusedWaypoint, realKC84GateConnections()),
			}
			handler, cmd, shipRepo := newStaleOriginJumpFixture(t, client, realGF41StoredTopology(), tc.destinationSystem, nil)

			_, err := handler.Handle(context.Background(), cmd)
			if err == nil {
				t.Fatal("a refused jump must surface an error, never a silent success")
			}

			// Fixture fidelity: the handler posted the exact waypoint production posted.
			if len(client.jumpedTo) != 1 || client.jumpedTo[0] != tc.refusedWaypoint {
				t.Fatalf("expected the incident's posted waypoint %q, got %v", tc.refusedWaypoint, client.jumpedTo)
			}

			// THE FIX: the stale belief is corrected against the server and persisted.
			if shipRepo.syncCalls != 1 {
				t.Fatalf("a not-connected refusal must re-anchor the hull on the server exactly once so the next tick plans from where it really is, got %d re-anchors", shipRepo.syncCalls)
			}
		})
	}
}

// The correction must be attributed to the system the hull is REALLY in. The refusal's
// connection set describes the gate the hull is standing on (X1-KC84's) — it says nothing
// whatsoever about X1-GF41's edges, which are correct and must not be touched. Attributing
// this evidence to the believed system would corrupt a healthy adjacency on every incident.
func TestJumpShip_NotConnectedRefusal_ReconcilesTheSystemTheHullIsReallyIn(t *testing.T) {
	client := &stubRefusingJumpAPIClient{
		jumpErr: notConnectedRefusal("X1-KP23-I53", realKC84GateConnections()),
	}
	store := realGF41StoredTopology()
	handler, cmd, _ := newStaleOriginJumpFixture(t, client, store, "X1-KP23", nil)

	if _, err := handler.Handle(context.Background(), cmd); err == nil {
		t.Fatal("a refused jump must surface an error")
	}

	if len(store.pruned) != 1 {
		t.Fatalf("the server's connection set must reconcile the stored adjacency exactly once, got %d reconciles", len(store.pruned))
	}
	got := store.pruned[0]
	if got.system != "X1-KC84" {
		t.Fatalf("the refusal describes the gate the hull is REALLY on (X1-KC84); reconciling %q instead would corrupt a healthy adjacency", got.system)
	}
	if strings.Join(got.authoritative, ",") != strings.Join(realKC84GateConnections(), ",") {
		t.Fatalf("the server's connection set must be passed through verbatim, got %v", got.authoritative)
	}
}

// FAIL CLOSED. Without the hull's true position we cannot know WHICH system the connection
// set describes, so it is unattributable evidence and the adjacency must be left completely
// alone — guessing would let a refusal delete a healthy system's edges. The original refusal
// still surfaces so the container retries.
func TestJumpShip_NotConnectedRefusal_WithUnattributableTruth_LeavesAdjacencyUntouched(t *testing.T) {
	client := &stubRefusingJumpAPIClient{
		jumpErr: notConnectedRefusal("X1-KP23-I53", realKC84GateConnections()),
	}
	store := realGF41StoredTopology()
	handler, cmd, shipRepo := newStaleOriginJumpFixture(t, client, store, "X1-KP23", errors.New("agent API unreachable"))

	if _, err := handler.Handle(context.Background(), cmd); err == nil {
		t.Fatal("a refused jump must still surface an error when the re-anchor fails")
	}

	if shipRepo.syncCalls != 1 {
		t.Fatalf("the re-anchor must be attempted exactly once, got %d", shipRepo.syncCalls)
	}
	if len(store.pruned) != 0 {
		t.Fatalf("an unattributable connection set must never touch the adjacency, got %v", store.pruned)
	}
}

// GUARD on the classifier: ONLY a not-connected verdict is position evidence. Every other
// refusal must leave both the hull and the adjacency alone — otherwise any unrelated failure
// spends an API read and rewrites topology.
//
// The two cases falsify different halves of the classifier. A 4262 (destination gate under
// construction) is a fact about the DESTINATION and is classified before this path is reached.
// The second case is the one that actually pins the CODE check: it carries a well-formed
// connections payload under a different code, so a classifier that keyed only on the payload
// shape — and not on 4255 — would wrongly read it as position evidence.
func TestJumpShip_OtherRefusalsAreNotTreatedAsPositionEvidence(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "destination gate under construction (4262) — a fact about the destination",
			body: `{"error":{"code":4262,"message":"Failed to execute jump. Jump gate is under construction."}}`,
		},
		{
			name: "a different code carrying a connections payload — the shape is not the verdict",
			body: `{"error":{"code":4256,"message":"Failed to execute jump.","data":{"connections":["X1-VV36-I60","X1-RX9-XZ2B","X1-GF41-I56"]}}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &stubRefusingJumpAPIClient{
				jumpErr: fmt.Errorf("failed to jump ship: %w", &ports.APIError{StatusCode: 400, Body: tc.body}),
			}
			store := realGF41StoredTopology()
			handler, cmd, shipRepo := newStaleOriginJumpFixture(t, client, store, "X1-KP23", nil)

			if _, err := handler.Handle(context.Background(), cmd); err == nil {
				t.Fatal("the refusal must still surface an error")
			}

			if shipRepo.syncCalls != 0 {
				t.Fatalf("only a not-connected verdict is position evidence; this refusal must not re-anchor, got %d", shipRepo.syncCalls)
			}
			if len(store.pruned) != 0 {
				t.Fatalf("only a not-connected verdict may reconcile adjacency, got %v", store.pruned)
			}
		})
	}
}

// A not-connected refusal is position evidence in its own right: it says the gate we asked for
// is not adjacent to where the hull IS, whether or not the server attached the connection list.
// So the hull must still be re-anchored — that is the half of the fix that breaks the replan
// loop. The adjacency, however, has nothing to reconcile against and must not be touched:
// treating an absent list as "connects nowhere" would delete a whole system's topology on a
// transient empty read (sp-hguq3/sp-dmxy5).
func TestJumpShip_NotConnectedRefusalWithNoConnections_StillReAnchorsButReconcilesNothing(t *testing.T) {
	client := &stubRefusingJumpAPIClient{
		jumpErr: fmt.Errorf("failed to jump ship: %w", &ports.APIError{
			StatusCode: 400,
			Body:       `{"error":{"code":4255,"message":"Failed to execute jump. Waypoint X1-KP23-I53 is not connected to the current location.","data":{"connections":[]}}}`,
		}),
	}
	store := realGF41StoredTopology()
	handler, cmd, shipRepo := newStaleOriginJumpFixture(t, client, store, "X1-KP23", nil)

	if _, err := handler.Handle(context.Background(), cmd); err == nil {
		t.Fatal("a refused jump must surface an error")
	}

	if shipRepo.syncCalls != 1 {
		t.Fatalf("a not-connected verdict is position evidence with or without a connection list; expected 1 re-anchor, got %d", shipRepo.syncCalls)
	}
	if len(store.pruned) != 0 {
		t.Fatalf("an absent connection list is nothing to reconcile against and must not touch adjacency, got %v", store.pruned)
	}
}

// NO REGRESSION on the flight path a legitimate multi-hop route is made of: a hop that the
// server ACCEPTS costs no re-anchor and no reconcile, and still lands and persists the hull
// at its destination. Every hop of a real route takes this path.
func TestJumpShip_AcceptedHop_NeitherReAnchorsNorReconciles(t *testing.T) {
	client := &stubRefusingJumpAPIClient{
		jumpResult: &ports.JumpResult{DestinationSystem: "X1-KC84", DestinationWaypoint: "X1-KC84-A13E", CooldownSeconds: 60},
	}
	store := realGF41StoredTopology()
	handler, cmd, shipRepo := newStaleOriginJumpFixture(t, client, store, "X1-KC84", nil)

	resp, err := handler.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("an accepted hop must succeed, got %v", err)
	}
	jumpResp, ok := resp.(*JumpShipResponse)
	if !ok || !jumpResp.Success || jumpResp.DestinationSystem != "X1-KC84" {
		t.Fatalf("expected a successful jump to X1-KC84, got %+v", resp)
	}
	if shipRepo.syncCalls != 0 {
		t.Fatalf("an accepted hop must not spend a re-anchor read, got %d", shipRepo.syncCalls)
	}
	if len(store.pruned) != 0 {
		t.Fatalf("an accepted hop must not reconcile adjacency, got %v", store.pruned)
	}
}
