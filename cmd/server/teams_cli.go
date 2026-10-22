package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// errUnknownTeamsSubcommand marks the one argument error that also deserves a
// usage dump, so the dispatcher can tell it apart from runtime failures.
var errUnknownTeamsSubcommand = errors.New("unknown teams subcommand")

// runTeamsCLI dispatches the teams command. It is the only place that exits the
// process; everything below returns an error so the logic stays testable
// without forking.
func runTeamsCLI(args []string) {
	if err := runTeams(args, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		if errors.Is(err, errUnknownTeamsSubcommand) {
			teamsUsage(os.Stderr)
		}
		os.Exit(1)
	}
}

// runTeams is the whole command surface. An empty argument list is the list
// command by design: `heron teams` alone lists, and `heron teams list` is
// rejected rather than aliased so the command count stays at four usages.
func runTeams(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return listTeams(".", stdout)
	}
	switch args[0] {
	case "add":
		return runTeamsAdd(args[1:], stdout, stderr)
	case "remove":
		return runTeamsRemove(".", args[1:], stdout)
	case "export":
		return runTeamsExport(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("%w: %s", errUnknownTeamsSubcommand, args[0])
	}
}

// runTeamsAdd parses add flags. Only --dry-run has behaviour this phase; the
// rest are accepted so scripts written against the final CLI do not fail on
// unknown flags, but they are ignored rather than half-implemented.
func runTeamsAdd(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("teams add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dryRun := fs.Bool("dry-run", false, "Parse and preview the package without writing anything")
	yes := fs.Bool("yes", false, "Skip script review confirmation")
	as := fs.String("as", "", "Override the install scope (default: manifest name)")
	refresh := fs.Bool("refresh", false, "Ignore the download cache")
	fs.Usage = func() { teamsUsage(stderr) }

	// Go flag stops parsing at the first positional argument, but every usage
	// documented for this command puts flags after <source> (teams add ./pkg
	// --dry-run). Pull the flags out first so both orders work.
	positional, flags := []string{}, []string{}
	// Value-taking flags are skipped together with their value, otherwise the
	// value of "--as other" would be mistaken for the source.
	valueFlags := map[string]bool{"-as": true, "--as": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		if valueFlags[a] && i+1 < len(args) {
			flags = append(flags, a, args[i+1])
			i++
			continue
		}
		flags = append(flags, a)
	}
	if err := fs.Parse(flags); err != nil {
		return err
	}

	if len(positional) == 0 {
		return errors.New("teams add: missing <source>")
	}
	if len(positional) > 1 {
		return fmt.Errorf("teams add: expected exactly one <source>, got %d", len(positional))
	}
	source := positional[0]

	// Both the dry run and the real install go through one pipeline: a dry run
	// is the same walk with the final write skipped, so the preview can never
	// drift from what an install would actually do.
	return runTeamsAddFromInputs(".", teamsAddInputs{
		Source:  source,
		DryRun:  *dryRun,
		Yes:     *yes,
		Scope:   *as,
		Refresh: *refresh,
		Stdout:  stdout,
		Stderr:  stderr,
		Stdin:   os.Stdin,
	})
}

func teamsUsage(w io.Writer) {
	lines := []string{
		"Usage:",
		"  heron teams                          List teams",
		"  heron teams add <source> [flags]     Install a team package",
		"  heron teams remove <name> [flags]    Remove an installed package",
		"  heron teams export <team> [flags]    Export a team as a package",
		"",
		"Flags for add:",
		"  --dry-run    Parse and preview the package without writing anything",
		"  --yes        Skip script review confirmation",
		"  --as <name>  Override the install scope",
		"  --refresh    Re-download instead of using the cache",
		"",
		"Flags for remove:",
		"  --keep-agents  Keep agents/, skills/ and rules/; delete only teams/",
	}
	_, _ = fmt.Fprintln(w, strings.Join(lines, "\n"))
}
