package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// CreateChart PUBLICLY charts the ship's current waypoint so an uncharted frontier gate
// becomes GetJumpGate-readable forever without a ship present. The live endpoint is
// POST /my/ships/{shipSymbol}/chart (charts the ship's CURRENT waypoint — no body-supplied
// waypoint). This pins the actual wire method+path the adapter sends, independent of the caller.
func TestCreateChart_PostsToShipChartPath(t *testing.T) {
	var gotMethod, gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated) // the chart endpoint returns 201 Created
		_, _ = w.Write([]byte(`{"data":{"chart":{"waypointSymbol":"X1-DA78-C24B"}}}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	if _, err := client.CreateChart(context.Background(), "TORWIND-16", "token"); err != nil {
		t.Fatalf("a successful chart must return no error, got %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("chart must be a POST, got %s", gotMethod)
	}
	if gotPath != "/my/ships/TORWIND-16/chart" {
		t.Fatalf("chart must POST to /my/ships/{ship}/chart, got %s", gotPath)
	}
}

// The 201 body carries a one-time charting REWARD (data.transaction.totalPrice) and the agent's
// post-reward balance (data.agent.credits). Discarding the body is the defect: the credits land
// in the balance with no ledger row and no re-anchor, so the chain gains an unexplained gap.
func TestCreateChart_ParsesRewardAndInBandAgentCredits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{
			"chart":{"waypointSymbol":"X1-DA78-C24B","submittedBy":"TORWIND"},
			"transaction":{"waypointSymbol":"X1-DA78-C24B","shipSymbol":"TORWIND-16","totalPrice":10000},
			"agent":{"symbol":"TORWIND","credits":1010000}}}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	result, err := client.CreateChart(context.Background(), "TORWIND-16", "token")
	if err != nil {
		t.Fatalf("a successful chart must return no error, got %v", err)
	}
	if result == nil {
		t.Fatal("a successful chart must return the parsed body, got nil")
	}
	if result.Reward != 10000 {
		t.Fatalf("Reward must carry data.transaction.totalPrice, want 10000, got %d", result.Reward)
	}
	if result.AgentCredits == nil {
		t.Fatal("AgentCredits must be set when the API returns data.agent, got nil")
	}
	if *result.AgentCredits != 1010000 {
		t.Fatalf("AgentCredits must carry data.agent.credits, want 1010000, got %d", *result.AgentCredits)
	}
	if result.WaypointSymbol != "X1-DA78-C24B" {
		t.Fatalf("WaypointSymbol must carry data.chart.waypointSymbol, got %q", result.WaypointSymbol)
	}
}

// An omitted agent block must stay distinguishable from a real zero balance, so the field is a
// POINTER: nil means "the API said nothing" and the ledger reconstructs instead of re-anchoring.
func TestCreateChart_OmittedAgentBlockLeavesCreditsNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"chart":{"waypointSymbol":"X1-DA78-C24B"},
			"transaction":{"totalPrice":7000}}}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	result, err := client.CreateChart(context.Background(), "TORWIND-16", "token")
	if err != nil {
		t.Fatalf("a successful chart must return no error, got %v", err)
	}
	if result.Reward != 7000 {
		t.Fatalf("Reward must still be parsed without an agent block, got %d", result.Reward)
	}
	if result.AgentCredits != nil {
		t.Fatalf("AgentCredits must stay nil when the API omits data.agent, got %d", *result.AgentCredits)
	}
}

// chartRewardServer is a stateful stand-in whose balance actually moves: charting pays
// chartRewardPrice and GET /my/agent reports the live balance, counting its own calls.
type chartRewardServer struct {
	mu       sync.Mutex
	credits  int
	getAgent int
}

const chartRewardPrice = 10000

func (s *chartRewardServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/chart") {
			s.credits += chartRewardPrice
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"data":{"chart":{"waypointSymbol":"X1-DA78-C24B"},`+
				`"transaction":{"totalPrice":%d},"agent":{"credits":%d}}}`, chartRewardPrice, s.credits)
			return
		}
		s.getAgent++
		fmt.Fprintf(w, `{"data":{"accountId":"A","symbol":"TORWIND","headquarters":"X1-HQ-A1",`+
			`"credits":%d,"startingFaction":"COSMIC"}}`, s.credits)
	}
}

// Charting RAISES the balance, so a cache left in place reads stale-LOW. That is the safe
// direction but a false one: the credits are real and a guard must be able to see them.
func TestCreateChart_InvalidatesTheAgentCache(t *testing.T) {
	fake := &chartRewardServer{credits: 1000000}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)
	ctx := context.Background()

	warm, err := client.GetAgent(ctx, "token")
	if err != nil {
		t.Fatalf("warming the agent cache: %v", err)
	}
	if warm.Credits != 1000000 {
		t.Fatalf("precondition: cached balance must be the pre-chart 1000000, got %d", warm.Credits)
	}

	if _, err := client.CreateChart(ctx, "TORWIND-16", "token"); err != nil {
		t.Fatalf("chart: %v", err)
	}

	after, err := client.GetAgent(ctx, "token")
	if err != nil {
		t.Fatalf("post-chart agent read: %v", err)
	}
	if after.Credits != 1000000+chartRewardPrice {
		t.Fatalf("the post-chart read returned the stale pre-reward balance %d: "+
			"CreateChart did not invalidate the agent cache", after.Credits)
	}
	if fake.getAgent != 2 {
		t.Fatalf("want 2 live Get Agent calls (warm + forced re-read), got %d", fake.getAgent)
	}
}

// The already-charted verdict (HTTP 400, code 4230) must surface as an ERROR that carries the
// wire body, so the gate-graph caller's isAlreadyCharted can classify it as a benign no-op and
// swallow it rather than error-spamming. This exercises the real request() typed-error
// path end-to-end against a test server — closing the loop the gategraph unit test asserts on.
func TestCreateChart_AlreadyCharted_SurfacesClassifiableError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":4230,"message":"Waypoint X1-DA78-C24B already charted."}}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	_, err := client.CreateChart(context.Background(), "TORWIND-16", "token")
	if err == nil {
		t.Fatal("an already-charted (400) response must surface as an error, got nil")
	}
	if !strings.Contains(err.Error(), "4230") {
		t.Fatalf("the error must carry the 4230 body so the caller can classify it as already-charted, got %q", err.Error())
	}
}
