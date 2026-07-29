package services

import (
	"context"
	"errors"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

// demandStubPipelineRepo embeds the domain interface so only FindByStatus needs a concrete
// implementation. It HONOURS the requested status filter rather than returning everything, so a
// test that hands it a CANCELLED pipeline is really exercising the reader's status scoping.
type demandStubPipelineRepo struct {
	manufacturing.PipelineRepository

	pipelines []*manufacturing.ManufacturingPipeline
	err       error
	asked     [][]manufacturing.PipelineStatus
}

func (r *demandStubPipelineRepo) FindByStatus(_ context.Context, _ int, statuses []manufacturing.PipelineStatus) ([]*manufacturing.ManufacturingPipeline, error) {
	r.asked = append(r.asked, append([]manufacturing.PipelineStatus(nil), statuses...))
	if r.err != nil {
		return nil, r.err
	}
	wanted := map[manufacturing.PipelineStatus]bool{}
	for _, s := range statuses {
		wanted[s] = true
	}
	var out []*manufacturing.ManufacturingPipeline
	for _, p := range r.pipelines {
		if wanted[p.Status()] {
			out = append(out, p)
		}
	}
	return out, nil
}

// constructionPipeline builds an EXECUTING construction pipeline carrying one material bill at
// delivered/target. Status is driven through the real transition so the fixture cannot claim a
// state the domain would refuse.
func constructionPipeline(t *testing.T, good string, delivered, target int) *manufacturing.ManufacturingPipeline {
	t.Helper()
	p := manufacturing.NewConstructionPipeline("X1-KP23-I53", 5, 0, 5)
	p.SetMaterials([]*manufacturing.ConstructionMaterialTarget{
		manufacturing.ReconstructConstructionMaterialTarget(good, target, delivered),
	})
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if p.Status() != manufacturing.PipelineStatusExecuting {
		t.Fatalf("fixture is %s, want EXECUTING", p.Status())
	}
	return p
}

// TestConstructionDemandReaderReadsTheBillNotTheTick is the anti-naive-fix test named in sp-bzvu2.
//
// The fixture is deliberately the state a promotion-count implementation gets WRONG: an executing
// pipeline that still owes 126 units of FAB_MATS while NOTHING has been promoted to READY and the
// task table is empty. A reader that answered from "tasks promoted last tick" would report idle
// here and hand trade 100% of the treasury while construction still owes a full bill. Reading the
// bill instead reports demand, and the trade side stays held to its share.
func TestConstructionDemandReaderReadsTheBillNotTheTick(t *testing.T) {
	repo := &demandStubPipelineRepo{pipelines: []*manufacturing.ManufacturingPipeline{
		constructionPipeline(t, "FAB_MATS", 0, 126),
	}}
	// No task repository is wired into the reader AT ALL — the signal cannot depend on task
	// state, which is exactly the property this test pins.
	has, err := NewConstructionDemandReader(repo).HasOutstandingConstructionDemand(context.Background(), 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !has {
		t.Fatal("an executing pipeline owing 126 FAB_MATS reported NO demand — trade would take 100% while construction still has a bill to fill")
	}
	if len(repo.asked) != 1 || len(repo.asked[0]) != 1 || repo.asked[0][0] != manufacturing.PipelineStatusExecuting {
		t.Fatalf("reader asked for %v, want exactly [EXECUTING]", repo.asked)
	}
}

// TestConstructionDemandReaderReleasesOnAFilledBill is the other half of the proof: the live
// era-5 state that motivated the bead. Every material target is met, so the drain has nothing
// left to buy however long it keeps ticking, and the reservation must be released.
func TestConstructionDemandReaderReleasesOnAFilledBill(t *testing.T) {
	repo := &demandStubPipelineRepo{pipelines: []*manufacturing.ManufacturingPipeline{
		constructionPipeline(t, "FAB_MATS", 1600, 1600),
	}}
	has, err := NewConstructionDemandReader(repo).HasOutstandingConstructionDemand(context.Background(), 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if has {
		t.Fatal("a fully delivered 1600/1600 bill still reported demand — trade stays capped at 60% funding nothing")
	}
}

// TestConstructionDemandReaderIgnoresPipelinesThatAreNotExecuting pins the status scoping against
// the exact live wreckage: two CANCELLED pipelines carrying unfilled targets (1474/1600 and
// 0/126) plus a stranded READY task. None of that is fundable work.
func TestConstructionDemandReaderIgnoresPipelinesThatAreNotExecuting(t *testing.T) {
	cancelled := manufacturing.NewConstructionPipeline("X1-KP23-I53", 5, 0, 5)
	cancelled.SetMaterials([]*manufacturing.ConstructionMaterialTarget{
		manufacturing.ReconstructConstructionMaterialTarget("FAB_MATS", 1600, 1474),
	})
	if err := cancelled.Cancel(); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	repo := &demandStubPipelineRepo{pipelines: []*manufacturing.ManufacturingPipeline{cancelled}}

	has, err := NewConstructionDemandReader(repo).HasOutstandingConstructionDemand(context.Background(), 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if has {
		t.Fatalf("a %s pipeline with 1474/1600 delivered reported demand — a dead bill would reserve capital forever", cancelled.Status())
	}
}

// TestConstructionDemandReaderResolvesUncertaintyTowardReserving pins every fail-conservative
// branch (RULINGS #4). Each case is one the reader cannot PROVE is satisfied, so each must
// report demand rather than release the reservation.
func TestConstructionDemandReaderResolvesUncertaintyTowardReserving(t *testing.T) {
	executingNoMaterials := manufacturing.NewConstructionPipeline("X1-KP23-I53", 5, 0, 5)
	if err := executingNoMaterials.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	cases := []struct {
		name string
		repo *demandStubPipelineRepo
	}{
		{
			name: "an executing construction pipeline with no recorded bill cannot be proven satisfied",
			repo: &demandStubPipelineRepo{pipelines: []*manufacturing.ManufacturingPipeline{executingNoMaterials}},
		},
		{
			name: "a partially filled bill is still owed",
			repo: &demandStubPipelineRepo{pipelines: []*manufacturing.ManufacturingPipeline{
				constructionPipeline(t, "FAB_MATS", 1599, 1600),
			}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			has, err := NewConstructionDemandReader(tc.repo).HasOutstandingConstructionDemand(context.Background(), 5)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !has {
				t.Fatal("released the capital reservation on a state it could not prove satisfied")
			}
		})
	}

	t.Run("an unwired repository never releases the reservation", func(t *testing.T) {
		has, err := NewConstructionDemandReader(nil).HasOutstandingConstructionDemand(context.Background(), 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !has {
			t.Fatal("an unwired reader released the reservation — a wiring slip would hand trade the whole treasury")
		}
	})

	t.Run("a read failure is surfaced, never answered idle", func(t *testing.T) {
		repo := &demandStubPipelineRepo{err: errors.New("db down")}
		has, err := NewConstructionDemandReader(repo).HasOutstandingConstructionDemand(context.Background(), 5)
		if err == nil {
			t.Fatal("swallowed a read error — the caller can no longer fail conservative")
		}
		if has {
			t.Fatal("reported demand AND an error; the caller must decide, so the bool must be false")
		}
	})
}
