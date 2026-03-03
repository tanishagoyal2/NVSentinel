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

	"go.opentelemetry.io/otel/trace"
	"k8s.io/client-go/util/workqueue"

	"github.com/nvidia/nvsentinel/store-client/pkg/client"
	"github.com/nvidia/nvsentinel/store-client/pkg/datastore"
)

type NodeEvent struct {
	NodeName         string
	EventID          string                     // Unique event ID for deduplication (from _id field)
	Event            *datastore.Event           // Database-agnostic event data
	HealthEventStore datastore.HealthEventStore // New database-agnostic interface
	Database         DataStore                  // New database-agnostic interface

	// TraceID is the OpenTelemetry trace ID of the health event (from store).
	TraceID string
	// ParentSpanID is the resolved span ID of the upstream service, extracted from
	// the span_ids map at enqueue time using the known pipeline topology.
	ParentSpanID string
	// DrainSessionSpan is the parent span for all process_event cycles for this item. Set on first process, ended when processing completes (no requeue).
	DrainSessionSpan trace.Span

	// Cumulative durations across all process_event cycles for this drain session. Set on drain_session span when it ends.
	ImmediateEvictionDurationMs     int64
	AwaitingPodCompletionDurationMs int64
	// Drain scope (set once when action is known); recorded on drain_session span when it ends.
	DrainScope              string
	PartialDrainEntityType  string
	PartialDrainEntityValue string

	// Deprecated fields for backward compatibility
	EventBSON *map[string]interface{} // DEPRECATED: Use Event instead
	// Collection field has been removed - use Database instead
}

// DataStore provides database-agnostic operations using store-client
type DataStore interface {
	UpdateDocument(ctx context.Context, filter interface{}, update interface{}) (*client.UpdateResult, error)
	FindDocument(ctx context.Context, filter interface{}, options *client.FindOneOptions) (client.SingleResult, error)
	FindDocuments(ctx context.Context, filter interface{}, options *client.FindOptions) (client.Cursor, error)
}

// DataStoreEventProcessor provides database-agnostic event processing
type DataStoreEventProcessor interface {
	ProcessEventGeneric(ctx context.Context, event datastore.Event, database DataStore,
		healthEventStore datastore.HealthEventStore, nodeName string) error
}

// EventProcessor interface has been removed - use DataStoreEventProcessor instead

type EventQueueManager interface {
	// New database-agnostic method. drainSessionSpan optional: when set, event data is on this trace root (created at first enqueue).
	EnqueueEventGeneric(ctx context.Context, nodeName string, event datastore.Event, database DataStore,
		healthEventStore datastore.HealthEventStore, drainSessionSpan trace.Span) error

	// Deprecated EnqueueEvent method has been removed - use EnqueueEventGeneric instead

	Start(ctx context.Context)
	Shutdown()

	// New database-agnostic method
	SetDataStoreEventProcessor(processor DataStoreEventProcessor)

	// Deprecated SetEventProcessor method has been removed - use SetDataStoreEventProcessor instead
}

type eventQueueManager struct {
	queue                   workqueue.TypedRateLimitingInterface[NodeEvent]
	dataStoreEventProcessor DataStoreEventProcessor // New database-agnostic processor
	shutdown                chan struct{}
}
