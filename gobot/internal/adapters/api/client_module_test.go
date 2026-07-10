package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestInstallShipModulePostsSymbolAndParsesResponse asserts the wire format
// InstallShipModule sends ({"symbol": <module>}) and that it parses the fee, the
// post-install cargo capacity, the agent balance, and the modules list from the
// 201 response.
func TestInstallShipModulePostsSymbolAndParsesResponse(t *testing.T) {
	var capturedBody map[string]interface{}
	var capturedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&capturedBody); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"data": {
				"agent": {"credits": 812345},
				"modules": [
					{"symbol": "MODULE_CARGO_HOLD_II", "name": "Cargo Hold II", "capacity": 80, "requirements": {}},
					{"symbol": "MODULE_CARGO_HOLD_III", "name": "Cargo Hold III", "capacity": 120, "requirements": {}}
				],
				"cargo": {"capacity": 320, "units": 0, "inventory": []},
				"transaction": {"waypointSymbol": "X1-JP61-A1", "shipSymbol": "SHIP-1", "tradeSymbol": "MODULE_CARGO_HOLD_III", "totalPrice": 4200, "timestamp": "2026-07-10T00:00:00Z"}
			}
		}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	result, err := client.InstallShipModule(context.Background(), "SHIP-1", "MODULE_CARGO_HOLD_III", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.HasSuffix(capturedPath, "/my/ships/SHIP-1/modules/install") {
		t.Fatalf("expected install path, got %q", capturedPath)
	}
	if got := capturedBody["symbol"]; got != "MODULE_CARGO_HOLD_III" {
		t.Fatalf("expected body symbol=MODULE_CARGO_HOLD_III, got %v", got)
	}
	if result.Fee != 4200 {
		t.Fatalf("expected fee 4200, got %d", result.Fee)
	}
	if result.CargoCapacity != 320 {
		t.Fatalf("expected new cargo capacity 320, got %d", result.CargoCapacity)
	}
	if result.AgentCredits == nil || *result.AgentCredits != 812345 {
		t.Fatalf("expected agent credits 812345, got %v", result.AgentCredits)
	}
	if len(result.Modules) != 2 || result.Modules[1].Symbol != "MODULE_CARGO_HOLD_III" || result.Modules[1].Capacity != 120 {
		t.Fatalf("unexpected modules parse: %+v", result.Modules)
	}
}

// TestRemoveShipModulePostsSymbolAndParsesResponse mirrors the install test for
// the remove endpoint.
func TestRemoveShipModulePostsSymbolAndParsesResponse(t *testing.T) {
	var capturedPath string
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
			"data": {
				"agent": {"credits": 500000},
				"modules": [{"symbol": "MODULE_CARGO_HOLD_II", "name": "Cargo Hold II", "capacity": 80, "requirements": {}}],
				"cargo": {"capacity": 200, "units": 1, "inventory": [{"symbol": "MODULE_CARGO_HOLD_III", "name": "Cargo Hold III", "description": "x", "units": 1}]},
				"transaction": {"waypointSymbol": "X1-JP61-A1", "shipSymbol": "SHIP-1", "tradeSymbol": "MODULE_CARGO_HOLD_III", "totalPrice": 4200, "timestamp": "2026-07-10T00:00:00Z"}
			}
		}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	result, err := client.RemoveShipModule(context.Background(), "SHIP-1", "MODULE_CARGO_HOLD_III", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(capturedPath, "/my/ships/SHIP-1/modules/remove") {
		t.Fatalf("expected remove path, got %q", capturedPath)
	}
	if got := capturedBody["symbol"]; got != "MODULE_CARGO_HOLD_III" {
		t.Fatalf("expected body symbol=MODULE_CARGO_HOLD_III, got %v", got)
	}
	if result.Fee != 4200 {
		t.Fatalf("expected fee 4200, got %d", result.Fee)
	}
	if result.CargoCapacity != 200 {
		t.Fatalf("expected post-remove cargo capacity 200, got %d", result.CargoCapacity)
	}
}

// TestGetShipModulesParsesResponse asserts GetShipModules parses the installed
// module list from the GET /modules response.
func TestGetShipModulesParsesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": [
				{"symbol": "MODULE_CARGO_HOLD_III", "name": "Cargo Hold III", "capacity": 120, "requirements": {}},
				{"symbol": "MODULE_CREW_QUARTERS_I", "name": "Crew Quarters I", "capacity": 8, "requirements": {}}
			]
		}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	modules, err := client.GetShipModules(context.Background(), "SHIP-1", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(modules) != 2 {
		t.Fatalf("expected 2 modules, got %d", len(modules))
	}
	if modules[0].Symbol != "MODULE_CARGO_HOLD_III" || modules[0].Capacity != 120 {
		t.Fatalf("unexpected first module: %+v", modules[0])
	}
}

// TestInstallShipModuleSurfacesModuleNotInCargoError asserts that a 4xx error
// (module not in cargo) is surfaced to the caller rather than silently
// swallowed.
func TestInstallShipModuleSurfacesModuleNotInCargoError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":4256,"message":"Ship module MODULE_CARGO_HOLD_III is not in your cargo."}}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	result, err := client.InstallShipModule(context.Background(), "SHIP-1", "MODULE_CARGO_HOLD_III", "token")
	if err == nil {
		t.Fatalf("expected an error for module-not-in-cargo, got nil (result=%+v)", result)
	}
	if result != nil {
		t.Fatalf("expected nil result on error, got %+v", result)
	}
	if !strings.Contains(err.Error(), "not in your cargo") {
		t.Fatalf("expected the API error message to be surfaced, got: %v", err)
	}
}
