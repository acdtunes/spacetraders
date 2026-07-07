package cli

import (
	"fmt"
	"runtime"

	"github.com/andrescamacho/spacetraders-go/internal/infrastructure/buildinfo"
	"github.com/spf13/cobra"
)

// NewVersionCommand prints the build stamp linked into this CLI binary. Unlike
// the daemon/watchkeeper banners (emitted to their logs at startup), a CLI runs
// per-invocation, so the stamp lives behind an explicit `version` subcommand
// (and the `--version` flag wired on the root) rather than printing on every
// command. This answers "which spacetraders binary am I running?" — the same
// stale-binary question the startup banners answer for the long-lived services
// (sp-898q).
//
// It does not talk to the daemon: build info is baked into the binary, so the
// command works even when the daemon is down.
func NewVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI build stamp (version, commit, build time)",
		Run: func(cmd *cobra.Command, args []string) {
			info := buildinfo.Get()
			fmt.Printf("spacetraders %s\n", info.Version)
			fmt.Printf("  commit:  %s\n", info.Commit)
			fmt.Printf("  built:   %s\n", info.BuildTime)
			fmt.Printf("  go:      %s\n", runtime.Version())
			fmt.Printf("  os/arch: %s/%s\n", runtime.GOOS, runtime.GOARCH)
		},
	}
}
