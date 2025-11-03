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
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

func TestMultipleFatalEventRule(t *testing.T) {
	type contextKey int

	const (
		keyGpuNodeName contextKey = iota
		ERRORCODE_13              = "13"
		ERRORCODE_48              = "48"
		ERRORCODE_31              = "31"
	)

	feature := features.New("TestMultipleFatalEventRule").
		WithLabel("suite", "health-event-analyzer")

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create kubernetes client")

		gpuNodes, err := helpers.GetAllNodesNames(ctx, client)
		assert.NoError(t, err, "failed to get nodes")
		assert.True(t, len(gpuNodes) > 0, "no gpu nodes found")

		gpuNodeName := gpuNodes[rand.Intn(len(gpuNodes))]
		ctx = context.WithValue(ctx, keyGpuNodeName, gpuNodeName)

		// clean up any existing node conditions
		t.Logf("Cleaning up any existing node conditions for node %s", gpuNodeName)
		err = helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_13, "data/health-event-analyzer-healthy-event.json", "")
		assert.NoError(t, err, "failed to send healthy event")

		return ctx
	})

	feature.Assess("Inject multiple fatal errors", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		gpuNodeName := ctx.Value(keyGpuNodeName).(string)
		t.Logf("Injecting fatal events to node %s", gpuNodeName)

		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create kubernetes client")

		xidsToInject := []string{ERRORCODE_13, ERRORCODE_48, ERRORCODE_13, ERRORCODE_48, ERRORCODE_13}

		// inject 5 fatal errors and let the remediation cycle finish
		t.Logf("Injecting fatal errors to node %s", gpuNodeName)
		for _, xid := range xidsToInject {
			// inject XID error
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

	feature.Assess("Check if MultipleFatalError node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		// Get GPU node name from context
		gpuNodeName := ctx.Value(keyGpuNodeName).(string)

		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create client")

		// inject XID 31 error to trigger the rule
		err = helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_31, "data/fatal-health-event.json", "")
		assert.NoError(t, err, "failed to send fatal events")

		// Check node condition for matched ruleset
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, gpuNodeName, "MultipleFatalError", "ErrorCode:31 GPU:0 XID error occurred Recommended Action=CONTACT_SUPPORT;")

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		gpuNodeName := ctx.Value(keyGpuNodeName).(string)

		t.Logf("Starting cleanup for node %s", gpuNodeName)

		err := helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_31, "data/health-event-analyzer-healthy-event.json", "MultipleFatalError")
		assert.NoError(t, err, "failed to send healthy event")

		err = helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_31, "data/healthy-event.json", "")
		assert.NoError(t, err, "failed to send healthy events")

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}

func TestMultipleNonFatalEventRule(t *testing.T) {
	type contextKey int

	const (
		keyGpuNodeName contextKey = iota
		ERRORCODE_13              = "13"
	)

	feature := features.New("TestMultipleNonFatalEventRule").
		WithLabel("suite", "health-event-analyzer")

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create kubernetes client")

		gpuNodes, err := helpers.GetAllNodesNames(ctx, client)
		assert.NoError(t, err, "failed to get nodes")

		gpuNodeName := gpuNodes[rand.Intn(len(gpuNodes))]
		ctx = context.WithValue(ctx, keyGpuNodeName, gpuNodeName)

		// clean up any existing node conditions
		t.Logf("Cleaning up any existing node conditions for node %s", gpuNodeName)
		err = helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_13, "data/health-event-analyzer-healthy-event.json", "")
		assert.NoError(t, err, "failed to send healthy event")

		return ctx
	})

	feature.Assess("Inject multiple fatal errors", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		gpuNodeName := ctx.Value(keyGpuNodeName).(string)

		t.Logf("Injecting non-fatal events to node %s", gpuNodeName)
		for i := 0; i < 5; i++ {
			// inject XID error
			err := helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_13, "data/non-fatal-health-event.json", "")
			assert.NoError(t, err, "failed to send fatal events")

			err = helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_13, "data/healthy-event.json", "")
			assert.NoError(t, err, "failed to send healthy events")
		}

		return ctx
	})

	feature.Assess("Check if MultipleFatalError node condition is NOT added for non-fatal events", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		// Get GPU node name from context
		gpuNodeName := ctx.Value(keyGpuNodeName).(string)

		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create client")

		// inject XID 13 non-fatal error - should NOT trigger the MultipleFatalError rule
		err = helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_13, "data/non-fatal-health-event.json", "")
		assert.NoError(t, err, "failed to send non-fatal events")

		// Ensure node condition is NOT added since these are non-fatal events
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, gpuNodeName, "MultipleFatalError")

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		gpuNodeName := ctx.Value(keyGpuNodeName).(string)

		t.Logf("Starting cleanup for node %s", gpuNodeName)

		err := helpers.SendHealthEventsToNodes(t, []string{gpuNodeName}, ERRORCODE_13, "data/healthy-event.json", "")
		assert.NoError(t, err, "failed to send healthy events")

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}
