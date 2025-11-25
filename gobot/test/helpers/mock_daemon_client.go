package helpers

import (
	"context"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
	"github.com/andrescamacho/spacetraders-go/internal/domain/daemon"
)

// Container is an alias for daemon.ContainerInfo for convenience
type Container = daemon.ContainerInfo

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
	mockContainers    []*MockContainer       // Simplified containers with metadata
	createdContainers []string               // Track container IDs created during test
	scoutTourCommands map[string]interface{} // containerID -> command
	CreatedContainers []*container.Container // Domain container entities created during test
}

// NewMockDaemonClient creates a new mock daemon client
func NewMockDaemonClient() *MockDaemonClient {
	return &MockDaemonClient{
		containers:        []Container{},
		mockContainers:    []*MockContainer{},
		createdContainers: []string{},
		scoutTourCommands: make(map[string]interface{}),
		CreatedContainers: []*container.Container{},
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
			PlayerID: v.PlayerID,
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
		if c.PlayerID == int(playerID) {
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
		PlayerID: int(playerID),
		Status:   "RUNNING",
		Type:     "scout-tour",
	})

	return nil
}

// StopContainer stops a running container
func (m *MockDaemonClient) StopContainer(ctx context.Context, containerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Find and update the container status
	for i, c := range m.containers {
		if c.ID == containerID {
			m.containers[i].Status = "STOPPED"
			break
		}
	}

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
	m.CreatedContainers = []*container.Container{}
}

// CreateContractWorkflowContainer creates AND STARTS a contract workflow container
func (m *MockDaemonClient) CreateContractWorkflowContainer(ctx context.Context, containerID string, playerID uint, command interface{}, completionCallback chan<- string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.createdContainers = append(m.createdContainers, containerID)
	m.containers = append(m.containers, Container{
		ID:       containerID,
		PlayerID: int(playerID),
		Status:   "RUNNING",
		Type:     "contract-workflow",
	})

	return nil
}

// PersistContractWorkflowContainer creates but does NOT start a contract workflow container
func (m *MockDaemonClient) PersistContractWorkflowContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.createdContainers = append(m.createdContainers, containerID)
	m.containers = append(m.containers, Container{
		ID:       containerID,
		PlayerID: int(playerID),
		Status:   "PENDING",
		Type:     "contract-workflow",
	})

	return nil
}

// StartContractWorkflowContainer starts a previously persisted contract workflow container
func (m *MockDaemonClient) StartContractWorkflowContainer(ctx context.Context, containerID string, completionCallback chan<- string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Find and update the container status
	for i, c := range m.containers {
		if c.ID == containerID {
			m.containers[i].Status = "RUNNING"
			break
		}
	}

	return nil
}

// PersistMiningWorkerContainer creates but does NOT start a mining worker container
func (m *MockDaemonClient) PersistMiningWorkerContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.createdContainers = append(m.createdContainers, containerID)
	m.containers = append(m.containers, Container{
		ID:       containerID,
		PlayerID: int(playerID),
		Status:   "PENDING",
		Type:     "mining-worker",
	})

	return nil
}

// StartMiningWorkerContainer starts a previously persisted mining worker container
func (m *MockDaemonClient) StartMiningWorkerContainer(ctx context.Context, containerID string, completionCallback chan<- string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Find and update the container status
	for i, c := range m.containers {
		if c.ID == containerID {
			m.containers[i].Status = "RUNNING"
			break
		}
	}

	return nil
}

// PersistTransportWorkerContainer creates but does NOT start a transport worker container
func (m *MockDaemonClient) PersistTransportWorkerContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.createdContainers = append(m.createdContainers, containerID)
	m.containers = append(m.containers, Container{
		ID:       containerID,
		PlayerID: int(playerID),
		Status:   "PENDING",
		Type:     "transport-worker",
	})

	return nil
}

// StartTransportWorkerContainer starts a previously persisted transport worker container
func (m *MockDaemonClient) StartTransportWorkerContainer(ctx context.Context, containerID string, completionCallback chan<- string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Find and update the container status
	for i, c := range m.containers {
		if c.ID == containerID {
			m.containers[i].Status = "RUNNING"
			break
		}
	}

	return nil
}

// PersistMiningCoordinatorContainer creates but does NOT start a mining coordinator container
func (m *MockDaemonClient) PersistMiningCoordinatorContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.createdContainers = append(m.createdContainers, containerID)
	m.containers = append(m.containers, Container{
		ID:       containerID,
		PlayerID: int(playerID),
		Status:   "PENDING",
		Type:     "mining-coordinator",
	})

	return nil
}

// StartMiningCoordinatorContainer starts a previously persisted mining coordinator container
func (m *MockDaemonClient) StartMiningCoordinatorContainer(ctx context.Context, containerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Find and update the container status
	for i, c := range m.containers {
		if c.ID == containerID {
			m.containers[i].Status = "RUNNING"
			break
		}
	}

	return nil
}

// PersistArbitrageWorkerContainer creates but does NOT start an arbitrage worker container
func (m *MockDaemonClient) PersistArbitrageWorkerContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.createdContainers = append(m.createdContainers, containerID)
	m.containers = append(m.containers, Container{
		ID:       containerID,
		PlayerID: int(playerID),
		Status:   "PENDING",
		Type:     "arbitrage-worker",
	})

	return nil
}

// StartArbitrageWorkerContainer starts a previously persisted arbitrage worker container
func (m *MockDaemonClient) StartArbitrageWorkerContainer(ctx context.Context, containerID string, completionCallback chan<- string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Find and update the container status
	for i, c := range m.containers {
		if c.ID == containerID {
			m.containers[i].Status = "RUNNING"
			break
		}
	}

	return nil
}

// PersistManufacturingWorkerContainer creates but does NOT start a manufacturing worker container
func (m *MockDaemonClient) PersistManufacturingWorkerContainer(ctx context.Context, containerID string, playerID uint, command interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.createdContainers = append(m.createdContainers, containerID)
	m.containers = append(m.containers, Container{
		ID:       containerID,
		PlayerID: int(playerID),
		Status:   "PENDING",
		Type:     "manufacturing-worker",
	})

	return nil
}

// StartManufacturingWorkerContainer starts a previously persisted manufacturing worker container
func (m *MockDaemonClient) StartManufacturingWorkerContainer(ctx context.Context, containerID string, completionCallback chan<- string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Find and update the container status
	for i, c := range m.containers {
		if c.ID == containerID {
			m.containers[i].Status = "RUNNING"
			break
		}
	}

	return nil
}
