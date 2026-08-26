package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/vanishedjob"
)

type config struct {
	StateFile      string             `json:"state_file"`
	LockFile       string             `json:"lock_file"`
	TokenFile      string             `json:"token_file"`
	GitHubEndpoint string             `json:"github_endpoint"`
	Policy         vanishedjob.Policy `json:"policy"`
}

type jsonEvents struct{ encoder *json.Encoder }

func (sink jsonEvents) Emit(_ context.Context, event vanishedjob.Event) error {
	return sink.encoder.Encode(event)
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gha-vanished-job-recovery", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "", "absolute recovery config path")
	jobPath := flags.String("job", "", "absolute observed job JSON path")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if *configPath == "" || *jobPath == "" || flags.NArg() != 1 || flags.Arg(0) != "plan" && flags.Arg(0) != "apply" {
		fmt.Fprintln(stderr, "usage: gha-vanished-job-recovery --config PATH --job PATH <plan|apply>")
		return 2
	}
	configuration, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	job, err := loadJob(*jobPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	store := vanishedjob.FileStore{Path: configuration.StateFile, LockPath: configuration.LockFile}
	existing, err := store.Get(vanishedjob.RecordKey(job.Repository, job.RunID, job.RunAttempt))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if existing == nil {
		existing, _, err = store.ForRun(job.Repository, job.RunID)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if flags.Arg(0) == "plan" {
		decision, err := vanishedjob.Evaluate(configuration.Policy, job, existing, time.Now().UTC())
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := json.NewEncoder(stdout).Encode(decision); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	token, err := readSecret(configuration.TokenFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	endpoint, _ := url.Parse(configuration.GitHubEndpoint)
	controller := vanishedjob.Controller{
		Policy: configuration.Policy, Store: store,
		Client: vanishedjob.GitHubClient{Endpoint: endpoint, Token: token, HTTP: &http.Client{Timeout: 20 * time.Second}},
		Events: jsonEvents{json.NewEncoder(stdout)}, Now: time.Now,
	}
	if _, err := controller.Reconcile(context.Background(), job); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func loadConfig(path string) (config, error) {
	if !boundedAbsolute(path) {
		return config{}, fmt.Errorf("recovery config path must be absolute and bounded")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return config{}, err
	}
	var configuration config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configuration); err != nil {
		return config{}, err
	}
	endpoint, endpointErr := url.Parse(configuration.GitHubEndpoint)
	if !boundedAbsolute(configuration.StateFile) || !boundedAbsolute(configuration.LockFile) || !boundedAbsolute(configuration.TokenFile) || endpointErr != nil || endpoint.Scheme != "https" && endpoint.Scheme != "http" || endpoint.Host == "" {
		return config{}, fmt.Errorf("recovery config paths or endpoint are invalid")
	}
	if err := configuration.Policy.Validate(); err != nil {
		return config{}, err
	}
	return configuration, nil
}

func loadJob(path string) (vanishedjob.Job, error) {
	if !boundedAbsolute(path) {
		return vanishedjob.Job{}, fmt.Errorf("job observation path must be absolute and bounded")
	}
	file, err := os.Open(path)
	if err != nil {
		return vanishedjob.Job{}, err
	}
	defer file.Close()
	var job vanishedjob.Job
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&job); err != nil {
		return vanishedjob.Job{}, err
	}
	return job, nil
}

func readSecret(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("token file must be a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return "", fmt.Errorf("token file must contain one non-empty line")
	}
	return token, nil
}

func boundedAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) != string(filepath.Separator)
}
