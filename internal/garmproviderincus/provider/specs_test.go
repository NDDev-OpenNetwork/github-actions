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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudbase/garm-provider-common/cloudconfig"
	"github.com/cloudbase/garm-provider-common/params"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testEncodedDirectJIT(t *testing.T) string {
	t.Helper()
	files := map[string]string{
		".runner":                base64.StdEncoding.EncodeToString([]byte(`{"agentId":1}`)),
		".credentials":           base64.StdEncoding.EncodeToString([]byte(`{"scheme":"OAuth"}`)),
		".credentials_rsaparams": base64.StdEncoding.EncodeToString([]byte(`{"d":"private"}`)),
	}
	return encodeDirectJITFiles(t, files)
}

func encodeDirectJITFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	raw, err := json.Marshal(files)
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(raw)
}

var testCases = []struct {
	name           string
	input          json.RawMessage
	expectedOutput extraSpecs
	errString      string
}{
	{
		name:  "full specs",
		input: json.RawMessage(`{"disable_updates": true, "extra_packages": ["package1", "package2"], "enable_boot_debug": true, "runner_install_template": "IyEvYmluL2Jhc2gKZWNobyBJbnN0YWxsaW5nIHJ1bm5lci4uLg==", "pre_install_scripts": {"setup.sh": "IyEvYmluL2Jhc2gKZWNobyBTZXR1cCBzY3JpcHQuLi4="}, "extra_context": {"key": "value"}}`),
		expectedOutput: extraSpecs{
			DisableUpdates:  true,
			ExtraPackages:   []string{"package1", "package2"},
			EnableBootDebug: true,
			CloudConfigSpec: cloudconfig.CloudConfigSpec{
				RunnerInstallTemplate: []byte("#!/bin/bash\necho Installing runner..."),
				PreInstallScripts: map[string][]byte{
					"setup.sh": []byte("#!/bin/bash\necho Setup script..."),
				},
				ExtraContext: map[string]string{"key": "value"},
			},
		},
		errString: "",
	},
	{
		name:  "specs just with disable_updates",
		input: json.RawMessage(`{"disable_updates": true}`),
		expectedOutput: extraSpecs{
			DisableUpdates: true,
		},
		errString: "",
	},
	{
		name:  "specs just with extra_packages",
		input: json.RawMessage(`{"extra_packages": ["package1", "package2"]}`),
		expectedOutput: extraSpecs{
			ExtraPackages: []string{"package1", "package2"},
		},
		errString: "",
	},
	{
		name:  "specs just with enable_boot_debug",
		input: json.RawMessage(`{"enable_boot_debug": true}`),
		expectedOutput: extraSpecs{
			EnableBootDebug: true,
		},
		errString: "",
	},
	{
		name:  "specs just with runner_install_template",
		input: json.RawMessage(`{"runner_install_template": "IyEvYmluL2Jhc2gKZWNobyBJbnN0YWxsaW5nIHJ1bm5lci4uLg=="}`),
		expectedOutput: extraSpecs{
			CloudConfigSpec: cloudconfig.CloudConfigSpec{
				RunnerInstallTemplate: []byte("#!/bin/bash\necho Installing runner..."),
			},
		},
		errString: "",
	},
	{
		name:  "specs just with pre_install_scripts",
		input: json.RawMessage(`{"pre_install_scripts": {"setup.sh": "IyEvYmluL2Jhc2gKZWNobyBTZXR1cCBzY3JpcHQuLi4="}}`),
		expectedOutput: extraSpecs{
			CloudConfigSpec: cloudconfig.CloudConfigSpec{
				PreInstallScripts: map[string][]byte{
					"setup.sh": []byte("#!/bin/bash\necho Setup script..."),
				},
			},
		},
		errString: "",
	},
	{
		name:  "specs just with extra_context",
		input: json.RawMessage(`{"extra_context": {"key": "value"}}`),
		expectedOutput: extraSpecs{
			CloudConfigSpec: cloudconfig.CloudConfigSpec{
				ExtraContext: map[string]string{"key": "value"},
			},
		},
		errString: "",
	},
	{
		name:           "empty specs",
		input:          json.RawMessage(`{}`),
		expectedOutput: extraSpecs{},
		errString:      "",
	},
	{
		name:           "invalid json",
		input:          json.RawMessage(`{"disable_updates": true, "extra_packages": ["package1", "package2", "enable_boot_debug": true}`),
		expectedOutput: extraSpecs{},
		errString:      "failed to validate extra specs",
	},
	{
		name:           "invalid input for disable_updates - wrong data type",
		input:          json.RawMessage(`{"disable_updates": "true"}`),
		expectedOutput: extraSpecs{},
		errString:      "schema validation failed: [disable_updates: Invalid type. Expected: boolean, given: string]",
	},
	{
		name:           "invalid input for extra_packages - wrong data type",
		input:          json.RawMessage(`{"extra_packages": "package1"}`),
		expectedOutput: extraSpecs{},
		errString:      "schema validation failed: [extra_packages: Invalid type. Expected: array, given: string]",
	},
	{
		name:           "invalid input for enable_boot_debug - wrong data type",
		input:          json.RawMessage(`{"enable_boot_debug": "true"}`),
		expectedOutput: extraSpecs{},
		errString:      "schema validation failed: [enable_boot_debug: Invalid type. Expected: boolean, given: string]",
	},
	{
		name:           "invalid input for runner_install_template - wrong data type",
		input:          json.RawMessage(`{"runner_install_template": true}`),
		expectedOutput: extraSpecs{},
		errString:      "schema validation failed: [runner_install_template: Invalid type. Expected: string, given: boolean]",
	},
	{
		name:           "invalid input for pre_install_scripts - wrong data type",
		input:          json.RawMessage(`{"pre_install_scripts": "setup.sh"}`),
		expectedOutput: extraSpecs{},
		errString:      "schema validation failed: [pre_install_scripts: Invalid type. Expected: object, given: string]",
	},
	{
		name:           "invalid input for extra_context - wrong data type",
		input:          json.RawMessage(`{"extra_context": ["key", "value"]}`),
		expectedOutput: extraSpecs{},
		errString:      "schema validation failed: [extra_context: Invalid type. Expected: object, given: array]",
	},
	{
		name:           "invalid input - additional property",
		input:          json.RawMessage(`{"additional_property": true}`),
		expectedOutput: extraSpecs{},
		errString:      "Additional property additional_property is not allowed",
	},
}

func TestParseExtraSpecsFromBootstrapParams(t *testing.T) {
	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseExtraSpecsFromBootstrapParams(params.BootstrapInstance{ExtraSpecs: tt.input})
			assert.Equal(t, tt.expectedOutput, got)
			if tt.errString != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errString)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestNDDevExtraSpecsPolicy(t *testing.T) {
	bootstrap := validBootstrap()
	require.NoError(t, validateNDDevExtraSpecs(bootstrap, extraSpecs{DisableUpdates: true}))
	require.NoError(t, validateNDDevExtraSpecs(bootstrap, extraSpecs{
		DisableUpdates: true,
		CloudConfigSpec: cloudconfig.CloudConfigSpec{
			RunnerInstallTemplate: renderPinnedGARMV021LinuxWrapper(bootstrap.MetadataURL, bootstrap.InstanceToken),
		},
	}))
	require.NoError(t, validateNDDevExtraSpecs(bootstrap, extraSpecs{
		DisableUpdates:   true,
		DirectJIT:        true,
		EncodedJITConfig: testEncodedDirectJIT(t),
		CloudConfigSpec: cloudconfig.CloudConfigSpec{
			RunnerInstallTemplate: renderPinnedGARMV021LinuxWrapper(bootstrap.MetadataURL, bootstrap.InstanceToken),
		},
	}))

	for _, specs := range []extraSpecs{
		{},
		{DisableUpdates: true, ExtraPackages: []string{"curl"}},
		{DisableUpdates: true, EnableBootDebug: true},
		{DisableUpdates: true, CloudConfigSpec: cloudconfig.CloudConfigSpec{RunnerInstallTemplate: []byte("unsafe")}},
		{DisableUpdates: true, CloudConfigSpec: cloudconfig.CloudConfigSpec{PreInstallScripts: map[string][]byte{"unsafe": []byte("unsafe")}}},
		{DisableUpdates: true, CloudConfigSpec: cloudconfig.CloudConfigSpec{ExtraContext: map[string]string{"unsafe": "unsafe"}}},
		{DisableUpdates: true, DirectJIT: true},
		{DisableUpdates: true, EncodedJITConfig: testEncodedDirectJIT(t)},
		{DisableUpdates: true, DirectJIT: true, EncodedJITConfig: "not-base64"},
	} {
		require.Error(t, validateNDDevExtraSpecs(bootstrap, specs))
	}
}

func TestDirectJITRequiresOfficialDottedFileNames(t *testing.T) {
	valid := map[string]string{
		".runner":                base64.StdEncoding.EncodeToString([]byte(`{"agentId":1}`)),
		".credentials":           base64.StdEncoding.EncodeToString([]byte(`{"scheme":"OAuth"}`)),
		".credentials_rsaparams": base64.StdEncoding.EncodeToString([]byte(`{"d":"private"}`)),
	}
	for _, test := range []struct {
		dotted   string
		undotted string
	}{
		{dotted: ".runner", undotted: "runner"},
		{dotted: ".credentials", undotted: "credentials"},
		{dotted: ".credentials_rsaparams", undotted: "credentials_rsaparams"},
	} {
		t.Run(test.dotted, func(t *testing.T) {
			files := make(map[string]string, len(valid))
			for name, contents := range valid {
				files[name] = contents
			}
			files[test.undotted] = files[test.dotted]
			delete(files, test.dotted)

			err := validateDirectJITConfig(encodeDirectJITFiles(t, files))
			require.Error(t, err)
			require.Contains(t, err.Error(), fmt.Sprintf("missing %q", test.dotted))
		})
	}
}

func TestPinnedGARMV021LinuxWrapperRendering(t *testing.T) {
	got := string(renderPinnedGARMV021LinuxWrapper("https://gateway.example/metadata", "opaque-token"))
	want := `#!/bin/bash

set -ex
set -o pipefail

METADATA_URL="https://gateway.example/metadata"
BEARER_TOKEN="opaque-token"

curl -H "Authorization: Bearer $BEARER_TOKEN" --retry 5 --retry-delay 5 --retry-connrefused --fail $METADATA_URL/install-script/ -o /tmp/real-install.sh
chmod +x /tmp/real-install.sh

/tmp/real-install.sh
rm -f /tmp/real-install.sh
`
	require.Equal(t, want, got)
}

func TestTrustedBootstrapMaterializesPinnedRunnerCache(t *testing.T) {
	raw, err := trustedBootstrapExtraSpecs(false, false)
	require.NoError(t, err)

	parsed, err := parseExtraSpecsFromBootstrapParams(params.BootstrapInstance{ExtraSpecs: raw})
	require.NoError(t, err)
	require.True(t, parsed.DisableUpdates)
	require.Len(t, parsed.PreInstallScripts, 2)
	script := string(parsed.PreInstallScripts["00-nddev-runner-cache.sh"])
	require.Contains(t, script, "/opt/cache/actions-runner/latest")
	require.Contains(t, script, "cp -a --reflink=auto")
	require.Contains(t, script, "chown -R runner:runner")
	require.Contains(t, script, "registration state")
	groupsScript := string(parsed.PreInstallScripts["01-nddev-runner-groups.sh"])
	require.Contains(t, groupsScript, "usermod --groups sudo runner")
	require.Contains(t, groupsScript, "runner supplementary groups are")
	require.NotContains(t, groupsScript, "unexpectedly contains the docker group")
	require.NotContains(t, groupsScript, "getent group docker")
	require.Contains(t, groupsScript, "forbidden lxd group membership")

	integrationRaw, err := trustedBootstrapExtraSpecs(true, false)
	require.NoError(t, err)
	integration, err := parseExtraSpecsFromBootstrapParams(params.BootstrapInstance{ExtraSpecs: integrationRaw})
	require.NoError(t, err)
	integrationGroupsScript := string(integration.PreInstallScripts["01-nddev-runner-groups.sh"])
	require.Contains(t, integrationGroupsScript, "usermod --groups sudo,docker runner")
	require.Contains(t, integrationGroupsScript, "getent group docker")

	for name, script := range map[string]string{
		"standard":    groupsScript,
		"integration": integrationGroupsScript,
	} {
		command := exec.Command("bash", "-n")
		command.Stdin = strings.NewReader(script)
		output, err := command.CombinedOutput()
		require.NoError(t, err, "%s group policy script is invalid: %s", name, output)
	}
}

func TestRunnerGroupBootstrapReducesExistingBroadMembership(t *testing.T) {
	t.Parallel()

	commandDirectory := t.TempDir()
	for name, contents := range map[string]string{
		"getent":  "#!/bin/sh\nif [ \"$1\" = passwd ]; then exit 0; fi\nif [ \"$1\" = group ] && [ \"$2\" = docker ]; then echo docker:x:999:runner; exit 0; fi\nexit 2\n",
		"id":      "#!/bin/sh\nprintf '%s\\n' \"$FAKE_ID_GROUPS\"\n",
		"usermod": "#!/bin/sh\nprintf '%s\\n' \"$*\" >\"$FAKE_USERMOD_LOG\"\n",
	} {
		path := filepath.Join(commandDirectory, name)
		require.NoError(t, os.WriteFile(path, []byte(contents), 0o755))
	}

	for _, test := range []struct {
		name          string
		dockerEnabled bool
		idGroups      string
		usermodArgs   string
	}{
		{"standard", false, "runner sudo", "--groups sudo runner"},
		{"integration", true, "runner docker sudo", "--groups sudo,docker runner"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := trustedBootstrapExtraSpecs(test.dockerEnabled, false)
			require.NoError(t, err)
			specs, err := parseExtraSpecsFromBootstrapParams(params.BootstrapInstance{ExtraSpecs: raw})
			require.NoError(t, err)
			logPath := filepath.Join(t.TempDir(), "usermod.log")
			command := exec.Command("bash", "-ceu", string(specs.PreInstallScripts["01-nddev-runner-groups.sh"]))
			command.Env = append(os.Environ(),
				"PATH="+commandDirectory+":/usr/bin:/bin",
				"FAKE_ID_GROUPS="+test.idGroups,
				"FAKE_USERMOD_LOG="+logPath,
			)
			output, err := command.CombinedOutput()
			require.NoError(t, err, "group reduction failed: %s", output)
			usermodArgs, err := os.ReadFile(logPath)
			require.NoError(t, err)
			require.Equal(t, test.usermodArgs+"\n", string(usermodArgs))
		})
	}
}
