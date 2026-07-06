package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNavigateShipCalculatesArrivalSecondsFromRoute(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"fuel": {"current": 380, "capacity": 400, "consumed": {"amount": 20}},
				"nav": {
					"waypointSymbol": "X1-ABC-DEST",
					"route": {
						"departureTime": "2024-01-01T12:00:00Z",
						"arrival": "2024-01-01T12:01:00Z"
					}
				}
			}
		}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	result, err := client.NavigateShip(context.Background(), "SHIP-1", "X1-ABC-DEST", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ArrivalTime != 60 {
		t.Fatalf("expected ArrivalTime 60 seconds, got %d", result.ArrivalTime)
	}
	if result.ArrivalTimeStr != "2024-01-01T12:01:00Z" {
		t.Fatalf("expected ArrivalTimeStr to be preserved, got %q", result.ArrivalTimeStr)
	}
}
