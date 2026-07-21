package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// These tests cover the `construction workers <site>` CLI surface (sp-duljg): the live mutation of a
// running construction pipeline's concurrent supplyTask-worker cap. By construction the fake below
// has ONLY the ConstructionWorkerCap RPC — no pipeline restart/stop method — so "no restart" is
// guaranteed by the surface this verb can reach, exactly as the `goods factory workers` tests
// guarantee it. The daemon is the sole writer of the persisted cap (RULINGS #3).

// constructionWorkerCapCall records one ConstructionWorkerCap invocation.
type constructionWorkerCapCall struct {
	site  string
	count int
}

// fakeConstructionWorkerCapMutator is an in-memory constructionWorkerCapMutator recording every call
// and serving a canned response. It has NO restart method by construction.
type fakeConstructionWorkerCapMutator struct {
	calls   []constructionWorkerCapCall
	resp    *pb.ConstructionWorkerCapResponse
	respErr error
}

func (f *fakeConstructionWorkerCapMutator) ConstructionWorkerCap(_ context.Context, site string, count int, _ *int32, _ *string) (*pb.ConstructionWorkerCapResponse, error) {
	f.calls = append(f.calls, constructionWorkerCapCall{site: site, count: count})
	if f.respErr != nil {
		return nil, f.respErr
	}
	return f.resp, nil
}

func TestRunConstructionWorkers_SetsCapLive(t *testing.T) {
	client := &fakeConstructionWorkerCapMutator{resp: &pb.ConstructionWorkerCapResponse{
		ConstructionSite: "X1-FB5-I56",
		WorkerCap:        10,
		Changed:          true,
	}}

	msg, err := runConstructionWorkers(context.Background(), client, "X1-FB5-I56", 10, nil, nil)
	require.NoError(t, err)

	require.Len(t, client.calls, 1)
	require.Equal(t, constructionWorkerCapCall{site: "X1-FB5-I56", count: 10}, client.calls[0])
	require.Contains(t, msg, "X1-FB5-I56")
	require.Contains(t, msg, "10")
	require.Contains(t, strings.ToLower(msg), "no restart")
}

func TestRunConstructionWorkers_AlreadyAtCount_ReportsNoOp(t *testing.T) {
	client := &fakeConstructionWorkerCapMutator{resp: &pb.ConstructionWorkerCapResponse{
		ConstructionSite: "X1-FB5-I56",
		WorkerCap:        5,
		Changed:          false, // already at count → daemon reports no change
	}}

	msg, err := runConstructionWorkers(context.Background(), client, "X1-FB5-I56", 5, nil, nil)
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(msg), "already")
}

func TestRunConstructionWorkers_DaemonError_Propagates(t *testing.T) {
	client := &fakeConstructionWorkerCapMutator{respErr: errors.New("no active construction pipeline for X1-NONE-I1")}

	_, err := runConstructionWorkers(context.Background(), client, "X1-NONE-I1", 10, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "X1-NONE-I1")
}

// TestConstructionWorkersCommand_RejectsNonPositiveCount proves the acceptance bound: a 0/negative
// --count is rejected at the CLI boundary BEFORE any daemon call (the check precedes connectDaemon),
// so no write can ever reach the pipeline row with a cap that would deadlock the drain's errgroup.
func TestConstructionWorkersCommand_RejectsNonPositiveCount(t *testing.T) {
	for _, count := range []string{"0", "-2"} {
		cmd := newConstructionWorkersCommand()
		cmd.SetArgs([]string{"X1-FB5-I56", "--count", count})
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true

		err := cmd.Execute()
		require.Error(t, err, "--count %s must be rejected before any write", count)
		require.Contains(t, strings.ToLower(err.Error()), "count")
	}
}
