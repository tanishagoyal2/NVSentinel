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
			spanCtx, nodeEvent.DrainSessionSpan = tracing.StartSpanFromTraceContext(ctx, nodeEvent.TraceID, nodeEvent.ParentSpanID, "node_drainer.drain_session")
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
		ImmediateEvictionDurationMs:     nodeEvent.ImmediateEvictionDurationMs,
		AwaitingPodCompletionDurationMs: nodeEvent.AwaitingPodCompletionDurationMs,
		DrainScope:                      nodeEvent.DrainScope,
		PartialDrainEntityType:          nodeEvent.PartialDrainEntityType,
		PartialDrainEntityValue:         nodeEvent.PartialDrainEntityValue,
	}
	processCtx = ContextWithDrainSessionMetrics(processCtx, drainMetrics)

	var err error
	if nodeEvent.Event != nil && nodeEvent.Database != nil && nodeEvent.HealthEventStore != nil {
		err = m.processEventGeneric(processCtx, *nodeEvent.Event, nodeEvent.Database, nodeEvent.HealthEventStore, nodeEvent.NodeName)
	} else {
		err = fmt.Errorf("event data, database interface, or health event store not available")
	}

	// Carry cumulative metrics and scope back to nodeEvent for next cycle or for final span attributes
	nodeEvent.ImmediateEvictionDurationMs = drainMetrics.ImmediateEvictionDurationMs
	nodeEvent.AwaitingPodCompletionDurationMs = drainMetrics.AwaitingPodCompletionDurationMs
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
			attrs := []attribute.KeyValue{
				attribute.Int64("node_drainer.immediate_eviction_duration_ms", nodeEvent.ImmediateEvictionDurationMs),
				attribute.Int64("node_drainer.awaiting_pod_completion_duration_ms", nodeEvent.AwaitingPodCompletionDurationMs),
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
