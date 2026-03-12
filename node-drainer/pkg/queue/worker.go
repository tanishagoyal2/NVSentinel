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

package queue

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/nvidia/nvsentinel/commons/pkg/tracing"
	"github.com/nvidia/nvsentinel/node-drainer/pkg/metrics"
	"github.com/nvidia/nvsentinel/store-client/pkg/datastore"
)

// Interfaces are defined in types.go

func (m *eventQueueManager) Start(ctx context.Context) {
	slog.Info("Starting workqueue processor")

	go m.runWorker(ctx)
}

func (m *eventQueueManager) runWorker(ctx context.Context) {
	for m.processNextWorkItem(ctx) {
	}

	slog.Info("Worker stopped")
}

func (m *eventQueueManager) processNextWorkItem(ctx context.Context) bool {
	nodeEvent, shutdown := m.queue.Get()
	if shutdown {
		return false
	}

	defer m.queue.Done(nodeEvent)

	// Start or reuse node_drainer.drain_session span so all process_event cycles for this item are under one parent.
	processCtx := ctx
	if nodeEvent.DrainSessionSpan == nil {
		if nodeEvent.TraceID != "" {
			var spanCtx context.Context
			spanCtx, nodeEvent.DrainSessionSpan = tracing.StartSpanWithLinkFromTraceContext(
				ctx, nodeEvent.TraceID, nodeEvent.ParentSpanID, "node_drainer.drain_session")
			tracing.SetSpanAttributes(nodeEvent.DrainSessionSpan,
				attribute.String("node_drainer.node_name", nodeEvent.NodeName),
				attribute.String("node_drainer.event_id", nodeEvent.EventID),
			)
			processCtx = spanCtx
		}
	} else {
		processCtx = trace.ContextWithSpan(ctx, nodeEvent.DrainSessionSpan)
	}

	// Attach cumulative metrics so reconciler can add durations/scope without creating multiple spans; carry across requeues.
	drainMetrics := &DrainSessionMetrics{
		ImmediateEvictionStartedAt:  nodeEvent.ImmediateEvictionStartedAt,
		AllowCompletionStartedAt:    nodeEvent.AllowCompletionStartedAt,
		DeleteAfterTimeoutStartedAt: nodeEvent.DeleteAfterTimeoutStartedAt,
		ImmediateEvictionEndedAt:    nodeEvent.ImmediateEvictionEndedAt,
		AllowCompletionEndedAt:      nodeEvent.AllowCompletionEndedAt,
		DeleteAfterTimeoutEndedAt:   nodeEvent.DeleteAfterTimeoutEndedAt,
		PodsForceDeletedCount:       nodeEvent.PodsForceDeletedCount,
		ForceDeletedPods:            nodeEvent.ForceDeletedPods,
		ImmediateEvictionPods:       nodeEvent.ImmediateEvictionPods,
		AllowCompletionPods:         nodeEvent.AllowCompletionPods,
		DeleteAfterTimeoutPods:      nodeEvent.DeleteAfterTimeoutPods,
		DrainScope:                  nodeEvent.DrainScope,
		PartialDrainEntityType:      nodeEvent.PartialDrainEntityType,
		PartialDrainEntityValue:     nodeEvent.PartialDrainEntityValue,
	}
	processCtx = ContextWithDrainSessionMetrics(processCtx, drainMetrics)
	processCtx = ContextWithNodeEvent(processCtx, &nodeEvent)

	var err error
	if nodeEvent.Event != nil && nodeEvent.Database != nil && nodeEvent.HealthEventStore != nil {
		err = m.processEventGeneric(processCtx, *nodeEvent.Event, nodeEvent.Database, nodeEvent.HealthEventStore, nodeEvent.NodeName)
	} else {
		err = fmt.Errorf("event data, database interface, or health event store not available")
	}

	// Carry cumulative metrics and scope back to nodeEvent for next cycle or for final span attributes
	nodeEvent.ImmediateEvictionStartedAt = drainMetrics.ImmediateEvictionStartedAt
	nodeEvent.AllowCompletionStartedAt = drainMetrics.AllowCompletionStartedAt
	nodeEvent.DeleteAfterTimeoutStartedAt = drainMetrics.DeleteAfterTimeoutStartedAt
	nodeEvent.ImmediateEvictionEndedAt = drainMetrics.ImmediateEvictionEndedAt
	nodeEvent.AllowCompletionEndedAt = drainMetrics.AllowCompletionEndedAt
	nodeEvent.DeleteAfterTimeoutEndedAt = drainMetrics.DeleteAfterTimeoutEndedAt
	nodeEvent.PodsForceDeletedCount = drainMetrics.PodsForceDeletedCount
	nodeEvent.ForceDeletedPods = drainMetrics.ForceDeletedPods
	nodeEvent.ImmediateEvictionPods = drainMetrics.ImmediateEvictionPods
	nodeEvent.AllowCompletionPods = drainMetrics.AllowCompletionPods
	nodeEvent.DeleteAfterTimeoutPods = drainMetrics.DeleteAfterTimeoutPods
	nodeEvent.DrainScope = drainMetrics.DrainScope
	nodeEvent.PartialDrainEntityType = drainMetrics.PartialDrainEntityType
	nodeEvent.PartialDrainEntityValue = drainMetrics.PartialDrainEntityValue

	if err != nil {
		slog.Warn("Error processing event for node (will retry)",
			"node", nodeEvent.NodeName,
			"attempt", m.queue.NumRequeues(nodeEvent)+1,
			"error", err)
		m.queue.AddRateLimited(nodeEvent)
	} else {
		if nodeEvent.DrainSessionSpan != nil {
			now := time.Now()

			// phaseDuration computes wall-clock time for a phase:
			// - entered and exited: EndedAt - StartedAt
			// - entered but never exited (last active phase): now - StartedAt
			// - never entered: 0
			phaseDuration := func(start, end time.Time) float64 {
				if start.IsZero() {
					return 0
				}
				if !end.IsZero() {
					return end.Sub(start).Seconds()
				}
				return now.Sub(start).Seconds()
			}

			attrs := []attribute.KeyValue{
				attribute.Float64("node_drainer.immediate_eviction_duration_s", phaseDuration(nodeEvent.ImmediateEvictionStartedAt, nodeEvent.ImmediateEvictionEndedAt)),
				attribute.Float64("node_drainer.allow_completion_duration_s", phaseDuration(nodeEvent.AllowCompletionStartedAt, nodeEvent.AllowCompletionEndedAt)),
				attribute.Float64("node_drainer.delete_after_timeout_duration_s", phaseDuration(nodeEvent.DeleteAfterTimeoutStartedAt, nodeEvent.DeleteAfterTimeoutEndedAt)),
				attribute.Int("node_drainer.pods_force_deleted_count", nodeEvent.PodsForceDeletedCount),
				attribute.Int("node_drainer.total_requeues", m.queue.NumRequeues(nodeEvent)),
			}
			if len(nodeEvent.ForceDeletedPods) > 0 {
				attrs = append(attrs, attribute.String("node_drainer.force_deleted_pods", nodeEvent.ForceDeletedPods))
			}
			if len(nodeEvent.ImmediateEvictionPods) > 0 {
				attrs = append(attrs, attribute.String("node_drainer.immediate_eviction_pods", nodeEvent.ImmediateEvictionPods))
			}
			if len(nodeEvent.AllowCompletionPods) > 0 {
				attrs = append(attrs, attribute.String("node_drainer.allow_completion_pods", nodeEvent.AllowCompletionPods))
			}
			if len(nodeEvent.DeleteAfterTimeoutPods) > 0 {
				attrs = append(attrs, attribute.String("node_drainer.delete_after_timeout_pods", nodeEvent.DeleteAfterTimeoutPods))
			}
			if nodeEvent.DrainScope != "" {
				attrs = append(attrs, attribute.String("node_drainer.drain.scope", nodeEvent.DrainScope))
				if nodeEvent.DrainScope == "partial" {
					attrs = append(attrs,
						attribute.String("node_drainer.partial_drain.entity_type", nodeEvent.PartialDrainEntityType),
						attribute.String("node_drainer.partial_drain.entity_value", nodeEvent.PartialDrainEntityValue),
					)
				}
			}
			nodeEvent.DrainSessionSpan.SetAttributes(attrs...)
			nodeEvent.DrainSessionSpan.End()
		}
		m.queue.Forget(nodeEvent)
	}

	metrics.QueueDepth.Set(float64(m.queue.Len()))

	return true
}

func (m *eventQueueManager) processEventGeneric(ctx context.Context,
	event datastore.Event, database DataStore, healthEventStore datastore.HealthEventStore, nodeName string) error {
	if m.dataStoreEventProcessor == nil {
		return fmt.Errorf("no datastore event processor configured")
	}

	return m.dataStoreEventProcessor.ProcessEventGeneric(ctx, event, database, healthEventStore, nodeName)
}

// processEvent method has been removed - only processEventGeneric is used now
