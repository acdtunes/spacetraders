package api

import (
	"context"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
)

func TestWithSource_RoundTrips(t *testing.T) {
	ctx := WithSource(context.Background(), apibudget.SourceCharting)
	if got := sourceFromContext(ctx); got != apibudget.SourceCharting {
		t.Fatalf("sourceFromContext = %q, want charting", got)
	}
	if got := sourceFromContext(context.Background()); got != apibudget.SourceUnspecified {
		t.Fatalf("untagged ctx = %q, want empty", got)
	}
}
