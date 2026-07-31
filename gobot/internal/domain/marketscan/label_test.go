package marketscan

import "testing"

// The label forms are defined on the enum so the metric's label DOMAIN is the
// enum itself. These pin the exact strings, because they are what every
// dashboard panel and alert expression matches on: renaming one silently breaks
// every query written against it, and nothing else in the build would notice.
func TestClassString(t *testing.T) {
	cases := []struct {
		class Class
		want  string
	}{
		{Discretionary, "discretionary"},
		{Earning, "earning"},
		{Paired, "paired"},
	}
	for _, tc := range cases {
		if got := tc.class.String(); got != tc.want {
			t.Errorf("Class(%d).String() = %q, want %q", tc.class, got, tc.want)
		}
	}
}

func TestDecisionString(t *testing.T) {
	if got := Spend.String(); got != "spend" {
		t.Errorf("Spend.String() = %q, want %q", got, "spend")
	}
	if got := ServeFromStore.String(); got != "serve_from_store" {
		t.Errorf("ServeFromStore.String() = %q, want %q", got, "serve_from_store")
	}
}

// An unnameable value must collapse onto the ZERO-VALUE name, never onto a
// numeric rendering. A "Class(9)" leaking into a label would mint one Prometheus
// series per stray integer — unbounded cardinality from a single bad cast — and
// the zero value is also the safe reading in both vocabularies: an unrecognised
// class is discretionary (budgeted), an unrecognised decision authorised no
// request.
func TestUnknownEnumValuesCollapseOntoTheZeroValueName(t *testing.T) {
	if got := Class(99).String(); got != "discretionary" {
		t.Errorf("Class(99).String() = %q, want the zero-value name %q", got, "discretionary")
	}
	if got := Decision(99).String(); got != "serve_from_store" {
		t.Errorf("Decision(99).String() = %q, want the zero-value name %q", got, "serve_from_store")
	}
}

// The zero values are load-bearing far beyond the labels: an unstamped read is
// Discretionary (budgeted, deniable) and an unreached decision is ServeFromStore
// (spends nothing). Both are the fail-safe direction, and the shipyard-overdraft
// fix rests on the first of them.
func TestZeroValuesAreTheFailSafeOnes(t *testing.T) {
	var class Class
	if class != Discretionary {
		t.Errorf("the zero Class must be Discretionary (budgeted), got %v", class)
	}
	var decision Decision
	if decision != ServeFromStore {
		t.Errorf("the zero Decision must be ServeFromStore (spends nothing), got %v", decision)
	}
}
