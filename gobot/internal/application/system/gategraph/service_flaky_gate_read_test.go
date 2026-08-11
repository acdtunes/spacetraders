package gategraph

import (
	"context"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/andrescamacho/spacetraders-go/internal/domain/system"
)

// sp-dmxy5 — the SYNC-time analog of sp-hguq3 (which fixed the EXECUTION-time flaky read
// with a bounded re-read in jump_ship.resolveDestinationGateWaypoint). The SpaceTraders
// jump-gate endpoint intermittently returns a 200 OK with an empty/incomplete connections
// list — a transient, eventually-consistent read the API client's status-code retry never
// catches (an empty 200 is not a 429/5xx). fetchAndStore copies that list verbatim and
// Replace()s the cache, so an empty-200 for a CHARTED gate would erase a real, previously
// good edge set — making a valid connection invisible until the next successful sync (~24h).
//
// The fix mirrors sp-hguq3's spirit at sync time: re-read a bounded few times before
// persisting (a transient empty recovers on the next read), AND refuse to overwrite a
// known-good non-empty cached set with an empty read (connections are static within an era,
// so an empty read for a charted gate we already hold edges for is the flaky-200, never a
// real topology change). A genuinely-connectionless gate with no prior cache still syncs
// empty. No flag — armed on deploy.

// seqGateAPI returns a SEQUENCE of GetJumpGate connection lists (consumed front to back;
// the last entry repeats), so a test can model the live gate read coming back empty on one
// call and complete on the next. It counts reads so a test can prove the re-read is bounded
// and that the happy path stays at a single read. Every probed neighbor waypoint reads as
// built (the construction probe is not what this suite exercises).
type seqGateAPI struct {
	reads    [][]string
	getCalls int
}

func (a *seqGateAPI) GetJumpGate(_ context.Context, _, waypointSymbol, _ string) (*ports.JumpGateData, error) {
	i := a.getCalls
	a.getCalls++
	conns := []string{}
	if i < len(a.reads) {
		conns = a.reads[i]
	} else if len(a.reads) > 0 {
		conns = a.reads[len(a.reads)-1]
	}
	return &ports.JumpGateData{Symbol: waypointSymbol, Connections: conns}, nil
}

func (a *seqGateAPI) GetWaypoint(_ context.Context, _, waypointSymbol, _ string) (*ports.WaypointDetail, error) {
	return &ports.WaypointDetail{Symbol: waypointSymbol, IsUnderConstruction: false}, nil
}

func (a *seqGateAPI) CreateChart(_ context.Context, _, _ string) (*ports.ChartResult, error) {
	return &ports.ChartResult{}, nil
}

// syncReadStore forces the fetch-through path (Edges always reports a stale MISS so
// Connections re-fetches), resolves the origin's own gate without a system graph
// (GateWaypointOf hits), serves any PRIOR cached edge set through Adjacency (stale-inclusive,
// exactly as GormGateEdgeRepository.Adjacency returns a stale row), and captures every
// Replace so a test can prove a good set is NOT clobbered. Never backed off.
type syncReadStore struct {
	originGate   string
	prior        map[string][]system.GateEdge
	replaced     map[string][]system.GateEdge
	replaceCalls int
}

func (s *syncReadStore) Edges(_ context.Context, _ string) ([]system.GateEdge, bool, error) {
	return nil, false, nil
}
func (s *syncReadStore) GateWaypointOf(_ context.Context, _ string) (string, bool, error) {
	return s.originGate, true, nil
}
func (s *syncReadStore) Replace(_ context.Context, systemSymbol string, edges []system.GateEdge) error {
	if s.replaced == nil {
		s.replaced = map[string][]system.GateEdge{}
	}
	s.replaced[systemSymbol] = edges
	s.replaceCalls++
	return nil
}
func (s *syncReadStore) Adjacency(_ context.Context) (map[string][]system.GateEdge, error) {
	return s.prior, nil
}
func (s *syncReadStore) UnreadableState(_ context.Context, _ string) (int, time.Time, bool, error) {
	return 0, time.Time{}, false, nil
}
func (s *syncReadStore) MarkUnreadable(_ context.Context, _, _ string, _ time.Time) (int, error) {
	return 0, nil
}

// goodCA43Connections is CA43's real charted connection set (the incident gate), as raw API
// connection waypoints; X1-XD86-I54 is the destination the empty-200 made invisible.
func goodCA43Connections() []string {
	return []string{"X1-XD86-I54", "X1-RJ93-EF6X"}
}

// newSyncReadService wires the gate-graph service against a sequenced API and an in-memory
// store, with a MockClock so the between-re-read settle is instant.
func newSyncReadService(store *syncReadStore, api *seqGateAPI) *Service {
	clock := &shared.MockClock{CurrentTime: time.Date(2026, 7, 23, 2, 25, 0, 0, time.UTC)}
	return NewService(store, api, nil, &stubPlayerRepo{token: "tok"}, WithClock(clock))
}

// THE BUG. A charted gate we already hold a good edge set for reads back EMPTY on every
// live attempt (the intermittent empty-200). The sync must NOT erase the good cache with
// nothing: the valid connection (X1-XD86) stays visible and no empty set is persisted over
// the good one. Unguarded, fetchAndStore Replace()s the empty list and returns it — the
// connection vanished for ~24h.
func TestConnections_EmptyReadForChartedGate_DoesNotOverwriteGoodCache(t *testing.T) {
	store := &syncReadStore{
		originGate: "X1-CA43-I56",
		prior: map[string][]system.GateEdge{
			"X1-CA43": {
				{ConnectedSystem: "X1-XD86", GateWaypoint: "X1-XD86-I54"},
				{ConnectedSystem: "X1-RJ93", GateWaypoint: "X1-RJ93-EF6X"},
			},
		},
	}
	api := &seqGateAPI{reads: [][]string{{}}} // every read is a transient empty-200
	svc := newSyncReadService(store, api)

	edges, err := svc.Connections(context.Background(), "X1-CA43", 1)
	if err != nil {
		t.Fatalf("an empty-200 for a charted gate must not error the sync, got %v", err)
	}
	if e := findEdge(edges, "X1-XD86"); e.GateWaypoint != "X1-XD86-I54" {
		t.Fatalf("the valid connection X1-XD86 must stay visible (not erased by an empty read), got edges %+v", edges)
	}
	if e, ok := store.replaced["X1-CA43"]; ok && len(e) == 0 {
		t.Fatalf("an empty read must NEVER overwrite a known-good cached edge set (good connection lost for ~24h), but Replace was called with %d edges", len(e))
	}
}

// RECOVERY (bounded re-read): the first live read comes back empty (the transient blip);
// the second returns the real connection set. The sync must RE-READ and persist the real
// edges — the charted connection is real and reappears on the next read.
func TestConnections_TransientEmptyThenGood_ReReadsAndPersistsRealConnections(t *testing.T) {
	store := &syncReadStore{originGate: "X1-CA43-I56"}
	api := &seqGateAPI{reads: [][]string{
		{},                    // transient incomplete 200
		goodCA43Connections(), // real list — XD86 present
	}}
	svc := newSyncReadService(store, api)

	edges, err := svc.Connections(context.Background(), "X1-CA43", 1)
	if err != nil {
		t.Fatalf("a transient empty read must be re-read and recovered, got %v", err)
	}
	if e := findEdge(edges, "X1-XD86"); e.GateWaypoint != "X1-XD86-I54" {
		t.Fatalf("the re-read must recover the real connection X1-XD86, got edges %+v", edges)
	}
	if api.getCalls != 2 {
		t.Fatalf("expected exactly 2 gate reads (transient-empty then recovered), got %d", api.getCalls)
	}
	if persisted, ok := store.replaced["X1-CA43"]; !ok || findEdge(persisted, "X1-XD86").GateWaypoint != "X1-XD86-I54" {
		t.Fatalf("the recovered real edge set must be persisted, got replaced=%+v", store.replaced)
	}
}

// CONTROL (genuinely-empty gate, no prior cache): a gate that has no prior cached edges and
// reads back empty on every bounded attempt still syncs empty — the fix protects a KNOWN-GOOD
// set, it does not invent connections. Bounded: exactly maxGateSyncReadAttempts reads, then
// the empty set is persisted (no infinite re-read).
func TestConnections_GenuinelyEmptyGate_SyncsEmptyWhenNoPriorCache(t *testing.T) {
	store := &syncReadStore{originGate: "X1-CA43-I56"} // no prior cached set
	api := &seqGateAPI{reads: [][]string{{}}}          // empty on every read
	svc := newSyncReadService(store, api)

	edges, err := svc.Connections(context.Background(), "X1-CA43", 1)
	if err != nil {
		t.Fatalf("a genuinely-empty gate must sync cleanly, got %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("a genuinely-empty gate must sync empty, got %+v", edges)
	}
	if persisted, ok := store.replaced["X1-CA43"]; !ok || len(persisted) != 0 {
		t.Fatalf("a genuinely-empty gate with no prior cache must persist the empty set, got replaced=%+v ok=%v", store.replaced, ok)
	}
	if api.getCalls != maxGateSyncReadAttempts {
		t.Fatalf("empty reads must be bounded to exactly %d attempts, got %d", maxGateSyncReadAttempts, api.getCalls)
	}
}

// EFFICIENCY GUARD (no API spam): when the connections are present on the FIRST read (the
// overwhelming common case), the sync resolves them in exactly ONE gate read — the re-read
// machinery adds zero overhead on the happy path.
func TestConnections_NonEmptyFirstRead_SingleGateRead(t *testing.T) {
	store := &syncReadStore{originGate: "X1-CA43-I56"}
	api := &seqGateAPI{reads: [][]string{goodCA43Connections()}}
	svc := newSyncReadService(store, api)

	edges, err := svc.Connections(context.Background(), "X1-CA43", 1)
	if err != nil {
		t.Fatalf("happy-path sync must succeed, got %v", err)
	}
	if e := findEdge(edges, "X1-XD86"); e.GateWaypoint != "X1-XD86-I54" {
		t.Fatalf("expected the real connection X1-XD86, got edges %+v", edges)
	}
	if api.getCalls != 1 {
		t.Fatalf("happy path must cost exactly ONE gate read (no wasted re-reads), got %d", api.getCalls)
	}
}
