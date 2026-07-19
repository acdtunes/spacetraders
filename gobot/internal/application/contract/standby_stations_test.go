package contract

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// These tests cover the contract coordinator's standby-station SET (its "hubs"):
// it must be mutable on a RUNNING coordinator with no restart and resolved LIVE
// each discovery pass — the same symmetry the dedicated-fleet ship tag already
// has. The persisted set is authoritative and a live change is not reverted by
// the launch snapshot (RULINGS #2, tested against a simulated restart in the
// grpc package).

// --- ApplyStandbyStationChange (the pure hub-set mutation) ------------------

func TestApplyStandbyStationChange_AddToEmpty_Adds(t *testing.T) {
	got, changed := ApplyStandbyStationChange(nil, "X1-TW-A1", true)
	if !changed {
		t.Fatalf("adding a hub to an empty set must report changed=true")
	}
	if !reflect.DeepEqual(got, []string{"X1-TW-A1"}) {
		t.Fatalf("expected [X1-TW-A1], got %v", got)
	}
}

func TestApplyStandbyStationChange_AddExisting_NoOp(t *testing.T) {
	got, changed := ApplyStandbyStationChange([]string{"X1-TW-A1", "X1-TW-A2"}, "X1-TW-A1", true)
	if changed {
		t.Fatalf("adding a hub already in the set must be a no-op (changed=false)")
	}
	if !reflect.DeepEqual(got, []string{"X1-TW-A1", "X1-TW-A2"}) {
		t.Fatalf("a no-op add must preserve the set unchanged, got %v", got)
	}
}

func TestApplyStandbyStationChange_AddNew_AppendsPreservingOrder(t *testing.T) {
	got, changed := ApplyStandbyStationChange([]string{"X1-TW-A1"}, "X1-TW-B2", true)
	if !changed {
		t.Fatalf("adding a new hub must report changed=true")
	}
	if !reflect.DeepEqual(got, []string{"X1-TW-A1", "X1-TW-B2"}) {
		t.Fatalf("a new hub must append, preserving order, got %v", got)
	}
}

func TestApplyStandbyStationChange_RemovePresent_Removes(t *testing.T) {
	got, changed := ApplyStandbyStationChange([]string{"X1-TW-A1", "X1-TW-B2"}, "X1-TW-A1", false)
	if !changed {
		t.Fatalf("removing a present hub must report changed=true")
	}
	if !reflect.DeepEqual(got, []string{"X1-TW-B2"}) {
		t.Fatalf("expected the remaining hub [X1-TW-B2], got %v", got)
	}
}

func TestApplyStandbyStationChange_RemoveAbsent_NoOp(t *testing.T) {
	got, changed := ApplyStandbyStationChange([]string{"X1-TW-A1"}, "X1-TW-Z9", false)
	if changed {
		t.Fatalf("removing an absent hub must be a no-op (changed=false)")
	}
	if !reflect.DeepEqual(got, []string{"X1-TW-A1"}) {
		t.Fatalf("a no-op remove must preserve the set unchanged, got %v", got)
	}
}

func TestApplyStandbyStationChange_RemoveLast_YieldsEmptySet(t *testing.T) {
	got, changed := ApplyStandbyStationChange([]string{"X1-TW-A1"}, "X1-TW-A1", false)
	if !changed {
		t.Fatalf("removing the last hub must report changed=true")
	}
	if len(got) != 0 {
		t.Fatalf("removing the last hub must yield an empty set (homing disabled), got %v", got)
	}
}

func TestApplyStandbyStationChange_TrimsWhitespace(t *testing.T) {
	// A CLI --waypoint value may carry stray whitespace; the mutation trims it so
	// " X1-TW-A1 " and "X1-TW-A1" are the same hub (no phantom duplicate).
	got, changed := ApplyStandbyStationChange([]string{"X1-TW-A1"}, "  X1-TW-A1  ", true)
	if changed {
		t.Fatalf("a whitespace-only difference must be treated as the same hub (no-op add)")
	}
	if !reflect.DeepEqual(got, []string{"X1-TW-A1"}) {
		t.Fatalf("expected the set unchanged, got %v", got)
	}
}

// --- ResolveStandbyStations (the live-read each pass) -----------------------

// fakeStandbyProvider serves a fixed live set (or error), standing in for the
// container-config-backed provider the daemon writes via `fleet hub`.
type fakeStandbyProvider struct {
	live []string
	err  error
}

func (p *fakeStandbyProvider) StandbyStations(_ context.Context, _ string, _ int) ([]string, error) {
	return p.live, p.err
}

// standbyCapturingLogger records level+message so the fallback WARNING is
// assertable (idleArbCapturingLogger drops the level).
type standbyCapturingLogger struct {
	levels   []string
	messages []string
}

func (l *standbyCapturingLogger) Log(level, message string, _ map[string]interface{}) {
	l.levels = append(l.levels, level)
	l.messages = append(l.messages, message)
}

func (l *standbyCapturingLogger) hasWarning() bool {
	for _, lv := range l.levels {
		if lv == "WARNING" {
			return true
		}
	}
	return false
}

// TestResolveStandbyStations_LiveSetWins: the LIVE set from the provider — not
// the frozen launch snapshot — is what homing reads each pass. A hub added live
// (present in the provider, absent from the launch list) is returned; the
// launch list is not consulted.
func TestResolveStandbyStations_LiveSetWins(t *testing.T) {
	provider := &fakeStandbyProvider{live: []string{"X1-TW-A1", "X1-TW-C3"}}
	launchList := []string{"X1-TW-A1"} // C3 was `fleet hub add`ed after launch

	got := ResolveStandbyStations(context.Background(), &standbyCapturingLogger{}, provider, "cc-1", 2, launchList)

	if !reflect.DeepEqual(got, []string{"X1-TW-A1", "X1-TW-C3"}) {
		t.Fatalf("homing must read the LIVE set including the live-added hub, got %v", got)
	}
}

// TestResolveStandbyStations_EmptyLiveSet_HomingDisabled proves an operator who
// `fleet hub remove`s every hub disables homing on the next pass even though the
// launch list was non-empty — the empty LIVE set is honored, never overridden by
// the stale launch snapshot (else a removal could never take effect live).
func TestResolveStandbyStations_EmptyLiveSet_HomingDisabled(t *testing.T) {
	provider := &fakeStandbyProvider{live: []string{}}
	launchList := []string{"X1-TW-A1", "X1-TW-B2"}

	got := ResolveStandbyStations(context.Background(), &standbyCapturingLogger{}, provider, "cc-1", 2, launchList)

	if len(got) != 0 {
		t.Fatalf("an empty live set must disable homing (not fall back to the launch list), got %v", got)
	}
}

// TestResolveStandbyStations_ReadError_FallsBackToLaunchList: a provider read
// failure falls back to the launch snapshot and warns, rather than losing all
// hub data (mirrors resolveDedicatedMembersForHoming's fallback).
func TestResolveStandbyStations_ReadError_FallsBackToLaunchList(t *testing.T) {
	provider := &fakeStandbyProvider{err: fmt.Errorf("db unavailable")}
	launchList := []string{"X1-TW-A1", "X1-TW-B2"}
	logger := &standbyCapturingLogger{}

	got := ResolveStandbyStations(context.Background(), logger, provider, "cc-1", 2, launchList)

	if !reflect.DeepEqual(got, launchList) {
		t.Fatalf("on a read error the resolver must fall back to the launch list, got %v", got)
	}
	if !logger.hasWarning() {
		t.Fatalf("expected a WARNING when the live standby-station read fails, got levels %v", logger.levels)
	}
}

// TestResolveStandbyStations_NilProvider_UsesLaunchList proves the optional-port
// contract: with no provider wired, the coordinator falls back to the launch
// snapshot.
func TestResolveStandbyStations_NilProvider_UsesLaunchList(t *testing.T) {
	launchList := []string{"X1-TW-A1"}

	got := ResolveStandbyStations(context.Background(), &standbyCapturingLogger{}, nil, "cc-1", 2, launchList)

	if !reflect.DeepEqual(got, launchList) {
		t.Fatalf("a nil provider must use the launch list unchanged, got %v", got)
	}
}

var _ common.ContainerLogger = (*standbyCapturingLogger)(nil)
