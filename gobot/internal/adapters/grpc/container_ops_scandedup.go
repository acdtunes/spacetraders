package grpc

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// MutateScanDedupAllowlist adds or removes shipSymbol on the sp-7q61f
// scan-dedup A/B test's live allowlist for playerID, and returns the resulting
// armed set plus whether this call changed it. The daemon is the SOLE writer
// (RULINGS #3: no CLI-side direct write); the store defaults to empty, so the
// experiment stays inert until an operator explicitly arms a hull, and stays
// scoped to the exact ships an operator names — never a fleet-wide flip
// (PLAYBOOK #10).
func (s *DaemonServer) MutateScanDedupAllowlist(ctx context.Context, shipSymbol string, add bool, playerID int) ([]string, bool, error) {
	repo := persistence.NewScanDedupAllowlistGORM(s.db)
	var changed bool
	var err error
	if add {
		changed, err = repo.Add(ctx, playerID, shipSymbol)
	} else {
		changed, err = repo.Remove(ctx, playerID, shipSymbol)
	}
	if err != nil {
		return nil, false, fmt.Errorf("mutate scan-dedup allowlist for %s: %w", shipSymbol, err)
	}
	ships, err := repo.List(ctx, playerID)
	if err != nil {
		return nil, false, fmt.Errorf("list scan-dedup allowlist after mutation: %w", err)
	}
	return ships, changed, nil
}

// ListScanDedupAllowlist returns the current armed set for playerID, live.
func (s *DaemonServer) ListScanDedupAllowlist(ctx context.Context, playerID int) ([]string, error) {
	repo := persistence.NewScanDedupAllowlistGORM(s.db)
	return repo.List(ctx, playerID)
}
