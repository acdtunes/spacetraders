package services

import (
	"context"
	"fmt"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/application/common"
	contractTypes "github.com/andrescamacho/spacetraders-go/internal/application/contract/types"
	domainContract "github.com/andrescamacho/spacetraders-go/internal/domain/contract"
	"github.com/andrescamacho/spacetraders-go/internal/domain/shared"
)

// acceptFakeMediator counts AcceptContractCommand dispatches for the
// EnsureAccepted tests (sp-1z2h: the defer gate must accept BEFORE parking —
// an unaccepted deferred contract rots past its accept-by deadline into a
// skip).
type acceptFakeMediator struct {
	common.Mediator

	acceptCalls int
	returned    *domainContract.Contract
}

func (m *acceptFakeMediator) Send(_ context.Context, request common.Request) (common.Response, error) {
	if _, ok := request.(*contractTypes.AcceptContractCommand); ok {
		m.acceptCalls++
		return &contractTypes.AcceptContractResponse{Contract: m.returned}, nil
	}
	return nil, fmt.Errorf("unexpected mediator command in accept test: %T", request)
}

func acceptTestContract(t *testing.T, accepted bool) *domainContract.Contract {
	t.Helper()
	terms := domainContract.Terms{
		Payment: domainContract.Payment{OnAccepted: 10_000, OnFulfilled: 90_000},
		Deliveries: []domainContract.Delivery{{
			TradeSymbol:       "IRON_ORE",
			DestinationSymbol: "X1-TEST-A1",
			UnitsRequired:     10,
		}},
		Deadline: "2026-07-16T00:00:00Z",
	}
	c, err := domainContract.NewContract("ct-accept", shared.MustNewPlayerID(1), "COSMIC", "PROCUREMENT", terms, nil)
	if err != nil {
		t.Fatalf("NewContract: %v", err)
	}
	if accepted {
		if err := c.Accept(); err != nil {
			t.Fatalf("Accept: %v", err)
		}
	}
	return c
}

func TestEnsureAccepted_UnacceptedContract_SendsAccept(t *testing.T) {
	acceptedCopy := acceptTestContract(t, true)
	mediator := &acceptFakeMediator{returned: acceptedCopy}
	service := NewContractMarketService(mediator, nil)

	got, err := service.EnsureAccepted(context.Background(), acceptTestContract(t, false), shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("EnsureAccepted: %v", err)
	}
	if mediator.acceptCalls != 1 {
		t.Fatalf("expected exactly one AcceptContractCommand, got %d", mediator.acceptCalls)
	}
	if got != acceptedCopy {
		t.Fatalf("expected the refreshed contract from the accept response")
	}
}

func TestEnsureAccepted_AlreadyAccepted_NoCommandSent(t *testing.T) {
	mediator := &acceptFakeMediator{}
	service := NewContractMarketService(mediator, nil)

	contract := acceptTestContract(t, true)
	got, err := service.EnsureAccepted(context.Background(), contract, shared.MustNewPlayerID(1))
	if err != nil {
		t.Fatalf("EnsureAccepted: %v", err)
	}
	if mediator.acceptCalls != 0 {
		t.Fatalf("already-accepted contract must be a no-op, got %d accept calls", mediator.acceptCalls)
	}
	if got != contract {
		t.Fatalf("expected the same contract back")
	}
}
