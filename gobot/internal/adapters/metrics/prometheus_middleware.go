package metrics

import (
	"context"
	"reflect"
	"strings"
	"time"

	"github.com/andrescamacho/spacetraders-go/internal/application/mediator"
)

// PrometheusMiddleware creates a middleware that records command execution metrics
//
// This middleware wraps all command/query execution and records:
// - Execution duration (histogram)
// - Success/failure counts (counter)
//
// Command names are extracted via reflection and simplified to remove package prefixes.
// For example: "*commands.NavigateRouteCommand" becomes "NavigateRouteCommand"
func PrometheusMiddleware(collector *CommandMetricsCollector) mediator.Middleware {
	return func(ctx context.Context, request mediator.Request, next mediator.HandlerFunc) (mediator.Response, error) {
		// Skip metrics if collector is nil (metrics disabled)
		if collector == nil {
			return next(ctx, request)
		}

		// Extract command name via reflection
		commandName := extractCommandName(request)

		// Start timer
		start := time.Now()

		// Execute command/query
		response, err := next(ctx, request)

		// Record metrics
		duration := time.Since(start).Seconds()
		success := err == nil
		collector.RecordCommandExecution(commandName, duration, success)

		return response, err
	}
}

// extractCommandName extracts a clean command name from the request using reflection
// Examples:
//   - "*commands.NavigateRouteCommand" → "NavigateRouteCommand"
//   - "*queries.GetProfitLossQuery" → "GetProfitLossQuery"
//   - "*ledgerCommands.RecordTransactionCommand" → "RecordTransactionCommand"
func extractCommandName(request mediator.Request) string {
	if request == nil {
		return "UnknownCommand"
	}

	// Get the type via reflection
	requestType := reflect.TypeOf(request)

	// Get the full type name (e.g., "*commands.NavigateRouteCommand")
	fullName := requestType.String()

	// Remove pointer prefix if present
	fullName = strings.TrimPrefix(fullName, "*")

	// Split by '.' to separate package from type name
	parts := strings.Split(fullName, ".")

	// Return the last part (the actual command/query name)
	if len(parts) > 0 {
		return parts[len(parts)-1]
	}

	return fullName
}
