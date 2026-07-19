package api

import "github.com/andrescamacho/spacetraders-go/internal/domain/apibudget"

// classifyPurpose buckets one HTTP attempt for the budget tracker: a retry
// (attempt > 0) is always PurposeRetry regardless of method, since
// retries represent budget spent recovering from a prior failure rather than
// genuine poll/transact work. A first attempt (attempt == 0) is PurposePoll
// for GET (status/read endpoints, the dominant poll-cadence cost) and
// PurposeTransact for every other verb (the action that actually earns).
func classifyPurpose(method string, attempt int) apibudget.Purpose {
	if attempt > 0 {
		return apibudget.PurposeRetry
	}
	if method == "GET" {
		return apibudget.PurposePoll
	}
	return apibudget.PurposeTransact
}
