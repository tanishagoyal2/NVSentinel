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

	"go.opentelemetry.io/otel/trace"
	"k8s.io/client-go/util/workqueue"

	"github.com/nvidia/nvsentinel/commons/pkg/tracing"

	"github.com/nvidia/nvsentinel/node-drainer/pkg/metrics"
	"github.com/nvidia/nvsentinel/store-client/pkg/datastore"
	"github.com/nvidia/nvsentinel/store-client/pkg/utils"
)

func NewEventQueueManager() EventQueueManager {
	mgr := &eventQueueManager{
		queue: workqueue.NewTypedRateLimitingQueue(
			workqueue.NewTypedItemExponentialFailureRateLimiter[NodeEvent](10*time.Second, 2*time.Minute),
		),
		shutdown: make(chan struct{}),
	}

	return mgr
}

// SetEventProcessor method has been removed - use SetDataStoreEventProcessor instead

func (m *eventQueueManager) SetDataStoreEventProcessor(processor DataStoreEventProcessor) {
	m.dataStoreEventProcessor = processor
}

// EnqueueEventGeneric enqueues an event using the new database-agnostic interface.
// If drainSessionSpan is non-nil, it is used as the trace root (event data is on that span); worker will not create a new one.
func (m *eventQueueManager) EnqueueEventGeneric(ctx context.Context, nodeName string, event datastore.Event,
	database DataStore, healthEventStore datastore.HealthEventStore, drainSessionSpan trace.Span) error {
	if ctx.Err() != nil {
		return fmt.Errorf("context cancelled while enqueueing event for node %s: %w", nodeName, ctx.Err())
	}

	select {
	case <-m.shutdown:
		return fmt.Errorf("queue is shutting down")
	default:
	}

	eventID := utils.ExtractEventID(event)
	traceID := ExtractTraceIDFromEvent(event)
	spanIDs := ExtractSpanIDsFromEvent(event)

	// Resolve the parent span ID at enqueue time from the span_ids map.
	// Node-drainer is downstream of fault-quarantine in the pipeline.
	parentSpanID := ""
	if spanIDs != nil {
		parentSpanID = spanIDs[tracing.ServiceFaultQuarantine]
	}

	nodeEvent := NodeEvent{
		NodeName:         nodeName,
		EventID:          eventID,
		Event:            &event,
		Database:         database,
		HealthEventStore: healthEventStore,
		TraceID:          traceID,
		ParentSpanID:     parentSpanID,
		DrainSessionSpan: drainSessionSpan,
	}

	slog.Debug("Enqueueing event", "nodeName", nodeName, "eventID", eventID)

	m.queue.Add(nodeEvent)
	metrics.QueueDepth.Set(float64(m.queue.Len()))

	return nil
}

// EnqueueEvent method has been removed - use EnqueueEventGeneric instead

func (m *eventQueueManager) Shutdown() {
	slog.Info("Shutting down workqueue")
	m.queue.ShutDown()
	close(m.shutdown)
	slog.Info("Workqueue shutdown complete")
}

// ExtractTraceIDFromEvent returns the trace_id from the event document (or fullDocument) for trace continuity.
func ExtractTraceIDFromEvent(event datastore.Event) string {
	doc := event
	if fullDoc, ok := event["fullDocument"].(map[string]interface{}); ok {
		doc = fullDoc
	}
	if tid, ok := doc["trace_id"].(string); ok {
		return tid
	}
	return ""
}

// ExtractSpanIDsFromEvent returns the span_ids map from the event document (or fullDocument).
func ExtractSpanIDsFromEvent(event datastore.Event) map[string]string {
	doc := event
	if fullDoc, ok := event["fullDocument"].(map[string]interface{}); ok {
		doc = fullDoc
	}
	raw, ok := doc["span_ids"]
	if !ok || raw == nil {
		return nil
	}
	switch m := raw.(type) {
	case map[string]interface{}:
		result := make(map[string]string, len(m))
		for k, v := range m {
			if s, ok := v.(string); ok {
				result[k] = s
			}
		}
		return result
	case map[string]string:
		return m
	default:
		return nil
	}
}

// DrainSessionMetrics accumulates durations and drain scope across process_event cycles so the drain_session span can show a single total.
type DrainSessionMetrics struct {
	ImmediateEvictionDurationMs     int64
	AwaitingPodCompletionDurationMs int64
	DrainScope                      string
	PartialDrainEntityType          string
	PartialDrainEntityValue         string
}

type drainSessionMetricsContextKey struct{}

var drainSessionMetricsKey = drainSessionMetricsContextKey{}

// ContextWithDrainSessionMetrics attaches metrics to ctx so the reconciler can add durations without creating per-cycle spans.
func ContextWithDrainSessionMetrics(ctx context.Context, m *DrainSessionMetrics) context.Context {
	return context.WithValue(ctx, drainSessionMetricsKey, m)
}

// DrainSessionMetricsFromContext returns the accumulator from ctx, or nil if not set.
func DrainSessionMetricsFromContext(ctx context.Context) *DrainSessionMetrics {
	if m, _ := ctx.Value(drainSessionMetricsKey).(*DrainSessionMetrics); m != nil {
		return m
	}
	return nil
}
