package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// These tests cover the `goods factory workers` CLI surface (sp-ev0n): the live,
// per-op mutation of a running goods factory's concurrent-hull cap. By construction
// the fake below has ONLY the FactoryWorkerCap RPC — no container-restart method — so
// "no restart" is guaranteed by the surface this verb can reach, exactly as the
// `fleet hub` tests guarantee it for the standby set. The daemon is the sole writer
// of the persisted cap (RULINGS #3).

// workerCapCall records one FactoryWorkerCap invocation.
type workerCapCall struct {
	containerID string
	count       int
}

// fakeWorkerCapMutator is an in-memory factoryWorkerCapMutator recording every call and
// serving a canned response. It has NO restart method by construction.
type fakeWorkerCapMutator struct {
	calls   []workerCapCall
	resp    *pb.FactoryWorkerCapResponse
	respErr error
}

func (f *fakeWorkerCapMutator) FactoryWorkerCap(_ context.Context, containerID string, count int, _ *int32, _ *string) (*pb.FactoryWorkerCapResponse, error) {
	f.calls = append(f.calls, workerCapCall{containerID: containerID, count: count})
	if f.respErr != nil {
		return nil, f.respErr
	}
	return f.resp, nil
}

func TestRunGoodsFactoryWorkers_SetsCapLive(t *testing.T) {
	client := &fakeWorkerCapMutator{resp: &pb.FactoryWorkerCapResponse{
		ContainerId: "goods_factory-FAB_MATS-abcd",
		WorkerCap:   2,
		Changed:     true,
	}}

	msg, err := runGoodsFactoryWorkers(context.Background(), client, "goods_factory-FAB_MATS-abcd", 2, nil, nil)
	require.NoError(t, err)

	require.Len(t, client.calls, 1)
	require.Equal(t, workerCapCall{containerID: "goods_factory-FAB_MATS-abcd", count: 2}, client.calls[0])
	require.Contains(t, msg, "goods_factory-FAB_MATS-abcd")
	require.Contains(t, msg, "2")
	require.Contains(t, strings.ToLower(msg), "no container restart")
}

func TestRunGoodsFactoryWorkers_AlreadyAtCount_ReportsNoOp(t *testing.T) {
	client := &fakeWorkerCapMutator{resp: &pb.FactoryWorkerCapResponse{
		ContainerId: "goods_factory-FAB_MATS-abcd",
		WorkerCap:   4,
		Changed:     false, // already at count → daemon reports no change
	}}

	msg, err := runGoodsFactoryWorkers(context.Background(), client, "goods_factory-FAB_MATS-abcd", 4, nil, nil)
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(msg), "already")
}

func TestRunGoodsFactoryWorkers_DaemonError_Propagates(t *testing.T) {
	client := &fakeWorkerCapMutator{respErr: errors.New("no factory container goods_factory-X for player 1")}

	_, err := runGoodsFactoryWorkers(context.Background(), client, "goods_factory-X", 2, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "goods_factory-X")
}
