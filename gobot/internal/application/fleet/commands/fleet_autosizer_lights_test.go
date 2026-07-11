package commands

import (
	"context"
	"errors"
	"testing"
)

type fakeLightSources struct {
	workers    int
	workersErr error
	chains     int
	chainsErr  error
	vacancies  int
	vacErr     error
	marginal   float64
	fleetAvg   float64
	declining  bool
	rateOK     bool
	rateErr    error
}

func (f *fakeLightSources) WorkerCount(ctx context.Context, playerID int) (int, error) {
	return f.workers, f.workersErr
}
func (f *fakeLightSources) DesiredChains(ctx context.Context, playerID int) (int, error) {
	return f.chains, f.chainsErr
}
func (f *fakeLightSources) Vacancies(ctx context.Context, playerID int) (int, error) {
	return f.vacancies, f.vacErr
}
func (f *fakeLightSources) MarginalWorkerRate(ctx context.Context, playerID int) (float64, float64, bool, bool, error) {
	return f.marginal, f.fleetAvg, f.declining, f.rateOK, f.rateErr
}

// The core C3-inverted math: K chains need K × rotation workers, plus rebalancer vacancies.
func TestComputeLightDemand_RotationPlusVacancies(t *testing.T) {
	d := computeLightDemand(lightDemandInputs{
		CurrentWorkers: 15,
		DesiredChains:  5,
		Vacancies:      2,
		RotationSlots:  3.5,
		RateReadable:   true,
	})
	// ceil(5 × 3.5) = ceil(17.5) = 18, + 2 vacancies = 20.
	if d.Demand != 20 {
		t.Fatalf("demand = %d, want 20 (ceil(5×3.5)=18 + 2 vac)", d.Demand)
	}
	if d.Current != 15 {
		t.Fatalf("current = %d, want 15", d.Current)
	}
	if d.Shortfall() != 5 {
		t.Fatalf("shortfall = %d, want 5", d.Shortfall())
	}
	if !d.Readable {
		t.Fatalf("expected Readable=true")
	}
}

func TestComputeLightDemand_RoundsUp(t *testing.T) {
	d := computeLightDemand(lightDemandInputs{DesiredChains: 3, RotationSlots: 3.5})
	// ceil(3 × 3.5) = ceil(10.5) = 11.
	if d.Demand != 11 {
		t.Fatalf("demand = %d, want 11 (ceil(10.5))", d.Demand)
	}
}

func TestComputeLightDemand_VacanciesAloneDrive(t *testing.T) {
	d := computeLightDemand(lightDemandInputs{DesiredChains: 0, Vacancies: 3, RotationSlots: 3.5})
	if d.Demand != 3 {
		t.Fatalf("demand = %d, want 3 (no chains, 3 vacancies)", d.Demand)
	}
}

func TestComputeLightDemand_DefaultRotationWhenZero(t *testing.T) {
	d := computeLightDemand(lightDemandInputs{DesiredChains: 2, RotationSlots: 0})
	// 0 → defaultLightRotationSlots (3.5): ceil(2 × 3.5) = 7.
	if d.Demand != 7 {
		t.Fatalf("demand = %d, want 7 (default rotation 3.5)", d.Demand)
	}
}

func TestLightProvider_ReadsAndSizes(t *testing.T) {
	src := &fakeLightSources{workers: 10, chains: 4, vacancies: 1, marginal: 72000, fleetAvg: 90000, declining: false, rateOK: true}
	p := NewLightDemandProvider(src)
	if p.Class() != HullClassLight {
		t.Fatalf("class = %q, want light", p.Class())
	}
	d, err := p.Demand(context.Background(), 1, DemandParams{LightRotationSlots: 3.5})
	if err != nil {
		t.Fatalf("Demand error: %v", err)
	}
	// ceil(4 × 3.5) = 14, + 1 vacancy = 15.
	if d.Demand != 15 || d.Current != 10 {
		t.Fatalf("demand/current = %d/%d, want 15/10", d.Demand, d.Current)
	}
	if d.MarginalRate != 72000 || d.FleetAvgRate != 90000 || !d.RateReadable {
		t.Fatalf("rate signals not threaded: marginal=%v fleetAvg=%v readable=%v", d.MarginalRate, d.FleetAvgRate, d.RateReadable)
	}
	if !d.Readable {
		t.Fatalf("expected Readable=true")
	}
}

func TestLightProvider_WorkerReadError_FailsClosed(t *testing.T) {
	src := &fakeLightSources{workersErr: errors.New("db down"), chains: 5}
	p := NewLightDemandProvider(src)
	d, err := p.Demand(context.Background(), 1, DemandParams{LightRotationSlots: 3.5})
	if err != nil {
		t.Fatalf("a read miss must fail closed, not error the tick; got %v", err)
	}
	if d.Readable {
		t.Fatalf("unreadable worker count must yield Readable=false (fail-closed)")
	}
}

func TestLightProvider_ChainReadError_FailsClosed(t *testing.T) {
	src := &fakeLightSources{workers: 10, chainsErr: errors.New("chains unreadable")}
	p := NewLightDemandProvider(src)
	d, _ := p.Demand(context.Background(), 1, DemandParams{LightRotationSlots: 3.5})
	if d.Readable {
		t.Fatalf("unreadable desired-chain count must yield Readable=false (fail-closed)")
	}
}

// A vacancy read miss is a partial degradation, not a fail-closed: the chain-derived base demand
// still sizes (vacancies default to zero), because vacancies are an ADDITIVE signal.
func TestLightProvider_VacancyReadError_TreatedAsZero(t *testing.T) {
	src := &fakeLightSources{workers: 5, chains: 2, vacErr: errors.New("vacancy query failed"), rateOK: true}
	p := NewLightDemandProvider(src)
	d, _ := p.Demand(context.Background(), 1, DemandParams{LightRotationSlots: 3.5})
	if !d.Readable {
		t.Fatalf("a vacancy read miss must not fail-close the whole demand — base chain demand still sizes")
	}
	if d.Demand != 7 { // ceil(2 × 3.5) = 7 + 0 vacancies
		t.Fatalf("demand = %d, want 7 (vacancies treated as 0 on read miss)", d.Demand)
	}
}

// An unreadable realized rate does NOT block sizing — the demand is still Readable — but it is
// surfaced as RateReadable=false so the guard stack fails the realized-rate gate closed on its own.
func TestLightProvider_RateUnreadable_DemandStillReadable(t *testing.T) {
	src := &fakeLightSources{workers: 5, chains: 2, rateErr: errors.New("no pnl yet")}
	p := NewLightDemandProvider(src)
	d, _ := p.Demand(context.Background(), 1, DemandParams{LightRotationSlots: 3.5})
	if !d.Readable {
		t.Fatalf("an unreadable rate must not fail-close DEMAND sizing")
	}
	if d.RateReadable {
		t.Fatalf("an unreadable rate must set RateReadable=false so the guard fails the rate gate closed")
	}
}
