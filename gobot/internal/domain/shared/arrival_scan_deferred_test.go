package shared

import (
	"context"
	"testing"
)

// The arrival-scan deferral marker is waypoint-scoped on purpose: it says "this ONE
// market gets its live read from a money guard seconds from now", never "stop scanning
// on this flight". These pin the round-trip and the two absent shapes that must read as
// "no deferral", so a missing or blank stamp can never suppress a scan.
func TestArrivalScanDeferred_RoundTrip(t *testing.T) {
	ctx := WithArrivalScanDeferred(context.Background(), "X1-AB12-C34", ArrivalScanSideSell)
	waypoint, ok := ArrivalScanDeferredFromContext(ctx)
	if !ok || waypoint != "X1-AB12-C34" {
		t.Fatalf("round-trip = (%q, %t), want (\"X1-AB12-C34\", true)", waypoint, ok)
	}
	if side := ArrivalScanDeferredSideFromContext(ctx); side != ArrivalScanSideSell {
		t.Fatalf("side = %q, want %q", side, ArrivalScanSideSell)
	}
}

func TestArrivalScanDeferred_AbsentOrBlankReadsAsNoDeferral(t *testing.T) {
	if waypoint, ok := ArrivalScanDeferredFromContext(context.Background()); ok || waypoint != "" {
		t.Fatalf("unstamped context = (%q, %t), want (\"\", false)", waypoint, ok)
	}
	if side := ArrivalScanDeferredSideFromContext(context.Background()); side != "" {
		t.Fatalf("unstamped side = %q, want \"\"", side)
	}
	blank := WithArrivalScanDeferred(context.Background(), "", ArrivalScanSideBuy)
	if waypoint, ok := ArrivalScanDeferredFromContext(blank); ok || waypoint != "" {
		t.Fatalf("blank stamp = (%q, %t), want (\"\", false)", waypoint, ok)
	}
	// A side without a waypoint defers nothing, so it must not read back as one either.
	if side := ArrivalScanDeferredSideFromContext(blank); side != "" {
		t.Fatalf("blank stamp side = %q, want \"\"", side)
	}
}
