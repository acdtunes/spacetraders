// internal/infrastructure/supervise/panic.go
// Package supervise provides panic isolation and restart supervision for the
// daemon's long-running background components. It complements — not replaces —
// the ContainerRunner restart machinery (containers) and the watchkeeper's
// crash-loop detector (cross-container): supervise covers the pieces that had
// NOTHING before it — boot-time goroutines and panic containment (sp-i01z).
// Before this package, the only recover() in production code was route
// execution (navigate_route.go); a panic in any coordinator iteration, timer
// callback, or boot loop killed the entire daemon process.
package supervise

import (
	"fmt"
	"log"
	"runtime/debug"
)

// PanicError is a recovered panic converted into an error so it can flow
// through ordinary error-handling (ContainerRunner.handleError, Supervisor
// restart). Stack is captured at recovery time for diagnosis.
type PanicError struct {
	Component string
	Value     any
	Stack     []byte
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("panic in %s: %v", e.Component, e.Value)
}

// CapturePanic is deferred inside a function with a named error return:
//
//	func run() (err error) {
//		defer supervise.CapturePanic(&err, "my-component")
//		...
//	}
//
// If the function panics, the panic is converted into a *PanicError assigned
// to *errp (and the stack is logged immediately, since callers usually log
// only err.Error()). If the function returns normally, *errp is untouched.
func CapturePanic(errp *error, component string) {
	if r := recover(); r != nil {
		perr := &PanicError{Component: component, Value: r, Stack: debug.Stack()}
		log.Printf("supervise: %s\n%s", perr.Error(), perr.Stack)
		*errp = perr
	}
}

// Guard runs fn and suppresses (but loudly logs) a panic. For fire-and-forget
// callbacks with no error channel — time.AfterFunc bodies, one-shot boot
// goroutines — where "log and survive" is the only sane recovery.
func Guard(component string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("supervise: panic in %s (suppressed): %v\n%s", component, r, debug.Stack())
		}
	}()
	fn()
}
