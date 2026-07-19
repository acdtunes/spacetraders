package commands

import (
	"context"
	"errors"
	"testing"
)

// sp-sjvv (ktio-B): the fleet autosizer must NOT auto-buy contract haulers for a contract-GRADUATED fleet
// (sp-difa.1), even with the contract_delivery class armed. This is defense-in-depth beside the capacity
// reconciler's own graduation idle: the ContractDeliveryDemandBridge holds the LATEST emitted demand and
// the reconciler simply STOPS emitting when graduated (it never clears a prior Present demand), so a fleet
// that emitted contract-delivery demand and THEN graduated would leave STALE Present demand the autosizer
// would otherwise keep buying against — re-spawning the contract op difa.1 durably retired. The gate is
// fail-OPEN: an unwired reader or a read error is treated as UN-graduated (armed scaling runs as today),
// and it is READ ONLY when the class is armed (a disarmed default triggers no graduation read at all).

// fakeGraduationReader records how many times it was consulted so a test can prove the disarmed default
// never even reads graduation (byte-identical to pre-sjvv).
type fakeGraduationReader struct {
	graduated bool
	err       error
	calls     int
}

func (f *fakeGraduationReader) IsContractGraduated(_ context.Context, _ int) (bool, error) {
	f.calls++
	return f.graduated, f.err
}

// GRADUATED: the armed contract_delivery class is SKIPPED entirely — the provider (the demand bridge) is
// never consulted, so a graduated fleet reads no contract-delivery demand and buys nothing.
func TestContractDelivery_Graduated_ClassSkipped(t *testing.T) {
	spy := &spyContractDeliveryProvider{}
	grad := &fakeGraduationReader{graduated: true}
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	h.AddDemandProvider(spy)
	h.SetContractGraduationReader(grad)

	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{ContainerID: "c1", ContractDeliveryHullsEnabled: true}); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if grad.calls == 0 {
		t.Fatalf("graduation must be consulted when the contract_delivery class is armed")
	}
	if spy.calls != 0 {
		t.Fatalf("GRADUATED: the contract_delivery class must be SKIPPED (provider never consulted), got %d calls", spy.calls)
	}
}

// NOT graduated: the armed class runs and the provider is consulted (the mature-fleet armed path).
func TestContractDelivery_NotGraduated_ClassRuns(t *testing.T) {
	spy := &spyContractDeliveryProvider{}
	grad := &fakeGraduationReader{graduated: false}
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	h.AddDemandProvider(spy)
	h.SetContractGraduationReader(grad)

	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{ContainerID: "c1", ContractDeliveryHullsEnabled: true}); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if spy.calls == 0 {
		t.Fatalf("NOT graduated + armed: the contract_delivery class must run (provider consulted)")
	}
}

// Fail-OPEN on an UNWIRED reader: no graduation reader ⇒ the armed class runs exactly as pre-sjvv (a
// mis-wire never silently suppresses armed scaling).
func TestContractDelivery_GraduationReaderNil_FailOpen(t *testing.T) {
	spy := &spyContractDeliveryProvider{}
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	h.AddDemandProvider(spy)
	// No SetContractGraduationReader — nil reader.

	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{ContainerID: "c1", ContractDeliveryHullsEnabled: true}); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if spy.calls == 0 {
		t.Fatalf("nil graduation reader must FAIL OPEN: the armed class runs (provider consulted)")
	}
}

// Fail-OPEN on a READ ERROR: a transient graduation-read failure ⇒ treated as UN-graduated, the armed
// class runs (never let a DB hiccup silently suppress the funding-floor scaling).
func TestContractDelivery_GraduationReadError_FailOpen(t *testing.T) {
	spy := &spyContractDeliveryProvider{}
	grad := &fakeGraduationReader{err: errors.New("db hiccup")}
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	h.AddDemandProvider(spy)
	h.SetContractGraduationReader(grad)

	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{ContainerID: "c1", ContractDeliveryHullsEnabled: true}); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if spy.calls == 0 {
		t.Fatalf("a graduation read error must FAIL OPEN: the armed class runs (provider consulted)")
	}
}

// DISARMED (the default): the graduation gate reads NOTHING — no DB call when the class is off, so the
// tick is byte-identical to pre-sjvv. (The class is skipped by classDisabled before any graduation read.)
func TestContractDelivery_Disarmed_NoGraduationRead(t *testing.T) {
	spy := &spyContractDeliveryProvider{}
	grad := &fakeGraduationReader{graduated: true} // would gate IF it were ever read
	h := NewRunFleetAutosizerCoordinatorHandler(nil)
	h.AddDemandProvider(spy)
	h.SetContractGraduationReader(grad)

	// DISARMED: ContractDeliveryHullsEnabled unset.
	if _, err := h.reconcileOnce(context.Background(), &RunFleetAutosizerCoordinatorCommand{ContainerID: "c1"}); err != nil {
		t.Fatalf("reconcile error: %v", err)
	}
	if grad.calls != 0 {
		t.Fatalf("DISARMED: graduation must NOT be read at all (byte-identical, no DB call), got %d calls", grad.calls)
	}
	if spy.calls != 0 {
		t.Fatalf("DISARMED: the class is skipped before any provider consult, got %d calls", spy.calls)
	}
}
