// Package cli implements the yasshm command line: argument dispatch and the
// subcommands, with the writers and arguments passed in so it is testable.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/InfiniteRoomLabs/yasshm/internal/config"
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
  serve --config <file>          serve the MCP endpoint until SIGTERM or SIGINT
  config check --config <file>   validate a config file (exit 0 valid, 1 invalid)
  version                        print the version, commit and build date
  help                           print this message (also -h, --help)
`

const configUsage = `usage: yasshm config check --config <file>

Validates the config file, including its private key and known_hosts files.
Exit 0 when it is valid, 1 with one line per problem on stderr when it is
not, 2 for a usage error. It never connects anywhere and never writes.
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
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "config":
		return runConfig(args[1:], stdout, stderr)
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

// runConfig dispatches the config subcommands; only "check" exists.
func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "check" {
		if len(args) == 0 {
			_, _ = fmt.Fprintf(stderr, "yasshm: config needs a subcommand\n\n%s", configUsage)
		} else {
			_, _ = fmt.Fprintf(stderr, "yasshm: unknown config subcommand %s\n\n%s", quoteBounded(args[0]), configUsage)
		}
		return exitUsage
	}
	return runConfigCheck(args[1:], stdout, stderr)
}

// runConfigCheck loads the config named by --config. On success it prints
// one short line that names no host, user, key path, audience or team
// domain; on failure it prints the problem lines to stderr.
func runConfigCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	path := fs.String("config", "", "path of the config file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if _, werr := fmt.Fprint(stdout, configUsage); werr != nil {
				return writeFailed(stderr, werr)
			}
			return exitOK
		}
		_, _ = fmt.Fprintf(stderr, "yasshm: %s\n\n%s", boundedText(err.Error()), configUsage)
		return exitUsage
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "yasshm: unexpected argument %s\n\n%s", quoteBounded(fs.Arg(0)), configUsage)
		return exitUsage
	}
	if *path == "" {
		_, _ = fmt.Fprintf(stderr, "yasshm: config check needs --config <file>\n\n%s", configUsage)
		return exitUsage
	}
	cfg, err := config.Load(*path)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return exitError
	}
	if _, err := fmt.Fprintf(stdout, "config ok: %s, %s\n", plural(len(cfg.Tenants), "tenant"), plural(serverCount(cfg.Tenants), "server")); err != nil {
		return writeFailed(stderr, err)
	}
	return exitOK
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// boundedText truncates a message that may echo a hostile argument.
func boundedText(s string) string {
	if len(s) > 4*maxEcho {
		return s[:4*maxEcho] + "..."
	}
	return s
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
