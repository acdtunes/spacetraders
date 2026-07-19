package api

import (
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"
)

// classifyPurpose buckets each HTTP attempt into poll/transact/retry so the
// budget tracker can show how much of the request budget is spent
// on status polling vs real transactions vs wasted retries.

func TestClassifyPurpose_FirstAttemptGet_IsPoll(t *testing.T) {
	got := classifyPurpose("GET", 0)
	if got != apibudget.PurposePoll {
		t.Fatalf("expected %q, got %q", apibudget.PurposePoll, got)
	}
}

func TestClassifyPurpose_FirstAttemptPost_IsTransact(t *testing.T) {
	got := classifyPurpose("POST", 0)
	if got != apibudget.PurposeTransact {
		t.Fatalf("expected %q, got %q", apibudget.PurposeTransact, got)
	}
}

func TestClassifyPurpose_FirstAttemptPatch_IsTransact(t *testing.T) {
	got := classifyPurpose("PATCH", 0)
	if got != apibudget.PurposeTransact {
		t.Fatalf("expected %q, got %q", apibudget.PurposeTransact, got)
	}
}

func TestClassifyPurpose_RetryAttemptGet_IsRetryNotPoll(t *testing.T) {
	got := classifyPurpose("GET", 1)
	if got != apibudget.PurposeRetry {
		t.Fatalf("expected %q, got %q", apibudget.PurposeRetry, got)
	}
}

func TestClassifyPurpose_RetryAttemptPost_IsRetryNotTransact(t *testing.T) {
	got := classifyPurpose("POST", 2)
	if got != apibudget.PurposeRetry {
		t.Fatalf("expected %q, got %q", apibudget.PurposeRetry, got)
	}
}
