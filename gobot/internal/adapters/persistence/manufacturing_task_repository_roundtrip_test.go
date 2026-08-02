package persistence

import (
	"reflect"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// Every field carries a DISTINCT sentinel: two fields holding equal values make a
// transposition between them invisible to a round-trip assertion.
func manufacturingTaskRoundTripFixture() (*ManufacturingTaskModel, []string) {
	base := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	str := func(s string) *string { return &s }
	at := func(minutes int) *time.Time {
		v := base.Add(time.Duration(minutes) * time.Minute)
		return &v
	}

	model := &ManufacturingTaskModel{
		ID:                    "task-01",
		PipelineID:            str("pipeline-02"),
		PlayerID:              103,
		TaskType:              string(manufacturing.TaskTypeStorageAcquireDeliver),
		Status:                string(manufacturing.TaskStatusExecuting),
		Good:                  "GOOD-06",
		Quantity:              107,
		ActualQuantity:        108,
		SourceMarket:          str("SOURCE-09"),
		TargetMarket:          str("TARGET-10"),
		FactorySymbol:         str("FACTORY-11"),
		StorageOperationID:    str("STORAGE-OP-12"),
		StorageWaypoint:       str("STORAGE-WP-13"),
		ConstructionSite:      str("CONSTRUCTION-14"),
		AssignedShip:          str("SHIP-16"),
		Priority:              117,
		RetryCount:            118,
		MaxRetries:            119,
		TotalCost:             120,
		TotalRevenue:          121,
		ErrorMessage:          str("error-22"),
		CreatedAt:             base,
		ReadyAt:               at(24),
		StartedAt:             at(25),
		CompletedAt:           at(26),
		CollectPhaseCompleted: true,
		AcquirePhaseCompleted: false,
		PhaseCompletedAt:      at(29),
	}
	return model, []string{"dep-15a", "dep-15b"}
}

func derefForCompare(v reflect.Value) any {
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return nil
		}
		return v.Elem().Interface()
	}
	return v.Interface()
}

func TestManufacturingTaskRoundTripFixtureSentinelsAreDistinct(t *testing.T) {
	model, _ := manufacturingTaskRoundTripFixture()

	seen := map[any]string{}
	v := reflect.ValueOf(*model)
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		value := derefForCompare(v.Field(i))
		if value == nil {
			t.Errorf("field %s is nil: a nil sentinel cannot detect a transposition", name)
			continue
		}
		if prior, dup := seen[value]; dup {
			t.Errorf("fields %s and %s share sentinel %v: a swap between them would be invisible", prior, name, value)
			continue
		}
		seen[value] = name
	}
}

func TestManufacturingTaskModelRoundTripsThroughDomain(t *testing.T) {
	repo := &GormManufacturingTaskRepository{}
	model, deps := manufacturingTaskRoundTripFixture()

	task, err := repo.modelToTask(model, deps)
	if err != nil {
		t.Fatalf("modelToTask returned error: %v", err)
	}
	got := repo.taskToModel(task)

	want := reflect.ValueOf(*model)
	have := reflect.ValueOf(*got)
	for i := 0; i < want.NumField(); i++ {
		name := want.Type().Field(i).Name
		w := derefForCompare(want.Field(i))
		h := derefForCompare(have.Field(i))
		if !reflect.DeepEqual(w, h) {
			t.Errorf("field %s did not round-trip: got %v, want %v", name, h, w)
		}
	}

	// DependsOn has no column; it travels as modelToTask's deps argument.
	if !reflect.DeepEqual(task.DependsOn(), deps) {
		t.Errorf("DependsOn did not round-trip: got %v, want %v", task.DependsOn(), deps)
	}
}
