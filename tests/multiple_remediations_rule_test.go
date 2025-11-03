// Copyright (c) 2025, NVIDIA CORPORATION.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package tests

import (
	"context"
	"math/rand"
	"testing"
	"tests/helpers"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

const (
	ERRORCODE_13 = "13"
	ERRORCODE_48 = "48"
	ERRORCODE_31 = "31"
)

func TestMultipleRemediationsCompleted(t *testing.T) {
	type contextKey int

	const (
		keyGpuNodeName contextKey = iota
		keyOriginalConfig
	)

	feature := features.New("TestMultipleRemediationsCompleted").
		WithLabel("suite", "health-event-analyzer")

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create kubernetes client")

		gpuNodes, err := helpers.GetAllNodesNames(ctx, client)
		assert.NoError(t, err, "failed to get nodes")
		assert.True(t, len(gpuNodes) > 0, "no gpu nodes found")

		gpuNodeName := gpuNodes[rand.Intn(len(gpuNodes))]
		ctx = context.WithValue(ctx, keyGpuNodeName, gpuNodeName)

		t.Logf("Cleaning up any existing node conditions for node %s", gpuNodeName)
		err = helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_13, "data/health-event-analyzer-healthy-event.json", "")
		assert.NoError(t, err, "failed to send healthy event")

		t.Logf("Backing up original health-events-analyzer config")
		originalConfig, err := helpers.GetConfigMap(ctx, client, "health-events-analyzer-config", "nvsentinel")
		assert.NoError(t, err, "failed to get original config")
		ctx = context.WithValue(ctx, keyOriginalConfig, originalConfig)

		err = helpers.UpdateConfig(ctx, t, client, "data/health-events-analyzer-config.yaml", "health-events-analyzer")
		assert.NoError(t, err, "failed to update health-events-analyzer config")

		return ctx
	})

	feature.Assess("Inject multiple fatal errors", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		gpuNodeName := ctx.Value(keyGpuNodeName).(string)
		
		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create kubernetes client")

		xidsToInject := []string{ERRORCODE_13, ERRORCODE_48, ERRORCODE_13, ERRORCODE_48, ERRORCODE_13}

		// inject 5 fatal errors and let the remediation cycle finish
		t.Logf("Injecting fatal errors to node %s", gpuNodeName)
		for _, xid := range xidsToInject {
			err = helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, xid, "data/fatal-health-event.json", "")
			assert.NoError(t, err, "failed to send fatal events")
			// Wait for RebootNode CR to be created and completed
			rebootNodeCR := helpers.WaitForRebootNodeCR(ctx, t, client, gpuNodeName)
			assert.NotNil(t, rebootNodeCR, "RebootNode CR should be created for XID 13 error")
			err = helpers.DeleteRebootNodeCR(ctx, client, rebootNodeCR)
			assert.NoError(t, err, "failed to delete RebootNode CR")

			err = helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, xid, "data/healthy-event.json", "")
			assert.NoError(t, err, "failed to send healthy events")
		}

		return ctx
	})

	feature.Assess("Check if MultipleRemediations node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		gpuNodeName := ctx.Value(keyGpuNodeName).(string)

		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create client")

		err = helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_31, "data/fatal-health-event.json", "")
		assert.NoError(t, err, "failed to send fatal events")

		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, gpuNodeName, "MultipleRemediations", "ErrorCode:31 GPU:0 XID error occurred Recommended Action=CONTACT_SUPPORT;")

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		gpuNodeName := ctx.Value(keyGpuNodeName).(string)

		t.Logf("Starting cleanup for node %s", gpuNodeName)

		err := helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_31, "data/health-event-analyzer-healthy-event.json", "MultipleRemediations")
		assert.NoError(t, err, "failed to send healthy event")

		err = helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_31, "data/healthy-event.json", "")
		assert.NoError(t, err, "failed to send healthy events")

		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create kubernetes client")

		if originalConfig := ctx.Value(keyOriginalConfig); originalConfig != nil {
			t.Logf("Restoring original health-events-analyzer config")
			err = helpers.ApplyConfigMap(ctx, t, client, originalConfig.(*v1.ConfigMap), "health-events-analyzer")
			assert.NoError(t, err, "failed to restore original config")
		}

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}

func TestMultipleRemediationsNotTriggered(t *testing.T) {
	type contextKey int

	const (
		keyGpuNodeName contextKey = iota
		keyOriginalConfig
	)

	feature := features.New("TestMultipleRemediationsNotTriggered").
		WithLabel("suite", "health-event-analyzer")

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create kubernetes client")

		gpuNodes, err := helpers.GetAllNodesNames(ctx, client)
		assert.NoError(t, err, "failed to get nodes")

		gpuNodeName := gpuNodes[rand.Intn(len(gpuNodes))]
		ctx = context.WithValue(ctx, keyGpuNodeName, gpuNodeName)

		t.Logf("Cleaning up any existing node conditions for node %s", gpuNodeName)
		err = helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_13, "data/health-event-analyzer-healthy-event.json", "")
		assert.NoError(t, err, "failed to send healthy event")

		t.Logf("Backing up original health-events-analyzer config")
		originalConfig, err := helpers.GetConfigMap(ctx, client, "health-events-analyzer-config", "nvsentinel")
		assert.NoError(t, err, "failed to get original config")
		ctx = context.WithValue(ctx, keyOriginalConfig, originalConfig)

		err = helpers.UpdateConfig(ctx, t, client, "data/health-events-analyzer-config.yaml", "health-events-analyzer")
		assert.NoError(t, err, "failed to update health-events-analyzer config")

		return ctx
	})

	feature.Assess("Inject multiple fatal errors", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		gpuNodeName := ctx.Value(keyGpuNodeName).(string)

		t.Logf("Injecting non-fatal events to node %s", gpuNodeName)
		for i := 0; i < 5; i++ {
			err := helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_13, "data/non-fatal-health-event.json", "")
			assert.NoError(t, err, "failed to send fatal events")

			err = helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_13, "data/healthy-event.json", "")
			assert.NoError(t, err, "failed to send healthy events")
		}

		return ctx
	})

	feature.Assess("Check if MultipleRemediations node condition is NOT added for non-fatal events", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		gpuNodeName := ctx.Value(keyGpuNodeName).(string)

		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create client")

		err = helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_13, "data/non-fatal-health-event.json", "")
		assert.NoError(t, err, "failed to send non-fatal events")

		helpers.EnsureNodeConditionNotPresent(ctx, t, client, gpuNodeName, "MultipleRemediations")

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		gpuNodeName := ctx.Value(keyGpuNodeName).(string)

		t.Logf("Starting cleanup for node %s", gpuNodeName)

		err := helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_13, "data/healthy-event.json", "")
		assert.NoError(t, err, "failed to send healthy events")

		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create kubernetes client")

		if originalConfig := ctx.Value(keyOriginalConfig); originalConfig != nil {
			t.Logf("Restoring original health-events-analyzer config")
			err = helpers.ApplyConfigMap(ctx, t, client, originalConfig.(*v1.ConfigMap), "health-events-analyzer")
			assert.NoError(t, err, "failed to restore original config")
		}

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}
