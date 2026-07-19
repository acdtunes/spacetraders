package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// (production wiring, permanent case): a terminal non-2xx GetJumpGate response must
// surface as a typed *ports.APIError carrying the status code, so the gate graph can negative-
// cache a PERMANENT 400 (uncharted / no ship present / not a gate). The error STRING stays
// byte-identical ("API error (status %d): %s") so the existing message/JSON parsers keep matching.
// This proves the REAL adapter — not just a fake — produces the typed error the gategraph
// classifier keys on; without it the classifier would be correct but never triggered live.
func TestGetJumpGate_400_SurfacesTypedClientError(t *testing.T) {
	const body = `{"error":{"code":4001,"message":"Waypoint X1-XX56-GATE is uncharted."}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	_, err := client.GetJumpGate(context.Background(), "X1-XX56", "X1-XX56-GATE", "token")
	if err == nil {
		t.Fatal("expected a 400 to surface as an error")
	}

	var apiErr *domainPorts.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("a non-2xx GetJumpGate must surface a *ports.APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected StatusCode 400, got %d", apiErr.StatusCode)
	}
	if !apiErr.IsClientError() {
		t.Fatal("a 400 must classify as a client error (the permanent, do-not-retry-soon verdict)")
	}
	// The legacy string is preserved verbatim (status line + body), so the body/error-code
	// parsers (cooldown, insufficient-credits, dock/orbit classifiers) still match.
	if !strings.Contains(err.Error(), "API error (status 400)") || !strings.Contains(err.Error(), `"code":4001`) {
		t.Fatalf("the error string must be preserved verbatim, got %q", err.Error())
	}
}

// (production wiring, transient case): a 5xx is retried and, once exhausted, surfaces as
// a "max retries exceeded" error — NOT a *ports.APIError. This is the other half of the boundary:
// the gategraph classifier declines to negative-cache it (isPermanentGateAbsence is false), so a
// transient server blip is re-probed on the next miss rather than suppressed for the backoff window.
func TestGetJumpGate_503_IsTransient_NotTypedAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("upstream unavailable"))
	}))
	defer server.Close()

	// maxRetries=0: the single attempt is classified retryable, immediately exhausts, and returns
	// the transient "max retries exceeded" wrapper (no real retries, so the test is fast).
	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	_, err := client.GetJumpGate(context.Background(), "X1-XX56", "X1-XX56-GATE", "token")
	if err == nil {
		t.Fatal("expected a 503 to surface as an error")
	}
	var apiErr *domainPorts.APIError
	if errors.As(err, &apiErr) {
		t.Fatalf("a transient 5xx must NOT surface as a *ports.APIError (it must not be negative-cached), got %v", err)
	}
	if !strings.Contains(err.Error(), "max retries exceeded") {
		t.Fatalf("a retried-then-exhausted 5xx must report max retries exceeded, got %q", err.Error())
	}
}
