package twinreport

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// capture is what the fake twin records for one received request. It is delivered
// over a channel so the assertions are race-clean under `go test -race`.
type capture struct {
	method string
	ct     string
	body   string
}

func newFakeTwin(t *testing.T) (*httptest.Server, <-chan capture) {
	t.Helper()
	ch := make(chan capture, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- capture{method: r.Method, ct: r.Header.Get("Content-Type"), body: string(b)}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

// With TWIN_REPORT_URL set, Report POSTs the expected JSON body.
func TestReportPostsJSONWhenURLSet(t *testing.T) {
	srv, ch := newFakeTwin(t)
	t.Setenv("TWIN_REPORT_URL", srv.URL)

	// detail present (the repurpose op carries the hauler symbol).
	Report("repurpose", map[string]any{"ship": "SHIP-1"})
	got := <-ch // Report is synchronous, so the request is already recorded.
	require.Equal(t, http.MethodPost, got.method)
	require.Equal(t, "application/json", got.ct)
	require.JSONEq(t, `{"call":"repurpose","detail":{"ship":"SHIP-1"}}`, got.body)

	// nil detail marshals to detail:null; the call name still flips the paired flag.
	Report("scout-assign", nil)
	got = <-ch
	require.JSONEq(t, `{"call":"scout-assign","detail":null}`, got.body)
}

// With TWIN_REPORT_URL unset (empty), Report makes no request — the production no-op.
func TestReportNoRequestWhenURLUnset(t *testing.T) {
	_, ch := newFakeTwin(t)
	t.Setenv("TWIN_REPORT_URL", "") // override any ambient value to empty for this test

	Report("scout-assign", nil)

	select {
	case c := <-ch:
		t.Fatalf("expected no request when TWIN_REPORT_URL is unset, but the twin received %+v", c)
	default:
	}
}
