// gobot/internal/adapters/flowfeed/handler_test.go
package flowfeed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fixedClock pins GeneratedAt so the golden bytes are stable.
func fixedRegistry() *Registry {
	r := New()
	r.now = func() time.Time { return time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC) }
	return r
}

func TestFlowsHandler_EmptyFleetReturnsEmptyFlows(t *testing.T) {
	srv := httptest.NewServer(NewFlowsHandler(fixedRegistry()))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	got := strings.TrimSpace(string(body))
	want := `{"flows":[],"generatedAt":"2026-07-11T12:00:00Z"}`
	if got != want {
		t.Errorf("empty payload mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestFlowsHandler_GoldenPayloadShape(t *testing.T) {
	reg := fixedRegistry()
	tourID := "tour-run-TORWIND-54-beba64e7"
	reg.Publish(Flow{
		ContainerID: tourID,
		Program:     ProgramTour,
		Ship:        "TORWIND-54",
		TourID:      &tourID,
		CurrentLeg: &Leg{
			From:       "X1-UU57-E21Z",
			To:         "X1-ZC66-C39A",
			DepartedAt: time.Date(2026, 7, 11, 11, 55, 0, 0, time.UTC),
			ArrivesAt:  time.Date(2026, 7, 11, 12, 3, 0, 0, time.UTC),
		},
		Cargo: []CargoItem{{Good: "EQUIPMENT", Units: 200}},
		RemainingHops: []Hop{{
			Waypoint: "X1-ZC66-F12F",
			Tranches: []Tranche{{Good: "ADVANCED_CIRCUITRY", IsBuy: false, Units: 100, ExpectedUnitPrice: 4100}},
		}},
		Projected: &Projection{Profit: 312000, RatePerHour: 445000},
		PlannedAt: time.Date(2026, 7, 11, 11, 54, 30, 0, time.UTC),
	})
	srv := httptest.NewServer(NewFlowsHandler(reg))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	got := strings.TrimSpace(string(body))
	want := `{"flows":[{"containerId":"tour-run-TORWIND-54-beba64e7","program":"tour",` +
		`"ship":"TORWIND-54","tourId":"tour-run-TORWIND-54-beba64e7",` +
		`"currentLeg":{"from":"X1-UU57-E21Z","to":"X1-ZC66-C39A",` +
		`"departedAt":"2026-07-11T11:55:00Z","arrivesAt":"2026-07-11T12:03:00Z"},` +
		`"cargo":[{"good":"EQUIPMENT","units":200}],` +
		`"remainingHops":[{"waypoint":"X1-ZC66-F12F","tranches":[{"good":"ADVANCED_CIRCUITRY",` +
		`"isBuy":false,"units":100,"expectedUnitPrice":4100}]}],` +
		`"projected":{"profit":312000,"ratePerHour":445000},` +
		`"plannedAt":"2026-07-11T11:54:30Z"}],"generatedAt":"2026-07-11T12:00:00Z"}`
	if got != want {
		t.Errorf("golden payload mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestFlowsHandler_RejectsNonGet(t *testing.T) {
	srv := httptest.NewServer(NewFlowsHandler(fixedRegistry()))
	t.Cleanup(srv.Close)
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST status = %d, want 405", resp.StatusCode)
	}
}

func TestFlowsHandler_IsReadOnly(t *testing.T) {
	reg := fixedRegistry()
	reg.Publish(Flow{ContainerID: "c1"})
	h := NewFlowsHandler(reg)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/flows", nil))
	}
	if n := len(reg.Snapshot().Flows); n != 1 {
		t.Fatalf("GET must not mutate the registry; want 1 flow, got %d", n)
	}
}
