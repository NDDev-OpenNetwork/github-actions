package garmapptoken

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	providerutil "github.com/cloudbase/garm-provider-common/util"
)

const maxCredentialEnvelopeBytes = 64 << 10

type appCredential struct {
	AppID           int64  `json:"app_id"`
	InstallationID  int64  `json:"installation_id"`
	PrivateKeyBytes []byte `json:"private_key_bytes"`
}

type Token struct {
	Value     string
	ExpiresAt time.Time
}

type Minter struct {
	Endpoint   *url.URL
	HTTP       *http.Client
	Now        func() time.Time
	Passphrase []byte
}

func (minter Minter) Mint(ctx context.Context, envelope io.Reader) (Token, error) {
	if minter.Endpoint == nil || minter.HTTP == nil || minter.Now == nil || len(minter.Passphrase) != 32 || minter.Endpoint.Scheme != "https" && minter.Endpoint.Scheme != "http" || minter.Endpoint.Host == "" {
		return Token{}, fmt.Errorf("GARM GitHub App token minter is incomplete")
	}
	sealed, err := io.ReadAll(io.LimitReader(envelope, maxCredentialEnvelopeBytes+1))
	if err != nil || len(sealed) == 0 || len(sealed) > maxCredentialEnvelopeBytes {
		return Token{}, fmt.Errorf("GARM credential envelope is invalid")
	}
	plaintext, err := providerutil.Unseal(sealed, minter.Passphrase)
	if err != nil {
		return Token{}, fmt.Errorf("unseal GARM credential: %w", err)
	}
	defer clear(plaintext)
	var credential appCredential
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credential); err != nil || credential.AppID <= 0 || credential.InstallationID <= 0 || len(credential.PrivateKeyBytes) == 0 {
		return Token{}, fmt.Errorf("GARM GitHub App credential is invalid")
	}
	defer clear(credential.PrivateKeyBytes)
	block, _ := pem.Decode(credential.PrivateKeyBytes)
	if block == nil {
		return Token{}, fmt.Errorf("GARM GitHub App private key is invalid")
	}
	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return Token{}, fmt.Errorf("parse GARM GitHub App private key: %w", err)
	}
	now := minter.Now().UTC()
	jwt, err := signJWT(privateKey, credential.AppID, now)
	if err != nil {
		return Token{}, err
	}
	endpoint := *minter.Endpoint
	endpoint.Path = strings.TrimSuffix(endpoint.Path, "/") + "/app/installations/" + strconv.FormatInt(credential.InstallationID, 10) + "/access_tokens"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return Token{}, err
	}
	request.Header.Set("Authorization", "Bearer "+jwt)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := minter.HTTP.Do(request)
	if err != nil {
		return Token{}, fmt.Errorf("mint GitHub App installation token: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		return Token{}, fmt.Errorf("read GitHub App installation response")
	}
	if response.StatusCode != http.StatusCreated {
		return Token{}, fmt.Errorf("mint GitHub App installation token: HTTP %d", response.StatusCode)
	}
	var decoded struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Token == "" || decoded.ExpiresAt.Before(now.Add(time.Minute)) {
		return Token{}, fmt.Errorf("GitHub App installation token response is invalid")
	}
	return Token{Value: decoded.Token, ExpiresAt: decoded.ExpiresAt.UTC()}, nil
}

func signJWT(privateKey *rsa.PrivateKey, appID int64, now time.Time) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]int64{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": appID})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(claims)
	unsigned := header + "." + payload
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
