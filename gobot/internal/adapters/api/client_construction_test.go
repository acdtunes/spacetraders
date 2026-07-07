package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Regression for sp-fi7q: SupplyConstruction must POST to the waypoint-scoped
// construction/supply endpoint. The previous path
// (/my/ships/{shipSymbol}/construction/supply) does not exist in the SpaceTraders
// API and returned 404 "Route not found", killing every construction delivery at
// the final supply step. The ship is identified via the request body, not the path.
func TestSupplyConstruction_UsesWaypointScopedEndpoint(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"construction":{"symbol":"X1-KA42-I53","materials":[],"isComplete":false},"cargo":{"capacity":40,"units":0,"inventory":[]}}}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	_, err := client.SupplyConstruction(context.Background(), "TORWIND-7", "X1-KA42-I53", "FAB_MATS", 40, "token")
	if err != nil {
		t.Fatalf("SupplyConstruction: unexpected error: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("expected POST, got %s", gotMethod)
	}

	wantPath := "/systems/X1-KA42/waypoints/X1-KA42-I53/construction/supply"
	if gotPath != wantPath {
		t.Fatalf("expected path %s, got %s", wantPath, gotPath)
	}

	// The ship is no longer in the URL path; the API identifies it via the body.
	if gotBody["shipSymbol"] != "TORWIND-7" {
		t.Fatalf("expected body shipSymbol TORWIND-7, got %v", gotBody["shipSymbol"])
	}
	if gotBody["tradeSymbol"] != "FAB_MATS" {
		t.Fatalf("expected body tradeSymbol FAB_MATS, got %v", gotBody["tradeSymbol"])
	}
}
