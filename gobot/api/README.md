# Vendored SpaceTraders OpenAPI spec

`openapi.json` is the official SpaceTraders API OpenAPI 3.0 description, vendored
into the repo so the outbound-payload contract test
(`internal/adapters/api/openapi_contract_test.go`, sp-wj8f) can validate the
bytes our API client actually sends against the published schema **at test time**
— before a field rename or type drift ships and only fails against the live API.

## Pinned version

| Field | Value |
|-------|-------|
| `info.version` | **2.3.0** |
| `openapi` | 3.0.1 |
| Fetched | 2026-07-09 |
| Paths | 55 |

## Source & refresh process

The file is the fully-dereferenced ("optimizedBundle") export from Stoplight, so
it is a single self-contained document with only internal `#/components/...`
`$ref`s (no external file refs) — loadable directly by `kin-openapi`.

To refresh (the live API drifts over time; re-vendor when the client targets a
newer contract):

```bash
curl -sL -o gobot/api/openapi.json \
  "https://stoplight.io/api/v1/projects/spacetraders/spacetraders/nodes/reference/SpaceTraders.json?fromExportButton=true&snapshotType=http_service&deref=optimizedBundle"
```

Then update the "Pinned version" table above from `info.version` and re-run:

```bash
go test ./internal/adapters/api/ -run OpenAPIContract -count=1
```

If the refresh makes the contract test fail, the client's outbound payloads no
longer match the published schema — fix the client (or, if the spec itself
changed shape, the test's driver list), do not silence the check.
