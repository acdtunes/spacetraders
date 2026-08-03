package navigation

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// sp-hguq3 — cross-system navigation bounced hulls FOREVER between two systems on a
// charted-but-live-rejected hop. Root cause (established by live investigation, NOT a
// stale cache): the gate_edges cache is a faithful, single-writer snapshot that MATCHES
// the live API; the failure is the LIVE jump-time read. SpaceTraders' jump-gate endpoint
// intermittently returns a 200 OK with an incomplete/empty connections list (a transient,
// eventually-consistent read — distinct from a 429, which the client already retries on
// status code). jump_ship treated that one bad read as a PERMANENT "no jump gate
// connection" and the container/coordinator re-flew the hull forever.
//
// The fix does NOT poison the cache. It re-reads the origin gate a BOUNDED few times when
// the intended destination is missing: a transient incomplete read recovers on the next
// read (the charted connection is real), the happy path costs exactly one read (no spam),
// and a destination genuinely absent from every read fails cleanly (no infinite bounce).

// stubSeqJumpAPIClient returns a SEQUENCE of GetJumpGate responses (consumed front to
// back; the last entry repeats), so a test can model the live gate read coming back
// incomplete on one call and complete on the next. It counts gate reads so a test can
// prove the re-read is bounded and that the happy path stays at a single read.
type stubSeqJumpAPIClient struct {
	ports.APIClient

	gateDataByCall      []*ports.JumpGateData
	result              *ports.JumpResult
	getJumpGateCalls    int
	jumpShipWaypointArg string
	jumpShipCalls       int
}

func (s *stubSeqJumpAPIClient) GetJumpGate(_ context.Context, _, _, _ string) (*ports.JumpGateData, error) {
	i := s.getJumpGateCalls
	s.getJumpGateCalls++
	if i < len(s.gateDataByCall) {
		return s.gateDataByCall[i], nil
	}
	if len(s.gateDataByCall) > 0 {
		return s.gateDataByCall[len(s.gateDataByCall)-1], nil
	}
	return &ports.JumpGateData{}, nil
}

func (s *stubSeqJumpAPIClient) JumpShip(_ context.Context, _ string, waypointSymbol string, _ string) (*ports.JumpResult, error) {
	s.jumpShipCalls++
	s.jumpShipWaypointArg = waypointSymbol
	return s.result, nil
}

// newFlakyGateJumpFixture wires a driveless hull sitting IN ORBIT on a complete jump
// gate (so the handler skips both the navigate-to-gate and orbit steps and goes straight
// to resolving the destination gate connection) against a sequenced gate-read API client.
func newFlakyGateJumpFixture(t *testing.T, client *stubSeqJumpAPIClient) (*JumpShipHandler, *JumpShipCommand) {
	t.Helper()
	gate := newJumpGateWaypoint(t, "X1-CA43-I56")
	ship := newDrivelessJumpTestShip(t, "TORWIND-56", gate)
	shipRepo := &stubJumpShipRepo{ship: ship}
	playerRepo := &stubJumpPlayerRepo{playerEntity: player.NewPlayer(shared.MustNewPlayerID(1), "AGENT", "test-token")}
	containerRepo := &stubJumpContainerRepo{}
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 23, 2, 25, 0, 0, time.UTC)}
	handler := NewJumpShipHandler(shipRepo, playerRepo, client, nil, containerRepo, nil, clock)

	playerIDInt := 1
	cmd := &JumpShipCommand{
		ShipSymbol:        "TORWIND-56",
		DestinationSystem: "X1-XD86",
		PlayerID:          &playerIDInt,
		SkipClaim:         true,
	}
	return handler, cmd
}

// realCA43Connections is CA43's actual charted/live gate connection set (from the live
// investigation), including the destination X1-XD86-I54 the incident could not find.
func realCA43Connections() []string {
	return []string{"X1-VB94-D23E", "X1-RJ93-EF6X", "X1-UZ45-C12Z", "X1-XD86-I54", "X1-ST87-I58", "X1-GR7-C24A"}
}

// THE FIX. The first live gate read comes back EMPTY (the transient incomplete read that
// stranded TORWIND-56); the second read returns the real connection set. The jump must
// RE-READ and RECOVER — reaching the destination — instead of treating the first bad read
// as a permanent "no jump gate connection" and bouncing the hull.
func TestJumpShip_DestinationMissingFromTransientRead_ReReadsAndRecovers(t *testing.T) {
	client := &stubSeqJumpAPIClient{
		gateDataByCall: []*ports.JumpGateData{
			{Symbol: "X1-CA43-I56", Connections: []string{}},            // transient incomplete 200
			{Symbol: "X1-CA43-I56", Connections: realCA43Connections()}, // real list — XD86 present
		},
		result: &ports.JumpResult{DestinationSystem: "X1-XD86", DestinationWaypoint: "X1-XD86-I54", CooldownSeconds: 60},
	}
	handler, cmd := newFlakyGateJumpFixture(t, client)

	resp, err := handler.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("a transient incomplete gate read must be re-read and recovered, not treated as a permanent 'no connection': %v", err)
	}
	jumpResp, ok := resp.(*JumpShipResponse)
	if !ok || !jumpResp.Success {
		t.Fatalf("expected a successful jump once the re-read recovered the connection, got %+v (ok=%v)", resp, ok)
	}
	// Recovered on the SECOND read: exactly two gate reads (one flaky, one good).
	if client.getJumpGateCalls != 2 {
		t.Fatalf("expected exactly 2 gate reads (transient-empty then recovered), got %d", client.getJumpGateCalls)
	}
	// And the jump fired at the destination gate waypoint the good read supplied.
	if client.jumpShipWaypointArg != "X1-XD86-I54" {
		t.Fatalf("expected the jump to target X1-XD86-I54 (resolved from the recovered read), got %q", client.jumpShipWaypointArg)
	}
}

// EFFICIENCY GUARD (no API spam): when the destination is present on the FIRST read (the
// overwhelming common case), the handler resolves it in exactly ONE gate read — the
// re-read machinery adds zero overhead on the happy path.
func TestJumpShip_DestinationPresentOnFirstRead_SingleGateRead(t *testing.T) {
	client := &stubSeqJumpAPIClient{
		gateDataByCall: []*ports.JumpGateData{
			{Symbol: "X1-CA43-I56", Connections: realCA43Connections()},
		},
		result: &ports.JumpResult{DestinationSystem: "X1-XD86", DestinationWaypoint: "X1-XD86-I54", CooldownSeconds: 60},
	}
	handler, cmd := newFlakyGateJumpFixture(t, client)

	resp, err := handler.Handle(context.Background(), cmd)
	if err != nil {
		t.Fatalf("happy-path jump must succeed, got error: %v", err)
	}
	if jumpResp, ok := resp.(*JumpShipResponse); !ok || !jumpResp.Success {
		t.Fatalf("expected a successful jump response, got %+v (ok=%v)", resp, ok)
	}
	if client.getJumpGateCalls != 1 {
		t.Fatalf("happy path must cost exactly ONE gate read (no wasted re-reads), got %d", client.getJumpGateCalls)
	}
}

// BOUNDED (no infinite bounce, no spam): when the destination is genuinely absent from
// EVERY live read, the handler gives up cleanly after exactly maxJumpGateReadAttempts
// reads and never fires a jump — instead of the previous behaviour where a hard error fed
// the container/coordinator an endless re-fly. This is the circuit that keeps a real
// (persistent) no-connection from becoming an API/fuel storm.
func TestJumpShip_DestinationGenuinelyAbsent_FailsCleanlyAfterBoundedReads(t *testing.T) {
	client := &stubSeqJumpAPIClient{
		// Every read lacks the destination system (X1-XD86): a genuinely severed/absent
		// connection, not a transient blip. The last entry repeats, so all reads miss.
		gateDataByCall: []*ports.JumpGateData{
			{Symbol: "X1-CA43-I56", Connections: []string{"X1-RJ93-EF6X", "X1-GR7-C24A"}},
		},
		result: &ports.JumpResult{DestinationSystem: "X1-XD86", DestinationWaypoint: "X1-XD86-I54", CooldownSeconds: 60},
	}
	handler, cmd := newFlakyGateJumpFixture(t, client)

	_, err := handler.Handle(context.Background(), cmd)
	if err == nil {
		t.Fatal("a destination absent from every live read must fail cleanly, not silently succeed")
	}
	// Bounded: exactly maxJumpGateReadAttempts reads — NOT one (before) and NOT unbounded.
	if client.getJumpGateCalls != maxJumpGateReadAttempts {
		t.Fatalf("expected exactly %d bounded gate reads before giving up, got %d", maxJumpGateReadAttempts, client.getJumpGateCalls)
	}
	// And no jump was ever fired at a non-existent connection.
	if client.jumpShipCalls != 0 {
		t.Fatalf("expected zero jump attempts when no connection exists, got %d", client.jumpShipCalls)
	}
}
