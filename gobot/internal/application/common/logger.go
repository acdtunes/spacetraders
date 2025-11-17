package common

import "context"

// ContainerLogger provides logging functionality for container operations
type ContainerLogger interface {
	Log(level, message string, metadata map[string]interface{})
}

// Context keys for passing logger through context
type contextKey int

const (
	loggerKey contextKey = iota
)

// WithLogger adds a logger to the context
func WithLogger(ctx context.Context, logger ContainerLogger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// LoggerFromContext extracts the logger from context, or returns a no-op logger if not found
func LoggerFromContext(ctx context.Context) ContainerLogger {
	if logger, ok := ctx.Value(loggerKey).(ContainerLogger); ok {
		return logger
	}
	return &noOpLogger{}
}

// noOpLogger is a logger that does nothing (fallback when no logger in context)
type noOpLogger struct{}

func (l *noOpLogger) Log(level, message string, metadata map[string]interface{}) {
	// Do nothing
}
