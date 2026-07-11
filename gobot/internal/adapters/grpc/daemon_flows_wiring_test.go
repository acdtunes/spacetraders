// gobot/internal/adapters/grpc/daemon_flows_wiring_test.go
package grpc

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/flowfeed"
)

func TestRegisterFlowsRoute_ServesRegistrySnapshot(t *testing.T) {
	reg := flowfeed.New()
	reg.Publish(flowfeed.Flow{ContainerID: "arb-run-SHIP-1-abc", Program: flowfeed.ProgramArb, Ship: "SHIP-1"})

	mux := http.NewServeMux()
	registerFlowsRoute(mux, reg)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/flows")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"arb-run-SHIP-1-abc"`) {
		t.Errorf("route did not serve the published flow; body=%s", body)
	}
}
