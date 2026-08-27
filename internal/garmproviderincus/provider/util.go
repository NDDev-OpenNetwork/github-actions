// SPDX-License-Identifier: Apache-2.0
// Copyright 2023 Cloudbase Solutions SRL
// Modified by NDDev in 2026 for the hardened NDDev fleet provider.
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
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	runnerErrors "github.com/cloudbase/garm-provider-common/errors"
	commonParams "github.com/cloudbase/garm-provider-common/params"

	"github.com/NDDev-OpenNetwork/github-actions/internal/garmproviderincus/config"
	"github.com/cloudbase/garm-provider-common/util"

	"github.com/invopop/jsonschema"
	"github.com/juju/clock"
	"github.com/juju/retry"
	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/pkg/errors"
)

var (
	dialIncusEndpoint    = net.DialTimeout
	connectIncusEndpoint = incus.ConnectIncusWithContext
)

var (
	//lint:ignore ST1005 imported error from incus
	errInstanceIsStopped error = fmt.Errorf("The instance is already stopped")
)

var httpResponseErrors = map[int][]error{
	http.StatusNotFound: {os.ErrNotExist, sql.ErrNoRows},
}

// isNotFoundError returns true if the error is considered a Not Found error.
func isNotFoundError(err error) bool {
	if errors.Is(err, runnerErrors.ErrNotFound) {
		return true
	}
	if api.StatusErrorCheck(err, http.StatusNotFound) {
		return true
	}

	for _, checkErr := range httpResponseErrors[http.StatusNotFound] {
		if errors.Is(err, checkErr) {
			return true
		}
	}

	return false
}

func incusInstanceToAPIInstance(instance *api.InstanceFull) commonParams.ProviderInstance {
	incusOS := instance.ExpandedConfig["image.os"]

	osType, _ := util.OSToOSType(incusOS)

	if osType == "" {
		osTypeFromTag := instance.ExpandedConfig[osTypeKeyName]
		osType = commonParams.OSType(osTypeFromTag)
	}
	osRelease := instance.ExpandedConfig["image.release"]

	state := instance.State
	addresses := []commonParams.Address{}
	if state != nil && state.Network != nil {
		for _, details := range state.Network {
			for _, addr := range details.Addresses {
				if addr.Scope != "global" {
					continue
				}
				addresses = append(addresses, commonParams.Address{
					Address: addr.Address,
					Type:    commonParams.PublicAddress,
				})
			}
		}
	}
	instanceArch := incusToConfigArch[instance.Architecture]

	status := commonParams.InstanceStatusUnknown
	if state != nil {
		status = incusStatusToProviderStatus(state.Status)
	}

	return commonParams.ProviderInstance{
		OSArch:     instanceArch,
		ProviderID: instance.Name,
		Name:       instance.Name,
		OSType:     osType,
		OSName:     strings.ToLower(incusOS),
		OSVersion:  osRelease,
		Addresses:  addresses,
		Status:     status,
	}
}

func incusInventoryInstanceToAPIInstance(instance *api.Instance) commonParams.ProviderInstance {
	if instance == nil {
		return commonParams.ProviderInstance{}
	}
	return incusInstanceToAPIInstance(&api.InstanceFull{
		Instance: *instance,
		State: &api.InstanceState{
			Status:     instance.Status,
			StatusCode: instance.StatusCode,
		},
	})
}

func incusStatusToProviderStatus(status string) commonParams.InstanceStatus {
	switch status {
	case "Running":
		return commonParams.InstanceRunning
	case "Stopped":
		return commonParams.InstanceStopped
	default:
		return commonParams.InstanceStatusUnknown
	}
}

func getClientFromConfig(ctx context.Context, cfg *config.Incus) (cli incus.InstanceServer, err error) {
	if cfg == nil {
		return nil, fmt.Errorf("no Incus configuration found")
	}

	if cfg.UnixSocket != "" {
		return incus.ConnectIncusUnixWithContext(ctx, cfg.UnixSocket, &incus.ConnectionArgs{SkipGetServer: true})
	}

	if cfg.URL == "" && len(cfg.ClusterURLs) == 0 {
		return nil, fmt.Errorf("no URL or UnixSocket specified")
	}

	var srvCrtContents, tlsCAContents, clientCertContents, clientKeyContents []byte

	if cfg.TLSServerCert != "" {
		srvCrtContents, err = os.ReadFile(cfg.TLSServerCert)
		if err != nil {
			return nil, errors.Wrap(err, "reading TLSServerCert")
		}
	}

	if cfg.TLSCA != "" {
		tlsCAContents, err = os.ReadFile(cfg.TLSCA)
		if err != nil {
			return nil, errors.Wrap(err, "reading TLSCA")
		}
	}

	if cfg.ClientCertificate != "" {
		clientCertContents, err = os.ReadFile(cfg.ClientCertificate)
		if err != nil {
			return nil, errors.Wrap(err, "reading ClientCertificate")
		}
	}

	if cfg.ClientKey != "" {
		clientKeyContents, err = os.ReadFile(cfg.ClientKey)
		if err != nil {
			return nil, errors.Wrap(err, "reading ClientKey")
		}
	}

	connectArgs := incus.ConnectionArgs{
		TLSServerCert: string(srvCrtContents),
		TLSCA:         string(tlsCAContents),
		TLSClientCert: string(clientCertContents),
		TLSClientKey:  string(clientKeyContents),
		SkipGetServer: false,
	}

	endpoints := cfg.ClusterURLs
	if len(endpoints) == 0 {
		endpoints = []string{cfg.URL}
	}
	var endpointErrors []error
	for _, endpoint := range endpoints {
		parsed, parseErr := url.Parse(endpoint)
		if parseErr != nil {
			endpointErrors = append(endpointErrors, fmt.Errorf("%s: parse endpoint: %w", endpoint, parseErr))
			continue
		}
		connection, dialErr := dialIncusEndpoint("tcp", parsed.Host, 2*time.Second)
		if dialErr != nil {
			endpointErrors = append(endpointErrors, fmt.Errorf("%s: reach endpoint: %w", endpoint, dialErr))
			continue
		}
		_ = connection.Close()
		incusCLI, connectErr := connectIncusEndpoint(ctx, endpoint, &connectArgs)
		if connectErr != nil {
			endpointErrors = append(endpointErrors, fmt.Errorf("%s: connect: %w", endpoint, connectErr))
			continue
		}
		return incusCLI, nil
	}
	return nil, fmt.Errorf("connecting to every Incus cluster endpoint: %v", endpointErrors)
}

func projectName(cfg *config.Incus) string {
	if cfg != nil && cfg.ProjectName != "" {
		return cfg.ProjectName
	}
	return DefaultProjectName
}

func resolveArchitecture(osArch commonParams.OSArch) (string, error) {
	if string(osArch) == "" {
		return configToIncusArchMap[commonParams.Amd64], nil
	}
	arch, ok := configToIncusArchMap[osArch]
	if !ok {
		return "", fmt.Errorf("architecture %s is not supported", osArch)
	}
	return arch, nil
}

// waitDeviceActive is a function capable of figuring out when a Equinix Metal
// device is active
func (l *Incus) waitInstanceHasIP(ctx context.Context, instanceName string) (commonParams.ProviderInstance, error) {
	var p commonParams.ProviderInstance
	var errIPNotFound error = fmt.Errorf("ip not found")
	err := retry.Call(retry.CallArgs{
		Func: func() error {
			var err error
			p, err = l.GetInstance(ctx, instanceName)
			if err != nil {
				return errors.Wrap(err, "fetching instance")
			}
			for _, addr := range p.Addresses {
				ip := net.ParseIP(addr.Address)
				if ip == nil {
					continue
				}
				if ip.To4() == nil {
					continue
				}
				return nil
			}
			return errIPNotFound
		},
		Attempts: 20,
		Delay:    5 * time.Second,
		Clock:    clock.WallClock,
	})

	if err != nil {
		if err == errIPNotFound {
			return commonParams.ProviderInstance{}, errors.Wrapf(
				runnerErrors.ErrTimeout,
				"instance %q did not receive an IPv4 address",
				instanceName,
			)
		}
		return commonParams.ProviderInstance{}, err
	}

	return p, nil
}

func ptr[T any](v T) *T {
	return &v
}

func generateJSONSchema() *jsonschema.Schema {
	reflector := jsonschema.Reflector{
		AllowAdditionalProperties: false,
	}
	// Reflect the extraSpecs struct
	schema := reflector.Reflect(extraSpecs{})

	return schema
}
