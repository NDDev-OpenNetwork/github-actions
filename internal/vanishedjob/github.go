package vanishedjob

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type GitHubClient struct {
	Endpoint *url.URL
	Token    string
	HTTP     *http.Client
}

func (client GitHubClient) ForceCancel(ctx context.Context, repository string, runID int64) error {
	status, err := client.post(ctx, repository, runID, "force-cancel")
	if err != nil {
		return err
	}
	// A 409 means the run crossed terminal concurrently with the request. The
	// next authoritative observation advances to rerun; repeating cancellation
	// cannot improve that state.
	if status != http.StatusAccepted && status != http.StatusConflict {
		return fmt.Errorf("force-cancel workflow run returned HTTP %d", status)
	}
	return nil
}

func (client GitHubClient) FullRerun(ctx context.Context, repository string, runID int64) error {
	status, err := client.post(ctx, repository, runID, "rerun")
	if err != nil {
		return err
	}
	if status != http.StatusCreated {
		return fmt.Errorf("rerun workflow returned HTTP %d", status)
	}
	return nil
}

func (client GitHubClient) post(ctx context.Context, repository string, runID int64, operation string) (int, error) {
	parts := strings.Split(repository, "/")
	if client.Endpoint == nil || client.Endpoint.Scheme != "https" && client.Endpoint.Scheme != "http" || len(parts) != 2 || parts[0] == "" || parts[1] == "" || runID <= 0 || client.Token == "" || client.HTTP == nil {
		return 0, fmt.Errorf("GitHub vanished-runner client is incomplete")
	}
	endpoint := *client.Endpoint
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/") + "/repos/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/actions/runs/" + strconv.FormatInt(runID, 10) + "/" + operation
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+client.Token)
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	response, err := client.HTTP.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	return response.StatusCode, nil
}
