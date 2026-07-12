// OpenAPI response-shape validation against the VENDORED real-API spec (gobot/api/openapi.json,
// pinned SpaceTraders 2.3.0). This is the twin analogue of the daemon's openapi_contract_test.go:
// the daemon validates its OUTBOUND request payloads against this spec; the twin validates its
// OUTBOUND responses against the SAME spec, closing the contract loop. A twin response that omits a
// required field or drifts a type fails here even when the Go client happens to tolerate it today.
import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import Ajv, { type ValidateFunction } from 'ajv';
import addFormats from 'ajv-formats';

const HERE = path.dirname(fileURLToPath(import.meta.url)); // twin/tests/helpers
const SPEC_PATH = path.resolve(HERE, '..', '..', '..', 'gobot', 'api', 'openapi.json');
const spec = JSON.parse(readFileSync(SPEC_PATH, 'utf8')) as {
  paths: Record<string, Record<string, { responses: Record<string, { content?: Record<string, { schema?: unknown }> }> }>>;
};

// strict:false — OpenAPI 3.0 carries keywords ajv's strict mode rejects (nullable, example, xml).
// validateFormats stays ON (ajv-formats) so date-time drift is caught. The whole spec is registered
// under the "openapi" id so response schemas resolve their #/components/schemas/* $refs.
const ajv = new Ajv({ strict: false, allErrors: true });
addFormats(ajv);
ajv.addSchema(spec, 'openapi');

const cache = new Map<string, ValidateFunction>();

/** Compile (and cache) the validator for one operation's response body schema. `templatePath` is the
 *  OpenAPI path template (e.g. "/my/ships/{shipSymbol}/purchase"), NOT the concrete request path. */
function validatorFor(method: string, templatePath: string, status: string): ValidateFunction {
  const key = `${method} ${templatePath} ${status}`;
  const hit = cache.get(key);
  if (hit) return hit;
  const op = spec.paths[templatePath]?.[method.toLowerCase()];
  if (!op) throw new Error(`openapi: no operation ${method} ${templatePath}`);
  const schema = op.responses?.[status]?.content?.['application/json']?.schema;
  if (!schema) throw new Error(`openapi: no ${status} JSON response schema for ${method} ${templatePath}`);
  // Re-anchor $refs: the schema fragment lives inside the "openapi" doc, so wrap it so relative
  // #/components refs resolve against the registered spec.
  const fn = ajv.compile({ $id: `resp:${key}`, $ref: 'openapi#/paths/' + encodeRef(templatePath) + `/${method.toLowerCase()}/responses/${status}/content/${encodeRef('application/json')}/schema` });
  cache.set(key, fn);
  return fn;
}

/** JSON-Pointer-encode a path segment (~ -> ~0, / -> ~1) so it can sit inside a $ref pointer. */
function encodeRef(seg: string): string {
  return seg.replace(/~/g, '~0').replace(/\//g, '~1');
}

export interface ShapeResult { valid: boolean; errors: string[] }

/** Validate a response body against the spec's response schema for (method, templatePath, status).
 *  Returns {valid, errors[]} — errors are compact "instancePath: message (params)" strings. */
export function validateResponse(method: string, templatePath: string, status: string, body: unknown): ShapeResult {
  const fn = validatorFor(method, templatePath, status);
  const valid = fn(body) as boolean;
  const errors = (fn.errors ?? []).map(
    (e) => `${e.instancePath || '(root)'} ${e.message}${e.params && Object.keys(e.params).length ? ' ' + JSON.stringify(e.params) : ''}`,
  );
  return { valid, errors };
}
