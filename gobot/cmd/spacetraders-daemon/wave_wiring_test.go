package main

// THE WAVE'S SHARED WIRING: one predicate, two consumers, and the facts they judge reaching both by
// INSTANCE rather than by agreement.
//
// Nothing else can see this. Both consumers have their own passing unit tests against fakes, and
// those tests are green whether the composition root hands them one lane reader or two, and whether
// the drain's demonstrated-capacity slot holds the ledger's peak reader or a point read of the live
// balance. Each of those substitutions compiles, resolves a perfectly valid answer, and simply
// disagrees with the other consumer — which is the split-brain the design exists to prevent.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	parkedSensingAdapters "github.com/andrescamacho/spacetraders-go/internal/adapters/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/database"
)

const (
	unservedLaneConstructor = "NewUnservedLaneReader"
	// The two consumers, by constructor name (package alias stripped): the coordinator that SPENDS
	// on the capacity-short signal, and the drain's wave port that PAUSES on it.
	unservedLaneSpender    = "NewFleetGrowthCoordinatorHandler"
	unservedLaneWithholder = "NewWavePort"
)

// sharedInstanceWiring is what the composition root says about one shared collaborator. It is the
// same shape TestSharedHeavyTargetIsOneInstanceServingBothConsumers pins for the heavy target,
// generalised over the constructor and consumer names rather than copied: two near-identical
// analysers is how one of them quietly stops matching after a rename.
type sharedInstanceWiring struct {
	// constructions counts every call to the constructor. The invariant is 1.
	constructions int
	// instanceName is the identifier the sole construction is bound to ("" if it is not a simple
	// assignment, e.g. it was constructed inline inside a consumer's argument list).
	instanceName string
	// identArgsByConsumer maps each consumer constructor to the plain identifiers it was passed. A
	// consumer handed an inline construction contributes no identifier here, which is what makes
	// the second-instance edit visible.
	identArgsByConsumer map[string][]string
	// callsByConsumer counts each consumer's call sites: a SECOND consumer built with its own
	// instance is the same divergence wearing a different hat.
	callsByConsumer map[string]int
}

// analyseSharedInstanceWiring extracts the wiring facts for one shared collaborator from Go source.
func analyseSharedInstanceWiring(t *testing.T, filename string, src []byte, constructor string, consumers ...string) sharedInstanceWiring {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	require.NoError(t, err, "parse %s", filename)

	w := sharedInstanceWiring{
		identArgsByConsumer: map[string][]string{},
		callsByConsumer:     map[string]int{},
	}

	ast.Inspect(file, func(n ast.Node) bool {
		// The construction, and the name it is bound to.
		if assign, ok := n.(*ast.AssignStmt); ok {
			for i, rhs := range assign.Rhs {
				if calledFuncName(rhs) != constructor {
					continue
				}
				if i < len(assign.Lhs) {
					if ident, ok := assign.Lhs[i].(*ast.Ident); ok {
						w.instanceName = ident.Name
					}
				}
			}
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calledFuncName(call)
		if name == constructor {
			w.constructions++
		}
		if !slices.Contains(consumers, name) {
			return true
		}
		w.callsByConsumer[name]++
		w.identArgsByConsumer[name] = append(w.identArgsByConsumer[name], identArgNames(call.Args)...)
		return true
	})
	return w
}

// identArgNames lists the plain identifiers a call was passed, looking THROUGH a narrowing wrapper
// such as sensingLanePort(x).
//
// The wrapper has to be followed rather than banned: dropping a possibly-nil concrete pointer to a
// genuinely nil interface can only be done by a function, and refusing that shape would push the
// wiring back into the typed-nil hazard it exists to remove. It stays strict where it matters — a
// consumer handed an INLINE construction contributes no shared identifier, so the second-instance
// edit is still visible as a missing name as well as a raised count.
func identArgNames(args []ast.Expr) []string {
	var names []string
	for _, arg := range args {
		switch a := arg.(type) {
		case *ast.Ident:
			names = append(names, a.Name)
		case *ast.CallExpr:
			names = append(names, identArgNames(a.Args)...)
		}
	}
	return names
}

// THE INVARIANT. Exactly one UnservedLaneReader is constructed in the composition root, and that
// same instance reaches both consumers. Two instances would not fail loudly — each counts
// profitable lanes off the same market cache, and they would simply disagree about whether the
// fleet is capacity-short, so one consumer would judge HEAVY while the other judged PROBE with
// nothing in either gauge to say why.
func TestSharedUnservedLaneReaderIsOneInstanceServingBothConsumers(t *testing.T) {
	mainGoPath, _ := gatePaths(t)
	src, err := os.ReadFile(mainGoPath)
	require.NoError(t, err)

	w := analyseSharedInstanceWiring(t, mainGoPath, src,
		unservedLaneConstructor, unservedLaneSpender, unservedLaneWithholder)

	require.Equal(t, 1, w.constructions,
		"the composition root must construct EXACTLY ONE %s: a second one resolves a valid lane count from the same cache and simply disagrees with the first",
		unservedLaneConstructor)
	require.NotEmpty(t, w.instanceName,
		"the sole %s must be bound to a name and shared; constructing it inline inside a consumer's argument list is how the second instance gets added without anyone noticing",
		unservedLaneConstructor)

	require.Equal(t, 1, w.callsByConsumer[unservedLaneSpender],
		"exactly one %s: a second heavy buyer would need its own signal and reintroduce the divergence", unservedLaneSpender)
	require.Equal(t, 1, w.callsByConsumer[unservedLaneWithholder],
		"exactly one %s: a second wave port would need its own signal and reintroduce the divergence", unservedLaneWithholder)

	require.True(t, slices.Contains(w.identArgsByConsumer[unservedLaneSpender], w.instanceName),
		"the SPENDER (%s) must be handed the shared %q, or it buys against a capacity signal the drain does not know about",
		unservedLaneSpender, w.instanceName)
	require.True(t, slices.Contains(w.identArgsByConsumer[unservedLaneWithholder], w.instanceName),
		"the WITHHOLDER (%s) must be handed the shared %q, or the drain pauses on a capacity signal the buyer does not know about",
		unservedLaneWithholder, w.instanceName)
}

// PROOF THE CHECK HAS TEETH — a green primary test must mean "one instance", not "the parser
// matched nothing". A second reader built for one consumer is caught twice over: the construction
// count rises AND that consumer no longer receives the shared name.
func TestSharedUnservedLaneReaderDetectionCatchesASecondInstance(t *testing.T) {
	diverged := []byte(`package main

func wire() {
	unservedLaneReader := tradingQueries.NewUnservedLaneReader(shipRepo, lanes)
	_ = grpc.NewFleetGrowthCoordinatorHandler(srv, api, ledger, ships, med, wps, evts, yards, heavy, unservedLaneReader, txns)
	_ = parkedSensingAdapters.NewWavePort(caps, reserve, sensingLanePort(tradingQueries.NewUnservedLaneReader(shipRepo, lanes)), txns, nil)
}
`)

	w := analyseSharedInstanceWiring(t, "diverged.go", diverged,
		unservedLaneConstructor, unservedLaneSpender, unservedLaneWithholder)

	require.Equal(t, 2, w.constructions, "a second construction must be counted")
	require.Equal(t, "unservedLaneReader", w.instanceName)
	require.True(t, slices.Contains(w.identArgsByConsumer[unservedLaneSpender], w.instanceName),
		"the spender still holds the shared instance in this scenario")
	require.False(t, slices.Contains(w.identArgsByConsumer[unservedLaneWithholder], w.instanceName),
		"the withholder was handed its OWN reader — the divergence must be visible as a missing shared name, not just as a count")
}

// The same check must also catch the quieter shape: one reader, but a consumer that was never
// handed it (a refactor that dropped the argument). The count stays at 1, so only the
// consumer-side assertion can see it.
func TestSharedUnservedLaneReaderDetectionCatchesAConsumerLeftUnwired(t *testing.T) {
	dropped := []byte(`package main

func wire() {
	unservedLaneReader := tradingQueries.NewUnservedLaneReader(shipRepo, lanes)
	_ = grpc.NewFleetGrowthCoordinatorHandler(srv, api, ledger, ships, med, wps, evts, yards, heavy, unservedLaneReader, txns)
	_ = parkedSensingAdapters.NewWavePort(caps, reserve, sensingLanePort(nil), txns, nil)
}
`)

	w := analyseSharedInstanceWiring(t, "dropped.go", dropped,
		unservedLaneConstructor, unservedLaneSpender, unservedLaneWithholder)

	require.Equal(t, 1, w.constructions, "the count alone cannot see this shape")
	require.False(t, slices.Contains(w.identArgsByConsumer[unservedLaneWithholder], w.instanceName),
		"a consumer left unwired must still be caught")
}

// THE DRAIN'S DEMONSTRATED-CAPACITY SLOT HOLDS THE LEDGER'S PEAK READER, asserted on the port the
// composition root actually builds.
//
// A live-balance reader wired here compiles and passes every test of the predicate — the property
// is carried by nothing but WHICH port lands in this field, which makes it a WIRING defect no
// behavioural test that supplies its own fake can catch. The peak-over-window contract itself is
// pinned where it is implemented.
func TestSensingWavePortReadsTheLedgersPeakNotAPointBalance(t *testing.T) {
	db, err := database.NewTestConnection()
	require.NoError(t, err)
	repo := persistence.NewGormTransactionRepository(db)

	// The REAL composition, with only the ledger varied: nothing here performs I/O.
	ports := sensingWiring{db: db, transactionRepo: repo}.enginePorts(nil, nil, 1)

	wave, ok := ports.Wave.(*parkedSensingAdapters.WavePort)
	require.True(t, ok, "the drain's wave port must be the sensing adapter's, not a substitute")
	require.Equal(t, reflect.ValueOf(repo).Pointer(), wavePortField(t, wave, "highWater").Elem().Pointer(),
		"the drain must read the ledger's own peak port, not an adapter over a point read")

	// And a ledger the composition never received must leave the field EMPTY rather than holding
	// something that silently reports a zero peak — proof this probe can tell the two apart.
	unwired := sensingWiring{db: db}.enginePorts(nil, nil, 1).Wave.(*parkedSensingAdapters.WavePort)
	require.True(t, wavePortField(t, unwired, "highWater").IsNil(),
		"no ledger means no port at all: a phantom zero peak would read as a fleet that has never held any money, which is a genuine unreachable")
}

// wavePortField reads a named port off the CONSTRUCTED wave port. The coupling to the field name is
// deliberate: the wiring outcome IS the behaviour under test. A rename must update this, never
// delete it.
func wavePortField(t *testing.T, port *parkedSensingAdapters.WavePort, field string) reflect.Value {
	t.Helper()
	v := reflect.ValueOf(port).Elem().FieldByName(field)
	require.True(t, v.IsValid(),
		"the wave port no longer has a %q field; this test pins what is WIRED into it, so a rename must update it rather than remove it", field)
	require.Equal(t, reflect.Interface, v.Kind(), "%q is expected to be a port interface", field)
	return v
}
