package config

import (
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// THE DEAD-SURFACE AUDIT. A config key with no reader is not inert — it is worse than inert. Viper
// unmarshals NON-STRICTLY, so a key with no matching struct field is dropped in silence: the file
// says the knob is set, the daemon never sees it, and the next engineer reads the file and reasons
// about a live path that does not exist. That is not hypothetical here. config.staging.yaml carried
//
//	fleet_autosizer:
//	  contract_delivery_hulls_enabled: true
//
// for eighteen days after the struct field behind it was deleted, and two beads were written
// against the live contract-scaling path it implied.
//
// The audit pairs with the duty guard in internal/adapters/grpc: that one stops two engines owning
// one duty, this one stops the SURFACE of a retired engine outliving it. Both are the same rule —
// a replacement is not delivered until its predecessor is gone.
//
// WHAT IT CANNOT SEE, stated plainly so nobody reads more safety into it than it provides:
//   - A key that HAS a struct field but whose field nothing reads. That needs a call-graph walk;
//     this audit only proves the key can reach the struct at all.
//   - gobot/config.yaml (prod). It is gitignored, so CI has no copy to audit. The two files here
//     are the committed ones: the staging config a daemon actually loads, and the example an
//     operator copies to make a config.yaml.
//   - Values injected into container launch configs rather than read off Config — the tune
//     registry's own keys are covered by its drift tests, not by this.
//   - Whether a live key is ARMED. That is RULINGS #19's dormant-knob audit, a deploy-time
//     process over the arming ledger, and it is a different question from this one.

// configFilesUnderAudit are the committed config files. Paths are relative to this package.
var configFilesUnderAudit = []string{
	"../../../config.staging.yaml",
	"../../../config.yaml.example",
}

// knownDeadConfigKeys is the baseline: keys that were ALREADY dead when this audit was written
// (2026-08-08, sp-bu6jd). It exists so the audit could ship enforcing at all, and it is the same
// shape as the duty registry's overlap ledger — an exemption is dated, attributed, and kept honest
// by the guard itself: a key listed here that becomes live again FAILS, so the baseline cannot rot
// into the very dead surface it records.
//
// It is a DEBT LIST, not an allowance. Nothing may be added to it: a newly-dead key is a
// predecessor that outlived its reader, and the change that removed the reader removes the key.
//
// The three retired-subsystem sections are the point. config.yaml.example is what an operator
// copies to build a config.yaml, and it still offers [fleet_autosizer], [capacity_reconciler] and
// [manufacturing] as configurable subsystems — all three deleted from the code.
var knownDeadConfigKeys = map[string]string{
	// Retired subsystems still advertised by the operator template. Tracked for deletion.
	"fleet_autosizer":     "coordinator deleted (sp-5pclx); the section survives in config.yaml.example",
	"capacity_reconciler": "stack deleted (sp-y2ptq); the section survives in config.yaml.example",
	"manufacturing":       "factory ops retired; the section survives in config.yaml.example",

	// [api] has never had a struct field. The rate limit is api.RateLimitPerSecond, a code
	// constant — so config.staging.yaml's "Raise to 2/10 once staging has its own account" and
	// deploy/staging/register.sh's reference to "config.staging.yaml's conservative
	// api.rate_limit" both describe a knob that does nothing.
	"api.base_url":            "no APIConfig field exists",
	"api.timeout":             "no APIConfig field exists",
	"api.rate_limit.requests": "no APIConfig field exists; the live limit is api.RateLimitPerSecond",
	"api.rate_limit.burst":    "no APIConfig field exists; the live limit is api.RateLimitPerSecond",
	"api.retry.max_attempts":  "no APIConfig field exists",
	"api.retry.backoff_base":  "no APIConfig field exists",

	// [logging] has never had a struct field either.
	"logging.level":                "no LoggingConfig field exists",
	"logging.format":               "no LoggingConfig field exists",
	"logging.output":               "no LoggingConfig field exists",
	"logging.file_path":            "no LoggingConfig field exists",
	"logging.include_caller":       "no LoggingConfig field exists",
	"logging.include_stacktrace":   "no LoggingConfig field exists",
	"logging.rotation.enabled":     "no LoggingConfig field exists",
	"logging.rotation.max_size":    "no LoggingConfig field exists",
	"logging.rotation.max_age":     "no LoggingConfig field exists",
	"logging.rotation.max_backups": "no LoggingConfig field exists",
	"logging.rotation.compress":    "no LoggingConfig field exists",

	// DaemonConfig carries socket_path/pid_file and the four tuning ints; the lifecycle keys
	// below have no field. deploy/staging/env.sh reads socket_path and pid_file only — the
	// isolation contract rests on live keys, these are decoration beside them.
	"daemon.address":                           "DaemonConfig has no Address field; the daemon serves on socket_path",
	"daemon.max_containers":                    "DaemonConfig has no such field",
	"daemon.health_check_interval":             "DaemonConfig has no such field",
	"daemon.shutdown_timeout":                  "DaemonConfig has no such field",
	"daemon.restart_policy.enabled":            "DaemonConfig has no RestartPolicy field",
	"daemon.restart_policy.max_attempts":       "DaemonConfig has no RestartPolicy field",
	"daemon.restart_policy.delay":              "DaemonConfig has no RestartPolicy field",
	"daemon.restart_policy.backoff_multiplier": "DaemonConfig has no RestartPolicy field",

	// RoutingConfig carries address only.
	"routing.timeout.connect":  "RoutingConfig has no Timeout field",
	"routing.timeout.dijkstra": "RoutingConfig has no Timeout field",
	"routing.timeout.tsp":      "RoutingConfig has no Timeout field",
	"routing.timeout.vrp":      "RoutingConfig has no Timeout field",

	// CaptainConfig carries enabled/thresholds/streams; the supervisor's operational keys below
	// have no field. The captain is a protected path — its retirement is its owner's call.
	"captain.claude_bin":                  "CaptainConfig has no such field",
	"captain.restart_cmd":                 "CaptainConfig has no such field",
	"captain.auto_merge":                  "CaptainConfig has no such field",
	"captain.max_fixes_per_day":           "CaptainConfig has no such field",
	"captain.max_features_per_day":        "CaptainConfig has no such field",
	"captain.max_feature_diff_lines":      "CaptainConfig has no such field",
	"captain.fix_session_timeout_minutes": "CaptainConfig has no such field",
}

// configKeyReachesStruct reports whether a dotted config key resolves to a field in the Config
// tree by mapstructure tag — i.e. whether viper's unmarshal has anywhere to put it.
//
// It stops descending at a map or slice (anything below is free-form data the field owns) and at
// time.Time (a struct viper fills from a scalar). A key with segments left over once the walk
// reaches a scalar is unreachable, which is the honest answer: `daemon.restart_policy.delay`
// cannot land anywhere if DaemonConfig has no RestartPolicy.
func configKeyReachesStruct(root reflect.Type, key string) bool {
	cur := root
	segments := strings.Split(key, ".")
	for i, segment := range segments {
		for cur.Kind() == reflect.Pointer {
			cur = cur.Elem()
		}
		if cur.Kind() == reflect.Map || cur.Kind() == reflect.Slice {
			return true
		}
		if cur == reflect.TypeOf(time.Time{}) {
			return i == len(segments)
		}
		if cur.Kind() != reflect.Struct {
			return false
		}
		field, ok := fieldByMapstructureTag(cur, segment)
		if !ok {
			return false
		}
		cur = field
	}
	return true
}

func fieldByMapstructureTag(t reflect.Type, name string) (reflect.Type, bool) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("mapstructure")
		if tag == "" {
			tag = strings.ToLower(f.Name)
		}
		if tag == name {
			return f.Type, true
		}
	}
	return nil, false
}

// loadConfigKeys reads a committed config file through VIPER — the same loader LoadConfig uses —
// so what this audit counts as "a key in the file" can never drift from what the daemon sees. The
// explicit config type is for config.yaml.example, whose extension viper does not recognise.
func loadConfigKeys(t *testing.T, path string) []string {
	t.Helper()
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadInConfig(), "config file under audit must parse: %s", path)
	keys := v.AllKeys()
	sort.Strings(keys)
	return keys
}

// THE AUDIT. Every key in every committed config file either reaches the Config struct or is a
// named, attributed entry in the debt list. A key that is neither is a knob nothing reads.
func TestConfigSurface_EveryCommittedKeyHasSomewhereToLand(t *testing.T) {
	for _, path := range configFilesUnderAudit {
		t.Run(path, func(t *testing.T) {
			var dead []string
			for _, key := range loadConfigKeys(t, path) {
				if configKeyReachesStruct(reflect.TypeOf(Config{}), key) {
					continue
				}
				if _, known := knownDeadConfigKeys[key]; known {
					continue
				}
				dead = append(dead, key)
			}
			require.Empty(t, dead,
				"%s sets keys the Config struct cannot receive — viper drops them in SILENCE, so the file "+
					"claims a knob the daemon never reads:\n  %s\nDelete the key in the change that deleted its "+
					"reader. Do not add it to knownDeadConfigKeys.", path, strings.Join(dead, "\n  "))
		})
	}
}

// ANTI-VACUITY. The audit is a loop over parsed files; an unreadable file, a viper that returns no
// keys, or a resolver that says yes to everything would all make it pass while checking nothing.
// These pin that it reads real files, finds real keys, and can still tell live from dead.
func TestConfigSurface_AuditActuallyReadsTheConfigFiles(t *testing.T) {
	for _, path := range configFilesUnderAudit {
		keys := loadConfigKeys(t, path)
		require.GreaterOrEqual(t, len(keys), 30, "%s should yield the whole file's keys, got %d", path, len(keys))

		live := 0
		for _, key := range keys {
			if configKeyReachesStruct(reflect.TypeOf(Config{}), key) {
				live++
			}
		}
		require.GreaterOrEqual(t, live, 15, "%s: only %d keys resolved — the resolver is not walking the struct", path, live)
		require.Less(t, live, len(keys), "%s: every key resolved, so the resolver never returns false here", path)
	}

	// The resolver's two answers, on keys whose status is not in doubt.
	require.True(t, configKeyReachesStruct(reflect.TypeOf(Config{}), "database.pool.max_open"),
		"a real nested key must resolve, or the audit's silence means nothing")
	require.False(t, configKeyReachesStruct(reflect.TypeOf(Config{}), "database.pool.no_such_knob"),
		"an invented key must not resolve, or the audit passes everything")
	require.False(t, configKeyReachesStruct(reflect.TypeOf(Config{}), "database.type.deeper"),
		"a path that runs past a scalar is unreachable, not reachable")
}

// RETRO-VALIDATION — the bead's instance 4, as it actually stood in config.staging.yaml between
// f861cd7c (which deleted FleetAutosizerConfig.ContractDeliveryHullsEnabled) and efe7758c
// (which finally deleted the key), against the struct as it is today, which for this key is the
// struct as it was the moment the reader went away.
//
// This is the catch point that matters: the audit does not fire when the key is written, it fires
// on the change that REMOVES ITS READER — which is exactly "a replacement is not delivered until
// its predecessor is deleted", enforced by the build.
func TestConfigSurface_RetroValidates_TheDeadAutosizerKey(t *testing.T) {
	historical := `
bootstrap:
  bootstrap_disabled: false
  tick_seconds: 45

fleet_autosizer:
  contract_delivery_hulls_enabled: true
`
	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(historical)))

	var dead []string
	for _, key := range v.AllKeys() {
		if !configKeyReachesStruct(reflect.TypeOf(Config{}), key) {
			dead = append(dead, key)
		}
	}

	require.Equal(t, []string{"fleet_autosizer.contract_delivery_hulls_enabled"}, dead,
		"the audit must name the dead autosizer key — and must NOT flag the live bootstrap keys beside it")
}

// The debt list is a record of what IS dead. An entry that has since been fixed is itself dead
// surface, and leaving it behind is how a ledger becomes the thing it was meant to prevent. This is
// the same self-cleaning rule the duty registry's overlap ledger carries.
func TestConfigSurface_DebtListHoldsNoResolvedEntries(t *testing.T) {
	var resolved []string
	for key, reason := range knownDeadConfigKeys {
		require.NotEmpty(t, strings.TrimSpace(reason), "%s must say WHY it is dead", key)
		if configKeyReachesStruct(reflect.TypeOf(Config{}), key) {
			resolved = append(resolved, key)
		}
	}
	sort.Strings(resolved)
	require.Empty(t, resolved,
		"these keys now reach the Config struct and are no longer dead — delete them from knownDeadConfigKeys:\n  %s",
		strings.Join(resolved, "\n  "))
}
