package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// sp-0xd0: the live SpaceTraders warp API takes the destination as
// "waypointSymbol" in the POST body (mirroring navigate) and returns the ship's
// fuel + nav.route envelope. This asserts the actual wire format WarpShip sends
// and that the post-warp fuel state + arrival time are parsed back onto the
// Result, so the RouteExecutor's fuel accounting after a warp is honest.
func TestWarpShipPostsWaypointSymbolAndParsesFuelAndArrival(t *testing.T) {
	var capturedBody map[string]interface{}
	var capturedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"fuel": {"current": 520, "capacity": 800, "consumed": {"amount": 280}},
				"nav": {
					"systemSymbol": "X1-SYSB",
					"waypointSymbol": "X1-SYSB-B1",
					"route": {"departureTime": "2026-01-01T00:00:00Z", "arrival": "2026-01-01T00:05:00Z"}
				}
			}
		}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	result, err := client.WarpShip(context.Background(), "EXPLORER-1", "X1-SYSB-B1", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if capturedPath != "/my/ships/EXPLORER-1/warp" {
		t.Fatalf("expected POST to /my/ships/EXPLORER-1/warp, got %s", capturedPath)
	}
	if _, hasSystemSymbol := capturedBody["systemSymbol"]; hasSystemSymbol {
		t.Fatalf("expected request body NOT to contain systemSymbol, got: %+v", capturedBody)
	}
	if got := capturedBody["waypointSymbol"]; got != "X1-SYSB-B1" {
		t.Fatalf("expected waypointSymbol=X1-SYSB-B1, got %v", got)
	}

	if result.Destination != "X1-SYSB-B1" {
		t.Fatalf("expected Destination=X1-SYSB-B1, got %q", result.Destination)
	}
	if result.FuelCurrent != 520 || result.FuelCapacity != 800 {
		t.Fatalf("expected post-warp fuel 520/800, got %d/%d", result.FuelCurrent, result.FuelCapacity)
	}
	if result.FuelConsumed != 280 {
		t.Fatalf("expected FuelConsumed=280, got %d", result.FuelConsumed)
	}
	if result.ArrivalTimeStr != "2026-01-01T00:05:00Z" {
		t.Fatalf("expected ArrivalTimeStr carried through, got %q", result.ArrivalTimeStr)
	}
}
