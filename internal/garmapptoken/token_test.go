package garmapptoken

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	providerutil "github.com/cloudbase/garm-provider-common/util"
	"github.com/stretchr/testify/require"
)

func TestMinterUnsealsCredentialAndMintsInstallationToken(t *testing.T) {
	now := time.Date(2026, 8, 26, 16, 0, 0, 0, time.UTC)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	credential, err := json.Marshal(appCredential{AppID: 123, InstallationID: 456, PrivateKeyBytes: keyPEM})
	require.NoError(t, err)
	passphrase := []byte("0123456789abcdefghijklmnopqrstuv")
	sealed, err := providerutil.Seal(credential, passphrase)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "/app/installations/456/access_tokens", request.URL.Path)
		require.Contains(t, request.Header.Get("Authorization"), "Bearer eyJ")
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(map[string]any{"token": "ghs_short_lived", "expires_at": now.Add(time.Hour)})
	}))
	defer server.Close()
	endpoint, err := url.Parse(server.URL)
	require.NoError(t, err)
	token, err := (Minter{Endpoint: endpoint, HTTP: server.Client(), Now: func() time.Time { return now }, Passphrase: passphrase}).Mint(context.Background(), bytes.NewReader(sealed))
	require.NoError(t, err)
	require.Equal(t, "ghs_short_lived", token.Value)
}
