package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

	if err := client.CreateChart(context.Background(), "TORWIND-16", "token"); err != nil {
		t.Fatalf("a successful chart must return no error, got %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("chart must be a POST, got %s", gotMethod)
	}
	if gotPath != "/my/ships/TORWIND-16/chart" {
		t.Fatalf("chart must POST to /my/ships/{ship}/chart, got %s", gotPath)
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

	err := client.CreateChart(context.Background(), "TORWIND-16", "token")
	if err == nil {
		t.Fatal("an already-charted (400) response must surface as an error, got nil")
	}
	if !strings.Contains(err.Error(), "4230") {
		t.Fatalf("the error must carry the 4230 body so the caller can classify it as already-charted, got %q", err.Error())
	}
}
