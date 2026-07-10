package persistence

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// ContainerLivenessGORM answers which of a player's containers are still live, so the
// absorption ledger's sweep can reclaim a PLANNED hold whose owning container has died
// without releasing it (the sp-vjwb orphan-reconcile idiom). A container is LIVE while
// non-terminal (PENDING / RUNNING / STOPPING / INTERRUPTED) and dead once terminal
// (COMPLETED / FAILED / STOPPED); a container that has vanished from the table entirely
// is likewise absent from the live set. It reads the same containers table the daemon's
// own recovery consults, so "live" here means exactly what the daemon runtime means.
type ContainerLivenessGORM struct {
	db *gorm.DB
}

// liveContainerStatuses are the non-terminal statuses whose containers still legitimately
// hold absorption depth.
var liveContainerStatuses = []string{"PENDING", "RUNNING", "STOPPING", "INTERRUPTED"}

// NewContainerLiveness builds the DB-backed liveness provider for the absorption ledger.
func NewContainerLiveness(db *gorm.DB) *ContainerLivenessGORM {
	return &ContainerLivenessGORM{db: db}
}

// LiveContainerIDs returns the set of the player's currently live (non-terminal)
// container IDs.
func (l *ContainerLivenessGORM) LiveContainerIDs(ctx context.Context, playerID int) (map[string]struct{}, error) {
	var ids []string
	if err := l.db.WithContext(ctx).
		Model(&ContainerModel{}).
		Where("player_id = ? AND status IN ?", playerID, liveContainerStatuses).
		Pluck("id", &ids).Error; err != nil {
		return nil, fmt.Errorf("read live container ids: %w", err)
	}
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set, nil
}

// ContainerLivenessGORM satisfies the ledger's liveness port.
var _ ContainerLivenessProvider = (*ContainerLivenessGORM)(nil)
