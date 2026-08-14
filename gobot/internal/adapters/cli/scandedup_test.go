package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	pb "github.com/andrescamacho/spacetraders-go/pkg/proto/daemon"
)

// These tests cover the `fleet scan-dedup add|remove|list` CLI surface: the
// live mutation/read of the sp-7q61f scan-dedup A/B test's ship-symbol
// allowlist. By construction the fake below has ONLY the ScanDedupAllowlist
// RPC — no container-restart method — so "no restart" is guaranteed by the
// surface these verbs can reach. The daemon is the sole writer of the
// persisted set (RULINGS #3).

// scanDedupCall records one ScanDedupAllowlist invocation so a test can assert
// the exact (action, shipSymbol) tuple the verb sent.
type scanDedupCall struct {
	action     string
	shipSymbol string
}

// fakeScanDedupMutator is an in-memory scanDedupMutator recording every call
// and serving a canned response. It has NO restart method by construction.
type fakeScanDedupMutator struct {
	calls   []scanDedupCall
	resp    *pb.ScanDedupAllowlistResponse
	respErr error
}

func (f *fakeScanDedupMutator) ScanDedupAllowlist(_ context.Context, action, shipSymbol string, _ *PlayerIdentifier) (*pb.ScanDedupAllowlistResponse, error) {
	f.calls = append(f.calls, scanDedupCall{action: action, shipSymbol: shipSymbol})
	if f.respErr != nil {
		return nil, f.respErr
	}
	return f.resp, nil
}

func TestRunScanDedupAdd_ArmsShipLive(t *testing.T) {
	client := &fakeScanDedupMutator{resp: &pb.ScanDedupAllowlistResponse{
		ShipSymbols: []string{"TORWIND-1", "TORWIND-5"},
		Changed:     true,
	}}

	msg, err := runScanDedupAdd(context.Background(), client, "TORWIND-5", nil)
	require.NoError(t, err)

	require.Len(t, client.calls, 1)
	require.Equal(t, scanDedupCall{action: "add", shipSymbol: "TORWIND-5"}, client.calls[0])
	require.Contains(t, msg, "TORWIND-5")
	require.Contains(t, strings.ToLower(msg), "no container restart")
}

func TestRunScanDedupAdd_AlreadyArmed_ReportsNoOp(t *testing.T) {
	client := &fakeScanDedupMutator{resp: &pb.ScanDedupAllowlistResponse{
		ShipSymbols: []string{"TORWIND-5"},
		Changed:     false,
	}}

	msg, err := runScanDedupAdd(context.Background(), client, "TORWIND-5", nil)
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(msg), "already")
}

func TestRunScanDedupRemove_DisarmsShipLive(t *testing.T) {
	client := &fakeScanDedupMutator{resp: &pb.ScanDedupAllowlistResponse{
		ShipSymbols: []string{},
		Changed:     true,
	}}

	msg, err := runScanDedupRemove(context.Background(), client, "TORWIND-5", nil)
	require.NoError(t, err)

	require.Len(t, client.calls, 1)
	require.Equal(t, scanDedupCall{action: "remove", shipSymbol: "TORWIND-5"}, client.calls[0])
	require.Contains(t, msg, "TORWIND-5")
	require.Contains(t, strings.ToLower(msg), "no container restart")
}

func TestRunScanDedupRemove_NotArmed_ReportsNoOp(t *testing.T) {
	client := &fakeScanDedupMutator{resp: &pb.ScanDedupAllowlistResponse{
		ShipSymbols: []string{},
		Changed:     false,
	}}

	msg, err := runScanDedupRemove(context.Background(), client, "TORWIND-9", nil)
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(msg), "not armed")
}

func TestRunScanDedupList_ReportsArmedSet(t *testing.T) {
	client := &fakeScanDedupMutator{resp: &pb.ScanDedupAllowlistResponse{
		ShipSymbols: []string{"TORWIND-1", "TORWIND-5"},
	}}

	msg, err := runScanDedupList(context.Background(), client, nil)
	require.NoError(t, err)

	require.Len(t, client.calls, 1)
	require.Equal(t, scanDedupCall{action: "list", shipSymbol: ""}, client.calls[0])
	require.Contains(t, msg, "TORWIND-1")
	require.Contains(t, msg, "TORWIND-5")
}

func TestRunScanDedupList_Empty_ReportsDefaultOffClearly(t *testing.T) {
	client := &fakeScanDedupMutator{resp: &pb.ScanDedupAllowlistResponse{ShipSymbols: []string{}}}

	msg, err := runScanDedupList(context.Background(), client, nil)
	require.NoError(t, err)
	require.Contains(t, strings.ToLower(msg), "no ships armed")
}

func TestRunScanDedupAdd_DaemonError_Propagates(t *testing.T) {
	client := &fakeScanDedupMutator{respErr: errors.New("daemon unreachable")}

	_, err := runScanDedupAdd(context.Background(), client, "TORWIND-5", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "TORWIND-5")
}
