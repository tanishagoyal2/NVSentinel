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
	"time"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

func TestMultipleFatalEventRule(t *testing.T) {
	type contextKey int

	const (
		keyGpuNodes contextKey = iota
		keyGpuNodeName
		ERRORCODE_13 = "13"
		ERRORCODE_48 = "48"
		ERRORCODE_31 = "31"
	)

	feature := features.New("TestMultipleFatalEventRule").
		WithLabel("suite", "health-event-analyzer")

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create kubernetes client")

		gpuNodes, err := helpers.GetAllNodesNames(ctx, client)
		assert.NoError(t, err, "failed to get nodes")

		ctx = context.WithValue(ctx, keyGpuNodes, gpuNodes)

		return ctx
	})

	feature.Assess("Inject multiple fatal errors", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		gpuNodes := ctx.Value(keyGpuNodes).([]string)
		assert.True(t, len(gpuNodes) > 0, "no gpu nodes found")
		gpuNodeName := gpuNodes[rand.Intn(len(gpuNodes))]
		ctx = context.WithValue(ctx, keyGpuNodeName, gpuNodeName)
		t.Logf("Injecting fatal events to node %s", gpuNodeName)

		// inject 5 fatal errors and let the remediation cycle finish

		// inject XID 13 error
		err := helpers.SendHealthEventsToNodes([]string{gpuNodeName}, ERRORCODE_13, "data/fatal-health-event.json", "")
		assert.NoError(t, err, "failed to send fatal events")
		time.Sleep(10 * time.Second)

		err = helpers.SendHealthEventsToNodes([]string{gpuNodeName}, ERRORCODE_13, "data/healthy-event.json", "")
		assert.NoError(t, err, "failed to send healthy events")
		time.Sleep(5 * time.Second)

		// inject XID 48 error
		err = helpers.SendHealthEventsToNodes([]string{gpuNodeName}, ERRORCODE_48, "data/fatal-health-event.json", "")
		assert.NoError(t, err, "failed to send fatal events")
		time.Sleep(10 * time.Second)

		err = helpers.SendHealthEventsToNodes([]string{gpuNodeName}, ERRORCODE_48, "data/healthy-event.json", "")
		assert.NoError(t, err, "failed to send healthy events")
		time.Sleep(5 * time.Second)

		// inject XID 13 error
		err = helpers.SendHealthEventsToNodes([]string{gpuNodeName}, ERRORCODE_13, "data/fatal-health-event.json", "")
		assert.NoError(t, err, "failed to send fatal events")
		time.Sleep(10 * time.Second)

		err = helpers.SendHealthEventsToNodes([]string{gpuNodeName}, ERRORCODE_13, "data/healthy-event.json", "")
		assert.NoError(t, err, "failed to send healthy events")
		time.Sleep(5 * time.Second)

		// inject XID 48 error
		err = helpers.SendHealthEventsToNodes([]string{gpuNodeName}, ERRORCODE_48, "data/fatal-health-event.json", "")
		assert.NoError(t, err, "failed to send fatal events")
		time.Sleep(10 * time.Second)

		err = helpers.SendHealthEventsToNodes([]string{gpuNodeName}, ERRORCODE_48, "data/healthy-event.json", "")
		assert.NoError(t, err, "failed to send healthy events")
		time.Sleep(5 * time.Second)

		// inject XID 13 error
		err = helpers.SendHealthEventsToNodes([]string{gpuNodeName}, ERRORCODE_13, "data/fatal-health-event.json", "")
		assert.NoError(t, err, "failed to send fatal events")
		time.Sleep(10 * time.Second)

		err = helpers.SendHealthEventsToNodes([]string{gpuNodeName}, ERRORCODE_13, "data/healthy-event.json", "")
		assert.NoError(t, err, "failed to send healthy events")
		time.Sleep(5 * time.Second)

		return ctx
	})

	feature.Assess("Check if health event analyzer published a new fatal event", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		// Get GPU node name from context
		gpuNodeName, ok := ctx.Value(keyGpuNodeName).(string)
		if !ok || gpuNodeName == "" {
			t.Fatal("GPU node name not found in context - previous assess step may have failed")
		}

		// inject XID 31 error to trigger the rule
		err := helpers.SendHealthEventsToNodes([]string{gpuNodeName}, ERRORCODE_31, "data/fatal-health-event.json", "")
		assert.NoError(t, err, "failed to send fatal events")
		time.Sleep(10 * time.Second)

		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create client")

		// Check node condition for matched ruleset
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, gpuNodeName, "MultipleFatalError")

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		gpuNodeName := ctx.Value(keyGpuNodeName).(string)

		t.Logf("Starting cleanup for node %s", gpuNodeName)

		err := helpers.SendHealthEventsToNodes([]string{gpuNodeName}, ERRORCODE_31, "data/health-event-analyzer-healthy-event.json", "MultipleFatalError")
		assert.NoError(t, err, "failed to send healthy events")

		err = helpers.SendHealthEventsToNodes([]string{gpuNodeName}, ERRORCODE_31, "data/healthy-event.json", "")
		assert.NoError(t, err, "failed to send healthy events")

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}
