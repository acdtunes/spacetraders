package main

// THE GATE FACTORY FLEET IS ARMED — pinned at the composition root, not merely intended.
//
// Every part of the two-fleet operation ships inert until main.go calls SetGateFactory. The
// handler's own unit tests are green either way: they call SetGateFactory themselves, from
// fixtures. Nothing below the composition root can see that production never does — which is
// precisely how this feature was built complete, tested, merged, and shipped dark.
//
// The blast radius of the missing call is wider than the feeding leg alone:
//
//   - gateLegRole gates the FACTORY role on h.factory.enabled(), so a factory-tagged hull falls
//     through to the shared fabricate path and the feeding leg never runs;
//   - reallocateGateRoles returns immediately unless BOTH h.gate.enabled() and h.factory.enabled(),
//     so the entire pause-driven role reallocation never runs on a live tick either.
//
// This check therefore pins FOUR things, because three of them fail silently:
//
//  1. SetGateFactory is called at all — the shipped-dark state this commit ends;
//  2. it is passed THREE non-nil collaborators — the setter's own nil-guard returns early on a nil
//     in ANY argument, so SetGateFactory(topo, exec, nil) compiles, ships, and does nothing;
//  3. it is called on the SAME handler as SetGateDelivery — split across two handlers, each leg
//     would report itself wired while reallocateGateRoles, which demands both on one handler,
//     never fires;
//  4. exactly one of each, so a second handler cannot quietly become the registered one.
//
// It parses the composition root because nothing else can see any of this, following the
// shared-heavy-target check in this same package.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	gateFactorySetter  = "SetGateFactory"
	gateDeliverySetter = "SetGateDelivery"
	// gateHandlerCommand is the command type whose registration must land on the SAME handler the
	// setters configured. Claim 4 is about THIS handler, so the guard has to pick this
	// registration out of main.go's ~50 rather than counting registrations in general.
	gateHandlerCommand = "RunConstructionCoordinatorCommand"
)

// gateFleetWiring is what the composition root says about the two gate fleets.
type gateFleetWiring struct {
	// factoryCalls / deliveryCalls count the setter call sites. The invariant is 1 each.
	factoryCalls  int
	deliveryCalls int
	// receivers are the identifiers the setters were called ON ("" when the call is not a plain
	// x.Setter(...) — e.g. chained off a constructor, which would also defeat the shared-handler
	// invariant).
	factoryReceiver  string
	deliveryReceiver string
	// registrations / registeredReceiver describe the mediator registration of the construction
	// coordinator: how many there are, and the identifier passed as the handler.
	//
	// WITHOUT THESE, CLAIM 4 WAS NOT IMPLEMENTED. The counts above are SETTER call sites; neither
	// they nor main_test.go's registration gate (which checks the registered TYPE, never the
	// instance) tie either receiver to the object the mediator actually dispatches to. Both setters
	// on h1 while h2 is registered satisfied every assertion in this file: two fully-wired legs on
	// a handler no command ever reaches.
	registrations      int
	registeredReceiver string
	// factoryArgs is the argument count at the SetGateFactory call site. The setter takes three.
	factoryArgs int
	// factoryNilArgs holds the zero-based positions of any argument that is syntactically nil at
	// the call site. A nil in ANY position makes the setter a no-op, so this is the shape that
	// ships green and does nothing. See isNilArg for exactly which shapes are visible here.
	factoryNilArgs []int
}

// analyseGateFleetWiring extracts the two setters' wiring facts from Go source.
func analyseGateFleetWiring(t *testing.T, filename string, src []byte) gateFleetWiring {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	require.NoError(t, err, "parse %s", filename)

	var w gateFleetWiring
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if typeName, handler, isRegistration := registerHandlerCall(call); isRegistration && typeName == gateHandlerCommand {
			w.registrations++
			w.registeredReceiver = handler
			return true
		}
		switch calledFuncName(call) {
		case gateFactorySetter:
			w.factoryCalls++
			w.factoryReceiver = receiverName(call)
			w.factoryArgs = len(call.Args)
			for i, arg := range call.Args {
				if isNilArg(arg) {
					w.factoryNilArgs = append(w.factoryNilArgs, i)
				}
			}
		case gateDeliverySetter:
			w.deliveryCalls++
			w.deliveryReceiver = receiverName(call)
		}
		return true
	})
	return w
}

// registerHandlerCall reduces mediator.RegisterHandler[T](med, handler) to T's short name and the
// identifier passed as the handler. It reuses main_test.go's typeArgName and mirrors its IndexExpr
// shape (RegisterHandler has exactly one type parameter, so every real call site is an IndexExpr,
// never an IndexListExpr).
//
// It returns ok=false for a handler that is not a plain identifier — a literal, a constructor call,
// a field selector. That is the right answer rather than a gap: such a value cannot be shown to be
// the same object either setter received, which is precisely what the invariant needs.
func registerHandlerCall(call *ast.CallExpr) (typeName, handler string, ok bool) {
	index, isIndex := call.Fun.(*ast.IndexExpr)
	if !isIndex {
		return "", "", false
	}
	sel, isSelector := index.X.(*ast.SelectorExpr)
	if !isSelector || sel.Sel.Name != "RegisterHandler" {
		return "", "", false
	}
	if pkg, isIdent := sel.X.(*ast.Ident); !isIdent || pkg.Name != "mediator" {
		return "", "", false
	}
	if len(call.Args) < 2 {
		return "", "", false
	}
	ident, isIdent := call.Args[1].(*ast.Ident)
	if !isIdent {
		return "", "", false
	}
	return typeArgName(index.Index), ident.Name, true
}

// isNilArg reports whether a call argument is nil AS WRITTEN.
//
// It covers the bare identifier, any parenthesisation of it, and a one-argument conversion of nil —
// `(*services.GateTopology)(nil)`, `GateFeeder(nil)` — which is the shape that satisfies the
// interface statically, trips SetGateFactory's run-time nil-guard, and leaves the leg dark. Without
// it the guard saw only `nil` itself.
//
// A ONE-ARGUMENT CALL WHOSE ARGUMENT IS nil IS REPORTED WHETHER IT IS A CONVERSION OR A FUNCTION
// CALL, deliberately. The AST cannot tell `T(nil)` from `f(nil)` without go/types, and this guard
// fails LOUD in the safe direction: a false positive is a one-line diagnosis at the call site, a
// false negative is a fleet that ships dark.
//
// WHAT REMAINS INVISIBLE, exactly: a nil-VALUED variable (`var feeder GateFeeder; …
// SetGateFactory(topo, exec, feeder)`) and a helper that RETURNS nil. Both reach the setter's
// nil-guard and go dark silently, and neither is decidable from syntax — the AST records an
// identifier and nothing about the value behind it. Closing them needs go/types to load the package
// and constant-fold the value, which this check deliberately does not do (it must run against
// hand-written source fragments in the teeth tests below, which do not type-check). Anyone adding a
// collaborator through a local variable is outside this guard's reach and inside the compile-time
// conformance assertions' instead.
func isNilArg(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.ParenExpr:
		return isNilArg(e.X)
	case *ast.Ident:
		return e.Name == "nil"
	case *ast.CallExpr:
		return len(e.Args) == 1 && isNilArg(e.Args[0])
	}
	return false
}

// receiverName reduces `x.Method(...)` to "x". It returns "" for anything else, which is itself a
// failure of the shared-handler invariant: a setter called on a temporary cannot be the same
// object the other setter received.
func receiverName(call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name
}

// THE INVARIANT. The factory fleet is wired, fully, on the same handler as the delivery fleet.
func TestGateFactoryFleetIsWiredArmedAtTheCompositionRoot(t *testing.T) {
	mainGoPath, _ := gatePaths(t)
	src, err := os.ReadFile(mainGoPath)
	require.NoError(t, err)

	w := analyseGateFleetWiring(t, mainGoPath, src)

	require.Equal(t, 1, w.factoryCalls,
		"the composition root must call %s exactly once. Without it the FACTORY role never runs: gateLegRole sends factory-tagged hulls down the shared fabricate path, and reallocateGateRoles — which requires BOTH legs wired — returns before doing anything. The handler's own tests wire it from fixtures and stay green regardless, so only this check can see it",
		gateFactorySetter)
	require.Equal(t, 1, w.deliveryCalls,
		"the composition root must call %s exactly once; the factory fleet's reallocation is gated on the delivery leg being wired too", gateDeliverySetter)

	require.Equal(t, 3, w.factoryArgs,
		"%s takes THREE collaborators (topology, buyer, feeder)", gateFactorySetter)
	require.Empty(t, w.factoryNilArgs,
		"%s was passed a bare nil at argument position(s) %v. Its nil-guard returns early on a nil in ANY argument, so this call compiles, ships, and leaves the feeding leg dark — the exact shape this check exists to catch",
		gateFactorySetter, w.factoryNilArgs)

	require.NotEmpty(t, w.factoryReceiver,
		"%s must be called on a named handler", gateFactorySetter)
	require.Equal(t, w.deliveryReceiver, w.factoryReceiver,
		"both setters must be called on the SAME handler (%s got %q, %s got %q). reallocateGateRoles requires h.gate.enabled() AND h.factory.enabled() on ONE handler; split across two, each leg reports itself wired and the reallocation never runs",
		gateDeliverySetter, w.deliveryReceiver, gateFactorySetter, w.factoryReceiver)

	// CLAIM 4, now actually implemented. Everything above is about SETTER call sites; this is the
	// only assertion that reaches the object the mediator dispatches to. Wire both legs onto h1 and
	// register h2 and every check above passes — including main_test.go's registration gate, which
	// matches the registered TYPE and never the instance — while the configured handler is the one
	// no command ever arrives at.
	require.Equal(t, 1, w.registrations,
		"the composition root must register %s exactly once; %d registration(s) of it means either none (the mediator dispatch fails outright) or a second one silently shadowing the first",
		gateHandlerCommand, w.registrations)
	require.Equal(t, w.factoryReceiver, w.registeredReceiver,
		"the handler the setters configured (%q) is not the handler registered for %s (%q). Both legs would report themselves wired on an object the mediator never dispatches to, and every count and nil-scan in this file would stay green",
		w.factoryReceiver, gateHandlerCommand, w.registeredReceiver)
}

// PROOF THE CHECK HAS TEETH, shape 1: the shipped-dark composition root. This is not a
// hypothetical — it is exactly what main.go looked like before this commit, with every collaborator
// already constructed and the delivery fleet already wired.
func TestGateFactoryWiringDetectionCatchesTheUnwiredComposition(t *testing.T) {
	dark := []byte(`package main

func wire() {
	constructionCoordinatorHandler.SetGateDelivery(
		goodsServices.NewGateTopology(goodsMarketLocator, goods.ExportToImportMap),
		constructionExecutor,
	)
	_ = mediator.RegisterHandler[*goodsCmd.RunConstructionCoordinatorCommand](med, constructionCoordinatorHandler)
}
`)

	w := analyseGateFleetWiring(t, "dark.go", dark)

	require.Equal(t, 1, w.deliveryCalls, "the delivery fleet was wired all along — which is why nothing looked wrong")
	require.Equal(t, 0, w.factoryCalls, "the missing factory wiring must be caught")
}

// Shape 2, the quieter one: the call IS present, so a check that only counted call sites would be
// green — but a bare nil in any argument trips the setter's own guard and the leg stays dark.
func TestGateFactoryWiringDetectionCatchesANilCollaborator(t *testing.T) {
	nilFeeder := []byte(`package main

func wire() {
	constructionCoordinatorHandler.SetGateDelivery(topology, constructionExecutor)
	constructionCoordinatorHandler.SetGateFactory(topology, constructionExecutor, nil)
}
`)

	w := analyseGateFleetWiring(t, "nilfeeder.go", nilFeeder)

	require.Equal(t, 1, w.factoryCalls, "the call count alone cannot see this shape")
	require.Equal(t, 3, w.factoryArgs, "nor can the argument count")
	require.Equal(t, []int{2}, w.factoryNilArgs, "the nil feeder must be caught, by position")
}

// Shape 3: both setters called, both fully wired, but on DIFFERENT handlers. Every count is 1 and
// no argument is nil, so only the shared-receiver assertion can see it — and reallocateGateRoles,
// which needs both flags on one handler, would never run.
func TestGateFactoryWiringDetectionCatchesASplitHandler(t *testing.T) {
	split := []byte(`package main

func wire() {
	constructionCoordinatorHandler.SetGateDelivery(topology, constructionExecutor)
	otherHandler.SetGateFactory(topology, constructionExecutor, constructionExecutor)
}
`)

	w := analyseGateFleetWiring(t, "split.go", split)

	require.Equal(t, 1, w.factoryCalls)
	require.Empty(t, w.factoryNilArgs, "neither the count nor a nil scan can see this shape")
	require.NotEqual(t, w.deliveryReceiver, w.factoryReceiver,
		"the split handler must be visible as a receiver mismatch")
}

// Shape 4, the one claim 4 named and did not implement: both setters on ONE handler, fully wired,
// no nil anywhere, receivers matching — and the mediator registered on a DIFFERENT object. Every
// assertion that existed before this shape was added passes, and main_test.go's registration gate
// passes too, because it matches the registered TYPE and never the instance.
func TestGateFactoryWiringDetectionCatchesARegistrationOnAnotherHandler(t *testing.T) {
	shadowed := []byte(`package main

func wire() {
	constructionCoordinatorHandler.SetGateDelivery(topology, constructionExecutor)
	constructionCoordinatorHandler.SetGateFactory(topology, constructionExecutor, constructionExecutor)
	_ = mediator.RegisterHandler[*goodsCmd.RunConstructionCoordinatorCommand](med, otherHandler)
}
`)

	w := analyseGateFleetWiring(t, "shadowed.go", shadowed)

	require.Equal(t, 1, w.factoryCalls)
	require.Equal(t, w.deliveryReceiver, w.factoryReceiver,
		"the setters agree, so no assertion about them can see this shape")
	require.Empty(t, w.factoryNilArgs, "and nothing is nil")
	require.Equal(t, 1, w.registrations, "the registration is present — it is simply on the wrong object")
	require.NotEqual(t, w.factoryReceiver, w.registeredReceiver,
		"the shadowed handler must be visible as a configured-vs-registered mismatch")
}

// Shape 5: the TYPED nil. It satisfies the interface statically, so it compiles and ships, and then
// trips SetGateFactory's own nil-guard at run time exactly as a bare nil does. A scan that only
// matched the bare identifier reported this composition as fully wired.
func TestGateFactoryWiringDetectionCatchesATypedNilCollaborator(t *testing.T) {
	typedNil := []byte(`package main

func wire() {
	constructionCoordinatorHandler.SetGateDelivery(topology, constructionExecutor)
	constructionCoordinatorHandler.SetGateFactory(topology, constructionExecutor, (*mfgServices.ProductionExecutor)(nil))
}
`)

	w := analyseGateFleetWiring(t, "typednil.go", typedNil)

	require.Equal(t, 1, w.factoryCalls, "the call count cannot see this shape")
	require.Equal(t, 3, w.factoryArgs, "nor can the argument count")
	require.Equal(t, []int{2}, w.factoryNilArgs,
		"a conversion of nil to a concrete type is the same dark leg as a bare nil, and must be caught by position")
}

// The calibration for shape 5: a genuinely-wired composition must NOT be reported as nil. Without
// this, an isNilArg that answered true for everything would pass every teeth test in this file
// while making the primary check unfalsifiable.
func TestGateFactoryNilDetectionDoesNotFireOnRealCollaborators(t *testing.T) {
	live := []byte(`package main

func wire() {
	constructionCoordinatorHandler.SetGateDelivery(topology, constructionExecutor)
	constructionCoordinatorHandler.SetGateFactory(goodsServices.NewGateTopology(a, b), constructionExecutor, constructionExecutor)
}
`)

	w := analyseGateFleetWiring(t, "live.go", live)

	require.Equal(t, 3, w.factoryArgs)
	require.Empty(t, w.factoryNilArgs,
		"a constructor call, a plain identifier and a selector are all real collaborators; reporting them as nil would make the primary assertion fail for every correct composition")
}
