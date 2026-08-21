package metrics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	bootstrapCmd "github.com/andrescamacho/spacetraders-go/internal/application/bootstrap/commands"
)

// derivablePhases reads the coordinator's Phase constants out of their own source, so this test never
// restates a phase name. Restating them is precisely the failure being guarded against: bootstrapKnownPhases
// is a hand-written copy of the Phase enum, and a copy the compiler does not check drifts silently.
// A test holding its own copy would drift in exactly the same way and keep passing.
func derivablePhases(t *testing.T) []string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed: cannot locate the Phase declarations")
	}
	typesFile := filepath.Join(filepath.Dir(thisFile),
		"..", "..", "application", "bootstrap", "commands", "bootstrap_types.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, typesFile, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", typesFile, err)
	}

	var phases []string
	for _, decl := range file.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			if typeName, ok := valueSpec.Type.(*ast.Ident); !ok || typeName.Name != "Phase" {
				continue
			}
			for _, value := range valueSpec.Values {
				literal, ok := value.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				name, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatalf("unquoting phase literal %s: %v", literal.Value, err)
				}
				phases = append(phases, name)
			}
		}
	}

	// The scan must be non-vacuous — an empty or partial result would quietly turn every assertion below
	// into a no-op. Naming the constants here is also what makes a renamed phase break this build, so the
	// rename cannot land without someone looking at the gauge.
	for _, want := range []bootstrapCmd.Phase{
		bootstrapCmd.PhaseColdStart,
		bootstrapCmd.PhaseGate,
		bootstrapCmd.PhaseExpansion,
	} {
		if !slices.Contains(phases, string(want)) {
			t.Fatalf("scanned phases %v do not include %q — the source scan is not seeing the Phase constants", phases, want)
		}
	}
	return phases
}

// testPlayerID is a fixed, arbitrary player_id used wherever a test only cares about the phase
// dimension — the player_id dimension itself is covered separately (bootstrap_metrics_player_id_test.go).
const testPlayerID = "42"

// The phase gauge must publish exactly the phases the coordinator can derive, the active one at 1 and every
// other at 0. Both drift directions are fleet-visible faults, so both are asserted:
//
//   - a derivable phase MISSING from bootstrapKnownPhases publishes no series at all, so the dashboard's
//     `bootstrap_phase == 1` matches nothing for that entire phase — indistinguishable from a dead coordinator;
//   - a name in bootstrapKnownPhases the coordinator can NEVER derive publishes a permanently-zero series,
//     which reads as a real phase that simply is not entered.
func TestRecordPhasePublishesExactlyTheDerivablePhases(t *testing.T) {
	phases := derivablePhases(t)

	for _, active := range phases {
		t.Run(active, func(t *testing.T) {
			InitRegistry()
			collector := NewBootstrapMetricsCollector()
			if err := collector.Register(); err != nil {
				t.Fatalf("Register failed: %v", err)
			}

			collector.RecordPhase(active, testPlayerID)

			for _, phase := range phases {
				want := 0.0
				if phase == active {
					want = 1.0
				}
				if got := testutil.ToFloat64(collector.phase.WithLabelValues(phase, testPlayerID)); got != want {
					t.Errorf("after RecordPhase(%q): bootstrap_phase{phase=%q} = %v, want %v", active, phase, got, want)
				}
			}

			// Whatever remains once every derivable phase is removed is a series the coordinator can never set.
			for _, phase := range phases {
				collector.phase.Delete(prometheus.Labels{"phase": phase, "player_id": testPlayerID})
			}
			if leftover := testutil.CollectAndCount(collector.phase); leftover != 0 {
				t.Errorf("after RecordPhase(%q): %d bootstrap_phase series published outside the derivable phases %v",
					active, leftover, phases)
			}
		})
	}
}
