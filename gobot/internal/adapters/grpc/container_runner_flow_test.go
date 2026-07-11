// gobot/internal/adapters/grpc/container_runner_flow_test.go
package grpc

import "testing"

// The sp-7yej lifecycle contract: a flow is removed on every TERMINAL exit reason,
// and preserved on the resumable "canceled" reason (the container re-adopts with the
// same id and re-publishes).
func TestFlowRemovalWanted_TerminalVsResumable(t *testing.T) {
	terminal := []string{"completed", "failed", "claim_failed", "stopped"}
	for _, reason := range terminal {
		if !flowRemovalWanted(reason) {
			t.Errorf("reason %q is terminal — want removal", reason)
		}
	}
	if flowRemovalWanted("canceled") {
		t.Error(`reason "canceled" is resumable (re-adopted) — must NOT remove the flow`)
	}
}
