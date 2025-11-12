package persistence

import (
	"context"
	"sync"
	"time"

	"gorm.io/gorm"
)

// ContainerLogRepository manages container log persistence
type ContainerLogRepository interface {
	// Log writes a log entry to the database with deduplication
	Log(ctx context.Context, containerID string, playerID int, message, level string) error

	// GetLogs retrieves logs for a container with optional filtering
	GetLogs(ctx context.Context, containerID string, playerID int, limit int, level *string, since *time.Time) ([]ContainerLogEntry, error)
}

// ContainerLogEntry represents a log entry
type ContainerLogEntry struct {
	LogID       int
	ContainerID string
	PlayerID    int
	Timestamp   time.Time
	Level       string
	Message     string
}

// GormContainerLogRepository is a GORM-based implementation
type GormContainerLogRepository struct {
	db *gorm.DB

	// Deduplication cache (matches Python implementation)
	dedupCache   map[string]time.Time // key: containerID+message, value: last logged time
	dedupMu      sync.Mutex
	dedupWindow  time.Duration
	dedupMaxSize int
}

// NewGormContainerLogRepository creates a new container log repository
func NewGormContainerLogRepository(db *gorm.DB) *GormContainerLogRepository {
	return &GormContainerLogRepository{
		db:           db,
		dedupCache:   make(map[string]time.Time),
		dedupWindow:  60 * time.Second, // 60-second deduplication window (matches Python)
		dedupMaxSize: 10000,            // Max cache entries before cleanup
	}
}

// Log writes a log entry with time-windowed deduplication
func (r *GormContainerLogRepository) Log(ctx context.Context, containerID string, playerID int, message, level string) error {
	now := time.Now()
	cacheKey := containerID + "|" + message

	// Thread-safe deduplication check
	r.dedupMu.Lock()

	// Check if this message was logged recently
	if lastLogged, exists := r.dedupCache[cacheKey]; exists {
		if now.Sub(lastLogged) < r.dedupWindow {
			// Duplicate within window, skip logging
			r.dedupMu.Unlock()
			return nil
		}
	}

	// Clean up cache if it's getting too large
	if len(r.dedupCache) >= r.dedupMaxSize {
		r.cleanupDedupCache()
	}

	// Update cache with current timestamp
	r.dedupCache[cacheKey] = now
	r.dedupMu.Unlock()

	// Persist to database
	logEntry := &ContainerLogModel{
		ContainerID: containerID,
		PlayerID:    playerID,
		Timestamp:   now,
		Level:       level,
		Message:     message,
	}

	return r.db.WithContext(ctx).Create(logEntry).Error
}

// cleanupDedupCache removes old entries from the deduplication cache
// Must be called while holding dedupMu lock
func (r *GormContainerLogRepository) cleanupDedupCache() {
	now := time.Now()
	cutoff := now.Add(-r.dedupWindow)

	// Remove entries older than the deduplication window
	for key, timestamp := range r.dedupCache {
		if timestamp.Before(cutoff) {
			delete(r.dedupCache, key)
		}
	}
}

// GetLogs retrieves logs for a container with optional filtering
func (r *GormContainerLogRepository) GetLogs(ctx context.Context, containerID string, playerID int, limit int, level *string, since *time.Time) ([]ContainerLogEntry, error) {
	var models []ContainerLogModel

	query := r.db.WithContext(ctx).
		Where("container_id = ? AND player_id = ?", containerID, playerID)

	if level != nil {
		query = query.Where("level = ?", *level)
	}

	if since != nil {
		query = query.Where("timestamp > ?", *since)
	}

	query = query.Order("timestamp DESC").Limit(limit)

	if err := query.Find(&models).Error; err != nil {
		return nil, err
	}

	// Convert models to entries
	entries := make([]ContainerLogEntry, len(models))
	for i, model := range models {
		entries[i] = ContainerLogEntry{
			LogID:       model.LogID,
			ContainerID: model.ContainerID,
			PlayerID:    model.PlayerID,
			Timestamp:   model.Timestamp,
			Level:       model.Level,
			Message:     model.Message,
		}
	}

	return entries, nil
}
