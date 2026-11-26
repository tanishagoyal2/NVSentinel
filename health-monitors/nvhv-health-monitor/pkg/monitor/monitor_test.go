// Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
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

package monitor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	pb "github.com/nvidia/nvsentinel/data-models/pkg/protos"
	"github.com/nvidia/nvsentinel/health-monitors/nvhv-health-monitor/pkg/metrics"
	healthv1 "github.com/nvidia/nvsentinel/health-monitors/nvhv-health-monitor/pkg/verdictpb/health/v1"
)

type harness struct {
	m       *Monitor
	batches [][]*pb.HealthEvent
	failing bool
	now     time.Time
}

func newHarness() *harness {
	h := &harness{now: time.Unix(1_000, 0)}
	h.m = New(Config{
		Agent:            "nvhv-health-monitor",
		NodeName:         "node-1",
		UnreachableAfter: 5 * time.Minute,
		Now:              func() time.Time { return h.now },
		Publish: func(_ context.Context, events []*pb.HealthEvent) error {
			if h.failing {
				return errors.New("publish failed")
			}

			h.batches = append(h.batches, events)

			return nil
		},
	})

	return h
}

func (h *harness) poll(results ...*healthv1.CheckVerdict) {
	h.m.Handle(context.Background(), &healthv1.NodeVerdict{CheckResults: results}, nil)
}

func result(id string, sev healthv1.Severity, entities ...string) *healthv1.CheckVerdict {
	s, c := sev, healthv1.Category_gpu

	return &healthv1.CheckVerdict{
		CheckId:          &id,
		Severity:         &s,
		Category:         &c,
		Messages:         []string{"msg " + id},
		AffectedEntities: entities,
	}
}

func withAction(r *healthv1.CheckVerdict, state healthv1.OperationalState, actions ...string) *healthv1.CheckVerdict {
	r.RecommendedAction = &healthv1.RecommendedAction{OperationalState: &state, RemediationActions: actions}

	return r
}

func TestEventFields(t *testing.T) {
	h := newHarness()
	r := withAction(result("row_remap", healthv1.Severity_CRITICAL, "GPU-abc"), healthv1.OperationalState_drain, "reset_gpu")
	r.ObservedValues = map[string]*structpb.Value{"pci": structpb.NewStringValue("0000:1b:00.0")}

	h.poll(r)

	require.Len(t, h.batches, 1)
	require.Len(t, h.batches[0], 1)

	e := h.batches[0][0]
	assert.Equal(t, "nvhv-health-monitor", e.Agent)
	assert.Equal(t, "node-1", e.NodeName)
	assert.Equal(t, "GPU", e.ComponentClass)
	assert.Equal(t, "NVHVrow_remap", e.CheckName)
	assert.True(t, e.IsFatal)
	assert.False(t, e.IsHealthy)
	assert.Equal(t, "msg row_remap", e.Message)
	assert.Equal(t, pb.RecommendedAction_COMPONENT_RESET, e.RecommendedAction)
	assert.Equal(t, pb.ProcessingStrategy_STORE_ONLY, e.ProcessingStrategy)
	require.Len(t, e.EntitiesImpacted, 2)
	assert.Equal(t, &pb.Entity{EntityType: "GPU_UUID", EntityValue: "GPU-abc"}, e.EntitiesImpacted[0])
	assert.Equal(t, &pb.Entity{EntityType: "PCI", EntityValue: "0000:1b:00.0"}, e.EntitiesImpacted[1])
	assert.NotNil(t, e.GeneratedTimestamp)
}

func TestRecommendedAction(t *testing.T) {
	act := func(state healthv1.OperationalState, actions ...string) pb.RecommendedAction {
		return recommendedAction(&healthv1.RecommendedAction{OperationalState: &state, RemediationActions: actions})
	}

	assert.Equal(t, pb.RecommendedAction_COMPONENT_RESET, act(healthv1.OperationalState_drain, "reset_gpu"))
	assert.Equal(t, pb.RecommendedAction_CONTACT_SUPPORT, act(healthv1.OperationalState_drain, "service_required"))
	assert.Equal(t, pb.RecommendedAction_RESTART_VM, act(healthv1.OperationalState_drain, "restart_os"))
	assert.Equal(t, pb.RecommendedAction_CONTACT_SUPPORT, act(healthv1.OperationalState_drain, "replace_node"))
	assert.Equal(t, pb.RecommendedAction_NONE, act(healthv1.OperationalState_monitor, "service_required"))
	assert.Equal(t, pb.RecommendedAction_NONE, act(healthv1.OperationalState_ready))
}

func TestSeverityMapping(t *testing.T) {
	h := newHarness()
	h.poll(
		result("warn", healthv1.Severity_WARNING),
		result("info", healthv1.Severity_INFO),
		result("ok", healthv1.Severity_PASS),
		result("unk", healthv1.Severity_UNKNOWN),
	)

	require.Len(t, h.batches, 1)
	require.Len(t, h.batches[0], 2)

	for _, e := range h.batches[0] {
		assert.False(t, e.IsFatal)
		assert.False(t, e.IsHealthy)
	}
}

func TestSkippedResultIsIgnored(t *testing.T) {
	h := newHarness()
	r := result("c", healthv1.Severity_CRITICAL)
	skipped := true
	r.Skipped = &skipped

	h.poll(r)

	assert.Empty(t, h.batches)
}

func TestPublishesOnlyChanges(t *testing.T) {
	h := newHarness()
	h.poll(result("c", healthv1.Severity_CRITICAL, "GPU-1"))
	h.poll(result("c", healthv1.Severity_CRITICAL, "GPU-1"))
	assert.Len(t, h.batches, 1, "an unchanged fault is not sent again")

	h.poll(result("c", healthv1.Severity_CRITICAL, "GPU-1"), result("c", healthv1.Severity_CRITICAL, "GPU-2"))
	require.Len(t, h.batches, 2)
	assert.Len(t, h.batches[1], 1, "only the new GPU's fault is sent")

	h.poll(result("c", healthv1.Severity_WARNING, "GPU-1"))
	assert.Len(t, h.batches, 3, "a changed severity is sent again")
}

func TestMissingOrUnknownNeverClears(t *testing.T) {
	h := newHarness()
	h.poll(result("c", healthv1.Severity_CRITICAL, "GPU-1"))
	h.poll()                                                // check missing
	h.poll(result("c", healthv1.Severity_UNKNOWN, "GPU-1")) // check UNKNOWN
	h.poll(result("c", healthv1.Severity_CRITICAL, "GPU-1"))
	assert.Len(t, h.batches, 1)
}

func TestPassForgetsFaultAndSendsNothing(t *testing.T) {
	h := newHarness()
	h.poll(result("c", healthv1.Severity_CRITICAL, "GPU-1"))
	h.poll(result("c", healthv1.Severity_PASS, "GPU-1"))
	assert.Len(t, h.batches, 1, "no recovery event is sent")

	h.poll(result("c", healthv1.Severity_CRITICAL, "GPU-1"))
	assert.Len(t, h.batches, 2, "the fault is reported again after it recovered")
}

func TestFailedPublishKeepsRecord(t *testing.T) {
	h := newHarness()
	h.failing = true
	h.poll(result("c", healthv1.Severity_CRITICAL, "GPU-1"))
	assert.Empty(t, h.batches)

	h.failing = false
	h.poll(result("c", healthv1.Severity_CRITICAL, "GPU-1"))
	assert.Len(t, h.batches, 1, "the fault is sent once publishing works")
}

func (h *harness) fail() { h.m.Handle(context.Background(), nil, errors.New("unavailable")) }

func TestUnreachableThreshold(t *testing.T) {
	h := newHarness()

	h.fail()
	h.now = h.now.Add(4 * time.Minute)
	h.fail()
	assert.Empty(t, h.batches, "below the threshold nothing is sent")

	h.now = h.now.Add(2 * time.Minute)
	h.fail()
	require.Len(t, h.batches, 1)

	e := h.batches[0][0]
	assert.Equal(t, UnreachableCheckName, e.CheckName)
	assert.Equal(t, "Node", e.ComponentClass)
	assert.True(t, e.IsFatal)
	assert.False(t, e.IsHealthy)
	assert.Equal(t, pb.RecommendedAction_CONTACT_SUPPORT, e.RecommendedAction)
	assert.Empty(t, e.EntitiesImpacted)
	assert.Equal(t, pb.ProcessingStrategy_STORE_ONLY, e.ProcessingStrategy)

	h.fail()
	assert.Len(t, h.batches, 1, "sent once per outage")

	h.poll() // NVHV answers again: no recovery event
	assert.Len(t, h.batches, 1)

	h.fail()
	h.now = h.now.Add(6 * time.Minute)
	h.fail()
	assert.Len(t, h.batches, 2, "a new outage sends a new event")
}

func TestUnreachableDoesNotClearFaults(t *testing.T) {
	h := newHarness()
	h.poll(result("c", healthv1.Severity_CRITICAL, "GPU-1"))
	h.fail()
	h.poll(result("c", healthv1.Severity_CRITICAL, "GPU-1"))
	assert.Len(t, h.batches, 1)
}

func TestUnreachableMetrics(t *testing.T) {
	h := newHarness()
	unreachable := metrics.Unreachable.WithLabelValues("node-1")
	failures := metrics.EvaluateFailures.WithLabelValues("node-1", codes.Unavailable.String())
	before := testutil.ToFloat64(failures)

	// A gRPC status wrapped the way the poller wraps it still gives its code.
	h.m.Handle(context.Background(), nil, fmt.Errorf("evaluate: %w", status.Error(codes.Unavailable, "down")))
	assert.InDelta(t, 1, testutil.ToFloat64(unreachable), 0)
	assert.InDelta(t, before+1, testutil.ToFloat64(failures), 0)

	h.poll()
	assert.InDelta(t, 0, testutil.ToFloat64(unreachable), 0)
}
