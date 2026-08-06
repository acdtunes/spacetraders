package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// serverContractReaderArgIndex is the position of the ServerContractReader in
// NewRunWorkflowHandler(mediator, shipRepo, contractRepo, serverContracts, clock, opts...).
// It is positional rather than a functional option precisely so the compiler forces every
// caller to answer for it — but the compiler is satisfied by `nil`, and a nil reader makes
// ReconcileWithServer return the contract untouched. That is the whole gap this test closes.
const serverContractReaderArgIndex = 3

// THE INVARIANT (sp-20eyn). The contract workflow must reconcile against the game server at the
// composition root, ARMED.
//
// On 2026-08-05 the TORWIND row read 0/47 delivered while the server read 94/47 with
// `fulfilled: false`. Every worker resumed that contract, walked into the delivery leg, tried to
// reload a hull the API could not return, died, and respawned — 34,279 containers, ~24h of zero
// income, with nothing left to deliver the whole time. Nothing was "down", so no recovery path
// fired. The reconcile is what turns that state into a fulfil.
//
// A nil reader here compiles, ships, and silently restores the pre-incident behaviour: the
// workflow plans every delivery against local counts that can only be BEHIND, which is the
// direction that spends. The reconcile's own tests pass a stub and stay green regardless, so
// only this check can see an un-armed composition root ("closed is not armed").
func TestServerContractReaderIsWiredArmedAtTheCompositionRoot(t *testing.T) {
	mainGoPath, _ := gatePaths(t)
	src, err := os.ReadFile(mainGoPath)
	require.NoError(t, err)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, mainGoPath, src, 0)
	require.NoError(t, err, "parse %s", mainGoPath)

	var ctorCalls int
	var readerArg string
	var readerArgNil bool
	var argCount int

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || calledFuncName(call) != contractHandlerCtor {
			return true
		}
		ctorCalls++
		argCount = len(call.Args)
		if len(call.Args) > serverContractReaderArgIndex {
			arg := call.Args[serverContractReaderArgIndex]
			readerArg = argIdentName(arg)
			readerArgNil = isNilArg(arg)
		}
		return true
	})

	require.Equal(t, 1, ctorCalls,
		"the composition root must call %s exactly once; found %d", contractHandlerCtor, ctorCalls)
	require.Greater(t, argCount, serverContractReaderArgIndex,
		"%s was called with %d arguments — too few to carry a server contract reader at index %d. If the signature moved, this test must move with it",
		contractHandlerCtor, argCount, serverContractReaderArgIndex)
	require.False(t, readerArgNil,
		"%s was passed a syntactically nil server contract reader. That compiles and ships, and ReconcileWithServer then returns every contract untouched — the workflow plans deliveries against local counts that can only be behind, which is exactly the state that double-delivered 47 ALUMINUM and then wedged TORWIND for 24h",
		contractHandlerCtor)
	require.NotEmpty(t, readerArg,
		"the server contract reader passed to %s must be a plain identifier so it can be shown to be the real API client, not a literal or an inline construction",
		contractHandlerCtor)
}
