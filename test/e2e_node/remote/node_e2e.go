/*
Copyright 2016 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package remote

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"k8s.io/kubernetes/test/e2e_node/builder"
	"k8s.io/kubernetes/test/e2e_node/system"
	"k8s.io/kubernetes/test/utils"
)

// NodeE2ERemote contains the specific functions in the node e2e test suite.
type NodeE2ERemote struct{}

// InitNodeE2ERemote initializes the node e2e test suite.
func InitNodeE2ERemote() TestSuite {
	// TODO: Register flags.
	return &NodeE2ERemote{}
}

// SetupTestPackage sets up the test package with binaries k8s required for node e2e tests
func (n *NodeE2ERemote) SetupTestPackage(tardir, systemSpecName string) error {
	// Build the executables
	if err := builder.BuildGo(); err != nil {
		return fmt.Errorf("failed to build the dependencies: %w", err)
	}

	// Make sure we can find the newly built binaries
	buildOutputDir, err := utils.GetK8sBuildOutputDir()
	if err != nil {
		return fmt.Errorf("failed to locate kubernetes build output directory: %w", err)
	}

	rootDir, err := utils.GetK8sRootDir()
	if err != nil {
		return fmt.Errorf("failed to locate kubernetes root directory: %w", err)
	}

	// Copy binaries
	requiredBins := []string{"kubelet", "e2e_node.test", "ginkgo", "mounter", "gcp-credential-provider"}
	for _, bin := range requiredBins {
		source := filepath.Join(buildOutputDir, bin)
		if _, err := os.Stat(source); err != nil {
			return fmt.Errorf("failed to locate test binary %s: %w", bin, err)
		}
		out, err := exec.Command("cp", source, filepath.Join(tardir, bin)).CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to copy %q: %v Output: %q", bin, err, out)
		}
	}

	if systemSpecName != "" {
		// Copy system spec file
		source := filepath.Join(rootDir, system.SystemSpecPath, systemSpecName+".yaml")
		if _, err := os.Stat(source); err != nil {
			return fmt.Errorf("failed to locate system spec %q: %w", source, err)
		}
		out, err := exec.Command("cp", source, tardir).CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to copy system spec %q: %v, output: %q", source, err, out)
		}
	}

	return nil
}

// prependCOSMounterFlag prepends the flag for setting the GCI mounter path to
// args and returns the result.
func prependCOSMounterFlag(args, host, workspace string) (string, error) {
	klog.V(2).Infof("GCI/COS node and GCI/COS mounter both detected, modifying --experimental-mounter-path accordingly")
	mounterPath := filepath.Join(workspace, "mounter")
	args = fmt.Sprintf("--kubelet-flags=--experimental-mounter-path=%s ", mounterPath) + args
	return args, nil
}

// prependMemcgNotificationFlag prepends the flag for enabling memcg
// notification to args and returns the result.
func prependMemcgNotificationFlag(args string) string {
	return "--kubelet-flags=--kernel-memcg-notification=true " + args
}

// prependGCPCredentialProviderFlag prepends the flags for enabling
// a credential provider plugin.
func prependGCPCredentialProviderFlag(args, workspace string) string {
	credentialProviderConfig := filepath.Join(workspace, "credential-provider.yaml")
	featureGateFlag := "--kubelet-flags=--feature-gates=DisableKubeletCloudCredentialProviders=true,KubeletCredentialProviders=true"
	configFlag := fmt.Sprintf("--kubelet-flags=--image-credential-provider-config=%s", credentialProviderConfig)
	binFlag := fmt.Sprintf("--kubelet-flags=--image-credential-provider-bin-dir=%s", workspace)
	return fmt.Sprintf("%s %s %s %s", featureGateFlag, configFlag, binFlag, args)
}

// osSpecificActions takes OS specific actions required for the node tests
func osSpecificActions(args, host, workspace string) (string, error) {
	output, err := getOSDistribution(host)
	if err != nil {
		return "", fmt.Errorf("issue detecting node's OS via node's /etc/os-release. Err: %v, Output:\n%s", err, output)
	}
	switch {
	case strings.Contains(output, "fedora"), strings.Contains(output, "rhcos"),
		strings.Contains(output, "centos"), strings.Contains(output, "rhel"):
		return args, setKubeletSELinuxLabels(host, workspace)
	case strings.Contains(output, "gci"), strings.Contains(output, "cos"):
		args = prependMemcgNotificationFlag(args)
		args = prependGCPCredentialProviderFlag(args, workspace)
		return prependCOSMounterFlag(args, host, workspace)
	case strings.Contains(output, "ubuntu"):
		args = prependGCPCredentialProviderFlag(args, workspace)
		return prependMemcgNotificationFlag(args), nil
	}
	return args, nil
}

// setKubeletSELinuxLabels set the appropriate SELinux labels for the
// kubelet on Fedora CoreOS distribution
func setKubeletSELinuxLabels(host, workspace string) error {
	cmd := getSSHCommand(" && ",
		fmt.Sprintf("/usr/bin/chcon -u system_u -r object_r -t bin_t %s", filepath.Join(workspace, "kubelet")),
		fmt.Sprintf("/usr/bin/chcon -u system_u -r object_r -t bin_t %s", filepath.Join(workspace, "e2e_node.test")),
		fmt.Sprintf("/usr/bin/chcon -u system_u -r object_r -t bin_t %s", filepath.Join(workspace, "ginkgo")),
		fmt.Sprintf("/usr/bin/chcon -u system_u -r object_r -t bin_t %s", filepath.Join(workspace, "mounter")),
		fmt.Sprintf("/usr/bin/chcon -R -u system_u -r object_r -t bin_t %s", filepath.Join(workspace, "cni", "bin/")),
	)
	output, err := SSH(host, "sh", "-c", cmd)
	if err != nil {
		return fmt.Errorf("Unable to apply SELinux labels. Err: %v, Output:\n%s", err, output)
	}
	return nil
}

func getOSDistribution(host string) (string, error) {
	output, err := SSH(host, "cat", "/etc/os-release")
	if err != nil {
		return "", fmt.Errorf("issue detecting node's OS via node's /etc/os-release. Err: %v, Output:\n%s", err, output)
	}

	var re = regexp.MustCompile(`(?m)^ID="?(\w+)"?`)
	subMatch := re.FindStringSubmatch(output)
	if len(subMatch) > 0 {
		return subMatch[1], nil
	}

	return "", fmt.Errorf("Unable to parse os-release for the host, %s", host)
}

// RunTest runs test on the node.
func (n *NodeE2ERemote) RunTest(host, workspace, results, imageDesc, junitFilePrefix, testArgs, ginkgoArgs, systemSpecName, extraEnvs, runtimeConfig string, timeout time.Duration) (string, error) {
	// Install the cni plugins and add a basic CNI configuration.
	// TODO(random-liu): Do this in cloud init after we remove containervm test.
	if err := setupCNI(host, workspace); err != nil {
		return "", err
	}

	// Configure iptables firewall rules
	if err := configureFirewall(host); err != nil {
		return "", err
	}

	// Install the kubelet credential provider plugin
	if err := configureCredentialProvider(host, workspace); err != nil {
		return "", err
	}

	// Kill any running node processes
	cleanupNodeProcesses(host)

	testArgs, err := osSpecificActions(testArgs, host, workspace)
	if err != nil {
		return "", err
	}

	systemSpecFile := ""
	if systemSpecName != "" {
		systemSpecFile = systemSpecName + ".yaml"
	}

	outputGinkgoFile := filepath.Join(results, fmt.Sprintf("%s-ginkgo.log", host))

	// Run the tests
	klog.V(2).Infof("Starting tests on %q", host)
	cmd := getSSHCommand(" && ",
		fmt.Sprintf("cd %s", workspace),
		// Note, we need to have set -o pipefail here to ensure we return the appriorate exit code from ginkgo; not tee
		fmt.Sprintf("set -o pipefail; timeout -k 30s %fs ./ginkgo %s ./e2e_node.test -- --system-spec-name=%s --system-spec-file=%s --extra-envs=%s --runtime-config=%s --v 4 --node-name=%s --report-dir=%s --report-prefix=%s --image-description=\"%s\" %s 2>&1 | tee -i %s",
			timeout.Seconds(), ginkgoArgs, systemSpecName, systemSpecFile, extraEnvs, runtimeConfig, host, results, junitFilePrefix, imageDesc, testArgs, outputGinkgoFile),
	)
	return SSH(host, "/bin/bash", "-c", cmd)
}
