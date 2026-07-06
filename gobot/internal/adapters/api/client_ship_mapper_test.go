package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
)

// fullShipJSON returns a single ship object with every field populated:
// role, flightMode, IN_TRANSIT route, cooldown, engine, frame, modules, cargo.
func fullShipJSON(symbol string) string {
	return fmt.Sprintf(`{
		"symbol": %q,
		"registration": {"role": "EXCAVATOR"},
		"nav": {
			"systemSymbol": "X1-SYS",
			"waypointSymbol": "X1-SYS-WP",
			"status": "IN_TRANSIT",
			"flightMode": "CRUISE",
			"route": {"arrival": "2024-01-01T12:05:00Z"}
		},
		"fuel": {"current": 380, "capacity": 400},
		"cargo": {
			"capacity": 60,
			"units": 10,
			"inventory": [{"symbol": "IRON_ORE", "name": "Iron Ore", "description": "raw", "units": 10}]
		},
		"cooldown": {"expiration": "2024-01-01T12:10:00Z"},
		"engine": {"speed": 30},
		"frame": {"symbol": "FRAME_MINER"},
		"modules": [
			{"symbol": "MODULE_MINERAL_PROCESSOR_I", "capacity": 0, "range": 0},
			{"symbol": "MODULE_CARGO_HOLD_I", "capacity": 15, "range": 0}
		]
	}`, symbol)
}

func newTestClient(handler http.HandlerFunc) (*SpaceTradersClient, func()) {
	server := httptest.NewServer(handler)
	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)
	return client, server.Close
}

// assertCommonShipFields checks the fields that every mapping path already
// populates identically. The divergent fields (Role, FlightMode, Modules,
// CooldownExpiration) are asserted per-path by the individual tests.
func assertCommonShipFields(t *testing.T, ship *navigation.ShipData, symbol string) {
	t.Helper()
	if ship == nil {
		t.Fatal("expected ship, got nil")
	}
	if ship.Symbol != symbol {
		t.Errorf("Symbol: want %q, got %q", symbol, ship.Symbol)
	}
	if ship.Location != "X1-SYS-WP" {
		t.Errorf("Location: want X1-SYS-WP, got %q", ship.Location)
	}
	if ship.NavStatus != "IN_TRANSIT" {
		t.Errorf("NavStatus: want IN_TRANSIT, got %q", ship.NavStatus)
	}
	if ship.ArrivalTime != "2024-01-01T12:05:00Z" {
		t.Errorf("ArrivalTime: want 2024-01-01T12:05:00Z, got %q", ship.ArrivalTime)
	}
	if ship.FuelCurrent != 380 || ship.FuelCapacity != 400 {
		t.Errorf("Fuel: want 380/400, got %d/%d", ship.FuelCurrent, ship.FuelCapacity)
	}
	if ship.CargoCapacity != 60 || ship.CargoUnits != 10 {
		t.Errorf("Cargo cap/units: want 60/10, got %d/%d", ship.CargoCapacity, ship.CargoUnits)
	}
	if ship.EngineSpeed != 30 {
		t.Errorf("EngineSpeed: want 30, got %d", ship.EngineSpeed)
	}
	if ship.FrameSymbol != "FRAME_MINER" {
		t.Errorf("FrameSymbol: want FRAME_MINER, got %q", ship.FrameSymbol)
	}
	if ship.Cargo == nil {
		t.Fatal("Cargo: want non-nil")
	}
	if len(ship.Cargo.Inventory) != 1 || ship.Cargo.Inventory[0].Symbol != "IRON_ORE" {
		t.Errorf("Cargo.Inventory: want [IRON_ORE], got %+v", ship.Cargo.Inventory)
	}
}

func assertFullModules(t *testing.T, ship *navigation.ShipData) {
	t.Helper()
	if len(ship.Modules) != 2 {
		t.Fatalf("Modules: want 2, got %d", len(ship.Modules))
	}
	if ship.Modules[0].Symbol != "MODULE_MINERAL_PROCESSOR_I" {
		t.Errorf("Modules[0].Symbol: want MODULE_MINERAL_PROCESSOR_I, got %q", ship.Modules[0].Symbol)
	}
	if ship.Modules[1].Symbol != "MODULE_CARGO_HOLD_I" || ship.Modules[1].Capacity != 15 {
		t.Errorf("Modules[1]: want MODULE_CARGO_HOLD_I/15, got %q/%d", ship.Modules[1].Symbol, ship.Modules[1].Capacity)
	}
}

// GetShip is the reference path: it produces the full ShipData field set.
func TestGetShipMapsFullFieldSet(t *testing.T) {
	client, closeFn := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data": %s}`, fullShipJSON("SHIP-1"))
	})
	defer closeFn()

	ship, err := client.GetShip(context.Background(), "SHIP-1", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertCommonShipFields(t, ship, "SHIP-1")
	if ship.FlightMode != "CRUISE" {
		t.Errorf("FlightMode: want CRUISE, got %q", ship.FlightMode)
	}
	if ship.Role != "EXCAVATOR" {
		t.Errorf("Role: want EXCAVATOR, got %q", ship.Role)
	}
	if ship.CooldownExpiration != "2024-01-01T12:10:00Z" {
		t.Errorf("CooldownExpiration: want 2024-01-01T12:10:00Z, got %q", ship.CooldownExpiration)
	}
	assertFullModules(t, ship)
}

// ListShips maps the full ShipData field set at parity with GetShip,
// including FlightMode, Role, Modules and CooldownExpiration.
func TestListShipsMapsShipFields(t *testing.T) {
	client, closeFn := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			_, _ = fmt.Fprintf(w, `{"data": [%s], "meta": {"total": 1, "page": 1, "limit": 20}}`, fullShipJSON("SHIP-1"))
			return
		}
		_, _ = fmt.Fprint(w, `{"data": [], "meta": {"total": 1, "page": 2, "limit": 20}}`)
	})
	defer closeFn()

	ships, err := client.ListShips(context.Background(), "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ships) != 1 {
		t.Fatalf("expected 1 ship, got %d", len(ships))
	}
	ship := ships[0]

	assertCommonShipFields(t, ship, "SHIP-1")
	if ship.FlightMode != "CRUISE" {
		t.Errorf("FlightMode: want CRUISE, got %q", ship.FlightMode)
	}
	if ship.Role != "EXCAVATOR" {
		t.Errorf("Role: want EXCAVATOR, got %q", ship.Role)
	}
	// HEALED: ListShips now populates the previously-dropped fields.
	if ship.CooldownExpiration != "2024-01-01T12:10:00Z" {
		t.Errorf("CooldownExpiration: want 2024-01-01T12:10:00Z, got %q", ship.CooldownExpiration)
	}
	assertFullModules(t, ship)
}

// PurchaseShip routes ship JSON through convertShipData, mapping the full
// ShipData field set including FlightMode, Role, CooldownExpiration and Modules.
func TestPurchaseShipMapsShipFields(t *testing.T) {
	client, closeFn := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data": {
			"agent": {"accountId": "acc", "symbol": "AGENT", "headquarters": "X1-SYS-WP", "credits": 1000, "startingFaction": "COSMIC"},
			"ship": %s,
			"transaction": {"waypointSymbol": "X1-SYS-WP", "shipSymbol": "SHIP-1", "shipType": "SHIP_MINING_DRONE", "price": 500, "agentSymbol": "AGENT", "timestamp": "2024-01-01T12:00:00Z"}
		}}`, fullShipJSON("SHIP-1"))
	})
	defer closeFn()

	result, err := client.PurchaseShip(context.Background(), "SHIP_MINING_DRONE", "X1-SYS-WP", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ship := result.Ship

	assertCommonShipFields(t, ship, "SHIP-1")
	// HEALED: convertShipData now populates the previously-dropped fields.
	if ship.FlightMode != "CRUISE" {
		t.Errorf("FlightMode: want CRUISE, got %q", ship.FlightMode)
	}
	if ship.Role != "EXCAVATOR" {
		t.Errorf("Role: want EXCAVATOR, got %q", ship.Role)
	}
	if ship.CooldownExpiration != "2024-01-01T12:10:00Z" {
		t.Errorf("CooldownExpiration: want 2024-01-01T12:10:00Z, got %q", ship.CooldownExpiration)
	}
	assertFullModules(t, ship)
}

// PurchaseShip must return an error (not panic) when the ship JSON is malformed,
// e.g. a numeric field arrives as a string.
func TestPurchaseShipReturnsErrorOnMalformedShipJSON(t *testing.T) {
	client, closeFn := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data": {
			"agent": {"accountId": "acc", "symbol": "AGENT", "headquarters": "X1-SYS-WP", "credits": 1000, "startingFaction": "COSMIC"},
			"ship": {
				"symbol": "SHIP-1",
				"nav": {"waypointSymbol": "X1-SYS-WP", "status": "DOCKED"},
				"fuel": {"current": "not-a-number", "capacity": 400},
				"cargo": {"capacity": 60, "units": 10, "inventory": []},
				"engine": {"speed": 30},
				"frame": {"symbol": "FRAME_MINER"}
			},
			"transaction": {"waypointSymbol": "X1-SYS-WP", "shipSymbol": "SHIP-1", "shipType": "SHIP_MINING_DRONE", "price": 500, "agentSymbol": "AGENT", "timestamp": "2024-01-01T12:00:00Z"}
		}}`)
	})
	defer closeFn()

	_, err := client.PurchaseShip(context.Background(), "SHIP_MINING_DRONE", "X1-SYS-WP", "token")
	if err == nil {
		t.Fatal("expected error for malformed ship JSON, got nil")
	}
}

// PurchaseShip must reject ship JSON whose symbol is missing/empty.
func TestPurchaseShipReturnsErrorOnMissingSymbol(t *testing.T) {
	client, closeFn := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"data": {
			"agent": {"accountId": "acc", "symbol": "AGENT", "headquarters": "X1-SYS-WP", "credits": 1000, "startingFaction": "COSMIC"},
			"ship": {
				"nav": {"waypointSymbol": "X1-SYS-WP", "status": "DOCKED"},
				"fuel": {"current": 380, "capacity": 400},
				"cargo": {"capacity": 60, "units": 10, "inventory": []},
				"engine": {"speed": 30},
				"frame": {"symbol": "FRAME_MINER"}
			},
			"transaction": {"waypointSymbol": "X1-SYS-WP", "shipSymbol": "SHIP-1", "shipType": "SHIP_MINING_DRONE", "price": 500, "agentSymbol": "AGENT", "timestamp": "2024-01-01T12:00:00Z"}
		}}`)
	})
	defer closeFn()

	_, err := client.PurchaseShip(context.Background(), "SHIP_MINING_DRONE", "X1-SYS-WP", "token")
	if err == nil {
		t.Fatal("expected error for missing ship symbol, got nil")
	}
}

// PurchaseShip must reject ship JSON that is missing a required section
// (nav/fuel/cargo/engine) rather than silently succeeding with zero values.
func TestPurchaseShipReturnsErrorOnMissingRequiredSection(t *testing.T) {
	cases := map[string]string{
		"nav": `{
			"symbol": "SHIP-1",
			"fuel": {"current": 380, "capacity": 400},
			"cargo": {"capacity": 60, "units": 10, "inventory": []},
			"engine": {"speed": 30},
			"frame": {"symbol": "FRAME_MINER"}
		}`,
		"fuel": `{
			"symbol": "SHIP-1",
			"nav": {"waypointSymbol": "X1-SYS-WP", "status": "DOCKED"},
			"cargo": {"capacity": 60, "units": 10, "inventory": []},
			"engine": {"speed": 30},
			"frame": {"symbol": "FRAME_MINER"}
		}`,
		"cargo": `{
			"symbol": "SHIP-1",
			"nav": {"waypointSymbol": "X1-SYS-WP", "status": "DOCKED"},
			"fuel": {"current": 380, "capacity": 400},
			"engine": {"speed": 30},
			"frame": {"symbol": "FRAME_MINER"}
		}`,
		"engine": `{
			"symbol": "SHIP-1",
			"nav": {"waypointSymbol": "X1-SYS-WP", "status": "DOCKED"},
			"fuel": {"current": 380, "capacity": 400},
			"cargo": {"capacity": 60, "units": 10, "inventory": []},
			"frame": {"symbol": "FRAME_MINER"}
		}`,
	}

	for missingSection, shipJSON := range cases {
		t.Run(missingSection, func(t *testing.T) {
			client, closeFn := newTestClient(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"data": {
					"agent": {"accountId": "acc", "symbol": "AGENT", "headquarters": "X1-SYS-WP", "credits": 1000, "startingFaction": "COSMIC"},
					"ship": %s,
					"transaction": {"waypointSymbol": "X1-SYS-WP", "shipSymbol": "SHIP-1", "shipType": "SHIP_MINING_DRONE", "price": 500, "agentSymbol": "AGENT", "timestamp": "2024-01-01T12:00:00Z"}
				}}`, shipJSON)
			})
			defer closeFn()

			_, err := client.PurchaseShip(context.Background(), "SHIP_MINING_DRONE", "X1-SYS-WP", "token")
			if err == nil {
				t.Fatalf("expected error for missing %q section, got nil", missingSection)
			}
		})
	}
}
