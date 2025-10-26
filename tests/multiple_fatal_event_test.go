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
	"go.mongodb.org/mongo-driver/bson"
	"k8s.io/klog/v2"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

func TestMultipleFatalEventRule(t *testing.T) {
	type contextKey int
	var GpuNodeName string

	const (
		keyGpuNodes  contextKey = iota
		ERRORCODE_13            = "13"
		ERRORCODE_48            = "48"
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
		GpuNodeName = gpuNodes[rand.Intn(len(gpuNodes))]
		t.Logf("Injecting fatal events to node %s", GpuNodeName)

		// inject 5 fatal errors and let the remediation cycle finish

		// inject XID 13 error
		err := helpers.SendHealthEventsToNodes([]string{GpuNodeName}, ERRORCODE_13, "data/fatal-health-event.json")
		assert.NoError(t, err, "failed to send fatal events")
		time.Sleep(10 * time.Second)

		err = helpers.SendHealthEventsToNodes([]string{GpuNodeName}, ERRORCODE_13, "data/healthy-event.json")
		assert.NoError(t, err, "failed to send healthy events")
		time.Sleep(5 * time.Second)

		// inject XID 48 error
		err = helpers.SendHealthEventsToNodes([]string{GpuNodeName}, ERRORCODE_48, "data/fatal-health-event.json")
		assert.NoError(t, err, "failed to send fatal events")
		time.Sleep(10 * time.Second)

		err = helpers.SendHealthEventsToNodes([]string{GpuNodeName}, ERRORCODE_48, "data/healthy-event.json")
		assert.NoError(t, err, "failed to send healthy events")
		time.Sleep(5 * time.Second)

		// inject XID 13 error
		err = helpers.SendHealthEventsToNodes([]string{GpuNodeName}, ERRORCODE_13, "data/fatal-health-event.json")
		assert.NoError(t, err, "failed to send fatal events")
		time.Sleep(10 * time.Second)

		err = helpers.SendHealthEventsToNodes([]string{GpuNodeName}, ERRORCODE_13, "data/healthy-event.json")
		assert.NoError(t, err, "failed to send healthy events")
		time.Sleep(5 * time.Second)

		// inject XID 48 error
		err = helpers.SendHealthEventsToNodes([]string{GpuNodeName}, ERRORCODE_48, "data/fatal-health-event.json")
		assert.NoError(t, err, "failed to send fatal events")
		time.Sleep(10 * time.Second)

		err = helpers.SendHealthEventsToNodes([]string{GpuNodeName}, ERRORCODE_48, "data/healthy-event.json")
		assert.NoError(t, err, "failed to send healthy events")
		time.Sleep(5 * time.Second)

		// inject XID 13 error
		err = helpers.SendHealthEventsToNodes([]string{GpuNodeName}, ERRORCODE_13, "data/fatal-health-event.json")
		assert.NoError(t, err, "failed to send fatal events")
		time.Sleep(10 * time.Second)

		err = helpers.SendHealthEventsToNodes([]string{GpuNodeName}, ERRORCODE_13, "data/healthy-event.json")
		assert.NoError(t, err, "failed to send healthy events")
		time.Sleep(5 * time.Second)

		return ctx
	})

	feature.Assess("Check if health event analyzer published a new fatal event", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		// Ensure cleanup at the end of the test
		defer func() {
			t.Logf("Starting cleanup for node %s", GpuNodeName)

			err := helpers.TestCleanUp(ctx, GpuNodeName, "MultipleFatalError", "31", c)
			assert.NoError(t, err, "failed to cleanup node condition and uncordon node %s", GpuNodeName)
			t.Logf("Successfully cleaned up node condition and uncordoned node %s", GpuNodeName)
		}()

		// inject XID 48 error to trigger the rule
		err := helpers.SendHealthEventsToNodes([]string{GpuNodeName}, "31", "data/fatal-health-event.json")
		assert.NoError(t, err, "failed to send fatal events")
		time.Sleep(10 * time.Second)

		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create client")

		// Check node condition for matched ruleset
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, GpuNodeName, "MultipleFatalError")
		// Check MongoDB for health event with checkName = "MultipleFatalError"
		filter := bson.M{
			"healthevent.nodename":  GpuNodeName,
			"healthevent.checkname": "MultipleFatalError",
		}

		event, err := helpers.QueryHealthEventByFilter(ctx, filter)
		assert.NoError(t, err, "failed to query health event from MongoDB")
		assert.NotNil(t, event, "health event should exist in MongoDB")

		// Verify the event has the expected checkName
		if healthEvent, ok := event["healthevent"].(map[string]interface{}); ok {
			nodeName, ok := healthEvent["nodename"].(string)
			assert.True(t, ok, "nodename should be a string")
			assert.Equal(t, GpuNodeName, nodeName, "nodeName should be the same as the node name")

			checkName, ok := healthEvent["checkname"].(string)
			assert.True(t, ok, "checkname should be a string")
			assert.Equal(t, "MultipleFatalError", checkName, "checkName should be MultipleFatalError")
			t.Logf("Successfully verified health event in MongoDB with checkName: %s", checkName)
		} else {
			t.Fatal("failed to extract healthevent from MongoDB document")
		}

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}
