package grpc

import (
	"testing"

	contractScalerCmd "github.com/andrescamacho/spacetraders-go/internal/application/contractscaler/commands"
)

// The contract scaler must rebuild identically from its persisted launch config so a daemon restart
// re-adopts it (RULINGS #2). These pin the registry round-trip: the "contract_scaler" spec is
// registered, and buildCommandForType reconstructs the command — LIVE by default (absent disable key),
// identity preserved.

func newContractScalerTestServer() *DaemonServer {
	s := &DaemonServer{containerSpecs: make(map[string]ContainerSpec)}
	s.registerContainerSpecs()
	return s
}

func buildRecoveredContractScalerCommand(t *testing.T, s *DaemonServer, persisted map[string]interface{}) *contractScalerCmd.RunContractScalerCommand {
	t.Helper()
	got, err := s.buildCommandForType("contract_scaler", persisted, 7, "contract-scaler-1")
	if err != nil {
		t.Fatalf("buildCommandForType(contract_scaler): %v", err)
	}
	cmd, ok := got.(*contractScalerCmd.RunContractScalerCommand)
	if !ok {
		t.Fatalf("expected *RunContractScalerCommand, got %T", got)
	}
	return cmd
}

// ABSENT config → coordinator LIVE once launched, identity preserved. The restart-recovery build carries
// Disabled=false with no disable flag anywhere, so a recovered scaler resumes ramping (no dark-shipping);
// the arm that gates whether it launches AT ALL is the bootstrap early-scaling flag, not this build.
func TestContractScaler_RecoveryBuild_LiveByDefault_IdentityPreserved(t *testing.T) {
	s := newContractScalerTestServer()
	cmd := buildRecoveredContractScalerCommand(t, s, map[string]interface{}{
		"container_id": "contract-scaler-1",
		"agent_symbol": "AGENT-1",
	})
	if cmd.Disabled {
		t.Fatal("absent config must leave the scaler LIVE (Disabled=false) on recovery")
	}
	if cmd.PlayerID != 7 || cmd.ContainerID != "contract-scaler-1" || cmd.AgentSymbol != "AGENT-1" {
		t.Fatalf("identity not preserved on recovery: %+v", cmd)
	}
}

// The escape-hatch launch keys reconstruct across a restart: a scaler launched (or tuned) disabled /
// dry-run / with a custom tick resumes in that mode after a daemon bounce.
func TestContractScaler_RecoveryBuild_ReconstructsEscapeHatchFlags(t *testing.T) {
	s := newContractScalerTestServer()
	cmd := buildRecoveredContractScalerCommand(t, s, map[string]interface{}{
		"container_id":              "contract-scaler-1",
		"contract_scaler_disabled":  true,
		"contract_scaler_dry_run":   true,
		"contract_scaler_tick_secs": 600,
	})
	if !cmd.Disabled || !cmd.DryRun || cmd.TickSeconds != 600 {
		t.Fatalf("escape-hatch flags not reconstructed on recovery: %+v", cmd)
	}
}
