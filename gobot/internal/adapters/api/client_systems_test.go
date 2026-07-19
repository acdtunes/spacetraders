package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestListSystems_DecodesSymbolCoordsAndType pins the universe reader: GET /systems
// is paginated exactly like GET /systems/{s}/waypoints, and the client surfaces each
// system's symbol, galaxy coordinates, and type — the inputs the off-gate explorer ranks
// warp targets on. This is an adapter integration test against a real HTTP round-trip.
func TestListSystems_DecodesSymbolCoordsAndType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("page"); got != "2" {
			t.Errorf("expected page=2 in query, got %q", got)
		}
		if got := r.URL.Query().Get("limit"); got != "20" {
			t.Errorf("expected limit=20 in query, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{"symbol": "X1-AAAA", "type": "BLUE_STAR", "x": 10, "y": -20},
				{"symbol": "X1-BBBB", "type": "BLACK_HOLE", "x": -5, "y": 33}
			],
			"meta": {"total": 42, "page": 2, "limit": 20}
		}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)
	resp, err := client.ListSystems(context.Background(), "token", 2, 20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 systems, got %d", len(resp.Data))
	}
	first := resp.Data[0]
	if first.Symbol != "X1-AAAA" || first.Type != "BLUE_STAR" || first.X != 10 || first.Y != -20 {
		t.Fatalf("first system decoded wrong: %+v", first)
	}
	second := resp.Data[1]
	if second.Symbol != "X1-BBBB" || second.Type != "BLACK_HOLE" || second.X != -5 || second.Y != 33 {
		t.Fatalf("second system decoded wrong: %+v", second)
	}
	if resp.Meta.Total != 42 || resp.Meta.Page != 2 || resp.Meta.Limit != 20 {
		t.Fatalf("pagination meta decoded wrong: %+v", resp.Meta)
	}
}
