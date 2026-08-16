package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/benchmarkevidence"
)

var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "collect":
		return runCollect(args[1:], stdout, stderr)
	case "version":
		return writeOutput(stdout, stderr, map[string]string{"version": version, "commit": commit})
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "gha-benchmark: unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runCollect(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("collect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repository := flags.String("repository", "NDDev-OpenNetwork/github-actions", "exact private owner/name repository")
	runID := flags.Int64("run-id", 0, "completed representative benchmark workflow run ID")
	timeout := flags.Duration("timeout", 2*time.Minute, "bounded GitHub API and artifact collection timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *runID <= 0 || *timeout <= 0 || *timeout > 5*time.Minute {
		fmt.Fprintln(stderr, "gha-benchmark: collect requires --run-id and a timeout in (0,5m]")
		return 2
	}
	token, err := githubToken(os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "gha-benchmark: %v\n", err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	evidence, err := (benchmarkevidence.Collector{Token: token}).Collect(ctx, benchmarkevidence.Options{
		Repository: *repository,
		RunID:      *runID,
	})
	if err != nil {
		fmt.Fprintf(stderr, "gha-benchmark: collect: %v\n", err)
		return 1
	}
	return writeOutput(stdout, stderr, evidence)
}

func githubToken(getenv func(string) string) (string, error) {
	rawGHToken := getenv("GH_TOKEN")
	rawGitHubToken := getenv("GITHUB_TOKEN")
	if rawGHToken != strings.TrimSpace(rawGHToken) || rawGitHubToken != strings.TrimSpace(rawGitHubToken) {
		return "", fmt.Errorf("GitHub token environment values must not contain surrounding whitespace")
	}
	ghToken := rawGHToken
	githubToken := rawGitHubToken
	if ghToken != "" && githubToken != "" && ghToken != githubToken {
		return "", fmt.Errorf("GH_TOKEN and GITHUB_TOKEN disagree")
	}
	if ghToken != "" {
		return ghToken, nil
	}
	if githubToken != "" {
		return githubToken, nil
	}
	return "", fmt.Errorf("GH_TOKEN or GITHUB_TOKEN with Actions read permission is required")
}

func writeOutput(stdout, stderr io.Writer, value any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintf(stderr, "gha-benchmark: encode output: %v\n", err)
		return 1
	}
	return 0
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: gha-benchmark <collect|version>")
}
