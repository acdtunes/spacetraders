// Package buildinfo exposes the git commit and build time stamped into a binary
// at link time, so a running process can answer "which build am I?" from its own
// startup banner — without a stamp, a merged fix is invisible if the live binary
// predates it.
//
// The values are injected via -ldflags "-X" from the Makefile (see build-cli /
// build-daemon / build-watchkeeper). BuildTime is a value passed at build time,
// never time.Now() at runtime — the point is to freeze the moment the binary was
// linked, not to report when it happened to start.
package buildinfo

import "fmt"

// shortCommitLen is how many hex characters of the commit sha the banners show.
// Twelve is unambiguous for any realistic repo while staying readable in a log.
const shortCommitLen = 12

// These are overwritten at link time via
//
//	-ldflags "-X github.com/andrescamacho/spacetraders-go/internal/infrastructure/buildinfo.Commit=<sha> ..."
//
// The defaults are deliberate, non-empty sentinels so a binary built without the
// stamp (go run, go test, a plain `go build`) is still self-describing instead of
// printing blanks.
var (
	Commit    = "unknown"
	BuildTime = "unknown"
	Version   = "dev"
)

// Info is a snapshot of the build stamp. It is a plain value so callers (and
// tests) can format arbitrary stamps without touching the package-level vars.
type Info struct {
	Commit    string
	BuildTime string
	Version   string
}

// Get returns the build stamp linked into this binary.
func Get() Info {
	return Info{Commit: Commit, BuildTime: BuildTime, Version: Version}
}

// ShortCommit returns the commit truncated to shortCommitLen characters. Values
// already shorter than that (an abbreviated sha, or the "unknown" sentinel) are
// returned unchanged.
func (i Info) ShortCommit() string {
	if len(i.Commit) > shortCommitLen {
		return i.Commit[:shortCommitLen]
	}
	return i.Commit
}

// Short renders a one-line stamp: "<version> (commit <short>, built <time>)".
func (i Info) Short() string {
	return fmt.Sprintf("%s (commit %s, built %s)", i.Version, i.ShortCommit(), i.BuildTime)
}

// Banner renders a startup line prefixed with the component name, e.g.
// "Watchkeeper v1.2.3 (commit abcdef012345, built 2026-07-07T12:00:00Z)".
// Emitting this at startup makes the live binary's commit greppable in the
// process log, which is what the deploy assertion (make assert-live-stamp) keys
// off of.
func (i Info) Banner(component string) string {
	return fmt.Sprintf("%s %s", component, i.Short())
}

// String implements fmt.Stringer as the Short form.
func (i Info) String() string {
	return i.Short()
}
