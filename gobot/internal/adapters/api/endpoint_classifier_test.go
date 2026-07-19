package api

import "testing"

func TestClassifyNegotiateContractRealPath(t *testing.T) {
	// The real SpaceTraders endpoint is POST /my/ships/{symbol}/negotiate/contract.
	// The metrics label must resolve to the human-readable name, not the raw pattern.
	got := apiEndpointClassifier.classify("/my/ships/SHIP-1/negotiate/contract")
	if got != "Negotiate Contract" {
		t.Fatalf("expected %q, got %q", "Negotiate Contract", got)
	}
}

// extractShipSymbol identifies which hull an API request belongs to so the
// budget tracker can attribute req/s per hull, not just globally.
func TestExtractShipSymbolFromShipScopedPath(t *testing.T) {
	got := extractShipSymbol("/my/ships/TORWIND-1/dock")
	if got != "TORWIND-1" {
		t.Fatalf("expected %q, got %q", "TORWIND-1", got)
	}
}

func TestExtractShipSymbolFromNestedShipScopedPath(t *testing.T) {
	got := extractShipSymbol("/my/ships/TORWIND-1/negotiate/contract")
	if got != "TORWIND-1" {
		t.Fatalf("expected %q, got %q", "TORWIND-1", got)
	}
}

func TestExtractShipSymbolStripsQueryString(t *testing.T) {
	got := extractShipSymbol("/my/ships/TORWIND-1/purchase?page=1&limit=20")
	if got != "TORWIND-1" {
		t.Fatalf("expected %q, got %q", "TORWIND-1", got)
	}
}

func TestExtractShipSymbolFromFleetWidePath_ReturnsEmpty(t *testing.T) {
	// /my/ships (no trailing segment) is the fleet-wide list/purchase
	// endpoint, not scoped to a single hull.
	got := extractShipSymbol("/my/ships?page=1&limit=20")
	if got != "" {
		t.Fatalf("expected empty hull for fleet-wide path, got %q", got)
	}
}

func TestExtractShipSymbolFromNonShipPath_ReturnsEmpty(t *testing.T) {
	got := extractShipSymbol("/my/agent")
	if got != "" {
		t.Fatalf("expected empty hull for non-ship path, got %q", got)
	}
}

func TestExtractShipSymbolFromWaypointPath_ReturnsEmpty(t *testing.T) {
	// Waypoint/system symbols start with "X" and must not be mistaken for a
	// ship symbol even though they also contain hyphens.
	got := extractShipSymbol("/systems/X1-FB5/waypoints/X1-FB5-I61/market")
	if got != "" {
		t.Fatalf("expected empty hull for waypoint-scoped path, got %q", got)
	}
}
