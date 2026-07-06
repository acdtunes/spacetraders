package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
)

// stubAPIClient satisfies domainPorts.APIClient without implementing every method.
// Embedding the interface gives a valid implementation; unused methods panic if
// called, but this test only checks identity, never invokes API methods.
type stubAPIClient struct {
	domainPorts.APIClient
}

// TestGetAPIClientReturnsInjectedClient reproduces st-0tw.1: construction ops must
// use the shared, rate-limited API client injected at construction time, not a
// fresh SpaceTradersClient whose own limiter bypasses the account-wide budget.
func TestGetAPIClientReturnsInjectedClient(t *testing.T) {
	injected := &stubAPIClient{}
	s := &DaemonServer{apiClient: injected}

	got := s.getAPIClient()

	require.Same(t, injected, got,
		"getAPIClient must return the injected shared client, not a fresh instance")
}
