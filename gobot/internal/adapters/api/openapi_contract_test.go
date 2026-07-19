package api

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/stretchr/testify/require"
)

// This file is the gate-hardening check (c): payload-vs-OpenAPI
// validation of the outbound SpaceTraders request bodies the client actually
// emits. It closes the last of the three real-API-contract mismatch classes the
// gate-hardening epic targeted - (a) unregistered handlers and (b) real
// error-shape tests already shipped; nothing before this caught the
// class where an outbound payload violates the API contract (a field rename or
// type drift) until the captain's LIVE acceptance rejected it in production.
//
// This exact defect shipped once: JumpShip sent a "systemSymbol" key where
// the API required "waypointSymbol". Unit tests against fakes can never catch it
// because the fake accepts whatever the client sends; only validating the real
// serialized bytes against the published schema can.
//
// Design (mirrors main_test.go's check (a): a real assertion plus a "has teeth"
// proof, so a green primary means "validated", not "matched nothing"):
//   - Point a real SpaceTradersClient at an httptest server via the injectable
//     baseURL (NewSpaceTradersClientWithConfig) - NO production code change and
//     NO re-declared payloads: we validate the exact JSON doWithRetry marshals.
//   - Drive every payload-bearing write method with schema-valid inputs; the
//     server records the real method+path+body it received.
//   - Validate each captured request against api/openapi.json (vendored, pinned
//     2.3.0) using kin-openapi's router + openapi3filter, which resolves the
//     concrete path to its operation and checks the body against the schema,
//     naming the offending endpoint+field on failure.
//
// Empty-body POSTs (orbit/dock/refuel/extract) are intentionally out of scope:
// they carry no fields to drift. This check targets the mis-keyed-payload class.

// specServerBase is the single server URL declared in the vendored spec. Captured
// paths are server-relative (the test client's baseURL is the httptest server,
// which has no /v2 prefix), so we re-anchor them here for the router to match.
const specServerBase = "https://api.spacetraders.io/v2"

// specPath resolves api/openapi.json relative to this test file, so the check is
// independent of the working directory `go test` runs in (mirrors gatePaths in
// cmd/spacetraders-daemon/main_test.go and migrationsDir in the persistence
// schema-drift test).
func specPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// thisFile = <gobot>/internal/adapters/api/openapi_contract_test.go
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "api", "openapi.json")
}

// loadContractRouter loads and validates the vendored spec and builds a router
// from it. Any failure here (spec missing, invalid, or zero paths) fails loudly
// rather than letting the primary test pass vacuously against an empty router.
func loadContractRouter(t *testing.T) routers.Router {
	t.Helper()
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false // the bundle is self-contained (#/ refs only)
	doc, err := loader.LoadFromFile(specPath(t))
	require.NoError(t, err, "load vendored OpenAPI spec at %s", specPath(t))
	require.NoError(t, doc.Validate(loader.Context), "vendored OpenAPI spec must be valid")
	require.NotEmpty(t, doc.Paths.Map(), "vendored spec has zero paths - wrong or truncated file")
	router, err := gorillamux.NewRouter(doc)
	require.NoError(t, err, "build router from vendored spec")
	return router
}

// validateOutboundBody re-anchors a captured server-relative request to the
// spec's server, resolves it to a spec operation, and validates the body against
// that operation's requestBody schema. A non-nil error names the endpoint+field.
// Security is skipped (NoopAuthenticationFunc): we validate the payload contract,
// not auth.
func validateOutboundBody(ctx context.Context, router routers.Router, method, serverRelPath string, body []byte) error {
	req, err := http.NewRequest(method, specServerBase+serverRelPath, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	route, pathParams, err := router.FindRoute(req)
	if err != nil {
		return fmt.Errorf("no matching operation in spec for %s %s: %w", method, serverRelPath, err)
	}

	return openapi3filter.ValidateRequest(ctx, &openapi3filter.RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
		Options: &openapi3filter.Options{
			AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
		},
	})
}

// contractCapture is one recorded outbound request.
type contractCapture struct {
	method string
	path   string // server-relative (may include query)
	body   []byte
}

// newRecordingClient returns a real client wired to an httptest server that
// records every request and answers 200 {"data":{}} so the driven methods reach
// the send (the capture) without retrying. The 200 short-circuits doWithRetry's
// retry loop; each driver call therefore produces exactly one captured request.
func newRecordingClient(t *testing.T) (client *SpaceTradersClient, captured *[]contractCapture, closeFn func()) {
	t.Helper()
	var mu sync.Mutex
	var recs []contractCapture

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		recs = append(recs, contractCapture{method: r.Method, path: r.URL.RequestURI(), body: b})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))

	// baseURL = server, maxRetries small, tiny backoff, nil clock -> RealClock.
	c := NewSpaceTradersClientWithConfig(srv.URL, 2, time.Millisecond, nil)
	return c, &recs, srv.Close
}

// driveIgnoringPanic runs a client call for its side effect (the outbound send)
// only. The request is captured inside doWithRetry BEFORE the response is parsed,
// so any panic/error from parsing our deliberately-minimal {"data":{}} stub is
// irrelevant to what we validate and is swallowed here.
func driveIgnoringPanic(fn func()) {
	defer func() { _ = recover() }()
	fn()
}

// TestOpenAPIContract_ClientPayloadsMatchSpec is the primary gate: the JSON the
// client actually serializes for every payload-bearing write must satisfy the
// vendored OpenAPI contract. A field rename or type drift here fails the suite -
// and therefore the captain-gate - instead of only failing in LIVE acceptance.
func TestOpenAPIContract_ClientPayloadsMatchSpec(t *testing.T) {
	router := loadContractRouter(t)
	client, captured, closeSrv := newRecordingClient(t)
	defer closeSrv()

	ctx := context.Background()
	const tok = "test-token"

	// Every payload-bearing outbound the client can make, driven with
	// schema-valid inputs (valid ShipType / ShipNavFlightMode enum values,
	// integer units) so any validation failure is a real client-side
	// field-name/type/shape drift, never a bad test value.
	drivers := []func(){
		func() { _, _ = client.NavigateShip(ctx, "SHIP-1", "X1-AB12-C34", tok) },
		func() { _ = client.SetFlightMode(ctx, "SHIP-1", "CRUISE", tok) },
		func() { _, _ = client.JumpShip(ctx, "SHIP-1", "X1-AB12-C34", tok) },
		func() { _, _ = client.PurchaseCargo(ctx, "SHIP-1", "IRON_ORE", 5, tok) },
		func() { _, _ = client.SellCargo(ctx, "SHIP-1", "IRON_ORE", 5, tok) },
		func() { _ = client.JettisonCargo(ctx, "SHIP-1", "IRON_ORE", 5, tok) },
		func() { _, _ = client.InstallShipModule(ctx, "SHIP-1", "MODULE_CARGO_HOLD_III", tok) },
		func() { _, _ = client.RemoveShipModule(ctx, "SHIP-1", "MODULE_CARGO_HOLD_III", tok) },
		func() { _, _ = client.TransferCargo(ctx, "SHIP-1", "SHIP-2", "IRON_ORE", 5, tok) },
		func() { _, _ = client.PurchaseShip(ctx, "SHIP_MINING_DRONE", "X1-AB12-C34", tok) },
		func() { _, _ = client.DeliverContract(ctx, "CONTRACT-1", "SHIP-1", "IRON_ORE", 5, tok) },
		func() { _, _ = client.SupplyConstruction(ctx, "SHIP-1", "X1-AB12-C34", "IRON_ORE", 5, tok) },
		func() { _, _ = client.Register(ctx, "account-token", "AGENT1", "COSMIC") },
	}
	for _, d := range drivers {
		driveIgnoringPanic(d)
	}

	// Anti-vacuous guard: if a driver silently stopped sending, an empty capture
	// set would make the loop below pass trivially. Require every driver landed.
	require.GreaterOrEqualf(t, len(*captured), len(drivers),
		"expected >= %d captured outbound requests, got %d - a driver method stopped sending (green would be vacuous)",
		len(drivers), len(*captured))

	var violations []string
	for _, rec := range *captured {
		if err := validateOutboundBody(ctx, router, rec.method, rec.path, rec.body); err != nil {
			violations = append(violations, fmt.Sprintf("%s %s\n    body:  %s\n    error: %v",
				rec.method, rec.path, string(rec.body), err))
		}
	}
	sort.Strings(violations)
	require.Emptyf(t, violations, "current client payloads violate the vendored OpenAPI contract (fix the client, not the spec):\n\n%s",
		strings.Join(violations, "\n\n"))
}

// TestOpenAPIContract_CatchesMisKeyedPayload is the acceptance proof for
// the "deliberately mis-keyed payload" requirement, exercising the SAME
// validation path the primary test uses. It reproduces the exact defect
// (navigate sent "systemSymbol" instead of the required "waypointSymbol") and
// asserts the check catches it and names the missing field - so a green primary
// test provably means the client's payloads were really validated.
func TestOpenAPIContract_CatchesMisKeyedPayload(t *testing.T) {
	router := loadContractRouter(t)

	err := validateOutboundBody(context.Background(), router, "POST",
		"/my/ships/SHIP-1/navigate", []byte(`{"systemSymbol":"X1-AB12"}`))

	require.Error(t, err, "a navigate payload missing the required waypointSymbol must fail validation")
	require.Containsf(t, err.Error(), "waypointSymbol",
		"failure must name the offending field so the fix is obvious; got: %v", err)
}

// TestOpenAPIContract_CatchesWrongFieldType proves the check also catches a type
// drift (not just a missing/renamed field): purchase requires an integer "units";
// a string must be rejected, naming the field.
func TestOpenAPIContract_CatchesWrongFieldType(t *testing.T) {
	router := loadContractRouter(t)

	err := validateOutboundBody(context.Background(), router, "POST",
		"/my/ships/SHIP-1/purchase", []byte(`{"symbol":"IRON_ORE","units":"five"}`))

	require.Error(t, err, "a purchase payload with a string units must fail validation")
	require.Containsf(t, err.Error(), "units", "failure must name the offending field; got: %v", err)
}

// TestOpenAPIContract_SpecValidNavigatePasses proves the router resolves a known
// operation and accepts a spec-valid payload - confirming the primary test's
// green is "the payloads matched real routes", not "the router matched nothing".
func TestOpenAPIContract_SpecValidNavigatePasses(t *testing.T) {
	router := loadContractRouter(t)

	err := validateOutboundBody(context.Background(), router, "POST",
		"/my/ships/SHIP-1/navigate", []byte(`{"waypointSymbol":"X1-AB12-C34"}`))

	require.NoError(t, err, "a spec-valid navigate payload must pass validation")
}
