package persistence

import (
	"context"
	"fmt"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
	"gorm.io/gorm"
)

// GormManufacturingTaskRepository implements TaskRepository using GORM
type GormManufacturingTaskRepository struct {
	db *gorm.DB
}

// NewGormManufacturingTaskRepository creates a new GORM manufacturing task repository
func NewGormManufacturingTaskRepository(db *gorm.DB) *GormManufacturingTaskRepository {
	return &GormManufacturingTaskRepository{db: db}
}

// Create persists a new task
func (r *GormManufacturingTaskRepository) Create(ctx context.Context, task *manufacturing.ManufacturingTask) error {
	model := r.taskToModel(task)

	result := r.db.WithContext(ctx).Create(model)
	if result.Error != nil {
		return fmt.Errorf("failed to create task: %w", result.Error)
	}

	// Create dependencies
	for _, depID := range task.DependsOn() {
		dep := &ManufacturingTaskDependencyModel{
			TaskID:      task.ID(),
			DependsOnID: depID,
		}
		if err := r.db.WithContext(ctx).Create(dep).Error; err != nil {
			return fmt.Errorf("failed to create task dependency: %w", err)
		}
	}

	return nil
}

// CreateBatch persists multiple tasks in a single transaction
func (r *GormManufacturingTaskRepository) CreateBatch(ctx context.Context, tasks []*manufacturing.ManufacturingTask) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, task := range tasks {
			model := r.taskToModel(task)
			if err := tx.Create(model).Error; err != nil {
				return fmt.Errorf("failed to create task %s: %w", task.ID(), err)
			}

			// Create dependencies
			for _, depID := range task.DependsOn() {
				dep := &ManufacturingTaskDependencyModel{
					TaskID:      task.ID(),
					DependsOnID: depID,
				}
				if err := tx.Create(dep).Error; err != nil {
					return fmt.Errorf("failed to create task dependency: %w", err)
				}
			}
		}
		return nil
	})
}

// Update saves changes to an existing task
func (r *GormManufacturingTaskRepository) Update(ctx context.Context, task *manufacturing.ManufacturingTask) error {
	model := r.taskToModel(task)

	result := r.db.WithContext(ctx).Save(model)
	if result.Error != nil {
		return fmt.Errorf("failed to update task: %w", result.Error)
	}

	return nil
}

// FindByID retrieves a task by its ID
func (r *GormManufacturingTaskRepository) FindByID(ctx context.Context, id string) (*manufacturing.ManufacturingTask, error) {
	var model ManufacturingTaskModel
	result := r.db.WithContext(ctx).Where("id = ?", id).First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find task: %w", result.Error)
	}

	// Load dependencies
	deps, err := r.FindDependencies(ctx, id)
	if err != nil {
		return nil, err
	}

	return r.modelToTask(&model, deps)
}

// FindByPipelineID retrieves all tasks for a pipeline
func (r *GormManufacturingTaskRepository) FindByPipelineID(ctx context.Context, pipelineID string) ([]*manufacturing.ManufacturingTask, error) {
	var models []ManufacturingTaskModel
	result := r.db.WithContext(ctx).
		Where("pipeline_id = ?", pipelineID).
		Order("priority DESC, created_at ASC").
		Find(&models)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to find tasks for pipeline: %w", result.Error)
	}

	tasks := make([]*manufacturing.ManufacturingTask, len(models))
	for i, model := range models {
		deps, err := r.FindDependencies(ctx, model.ID)
		if err != nil {
			return nil, err
		}

		t, err := r.modelToTask(&model, deps)
		if err != nil {
			return nil, fmt.Errorf("failed to convert task model: %w", err)
		}
		tasks[i] = t
	}

	return tasks, nil
}

// FindByStatus retrieves tasks by status for a player
func (r *GormManufacturingTaskRepository) FindByStatus(ctx context.Context, playerID int, status manufacturing.TaskStatus) ([]*manufacturing.ManufacturingTask, error) {
	var models []ManufacturingTaskModel
	result := r.db.WithContext(ctx).
		Where("player_id = ? AND status = ?", playerID, string(status)).
		Order("priority DESC, created_at ASC").
		Find(&models)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to find tasks by status: %w", result.Error)
	}

	tasks := make([]*manufacturing.ManufacturingTask, len(models))
	for i, model := range models {
		deps, err := r.FindDependencies(ctx, model.ID)
		if err != nil {
			return nil, err
		}

		t, err := r.modelToTask(&model, deps)
		if err != nil {
			return nil, fmt.Errorf("failed to convert task model: %w", err)
		}
		tasks[i] = t
	}

	return tasks, nil
}

// FindReadyTasks retrieves all READY tasks for a player, sorted by priority
func (r *GormManufacturingTaskRepository) FindReadyTasks(ctx context.Context, playerID int) ([]*manufacturing.ManufacturingTask, error) {
	return r.FindByStatus(ctx, playerID, manufacturing.TaskStatusReady)
}

// FindByAssignedShip retrieves tasks assigned to a specific ship
func (r *GormManufacturingTaskRepository) FindByAssignedShip(ctx context.Context, shipSymbol string) (*manufacturing.ManufacturingTask, error) {
	var model ManufacturingTaskModel
	result := r.db.WithContext(ctx).
		Where("assigned_ship = ? AND status IN ?", shipSymbol, []string{
			string(manufacturing.TaskStatusAssigned),
			string(manufacturing.TaskStatusExecuting),
		}).
		First(&model)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to find task by ship: %w", result.Error)
	}

	deps, err := r.FindDependencies(ctx, model.ID)
	if err != nil {
		return nil, err
	}

	return r.modelToTask(&model, deps)
}

// FindIncomplete retrieves all non-terminal tasks for a player
func (r *GormManufacturingTaskRepository) FindIncomplete(ctx context.Context, playerID int) ([]*manufacturing.ManufacturingTask, error) {
	var models []ManufacturingTaskModel
	result := r.db.WithContext(ctx).
		Where("player_id = ? AND status NOT IN ?", playerID, []string{
			string(manufacturing.TaskStatusCompleted),
			string(manufacturing.TaskStatusFailed),
		}).
		Order("priority DESC, created_at ASC").
		Find(&models)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to find incomplete tasks: %w", result.Error)
	}

	tasks := make([]*manufacturing.ManufacturingTask, len(models))
	for i, model := range models {
		deps, err := r.FindDependencies(ctx, model.ID)
		if err != nil {
			return nil, err
		}

		t, err := r.modelToTask(&model, deps)
		if err != nil {
			return nil, fmt.Errorf("failed to convert task model: %w", err)
		}
		tasks[i] = t
	}

	return tasks, nil
}

// FindDependencies retrieves the task dependencies for a task
func (r *GormManufacturingTaskRepository) FindDependencies(ctx context.Context, taskID string) ([]string, error) {
	var deps []ManufacturingTaskDependencyModel
	result := r.db.WithContext(ctx).
		Where("task_id = ?", taskID).
		Find(&deps)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to find dependencies: %w", result.Error)
	}

	depIDs := make([]string, len(deps))
	for i, dep := range deps {
		depIDs[i] = dep.DependsOnID
	}

	return depIDs, nil
}

// AddDependency adds a dependency between tasks
func (r *GormManufacturingTaskRepository) AddDependency(ctx context.Context, taskID string, dependsOnID string) error {
	dep := &ManufacturingTaskDependencyModel{
		TaskID:      taskID,
		DependsOnID: dependsOnID,
	}

	result := r.db.WithContext(ctx).Create(dep)
	if result.Error != nil {
		return fmt.Errorf("failed to add dependency: %w", result.Error)
	}

	return nil
}

// Delete removes a task
func (r *GormManufacturingTaskRepository) Delete(ctx context.Context, id string) error {
	result := r.db.WithContext(ctx).Where("id = ?", id).Delete(&ManufacturingTaskModel{})
	if result.Error != nil {
		return fmt.Errorf("failed to delete task: %w", result.Error)
	}

	return nil
}

// taskToModel converts domain entity to database model
func (r *GormManufacturingTaskRepository) taskToModel(t *manufacturing.ManufacturingTask) *ManufacturingTaskModel {
	var errorMsg *string
	if t.ErrorMessage() != "" {
		msg := t.ErrorMessage()
		errorMsg = &msg
	}

	var pipelineID, sourceMarket, targetMarket, factorySymbol, assignedShip *string
	if t.PipelineID() != "" {
		s := t.PipelineID()
		pipelineID = &s
	}
	if t.SourceMarket() != "" {
		s := t.SourceMarket()
		sourceMarket = &s
	}
	if t.TargetMarket() != "" {
		s := t.TargetMarket()
		targetMarket = &s
	}
	if t.FactorySymbol() != "" {
		s := t.FactorySymbol()
		factorySymbol = &s
	}
	if t.AssignedShip() != "" {
		s := t.AssignedShip()
		assignedShip = &s
	}

	return &ManufacturingTaskModel{
		ID:             t.ID(),
		PipelineID:     pipelineID,
		PlayerID:       t.PlayerID(),
		TaskType:       string(t.TaskType()),
		Status:         string(t.Status()),
		Good:           t.Good(),
		Quantity:       t.Quantity(),
		ActualQuantity: t.ActualQuantity(),
		SourceMarket:   sourceMarket,
		TargetMarket:   targetMarket,
		FactorySymbol:  factorySymbol,
		AssignedShip:   assignedShip,
		Priority:       t.Priority(),
		RetryCount:     t.RetryCount(),
		MaxRetries:     t.MaxRetries(),
		TotalCost:      t.TotalCost(),
		TotalRevenue:   t.TotalRevenue(),
		ErrorMessage:   errorMsg,
		CreatedAt:      t.CreatedAt(),
		ReadyAt:        t.ReadyAt(),
		StartedAt:      t.StartedAt(),
		CompletedAt:    t.CompletedAt(),
	}
}

// modelToTask converts database model to domain entity
func (r *GormManufacturingTaskRepository) modelToTask(m *ManufacturingTaskModel, deps []string) (*manufacturing.ManufacturingTask, error) {
	var errorMsg string
	if m.ErrorMessage != nil {
		errorMsg = *m.ErrorMessage
	}

	var pipelineID, sourceMarket, targetMarket, factorySymbol, assignedShip string
	if m.PipelineID != nil {
		pipelineID = *m.PipelineID
	}
	if m.SourceMarket != nil {
		sourceMarket = *m.SourceMarket
	}
	if m.TargetMarket != nil {
		targetMarket = *m.TargetMarket
	}
	if m.FactorySymbol != nil {
		factorySymbol = *m.FactorySymbol
	}
	if m.AssignedShip != nil {
		assignedShip = *m.AssignedShip
	}

	return manufacturing.ReconstituteTask(
		m.ID,
		pipelineID,
		m.PlayerID,
		manufacturing.TaskType(m.TaskType),
		manufacturing.TaskStatus(m.Status),
		m.Good,
		m.Quantity,
		m.ActualQuantity,
		sourceMarket,
		targetMarket,
		factorySymbol,
		deps,
		assignedShip,
		m.Priority,
		m.RetryCount,
		m.MaxRetries,
		m.TotalCost,
		m.TotalRevenue,
		errorMsg,
		m.CreatedAt,
		m.ReadyAt,
		m.StartedAt,
		m.CompletedAt,
	), nil
}
