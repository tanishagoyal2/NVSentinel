//go:build amd64_group
// +build amd64_group

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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"
	"tests/helpers"

	pb "github.com/nvidia/nvsentinel/data-models/pkg/protos"
)

func TestMultipleRemediationsCompleted(t *testing.T) {
	feature := features.New("TestMultipleRemediationsCompleted").
		WithLabel("suite", "health-event-analyzer")

	var testCtx *helpers.HealthEventsAnalyzerTestContext

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		var newCtx context.Context
		newCtx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test")

		t.Log("Waiting 90 seconds for the MultipleRemediations rule time window to complete")
		time.Sleep(90 * time.Second)

		t.Log("Triggering multiple remediations cycle")
		client, err := c.NewClient()
		require.NoError(t, err)
		helpers.TriggerMultipleRemediationsCycle(ctx, t, client, testCtx.NodeName)

		return newCtx
	})

	feature.Assess("Check if MultipleRemediations node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create client")
		gpuNodeName := testCtx.NodeName

		event := helpers.NewHealthEvent(gpuNodeName).
			WithFatal(true).
			WithErrorCode(helpers.ERRORCODE_31).
			WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM))
		helpers.SendHealthEvent(ctx, t, event)

		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, gpuNodeName, "MultipleRemediations",
			"ErrorCode:31 GPU:0 Recommended Action=CONTACT_SUPPORT;", "MultipleRemediationsIsNotHealthy", v1.ConditionTrue)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		helpers.SendHealthyEvent(ctx, t, testCtx.NodeName)

		return helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testCtx.NodeName, testCtx.ConfigMapBackup)
	})

	testEnv.Test(t, feature.Feature())
}

func TestMultipleRemediationsNotTriggered(t *testing.T) {
	feature := features.New("TestMultipleRemediationsNotTriggered").
		WithLabel("suite", "health-event-analyzer")

	var testCtx *helpers.HealthEventsAnalyzerTestContext

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)
		testNodeName := helpers.AcquireNodeFromPool(ctx, t, client, helpers.DefaultExpiry)

		var newCtx context.Context
		newCtx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test", testNodeName)

		gpuNodeName := testCtx.NodeName

		t.Logf("Injecting non-fatal events to node %s", gpuNodeName)
		for range 5 {
			event := helpers.NewHealthEvent(gpuNodeName).
				WithFatal(false).
				WithErrorCode(helpers.ERRORCODE_13).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM))

			helpers.SendHealthEvent(ctx, t, event)

			helpers.SendHealthyEvent(ctx, t, gpuNodeName)
		}

		return newCtx
	})

	feature.Assess("Check if MultipleRemediations node condition is NOT added for non-fatal events", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		gpuNodeName := testCtx.NodeName

		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create client")

		event := helpers.NewHealthEvent(gpuNodeName).
			WithFatal(false).
			WithErrorCode(helpers.ERRORCODE_13).
			WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM))
		helpers.SendHealthEvent(ctx, t, event)

		helpers.EnsureNodeConditionNotPresent(ctx, t, client, gpuNodeName, "MultipleRemediations")

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		helpers.SendHealthyEvent(ctx, t, testCtx.NodeName)

		return helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testCtx.NodeName, testCtx.ConfigMapBackup)
	})

	testEnv.Test(t, feature.Feature())
}

func TestRepeatedXIDOnSameGPU(t *testing.T) {
	// Works with both MongoDB ($setWindowFields pipeline) and PostgreSQL (XidBurstDetector).
	feature := features.New("TestRepeatedXIDOnSameGPU").
		WithLabel("suite", "health-event-analyzer")

	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		testNodeName = helpers.AcquireNodeFromPool(ctx, t, client, helpers.DefaultExpiry)

		var newCtx context.Context
		newCtx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test", testNodeName)

		return newCtx
	})

	feature.Assess("Inject multiple XID errors and check if node condition is added if required", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create client")

		entities := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
		}

		entitiesImpacted = append(entitiesImpacted, entities)

		// Burst 1: 5 events within 10s gaps (same burst)
		// Burst 1 contents: XID 119 (x2), 120, 48, 31
		// Expectations: No trigger yet (need at least 2 bursts to trigger)
		errorCodes := []string{helpers.ERRORCODE_119, helpers.ERRORCODE_120, helpers.ERRORCODE_48, helpers.ERRORCODE_119, helpers.ERRORCODE_31}
		for _, errorCode := range errorCodes {
			helpers.SendHealthEvent(ctx, t, helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities).
				WithFatal(true).
				WithErrorCode(errorCode).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
			)
		}

		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "RepeatedXIDErrorOnSameGPU")

		t.Log("Waiting 22s to create burst gap (>20s required)")
		time.Sleep(22 * time.Second)

		// Burst 2: XID 120 (non-sticky) creates new burst after 22s gap
		// Burst 2 initial contents: XID 120, 79
		// Expectations: XID 120 triggers (appears in Burst 1 and Burst 2)
		errorCodes = []string{helpers.ERRORCODE_120, helpers.ERRORCODE_79}
		for _, errorCode := range errorCodes {
			helpers.SendHealthEvent(ctx, t, helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities).
				WithFatal(true).
				WithErrorCode(errorCode).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
			)
		}

		message := fmt.Sprintf("ErrorCode:%s PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 Recommended Action=CONTACT_SUPPORT;", helpers.ERRORCODE_120)
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "RepeatedXIDErrorOnSameGPU",
			message, "RepeatedXIDErrorOnSameGPUIsNotHealthy", v1.ConditionTrue)

		t.Log("Waiting 22s to create burst gap (>20s required)")
		time.Sleep(22 * time.Second)

		// Burst 2 (continued): XID 119 (sticky) arrives but merges into existing Burst 2
		// because XID 79 (sticky) occurred 22s ago (within 30s sticky window)
		// Burst 2 final contents: XID 120, 79, 119, 48
		// Expectations: 119 and 48 trigger (both appear in Burst 1 and Burst 2)
		errorCodes = []string{helpers.ERRORCODE_119, helpers.ERRORCODE_48}
		for _, errorCode := range errorCodes {
			helpers.SendHealthEvent(ctx, t, helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities).
				WithFatal(true).
				WithErrorCode(errorCode).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
			)
		}

		t.Logf("Verifying RepeatedXIDErrorOnSameGPU condition exists after events merged into Burst 2")
		message += fmt.Sprintf("ErrorCode:%s PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 Recommended Action=CONTACT_SUPPORT;", helpers.ERRORCODE_119)
		message += fmt.Sprintf("ErrorCode:%s PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 Recommended Action=CONTACT_SUPPORT;", helpers.ERRORCODE_48)
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "RepeatedXIDErrorOnSameGPU",
			message, "RepeatedXIDErrorOnSameGPUIsNotHealthy", v1.ConditionTrue)

		t.Log("Waiting 22s to create burst gap (>20s required)")
		time.Sleep(22 * time.Second)

		// Burst 3: XID 13 (non-sticky) creates new burst after 16s gap
		// Burst 3 contents: XID 13, 31
		// Expectations: XID 31 triggers (appears in Burst 1 and Burst 3)
		errorCodes = []string{helpers.ERRORCODE_13, helpers.ERRORCODE_31}
		for _, errorCode := range errorCodes {
			helpers.SendHealthEvent(ctx, t, helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities).
				WithFatal(true).
				WithErrorCode(errorCode).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
			)
		}

		time.Sleep(5 * time.Second)

		// Burst 3 (continued): XID 13 arrives again after 5s gap (< 20s), stays in same burst
		// Burst 3 final contents: XID 13 (x2), 31 (x1)
		// Expectations: XID 13 will NOT trigger (only appears in Burst 3, and targetXidCount=2 in maxBurst),
		// 				 XID 31 will also not trigger as we are excluding XID 31 from RepeatedXIDErrorOnSameGPU rule
		helpers.SendHealthEvent(ctx, t, helpers.NewHealthEvent(testNodeName).
			WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
			WithCheckName("SyslogXIDError").
			WithEntitiesImpacted(entities).
			WithFatal(true).
			WithErrorCode(helpers.ERRORCODE_13).
			WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)))

		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "RepeatedXIDErrorOnSameGPU",
			message, "RepeatedXIDErrorOnSameGPUIsNotHealthy", v1.ConditionTrue)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")

			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}

		return helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)
	})

	testEnv.Test(t, feature.Feature())
}

func TestRepeatedXID31OnSameGPU(t *testing.T) {
	feature := features.New("TestRepeatedXID31OnSameGPU").
		WithLabel("suite", "health-event-analyzer")

	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		testNodeName = helpers.AcquireNodeFromPool(ctx, t, client, helpers.DefaultExpiry)

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test", testNodeName)

		t.Logf("Using node: %s", testNodeName)

		return ctx
	})

	feature.Assess("Inject multiple XID errors and check if node condition is added if required", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create client")

		entities1 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
		}

		entities2 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0002:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-22222222-2222-2222-2222-222222222222",
			},
		}

		entitiesImpacted = append(entitiesImpacted, entities1)
		entitiesImpacted = append(entitiesImpacted, entities2)

		// Burst 1: 5 events within 10s gaps (same burst)
		// Burst 1 contents: XID 119, 31
		// Expectations: No trigger yet (need at least 2 bursts to trigger)
		errorCodes := []string{helpers.ERRORCODE_119, helpers.ERRORCODE_31}
		for _, errorCode := range errorCodes {
			helpers.SendHealthEvent(ctx, t, helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(errorCode).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
			)
		}

		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "RepeatedXID31OnDifferentGPU")

		t.Log("Waiting 22s to create burst gap (>20s required)")
		time.Sleep(22 * time.Second)

		// Burst 2: XID 31 (non-sticky) creates new burst after 25s gap
		// Burst 2 initial contents: XID 31
		// Expectations: XID 31 triggers (appears in Burst 1 and Burst 2 but with different PCI addresses)
		errorCodes = []string{helpers.ERRORCODE_31}
		for _, errorCode := range errorCodes {
			helpers.SendHealthEvent(ctx, t, helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities2).
				WithFatal(true).
				WithErrorCode(errorCode).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
			)
		}

		expectedEvent := v1.Event{
			Type:    "RepeatedXID31OnDifferentGPU",
			Reason:  "RepeatedXID31OnDifferentGPUIsNotHealthy",
			Message: "ErrorCode:31 PCI:0002:00:00 GPU_UUID:GPU-22222222-2222-2222-2222-222222222222 App passing bad data or using incorrect GPU methods; check error PID to identify source of the problem, if application is known good and problem persists, then contact support Recommended Action=NONE;",
		}

		helpers.WaitForNodeEvent(ctx, t, client, testNodeName, expectedEvent)

		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "RepeatedXID31OnSameGPU")

		t.Log("Waiting 22s to create burst gap (>20s required)")
		time.Sleep(22 * time.Second)

		// Burst 3: XID 13 (non-sticky) creates new burst after 16s gap
		// Burst 3 contents: XID 13, 31
		// Expectations: XID 31 triggers (appears in Burst 1 and Burst 3)
		errorCodes = []string{helpers.ERRORCODE_13, helpers.ERRORCODE_31}
		for _, errorCode := range errorCodes {
			helpers.SendHealthEvent(ctx, t, helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(errorCode).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
			)
		}

		message := fmt.Sprintf("ErrorCode:%s PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 if DCGM EUD tests pass, run field diagnostics Recommended Action=RUN_DCGMEUD;", helpers.ERRORCODE_31)
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "RepeatedXID31OnSameGPU",
			message, "RepeatedXID31OnSameGPUIsNotHealthy", v1.ConditionTrue)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}

		return helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)
	})

	testEnv.Test(t, feature.Feature())
}

func TestRepeatedXID31OnDifferentGPU(t *testing.T) {
	feature := features.New("TestRepeatedXID31OnDifferentGPU").
		WithLabel("suite", "health-event-analyzer")

	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		// Acquire a fresh node from pool (no prior events, no time window wait needed!)
		testNodeName = helpers.AcquireNodeFromPool(ctx, t, client, helpers.DefaultExpiry)

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test", testNodeName)
		t.Logf("Using node: %s", testNodeName)

		entities1 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
		}
		entities2 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0002:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-22222222-2222-2222-2222-222222222222",
			},
		}

		entitiesImpacted = append(entitiesImpacted, entities1)
		entitiesImpacted = append(entitiesImpacted, entities2)

		errorCodes := []string{helpers.ERRORCODE_13, helpers.ERRORCODE_31}
		for _, errorCode := range errorCodes {
			helpers.SendHealthEvent(ctx, t, helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(errorCode).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
			)
		}

		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "RepeatedXID31OnDifferentGPU")

		t.Log("Waiting 22s to create burst gap (>20s required)")
		time.Sleep(22 * time.Second)

		helpers.SendHealthEvent(ctx, t, helpers.NewHealthEvent(testNodeName).
			WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
			WithCheckName("SyslogXIDError").
			WithEntitiesImpacted(entities2).
			WithFatal(true).
			WithErrorCode(helpers.ERRORCODE_31).
			WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		)

		return ctx
	})

	feature.Assess("Inject multiple XID errors and check if node condition is added if required", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		expectedEvent := v1.Event{
			Type:    "RepeatedXID31OnDifferentGPU",
			Reason:  "RepeatedXID31OnDifferentGPUIsNotHealthy",
			Message: "ErrorCode:31 PCI:0002:00:00 GPU_UUID:GPU-22222222-2222-2222-2222-222222222222 App passing bad data or using incorrect GPU methods; check error PID to identify source of the problem, if application is known good and problem persists, then contact support Recommended Action=NONE;",
		}

		helpers.WaitForNodeEvent(ctx, t, client, testNodeName, expectedEvent)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}

		return helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)
	})

	testEnv.Test(t, feature.Feature())
}

func TestXIDErrorOnGPCAndTPC(t *testing.T) {
	feature := features.New("TestXIDErrorOnGPCAndTPC").
		WithLabel("suite", "health-event-analyzer")

	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		assert.NoError(t, err, "failed to create client")

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test")

		testNodeName = testCtx.NodeName
		t.Logf("Using node: %s", testNodeName)

		entities1 := []helpers.EntityImpacted{

			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "GPC",
				EntityValue: "0",
			},
			{
				EntityType:  "TPC",
				EntityValue: "1",
			},
			{
				EntityType:  "SM",
				EntityValue: "0",
			},
		}
		entities2 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "GPC",
				EntityValue: "0",
			},
			{
				EntityType:  "TPC",
				EntityValue: "0",
			},
			{
				EntityType:  "SM",
				EntityValue: "1",
			},
		}

		entities3 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "GPC",
				EntityValue: "0",
			},
			{
				EntityType:  "TPC",
				EntityValue: "1",
			},
			{
				EntityType:  "SM",
				EntityValue: "1",
			},
		}

		entitiesImpacted = append(entitiesImpacted, entities1)
		entitiesImpacted = append(entitiesImpacted, entities2)
		entitiesImpacted = append(entitiesImpacted, entities3)

		// STEP 1: Inject two XID 13 errors on GPC:0, TPC:1, SM:0
		// EXPECTED: This alone won't trigger the "same" rule yet as it needs multiple occurrences
		// on the same GPC/TPC combination.
		t.Log("Inject XID 13 events on GPC: 0, TPC: 1, SM: 0")
		xidEvents := []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_13).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_13).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}

		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "RepeatedXID13OnDifferentGPCAndTPC")

		t.Log("Waiting 5s to create burst gap")
		time.Sleep(5 * time.Second)

		// STEP 2: Inject XID 13 error on GPC:0, TPC:0, SM:1
		// EXPECTED: This differs from the previous errors which were on GPC:0, TPC:1.
		// This should trigger the "RepeatedXID13OnDifferentGPCAndTPC" condition
		// because we have errors occurring on different processing clusters, indicating
		// a potentially broader GPU issue rather than a localized problem.
		t.Log("Inject XID 13 events on GPC: 0, TPC: 0, SM: 1")
		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities2).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_13).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}

		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		expectedEvent := v1.Event{
			Type:    "RepeatedXID13OnDifferentGPCAndTPC",
			Reason:  "RepeatedXID13OnDifferentGPCAndTPCIsNotHealthy",
			Message: "ErrorCode:13 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 GPC:0 TPC:0 SM:1 App passing bad data or using incorrect GPU methods; check error PID to identify source of the problem, if application is known good and problem persists, then contact support Recommended Action=NONE;",
		}

		helpers.WaitForNodeEvent(ctx, t, client, testNodeName, expectedEvent)

		// EXPECTED: RepeatedXID13OnSameGPCAndTPC is not present.
		// Burst 1: XID 13 on GPC: 0, TPC: 1, SM: 0
		//          XID 13 on GPC: 0, TPC: 1, SM: 0
		// Burst 2: XID 13 on GPC: 0, TPC: 0, SM: 1
		// Errors on different GPC/TPC combinations.
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "RepeatedXID13OnSameGPCAndTPC")

		t.Log("Waiting 5s to create burst gap")
		time.Sleep(5 * time.Second)

		// STEP 3: Inject XID 13 error on GPC:0, TPC:1, SM:1
		// EXPECTED: This creates a third burst on GPC:0, TPC:1, the same GPC/TPC as Burst 1.
		// Now we have:
		//   Burst 1: GPC:0, TPC:1
		//   Burst 2: GPC:0, TPC:0
		//   Burst 3: GPC:0, TPC:1
		// This should trigger the "RepeatedXID13OnDifferentGPCAndTPC" condition (bursts on different GPC/TPC)
		// and also set up the condition for "RepeatedXID13OnSameGPCAndTPC" (bursts 1 and 3 on same GPC/TPC).
		t.Log("Inject XID 13 events on GPC: 0, TPC: 1, SM: 1")
		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities3).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_13).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}

		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		expectedEvent = v1.Event{
			Type:    "RepeatedXID13OnDifferentGPCAndTPC",
			Reason:  "RepeatedXID13OnDifferentGPCAndTPCIsNotHealthy",
			Message: "ErrorCode:13 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 GPC:0 TPC:1 SM:1 App passing bad data or using incorrect GPU methods; check error PID to identify source of the problem, if application is known good and problem persists, then contact support Recommended Action=NONE;",
		}

		helpers.WaitForNodeEvent(ctx, t, client, testNodeName, expectedEvent)

		return ctx
	})

	feature.Assess("Check if RepeatedXID13OnSameGPCAndTPC node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		// EXPECTED: RepeatedXID13OnSameGPCAndTPC is present.
		// We have injected XID 13 errors in three separate bursts (>20s gaps):
		// Burst 1: XID 13 on GPC: 0, TPC: 1, SM: 0 (two events combined)
		// Burst 2: XID 13 on GPC: 0, TPC: 0, SM: 1 (different TPC)
		// Burst 3: XID 13 on GPC: 0, TPC: 1, SM: 1 (same GPC/TPC as Burst 1)
		// Bursts 1 and 3 both occur on GPC:0, TPC:1, triggering the rule.
		message := "ErrorCode:13 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 GPC:0 TPC:1 SM:1 if DCGM EUD tests pass, run field diagnostics Recommended Action=RUN_DCGMEUD;"
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "RepeatedXID13OnSameGPCAndTPC",
			message, "RepeatedXID13OnSameGPCAndTPCIsNotHealthy", v1.ConditionTrue)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}

		return helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)
	})

	testEnv.Test(t, feature.Feature())
}

func TestXID13And31SoloNoBurstRule(t *testing.T) {
	feature := features.New("TestXID13And31SoloNoBurstRule").
		WithLabel("suite", "health-event-analyzer")

	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		testNodeName = helpers.AcquireNodeFromPool(ctx, t, client, helpers.DefaultExpiry)

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test", testNodeName)
		t.Logf("Using node: %s", testNodeName)

		entities1 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "GPC",
				EntityValue: "0",
			},
			{
				EntityType:  "TPC",
				EntityValue: "1",
			},
			{
				EntityType:  "SM",
				EntityValue: "2",
			},
		}

		entities2 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0002:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-22222222-2222-2222-2222-222222222222",
			},
		}

		entitiesImpacted = append(entitiesImpacted, entities1)
		entitiesImpacted = append(entitiesImpacted, entities2)

		errorCodes := []string{helpers.ERRORCODE_13, helpers.ERRORCODE_13}
		for _, errorCode := range errorCodes {
			helpers.SendHealthEvent(ctx, t, helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(errorCode).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
			)
		}

		t.Log("Waiting 5s to create burst gap")
		time.Sleep(5 * time.Second)

		helpers.SendHealthEvent(ctx, t, helpers.NewHealthEvent(testNodeName).
			WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
			WithCheckName("SyslogXIDError").
			WithEntitiesImpacted(entities2).
			WithFatal(true).
			WithErrorCode(helpers.ERRORCODE_13).
			WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)))

		return ctx
	})

	feature.Assess("Check if XIDErrorSoloNoBurst node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		expectedEvent := v1.Event{
			Type:    "XIDErrorSoloNoBurst",
			Reason:  "XIDErrorSoloNoBurstIsNotHealthy",
			Message: "ErrorCode:13 PCI:0002:00:00 GPU_UUID:GPU-22222222-2222-2222-2222-222222222222 App passing bad data or using incorrect GPU methods; check error PID to identify source of the problem, if application is known good and problem persists, then contact support Recommended Action=NONE;",
		}

		helpers.WaitForNodeEvent(ctx, t, client, testNodeName, expectedEvent)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}

		return helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)
	})

	testEnv.Test(t, feature.Feature())
}

func TestXID74Reg0SoloNVLinkError(t *testing.T) {
	feature := features.New("TestXID74Reg0SoloNVLinkError").
		WithLabel("suite", "health-event-analyzer")

	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Logf("Waiting 70 seconds for the XIDErrorSoloNoBurst rule time window to complete")
		time.Sleep(70 * time.Second)

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test")
		testNodeName = testCtx.NodeName
		t.Logf("Using node: %s", testNodeName)

		return ctx
	})

	feature.Assess("Check if XID74Reg0Bits1Or20Set node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		entities1 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000100000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}
		entities2 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
		}
		entitiesImpacted = append(entitiesImpacted, entities1)

		xidEvents := []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities2).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_13).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "SysLogsXIDError",
			"ErrorCode:13 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 Recommended Action=RESTART_VM;",
			"SysLogsXIDErrorIsNotHealthy", v1.ConditionTrue)

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		// case 3: xid 74 has occurred and bits 1 or 20 are set (xid 13 is present and all others bits are 0) (rule will not be triggered)
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "XID74Reg0SoloNVLinkError")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities2).
				WithCheckName("SysLogsXIDError").
				WithErrorCode(helpers.ERRORCODE_13).
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU"),
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}

		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "XID74Reg0SoloNVLinkError",
			"ErrorCode:74 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 "+
				"REG0:00000000000100000000000000000000 REG1:00000000000000000000000000000000 "+
				"REG2:00000000000000000000000000000000 REG3:00000000000000000000000000000000 "+
				"REG4:00000000000000000000000000000000 REG5:00000000000000000000000000000000 "+
				"REG6:00000000000000000000000000000000 one of the bits (1 or 20) is set in register 0, unexpected error please open an NVBug Recommended Action=CONTACT_SUPPORT;",
			"XID74Reg0SoloNVLinkErrorIsNotHealthy", v1.ConditionTrue)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}

		return helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)
	})

	testEnv.Test(t, feature.Feature())
}

func TestXID74Reg0ECCParityError(t *testing.T) {
	feature := features.New("TestXID74Reg0ECCParityError").
		WithLabel("suite", "health-event-analyzer")

	// Cases to cover
	// 1. error has occurred only 1 time on the same NVLink and GPU --> no rule should be triggered
	// 2. error has occurred on different NVLink and same GPU --> even though occurrence is 2 but it has occurred on diff nvlink --> rule should not be triggered
	// 3. error has occurred more than 1 time on the same NVLink and GPU --> rule should be triggered
	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Logf("Waiting 100 seconds for the XID74Reg0ECCParityError rule time window to complete")
		time.Sleep(100 * time.Second)

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test")
		testNodeName = testCtx.NodeName
		t.Logf("Using node: %s", testNodeName)

		return ctx
	})

	feature.Assess("Check if XID74Reg0ECCParityError node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		entities1 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "14",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000000000000010000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}

		// Same GPU and same bits are set but different NVLink
		entities2 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "15",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000000000000010000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}

		entitiesImpacted = append(entitiesImpacted, entities1)
		entitiesImpacted = append(entitiesImpacted, entities2)

		xidEvents := []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should not be triggered as error has occurred only 1 time on the same NVLink and GPU")
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "XID74Reg0ECCParityError")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities2).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}

		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should not be triggered as error has occurred on different NVLink")
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "XID74Reg0ECCParityError")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}

		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should be triggered as error has occurred more than 1 time on the same NVLink and GPU")
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "XID74Reg0ECCParityError",
			"ErrorCode:74 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 "+
				"NVLINK:14 REG0:00000000000000000000000000010000 REG1:00000000000000000000000000000000 "+
				"REG2:00000000000000000000000000000000 REG3:00000000000000000000000000000000 "+
				"REG4:00000000000000000000000000000000 REG5:00000000000000000000000000000000 "+
				"REG6:00000000000000000000000000000000 one of the bits (4 or 5) is set in register 0 and its repeating on same NVLink and GPU, likely a HW issue with ECC/Parity Recommended Action=CONTACT_SUPPORT;",
			"XID74Reg0ECCParityErrorIsNotHealthy", v1.ConditionTrue)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}

		helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}

func TestRepeatedXID74Reg0HardwareIssueRule(t *testing.T) {
	feature := features.New("TestRepeatedXID74Reg0HardwareIssueRule").
		WithLabel("suite", "health-event-analyzer")

	// Cases to cover
	// 1. error has occurred only 1 time on the same GPU --> no rule should be triggered
	// 2. error has occurred more than 1 time on the same GPU --> rule should be triggered
	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Logf("Waiting 100 seconds for the RepeatedXID74Reg0HardwareIssue rule time window to complete")
		time.Sleep(100 * time.Second)

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test")
		testNodeName = testCtx.NodeName
		t.Logf("Using node: %s", testNodeName)

		return ctx
	})

	feature.Assess("Check if RepeatedXID74Reg0HardwareIssue node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		entities1 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "14",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000001000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}

		entitiesImpacted = append(entitiesImpacted, entities1)

		xidEvents := []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should not be triggered as error has occurred only 1 time on the same GPU")
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "RepeatedXID74Reg0HardwareIssue")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}

		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should be triggered as error has occurred more than 1 time on the same GPU")
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "RepeatedXID74Reg0HardwareIssue",
			"ErrorCode:74 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 "+
				"NVLINK:14 REG0:00000000000000000001000000000000 REG1:00000000000000000000000000000000 "+
				"REG2:00000000000000000000000000000000 REG3:00000000000000000000000000000000 "+
				"REG4:00000000000000000000000000000000 REG5:00000000000000000000000000000000 "+
				"REG6:00000000000000000000000000000000 one of the bits (8, 9, 12, 16, 17, 24 or 28) is set in register 0 and its repeating on same GPU, could be a hardware issue, request to check link mechanical connections and run field diagnosis if issue persists Recommended Action=CONTACT_SUPPORT;",
			"RepeatedXID74Reg0HardwareIssueIsNotHealthy", v1.ConditionTrue)

		return ctx
	})
	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}
		helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}

func TestXID74Reg0SignalIntegrityErrorRule(t *testing.T) {
	feature := features.New("TestXID74Reg0SignalIntegrityErrorRule").
		WithLabel("suite", "health-event-analyzer")

	// Cases to cover
	// 1. error has occurred only 1 time on the same GPU --> no rule should be triggered
	// 2. error has occurred more than 1 time on the same GPU --> rule should be triggered
	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Logf("Waiting 100 seconds for the XID74Reg0SignalIntegrityError rule time window to complete")
		time.Sleep(100 * time.Second)

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test")
		testNodeName = testCtx.NodeName
		t.Logf("Using node: %s", testNodeName)

		return ctx
	})

	feature.Assess("Check if XID74Reg0SignalIntegrityError node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		entities1 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "14",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000001000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}
		entities2 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
		}
		entitiesImpacted = append(entitiesImpacted, entities1)

		xidEvents := []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities2).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_31).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "SysLogsXIDError",
			"ErrorCode:31 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 Recommended Action=RESTART_VM;",
			"SysLogsXIDErrorIsNotHealthy", v1.ConditionTrue)

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}

		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should not be triggered as error has occurred with another error XID 31 on the same GPU")
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "XID74Reg0SignalIntegrityError")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities1).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithErrorCode(helpers.ERRORCODE_31).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU"),
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities1).
				WithCheckName("SysLogsXIDError").
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}

		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should be triggered as error has occurred without any other active error on the same GPU")
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "XID74Reg0SignalIntegrityError",
			"ErrorCode:74 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 "+
				"NVLINK:14 REG0:00000000001000000000000000000000 REG1:00000000000000000000000000000000 "+
				"REG2:00000000000000000000000000000000 REG3:00000000000000000000000000000000 "+
				"REG4:00000000000000000000000000000000 REG5:00000000000000000000000000000000 "+
				"REG6:00000000000000000000000000000000 one of the bits (21 or 22) is set in register 0, could be a marginal SI (signal integrity) issue, request to check link mechanical connections and run field diagnosis if issue persists Recommended Action=CONTACT_SUPPORT;",
			"XID74Reg0SignalIntegrityErrorIsNotHealthy", v1.ConditionTrue)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}
		helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}

func TestXID74Reg0RepeatedLinkErrorule(t *testing.T) {
	feature := features.New("TestXID74Reg0RepeatedLinkErrorule").
		WithLabel("suite", "health-event-analyzer")

	// Cases to cover
	// 1. error has occurred only 1 time on the same GPU --> no rule should be triggered
	// 2. error has occurred on different GPU --> rule should not be triggered
	// 3. error has occurred more than 1 time on the same GPU --> rule should be triggered
	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Logf("Waiting 100 seconds for the XID74Reg0RepeatedLinkError rule time window to complete")
		time.Sleep(100 * time.Second)

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test")
		testNodeName = testCtx.NodeName
		t.Logf("Using node: %s", testNodeName)

		return ctx
	})

	feature.Assess("Check if XID74Reg0RepeatedLinkError node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		entities1 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "14",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00001000000000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}
		entities2 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0002:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-22222222-2222-2222-2222-222222222222",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "14",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00001000000000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}
		entitiesImpacted = append(entitiesImpacted, entities1)
		entitiesImpacted = append(entitiesImpacted, entities2)

		xidEvents := []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should not be triggered as error has occurred on different GPU")
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "XID74Reg0RepeatedLinkError")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities2).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should not be triggered as error has occurred on different GPU")
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "XID74Reg0RepeatedLinkError")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should be triggered as error has occurred more than 1 time on the same GPU")
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "XID74Reg0RepeatedLinkError",
			"ErrorCode:74 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 "+
				"NVLINK:14 REG0:00001000000000000000000000000000 REG1:00000000000000000000000000000000 "+
				"REG2:00000000000000000000000000000000 REG3:00000000000000000000000000000000 "+
				"REG4:00000000000000000000000000000000 REG5:00000000000000000000000000000000 "+
				"REG6:00000000000000000000000000000000 one of the bits (27 or 29) is set in register 0 and its repeating on same GPU, unexpected error please open NVBug Recommended Action=CONTACT_SUPPORT;",
			"XID74Reg0RepeatedLinkErrorIsNotHealthy", v1.ConditionTrue)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}
		helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}

func TestRepeatedXID74Reg2HardwareIssue(t *testing.T) {
	feature := features.New("TestRepeatedXID74Reg2HardwareIssue").
		WithLabel("suite", "health-event-analyzer")

	// Cases to cover
	// 1. error has occurred only 1 time on the same NVLink and GPU --> no rule should be triggered
	// 2. error has occurred on different NVLink --> rule should not be triggered
	// 3. error has occurred more than 1 time on the same NVLink and GPU --> rule should be triggered
	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Logf("Waiting 100 seconds for the RepeatedXID74Reg2HardwareIssue rule time window to complete")
		time.Sleep(100 * time.Second)

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test")
		testNodeName = testCtx.NodeName
		t.Logf("Using node: %s", testNodeName)

		return ctx
	})

	feature.Assess("Check if RepeatedXID74Reg2HardwareIssue node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		entities1 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "14",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000000000000000100",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}

		entities2 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "15",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000000000000000100",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}

		entitiesImpacted = append(entitiesImpacted, entities1)
		entitiesImpacted = append(entitiesImpacted, entities2)

		xidEvents := []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should not be triggered as error has occurred only 1 time")
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "RepeatedXID74Reg2HardwareIssue")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities2).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should not be triggered as error has occurred on different NVLink")
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "RepeatedXID74Reg2HardwareIssue")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should be triggered as error has occurred more than 1 time on the same NVLink and GPU")
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "RepeatedXID74Reg2HardwareIssue",
			"ErrorCode:74 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 "+
				"NVLINK:14 REG0:00000000000000000000000000000000 REG1:00000000000000000000000000000000 "+
				"REG2:00000000000000000000000000000100 REG3:00000000000000000000000000000000 "+
				"REG4:00000000000000000000000000000000 REG5:00000000000000000000000000000000 "+
				"REG6:00000000000000000000000000000000 one of the bits (0, 1, 2 or 6) is set in register 1 and its repeating on same NVLink and GPU, likely a HW issue with ECC/Parity Recommended Action=CONTACT_SUPPORT;",
			"RepeatedXID74Reg2HardwareIssueIsNotHealthy", v1.ConditionTrue)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}
		helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}

func TestXID74Reg2Bit13SetRule(t *testing.T) {
	feature := features.New("TestXID74Reg2Bit13SetRule").
		WithLabel("suite", "health-event-analyzer")

	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test")
		testNodeName = testCtx.NodeName
		t.Logf("Using node: %s", testNodeName)

		return ctx
	})

	feature.Assess("Check if XID74Reg2Bit13SetRule node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		entities := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "14",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000010000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}
		entitiesImpacted = append(entitiesImpacted, entities)

		xidEvents := []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should be triggered as error has occurred with bit 13 set")
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "XID74Reg2Bit13Set",
			"ErrorCode:74 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 "+
				"NVLINK:14 REG0:00000000000000000000000000000000 REG1:00000000000000000000000000000000 "+
				"REG2:00000000000000000010000000000000 REG3:00000000000000000000000000000000 "+
				"REG4:00000000000000000000000000000000 REG5:00000000000000000000000000000000 "+
				"REG6:00000000000000000000000000000000 bit 13 is set in register 2, its an unexpected error please open an NVBug Recommended Action=CONTACT_SUPPORT;",
			"XID74Reg2Bit13SetIsNotHealthy", v1.ConditionTrue)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}
		helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}

func TestXID74Reg2Bit16Or19SetRule(t *testing.T) {
	feature := features.New("TestXID74Reg2Bit16Or19SetRule").
		WithLabel("suite", "health-event-analyzer")

	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	// cases to cover
	// 1. error has occurred only 1 time on the same NVLink and GPU --> no rule should be triggered
	// 2. error has occurred on different GPU --> rule should not be triggered as GPU is different
	// 3. error has occurred more than 1 time on the same NVLink and GPU --> rule should be triggered
	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Logf("Waiting 100 seconds for the XID74Reg2Bit16Or19SetRule rule time window to complete")
		time.Sleep(100 * time.Second)

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test")
		testNodeName = testCtx.NodeName
		t.Logf("Using node: %s", testNodeName)

		return ctx
	})

	feature.Assess("Check if XID74Reg2Bit16Or19SetRule node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		entities1 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "14",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000010000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}
		entities2 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0002:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-22222222-2222-2222-2222-222222222222",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "14",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000010000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}
		entitiesImpacted = append(entitiesImpacted, entities1)
		entitiesImpacted = append(entitiesImpacted, entities2)

		xidEvents := []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "RepeatedXID74Reg2Bit16Or19Set")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities2).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "RepeatedXID74Reg2Bit16Or19Set")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}
		t.Log("Rule should be triggered as error has occurred with bit 16 or 19 set")
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "RepeatedXID74Reg2Bit16Or19Set",
			"ErrorCode:74 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 "+
				"NVLINK:14 REG0:00000000000000000000000000000000 REG1:00000000000000000000000000000000 "+
				"REG2:00000000000010000000000000000000 REG3:00000000000000000000000000000000 "+
				"REG4:00000000000000000000000000000000 REG5:00000000000000000000000000000000 "+
				"REG6:00000000000000000000000000000000 one of the bits (16 or 19) is set in register 2 and its repeating on same GPU, request for field diagnosis Recommended Action=CONTACT_SUPPORT;",
			"RepeatedXID74Reg2Bit16Or19SetIsNotHealthy", v1.ConditionTrue)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}
		helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}

func TestXID74Reg2Bit17Or18SetRule(t *testing.T) {
	feature := features.New("TestXID74Reg2Bit17Or18SetRule").
		WithLabel("suite", "health-event-analyzer")

	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	// cases to cover
	// 1. error has occurred only 1 time on the same NVLink and GPU --> no rule should be triggered
	// 2. error has occurred on different GPU --> rule should not be triggered as GPU is different
	// 3. error has occurred more than 1 time on the same NVLink and GPU --> rule should be triggered
	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Logf("Waiting 100 seconds for the XID74Reg2Bit16Or19SetRule rule time window to complete")
		time.Sleep(100 * time.Second)

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test")
		testNodeName = testCtx.NodeName
		t.Logf("Using node: %s", testNodeName)

		return ctx
	})

	feature.Assess("Check if XID74Reg2Bit16Or19SetRule node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		entities1 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "14",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000001000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}
		entities2 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0002:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-22222222-2222-2222-2222-222222222222",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "14",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000001000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}
		entitiesImpacted = append(entitiesImpacted, entities1)
		entitiesImpacted = append(entitiesImpacted, entities2)

		xidEvents := []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "RepeatedXID74Reg2Bit17Or18Set")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities2).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "RepeatedXID74Reg2Bit17Or18Set")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}
		t.Log("Rule should be triggered as error has occurred with bit 16 or 19 set")
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "RepeatedXID74Reg2Bit17Or18Set",
			"ErrorCode:74 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 "+
				"NVLINK:14 REG0:00000000000000000000000000000000 REG1:00000000000000000000000000000000 "+
				"REG2:00000000000001000000000000000000 REG3:00000000000000000000000000000000 "+
				"REG4:00000000000000000000000000000000 REG5:00000000000000000000000000000000 "+
				"REG6:00000000000000000000000000000000 one of the bits (17 or 18) is set in register 2 and its repeating on same GPU, request for field diagnosis Recommended Action=CONTACT_SUPPORT;",
			"RepeatedXID74Reg2Bit17Or18SetIsNotHealthy", v1.ConditionTrue)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}
		helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}

func TestXID74Reg3UnexpectedErrorRule(t *testing.T) {
	feature := features.New("TestXID74Reg3UnexpectedErrorRule").
		WithLabel("suite", "health-event-analyzer")

	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	// cases to cover
	// 1. XID 31 has occurred on the same GPU
	// 2. XID 74 has occurred on the same GPU with bits 16 or 17 set --> no rule should be triggered as there is active error present on same GPU
	// 3. remove XID 31 by sending health event with healthy flag set to true
	// 4. XID 74 has occurred on the same GPU with bits 16 or 17 set --> rule should be triggered as there is no active error present on same GPU
	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Logf("Waiting 100 seconds for the XID74Reg3UnexpectedErrorRule rule time window to complete")
		time.Sleep(100 * time.Second)

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test")
		testNodeName = testCtx.NodeName
		t.Logf("Using node: %s", testNodeName)

		return ctx
	})

	feature.Assess("Check if XID74Reg3UnexpectedErrorRule node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		entities1 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "14",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000100000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}

		entities2 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
		}

		entitiesImpacted = append(entitiesImpacted, entities1)
		entitiesImpacted = append(entitiesImpacted, entities2)

		xidEvents := []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities2).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_13).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testCtx.NodeName, "SysLogsXIDError",
			"ErrorCode:13 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 Recommended Action=RESTART_VM;",
			"SysLogsXIDErrorIsNotHealthy", v1.ConditionTrue)

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "XID74Reg3UnexpectedError")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities2).
				WithErrorCode(helpers.ERRORCODE_13).
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU"),
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "XID74Reg3UnexpectedError",
			"ErrorCode:74 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 "+
				"NVLINK:14 REG0:00000000000000000000000000000000 REG1:00000000000000000000000000000000 "+
				"REG2:00000000000000000000000000000000 REG3:00000000000000100000000000000000 "+
				"REG4:00000000000000000000000000000000 REG5:00000000000000000000000000000000 "+
				"REG6:00000000000000000000000000000000 one of the bits (16 or 17) is set in register 3, unexpected error please open an NVBug Recommended Action=CONTACT_SUPPORT;",
			"XID74Reg3UnexpectedErrorIsNotHealthy", v1.ConditionTrue)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}
		helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}

func TestXID74Reg3Bit18SetRule(t *testing.T) {
	feature := features.New("TestXID74Reg3Bit18SetRule").
		WithLabel("suite", "health-event-analyzer")

	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	// cases to cover
	// 1. XID 31 has occurred on the same GPU
	// 2. XID 74 has occurred on the same GPU with bits 18 or 19 set --> no rule should be triggered as there is active error present on same GPU
	// 3. remove XID 31 by sending health event with healthy flag set to true
	// 4. XID 74 has occurred on the same GPU with bits 18 set --> rule should be triggered as there is no active error present on same GPU
	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Logf("Waiting 100 seconds for the XID74Reg3Bit18SetRule rule time window to complete")
		time.Sleep(100 * time.Second)

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test")
		testNodeName = testCtx.NodeName
		t.Logf("Using node: %s", testNodeName)

		return ctx
	})
	feature.Assess("Check if XID74Reg3Bit18SetRule node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		entities1 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "14",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000001000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}
		entities2 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
		}
		entitiesImpacted = append(entitiesImpacted, entities1)
		entitiesImpacted = append(entitiesImpacted, entities2)

		xidEvents := []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities2).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_13).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "SysLogsXIDError",
			"ErrorCode:13 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 Recommended Action=RESTART_VM;",
			"SysLogsXIDErrorIsNotHealthy", v1.ConditionTrue)

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "XID74Reg3Bit18Set")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities2).
				WithErrorCode(helpers.ERRORCODE_13).
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU"),
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "XID74Reg3Bit18Set",
			"ErrorCode:74 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 "+
				"NVLINK:14 REG0:00000000000000000000000000000000 REG1:00000000000000000000000000000000 "+
				"REG2:00000000000000000000000000000000 REG3:00000000000001000000000000000000 "+
				"REG4:00000000000000000000000000000000 REG5:00000000000000000000000000000000 "+
				"REG6:00000000000000000000000000000000 bit 18 is set in register 3, reset of fabric is required Recommended Action=CONTACT_SUPPORT;",
			"XID74Reg3Bit18SetIsNotHealthy", v1.ConditionTrue)

		return ctx

	})
	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}
		helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}

func TestXID74Reg4HardwareIssueRule(t *testing.T) {
	feature := features.New("TestXID74Reg4HardwareIssueRule").
		WithLabel("suite", "health-event-analyzer")

	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	// cases to cover
	// 1. error has occurred only 1 time on the same NVLink and GPU --> no rule should be triggered
	// 2. error has occurred on different NVLink --> rule should not be triggered
	// 3. error has occurred more than 1 time on the same NVLink and GPU --> rule should be triggered
	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Logf("Waiting 100 seconds for the XID74Reg4HardwareIssueRule rule time window to complete")
		time.Sleep(100 * time.Second)

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test")
		testNodeName = testCtx.NodeName
		t.Logf("Using node: %s", testNodeName)

		return ctx
	})
	feature.Assess("Check if XID74Reg4HardwareIssueRule node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		entities1 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "14",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000001000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}
		entities2 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "15",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000001000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}
		entitiesImpacted = append(entitiesImpacted, entities1)
		entitiesImpacted = append(entitiesImpacted, entities2)

		xidEvents := []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}

		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should not be triggered as error has occurred only 1 time on the same NVLink and GPU")
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "XID74Reg4HardwareIssue")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities2).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should not be triggered as error has occurred on different NVLink")
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "XID74Reg4HardwareIssue")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should be triggered as error has occurred more than 1 time on the same NVLink and GPU")
		helpers.WaitForNodeConditionWithCheckName(ctx, t, client, testNodeName, "XID74Reg4HardwareIssue",
			"ErrorCode:74 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 "+
				"NVLINK:14 REG0:00000000000000000000000000000000 REG1:00000000000000000000000000000000 "+
				"REG2:00000000000000000000000000000000 REG3:00000000000000000000000000000000 "+
				"REG4:00000001000000000000000000000000 REG5:00000000000000000000000000000000 "+
				"REG6:00000000000000000000000000000000 one of the bits (18, 19, 21, 22, 24, 25, 27, 28) is set in register 4 and its repeating on same NVLink, likely a HW issue with ECC/Parity Recommended Action=CONTACT_SUPPORT;",
			"XID74Reg4HardwareIssueIsNotHealthy", v1.ConditionTrue)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}
		helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}

func TestXID74Reg4ECCError(t *testing.T) {
	feature := features.New("TestXID74Reg4ECCError").
		WithLabel("suite", "health-event-analyzer")

	var testCtx *helpers.HealthEventsAnalyzerTestContext
	var testNodeName string
	var entitiesImpacted [][]helpers.EntityImpacted

	// cases to cover
	// 1. error has occurred only 1 time on the same NVLink and GPU --> no rule should be triggered
	// 2. error has occurred on different NVLink --> rule should not be triggered
	// 3. error has occurred more than 1 time on the same NVLink and GPU --> rule should be triggered
	feature.Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		t.Logf("Waiting 100 seconds for the XID74Reg4ECCError rule time window to complete")
		time.Sleep(100 * time.Second)

		ctx, testCtx = helpers.SetupHealthEventsAnalyzerTest(ctx, t, c, "data/health-events-analyzer-config.yaml", "health-events-analyzer-test")
		testNodeName = testCtx.NodeName
		t.Logf("Using node: %s", testNodeName)

		return ctx
	})
	feature.Assess("Check if XID74Reg4ECCError node condition is added", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		client, err := c.NewClient()
		require.NoError(t, err)

		entities1 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "14",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000100000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}
		entities2 := []helpers.EntityImpacted{
			{
				EntityType:  "PCI",
				EntityValue: "0001:00:00",
			},
			{
				EntityType:  "GPU_UUID",
				EntityValue: "GPU-11111111-1111-1111-1111-111111111111",
			},
			{
				EntityType:  "NVLINK",
				EntityValue: "15",
			},
			{
				EntityType:  "REG0",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG1",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG2",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG3",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG4",
				EntityValue: "00000100000000000000000000000000",
			},
			{
				EntityType:  "REG5",
				EntityValue: "00000000000000000000000000000000",
			},
			{
				EntityType:  "REG6",
				EntityValue: "00000000000000000000000000000000",
			},
		}
		entitiesImpacted = append(entitiesImpacted, entities1)
		entitiesImpacted = append(entitiesImpacted, entities2)

		xidEvents := []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}

		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should not be triggered as error has occurred only 1 time on the same NVLink and GPU")
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "XID74Reg4ECCError")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities2).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should not be triggered as error has occurred on different NVLink")
		helpers.EnsureNodeConditionNotPresent(ctx, t, client, testNodeName, "XID74Reg4ECCError")

		xidEvents = []*helpers.HealthEventTemplate{
			helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithCheckName("SysLogsXIDError").
				WithEntitiesImpacted(entities1).
				WithFatal(true).
				WithErrorCode(helpers.ERRORCODE_74).
				WithRecommendedAction(int(pb.RecommendedAction_RESTART_VM)),
		}
		for _, xidEvent := range xidEvents {
			helpers.SendHealthEvent(ctx, t, xidEvent)
		}

		t.Log("Rule should be triggered as error has occurred more than 1 time on the same NVLink and GPU")
		expectedMessage := "ErrorCode:74 PCI:0001:00:00 GPU_UUID:GPU-11111111-1111-1111-1111-111111111111 " +
			"NVLINK:14 REG0:00000000000000000000000000000000 REG1:00000000000000000000000000000000 " +
			"REG2:00000000000000000000000000000000 REG3:00000000000000000000000000000000 " +
			"REG4:00000100000000000000000000000000 REG5:00000000000000000000000000000000 " +
			"REG6:00000000000000000000000000000000 one of the bits (20, 23, 26, 29) is set in register 4, request for field diagnosis if user jobs are interrupted or error occurs repeatedly Recommended Action=NONE;"

		expectedEvent := v1.Event{
			Type:    "XID74Reg4ECCError",
			Reason:  "XID74Reg4ECCErrorIsNotHealthy",
			Message: expectedMessage,
		}
		helpers.WaitForNodeEvent(ctx, t, client, testNodeName, expectedEvent)

		return ctx
	})

	feature.Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
		for _, entities := range entitiesImpacted {
			syslogHealthEvent := helpers.NewHealthEvent(testNodeName).
				WithAgent(helpers.SYSLOG_HEALTH_MONITOR_AGENT).
				WithEntitiesImpacted(entities).
				WithCheckName("SysLogsXIDError").
				WithFatal(false).
				WithHealthy(true).
				WithMessage("No health failures").
				WithComponentClass("GPU")
			helpers.SendHealthEvent(ctx, t, syslogHealthEvent)
		}
		helpers.TeardownHealthEventsAnalyzer(ctx, t, c, testNodeName, testCtx.ConfigMapBackup)

		return ctx
	})

	testEnv.Test(t, feature.Feature())
}
