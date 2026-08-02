package grpc

import (
	"strings"

	manufacturingDomain "github.com/andrescamacho/spacetraders-go/internal/domain/manufacturing"
)

type configFieldError struct {
	Fields []string
}

func (e *configFieldError) Error() string {
	return "missing or invalid " + strings.Join(e.Fields, ", ")
}

type configReader struct {
	values  map[string]interface{}
	invalid []string
}

func newConfigReader(values map[string]interface{}) *configReader {
	return &configReader{values: values}
}

func (r *configReader) fail(key string) {
	r.invalid = append(r.invalid, key)
}

func (r *configReader) Err() error {
	if len(r.invalid) == 0 {
		return nil
	}
	return &configFieldError{Fields: r.invalid}
}

func (r *configReader) RequiredString(key string) string {
	value, ok := r.values[key].(string)
	if !ok {
		r.fail(key)
	}
	return value
}

func (r *configReader) RequiredNonEmptyString(key string) string {
	value, ok := r.values[key].(string)
	if !ok || value == "" {
		r.fail(key)
		return ""
	}
	return value
}

func (r *configReader) OptionalString(key string) string {
	value, _ := r.values[key].(string)
	return value
}

func (r *configReader) OptionalStringDefault(key, fallback string) string {
	if value, ok := r.values[key].(string); ok && value != "" {
		return value
	}
	return fallback
}

func (r *configReader) RequiredInt(key string) int {
	value, ok := intValue(r.values[key])
	if !ok {
		r.fail(key)
	}
	return value
}

// PresentInt reads an int value and reports whether the key was present and
// valid — for genuinely optional numeric knobs whose ABSENCE means something
// (RefuelShip's nil-units = full tank), where OptionalInt's fallback would
// erase the present-vs-absent distinction.
func (r *configReader) PresentInt(key string) (int, bool) {
	return intValue(r.values[key])
}

func (r *configReader) OptionalInt(key string, fallback int) int {
	value, ok := intValue(r.values[key])
	if !ok {
		return fallback
	}
	return value
}

// PresentOrFailInt reads a numeric knob that, WHEN THE KEY IS PRESENT, MUST parse — a
// present-but-unparseable value is a hard build failure (RULINGS #4 fail-closed) rather
// than a silent fallback to the caller's default. Absent → fallback (a genuinely omitted
// knob still defers to the coordinator's own default). For a money guard a failed build
// (no tour, no buy — the hull is released cleanly) is correct: a tour must never spend
// beneath a floor it could not determine.
func (r *configReader) PresentOrFailInt(key string, fallback int) int {
	raw, present := r.values[key]
	if !present {
		return fallback
	}
	value, ok := intValue(raw)
	if !ok {
		r.fail(key)
		return fallback
	}
	return value
}

// OptionalFloat reads a float config value, returning fallback when the key is
// absent or non-numeric. JSON numbers round-trip through float64, and an int is
// accepted too.
func (r *configReader) OptionalFloat(key string, fallback float64) float64 {
	value, ok := floatValue(r.values[key])
	if !ok {
		return fallback
	}
	return value
}

func (r *configReader) OptionalBool(key string) bool {
	value, _ := r.values[key].(bool)
	return value
}

// PresentBool reads a bool knob and reports whether the key was present, for a
// genuinely optional bool whose ABSENCE means "defer to a default" that is not
// simply false — OptionalBool would collapse "unset" into false and silently
// disable a default-on behaviour.
func (r *configReader) PresentBool(key string) (bool, bool) {
	value, ok := r.values[key].(bool)
	return value, ok
}

func (r *configReader) RequiredStringSlice(key string, aliases ...string) []string {
	if value, ok := stringSliceValue(r.values[key]); ok {
		return value
	}
	for _, alias := range aliases {
		if value, ok := stringSliceValue(r.values[alias]); ok {
			return value
		}
	}
	r.fail(key)
	return nil
}

// OptionalStringSlice reads a string-slice config value with no required default
// (opt-in knobs disabled entirely when absent). Unlike RequiredStringSlice, a
// missing or wrong-typed key is not a validation failure - it simply returns nil.
func (r *configReader) OptionalStringSlice(key string, aliases ...string) []string {
	if value, ok := stringSliceValue(r.values[key]); ok {
		return value
	}
	for _, alias := range aliases {
		if value, ok := stringSliceValue(r.values[alias]); ok {
			return value
		}
	}
	return nil
}

// OptionalGoodGatingOverrides reads the per-good buy-gating override map from a launch
// config key holding the map's JSON encoding (GoodGatingOverrides.Encode). A missing, non-string,
// or malformed value yields nil (no overrides) — the guard-tightening default that keeps every
// good on the global gates, matching the lenient Optional* family. This is a PER-LAUNCH key (not a
// global config.yaml knob and not in manufacturingConfigKeys), so it persists in the container
// config as a JSON string and reloads on restart untouched (RULINGS #2).
func (r *configReader) OptionalGoodGatingOverrides(key string) manufacturingDomain.GoodGatingOverrides {
	raw, ok := r.values[key].(string)
	if !ok || raw == "" {
		return nil
	}
	overrides, err := manufacturingDomain.DecodeGoodGatingOverrides(raw)
	if err != nil {
		return nil
	}
	return overrides
}

// intValue coerces a config value to int. It MUST handle every numeric type a launch
// config can carry on EITHER build path: float64 (the JSON-recovery path — persisted
// numbers round-trip through float64) AND the native int/int64 the daemon stores on the
// fresh-start/coordinator-launch path (buildCommandForType is called directly on the
// in-memory map before any JSON round-trip). int is 64-bit on the daemon's target so the
// int64→int narrowing never overflows a credit value.
func intValue(raw interface{}) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case int32:
		return int(v), true
	case float64:
		return int(v), true
	}
	return 0, false
}

func floatValue(raw interface{}) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	}
	return 0, false
}

func stringSliceValue(raw interface{}) ([]string, bool) {
	switch v := raw.(type) {
	case []string:
		return v, true
	case []interface{}:
		out := make([]string, len(v))
		for i, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out[i] = s
		}
		return out, true
	}
	return nil, false
}
