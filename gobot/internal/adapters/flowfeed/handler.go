// gobot/internal/adapters/flowfeed/handler.go
package flowfeed

import (
	"encoding/json"
	"net/http"
)

// NewFlowsHandler returns the read-only GET /api/flows handler backed by reg. It
// serializes a registry snapshot and never mutates state (RULINGS #4). It shares
// the metrics listener's trust boundary — no auth is added here.
func NewFlowsHandler(reg *Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(reg.Snapshot()); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
}
