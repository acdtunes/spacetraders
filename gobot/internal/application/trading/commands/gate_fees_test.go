package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
	"github.com/stretchr/testify/require"
)

// gateFeeFakeClock is a hand-wound clock so the TTL can be crossed deterministically.
type gateFeeFakeClock struct{ now time.Time }

func (c *gateFeeFakeClock) Now() time.Time      { return c.now }
func (c *gateFeeFakeClock) Sleep(time.Duration) {}

// gateFeeFakeRepo counts reads so a test can prove a table was SERVED FROM CACHE rather
// than merely returned — the two are indistinguishable from the value alone.
type gateFeeFakeRepo struct {
	fees  map[string]int64
	err   error
	calls int
	since time.Time
}

func (r *gateFeeFakeRepo) PerOriginGateFees(
	_ context.Context, _ shared.PlayerID, since time.Time,
) (map[string]int64, error) {
	r.calls++
	r.since = since
	if r.err != nil {
		return nil, r.err
	}
	return r.fees, nil
}

func TestGateFeeConstraintsDropsAnythingThatCannotBeAPrice(t *testing.T) {
	// The wire filter. A zero or negative fee is the one value that must never reach the
	// solver: it makes a crossing FREE and biases every candidate toward crossing, which is
	// the defect the fee term exists to close. An empty symbol addresses no gate at all.
	out := gateFeeConstraints(map[string]int64{
		"X1-KD64": 6667,
		"X1-KY38": 5312,
		"X1-ZERO": 0,
		"X1-NEG":  -4000,
		"":        9000,
	})
	require.Len(t, out, 2)
	// Sorted, so a request payload and its log are reproducible run to run.
	require.Equal(t, "X1-KD64", out[0].System)
	require.Equal(t, int64(6667), out[0].FeeCredits)
	require.Equal(t, "X1-KY38", out[1].System)
}

func TestGateFeeConstraintsIsNilWhenNothingSurvives(t *testing.T) {
	// nil, not an empty slice: nil is what serializes to nothing on the wire, and "no table"
	// must be byte-identical to a binary that predates the table.
	require.Nil(t, gateFeeConstraints(nil))
	require.Nil(t, gateFeeConstraints(map[string]int64{}))
	require.Nil(t, gateFeeConstraints(map[string]int64{"X1-A": 0, "X1-B": -1}))
}

func TestGateFeeReaderServesTheCachedTableWithinItsTTL(t *testing.T) {
	// The load argument for the whole cache: a tour solves many times a minute per hull, and
	// this aggregate groups a week of ledger rows. Proven by CALL COUNT, because the returned
	// value is identical either way.
	clock := &gateFeeFakeClock{now: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)}
	repo := &gateFeeFakeRepo{fees: map[string]int64{"X1-KD64": 6667}}
	r := NewLedgerGateFeeReader(repo, clock)

	require.Equal(t, map[string]int64{"X1-KD64": 6667}, r.GateFees(context.Background(), 5))
	require.Equal(t, 1, repo.calls)

	clock.now = clock.now.Add(gateFeeCacheTTL - time.Minute)
	require.Equal(t, map[string]int64{"X1-KD64": 6667}, r.GateFees(context.Background(), 5))
	require.Equal(t, 1, repo.calls, "still inside the TTL — must not re-read")

	clock.now = clock.now.Add(2 * time.Minute) // now past the TTL
	require.Equal(t, map[string]int64{"X1-KD64": 6667}, r.GateFees(context.Background(), 5))
	require.Equal(t, 2, repo.calls, "TTL expired — must re-read")
}

func TestGateFeeReaderScansOnlyTheLookbackWindow(t *testing.T) {
	// The cost bound. Without it this aggregate walks the whole ledger.
	clock := &gateFeeFakeClock{now: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)}
	repo := &gateFeeFakeRepo{fees: map[string]int64{"X1-A": 5000}}
	NewLedgerGateFeeReader(repo, clock).GateFees(context.Background(), 5)
	require.Equal(t, clock.now.Add(-gateFeeLookbackWindow), repo.since)
}

func TestGateFeeReaderServesTheLastGoodTableWhenTheLedgerReadFails(t *testing.T) {
	// A failed read proves nothing about gate prices, and a gate fee is a CONSTANT of the
	// map — so an expired copy is a better estimate than discarding it and pricing every
	// crossing at the flat charge again.
	clock := &gateFeeFakeClock{now: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)}
	repo := &gateFeeFakeRepo{fees: map[string]int64{"X1-KD64": 6667}}
	r := NewLedgerGateFeeReader(repo, clock)
	require.Equal(t, map[string]int64{"X1-KD64": 6667}, r.GateFees(context.Background(), 5))

	repo.err = errors.New("ledger unreadable")
	clock.now = clock.now.Add(gateFeeCacheTTL + time.Minute)
	require.Equal(t, map[string]int64{"X1-KD64": 6667}, r.GateFees(context.Background(), 5),
		"must fall back to the last good table, not to nothing")
}

func TestGateFeeReaderIsNilSafeAndFailsOpen(t *testing.T) {
	// Every failure direction yields nil, and nil means the solver prices exactly as it did
	// before this reader existed. This is a pricing refinement, not a money guard: there is
	// no spend to fail closed on, so the worst case must be that it does nothing.
	clock := &gateFeeFakeClock{now: time.Now()}

	var nilReader *LedgerGateFeeReader
	require.Nil(t, nilReader.GateFees(context.Background(), 5))

	require.Nil(t, NewLedgerGateFeeReader(nil, clock).GateFees(context.Background(), 5))

	failing := &gateFeeFakeRepo{err: errors.New("boom")}
	require.Nil(t, NewLedgerGateFeeReader(failing, clock).GateFees(context.Background(), 5),
		"a first read that fails with no cached table must yield no table")

	// An identity that will not parse cannot address a ledger.
	repo := &gateFeeFakeRepo{fees: map[string]int64{"X1-A": 5000}}
	require.Nil(t, NewLedgerGateFeeReader(repo, clock).GateFees(context.Background(), 0))
	require.Equal(t, 0, repo.calls, "must not query on an unusable player identity")
}

func TestGateFeeReaderCachesPerPlayer(t *testing.T) {
	// One cache map shared across players must not serve player A's gates to player B.
	clock := &gateFeeFakeClock{now: time.Now()}
	repo := &gateFeeFakeRepo{fees: map[string]int64{"X1-A": 5000}}
	r := NewLedgerGateFeeReader(repo, clock)

	r.GateFees(context.Background(), 5)
	require.Equal(t, 1, repo.calls)
	r.GateFees(context.Background(), 6)
	require.Equal(t, 2, repo.calls, "a different player is a different table")
}
