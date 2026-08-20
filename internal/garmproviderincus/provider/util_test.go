// SPDX-License-Identifier: Apache-2.0
// Copyright 2023 Cloudbase Solutions SRL
// Modified by NDDev in 2026 for integration into the github-actions module.
//
//    Licensed under the Apache License, Version 2.0 (the "License"); you may
//    not use this file except in compliance with the License. You may obtain
//    a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//    WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//    License for the specific language governing permissions and limitations
//    under the License.

package provider

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/garmproviderincus/config"
	commonParams "github.com/cloudbase/garm-provider-common/params"
	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/assert"
)

func TestIncusInstanceToAPIInstance(t *testing.T) {
	instance := &api.InstanceFull{
		Instance: api.Instance{
			Name: "test-instance",
			InstancePut: api.InstancePut{
				Architecture: "x86_64",
			},
			ExpandedConfig: map[string]string{
				"image.os":      "ubuntu",
				osTypeKeyName:   "linux",
				"image.release": "20.04",
			},
		},
		State: &api.InstanceState{
			Network: map[string]api.InstanceStateNetwork{
				"eth0": {
					Addresses: []api.InstanceStateNetworkAddress{
						{
							Family:  "inet",
							Address: "10.10.0.4",
							Netmask: "24",
							Scope:   "global",
						},
					},
				},
			},
			Status: "Running",
		},
	}
	expectedOutput := commonParams.ProviderInstance{
		OSArch:     "amd64",
		ProviderID: "test-instance",
		Name:       "test-instance",
		OSType:     "linux",
		OSName:     "ubuntu",
		OSVersion:  "20.04",
		Addresses: []commonParams.Address{
			{
				Address: "10.10.0.4",
				Type:    "public",
			},
		},
		Status: "running",
	}

	apiInstance := incusInstanceToAPIInstance(instance)
	assert.Equal(t, expectedOutput, apiInstance)
}

func TestGetClientFromConfig(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		cfg       *config.Incus
		errString string
	}{
		{
			name:      "Nil config",
			cfg:       nil,
			errString: "no Incus configuration found",
		},
		{
			name:      "empty config",
			cfg:       &config.Incus{},
			errString: "no URL or UnixSocket specified",
		},
		{
			name: "invalid TSLServerCert",
			cfg: &config.Incus{
				URL:           "https://localhost:8443",
				TLSServerCert: "invalid",
			},
			errString: "reading TLSServerCert",
		},
		{
			name: "invalid TLSCA",
			cfg: &config.Incus{
				URL:   "https://localhost:8443",
				TLSCA: "invalid",
			},
			errString: "reading TLSCA",
		},
		{
			name: "invalid ClientCertificate",
			cfg: &config.Incus{
				URL:               "https://localhost:8443",
				ClientCertificate: "invalid",
			},
			errString: "reading ClientCertificate",
		},
		{
			name: "invalid ClientKey",
			cfg: &config.Incus{
				URL:       "https://localhost:8443",
				ClientKey: "invalid",
			},
			errString: "reading ClientKey",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, err := getClientFromConfig(ctx, tt.cfg)
			if tt.errString != "" {
				assert.ErrorContains(t, err, tt.errString)
				assert.Nil(t, output)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, output)
			}
		})
	}

}

func TestGetClientFromConfigFailsOverToTheNextClusterMember(t *testing.T) {
	directory := t.TempDir()
	paths := []string{
		filepath.Join(directory, "server.crt"),
		filepath.Join(directory, "client.crt"),
		filepath.Join(directory, "client.key"),
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("test\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	originalDial, originalConnect := dialIncusEndpoint, connectIncusEndpoint
	t.Cleanup(func() {
		dialIncusEndpoint, connectIncusEndpoint = originalDial, originalConnect
	})
	var dialed, connected []string
	dialIncusEndpoint = func(_, address string, _ time.Duration) (net.Conn, error) {
		dialed = append(dialed, address)
		if address == "10.200.0.5:8443" {
			return nil, fmt.Errorf("member unavailable")
		}
		client, server := net.Pipe()
		_ = server.Close()
		return client, nil
	}
	connectIncusEndpoint = func(_ context.Context, endpoint string, _ *incus.ConnectionArgs) (incus.InstanceServer, error) {
		connected = append(connected, endpoint)
		return nil, nil
	}
	got, err := getClientFromConfig(context.Background(), &config.Incus{
		ClusterURLs:       []string{"https://10.200.0.5:8443", "https://10.200.0.6:8443"},
		TLSServerCert:     paths[0],
		ClientCertificate: paths[1],
		ClientKey:         paths[2],
	})
	if err != nil {
		t.Fatal(err)
	}
	assert.Nil(t, got)
	assert.Equal(t, []string{"10.200.0.5:8443", "10.200.0.6:8443"}, dialed)
	assert.Equal(t, []string{"https://10.200.0.6:8443"}, connected)
}
