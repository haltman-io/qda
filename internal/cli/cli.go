// Package cli implements the qda command line: subcommands run, api, db,
// generate, init-config, version and help.
package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"qda/internal/banner"
	"qda/internal/version"
)

// Execute routes os.Args[1:] to the right subcommand. It returns the
// process exit code.
func Execute(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		printRootHelp(stderr)
		return 2
	}

	command := args[0]
	rest := args[1:]

	switch command {
	case "run", "scan":
		return runCmd(rest, stdout, stderr)
	case "api", "server", "serve":
		return apiCmd(rest, stdout, stderr)
	case "db", "query":
		return dbCmd(rest, stdout, stderr)
	case "generate", "gen":
		return generateCmd(rest, stdout, stderr)
	case "init-config":
		return initConfigCmd(rest, stdout, stderr)
	case "version", "--version", "-version":
		fmt.Fprintf(stdout, "qda %s\n", version.Version)
		return 0
	case "help", "--help", "-help", "-h":
		printRootHelp(stderr)
		return 0
	default:
		// Backwards compatibility: `qda words.txt -config qda.toml` behaves
		// like `qda run words.txt -config qda.toml`.
		if strings.HasPrefix(command, "-") || fileExists(command) {
			return runCmd(args, stdout, stderr)
		}
		fmt.Fprintf(stderr, "unknown command: %s\n\n", command)
		printRootHelp(stderr)
		return 2
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// helpColor is true when out is a TTY and NO_COLOR is unset.
func helpColor(out io.Writer) bool {
	f, ok := out.(*os.File)
	if !ok {
		return false
	}
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func printRootHelp(out io.Writer) {
	fmt.Fprintf(out, `%s
Massive domain availability checking with RDAP, registrar fallbacks,
a local database, resume support, notifications and an HTTP API mode.

USAGE
  qda <command> [options]

COMMANDS
  run          Scan words/domains for availability (default command)
  api          Start the HTTP API server mode
  db           Query the local results database
  generate     Generate wordlists/domain lists on demand
  init-config  Write a sample qda.toml configuration
  version      Print version
  help         Show this help

EXAMPLES
  qda run wl-top-10.txt -config qda.toml
  qda run -l words.txt -tld net,org,com -concurrency 8
  qda run -d bug.net
  cat words.txt | qda run -tld net
  qda run -resume
  qda db -available
  qda db -expiring-in 30
  qda api -listen 127.0.0.1:8080
  qda generate -len 3 -charset alnum -o combinacoes_3.txt

Run "qda <command> -h" for command-specific options.
`, banner.Text(helpColor(out)))
}
