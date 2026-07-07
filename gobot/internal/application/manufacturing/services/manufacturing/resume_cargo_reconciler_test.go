package manufacturing

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/navigation"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// resumeReconcilerFakeNav is a minimal Navigator whose ReloadShipFromAPI stands in
// for the authoritative GET /my/ships. It records whether the server was hit and can
// force a refresh error to exercise the best-effort fallback.
type resumeReconcilerFakeNav struct {
	Navigator

	apiShip   *navigation.Ship
	err       error
	apiCalled bool
}

func (n *resumeReconcilerFakeNav) ReloadShipFromAPI(_ context.Context, _ string, _ shared.PlayerID) (*navigation.Ship, error) {
	n.apiCalled = true
	if n.err != nil {
		return nil, n.err
	}
	return n.apiShip, nil
}

// A cached non-empty hold that the server reports empty is a phantom desync (L47).
// The reconciler must force a server refresh and return the server-truth (empty)
// ship so the caller takes the acquire branch instead of resuming into a doomed
// sell/deliver. It logs an INFO "cache refreshed, cargo N/M" audit line - counts in
// the MESSAGE, because the container log renderer drops structured map fields.
func TestReconcileResumeCargo_PhantomCacheRefreshesToServerTruth(t *testing.T) {
	cached := newConstructionTestShipWithCargo(t, "FAB_MATS", 40) // cache: 40 phantom units
	server := newConstructionTestShip(t)                          // server: hold actually empty
	nav := &resumeReconcilerFakeNav{apiShip: server}
	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	got := reconcileResumeCargo(ctx, nav, cached, "FAB_MATS", "TORWIND-3", shared.MustNewPlayerID(1))

	if !nav.apiCalled {
		t.Fatalf("expected a server refresh (GET /my/ships) when the cache claims cargo")
	}
	if got.Cargo().HasItem("FAB_MATS", 1) {
		t.Fatalf("expected the reconciled ship to reflect the empty server hold, got %d units", got.Cargo().GetItemUnits("FAB_MATS"))
	}

	var found bool
	for _, e := range logger.entries {
		if e.level == "INFO" && strings.Contains(e.message, "cache refreshed") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected an INFO 'cache refreshed' audit line in the message; got %+v", logger.entries)
	}
}

// A cache that already reads empty will take the acquire branch anyway, so the
// reconciler must NOT waste a server round-trip on it - it returns the cached ship
// unchanged. Guards against turning every fresh task into an extra GET /my/ships.
func TestReconcileResumeCargo_EmptyCacheSkipsServerRoundTrip(t *testing.T) {
	cached := newConstructionTestShip(t) // cache already empty
	nav := &resumeReconcilerFakeNav{apiShip: newConstructionTestShipWithCargo(t, "FAB_MATS", 40)}

	got := reconcileResumeCargo(context.Background(), nav, cached, "FAB_MATS", "TORWIND-3", shared.MustNewPlayerID(1))

	if nav.apiCalled {
		t.Fatalf("expected NO server round-trip when the cache is already empty")
	}
	if got != cached {
		t.Fatalf("expected the cached ship returned unchanged when the cache is empty")
	}
}

// The refresh is best-effort: a transient API failure must fall back to the cached
// ship so a genuinely loaded ship is never stranded out of its resume branch by a
// hiccup. The caller then proceeds on the cached hold exactly as before the guard.
func TestReconcileResumeCargo_RefreshFailureFallsBackToCache(t *testing.T) {
	cached := newConstructionTestShipWithCargo(t, "FAB_MATS", 40)
	nav := &resumeReconcilerFakeNav{err: errors.New("api boom")}
	logger := &capturingLogger{}
	ctx := common.WithLogger(context.Background(), logger)

	got := reconcileResumeCargo(ctx, nav, cached, "FAB_MATS", "TORWIND-3", shared.MustNewPlayerID(1))

	if got != cached {
		t.Fatalf("expected fallback to the cached ship on refresh failure so a loaded ship is never stranded")
	}
	if !got.Cargo().HasItem("FAB_MATS", 1) {
		t.Fatalf("expected the cached cargo preserved on fallback")
	}
}
