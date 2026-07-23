package persistence

import (
	"context"
	"encoding/json"
	"fmt"
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

	r.dedupMu.Lock()

	if lastLogged, exists := r.dedupCache[cacheKey]; exists {
		if now.Sub(lastLogged) < r.dedupWindow {
			r.dedupMu.Unlock()
			return nil
		}
	}

	if len(r.dedupCache) >= r.dedupMaxSize {
		r.cleanupDedupCache()
	}

	r.dedupCache[cacheKey] = now
	r.dedupMu.Unlock()

	// Marshal metadata to JSON string (or nil for NULL in database)
	var metadataJSON *string
	if metadata != nil && len(metadata) > 0 {
		jsonBytes, err := json.Marshal(metadata)
		if err != nil {
			// Log warning but continue (metadata is optional)
			// Leave nil which will be stored as NULL
			metadataJSON = nil
		} else {
			jsonStr := string(jsonBytes)
			metadataJSON = &jsonStr
		}
	}
	// If metadata is nil or empty, metadataJSON remains nil which GORM will store as NULL

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

	for key, timestamp := range r.dedupCache {
		if timestamp.Before(cutoff) {
			delete(r.dedupCache, key)
		}
	}
}

// LatestLogTimestamps returns, for each of the given container IDs, the timestamp of its most
// recent log line — the daemon's persistent per-container ACTIVITY signal (sp-m3122). A tour
// logs on every real step (plan/navigate/arrive/buy/sell) and each iteration boundary, while
// the wall-clock heartbeat writes NO log on success, so the newest log timestamp advances on
// real progress and STALLS the moment a tour goes silent — exactly what the liveness watchdog
// needs to tell a working tour from a RUNNING-but-hung one (the incident's "last log line
// 04:34:02, silent 2h, still RUNNING"). Containers with no logs are simply absent from the
// map, so the watchdog leaves them alone (fail-closed on unknown progress).
func (r *GormContainerLogRepository) LatestLogTimestamps(ctx context.Context, playerID int, containerIDs []string) (map[string]time.Time, error) {
	out := make(map[string]time.Time, len(containerIDs))
	// One newest-row read per container, mirroring GetLogs' proven shape (WHERE
	// container_id+player_id, ORDER BY timestamp DESC, LIMIT 1) — read into the model so
	// GORM parses the datetime column on BOTH sqlite (tests) and postgres (prod), which a
	// SELECT MAX(timestamp) aggregate does not (sqlite returns it as a bare string). N is the
	// running-tour count (tens), the read runs at the reconcile cadence (30s), and the query
	// is index-identical to GetLogs, so the per-container loop is cheap.
	for _, id := range containerIDs {
		if id == "" {
			continue
		}
		var newest []ContainerLogModel
		if err := r.db.WithContext(ctx).
			Where("container_id = ? AND player_id = ?", id, playerID).
			Order("timestamp DESC").
			Limit(1).
			Find(&newest).Error; err != nil {
			return nil, fmt.Errorf("read latest container log timestamp for %s: %w", id, err)
		}
		if len(newest) == 1 && !newest[0].Timestamp.IsZero() {
			out[id] = newest[0].Timestamp
		}
	}
	return out, nil
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

	return containerLogModelsToEntries(models), nil
}

func containerLogModelsToEntries(models []ContainerLogModel) []ContainerLogEntry {
	entries := make([]ContainerLogEntry, len(models))
	for i, model := range models {
		var metadata map[string]interface{}
		if model.Metadata != nil && *model.Metadata != "" {
			if err := json.Unmarshal([]byte(*model.Metadata), &metadata); err != nil {
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

	return entries
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

	return containerLogModelsToEntries(models), nil
}
