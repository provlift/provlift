// Command provlift discovers and upgrades Terraform provider dependencies.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/provlift/provlift/internal/cli"
)

// Stamped at build time via -ldflags.
var (
	buildVersion = "dev"
	buildCommit  = "none"
	buildDate    = "unknown"
)

func main() {
	// A cancellable context means Ctrl-C stops a long `terraform init` instead
	// of orphaning it.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	info, _ := debug.ReadBuildInfo()
	root := cli.NewRootCommand(cli.BuildInfo{
		Version: versionFromBuildInfo(buildVersion, info),
		Commit:  buildCommit,
		Date:    buildDate,
	})

	if err := cli.ExecuteContext(ctx, root, os.Args[1:]); err != nil {
		var exit *cli.ExitCodeError
		if errors.As(err, &exit) {
			if exit.Err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", exit.Err)
			}
			os.Exit(exit.Code)
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(cli.ExitError)
	}
}

func versionFromBuildInfo(stamped string, info *debug.BuildInfo) string {
	if stamped == "dev" && info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return stamped
}
