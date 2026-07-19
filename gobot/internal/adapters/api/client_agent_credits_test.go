package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The SpaceTraders purchase/sell/refuel/accept/fulfill responses all return the
// agent's post-transaction credit balance in-band (data.agent.credits). The
// client used to discard it, forcing the ledger to reconstruct balance_after
// and letting it drift +~470k from the live API. These tests pin that
// the client now surfaces the authoritative value.

func TestPurchaseCargoCapturesInBandAgentCredits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"agent": {"credits": 123456},
				"transaction": {"totalPrice": 5000, "units": 10}
			}
		}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)
	res, err := client.PurchaseCargo(context.Background(), "SHIP-1", "IRON_ORE", 10, "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.TotalCost != 5000 || res.UnitsAdded != 10 {
		t.Fatalf("transaction fields wrong: %+v", res)
	}
	if res.AgentCredits == nil {
		t.Fatalf("expected AgentCredits to be populated from data.agent.credits")
	}
	if *res.AgentCredits != 123456 {
		t.Fatalf("expected AgentCredits 123456, got %d", *res.AgentCredits)
	}
}

func TestSellCargoCapturesInBandAgentCredits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"agent": {"credits": 777000},
				"transaction": {"totalPrice": 8200, "units": 20}
			}
		}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)
	res, err := client.SellCargo(context.Background(), "SHIP-1", "FUEL", 20, "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.TotalRevenue != 8200 || res.UnitsSold != 20 {
		t.Fatalf("transaction fields wrong: %+v", res)
	}
	if res.AgentCredits == nil || *res.AgentCredits != 777000 {
		t.Fatalf("expected AgentCredits 777000, got %v", res.AgentCredits)
	}
}

func TestRefuelShipCapturesInBandAgentCredits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"agent": {"credits": 42000},
				"fuel": {"current": 400, "capacity": 400},
				"transaction": {"units": 100, "totalPrice": 300}
			}
		}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)
	res, err := client.RefuelShip(context.Background(), "SHIP-1", "token", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.CreditsCost != 300 {
		t.Fatalf("expected CreditsCost 300, got %d", res.CreditsCost)
	}
	if res.AgentCredits == nil || *res.AgentCredits != 42000 {
		t.Fatalf("expected AgentCredits 42000, got %v", res.AgentCredits)
	}
}

func TestAcceptContractCapturesInBandAgentCredits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"agent": {"credits": 250000},
				"contract": {
					"id": "C-1", "factionSymbol": "COSMIC", "type": "PROCUREMENT",
					"terms": {"deadline": "2030-01-01T00:00:00Z", "payment": {"onAccepted": 50000, "onFulfilled": 150000}, "deliver": []},
					"accepted": true, "fulfilled": false
				}
			}
		}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)
	res, err := client.AcceptContract(context.Background(), "C-1", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.AgentCredits == nil || *res.AgentCredits != 250000 {
		t.Fatalf("expected AgentCredits 250000, got %v", res.AgentCredits)
	}
}

func TestFulfillContractCapturesInBandAgentCredits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"data": {
				"agent": {"credits": 400000},
				"contract": {
					"id": "C-1", "factionSymbol": "COSMIC", "type": "PROCUREMENT",
					"terms": {"deadline": "2030-01-01T00:00:00Z", "payment": {"onAccepted": 50000, "onFulfilled": 150000}, "deliver": []},
					"accepted": true, "fulfilled": true
				}
			}
		}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)
	res, err := client.FulfillContract(context.Background(), "C-1", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.AgentCredits == nil || *res.AgentCredits != 400000 {
		t.Fatalf("expected AgentCredits 400000, got %v", res.AgentCredits)
	}
}

// A response that omits the agent block must leave AgentCredits nil so the
// ledger falls back to reconstruction rather than anchoring on a bogus zero.
func TestPurchaseCargoWithoutAgentBlockLeavesCreditsNil(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data": {"transaction": {"totalPrice": 5000, "units": 10}}}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)
	res, err := client.PurchaseCargo(context.Background(), "SHIP-1", "IRON_ORE", 10, "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.AgentCredits != nil {
		t.Fatalf("expected nil AgentCredits when response omits agent, got %d", *res.AgentCredits)
	}
}
