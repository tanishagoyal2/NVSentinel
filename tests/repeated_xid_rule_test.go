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
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

func TestRepeatedXIDRule(t *testing.T) {
	type contextKey int

	const (
		keyGpuNodes contextKey = iota
		keyGpuNodeName
	)

	feature := features.New("TestRepeatedXIDRule").
		WithLabel("suite", "health-event-analyzer")

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create kubernetes client")

		gpuNodes, err := helpers.GetAllNodesNames(ctx, client)
		assert.NoError(t, err, "failed to get nodes")
		assert.True(t, len(gpuNodes) > 0, "no gpu nodes found")
		gpuNodeName := gpuNodes[rand.Intn(len(gpuNodes))]
		ctx = context.WithValue(ctx, keyGpuNodeName, gpuNodeName)

		return ctx
	})

	feature.Assess("Inject multiple fatal errors", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		gpuNodeName := ctx.Value(keyGpuNodeName).(string)
		if gpuNodeName == "" {
			t.Fatal("GPU node name not found in context - previous assess step may have failed")
		}
		t.Logf("Injecting fatal events to node %s", gpuNodeName)

		for i := 0; i < 5; i++ {
			err := helpers.SendHealthEventsToNodes([]string{gpuNodeName}, "13", "data/fatal-health-event.json")
			assert.NoError(t, err, "failed to send fatal events")
		}

		return ctx
	})

	feature.Assess("Check if health event analyzer published a new fatal event", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		gpuNodeName, ok := ctx.Value(keyGpuNodeName).(string)
		if !ok || gpuNodeName == "" {
			t.Fatal("GPU node name not found in context - previous assess step may have failed")
		}

		defer func() {
			t.Logf("Starting cleanup for node %s", gpuNodeName)

			err := helpers.TestCleanUp(ctx, gpuNodeName, "RepeatedXid13", "13", c)
			assert.NoError(t, err, "failed to cleanup node condition and uncordon node %s", gpuNodeName)
			t.Logf("Successfully cleaned up node condition and uncordoned node %s", gpuNodeName)
		}()

		t.Logf("Checking node conditions and MongoDB for node %s", gpuNodeName)

		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create client")

		// Check node condition for matched ruleset
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, gpuNodeName, "RepeatedXid13")

		// Check MongoDB for health event with checkName = "RepeatedXid13"
		filter := bson.M{
			"healthevent.nodename":  gpuNodeName,
			"healthevent.checkname": "RepeatedXid13",
		}

		event, err := helpers.QueryHealthEventByFilter(ctx, filter)
		assert.NoError(t, err, "failed to query health event from MongoDB")
		assert.NotNil(t, event, "health event should exist in MongoDB")

		// Verify the event has the expected checkName
		if healthEvent, ok := event["healthevent"].(map[string]interface{}); ok {
			nodeName, ok := healthEvent["nodename"].(string)
			assert.True(t, ok, "nodename should be a string")
			assert.Equal(t, gpuNodeName, nodeName, "nodeName should be the same as the node name")

			checkName, ok := healthEvent["checkname"].(string)
			assert.True(t, ok, "checkname should be a string")
			assert.Equal(t, "RepeatedXid13", checkName, "checkName should be RepeatedXid13")
			t.Logf("Successfully verified health event in MongoDB with checkName: %s", checkName)
		} else {
			t.Fatal("failed to extract healthevent from MongoDB document")
		}

		err = helpers.SendHealthEventsToNodes([]string{gpuNodeName}, "13", "data/healthy-event.json")
		assert.NoError(t, err, "failed to send healthy events")

		time.Sleep(5 * time.Second)

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}
