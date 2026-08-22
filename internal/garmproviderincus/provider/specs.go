// SPDX-License-Identifier: Apache-2.0
// Copyright 2023 Cloudbase Solutions SRL
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
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudbase/garm-provider-common/cloudconfig"
	commonParams "github.com/cloudbase/garm-provider-common/params"
	"github.com/pkg/errors"
	"github.com/xeipuuv/gojsonschema"
)

const runnerCacheBootstrapScript = `#!/usr/bin/env bash
set -Eeuo pipefail

source_root=/opt/cache/actions-runner/latest
runner_root=/home/runner/actions-runner

test -x "${source_root}/bin/Runner.Listener"
test -x "${runner_root}/bin/Runner.Listener"
if find "${source_root}" -type f \( -name .runner -o -name .credentials -o -name .credentials_rsaparams -o -name .service \) -print -quit | grep -q .; then
  echo "registration state exists in the immutable runner cache" >&2
  exit 1
fi

if find "${runner_root}" -xdev ! -user runner -print -quit | grep -q .; then
  echo "pre-materialized runner tree is not owned by runner" >&2
  exit 1
fi
source_version="$("${source_root}/bin/Runner.Listener" --version | tail -n 1 | tr -d '\r')"
runner_version="$(runuser --user runner -- "${runner_root}/bin/Runner.Listener" --version | tail -n 1 | tr -d '\r')"
if [[ -z "${source_version}" || "${runner_version}" != "${source_version}" ]]; then
  echo "pre-materialized runner tree differs from the immutable source" >&2
  exit 1
fi
if find "${runner_root}" -type f \( -name .runner -o -name .credentials -o -name .credentials_rsaparams -o -name .service \) -print -quit | grep -q .; then
  echo "registration state appeared while materializing the runner cache" >&2
  exit 1
fi
`

const runnerGroupsBootstrapTemplate = `#!/usr/bin/env bash
set -Eeuo pipefail

getent passwd runner >/dev/null
%s
usermod --groups %s runner
actual_groups="$(id --groups --name runner | tr ' ' '\n' | grep -vx runner | LC_ALL=C sort | paste -sd' ' -)"
if [[ "${actual_groups}" != %q ]]; then
  printf 'runner supplementary groups are %%q, expected %%q\n' "${actual_groups}" %q >&2
  exit 1
fi
if id --groups --name runner | tr ' ' '\n' | grep -qx lxd; then
  echo "runner retained forbidden lxd group membership" >&2
  exit 1
fi
`

// pinnedGARMV021LinuxWrapper is rendered by GARM v0.2.1 immediately before it
// invokes an external provider. It is not pool-owned configuration: GARM adds
// it at runtime when the stored extra specs do not contain an installer
// override. Keep this literal byte-for-byte aligned with the pinned upstream
// template so another GARM version or a database-injected template fails
// closed at the provider boundary.
const pinnedGARMV021LinuxWrapper = `#!/bin/bash

set -ex
set -o pipefail

METADATA_URL="{{ .MetadataURL }}"
BEARER_TOKEN="{{ .CallbackToken }}"

curl -H "Authorization: Bearer $BEARER_TOKEN" --retry 5 --retry-delay 5 --retry-connrefused --fail $METADATA_URL/install-script/ -o /tmp/real-install.sh
chmod +x /tmp/real-install.sh

/tmp/real-install.sh
rm -f /tmp/real-install.sh
`

type extraSpecs struct {
	ExtraPackages    []string `json:"extra_packages,omitempty" jsonschema:"description=A list of packages that cloud-init should install on the instance."`
	DisableUpdates   bool     `json:"disable_updates,omitempty" jsonschema:"description=Whether to disable updates when cloud-init comes online."`
	EnableBootDebug  bool     `json:"enable_boot_debug,omitempty" jsonschema:"description=Allows providers to set the -x flag in the runner install script."`
	DirectJIT        bool     `json:"nddev_direct_jit,omitempty" jsonschema:"description=Allow the NDDev GARM derivative to hand the official GitHub JIT blob directly to a prebooted worker."`
	EncodedJITConfig string   `json:"nddev_encoded_jit_config,omitempty" jsonschema:"description=Ephemeral provider request field reconstructed by GARM from its sealed JIT record."`
	cloudconfig.CloudConfigSpec
}

const maximumDirectJITJSONBytes = 64 * 1024

func validateDirectJITConfig(encoded string) error {
	if encoded == "" {
		return fmt.Errorf("direct JIT is enabled without an encoded JIT configuration")
	}
	if len(encoded) > base64.StdEncoding.EncodedLen(maximumDirectJITJSONBytes) {
		return fmt.Errorf("encoded JIT configuration exceeds %d decoded bytes", maximumDirectJITJSONBytes)
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return fmt.Errorf("decoding direct JIT configuration: %w", err)
	}
	defer clear(raw)
	if len(raw) == 0 || len(raw) > maximumDirectJITJSONBytes {
		return fmt.Errorf("decoded JIT configuration has invalid size %d", len(raw))
	}
	var files map[string]string
	if err := json.Unmarshal(raw, &files); err != nil {
		return fmt.Errorf("decoding direct JIT file map: %w", err)
	}
	wanted := map[string]struct{}{
		".runner": {}, ".credentials": {}, ".credentials_rsaparams": {},
	}
	if len(files) != len(wanted) {
		return fmt.Errorf("direct JIT file map has %d entries, expected %d", len(files), len(wanted))
	}
	for name := range wanted {
		value, ok := files[name]
		if !ok || value == "" {
			return fmt.Errorf("direct JIT file map is missing %q", name)
		}
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil || len(decoded) == 0 {
			clear(decoded)
			return fmt.Errorf("direct JIT file %q is not non-empty base64", name)
		}
		clear(decoded)
	}
	return nil
}

func jsonSchemaValidation(schema json.RawMessage) error {
	jsonSchema := generateJSONSchema()
	schemaLoader := gojsonschema.NewGoLoader(jsonSchema)
	extraSpecsLoader := gojsonschema.NewBytesLoader(schema)
	result, err := gojsonschema.Validate(schemaLoader, extraSpecsLoader)
	if err != nil {
		return fmt.Errorf("failed to validate schema: %w", err)
	}
	if !result.Valid() {
		return fmt.Errorf("schema validation failed: %s", result.Errors())
	}
	return nil
}

func parseExtraSpecsFromBootstrapParams(bootstrapParams commonParams.BootstrapInstance) (extraSpecs, error) {
	specs := extraSpecs{}
	if bootstrapParams.ExtraSpecs == nil {
		return specs, nil
	}

	if err := jsonSchemaValidation(bootstrapParams.ExtraSpecs); err != nil {
		return specs, fmt.Errorf("failed to validate extra specs: %w", err)
	}

	if err := json.Unmarshal(bootstrapParams.ExtraSpecs, &specs); err != nil {
		return specs, errors.Wrap(err, "unmarshaling extra specs")
	}
	return specs, nil
}

func renderPinnedGARMV021LinuxWrapper(metadataURL, instanceToken string) []byte {
	rendered := strings.NewReplacer(
		"{{ .MetadataURL }}", metadataURL,
		"{{ .CallbackToken }}", instanceToken,
	).Replace(pinnedGARMV021LinuxWrapper)
	return []byte(rendered)
}

// validateNDDevExtraSpecs keeps stored pool configuration declarative. GARM
// v0.2.1 adds its pinned Linux wrapper at runtime, so that exact rendering is
// accepted. Any other template, package, debug flag, root script or context is
// rejected. The accepted wrapper is deliberately discarded before cloud-init
// generation; the provider owns the immutable runner-cache bootstrap.
func validateNDDevExtraSpecs(bootstrapParams commonParams.BootstrapInstance, specs extraSpecs) error {
	if !specs.DisableUpdates {
		return fmt.Errorf("disable_updates must be true for immutable workers")
	}
	if len(specs.ExtraPackages) != 0 || specs.EnableBootDebug || len(specs.PreInstallScripts) != 0 || len(specs.ExtraContext) != 0 {
		return fmt.Errorf("only disable_updates=true and the pinned GARM runtime wrapper are allowed in extra specs")
	}
	if specs.DirectJIT {
		if err := validateDirectJITConfig(specs.EncodedJITConfig); err != nil {
			return err
		}
	} else if specs.EncodedJITConfig != "" {
		return fmt.Errorf("encoded JIT configuration is present while direct JIT is disabled")
	}
	if len(specs.RunnerInstallTemplate) != 0 && !bytes.Equal(
		specs.RunnerInstallTemplate,
		renderPinnedGARMV021LinuxWrapper(bootstrapParams.MetadataURL, bootstrapParams.InstanceToken),
	) {
		return fmt.Errorf("runner_install_template does not match the pinned GARM v0.2.1 Linux wrapper")
	}
	return nil
}

func trustedBootstrapExtraSpecs(dockerEnabled, cacheEnabled bool) (json.RawMessage, error) {
	groups := "sudo"
	expectedGroups := "sudo"
	dockerGuard := ":"
	if dockerEnabled {
		groups = "sudo,docker"
		expectedGroups = "docker sudo"
		dockerGuard = "getent group docker >/dev/null"
	}
	runnerGroupsBootstrapScript := fmt.Sprintf(
		runnerGroupsBootstrapTemplate,
		dockerGuard,
		groups,
		expectedGroups,
		expectedGroups,
	)
	preInstallScripts := map[string][]byte{}
	if cacheEnabled {
		preInstallScripts["00-nddev-cache-delivery.sh"] = cacheSetupPreInstallScript()
		preInstallScripts["01-nddev-runner-cache.sh"] = []byte(runnerCacheBootstrapScript)
		preInstallScripts["02-nddev-runner-groups.sh"] = []byte(runnerGroupsBootstrapScript)
	} else {
		preInstallScripts["00-nddev-runner-cache.sh"] = []byte(runnerCacheBootstrapScript)
		preInstallScripts["01-nddev-runner-groups.sh"] = []byte(runnerGroupsBootstrapScript)
	}
	specs := extraSpecs{
		DisableUpdates: true,
		CloudConfigSpec: cloudconfig.CloudConfigSpec{
			PreInstallScripts: preInstallScripts,
		},
	}
	encoded, err := json.Marshal(specs)
	if err != nil {
		return nil, fmt.Errorf("encode trusted bootstrap extra specs: %w", err)
	}
	return encoded, nil
}
