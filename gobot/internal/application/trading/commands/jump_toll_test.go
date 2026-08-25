package commands

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/trading"
)

type jumpTollFakeClock struct{ now time.Time }

func (c *jumpTollFakeClock) Now() time.Time      { return c.now }
func (c *jumpTollFakeClock) Sleep(time.Duration) {}

// jumpTollFakeRepo counts reads so a test can prove a value was SERVED FROM CACHE rather
// than merely returned, and records what window was asked for.
type jumpTollFakeRepo struct {
	samples []trading.JumpTollSample
	err     error
	calls   int
	since   time.Time
	limit   int
	written []trading.JumpTollSample
}

func (r *jumpTollFakeRepo) RecordJumpToll(
	_ context.Context, _ int, _, _, _ string, sample trading.JumpTollSample,
) error {
	r.written = append(r.written, sample)
	return r.err
}

func (r *jumpTollFakeRepo) RecentJumpTolls(
	_ context.Context, _ int, since time.Time, limit int,
) ([]trading.JumpTollSample, error) {
	r.calls++
	r.since = since
	r.limit = limit
	if r.err != nil {
		return nil, r.err
	}
	return r.samples, nil
}

func tollSamples(now time.Time, waitSeconds, n int) []trading.JumpTollSample {
	out := make([]trading.JumpTollSample, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, trading.JumpTollSample{
			WaitSeconds:     waitSeconds,
			CooldownSeconds: waitSeconds * 4 / 5,
			RecordedAt:      now.Add(-time.Duration(i) * time.Minute),
		})
	}
	return out
}

func TestJumpTollReaderServesTheMeasuredTollOnceItHasSamples(t *testing.T) {
	clock := &jumpTollFakeClock{now: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)}
	params := trading.DefaultJumpTollParams()
	repo := &jumpTollFakeRepo{samples: tollSamples(clock.now, 1180, params.MinSamples)}

	require.Equal(t, 1180, NewLedgerJumpTollReader(repo, clock, params).PerHopTollSeconds(context.Background(), 5))
}

func TestJumpTollReaderWithholdsTheOverrideBelowTheSampleFloor(t *testing.T) {
	// THE FAIL-OPEN PIN. 0 is what the caller drops from the request, so the solver never
	// sees a request value and prices the tour on its fitted default exactly as today.
	clock := &jumpTollFakeClock{now: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)}
	params := trading.DefaultJumpTollParams()
	repo := &jumpTollFakeRepo{samples: tollSamples(clock.now, 1180, params.MinSamples-1)}

	require.Zero(t, NewLedgerJumpTollReader(repo, clock, params).PerHopTollSeconds(context.Background(), 5))
}

func TestJumpTollReaderReadsOnlyTheEstimatorsWindow(t *testing.T) {
	// The cost bound: without it this read walks the whole history of jumps.
	clock := &jumpTollFakeClock{now: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)}
	params := trading.DefaultJumpTollParams()
	repo := &jumpTollFakeRepo{}

	NewLedgerJumpTollReader(repo, clock, params).PerHopTollSeconds(context.Background(), 5)
	require.Equal(t, clock.now.Add(-params.Window), repo.since)
	require.Positive(t, repo.limit, "an unbounded read is a load problem waiting to happen")
}

func TestJumpTollReaderServesTheCachedTollWithinItsTTL(t *testing.T) {
	clock := &jumpTollFakeClock{now: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)}
	params := trading.DefaultJumpTollParams()
	repo := &jumpTollFakeRepo{samples: tollSamples(clock.now, 1180, params.MinSamples)}
	r := NewLedgerJumpTollReader(repo, clock, params)

	require.Equal(t, 1180, r.PerHopTollSeconds(context.Background(), 5))
	require.Equal(t, 1, repo.calls)

	clock.now = clock.now.Add(jumpTollCacheTTL - time.Minute)
	require.Equal(t, 1180, r.PerHopTollSeconds(context.Background(), 5))
	require.Equal(t, 1, repo.calls, "still inside the TTL — must not re-read")

	clock.now = clock.now.Add(2 * time.Minute)
	require.Equal(t, 1180, r.PerHopTollSeconds(context.Background(), 5))
	require.Equal(t, 2, repo.calls, "TTL expired — must re-read")
}

func TestJumpTollReaderTracksAChangingRegimeAcrossReReads(t *testing.T) {
	// The whole point of the bead: the toll must MOVE when conditions do. Same reader,
	// re-read past the TTL against a fleet that has since sped up.
	clock := &jumpTollFakeClock{now: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)}
	params := trading.DefaultJumpTollParams()
	repo := &jumpTollFakeRepo{samples: tollSamples(clock.now, 1500, params.MinSamples)}
	r := NewLedgerJumpTollReader(repo, clock, params)
	require.Equal(t, 1500, r.PerHopTollSeconds(context.Background(), 5))

	clock.now = clock.now.Add(jumpTollCacheTTL + time.Minute)
	repo.samples = tollSamples(clock.now, 820, params.MinSamples)
	require.Equal(t, 820, r.PerHopTollSeconds(context.Background(), 5))
}

func TestJumpTollReaderRecoversTheTollAfterARestart(t *testing.T) {
	// RULINGS #2 as it applies here: the estimate is never persisted, so a "restart" is just
	// a brand-new reader over the SAME durable rows. It must produce the same toll on its
	// first solve, with no reload path and no warm-up.
	clock := &jumpTollFakeClock{now: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)}
	params := trading.DefaultJumpTollParams()
	repo := &jumpTollFakeRepo{samples: tollSamples(clock.now, 1180, params.MinSamples)}

	before := NewLedgerJumpTollReader(repo, clock, params).PerHopTollSeconds(context.Background(), 5)
	afterRestart := NewLedgerJumpTollReader(repo, clock, params).PerHopTollSeconds(context.Background(), 5)
	require.Equal(t, before, afterRestart)
	require.Equal(t, 1180, afterRestart)
}

func TestJumpTollReaderIsNilSafeAndFailsOpen(t *testing.T) {
	clock := &jumpTollFakeClock{now: time.Now()}
	params := trading.DefaultJumpTollParams()

	var nilReader *LedgerJumpTollReader
	require.Zero(t, nilReader.PerHopTollSeconds(context.Background(), 5))
	require.Zero(t, NewLedgerJumpTollReader(nil, clock, params).PerHopTollSeconds(context.Background(), 5))

	failing := &jumpTollFakeRepo{err: errors.New("boom")}
	require.Zero(t, NewLedgerJumpTollReader(failing, clock, params).PerHopTollSeconds(context.Background(), 5),
		"an unreadable sample table must leave the solver on its fitted default")

	// A degenerate params value must not be filled in from somewhere else.
	repo := &jumpTollFakeRepo{samples: tollSamples(clock.now, 1180, 200)}
	require.Zero(t, NewLedgerJumpTollReader(repo, clock, trading.JumpTollParams{}).PerHopTollSeconds(context.Background(), 5))
}

func TestJumpTollReaderCachesPerPlayer(t *testing.T) {
	clock := &jumpTollFakeClock{now: time.Now()}
	params := trading.DefaultJumpTollParams()
	repo := &jumpTollFakeRepo{samples: tollSamples(clock.now, 1180, params.MinSamples)}
	r := NewLedgerJumpTollReader(repo, clock, params)

	r.PerHopTollSeconds(context.Background(), 5)
	require.Equal(t, 1, repo.calls)
	r.PerHopTollSeconds(context.Background(), 6)
	require.Equal(t, 2, repo.calls, "a different player is a different fleet")
}
