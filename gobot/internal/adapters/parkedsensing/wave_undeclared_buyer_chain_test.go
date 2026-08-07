package parkedsensing

// THE WHOLE CHAIN, from one persisted container row to whether a credit moves.
//
// The switch read, the wave port and the drain's gate are each covered where they live, and every
// one of those tests passes with a leftover coordinator deriving HEAVY — because the defect is not
// in any of them, it is in what they COMPOSE into. A container carrying a cap knob answers
// "a heavy buyer exists" for the cap ladder; the switch key is absent on it, and absent means the
// documented ON; the wave then prices a target off that leftover's cap and derives HEAVY; and HEAVY
// stops probe buying outright. Nothing is buying heavies in that state, so nothing ever spends the
// treasury the pause is accumulating and the pause has no end — the deadlock the switch was added
// to the predicate to prevent, rebuilt one layer up out of parts that each behave as documented.
//
// So these two tests drive the REAL cap/switch read over a REAL container row, through the REAL
// reserve and wave ports, into DrainBuyQueue, and ask the only question that matters: did a probe
// get bought. They are a PAIR and neither is evidence alone — the fixture is identical down to the
// treasury, and the ONLY difference between them is whether the container's type is one the
// heavy-buyer declaration claims.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/application/logging"
	appSensing "github.com/andrescamacho/spacetraders-go/internal/application/parkedsensing"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// --- the drain's minimal buying world ----------------------------------------
//
// One WANTED market placement in a screened system, one probe yard there with a hull of ours
// already standing on it, and a treasury that clears the floor with the probe's price to spare. The
// point of the fixture is that it BUYS: an assertion that a paused wave bought nothing is worth
// nothing unless the same world unpaused buys.

type chainLedger struct {
	slots   []appSensing.QueuedSlot
	systems []appSensing.ScreenedSystem
}

func (l *chainLedger) SlotsByState(_ context.Context, _ int, states ...string) ([]appSensing.QueuedSlot, error) {
	want := map[string]bool{}
	for _, s := range states {
		want[s] = true
	}
	var out []appSensing.QueuedSlot
	for _, s := range l.slots {
		if want[s.State] {
			out = append(out, s)
		}
	}
	return out, nil
}

func (l *chainLedger) SlotsBySystem(_ context.Context, _ int, system string) ([]appSensing.QueuedSlot, error) {
	var out []appSensing.QueuedSlot
	for _, s := range l.slots {
		if s.System == system {
			out = append(out, s)
		}
	}
	return out, nil
}

func (l *chainLedger) SystemsByVerdict(_ context.Context, _ int, _ string) ([]appSensing.ScreenedSystem, error) {
	return l.systems, nil
}

func (l *chainLedger) CountOwnedProbes(context.Context, int) (int64, error) { return 0, nil }

// TransitionSlot ERRORS on a transition this fixture does not model, rather than accepting it
// silently: an unmodelled edge means the drain took a path these tests are not describing, and a
// permissive nil would let that pass as a green.
func (l *chainLedger) TransitionSlot(_ context.Context, _ int, tr appSensing.SlotTransition, set appSensing.SlotFields) error {
	for i := range l.slots {
		if l.slots[i].Waypoint != tr.Waypoint || l.slots[i].Kind != tr.Kind {
			continue
		}
		if l.slots[i].State != tr.From {
			return fmt.Errorf("slot %s is %s, not %s", tr.Waypoint, l.slots[i].State, tr.From)
		}
		l.slots[i].State = tr.To
		if set.AssignedShip != nil {
			l.slots[i].AssignedShip = *set.AssignedShip
		}
		if set.PurchaseYard != nil {
			l.slots[i].PurchaseYard = *set.PurchaseYard
		}
		return nil
	}
	return fmt.Errorf("no slot %s/%s", tr.Waypoint, tr.Kind)
}

type chainTreasury struct{ credits int64 }

func (c *chainTreasury) LiveCredits(context.Context, int) (int64, error) { return c.credits, nil }

type chainCargoSpend struct{ spend int64 }

func (c *chainCargoSpend) AbsCargoBuySpendSince(context.Context, int, time.Time) (int64, error) {
	return c.spend, nil
}

type chainPurchaser struct {
	price  int64
	quotes []string
	buys   []string
}

func (p *chainPurchaser) Quote(_ context.Context, _ int, yard string) (int64, error) {
	p.quotes = append(p.quotes, yard)
	return p.price, nil
}

func (p *chainPurchaser) Buy(_ context.Context, _ int, _, yard, _ string) (appSensing.BoughtProbe, error) {
	p.buys = append(p.buys, yard)
	return appSensing.BoughtProbe{ShipSymbol: "PROBE-NEW", Price: p.price}, nil
}

type chainYards struct{ yards map[string][]string }

func (y *chainYards) ListProbeYards(_ context.Context, system string) ([]string, error) {
	return y.yards[system], nil
}

type chainShips struct{ docked map[string]string }

func (s *chainShips) DockedProbeAt(_ context.Context, _ int, waypoint string) (string, bool, error) {
	hull, ok := s.docked[waypoint]
	return hull, ok, nil
}

func (s *chainShips) DockedBuyerAt(_ context.Context, _ int, waypoint string) (string, bool, error) {
	hull, ok := s.docked[waypoint]
	return hull, ok, nil
}

func (s *chainShips) LendableHulls(context.Context, int, int) ([]appSensing.LendableHull, error) {
	return nil, nil
}

func (s *chainShips) ShipAt(context.Context, int, string) (appSensing.ShipPos, error) {
	return appSensing.ShipPos{}, nil
}

type chainFleet struct{}

func (chainFleet) AssignFleet(context.Context, int, string, string) error { return nil }

// chainKnobs are the documented default floors with the operator's switch ON, so the ONLY gate that
// can stop this drain buying is the wave. Folding the switch off here would make both tests below
// pass against a fixture that was never allowed to spend.
var chainKnobs = appSensing.BuyKnobs{SpendEnabled: true, ProbeCap: 100, CapexReserve: 100_000, KMilli: 2000}

// waveOverContainerRow builds the drain's wave read the way the composition root does — the same
// container-config port serving BOTH the cap and the switch — over a heavy world that is otherwise
// unambiguously HEAVY: a priced heavy yard, no heavies owned, unserved lanes, and a demonstrated
// peak well past the ask's entry share. Every other clause of the predicate is satisfied, so the
// switch read is the only thing left that can decide the regime.
func waveOverContainerRow(db *gorm.DB) *WavePort {
	caps := NewHeavyBuyerCapPort(db)
	return NewWavePort(
		caps,
		NewHeavyReservePort(&fakeCensus{owned: 0}, &fakeYardPricer{price: 1_565_500, found: true}, caps),
		&fakeLanes{count: 7, readable: true},
		&fakePeak{peak: 4_000_000, readable: true},
		stoppedClock{time.Unix(1_700_000_000, 0)},
	)
}

func chainPorts(wave appSensing.WaveReader) (appSensing.BuyPorts, *chainPurchaser) {
	purchaser := &chainPurchaser{price: 23_540}
	return appSensing.BuyPorts{
		Treasury:   &chainTreasury{credits: 780_000},
		CargoSpend: &chainCargoSpend{spend: 300_000},
		Purchaser:  purchaser,
		Ledger: &chainLedger{
			slots: []appSensing.QueuedSlot{{
				Waypoint: "X1-AA-M1", System: "X1-AA", Kind: appSensing.SlotKindMarket,
				State: appSensing.SlotStateWanted, DepthCredits: 900,
			}},
			systems: []appSensing.ScreenedSystem{{System: "X1-AA", DepthCredits: 900}},
		},
		Yards: &chainYards{yards: map[string][]string{"X1-AA": {"X1-AA-Y1"}}},
		Ships: &chainShips{docked: map[string]string{"X1-AA-Y1": "PROBE-OLD"}},
		Fleet: &chainFleet{},
		Wave:  wave,
	}, purchaser
}

func drainOver(t *testing.T, db *gorm.DB, playerID int) (appSensing.BuyReport, *chainPurchaser, *warnLogger) {
	t.Helper()
	ports, purchaser := chainPorts(waveOverContainerRow(db))
	log := &warnLogger{}
	rep, err := appSensing.DrainBuyQueue(
		logging.WithLogger(context.Background(), log),
		ports, playerID, chainKnobs, stoppedClock{time.Unix(1_700_000_000, 0)},
	)
	require.NoError(t, err)
	return rep, purchaser, log
}

// --- the pair ----------------------------------------------------------------

// A LEFTOVER COORDINATOR MUST NOT PAUSE PROBE BUYING. Its row is RUNNING and carries the launch cap
// key its own coordinator was started with; no declaration claims its type, and it has no switch key
// at all — the shape a coordinator leaves behind when heavy buying moves elsewhere.
//
// Every input except the switch points at HEAVY here, which is what makes this the failing case: the
// target prices, the lanes are short and the peak clears the entry share, so a switch read that says
// "absent, therefore ON" pauses buying with nothing on the other side to ever end the pause.
func TestWaveChain_LeftoverStrangerDoesNotPauseProbeBuying(t *testing.T) {
	_, db, pid := capPortDB(t)
	addContainer(t, db, pid, "a1", string(container.ContainerTypeFleetAutosizer), string(container.ContainerStatusRunning), `{"autosizer_heavy_cap": 5}`)

	rep, purchaser, log := drainOver(t, db, pid)

	require.Equal(t, common.WaveProbe, rep.Wave,
		"a container no declaration claims derived HEAVY: probe buying is now paused for a buyer that does not exist, and no spender can reach the ask to clear it")
	require.Equal(t, common.WaveProbeReasonGrowthDisabled, rep.WaveProbeReason)
	require.False(t, rep.SpendingPaused,
		"the drain paused purchases on a leftover row — the pause has no end, because nothing is buying heavies")
	require.Equal(t, 1, rep.Bought, "probe buying must continue: it is the recoverable direction, and a deferred probe is a strategic cost while an endless pause is not")
	require.Len(t, purchaser.buys, 1)

	require.NotEmpty(t, log.lines,
		"the undeclared-buyer WARN must still fire on this tick — if the switch short-circuited the reserve read, a genuinely stale declaration would become the SILENT state")
}

// THE CALIBRATION, and the line the fix must not cross: the identical world with the SAME absent
// switch key on a DECLARED owner still derives HEAVY and still pauses. Without this the test above
// passes just as well against a wave wired permanently to PROBE, or a fixture too poor to buy.
func TestWaveChain_DeclaredOwnerStillPausesProbeBuying(t *testing.T) {
	_, db, pid := capPortDB(t)
	addHeavyBuyer(t, db, pid, "g1", string(container.ContainerStatusRunning), `{"growth_heavy_cap": 5}`)

	rep, purchaser, _ := drainOver(t, db, pid)

	require.Equal(t, common.WaveHeavy, rep.Wave,
		"the declared owner's untuned switch is ON, so this world must still reach HEAVY — otherwise the stranger test above proves nothing")
	require.True(t, rep.SpendingPaused)
	require.Zero(t, rep.Bought)
	require.Empty(t, purchaser.buys)
}
