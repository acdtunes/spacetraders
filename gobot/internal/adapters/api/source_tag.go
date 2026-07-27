package api

import (
	"context"

	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
)

type sourceContextKey struct{}

// WithSource tags ctx with the fleet activity driving the calls made under it,
// so every API attempt on that path is attributed to the right consumer of the
// shared request budget. Callers tag at the top of a unit of work (a tour, a
// delivery, a scan sweep) and every nested call inherits it.
//
// The tag is an accounting label, not a scheduling one: it never changes the
// order or rate of token acquisition. Priority is a separate, orthogonal axis
// (see WithPriority).
func WithSource(ctx context.Context, s apibudget.Source) context.Context {
	return context.WithValue(ctx, sourceContextKey{}, s)
}

// sourceFromContext reads the source tag, returning apibudget.SourceUnspecified
// for an untagged context. Untagged is a valid state — the residual arithmetic
// counts it as non-sensing, so a missing tag can only shrink the sensing
// budget, never inflate it.
func sourceFromContext(ctx context.Context) apibudget.Source {
	s, _ := ctx.Value(sourceContextKey{}).(apibudget.Source)
	return s
}

// SourceForTest reads the source tag back out of a context, for tests in OTHER
// packages.
//
// It exists because the tag is keyed by an unexported type: an adapter whose
// entire job is to attribute its calls to the right budget consumer has no way
// to prove it did so, and a mis-tagged call is invisible at runtime — it does
// not fail, it just silently shifts spend between budgets. Production code reads
// the tag through sourceFromContext; nothing but a test should call this.
func SourceForTest(ctx context.Context) apibudget.Source {
	return sourceFromContext(ctx)
}
