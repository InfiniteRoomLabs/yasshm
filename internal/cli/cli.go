// Package cli implements the yasshm command line: argument dispatch and the
// subcommands, with the writers and arguments passed in so it is testable.
package cli

import (
	"fmt"
	"io"
)

// Build metadata, injected at build time with -ldflags -X.
var (
	// Version is the release version, or "dev" for an unstamped build.
	Version = "dev"
	// Commit is the source commit the binary was built from.
	Commit = "unknown"
	// Date is the build date in RFC 3339 form.
	Date = "unknown"
)

// Exit codes returned by Run.
const (
	exitOK    = 0
	exitError = 1
	exitUsage = 2
)

const usage = `usage: yasshm <command>

commands:
  version   print the version, commit and build date
  help      print this message (also -h, --help)
`

// Run executes the command named by args (without the program name), writing
// normal output to stdout and diagnostics to stderr. It returns the process
// exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "help", "-h", "--help":
		if _, err := fmt.Fprint(stdout, usage); err != nil {
			return writeFailed(stderr, err)
		}
		return exitOK
	case "version":
		if len(args) > 1 {
			_, _ = fmt.Fprintf(stderr, "yasshm: version takes no arguments\n\n%s", usage)
			return exitUsage
		}
		if _, err := fmt.Fprintf(stdout, "yasshm %s\ncommit %s\nbuilt %s\n", Version, Commit, Date); err != nil {
			return writeFailed(stderr, err)
		}
		return exitOK
	default:
		_, _ = fmt.Fprintf(stderr, "yasshm: unknown command %s\n\n%s", quoteBounded(args[0]), usage)
		return exitUsage
	}
}

// maxEcho bounds how much of a hostile argument is echoed back.
const maxEcho = 64

// quoteBounded quotes s with %q after truncating it to maxEcho bytes.
func quoteBounded(s string) string {
	if len(s) > maxEcho {
		return fmt.Sprintf("%q...", s[:maxEcho])
	}
	return fmt.Sprintf("%q", s)
}

func writeFailed(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintf(stderr, "yasshm: %v\n", err)
	return exitError
}
