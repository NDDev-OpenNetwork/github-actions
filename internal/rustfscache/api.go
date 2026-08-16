package rustfscache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const maximumResponseBytes = 1024 * 1024

type Credential struct {
	AccessKey string
	SecretKey []byte
}

type Response struct {
	StatusCode int
	Body       []byte
}

type Requester interface {
	Do(context.Context, Credential, string, string, string, []byte) (Response, error)
}

type HTTPRequester struct {
	Endpoint string
	Region   string
	Client   *http.Client
	Now      func() time.Time
}

func NewHTTPRequester(config Config) (*HTTPRequester, error) {
	caPEM, err := os.ReadFile(config.CAFile)
	if err != nil {
		return nil, fmt.Errorf("read RustFS CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("RustFS CA file does not contain a valid certificate")
	}
	transport := &http.Transport{
		Proxy:               nil,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		DisableCompression:  true,
		MaxIdleConns:        8,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 5 * time.Second,
	}
	return &HTTPRequester{
		Endpoint: config.Endpoint,
		Region:   config.Region,
		Client: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (r *HTTPRequester) Do(
	ctx context.Context,
	credential Credential,
	method, requestPath, contentType string,
	body []byte,
) (Response, error) {
	if r == nil || r.Client == nil {
		return Response{}, fmt.Errorf("RustFS HTTP requester is not initialized")
	}
	base, err := url.Parse(r.Endpoint)
	if err != nil {
		return Response{}, fmt.Errorf("parse RustFS endpoint: %w", err)
	}
	relative, err := url.Parse(requestPath)
	if err != nil || relative.IsAbs() || !strings.HasPrefix(relative.Path, "/") {
		return Response{}, fmt.Errorf("RustFS request path must be an absolute-path reference")
	}
	target := base.ResolveReference(relative)
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("build RustFS request: %w", err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	payload := sha256.Sum256(body)
	payloadHex := hex.EncodeToString(payload[:])
	request.Header.Set("X-Amz-Content-Sha256", payloadHex)
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	secret := string(credential.SecretKey)
	if err := awsv4.NewSigner().SignHTTP(
		ctx,
		aws.Credentials{AccessKeyID: credential.AccessKey, SecretAccessKey: secret},
		request,
		payloadHex,
		"s3",
		r.Region,
		now,
	); err != nil {
		return Response{}, fmt.Errorf("sign RustFS request: %w", err)
	}
	secret = ""
	response, err := r.Client.Do(request)
	if err != nil {
		return Response{}, fmt.Errorf("execute RustFS request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maximumResponseBytes+1))
	if err != nil {
		return Response{}, fmt.Errorf("read RustFS response: %w", err)
	}
	if len(responseBody) > maximumResponseBytes {
		return Response{}, fmt.Errorf("RustFS response exceeded %d bytes", maximumResponseBytes)
	}
	return Response{StatusCode: response.StatusCode, Body: responseBody}, nil
}
