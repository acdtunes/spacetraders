package commands

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/player"
	domainPorts "github.com/andrescamacho/spacetraders-go/internal/domain/ports"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// termsMetStoringContractRepo is an in-memory ContractRepository that stores the
// contract by pointer and counts Add() calls, so a test can prove the 4509
// reconciliation was PERSISTED (RULINGS #2 restart-safe), not merely mutated in
// memory.
type termsMetStoringContractRepo struct {
	contract.ContractRepository
	c        *contract.Contract
	addCalls int
}

func (r *termsMetStoringContractRepo) FindByID(_ context.Context, _ string) (*contract.Contract, error) {
	return r.c, nil
}

func (r *termsMetStoringContractRepo) Add(_ context.Context, c *contract.Contract) error {
	r.addCalls++
	r.c = c
	return nil
}

// termsMet4509APIClient fakes the deliver-cargo port returning the exact
// SpaceTraders 400 code 4509 "delivery terms have been met" wire error, wrapped
// the same way the real client wraps it, and counts the calls so a test can
// prove there is no retry loop.
type termsMet4509APIClient struct {
	domainPorts.APIClient
	deliverCalls int
}

func (c *termsMet4509APIClient) DeliverContract(_ context.Context, _, _, _ string, _ int, _ string) (*domainPorts.ContractData, error) {
	c.deliverCalls++
	return nil, fmt.Errorf("failed to deliver contract: %w",
		fmt.Errorf(`API error (status 400): {"error":{"message":"Contract delivery terms for FERTILIZER have been met.","code":4509}}`))
}

func mustAcceptedTermsMetContract(t *testing.T, unitsRequired, unitsFulfilled int) *contract.Contract {
	t.Helper()
	terms := contract.Terms{
		Payment: contract.Payment{OnAccepted: 5000, OnFulfilled: 20000},
		Deliveries: []contract.Delivery{
			{TradeSymbol: "FERTILIZER", DestinationSymbol: "X1-UM5-K83", UnitsRequired: unitsRequired, UnitsFulfilled: unitsFulfilled},
		},
		Deadline: "2999-01-01T00:00:00Z",
	}
	c, err := contract.NewContract("C-1", shared.MustNewPlayerID(4), "COSMIC", "PROCUREMENT", terms, nil)
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	if err := c.Accept(); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	return c
}

// The P1 wedge: an over-fetching hull tries to deliver to a contract whose terms
// the server already considers MET (server 64/64), while the local row lagged
// behind (0/64). Before this fix the deliver handler wrapped the 4509 as a plain
// error; the worker crashed, the coordinator re-selected the same cargo-laden
// hull, and the deliver 4509'd again — forever, wedging the whole contract
// income stream.
//
// The handler must instead treat the 4509 as authoritative server truth:
// reconcile the local delivery to fully fulfilled (UnitsFulfilled=UnitsRequired,
// NOT the partial cmd.Units), PERSIST it (restart-safe), and return a terminal
// success so the worker completes -> CanFulfill()=true -> the contract leaves the
// active set and never re-selects.
func TestDeliverContract_TermsMet4509_ReconcilesToRequired_PersistsAndSucceeds(t *testing.T) {
	// Local diverged at 0/64; this hull attempts a PARTIAL 40 (it holds less than
	// the diverged remaining). Reconcile must set fulfilled to the REQUIRED 64
	// (server truth), never to the attempted 40 — otherwise CanFulfill() stays
	// false and the contract keeps re-selecting.
	c := mustAcceptedTermsMetContract(t, 64, 0)
	repo := &termsMetStoringContractRepo{c: c}
	api := &termsMet4509APIClient{}
	playerRepo := &fakeContractPlayerRepo{p: player.NewPlayer(shared.MustNewPlayerID(4), "AGENT", "tok")}
	h := NewDeliverContractHandler(repo, api, playerRepo)

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	resp, err := h.Handle(ctx, &DeliverContractCommand{
		ContractID:  "C-1",
		ShipSymbol:  "TORWIND-19",
		TradeSymbol: "FERTILIZER",
		Units:       40,
		PlayerID:    shared.MustNewPlayerID(4),
	})

	// Terminal SUCCESS, not an error: the worker must NOT crash-and-retry.
	if err != nil {
		t.Fatalf("a 4509 terms-met delivery must be reconciled to terminal success, got error: %v", err)
	}
	deliverResp, ok := resp.(*DeliverContractResponse)
	if !ok {
		t.Fatalf("expected *DeliverContractResponse, got %T", resp)
	}
	if deliverResp.Contract == nil {
		t.Fatalf("expected the reconciled contract on the response")
	}

	// Reconciled to REQUIRED (server truth), not the attempted 40.
	saved, err := repo.FindByID(ctx, "C-1")
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	d := saved.Terms().Deliveries[0]
	if d.UnitsFulfilled != d.UnitsRequired {
		t.Fatalf("expected the delivery reconciled to UnitsRequired=%d (server terms met), got UnitsFulfilled=%d", d.UnitsRequired, d.UnitsFulfilled)
	}
	if !saved.CanFulfill() {
		t.Fatalf("reconciled contract must be fulfillable so it leaves the active set and never re-selects")
	}

	// Persisted (RULINGS #2 restart-safe) and NOT retried.
	if repo.addCalls == 0 {
		t.Fatalf("expected the reconciled contract to be persisted (Add), so a restart cannot resume the loop")
	}
	if api.deliverCalls != 1 {
		t.Fatalf("expected exactly one deliver attempt (no retry loop), got %d", api.deliverCalls)
	}
}

// A non-4509 delivery failure must still propagate unchanged — the reconciliation
// is scoped strictly to the server's terms-met signal and must never swallow a
// genuine delivery error (e.g. a transient 500 or a 4219 "ship has 0 units").
func TestDeliverContract_NonTermsMetError_StillPropagates(t *testing.T) {
	c := mustAcceptedTermsMetContract(t, 64, 0)
	repo := &termsMetStoringContractRepo{c: c}
	api := &deliverErrorAPIClient{err: fmt.Errorf(`API error (status 400): {"error":{"message":"Ship has 0 unit(s) of FERTILIZER.","code":4219}}`)}
	playerRepo := &fakeContractPlayerRepo{p: player.NewPlayer(shared.MustNewPlayerID(4), "AGENT", "tok")}
	h := NewDeliverContractHandler(repo, api, playerRepo)

	ctx := auth.WithPlayerToken(context.Background(), "tok")
	_, err := h.Handle(ctx, &DeliverContractCommand{
		ContractID:  "C-1",
		ShipSymbol:  "TORWIND-19",
		TradeSymbol: "FERTILIZER",
		Units:       40,
		PlayerID:    shared.MustNewPlayerID(4),
	})

	if err == nil {
		t.Fatalf("a non-terms-met (4219) delivery failure must propagate, not be swallowed as success")
	}
	if repo.addCalls != 0 {
		t.Fatalf("a genuine delivery failure must NOT reconcile/persist the contract, got %d Add calls", repo.addCalls)
	}
}

// deliverErrorAPIClient returns a fixed error from DeliverContract.
type deliverErrorAPIClient struct {
	domainPorts.APIClient
	err error
}

func (c *deliverErrorAPIClient) DeliverContract(_ context.Context, _, _, _ string, _ int, _ string) (*domainPorts.ContractData, error) {
	return nil, c.err
}

func TestIsContractDeliveryTermsMetError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"4509 wire format", fmt.Errorf(`API error (status 400): {"error":{"message":"Contract delivery terms for FERTILIZER have been met.","code":4509}}`), true},
		{"wrapped 4509", fmt.Errorf("failed to deliver cargo: %w", fmt.Errorf(`API error (status 400): {"error":{"code":4509}}`)), true},
		{"insufficient credits 4600", fmt.Errorf(`API error (status 400): {"error":{"message":"x","code":4600}}`), false},
		{"ship-has-zero-units 4219", fmt.Errorf(`API error (status 400): {"error":{"message":"x","code":4219}}`), false},
		{"unrelated error", errors.New("server error (500)"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContractDeliveryTermsMetError(tc.err); got != tc.want {
				t.Errorf("isContractDeliveryTermsMetError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
