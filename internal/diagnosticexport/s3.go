package diagnosticexport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"golang.org/x/sys/unix"
)

const (
	maxCredentialBytes = 512
	maxCABytes         = 1024 * 1024
)

var secretPattern = regexp.MustCompile(`^[A-Za-z0-9_+/=-]{32,256}$`)

type S3Store struct {
	client *s3.Client
}

func NewS3Store(config Config) (*S3Store, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	accessKey, err := readCredential(config.AccessKeyFile, maxCredentialBytes)
	if err != nil {
		return nil, fmt.Errorf("read RustFS access-key credential: %w", err)
	}
	secretKey, err := readCredential(config.SecretKeyFile, maxCredentialBytes)
	if err != nil {
		return nil, fmt.Errorf("read RustFS secret-key credential: %w", err)
	}
	if !validIdentity(accessKey) || !secretPattern.MatchString(secretKey) {
		return nil, errors.New("RustFS exporter credential format is invalid")
	}
	caPEM, err := readCredentialBytes(config.CAFile, maxCABytes)
	if err != nil {
		return nil, fmt.Errorf("read RustFS CA credential: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("RustFS CA credential contains no certificate")
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: time.Duration(config.RequestTimeoutSeconds) * time.Second,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS13,
			RootCAs:    roots,
		},
	}
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(config.RequestTimeoutSeconds) * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("RustFS exporter refuses HTTP redirects")
		},
	}
	provider := credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")
	client := s3.New(s3.Options{
		BaseEndpoint:     aws.String(strings.TrimSuffix(config.Endpoint, "/")),
		Credentials:      aws.NewCredentialsCache(provider),
		Region:           config.Region,
		UsePathStyle:     config.PathStyle,
		HTTPClient:       httpClient,
		RetryMaxAttempts: 3,
		RetryMode:        aws.RetryModeStandard,
		AppID:            "nddev-diagnostic-exporter",
	})
	return &S3Store{client: client}, nil
}

func (s *S3Store) Head(ctx context.Context, bucket, key string) (RemoteObject, error) {
	output, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if s3ErrorCode(err, "NotFound", "NoSuchKey", "404") {
			return RemoteObject{}, nil
		}
		return RemoteObject{}, fmt.Errorf("head RustFS diagnostic object: %w", err)
	}
	return RemoteObject{
		Exists:        true,
		Bytes:         aws.ToInt64(output.ContentLength),
		SHA256:        output.Metadata["sha256"],
		SchemaVersion: output.Metadata["diagnostic-schema"],
	}, nil
}

func (s *S3Store) Put(ctx context.Context, bucket string, bundle Bundle) error {
	digest, err := hexDigest(bundle.SHA256)
	if err != nil {
		return err
	}
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:         aws.String(bucket),
		Key:            aws.String(bundle.ObjectKey),
		Body:           bytes.NewReader(bundle.Content),
		ContentLength:  aws.Int64(int64(len(bundle.Content))),
		ContentType:    aws.String("application/gzip"),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(digest)),
		IfNoneMatch:    aws.String("*"),
		Metadata: map[string]string{
			"sha256":            bundle.SHA256,
			"diagnostic-schema": fmt.Sprint(bundle.Manifest.SchemaVersion),
			"source-name":       bundle.Name,
		},
	})
	if err != nil {
		if s3ErrorCode(err, "PreconditionFailed", "ConditionalRequestConflict", "412", "409") {
			return ErrRemoteObjectExists
		}
		return fmt.Errorf("put RustFS diagnostic object: %w", err)
	}
	return nil
}

func readCredential(filename string, maximum int64) (string, error) {
	content, err := readCredentialBytes(filename, maximum)
	if err != nil {
		return "", err
	}
	value := strings.TrimSuffix(string(content), "\n")
	value = strings.TrimSuffix(value, "\r")
	if value == "" || strings.TrimSpace(value) != value {
		return "", errors.New("credential is empty or has surrounding whitespace")
	}
	return value, nil
}

func readCredentialBytes(filename string, maximum int64) ([]byte, error) {
	if !filepath.IsAbs(filename) || maximum < 1 {
		return nil, errors.New("credential path or size boundary is invalid")
	}
	fd, err := unix.Open(filename, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filename)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("invalid credential file descriptor")
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Mode&0o077 != 0 ||
		stat.Uid != uint32(os.Geteuid()) || stat.Size < 1 || stat.Size > maximum {
		return nil, errors.New("credential type, link count, mode or size is unsafe")
	}
	content, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) != stat.Size {
		return nil, errors.New("credential changed while reading")
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, err
	}
	if stat.Dev != after.Dev || stat.Ino != after.Ino || stat.Size != after.Size ||
		stat.Mode != after.Mode || stat.Uid != after.Uid || stat.Gid != after.Gid ||
		stat.Mtim != after.Mtim || stat.Ctim != after.Ctim || after.Nlink != 1 {
		return nil, errors.New("credential changed while reading")
	}
	return content, nil
}

func s3ErrorCode(err error, allowed ...string) bool {
	var apiError smithy.APIError
	code := ""
	if errors.As(err, &apiError) {
		code = apiError.ErrorCode()
	}
	var responseError *smithyhttp.ResponseError
	status := ""
	if errors.As(err, &responseError) {
		status = fmt.Sprint(responseError.HTTPStatusCode())
	}
	for _, candidate := range allowed {
		if code == candidate || status == candidate {
			return true
		}
	}
	return false
}

func hexDigest(value string) ([]byte, error) {
	if len(value) != sha256.Size*2 {
		return nil, errors.New("diagnostic SHA-256 is invalid")
	}
	result, err := hex.DecodeString(value)
	if err != nil || len(result) != sha256.Size {
		return nil, errors.New("diagnostic SHA-256 is invalid")
	}
	return result, nil
}
