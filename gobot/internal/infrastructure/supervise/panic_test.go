// internal/infrastructure/supervise/panic_test.go
package supervise

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// CapturePanic converts an in-flight panic into an error carrying the
// component name, the panic value, and the stack — so the existing
// error-path restart machinery (ContainerRunner.handleError, Supervisor)
// can treat a panic like any other failure instead of the process dying.
func TestCapturePanic_ConvertsPanicToError(t *testing.T) {
	run := func() (err error) {
		defer CapturePanic(&err, "test-component")
		panic("boom")
	}
	err := run()
	require.Error(t, err)
	var perr *PanicError
	require.ErrorAs(t, err, &perr)
	require.Equal(t, "test-component", perr.Component)
	require.Equal(t, "boom", fmt.Sprintf("%v", perr.Value))
	require.NotEmpty(t, perr.Stack, "stack must be captured for diagnosis")
	require.True(t, strings.Contains(err.Error(), "test-component"))
	require.True(t, strings.Contains(err.Error(), "boom"))
}

// A normal return (nil or a real error) must pass through untouched:
// CapturePanic only acts when recover() is non-nil.
func TestCapturePanic_NoPanicLeavesErrorUntouched(t *testing.T) {
	sentinel := errors.New("real failure")
	run := func() (err error) {
		defer CapturePanic(&err, "test-component")
		return sentinel
	}
	require.ErrorIs(t, run(), sentinel)

	runNil := func() (err error) {
		defer CapturePanic(&err, "test-component")
		return nil
	}
	require.NoError(t, runNil())
}

// Guard is for fire-and-forget callbacks (time.AfterFunc, boot goroutines)
// where there is no error channel: the panic is logged and suppressed, and
// the process survives.
func TestGuard_SuppressesPanic(t *testing.T) {
	require.NotPanics(t, func() {
		Guard("test-callback", func() { panic("boom") })
	})
}

func TestGuard_RunsFnNormally(t *testing.T) {
	ran := false
	Guard("test-callback", func() { ran = true })
	require.True(t, ran)
}
