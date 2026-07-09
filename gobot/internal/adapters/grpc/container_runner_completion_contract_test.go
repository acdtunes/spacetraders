package grpc

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
	"github.com/andrescamacho/spacetraders-go/internal/domain/container"
)

// The honest-completion contract (sp-7yej invariant 2, first enforced for
// trade-route by sp-1hj5): a coordinator that ends its run deliberately (nil Go
// error, so the restart loop can't crashloop it) but reports its task
// incomplete — cargo bought this run still aboard — must NOT reach the
// clean-exit success=true. The live incident: trade-route-TORWIND-19 completed
// success=true and released its hull DOCKED holding 18 LAB_INSTRUMENTS.

// reporterResponse is a coordinator response implementing
// common.CompletionReporter with a fixed outcome.
type reporterResponse struct {
	ok     bool
	reason string
}

func (r *reporterResponse) CompletionOutcome() (bool, string) { return r.ok, r.reason }

// reporterMediator returns the configured response with a nil error — the
// deliberate clean-exit shape the veto exists for.
type reporterMediator struct{ resp common.Response }

func (m *reporterMediator) Send(_ context.Context, _ common.Request) (common.Response, error) {
	return m.resp, nil
}
func (m *reporterMediator) Register(_ reflect.Type, _ common.RequestHandler) error { return nil }
func (m *reporterMediator) RegisterMiddleware(_ common.Middleware)                 {}

// newCompletionContractRunner builds a started single-iteration runner (the
// trade_route shape: the run IS one coordinator invocation) around a mediator
// that returns resp with a nil error.
func newCompletionContractRunner(t *testing.T, resp common.Response) *ContainerRunner {
	t.Helper()
	entity := container.NewContainer(
		"trade-route-TORWIND-19-afa492c5",
		container.ContainerTypeTrading,
		2,
		1, // single circuit run, exactly like DaemonServer.StartTradeRoute
		nil,
		nil, // no ship_symbol metadata: ship claim/release is out of scope here
		nil,
	)
	require.NoError(t, entity.Start())
	return NewContainerRunner(entity, &reporterMediator{resp: resp}, nil, noopLogRepo{}, nil, nil, nil)
}

// runIterationsAndFinish drives the runner the way execute() does — iterate,
// then terminalize via the clean-exit choke point — without execute()'s
// real-time startup jitter.
func runIterationsAndFinish(t *testing.T, r *ContainerRunner) {
	t.Helper()
	for r.containerEntity.ShouldContinue() {
		require.NoError(t, r.executeIteration())
		require.NoError(t, r.containerEntity.IncrementIteration())
	}
	r.finishCleanExit()
}

// (a) A clean-exit iteration whose response vetoes success must terminalize the
// container FAILED and signal success=false carrying the veto reason — never
// the laden success=true of the incident. And it is NOT a crash: the run ended
// at a safe exit point; it just may not claim success.
func TestCleanExit_CompletionVetoed_TerminalizesFailed(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	r := newCompletionContractRunner(t, &reporterResponse{
		ok:     false,
		reason: "stranded cargo: 18 unsold units of LAB_INSTRUMENTS aboard TORWIND-19",
	})
	runIterationsAndFinish(t, r)

	require.Equal(t, container.ContainerStatusFailed, r.containerEntity.Status(),
		"a vetoed completion must terminalize FAILED, not COMPLETED")

	require.Equal(t, 1, countEvents(rec.events, captain.EventWorkflowFailed),
		"a vetoed completion must record workflow.failed")
	require.Zero(t, countEvents(rec.events, captain.EventWorkflowFinished),
		"a vetoed completion must NOT record the success-shaped workflow.finished")
	require.Zero(t, countEvents(rec.events, captain.EventContainerCrashed),
		"an honest-completion refusal is not a crash")

	ev := findEvent(rec.events, captain.EventWorkflowFailed)
	require.NotNil(t, ev)
	require.Contains(t, ev.Payload, "LAB_INSTRUMENTS",
		"the failure event must carry the veto reason as its signature")
}

// (b) Control: a response that implements the reporter and affirms completion
// flows through the unchanged completed path.
func TestCleanExit_CompletionAffirmed_CompletesNormally(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	r := newCompletionContractRunner(t, &reporterResponse{ok: true})
	runIterationsAndFinish(t, r)

	require.Equal(t, container.ContainerStatusCompleted, r.containerEntity.Status())
	require.Equal(t, 1, countEvents(rec.events, captain.EventWorkflowFinished))
	require.Zero(t, countEvents(rec.events, captain.EventWorkflowFailed))
}

// (c) Control: a response that does not implement the reporter keeps today's
// behavior byte-for-byte — the contract is opt-in per coordinator, so the
// other container types are untouched until they adopt it.
func TestCleanExit_NonReporterResponse_CompletesNormally(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	r := newCompletionContractRunner(t, struct{ common.Response }{})
	runIterationsAndFinish(t, r)

	require.Equal(t, container.ContainerStatusCompleted, r.containerEntity.Status())
	require.Equal(t, 1, countEvents(rec.events, captain.EventWorkflowFinished))
	require.Zero(t, countEvents(rec.events, captain.EventWorkflowFailed))
}

// (d) Last iteration governs: an earlier vetoed iteration followed by an
// affirming one completes — the veto is per-iteration state, not a latch. (A
// multi-iteration coordinator that recovers on a later pass must not be failed
// for its history.)
func TestCleanExit_VetoClearedByLaterIteration_Completes(t *testing.T) {
	rec := &fakeRecorder{}
	SetCaptainEventRecorder(rec)
	defer SetCaptainEventRecorder(nil)

	entity := container.NewContainer("trade-route-TORWIND-19-two", container.ContainerTypeTrading, 2, 2, nil, nil, nil)
	require.NoError(t, entity.Start())
	med := &sequencedReporterMediator{responses: []common.Response{
		&reporterResponse{ok: false, reason: "stranded cargo: transient"},
		&reporterResponse{ok: true},
	}}
	r := NewContainerRunner(entity, med, nil, noopLogRepo{}, nil, nil, nil)
	runIterationsAndFinish(t, r)

	require.Equal(t, container.ContainerStatusCompleted, r.containerEntity.Status(),
		"an affirming later iteration must clear an earlier veto")
	require.Equal(t, 1, countEvents(rec.events, captain.EventWorkflowFinished))
}

// sequencedReporterMediator returns each configured response in order.
type sequencedReporterMediator struct {
	responses []common.Response
	calls     int
}

func (m *sequencedReporterMediator) Send(_ context.Context, _ common.Request) (common.Response, error) {
	resp := m.responses[m.calls%len(m.responses)]
	m.calls++
	return resp, nil
}
func (m *sequencedReporterMediator) Register(_ reflect.Type, _ common.RequestHandler) error {
	return nil
}
func (m *sequencedReporterMediator) RegisterMiddleware(_ common.Middleware) {}
