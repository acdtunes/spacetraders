package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The resolver drives config discovery when LoadConfig is called with an empty
// configPath (every CLI caller). These tests pin the resolution ORDER:
//   1. SPACETRADERS_CONFIG env override (file wins outright; dir searched first)
//   2. current working directory (legacy ".", "./configs", "/etc/spacetraders")
//   3. the executable's own directory and its parent
// so the CLI is runnable from any cwd while daemon/captain (cwd=gobot) stay
// bit-identical because "." is still consulted before the executable dirs.

func TestResolveConfigSearch_NoEnvNoExecDir_LegacyPathsOnly(t *testing.T) {
	got := resolveConfigSearch("", "")

	require.Empty(t, got.file)
	require.Equal(t, []string{".", "./configs", "/etc/spacetraders"}, got.paths)
}

func TestResolveConfigSearch_ExecDirAppendedAfterCwd(t *testing.T) {
	got := resolveConfigSearch("", "/opt/app/bin")

	require.Empty(t, got.file)
	// cwd paths must come first (so cwd=gobot keeps winning), then exec dir + parent.
	require.Equal(t, []string{".", "./configs", "/etc/spacetraders", "/opt/app/bin", "/opt/app"}, got.paths)
}

func TestResolveConfigSearch_EnvDirSearchedFirst(t *testing.T) {
	envDir := t.TempDir()

	got := resolveConfigSearch(envDir, "/opt/app/bin")

	require.Empty(t, got.file)
	require.Equal(t, []string{envDir, ".", "./configs", "/etc/spacetraders", "/opt/app/bin", "/opt/app"}, got.paths)
}

func TestResolveConfigSearch_EnvFileSetsExplicitFile(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "custom.yaml")
	require.NoError(t, os.WriteFile(envFile, []byte("database:\n  host: h\n"), 0o644))

	got := resolveConfigSearch(envFile, "/opt/app/bin")

	// An explicit file override is decisive: use it directly, ignore search paths.
	require.Equal(t, envFile, got.file)
	require.Empty(t, got.paths)
}

// --- end-to-end LoadConfig wiring ---

func TestLoadConfig_UsesCwdConfig_LegacyUnchanged(t *testing.T) {
	// Reproduces daemon/captain behavior: config.yaml sits in the cwd.
	t.Setenv("SPACETRADERS_CONFIG", "")
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("database:\n  host: fromcwd\n"), 0o644))
	t.Chdir(dir)

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Equal(t, "fromcwd", cfg.Database.Host)
}

func TestLoadConfig_EnvOverrideFileFromAnyCwd(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "override.yaml")
	require.NoError(t, os.WriteFile(cfgFile,
		[]byte("database:\n  host: fromenv\n"), 0o644))
	t.Setenv("SPACETRADERS_CONFIG", cfgFile)
	t.Chdir(t.TempDir()) // empty cwd — no config.yaml here

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Equal(t, "fromenv", cfg.Database.Host)
}

func TestLoadConfig_ExecutableDirFallback(t *testing.T) {
	// config.yaml lives next to the real binary (execRoot/config.yaml), mirroring
	// gobot/config.yaml sitting next to gobot/bin/spacetraders, while cwd is empty.
	t.Setenv("SPACETRADERS_CONFIG", "")
	execRoot := t.TempDir()
	binDir := filepath.Join(execRoot, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(execRoot, "config.yaml"),
		[]byte("database:\n  host: fromexecdir\n"), 0o644))
	fakeExe := filepath.Join(binDir, "spacetraders")
	require.NoError(t, os.WriteFile(fakeExe, []byte("#!/bin/sh\n"), 0o755))

	orig := osExecutable
	osExecutable = func() (string, error) { return fakeExe, nil }
	t.Cleanup(func() { osExecutable = orig })

	t.Chdir(t.TempDir()) // empty cwd — forces the executable-dir fallback

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Equal(t, "fromexecdir", cfg.Database.Host)
}

// sp-wj0h: the tour market-model artifact path resolves against the config file's dir
// so the tour executor reads it regardless of the daemon's cwd. Pins all four branches.
func TestResolveModelArtifactPath(t *testing.T) {
	cfgFile := filepath.Join("/etc", "spacetraders", "config.yaml")
	dir := filepath.Dir(cfgFile)

	// empty → config-dir-derived absolute default
	require.Equal(t,
		filepath.Join(dir, "services", "routing-service", "model_artifacts", "market_model.json"),
		resolveModelArtifactPath(cfgFile, ""))

	// relative → resolved against the config file's dir
	require.Equal(t, filepath.Join(dir, "custom", "model.json"),
		resolveModelArtifactPath(cfgFile, filepath.Join("custom", "model.json")))

	// absolute → unchanged
	abs := filepath.Join("/opt", "models", "m.json")
	require.Equal(t, abs, resolveModelArtifactPath(cfgFile, abs))

	// no config file (pure-env boot) → unchanged, so the coordinator falls back to its constant
	require.Equal(t, "", resolveModelArtifactPath("", ""))
	require.Equal(t, "rel/path.json", resolveModelArtifactPath("", "rel/path.json"))
}

// End-to-end: LoadConfig resolves an empty model_artifact_path to the config-dir
// absolute default (this is the exact path the deployed daemon feeds the tour executor).
func TestLoadConfig_ResolvesModelArtifactPathToConfigDirDefault(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile, []byte("database:\n  host: h\n"), 0o644))
	t.Setenv("SPACETRADERS_CONFIG", cfgFile)
	t.Chdir(t.TempDir())

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	want := filepath.Join(filepath.Dir(cfgFile), "services", "routing-service", "model_artifacts", "market_model.json")
	require.Equal(t, want, cfg.Routing.ModelArtifactPath)
	require.True(t, filepath.IsAbs(cfg.Routing.ModelArtifactPath))
}

// A relative model_artifact_path in the config resolves against the config file's dir.
func TestLoadConfig_ResolvesRelativeModelArtifactPathAgainstConfigDir(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfgFile,
		[]byte("routing:\n  model_artifact_path: alt/m.json\n"), 0o644))
	t.Setenv("SPACETRADERS_CONFIG", cfgFile)
	t.Chdir(t.TempDir())

	cfg, err := LoadConfig("")

	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "alt", "m.json"), cfg.Routing.ModelArtifactPath)
}
