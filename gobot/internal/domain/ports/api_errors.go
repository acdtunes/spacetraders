package ports

import "fmt"

// APIError is a terminal (non-retryable) HTTP error the SpaceTraders API returned,
// carrying the status code so a caller can distinguish a PERMANENT client-error verdict
// (4xx — e.g. a jump-gate fetch that 400s because the waypoint is uncharted, has no ship
// present, or is not a gate) from a TRANSIENT server/network failure (5xx / 429 / network,
// which the adapter retries and surfaces as "max retries exceeded", never as an *APIError).
//
// It preserves the exact legacy error string ("API error (status <code>): <body>") that the
// adapter's request() used to build with fmt.Errorf, so every existing body/error-code string
// parser (cooldown extraction, insufficient-credits, the dock/orbit classifiers) keeps matching
// unchanged. The only added capability is the typed StatusCode for errors.As classification
// (sp-4bm3 — negative-cache a 400'd jump gate WITHOUT caching a transient blip).
type APIError struct {
	StatusCode int
	Body       string
}

// Error preserves the legacy "API error (status %d): %s" wording verbatim (body included), so
// callers that string-match on the message or its embedded JSON are unaffected by the typing.
func (e *APIError) Error() string {
	return fmt.Sprintf("API error (status %d): %s", e.StatusCode, e.Body)
}

// IsClientError reports whether the status is a 4xx — a permanent, do-not-retry-soon verdict
// the API rendered on the request's merits (uncharted / no ship present / not a gate), as
// opposed to a transient 5xx the caller should retry.
func (e *APIError) IsClientError() bool {
	return e.StatusCode >= 400 && e.StatusCode < 500
}
