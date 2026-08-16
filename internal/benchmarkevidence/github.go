package benchmarkevidence

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

type runResponse struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Event      string    `json:"event"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	HeadSHA    string    `json:"head_sha"`
	RunAttempt int64     `json:"run_attempt"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	HTMLURL    string    `json:"html_url"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type jobsResponse struct {
	TotalCount int           `json:"total_count"`
	Jobs       []jobResponse `json:"jobs"`
}

type jobResponse struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Status      string         `json:"status"`
	Conclusion  string         `json:"conclusion"`
	RunnerName  string         `json:"runner_name"`
	Labels      []string       `json:"labels"`
	StartedAt   time.Time      `json:"started_at"`
	CompletedAt time.Time      `json:"completed_at"`
	Steps       []stepResponse `json:"steps"`
}

type stepResponse struct {
	Number      int       `json:"number"`
	Name        string    `json:"name"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
}

type artifactsResponse struct {
	TotalCount int                `json:"total_count"`
	Artifacts  []artifactResponse `json:"artifacts"`
}

type artifactResponse struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	SizeInBytes int64     `json:"size_in_bytes"`
	Digest      string    `json:"digest"`
	Expired     bool      `json:"expired"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	WorkflowRun struct {
		ID      int64  `json:"id"`
		HeadSHA string `json:"head_sha"`
	} `json:"workflow_run"`
}

func (c Collector) getJSON(ctx context.Context, client *http.Client, baseURL *url.URL, token, relative string, destination any) error {
	requestURL, err := resolveAPIURL(baseURL, relative)
	if err != nil {
		return err
	}
	request, err := newAPIRequest(ctx, requestURL, token)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return responseError(response)
	}
	raw, err := readBounded(response.Body, maximumJSONBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return fmt.Errorf("decode GitHub response: %w", err)
	}
	return nil
}

func (c Collector) downloadArtifact(ctx context.Context, client *http.Client, baseURL *url.URL, token, repository string, artifact artifactResponse) ([]byte, error) {
	if artifact.ID <= 0 || artifact.Name == "" || artifact.SizeInBytes <= 0 || artifact.SizeInBytes > maximumArchiveBytes || artifact.Expired {
		return nil, fmt.Errorf("artifact metadata is invalid or expired")
	}
	if !digestPattern.MatchString(artifact.Digest) || artifact.CreatedAt.IsZero() || !artifact.ExpiresAt.After(artifact.CreatedAt) {
		return nil, fmt.Errorf("artifact integrity or retention metadata is invalid")
	}
	if artifact.WorkflowRun.ID <= 0 || !shaPattern.MatchString(artifact.WorkflowRun.HeadSHA) {
		return nil, fmt.Errorf("artifact workflow identity is invalid")
	}

	requestURL, err := resolveAPIURL(baseURL, apiPath(repository, "actions", "artifacts", artifact.ID, "zip"))
	if err != nil {
		return nil, err
	}
	request, err := newAPIRequest(ctx, requestURL, token)
	if err != nil {
		return nil, err
	}
	noRedirectClient := *client
	noRedirectClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := noRedirectClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound {
		return nil, responseError(response)
	}
	location, err := response.Location()
	if err != nil {
		return nil, fmt.Errorf("parse artifact redirect: %w", err)
	}
	if location.User != nil || location.Fragment != "" || location.Host == "" || location.Scheme != baseURL.Scheme {
		return nil, fmt.Errorf("artifact redirect crossed an invalid transport boundary")
	}

	downloadRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
	if err != nil {
		return nil, err
	}
	downloadRequest.Header.Set("User-Agent", "nddev-gha-benchmark-evidence/1")
	downloadClient := *client
	downloadClient.CheckRedirect = func(next *http.Request, previous []*http.Request) error {
		if len(previous) >= maximumRedirects {
			return fmt.Errorf("artifact download exceeded redirect limit")
		}
		if next.URL.Scheme != location.Scheme || next.URL.Host != location.Host {
			return fmt.Errorf("artifact download redirect changed origin")
		}
		next.Header.Del("Authorization")
		return nil
	}
	downloadResponse, err := downloadClient.Do(downloadRequest)
	if err != nil {
		return nil, err
	}
	defer downloadResponse.Body.Close()
	if downloadResponse.StatusCode != http.StatusOK {
		return nil, responseError(downloadResponse)
	}
	archive, err := readBounded(downloadResponse.Body, maximumArchiveBytes)
	if err != nil {
		return nil, err
	}
	if int64(len(archive)) != artifact.SizeInBytes {
		return nil, fmt.Errorf("artifact size is %d, expected %d", len(archive), artifact.SizeInBytes)
	}
	digest := sha256.Sum256(archive)
	if "sha256:"+hex.EncodeToString(digest[:]) != artifact.Digest {
		return nil, fmt.Errorf("artifact digest mismatch")
	}
	return archive, nil
}

func decodeRecord(archive []byte) (BenchmarkRecord, error) {
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return BenchmarkRecord{}, err
	}
	if len(reader.File) != 1 {
		return BenchmarkRecord{}, fmt.Errorf("artifact archive must contain exactly one file")
	}
	file := reader.File[0]
	if file.Name != "result.json" || path.Clean(file.Name) != file.Name || !file.Mode().IsRegular() || file.UncompressedSize64 > maximumRecordBytes {
		return BenchmarkRecord{}, fmt.Errorf("artifact archive contains an invalid record entry")
	}
	stream, err := file.Open()
	if err != nil {
		return BenchmarkRecord{}, err
	}
	defer stream.Close()
	raw, err := readBounded(stream, maximumRecordBytes)
	if err != nil {
		return BenchmarkRecord{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var record BenchmarkRecord
	if err := decoder.Decode(&record); err != nil {
		return BenchmarkRecord{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return BenchmarkRecord{}, fmt.Errorf("record contains trailing JSON")
	}
	return record, nil
}

func newAPIRequest(ctx context.Context, requestURL *url.URL, token string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "nddev-gha-benchmark-evidence/1")
	request.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	return request, nil
}

func resolveAPIURL(baseURL *url.URL, relative string) (*url.URL, error) {
	reference, err := url.Parse(relative)
	if err != nil || !strings.HasPrefix(reference.Path, "/repos/") || reference.IsAbs() || reference.Host != "" {
		return nil, fmt.Errorf("invalid GitHub API path")
	}
	resolved := baseURL.ResolveReference(reference)
	if resolved.Scheme != baseURL.Scheme || resolved.Host != baseURL.Host {
		return nil, fmt.Errorf("GitHub API path changed origin")
	}
	return resolved, nil
}

func apiPath(repository string, components ...any) string {
	owner, name, _ := strings.Cut(repository, "/")
	parts := []string{"repos", url.PathEscape(owner), url.PathEscape(name)}
	for _, component := range components {
		parts = append(parts, url.PathEscape(fmt.Sprint(component)))
	}
	return "/" + strings.Join(parts, "/")
}

func readBounded(reader io.Reader, maximum int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maximum {
		return nil, fmt.Errorf("response exceeds %s bytes", strconv.FormatInt(maximum, 10))
	}
	return raw, nil
}

func responseError(response *http.Response) error {
	raw, _ := readBounded(response.Body, 4<<10)
	message := strings.TrimSpace(string(raw))
	if message == "" {
		message = http.StatusText(response.StatusCode)
	}
	return fmt.Errorf("GitHub returned HTTP %d: %s", response.StatusCode, message)
}
