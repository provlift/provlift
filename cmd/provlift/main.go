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
	commit, date := commitAndDateFromBuildInfo(buildCommit, buildDate, info)
	root := cli.NewRootCommand(cli.BuildInfo{
		Version: versionFromBuildInfo(buildVersion, info),
		Commit:  commit,
		Date:    date,
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

// commitAndDateFromBuildInfo returns the commit and build date to report. Values
// stamped with -ldflags win. Otherwise they come from the VCS settings Go records
// in a build from a clone: vcs.revision shortened to 12 characters (as in a Go
// pseudo-version), with -dirty when vcs.modified is true, and vcs.time. A build
// with neither, such as `go install ...@vX.Y.Z`, gets empty strings rather than
// placeholders.
func commitAndDateFromBuildInfo(stampedCommit, stampedDate string, info *debug.BuildInfo) (commit, date string) {
	var revision, modified, vcsTime string
	if info != nil {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				revision = setting.Value
			case "vcs.modified":
				modified = setting.Value
			case "vcs.time":
				vcsTime = setting.Value
			}
		}
	}
	commit, date = stampedCommit, stampedDate
	if commit == "none" {
		commit = ""
		if revision != "" {
			commit = revision
			if len(commit) > 12 {
				commit = commit[:12]
			}
			if modified == "true" {
				commit += "-dirty"
			}
		}
	}
	if date == "unknown" {
		date = vcsTime
	}
	return commit, date
}
