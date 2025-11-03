// Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package helpers

import (
	"context"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/e2e-framework/klient"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
)

const (
	ERRORCODE_13 = "13"
	ERRORCODE_48 = "48"
	ERRORCODE_31 = "31"
)

type HealthEventsAnalyzerTestContext struct {
	NodeName        string
	ConfigMapBackup []byte
	TestNamespace   string
}

func SetupHealthEventsAnalyzerTest(ctx context.Context,
	t *testing.T,
	c *envconf.Config,
	configMapPath, testNamespace string) (
	context.Context, *HealthEventsAnalyzerTestContext) {
	t.Helper()
	client, err := c.NewClient()
	require.NoError(t, err)

	gpuNodes, err := GetAllNodesNames(ctx, client)
	require.NoError(t, err, "failed to get nodes")
	require.True(t, len(gpuNodes) > 0, "no gpu nodes found")

	gpuNodeName := gpuNodes[rand.Intn(len(gpuNodes))]

	testCtx := &HealthEventsAnalyzerTestContext{
		TestNamespace: testNamespace,
		NodeName:      gpuNodeName,
	}

	t.Logf("Cleaning up any existing node conditions for node %s", testCtx.NodeName)
	err = SendHealthEventsToNodes([]string{testCtx.NodeName}, "data/health-event-analyzer-healthy-event.json", ERRORCODE_13, "")
	require.NoError(t, err, "failed to send healthy event")

	t.Log("Backing up current health-events-analyzer configmap")
	backupData, err := BackupConfigMap(ctx, client, "health-events-analyzer-config", NVSentinelNamespace)
	require.NoError(t, err)
	t.Log("Backup created in memory")
	testCtx.ConfigMapBackup = backupData

	err = applyHealthEventsAnalyzerConfigAndRestart(ctx, t, client, configMapPath)
	require.NoError(t, err)

	return ctx, testCtx
}

func applyHealthEventsAnalyzerConfigAndRestart(ctx context.Context, t *testing.T, client klient.Client, configMapPath string) error {
	t.Helper()
	t.Logf("Applying health-events-analyzer configmap: %s", configMapPath)
	err := createConfigMapFromFilePath(ctx, client, configMapPath, "health-events-analyzer-config", NVSentinelNamespace)
	if err != nil {
		return err
	}

	t.Log("Restarting health-events-analyzer deployment")
	err = RestartDeployment(ctx, t, client, "health-events-analyzer", NVSentinelNamespace)
	return err
}

func TriggerMultipleRemediationsCycle(ctx context.Context, t *testing.T, client klient.Client, nodeName string) {
	xidsToInject := []string{ERRORCODE_13, ERRORCODE_48, ERRORCODE_13, ERRORCODE_48, ERRORCODE_13}

	// inject 5 fatal errors and let the remediation cycle finish
	t.Logf("Injecting fatal errors to node %s", nodeName)
	for _, xid := range xidsToInject {
		waitForRemediationToComplete(ctx, t, client, nodeName, xid)
	}

}

func waitForRemediationToComplete(ctx context.Context, t *testing.T, client klient.Client, nodeName, xid string) {
	err := SendHealthEventsToNodes([]string{nodeName}, "data/fatal-health-event.json", xid, "")
	require.NoError(t, err, "failed to send fatal events")

	rebootNodeCR := WaitForRebootNodeCR(ctx, t, client, nodeName)
	require.NotNil(t, rebootNodeCR, "RebootNode CR should be created for XID 13 error")

	err = DeleteRebootNodeCR(ctx, client, rebootNodeCR)
	require.NoError(t, err, "failed to delete RebootNode CR")

	err = SendHealthEventsToNodes([]string{nodeName}, "data/healthy-event.json", xid, "")
	require.NoError(t, err, "failed to send healthy events")
}

func TeardownHealthEventsAnalyzer(ctx context.Context, t *testing.T,
	c *envconf.Config, nodeName string, configMapBackup []byte) context.Context {
	t.Logf("Starting cleanup for node %s", nodeName)

	err := SendHealthEventsToNodes([]string{nodeName}, "data/health-event-analyzer-healthy-event.json", ERRORCODE_31, "MultipleRemediations")
	require.NoError(t, err, "failed to send healthy event")

	SendHealthyEvent(ctx, t, nodeName)
	restoreHealthEventsAnalyzerConfig(ctx, t, c, configMapBackup)

	return ctx
}

// restoreHealthEventsAnalyzerConfig restores the health-events-analyzer config from backup and restarts the deployment.
func restoreHealthEventsAnalyzerConfig(ctx context.Context, t *testing.T, c *envconf.Config, configMapBackup []byte) {
	t.Helper()
	client, err := c.NewClient()
	require.NoError(t, err)

	t.Log("Restoring configmap from memory")

	err = createConfigMapFromBytes(ctx, client, configMapBackup, "health-events-analyzer-config", NVSentinelNamespace)
	require.NoError(t, err)

	err = RestartDeployment(ctx, t, client, "health-events-analyzer", NVSentinelNamespace)
	require.NoError(t, err)
}
