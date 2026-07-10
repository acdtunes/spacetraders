package shared

import "testing"

// NormalizedOperationType is the single point where a coordinator's raw operation
// string becomes the operation_type persisted on every ledger row it writes. The
// tour_run → tour mapping is what lets the graduation baseline (tour_report.go)
// exclude tour trades via operation_type <> 'tour' (sp-lgnh); the other rows pin
// that adding it re-tagged nothing else — every existing coordinator's value is
// byte-identical to before.
func TestNormalizedOperationType(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"tour_run", "tour"}, // sp-lgnh: the mapping this change adds
		{"arbitrage_worker", "arbitrage"},
		{"contract_workflow", "contract"},
		{"balance_ship_position", "fleet rebalancing"},
		{"goods_factory_coordinator", "factory"},
		{"manufacturing_worker", "manufacturing"},
		{"trade_route", "trade_route"},           // passthrough (unmapped) — unchanged
		{"factory_workflow", "factory_workflow"}, // passthrough (unmapped) — unchanged
	}
	for _, tc := range cases {
		got := NewOperationContext("ctr-1", tc.raw).NormalizedOperationType()
		if got != tc.want {
			t.Errorf("NormalizedOperationType(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// A nil/empty context normalizes to "" (the cargo-tx path then records "manual"),
// so the tour mapping cannot accidentally tag an unrelated contextless write.
func TestNormalizedOperationType_NilAndEmpty(t *testing.T) {
	var nilCtx *OperationContext
	if got := nilCtx.NormalizedOperationType(); got != "" {
		t.Errorf("nil context normalized to %q, want \"\"", got)
	}
	// NewOperationContext rejects an empty type (returns nil), so build the empty
	// case directly to prove the guard clause, not the constructor.
	empty := &OperationContext{ContainerID: "ctr-1", OperationType: ""}
	if got := empty.NormalizedOperationType(); got != "" {
		t.Errorf("empty operation type normalized to %q, want \"\"", got)
	}
}
