package hullrepair

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// fakeProbe scripts the composite reads in order and one parts reading.
type fakeProbe struct {
	composite  []Verdict
	compositeN int
	parts      Subresources
	partsErr   error
	partsCalls int
}

func (p *fakeProbe) ReadComposite(context.Context, string) (Verdict, error) {
	v := ReadRefusedServer
	if p.compositeN < len(p.composite) {
		v = p.composite[p.compositeN]
	}
	p.compositeN++
	if v == ReadOK {
		return ReadOK, nil
	}
	return v, errors.New("API error (status 500)")
}

func (p *fakeProbe) ProbeSubresources(context.Context, string) (Subresources, error) {
	p.partsCalls++
	return p.parts, p.partsErr
}

// fakeWriter records every game action the repair took.
type fakeWriter struct {
	docked    int
	orbited   int
	refuelled int
	dockErr   error
	refuelErr error
	receipt   RefuelReceipt
}

func (w *fakeWriter) Dock(context.Context, string) error  { w.docked++; return w.dockErr }
func (w *fakeWriter) Orbit(context.Context, string) error { w.orbited++; return nil }
func (w *fakeWriter) Refuel(context.Context, string) (RefuelReceipt, error) {
	w.refuelled++
	if w.refuelErr != nil {
		return RefuelReceipt{}, w.refuelErr
	}
	return w.receipt, nil
}

func (w *fakeWriter) wrote() int { return w.docked + w.orbited + w.refuelled }

type fakeMarket struct {
	price int
	sells bool
	err   error
}

func (m fakeMarket) FuelAsk(context.Context, int, string) (int, bool, error) {
	return m.price, m.sells, m.err
}

type fakeTreasury struct {
	credits int64
	err     error
}

func (t fakeTreasury) Credits(context.Context, int) (int64, error) { return t.credits, t.err }

type fakeTanks struct {
	capacity int
	err      error
}

func (t fakeTanks) FuelCapacity(context.Context, int, string) (int, error) {
	return t.capacity, t.err
}

type fakeRefresher struct {
	calls int
	err   error
}

func (r *fakeRefresher) Refresh(context.Context, int, string) error { r.calls++; return r.err }

// orbitingHull is the parts reading of a hull standing in orbit somewhere.
func orbitingHull() Subresources {
	return Subresources{
		Nav:      &NavReading{WaypointSymbol: "X1-AA-A1", Status: NavInOrbit},
		Answered: []string{"nav"},
	}
}

type harness struct {
	probe     *fakeProbe
	writer    *fakeWriter
	market    fakeMarket
	treasury  fakeTreasury
	tanks     fakeTanks
	refresher *fakeRefresher
}

func newHarness() *harness {
	return &harness{
		probe:     &fakeProbe{parts: orbitingHull()},
		writer:    &fakeWriter{receipt: RefuelReceipt{FuelCurrent: 600, FuelCapacity: 600, CreditsCost: 700}},
		market:    fakeMarket{price: 2, sells: true},
		treasury:  fakeTreasury{credits: 5_000_000},
		tanks:     fakeTanks{capacity: 600},
		refresher: &fakeRefresher{},
	}
}

func (h *harness) repairer() *Repairer {
	return NewRepairer(h.probe, h.writer, h.market, h.treasury, h.tanks, h.refresher, nil)
}

func (h *harness) repair() Result {
	return h.repairer().Repair(context.Background(), 10, "SHIP-1")
}

// The confirmed signature: the composite refuses while a part answers, so the fault is
// local to this record and the fuel write is the repair.
func TestRepairWritesFuelOnlyAfterTheSignatureIsConfirmed(t *testing.T) {
	h := newHarness()
	h.probe.composite = []Verdict{ReadRefusedServer, ReadOK}

	res := h.repair()

	require.Equal(t, OutcomeRepaired, res.Outcome, res.Reason)
	require.Equal(t, 1, h.probe.partsCalls, "the parts must be read before anything is written")
	require.Equal(t, 1, h.writer.refuelled)
	require.Equal(t, 2, h.probe.compositeN, "the repair must be verified by re-reading the composite record")
	require.Equal(t, 1, h.refresher.calls, "a repaired hull's row must be re-read so coordinators stop using the stale one")
}

// A single failed read is not evidence. Re-reading the composite is the first thing the
// repair does, and a hull that now serves is left alone.
func TestTransientCompositeFailureWritesNothing(t *testing.T) {
	h := newHarness()
	h.probe.composite = []Verdict{ReadOK}

	res := h.repair()

	require.Equal(t, OutcomeAlreadyHealthy, res.Outcome, res.Reason)
	require.Zero(t, h.writer.wrote(), "a hull that reads must never be written to")
	require.Zero(t, h.probe.partsCalls, "and its parts must not even be probed")
}

// The outage case: every part refuses too, so nothing has been established about this hull
// and firing repairs across the fleet would turn an outage into fleet-wide spend.
func TestApiWideOutageTriggersNoRepair(t *testing.T) {
	h := newHarness()
	h.probe.composite = []Verdict{ReadRefusedServer}
	h.probe.parts = Subresources{Refused: []string{"nav", "cargo", "cooldown", "mounts", "modules"}}

	res := h.repair()

	require.Equal(t, OutcomeAPIUnavailable, res.Outcome, res.Reason)
	require.Zero(t, h.writer.wrote(), "an API-wide outage must never trigger a write")
}

// A refusal that is not a server error says nothing about the record being corrupt.
func TestNonServerRefusalIsNotRepaired(t *testing.T) {
	h := newHarness()
	h.probe.composite = []Verdict{ReadRefusedClient}

	res := h.repair()

	require.Equal(t, OutcomeGone, res.Outcome, res.Reason)
	require.Zero(t, h.writer.wrote())
	require.Zero(t, h.probe.partsCalls)
}

// The signature can hold while /nav is not the part that answered; there is then no live
// position, and the stored one is exactly what cannot be trusted.
func TestSignatureWithoutNavDoesNotWrite(t *testing.T) {
	h := newHarness()
	h.probe.composite = []Verdict{ReadRefusedServer}
	h.probe.parts = Subresources{Answered: []string{"cargo"}, Refused: []string{"nav"}}

	res := h.repair()

	require.Equal(t, OutcomeNavUnreadable, res.Outcome, res.Reason)
	require.Zero(t, h.writer.wrote())
}

func TestInTransitHullIsLeftAlone(t *testing.T) {
	h := newHarness()
	h.probe.composite = []Verdict{ReadRefusedServer}
	h.probe.parts = Subresources{
		Nav:      &NavReading{WaypointSymbol: "X1-AA-A1", Status: NavInTransit, ArrivalAt: time.Now().Add(time.Hour)},
		Answered: []string{"nav"},
	}

	res := h.repair()

	require.Equal(t, OutcomeInTransit, res.Outcome, res.Reason)
	require.Zero(t, h.writer.docked, "a hull mid-leg must not be docked")
}

func TestOrbitingHullIsReturnedToOrbit(t *testing.T) {
	h := newHarness()
	h.probe.composite = []Verdict{ReadRefusedServer, ReadOK}

	require.Equal(t, OutcomeRepaired, h.repair().Outcome)
	require.Equal(t, 1, h.writer.docked)
	require.Equal(t, 1, h.writer.orbited, "the hull must be left in the nav state it was found in")
}

func TestDockedHullIsNeitherDockedNorOrbited(t *testing.T) {
	h := newHarness()
	h.probe.composite = []Verdict{ReadRefusedServer, ReadOK}
	h.probe.parts = Subresources{
		Nav:      &NavReading{WaypointSymbol: "X1-AA-A1", Status: NavDocked},
		Answered: []string{"nav"},
	}

	require.Equal(t, OutcomeRepaired, h.repair().Outcome)
	require.Zero(t, h.writer.docked)
	require.Zero(t, h.writer.orbited, "a hull found docked must be left docked")
}

// A fill that lands and leaves the composite refusing proves the corrupt field is not
// fuel, so repeating the spend cannot help.
func TestFuelWriteThatDoesNotHelpIsTerminal(t *testing.T) {
	h := newHarness()
	h.probe.composite = []Verdict{ReadRefusedServer, ReadRefusedServer}

	res := h.repair()

	require.Equal(t, OutcomeNotFuel, res.Outcome, res.Reason)
	require.True(t, res.Outcome.Terminal(), "a proven-wrong repair must not be retried")
	require.True(t, res.Outcome.SpentAttempt())
	require.Equal(t, 1, h.writer.refuelled)
	require.Equal(t, 1, h.writer.orbited, "the hull is still returned to orbit when the repair fails")
}

func TestNoFuelMarketBlocksTheRepairWithoutFlyingTheHull(t *testing.T) {
	h := newHarness()
	h.probe.composite = []Verdict{ReadRefusedServer}
	h.market = fakeMarket{sells: false}

	res := h.repair()

	require.Equal(t, OutcomeNoFuelMarket, res.Outcome, res.Reason)
	require.Zero(t, h.writer.wrote())
	require.False(t, res.Outcome.SpentAttempt(), "a repair that could not run must not spend the attempt budget")
}

// RULINGS #4: the guard fails closed on every unreadable input.
func TestMoneyGuardFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*harness)
		outcome Outcome
	}{
		{"unreadable treasury", func(h *harness) { h.treasury = fakeTreasury{err: errors.New("ledger stale, live read failed")} }, OutcomeUnaffordable},
		{"unreadable fuel market", func(h *harness) { h.market = fakeMarket{err: errors.New("db down")} }, OutcomeUnpriceable},
		{"unpriced fuel", func(h *harness) { h.market = fakeMarket{sells: true, price: 0} }, OutcomeUnpriceable},
		{"unreadable tank size", func(h *harness) { h.tanks = fakeTanks{err: errors.New("no row")} }, OutcomeUnpriceable},
		{"zero tank size", func(h *harness) { h.tanks = fakeTanks{capacity: 0} }, OutcomeUnpriceable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness()
			h.probe.composite = []Verdict{ReadRefusedServer}
			tc.mutate(h)

			res := h.repair()

			require.Equal(t, tc.outcome, res.Outcome, res.Reason)
			require.Zero(t, h.writer.wrote(), "a guard that cannot read must not spend")
		})
	}
}

// The floor is held against the WORST case fill, since the deficit is unknowable: fuel is
// the field that will not read.
func TestMoneyGuardHoldsTheReserveFloorAgainstAFullTank(t *testing.T) {
	worstCase := int64(600 * 2)

	blocked := newHarness()
	blocked.probe.composite = []Verdict{ReadRefusedServer}
	blocked.treasury = fakeTreasury{credits: int64(RepairFloor) + worstCase - 1}
	res := blocked.repair()
	require.Equal(t, OutcomeUnaffordable, res.Outcome, res.Reason)
	require.Zero(t, blocked.writer.wrote())

	allowed := newHarness()
	allowed.probe.composite = []Verdict{ReadRefusedServer, ReadOK}
	allowed.treasury = fakeTreasury{credits: int64(RepairFloor) + worstCase}
	require.Equal(t, OutcomeRepaired, allowed.repair().Outcome)
}

func TestRefuelRefusalSpendsAnAttemptAndRestoresOrbit(t *testing.T) {
	h := newHarness()
	h.probe.composite = []Verdict{ReadRefusedServer}
	h.writer.refuelErr = errors.New("API error (status 400)")

	res := h.repair()

	require.Equal(t, OutcomeWriteFailed, res.Outcome, res.Reason)
	require.True(t, res.Outcome.SpentAttempt())
	require.Equal(t, 1, h.writer.orbited)
}

func TestDockRefusalSpendsAnAttempt(t *testing.T) {
	h := newHarness()
	h.probe.composite = []Verdict{ReadRefusedServer}
	h.writer.dockErr = errors.New("API error (status 409)")

	res := h.repair()

	require.Equal(t, OutcomeWriteFailed, res.Outcome, res.Reason)
	require.Zero(t, h.writer.refuelled, "a hull that would not dock is never charged for fuel")
}

// Rate limiting and transport failures establish nothing, so they must not be read as the
// hull being gone — that would close the episode on a hull still in trouble.
func TestAnUnestablishedReadNeitherRepairsNorClosesTheEpisode(t *testing.T) {
	h := newHarness()
	h.probe.composite = []Verdict{ReadUnavailable}

	res := h.repair()

	require.Equal(t, OutcomeAPIUnavailable, res.Outcome, res.Reason)
	require.False(t, res.Outcome.Resolved(), "an episode must survive a read that concluded nothing")
	require.Zero(t, h.writer.wrote())
}

// The verification read is the same: a fill that landed must not be reported as repaired
// on a read that never came back.
func TestAnUnverifiableWriteIsNotReportedAsRepaired(t *testing.T) {
	h := newHarness()
	h.probe.composite = []Verdict{ReadRefusedServer, ReadUnavailable}

	res := h.repair()

	require.Equal(t, OutcomeWriteFailed, res.Outcome, res.Reason)
	require.Equal(t, 1, h.writer.refuelled)
}
