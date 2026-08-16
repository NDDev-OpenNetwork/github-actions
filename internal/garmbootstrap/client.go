package garmbootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const maxAPIResponseBytes = 2 << 20

type apiClient struct {
	baseURL string
	client  *http.Client
}

func newAPIClient(baseURL string, client *http.Client) (apiClient, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return apiClient{}, fmt.Errorf("parse GARM URL: %w", err)
	}
	host := parsed.Hostname()
	if parsed.Scheme != "http" || host != "127.0.0.1" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return apiClient{}, fmt.Errorf("GARM URL must be an uncredentialed 127.0.0.1 HTTP URL")
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/api/v1"
	}
	if path.Clean(parsed.Path) != "/api/v1" {
		return apiClient{}, fmt.Errorf("GARM URL path must be /api/v1")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	localClient := *client
	localClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return apiClient{baseURL: strings.TrimRight(parsed.String(), "/"), client: &localClient}, nil
}

func (c apiClient) login(ctx context.Context, credentials adminCredentials) (string, error) {
	request := struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}{Username: credentials.Username, Password: credentials.Password}
	var response struct {
		Token string `json:"token"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/auth/login", "", request, &response); err != nil {
		return "", fmt.Errorf("authenticate to GARM: %w", err)
	}
	if response.Token == "" || strings.ContainsAny(response.Token, "\r\n") {
		return "", fmt.Errorf("GARM returned an invalid bearer token")
	}
	return response.Token, nil
}

func (c apiClient) doJSON(ctx context.Context, method, endpoint, token string, input, output any) error {
	var body io.Reader
	var encoded []byte
	if input != nil {
		var err error
		encoded, err = json.Marshal(input)
		if err != nil {
			return fmt.Errorf("encode GARM request: %w", err)
		}
		defer clear(encoded)
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+endpoint, body)
	if err != nil {
		return fmt.Errorf("create GARM request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("call GARM API: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxAPIResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read GARM response: %w", err)
	}
	if len(data) > maxAPIResponseBytes {
		clear(data)
		return fmt.Errorf("GARM response exceeds %d bytes", maxAPIResponseBytes)
	}
	defer clear(data)
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("GARM API returned HTTP %d for %s %s", response.StatusCode, method, endpoint)
	}
	if output == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("decode GARM response: %w", err)
	}
	return nil
}
