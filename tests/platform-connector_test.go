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
	"testing"
	"tests/helpers"

	"github.com/nvidia/nvsentinel/data-models/pkg/protos"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
)

type PlatformConnectorTestContext struct {
	NodeName        string
	ConfigMapBackup []byte
	TestNamespace   string
}

func TestPlatformConnectorWithProcessingStrategy(t *testing.T) {
	feature := features.New("TestPlatformConnector").
		WithLabel("suite", "platform-connector")

	var testCtx *PlatformConnectorTestContext

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		nodeName := helpers.SelectTestNodeFromUnusedPool(ctx, t, client)

		testCtx = &PlatformConnectorTestContext{
			NodeName: nodeName,
		}

		return ctx
	})

	feature.Assess("Check that platform connector is not applying node events/conditions for STORE_ONLY events", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		event := helpers.NewHealthEvent(testCtx.NodeName).
			WithErrorCode(helpers.ERRORCODE_79).
			WithMessage("XID error occurred").
			WithProcessingStrategy(int(protos.ProcessingStrategy_STORE_ONLY))
		helpers.SendHealthEvent(ctx, t, event)

		t.Logf("Node %s should not have condition SysLogsXIDError", testCtx.NodeName)
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testCtx.NodeName, "SysLogsXIDError")

		event = helpers.NewHealthEvent(testCtx.NodeName).
			WithErrorCode(helpers.ERRORCODE_31).
			WithMessage("XID error occurred").
			WithFatal(false).
			WithProcessingStrategy(int(protos.ProcessingStrategy_STORE_ONLY))
		helpers.SendHealthEvent(ctx, t, event)

		t.Logf("Node %s should not have event SysLogsXIDError", testCtx.NodeName)
		helpers.EnsureNodeEventNotPresent(ctx, t, client, testCtx.NodeName, "SysLogsXIDError", "SysLogsXIDErrorIsNotHealthy")

		event = helpers.NewHealthEvent(testCtx.NodeName).
			WithErrorCode(helpers.ERRORCODE_79).
			WithMessage("XID error occurred").
			WithProcessingStrategy(int(protos.ProcessingStrategy_EXECUTE_REMEDIATION))
		helpers.SendHealthEvent(ctx, t, event)

		t.Logf("Node %s should have condition SysLogsXIDError", testCtx.NodeName)
		helpers.CheckNodeConditionExists(ctx, client, testCtx.NodeName, "SysLogsXIDError", "SysLogsXIDErrorIsNotHealthy")

		event = helpers.NewHealthEvent(testCtx.NodeName).
			WithErrorCode(helpers.ERRORCODE_31).
			WithMessage("XID error occurred").
			WithFatal(false).
			WithProcessingStrategy(int(protos.ProcessingStrategy_EXECUTE_REMEDIATION))
		helpers.SendHealthEvent(ctx, t, event)

		t.Logf("Node %s should have event SysLogsXIDError", testCtx.NodeName)
		helpers.CheckNodeEventExists(ctx, client, testCtx.NodeName, "SysLogsXIDError", "SysLogsXIDErrorIsNotHealthy")

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		helpers.SendHealthyEvent(ctx, t, testCtx.NodeName)
		return ctx
	})

	testEnv.Test(t, feature.Feature())
}
