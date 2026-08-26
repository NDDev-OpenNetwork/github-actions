package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/NDDev-OpenNetwork/github-actions/internal/garmapptoken"
)

type garmConfig struct {
	Database struct {
		Passphrase string `toml:"passphrase"`
	} `toml:"database"`
}

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gha-garm-app-token", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("garm-config", "", "absolute GARM config path")
	endpointValue := flags.String("github-endpoint", "https://api.github.com", "GitHub API endpoint")
	if err := flags.Parse(arguments); err != nil || *configPath == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: gha-garm-app-token --garm-config PATH [--github-endpoint URL]")
		return 2
	}
	var configuration garmConfig
	if _, err := toml.DecodeFile(*configPath, &configuration); err != nil || len(configuration.Database.Passphrase) != 32 {
		fmt.Fprintln(stderr, "GARM database passphrase configuration is invalid")
		return 1
	}
	endpoint, err := url.Parse(*endpointValue)
	if err != nil {
		fmt.Fprintln(stderr, "GitHub endpoint is invalid")
		return 1
	}
	token, err := (garmapptoken.Minter{
		Endpoint: endpoint, HTTP: &http.Client{Timeout: 20 * time.Second}, Now: time.Now,
		Passphrase: []byte(configuration.Database.Passphrase),
	}).Mint(context.Background(), stdin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, token.Value)
	return 0
}
