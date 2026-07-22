package commands

import (
	"context"
	"strings"
	"sync"

	"github.com/andrescamacho/spacetraders-go/internal/application/auth"
	"github.com/andrescamacho/spacetraders-go/internal/application/common"
)

// propFloorCapturingLogger records log lines so INFO-level assertions can be made against
// them. Originally introduced alongside the now-deleted proportional-floor tests
// (sp-05glh removed the counter-cyclical pct mode entirely); kept here as a generic
// log-capturing test double still used by the prepos-ceiling suite.
type propFloorCapturingLogger struct {
	mu      sync.Mutex
	entries []struct{ level, message string }
}

func (l *propFloorCapturingLogger) Log(level, message string, _ map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, struct{ level, message string }{level, message})
}

func (l *propFloorCapturingLogger) infoContains(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if e.level == "INFO" && strings.Contains(e.message, sub) {
			return true
		}
	}
	return false
}

func propFloorCtx(token string, logger *propFloorCapturingLogger) context.Context {
	return common.WithLogger(auth.WithPlayerToken(context.Background(), token), logger)
}
