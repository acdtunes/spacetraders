package database

import (
	"strings"
	"testing"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/config"
)

// TestBuildPostgresDSN_URLStyle_NoExistingParams pins sp-6g96 Fix 3: a
// URL-style DSN (cfg.URL / DATABASE_URL, e.g. Heroku/Railway convention) with
// no existing query string must gain the exec-mode param via "?".
func TestBuildPostgresDSN_URLStyle_NoExistingParams(t *testing.T) {
	cfg := &config.DatabaseConfig{
		Type: "postgres",
		URL:  "postgres://user:pass@localhost:5432/mydb",
	}

	dsn := buildPostgresDSN(cfg)

	if !strings.Contains(dsn, "?default_query_exec_mode=cache_describe") {
		t.Fatalf("expected DSN to gain ?default_query_exec_mode=cache_describe, got %q", dsn)
	}
	if !strings.HasPrefix(dsn, cfg.URL) {
		t.Fatalf("expected original URL to be preserved as a prefix, got %q", dsn)
	}
}

// TestBuildPostgresDSN_URLStyle_ExistingParams covers a URL DSN that already
// carries a query string (e.g. ?sslmode=require): the new param must be
// appended with "&", not clobber or duplicate the "?".
func TestBuildPostgresDSN_URLStyle_ExistingParams(t *testing.T) {
	cfg := &config.DatabaseConfig{
		Type: "postgres",
		URL:  "postgres://user:pass@localhost:5432/mydb?sslmode=require",
	}

	dsn := buildPostgresDSN(cfg)

	if !strings.Contains(dsn, "&default_query_exec_mode=cache_describe") {
		t.Fatalf("expected DSN to gain &default_query_exec_mode=cache_describe, got %q", dsn)
	}
	if !strings.Contains(dsn, "sslmode=require") {
		t.Fatalf("expected existing sslmode=require param to be preserved, got %q", dsn)
	}
	if strings.Count(dsn, "?") != 1 {
		t.Fatalf("expected exactly one '?' in the DSN, got %q", dsn)
	}
}

// TestBuildPostgresDSN_FieldsStyle_KeywordValue covers the fields-based DSN
// this package builds itself (always libpq keyword/value style, never a URL):
// the param must be appended as a space-separated keyword=value pair, and all
// original fields must still be present.
func TestBuildPostgresDSN_FieldsStyle_KeywordValue(t *testing.T) {
	cfg := &config.DatabaseConfig{
		Type:     "postgres",
		Host:     "db.internal",
		Port:     5432,
		User:     "captain",
		Password: "secret",
		Name:     "spacetraders",
		SSLMode:  "disable",
	}

	dsn := buildPostgresDSN(cfg)

	for _, want := range []string{
		"host=db.internal", "port=5432", "user=captain", "password=secret",
		"dbname=spacetraders", "sslmode=disable",
	} {
		if !strings.Contains(dsn, want) {
			t.Fatalf("expected DSN to retain %q, got %q", want, dsn)
		}
	}
	if !strings.Contains(dsn, " default_query_exec_mode=cache_describe") {
		t.Fatalf("expected DSN to gain a space-separated default_query_exec_mode=cache_describe, got %q", dsn)
	}
}

// Note: NewConnection itself is not exercised here against a live/fake
// postgres host. gorm.Open performs an automatic Ping() as part of Open()
// unless gorm.Config.DisableAutomaticPing is set (which NewConnection doesn't
// set), so — unlike the dialector's own Initialize(), which is pure DSN
// parsing — NewConnection("postgres", ...) always dials. Confirmed
// experimentally: pointing it at a nonexistent host surfaces a real DNS
// resolution error rather than returning a lazily-unconnected *gorm.DB. That
// makes a real Postgres server a precondition for testing NewConnection's
// postgres path end-to-end, which this package deliberately avoids elsewhere
// too (see NewTestConnection: sqlite-only). buildPostgresDSN is where
// Fix 3's actual logic lives and is fully covered above without needing any
// network; NewConnection's postgres case is a one-line call to it
// (postgres.Open(buildPostgresDSN(cfg))), correct by inspection.
