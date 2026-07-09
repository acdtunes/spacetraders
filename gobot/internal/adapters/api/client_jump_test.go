package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// sp-n0x7 round 2: the live SpaceTraders jump API requires the destination
// as "waypointSymbol" in the POST body - not "systemSymbol" - and 422s with
// "waypointSymbol Required, received undefined" otherwise. This asserts the
// actual wire format JumpShip sends, independent of what the caller names
// its parameter.
func TestJumpShipPostsWaypointSymbolNotSystemSymbol(t *testing.T) {
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"nav": {"systemSymbol": "X1-GQ92", "waypointSymbol": "X1-GQ92-I51"},
				"cooldown": {"remainingSeconds": 60},
				"transaction": {"totalPrice": 0}
			}
		}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	result, err := client.JumpShip(context.Background(), "SHIP-1", "X1-GQ92-I51", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, hasSystemSymbol := capturedBody["systemSymbol"]; hasSystemSymbol {
		t.Fatalf("expected request body NOT to contain systemSymbol, got: %+v", capturedBody)
	}
	got, ok := capturedBody["waypointSymbol"]
	if !ok {
		t.Fatalf("expected request body to contain waypointSymbol, got: %+v", capturedBody)
	}
	if got != "X1-GQ92-I51" {
		t.Fatalf("expected waypointSymbol=X1-GQ92-I51, got %v", got)
	}

	if result.DestinationWaypoint != "X1-GQ92-I51" {
		t.Fatalf("expected parsed DestinationWaypoint=X1-GQ92-I51, got %q", result.DestinationWaypoint)
	}
}
