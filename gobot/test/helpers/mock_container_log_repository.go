package helpers

import (
	"context"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/adapters/persistence"
)

// MockContainerLogRepository is an in-memory implementation of ContainerLogRepository for testing
type MockContainerLogRepository struct {
	Logs   map[string][]persistence.ContainerLogEntry // key: container_id
	LogErr error
}

// NewMockContainerLogRepository creates a new mock container log repository
func NewMockContainerLogRepository() *MockContainerLogRepository {
	return &MockContainerLogRepository{
		Logs: make(map[string][]persistence.ContainerLogEntry),
	}
}

// Log writes a log entry (in-memory only for testing)
func (m *MockContainerLogRepository) Log(ctx context.Context, containerID string, playerID int, message, level string) error {
	if m.LogErr != nil {
		return m.LogErr
	}

	entry := persistence.ContainerLogEntry{
		ContainerID: containerID,
		PlayerID:    playerID,
		Message:     message,
		Level:       level,
		Timestamp:   time.Now(),
	}

	m.Logs[containerID] = append(m.Logs[containerID], entry)
	return nil
}

// GetLogs retrieves logs for a container with optional filtering
func (m *MockContainerLogRepository) GetLogs(ctx context.Context, containerID string, playerID int, limit int, level *string, since *time.Time) ([]persistence.ContainerLogEntry, error) {
	return m.GetLogsWithOffset(ctx, containerID, playerID, limit, 0, level, since)
}

// GetLogsWithOffset retrieves logs for a container with pagination support
func (m *MockContainerLogRepository) GetLogsWithOffset(ctx context.Context, containerID string, playerID int, limit, offset int, level *string, since *time.Time) ([]persistence.ContainerLogEntry, error) {
	logs, exists := m.Logs[containerID]
	if !exists {
		return []persistence.ContainerLogEntry{}, nil
	}

	// Filter by level if specified
	filtered := make([]persistence.ContainerLogEntry, 0)
	for _, log := range logs {
		if level != nil && log.Level != *level {
			continue
		}
		if since != nil && log.Timestamp.Before(*since) {
			continue
		}
		filtered = append(filtered, log)
	}

	// Apply offset and limit
	if offset >= len(filtered) {
		return []persistence.ContainerLogEntry{}, nil
	}

	filtered = filtered[offset:]
	if limit > 0 && limit < len(filtered) {
		filtered = filtered[:limit]
	}

	return filtered, nil
}
