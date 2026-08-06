package grpc

// THE API-UTIL READER IS ARMED, AND IT RESOLVES RATHER THAN CAPTURES — pinned structurally,
// because both failures are silent (sp-a75fz, following sp-ps2oc's spend_ledger_wiring_test.go).
//
// GuardAPIUtil's reader fails CLOSED when it cannot read, which is correct (RULINGS #4) and is
// exactly what makes both defects below invisible: the fleet simply stops growing, everywhere,
// permanently, with no error and no metric. It reads as "the autosizer decided not to grow".
//
// TWO DISTINCT WAYS TO REACH THAT STATE, and the resolver fix only closes one of them:
//
//  1. THE READER IS NEVER WIRED. Delete or nil out the SetAPIUtilizationReader call — as
//     sp-ps2oc's SetSpendLedger was deleted by an unrelated refactor — and h.apiUtil stays nil.
//     No resolver design prevents this; only a check at the composition point can see it.
//  2. THE TRACKER IS CAPTURED AT WIRING TIME. The original defect: the pointer was read during
//     wiring, so the reader was correct only because main.go constructs the tracker before it
//     wires the autosizer. Nothing enforced that order.
//
// The reader's behavioural tests cannot see either one. They construct their own reader from a
// fixture and stay green whatever the composition root does — the same blind spot that let
// sp-ps2oc's money cap fail open on every gate buy ever made.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// portsFile is the composition point for the autosizer's adapters — where the reader is built and
// handed to the handler.
const portsFile = "fleet_autosizer_ports.go"

const (
	apiUtilSetter     = "SetAPIUtilizationReader"
	apiUtilReaderType = "autosizerAPIUtilReader"
	// resolveField is the field that makes the wiring order irrelevant. A literal setting anything
	// else is holding a tracker, which is the captured-pointer defect returning.
	resolveField = "resolve"
)

// apiUtilWiring is what the composition point says about the api_util reader.
type apiUtilWiring struct {
	// setterCalls counts SetAPIUtilizationReader call sites. The invariant is 1.
	setterCalls int
	// setterArgNil records a syntactically nil reader — satisfies the interface, compiles, ships,
	// and leaves the guard permanently unreadable.
	setterArgNil bool
	// readerLiterals counts autosizerAPIUtilReader composite literals.
	readerLiterals int
	// literalFields is every field name set on those literals. It must be exactly {resolve}: any
	// other field means the reader is holding a tracker resolved at wiring time.
	literalFields []string
	// resolveArg is the identifier assigned to `resolve`, or "" when it is anything else — a
	// closure, a call, a selector.
	//
	// A FIELD-NAME CHECK ALONE IS NOT ENOUGH, and this exists because probing found the gap: a
	// resolver that CLOSES OVER a tracker read at wiring time
	// (captured := ...; resolve: func() apiBudgetReporter { return captured })
	// sets only `resolve`, compiles, and is the original defect wearing the fix's clothes. Only a
	// plain named function can be shown to resolve at call time.
	resolveArg string
}

func analyseAPIUtilWiring(t *testing.T, filename string, src []byte) apiUtilWiring {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	require.NoError(t, err, "parse %s", filename)

	var w apiUtilWiring
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if calledName(node) == apiUtilSetter {
				w.setterCalls++
				if len(node.Args) == 1 && isNilExpr(node.Args[0]) {
					w.setterArgNil = true
				}
			}
		case *ast.CompositeLit:
			if literalTypeName(node) != apiUtilReaderType {
				return true
			}
			w.readerLiterals++
			for _, elt := range node.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				w.literalFields = append(w.literalFields, key.Name)
				if key.Name == resolveField {
					if ident, ok := kv.Value.(*ast.Ident); ok {
						w.resolveArg = ident.Name
					}
				}
			}
		}
		return true
	})
	return w
}

// calledName reduces a call to the bare function or method name being invoked.
func calledName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// literalTypeName is the type name of a composite literal, seeing through the & of &T{...}.
func literalTypeName(lit *ast.CompositeLit) string {
	switch t := lit.Type.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

// isNilExpr reports a bare nil or a typed-nil conversion like (*T)(nil) — both satisfy the
// interface and ship dark.
func isNilExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name == "nil"
	case *ast.ParenExpr:
		return isNilExpr(e.X)
	case *ast.CallExpr:
		return len(e.Args) == 1 && isNilExpr(e.Args[0])
	}
	return false
}

// THE INVARIANT, against the real source.
func TestAPIUtilReaderIsWiredAndResolvesRatherThanCaptures(t *testing.T) {
	src, err := os.ReadFile(portsFile)
	require.NoError(t, err)

	w := analyseAPIUtilWiring(t, portsFile, src)

	// CLAIM 1 — the sp-ps2oc shape. Without this call h.apiUtil is nil, readTickInputs leaves
	// apiOK false, and guardAPIUtil refuses every sizing decision forever.
	require.Equal(t, 1, w.setterCalls,
		"%s must be called exactly once at the autosizer's composition point. Unwired, the api_util guard reads UNREADABLE and fails closed on every tick — the fleet stops growing everywhere, permanently, with no error. The reader's own tests build their own fixture and stay green regardless, so only this check can see it",
		apiUtilSetter)

	require.False(t, w.setterArgNil,
		"%s was passed a syntactically nil reader — it satisfies the interface, compiles, ships, and wedges growth exactly as if the call were absent",
		apiUtilSetter)

	// CLAIM 2 — this bead. The reader must resolve the tracker per read, never hold one obtained
	// during wiring.
	require.Equal(t, 1, w.readerLiterals, "expected exactly one %s literal at the composition point", apiUtilReaderType)
	require.Equal(t, []string{resolveField}, w.literalFields,
		"the %s literal sets %v; it must set ONLY %q. Any other field holds a tracker read at WIRING time, which is correct only while main.go happens to construct the tracker before wiring the autosizer — reverse those two lines and the captured pointer is nil forever, the guard fails closed correctly, and nothing anywhere reports it",
		apiUtilReaderType, w.literalFields, resolveField)

	// CLAIM 3 — the resolver must be a NAMED FUNCTION, not a closure. A closure over a tracker read
	// during wiring satisfies every check above while reintroducing the exact defect.
	require.Equal(t, "globalAPIBudgetReporter", w.resolveArg,
		"the %s field must be the named function %q, passed uncalled. A closure or a call expression here can capture a tracker at WIRING time — the original defect with a resolver wrapped around it, invisible to a field-name check",
		resolveField, "globalAPIBudgetReporter")
}

// TEETH. The analyser must genuinely reject each shape the claims are about; a check that cannot
// fail is the failure mode this file exists to answer.
func TestAPIUtilWiringAnalyserRejectsEachDefect(t *testing.T) {
	const wired = `package grpc
func f(h *H) {
	h.SetAPIUtilizationReader(&autosizerAPIUtilReader{resolve: globalAPIBudgetReporter})
}`
	// Non-vacuity: the correct shape must READ as correct, or every rejection below is empty.
	good := analyseAPIUtilWiring(t, "wired.go", []byte(wired))
	require.Equal(t, 1, good.setterCalls)
	require.False(t, good.setterArgNil)
	require.Equal(t, []string{"resolve"}, good.literalFields)

	t.Run("claim 1: setter deleted by an unrelated refactor", func(t *testing.T) {
		const src = `package grpc
func f(h *H) {
	h.SetTreasuryReader(&autosizerTreasuryReader{})
}`
		require.Zero(t, analyseAPIUtilWiring(t, "x.go", []byte(src)).setterCalls,
			"a composition point holding only the sibling setters must read as UNWIRED — this is the sp-ps2oc production state")
	})

	t.Run("claim 1: nil reader", func(t *testing.T) {
		const src = `package grpc
func f(h *H) { h.SetAPIUtilizationReader(nil) }`
		require.True(t, analyseAPIUtilWiring(t, "x.go", []byte(src)).setterArgNil)
	})

	t.Run("claim 1: typed-nil reader", func(t *testing.T) {
		const src = `package grpc
func f(h *H) { h.SetAPIUtilizationReader((*autosizerAPIUtilReader)(nil)) }`
		require.True(t, analyseAPIUtilWiring(t, "x.go", []byte(src)).setterArgNil,
			"a typed nil satisfies the interface and ships dark")
	})

	t.Run("claim 2: the original captured-pointer defect", func(t *testing.T) {
		const src = `package grpc
func f(h *H) {
	h.SetAPIUtilizationReader(&autosizerAPIUtilReader{reporter: metrics.GetGlobalAPIBudgetTracker()})
}`
		w := analyseAPIUtilWiring(t, "x.go", []byte(src))
		require.Equal(t, 1, w.setterCalls, "the setter is present...")
		require.NotEqual(t, []string{"resolve"}, w.literalFields,
			"...but the literal captures a tracker at wiring time, which must NOT read as correct. This is the exact line sp-a75fz was filed against")
	})

	t.Run("claim 3: a resolver that CLOSES OVER a wiring-time capture", func(t *testing.T) {
		const src = `package grpc
func f(h *H) {
	captured := globalAPIBudgetReporter()
	h.SetAPIUtilizationReader(&autosizerAPIUtilReader{resolve: func() apiBudgetReporter { return captured }})
}`
		w := analyseAPIUtilWiring(t, "x.go", []byte(src))
		require.Equal(t, []string{"resolve"}, w.literalFields,
			"the field-name check PASSES this — which is why it is not sufficient on its own")
		require.Empty(t, w.resolveArg,
			"...but the resolver is a closure, not a named function, so it must be rejected. It reads the tracker once at wiring time and hands back the same answer forever — the original defect exactly")
	})

	t.Run("claim 3: a resolver built by calling a factory", func(t *testing.T) {
		const src = `package grpc
func f(h *H) {
	h.SetAPIUtilizationReader(&autosizerAPIUtilReader{resolve: staticReporter(metrics.GetGlobalAPIBudgetTracker())})
}`
		require.Empty(t, analyseAPIUtilWiring(t, "x.go", []byte(src)).resolveArg,
			"a call expression evaluates its argument at WIRING time; only a named function passed uncalled resolves per read")
	})

	t.Run("claim 2: a resolver plus a cached tracker alongside it", func(t *testing.T) {
		const src = `package grpc
func f(h *H) {
	h.SetAPIUtilizationReader(&autosizerAPIUtilReader{resolve: globalAPIBudgetReporter, cached: tracker})
}`
		require.NotEqual(t, []string{"resolve"}, analyseAPIUtilWiring(t, "x.go", []byte(src)).literalFields,
			"a resolver is not enough if a captured tracker rides alongside it — the memoised form is the same defect one layer down")
	})
}
