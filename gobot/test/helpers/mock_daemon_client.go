package helpers

import (
	"context"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
)

// Container is an alias for daemon.Container for convenience
type Container = daemon.Container

// MockContainer represents a simplified container for testing
type MockContainer struct {
	ID       string
	PlayerID int
	Status   string
	Type     string
}

// MockDaemonClient simulates daemon container operations for testing
type MockDaemonClient struct {
	mu sync.RWMutex

	containers        []Container
	mockContainers    []*MockContainer   // Simplified containers with metadata
	createdContainers []string            // Track container IDs created during test
	scoutTourCommands map[string]interface{} // containerID -> command
}

// NewMockDaemonClient creates a new mock daemon client
func NewMockDaemonClient() *MockDaemonClient {
	return &MockDaemonClient{
		containers:        []Container{},
		mockContainers:    []*MockContainer{},
		createdContainers: []string{},
		scoutTourCommands: make(map[string]interface{}),
	}
}

// AddContainer adds a pre-existing container (for testing reuse scenarios)
func (m *MockDaemonClient) AddContainer(container interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	switch v := container.(type) {
	case Container:
		m.containers = append(m.containers, v)
	case *MockContainer:
		m.mockContainers = append(m.mockContainers, v)
		// Also add to containers list for compatibility
		m.containers = append(m.containers, Container{
			ID:       v.ID,
			PlayerID: uint(v.PlayerID),
			Status:   v.Status,
			Type:     v.Type,
		})
	}
}

// ListContainers returns all containers for a player
func (m *MockDaemonClient) ListContainers(ctx context.Context, playerID uint) ([]Container, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := []Container{}
	for _, c := range m.containers {
		if c.PlayerID == playerID {
			result = append(result, c)
		}
	}
	return result, nil
}

// CreateScoutTourContainer creates a new scout tour container
func (m *MockDaemonClient) CreateScoutTourContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.createdContainers = append(m.createdContainers, containerID)
	m.scoutTourCommands[containerID] = command

	// Add to containers list
	m.containers = append(m.containers, Container{
		ID:       containerID,
		PlayerID: playerID,
		Status:   "RUNNING",
		Type:     "scout-tour",
	})

	return nil
}

// GetCreatedContainers returns the list of container IDs created during the test
func (m *MockDaemonClient) GetCreatedContainers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]string{}, m.createdContainers...)
}

// GetScoutTourCommand retrieves the command for a specific container
func (m *MockDaemonClient) GetScoutTourCommand(containerID string) (interface{}, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	cmd, ok := m.scoutTourCommands[containerID]
	return cmd, ok
}

// Reset clears all state (useful between test scenarios)
func (m *MockDaemonClient) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.containers = []Container{}
	m.createdContainers = []string{}
	m.scoutTourCommands = make(map[string]interface{})
}
