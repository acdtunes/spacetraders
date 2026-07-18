package grpc

import (
	"testing"

	contractCmd "github.com/andrescamacho/spacetraders-go/internal/application/contract/commands"
)

// sp-ehg9: recovery-safety for the continuous single-hull contract loop. A
// looping batch-contract container persists iterations=-1 in its launch config;
// recoverContainer reads that back for the container's maxIterations, and this
// builder must reconstruct the RunWorkflowCommand with Loop=true so a daemon
// restart RE-ADOPTS the frigate loop as a loop (not a one-shot). A single-shot
// worker (iterations 1 or absent — every coordinator-spawned worker) rebuilds
// with Loop=false, byte-identical to today.
func TestBuildContractWorkflowCommand_LoopFromIterations(t *testing.T) {
	tests := []struct {
		name     string
		config   map[string]interface{}
		wantLoop bool
	}{
		{
			name:     "iterations -1 rebuilds a loop (recovery-safe)",
			config:   map[string]interface{}{"ship_symbol": "TORWIND-1", "iterations": -1},
			wantLoop: true,
		},
		{
			name:     "iterations -1 as float64 (JSON recovery round-trip) rebuilds a loop",
			config:   map[string]interface{}{"ship_symbol": "TORWIND-1", "iterations": float64(-1)},
			wantLoop: true,
		},
		{
			name:     "iterations 1 rebuilds single-shot",
			config:   map[string]interface{}{"ship_symbol": "TORWIND-1", "iterations": 1},
			wantLoop: false,
		},
		{
			name:     "absent iterations rebuilds single-shot (byte-identical to coordinator workers)",
			config:   map[string]interface{}{"ship_symbol": "TORWIND-1"},
			wantLoop: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			built := buildContractWorkflowCommand(newConfigReader(tt.config), 1, "container-1")
			cmd, ok := built.(*contractCmd.RunWorkflowCommand)
			if !ok {
				t.Fatalf("expected *RunWorkflowCommand, got %T", built)
			}
			if cmd.Loop != tt.wantLoop {
				t.Fatalf("Loop = %v, want %v", cmd.Loop, tt.wantLoop)
			}
			if cmd.ShipSymbol != "TORWIND-1" {
				t.Fatalf("ShipSymbol = %q, want TORWIND-1", cmd.ShipSymbol)
			}
		})
	}
}
