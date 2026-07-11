// internal/adapters/grpc/container_runner_panic_test.go
package grpc

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	commonMediator "github.com/andrescamacho/spacetraders-go/internal/application/mediator"
	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/supervise"
)

// errMediator returns an error from Send without panicking. Embedding the
// interface keeps this compiling even if Mediator grows methods.
type errMediator struct {
	commonMediator.Mediator
	err error
}

func (m errMediator) Send(_ context.Context, _ commonMediator.Request) (commonMediator.Response, error) {
	return nil, m.err
}

// A panic inside an iteration (here: the nil-mediator deref that
// newCrashTestRunner sets up) must be converted into an error and returned —
// never propagated up the runner goroutine, where it would kill the entire
// daemon process (sp-i01z; before this barrier the ONLY recover() in
// production code was route execution).
func TestRunIterationProtected_ConvertsHandlerPanicToError(t *testing.T) {
	r := newCrashTestRunner(t, "contract-work-TORWIND-3-panic")

	var err error
	require.NotPanics(t, func() { err = r.runIterationProtected() })
	require.Error(t, err)

	var perr *supervise.PanicError
	require.ErrorAs(t, err, &perr, "panic must surface as supervise.PanicError so handleError logs it with the container id")
	require.Contains(t, perr.Component, "contract-work-TORWIND-3-panic")
}

// The barrier must not disturb the normal error path: an iteration that
// RETURNS an error still returns that error unchanged.
func TestRunIterationProtected_PassesThroughOrdinaryErrors(t *testing.T) {
	// mediator that returns an error without panicking
	r := newCrashTestRunner(t, "contract-work-TORWIND-3-err")
	r.mediator = errMediator{err: errors.New("API 4203 insufficient fuel")}

	err := r.runIterationProtected()
	require.Error(t, err)
	var perr *supervise.PanicError
	require.False(t, errors.As(err, &perr), "ordinary errors must not be wrapped as panics")
}
