package persistence

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"gorm.io/gorm"
)

// ContainerLogRepository manages container log persistence
type ContainerLogRepository interface {
	// Log writes a log entry to the database with deduplication
	Log(ctx context.Context, containerID string, playerID int, message, level string, metadata map[string]interface{}) error

	// GetLogs retrieves logs for a container with optional filtering
	GetLogs(ctx context.Context, containerID string, playerID int, limit int, level *string, since *time.Time) ([]ContainerLogEntry, error)

	// GetLogsWithOffset retrieves logs for a container with pagination support
	GetLogsWithOffset(ctx context.Context, containerID string, playerID int, limit, offset int, level *string, since *time.Time) ([]ContainerLogEntry, error)
}

// ContainerLogEntry represents a log entry
type ContainerLogEntry struct {
	ID          int
	ContainerID string
	PlayerID    int
	Timestamp   time.Time
	Level       string
	Message     string
	Metadata    map[string]interface{}
}

// GormContainerLogRepository is a GORM-based implementation
type GormContainerLogRepository struct {
	db    *gorm.DB
	clock shared.Clock

	// Deduplication cache (matches Python implementation)
	dedupCache   map[string]time.Time // key: containerID+message, value: last logged time
	dedupMu      sync.Mutex
	dedupWindow  time.Duration
	dedupMaxSize int
}

// NewGormContainerLogRepository creates a new container log repository
// If clock is nil, uses RealClock (production behavior)
func NewGormContainerLogRepository(db *gorm.DB, clock shared.Clock) *GormContainerLogRepository {
	if clock == nil {
		clock = shared.NewRealClock()
	}
	return &GormContainerLogRepository{
		db:           db,
		clock:        clock,
		dedupCache:   make(map[string]time.Time),
		dedupWindow:  60 * time.Second, // 60-second deduplication window (matches Python)
		dedupMaxSize: 10000,            // Max cache entries before cleanup
	}
}

// Log writes a log entry with time-windowed deduplication
func (r *GormContainerLogRepository) Log(ctx context.Context, containerID string, playerID int, message, level string, metadata map[string]interface{}) error {
	now := r.clock.Now()
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

	// Marshal metadata to JSON string
	var metadataJSON string
	if metadata != nil && len(metadata) > 0 {
		jsonBytes, err := json.Marshal(metadata)
		if err != nil {
			// Log warning but continue (metadata is optional)
			metadataJSON = ""
		} else {
			metadataJSON = string(jsonBytes)
		}
	}

	// Persist to database
	logEntry := &ContainerLogModel{
		ContainerID: containerID,
		PlayerID:    playerID,
		Timestamp:   now,
		Level:       level,
		Message:     message,
		Metadata:    metadataJSON,
	}

	return r.db.WithContext(ctx).Create(logEntry).Error
}

// cleanupDedupCache removes old entries from the deduplication cache
// Must be called while holding dedupMu lock
func (r *GormContainerLogRepository) cleanupDedupCache() {
	now := r.clock.Now()
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
		// Unmarshal metadata if present
		var metadata map[string]interface{}
		if model.Metadata != "" {
			if err := json.Unmarshal([]byte(model.Metadata), &metadata); err != nil {
				// If unmarshal fails, leave metadata as nil
				metadata = nil
			}
		}

		entries[i] = ContainerLogEntry{
			ID:          model.ID,
			ContainerID: model.ContainerID,
			PlayerID:    model.PlayerID,
			Timestamp:   model.Timestamp,
			Level:       model.Level,
			Message:     model.Message,
			Metadata:    metadata,
		}
	}

	return entries, nil
}

// GetLogsWithOffset retrieves logs for a container with pagination support
func (r *GormContainerLogRepository) GetLogsWithOffset(ctx context.Context, containerID string, playerID int, limit, offset int, level *string, since *time.Time) ([]ContainerLogEntry, error) {
	var models []ContainerLogModel

	query := r.db.WithContext(ctx).
		Where("container_id = ? AND player_id = ?", containerID, playerID)

	if level != nil {
		query = query.Where("level = ?", *level)
	}

	if since != nil {
		query = query.Where("timestamp > ?", *since)
	}

	query = query.Order("timestamp DESC").Limit(limit).Offset(offset)

	if err := query.Find(&models).Error; err != nil {
		return nil, err
	}

	// Convert models to entries
	entries := make([]ContainerLogEntry, len(models))
	for i, model := range models {
		// Unmarshal metadata if present
		var metadata map[string]interface{}
		if model.Metadata != "" {
			if err := json.Unmarshal([]byte(model.Metadata), &metadata); err != nil {
				// If unmarshal fails, leave metadata as nil
				metadata = nil
			}
		}

		entries[i] = ContainerLogEntry{
			ID:          model.ID,
			ContainerID: model.ContainerID,
			PlayerID:    model.PlayerID,
			Timestamp:   model.Timestamp,
			Level:       model.Level,
			Message:     model.Message,
			Metadata:    metadata,
		}
	}

	return entries, nil
}
