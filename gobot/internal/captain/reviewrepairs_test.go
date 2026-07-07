package watchkeeper

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
)

// deadlineCapturingAgentAPI records whether the context handed to GetAgent
// carried a deadline, so a test can prove refreshCredits bounds the live probe.
type deadlineCapturingAgentAPI struct {
	hadDeadline bool
	deadline    time.Time
}

func (f *deadlineCapturingAgentAPI) GetAgent(ctx context.Context, _ string) (*player.AgentData, error) {
	if dl, ok := ctx.Deadline(); ok {
		f.hadDeadline = true
		f.deadline = dl
	}
	return nil, errors.New("probe: forced error")
}

// Finding 1 (must-fix): the per-tick live-credits probe must carry a call
// deadline. Without one, GetAgent goes through the shared client's up-to-10
// retries with 30s per-request timeouts and ctx-ignoring backoff sleeps, so a
// single call during an API outage can hang a whole tick for ~10 minutes and
// defer interrupt delivery far past the one-tick target.
func TestRefreshCreditsBoundsLiveProbeWithDeadline(t *testing.T) {
	sup, s, _ := newBridgeSupervisor(t)
	seedBalance(t, sup, s.playerID, 700000)
	probe := &deadlineCapturingAgentAPI{}
	sup.SetAgentAPI(probe, "tok")

	start := time.Now()
	_ = captureOutput(t, func() { sup.refreshCredits(context.Background()) })

	require.True(t, probe.hadDeadline, "the live-credits probe must run under a bounded context")
	require.Positive(t, probe.deadline.Sub(start), "the probe deadline must be in the future")
	require.LessOrEqual(t, probe.deadline.Sub(start), creditsProbeTimeout+time.Second,
		"the probe deadline must be no further out than creditsProbeTimeout")
	require.Equal(t, 700000, sup.lastCredits,
		"a failed probe still falls back to the reconstruction when live was never observed")
}

// Finding 2 (should-fix): once a live value has been observed, a transient API
// error must RETAIN it, not flip the gate back to the divergent ledger
// reconstruction (the exact source whose mismatch with live motivated D3).
func TestRefreshCreditsRetainsLiveValueOnTransientErrorAfterFirstSuccess(t *testing.T) {
	sup, s, _ := newBridgeSupervisor(t)
	// A DIVERGENT reconstruction: if the fix regressed, the gate would flip to
	// this 700,000 on the blip instead of retaining the live 1,050,000.
	seedBalance(t, sup, s.playerID, 700000)

	api := &fakeAgentAPI{credits: 1050000}
	sup.SetAgentAPI(api, "tok")

	// First refresh: live succeeds and is recorded as observed.
	sup.refreshCredits(context.Background())
	require.Equal(t, 1050000, sup.lastCredits)
	require.True(t, sup.liveCreditsObserved)

	// Second refresh: a transient API error must retain the last LIVE value,
	// never the 700,000 reconstruction.
	api.err = errors.New("api 503")
	out := captureOutput(t, func() { sup.refreshCredits(context.Background()) })

	require.Equal(t, 1050000, sup.lastCredits,
		"a transient blip must not resurface the divergent reconstruction once live is trusted")
	require.Contains(t, out, "retaining last live value", "the retain path must be grep-able")
}

// Finding 3 (should-fix): an interrupt-class event that has never been
// successfully mailed must keep retrying promptly (capped at 2x poll) even
// after its first bypassed attempt failed — it must NOT inherit the full 15m
// backoff that failed heartbeat nudges accumulate on the shared counter.
func TestUndeliveredInterruptCapsDeliveryBackoff(t *testing.T) {
	sup, s, _ := newBridgeSupervisor(t)
	policy := WakePolicy{} // default interrupt classification
	t0 := time.Now()

	// A deep, heartbeat-driven backoff: at 6 consecutive failures the full
	// backoff is the 15m ceiling — far beyond the 2x-poll (60s) cap.
	sup.deliveryFailures = 6
	sup.lastDeliveryAttempt = t0
	require.Greater(t, backoffDelay(sup.pollInterval(), 6), 2*sup.pollInterval())

	// An interrupt present at the last attempt (so not "new") and never mailed
	// (no renudges entry).
	ev := &captain.Event{ID: 42, Type: captain.EventWorkflowFailed, PlayerID: s.playerID}
	events := []*captain.Event{ev}
	sup.lastAttemptInterrupts = map[int64]bool{42: true}

	require.True(t, sup.deliveryThrottled(t0.Add(59*time.Second), events, policy),
		"just before the 2x-poll slot the undelivered interrupt is still throttled")
	require.False(t, sup.deliveryThrottled(t0.Add(60*time.Second), events, policy),
		"at 2x poll the still-undelivered interrupt must retry, not wait out the 15m backoff")

	// Contrast: an already-MAILED interrupt (renudges entry present) is a
	// reminder ping — the captain already has it — so it keeps the full backoff.
	sup.renudges = map[int64]int{42: 0}
	require.True(t, sup.deliveryThrottled(t0.Add(60*time.Second), events, policy),
		"a delivered interrupt awaiting ack keeps the full heartbeat backoff")
}

// Finding 4 (should-fix): a CreditsAbove/Below bound newly satisfied since the
// last delivery attempt is a true edge and must bypass the delivery backoff; a
// standing (level-triggered) bound must NOT, or it would defeat the backoff on
// every tick against a dead channel.
func TestNewlyCrossedCreditsBoundBypassesDeliveryBackoffButStandingDoesNot(t *testing.T) {
	sup, _, _ := newBridgeSupervisor(t)
	t0 := time.Now()
	sup.deliveryFailures = 6
	sup.lastDeliveryAttempt = t0
	above := 1000000
	policy := WakePolicy{CreditsAbove: &above}
	var events []*captain.Event // the crossing is a gate condition, not an event

	// At the last attempt credits were below the bound.
	sup.lastAttemptCreditsAbove = false

	sup.lastCredits = 900000
	require.True(t, sup.deliveryThrottled(t0.Add(time.Second), events, policy),
		"still below the bound: no crossing, stays throttled")

	sup.lastCredits = 1100000
	require.False(t, sup.deliveryThrottled(t0.Add(time.Second), events, policy),
		"a newly-satisfied CreditsAbove bound is an edge and bypasses the backoff")

	// Standing bound (already satisfied at the last attempt): must stay throttled.
	sup.lastAttemptCreditsAbove = true
	require.True(t, sup.deliveryThrottled(t0.Add(time.Second), events, policy),
		"a level-triggered standing bound must not defeat the backoff every tick")
}
