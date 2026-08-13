package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The GET half is a PREVIEW: it prices the hull without selling it, so it must
// stay a GET on /my/ships/{ship}/scrap and read data.transaction.
func TestGetScrapQuote_ReadsThePriceWithoutSellingTheHull(t *testing.T) {
	var gotMethod, gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"transaction":{
			"waypointSymbol":"X1-JP61-A1","shipSymbol":"TORWIND-446",
			"totalPrice":733517,"timestamp":"2026-08-08T12:00:00Z"}}}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	quote, err := client.GetScrapQuote(context.Background(), "TORWIND-446", "token")
	if err != nil {
		t.Fatalf("a successful quote must return no error, got %v", err)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("a quote must not sell the hull: want GET, got %s", gotMethod)
	}
	if gotPath != "/my/ships/TORWIND-446/scrap" {
		t.Fatalf("quote path = %s, want /my/ships/{ship}/scrap", gotPath)
	}
	if quote.TotalPrice != 733517 {
		t.Fatalf("TotalPrice = %d, want data.transaction.totalPrice 733517", quote.TotalPrice)
	}
	if quote.ShipSymbol != "TORWIND-446" || quote.WaypointSymbol != "X1-JP61-A1" {
		t.Fatalf("quote identifies the wrong hull/waypoint: %+v", quote)
	}
	if quote.Timestamp != "2026-08-08T12:00:00Z" {
		t.Fatalf("Timestamp = %q, want the transaction timestamp", quote.Timestamp)
	}
}

// The POST half banks credits, so the in-band post-sale balance must survive into
// ScrapResult — without it the ledger row cannot re-anchor to API truth.
func TestScrapShip_PostsAndParsesTheInBandAgentCredits(t *testing.T) {
	var gotMethod, gotPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{
			"agent":{"symbol":"TORWIND","credits":313733517},
			"transaction":{"waypointSymbol":"X1-JP61-A1","shipSymbol":"TORWIND-446",
			"totalPrice":733517,"timestamp":"2026-08-08T12:00:00Z"}}}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	result, err := client.ScrapShip(context.Background(), "TORWIND-446", "token")
	if err != nil {
		t.Fatalf("a successful scrap must return no error, got %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("scrap must be a POST, got %s", gotMethod)
	}
	if gotPath != "/my/ships/TORWIND-446/scrap" {
		t.Fatalf("scrap path = %s, want /my/ships/{ship}/scrap", gotPath)
	}
	if result.TotalPrice != 733517 {
		t.Fatalf("TotalPrice = %d, want 733517", result.TotalPrice)
	}
	if result.AgentCredits == nil {
		t.Fatal("AgentCredits must be set when the API returns data.agent, got nil")
	}
	if *result.AgentCredits != 313733517 {
		t.Fatalf("AgentCredits = %d, want the post-sale balance 313733517", *result.AgentCredits)
	}
}

// A response with no agent block must leave AgentCredits nil, so an omitted
// balance is never recorded as a real zero.
func TestScrapShip_LeavesAgentCreditsNilWhenTheAgentBlockIsAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"transaction":{"shipSymbol":"TORWIND-446","totalPrice":733517}}}`))
	}))
	defer server.Close()

	client := NewSpaceTradersClientWithConfig(server.URL, 0, time.Millisecond, nil)

	result, err := client.ScrapShip(context.Background(), "TORWIND-446", "token")
	if err != nil {
		t.Fatalf("a successful scrap must return no error, got %v", err)
	}
	if result.AgentCredits != nil {
		t.Fatalf("AgentCredits = %d, want nil for an omitted agent block", *result.AgentCredits)
	}
}
