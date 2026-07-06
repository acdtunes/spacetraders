package api

import "testing"

func TestClassifyNegotiateContractRealPath(t *testing.T) {
	// The real SpaceTraders endpoint is POST /my/ships/{symbol}/negotiate/contract.
	// The metrics label must resolve to the human-readable name, not the raw pattern.
	got := apiEndpointClassifier.classify("/my/ships/SHIP-1/negotiate/contract")
	if got != "Negotiate Contract" {
		t.Fatalf("expected %q, got %q", "Negotiate Contract", got)
	}
}
