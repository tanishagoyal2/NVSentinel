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

package store

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/protobuf/proto"

	"github.com/nvidia/nvsentinel/commons/pkg/tracing"
	"github.com/nvidia/nvsentinel/data-models/pkg/model"
	"github.com/nvidia/nvsentinel/data-models/pkg/protos"
	"github.com/nvidia/nvsentinel/platform-connectors/pkg/ringbuffer"
	"github.com/nvidia/nvsentinel/store-client/pkg/client"
	_ "github.com/nvidia/nvsentinel/store-client/pkg/datastore/providers"
	"github.com/nvidia/nvsentinel/store-client/pkg/factory"
	"go.opentelemetry.io/otel/trace"
)

type DatabaseStoreConnector struct {
	// databaseClient is the database-agnostic client
	databaseClient client.DatabaseClient
	// resourceSinkClients are client for pushing data to the resource count sink
	ringBuffer *ringbuffer.RingBuffer
	nodeName   string
	maxRetries int
}

func new(
	databaseClient client.DatabaseClient,
	ringBuffer *ringbuffer.RingBuffer,
	nodeName string,
	maxRetries int,
) *DatabaseStoreConnector {
	return &DatabaseStoreConnector{
		databaseClient: databaseClient,
		ringBuffer:     ringBuffer,
		nodeName:       nodeName,
		maxRetries:     maxRetries,
	}
}

func InitializeDatabaseStoreConnector(ctx context.Context, ringbuffer *ringbuffer.RingBuffer,
	clientCertMountPath string, maxRetries int) (*DatabaseStoreConnector, error) {
	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		return nil, fmt.Errorf("NODE_NAME is not set")
	}

	// Create database client factory using store-client
	clientFactory, err := createClientFactory(clientCertMountPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create database client factory: %w", err)
	}

	// Create database client
	databaseClient, err := clientFactory.CreateDatabaseClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create database client: %w", err)
	}

	slog.Info("Successfully initialized database store connector", "maxRetries", maxRetries)

	return new(databaseClient, ringbuffer, nodeName, maxRetries), nil
}

func createClientFactory(databaseClientCertMountPath string) (*factory.ClientFactory, error) {
	// Always pass the cert path through explicitly. NewClientFactoryFromEnv()
	// falls back to DefaultCertMountPath even when TLS is disabled, causing
	// infinite cert polling. Using the explicit path variant ensures an empty
	// string (TLS disabled) propagates correctly.
	return factory.NewClientFactoryFromEnvWithCertPath(databaseClientCertMountPath)
}

func (r *DatabaseStoreConnector) FetchAndProcessHealthMetric(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			slog.Info("Context canceled, exiting health metric processing loop")
			return
		default:
			item, quit := r.ringBuffer.Dequeue()
			if quit {
				slog.Info("Queue signaled shutdown, exiting processing loop")
				return
			}

			healthEvents := item.Events
			if healthEvents == nil || len(healthEvents.GetEvents()) == 0 {
				r.ringBuffer.HealthMetricEleProcessingCompleted(item)
				continue
			}

			// Continue the trace from the gRPC handler so one trace shows both store and K8s processing.
			batchCtx := trace.ContextWithRemoteSpanContext(ctx, item.ParentSpanContext)
			batchCtx, span := tracing.StartSpan(batchCtx, "platform_connector.store.fetch_and_process_health_metric")
			eventCount := len(healthEvents.GetEvents())
			span.SetAttributes(
				attribute.Int("platform_connector.store.batch_event_count", eventCount),
			)
			if eventCount > 0 && healthEvents.GetEvents()[0] != nil {
				span.SetAttributes(attribute.String("platform_connector.store.node_name", healthEvents.GetEvents()[0].GetNodeName()))
			}

			err := r.insertHealthEvents(batchCtx, healthEvents)
			if err != nil {
				retryCount := r.ringBuffer.NumRequeues(item)
				tracing.RecordError(span, err)
				span.SetAttributes(
					attribute.String("platform_connector.store.error", err.Error()),
					attribute.Int("platform_connector.store.retry_count", retryCount),
					attribute.Int("platform_connector.store.max_retries", r.maxRetries),
				)
				if retryCount < r.maxRetries {
					span.SetAttributes(attribute.String("platform_connector.store.outcome", "retry"))
					slog.Warn("Error inserting health events, will retry with exponential backoff",
						"error", err,
						"retryCount", retryCount,
						"maxRetries", r.maxRetries,
						"eventCount", eventCount)

					r.ringBuffer.AddRateLimited(item)
				} else {
					span.SetAttributes(attribute.String("platform_connector.store.outcome", "dropped"))
					slog.Error("Max retries exceeded, dropping health events permanently",
						"error", err,
						"retryCount", retryCount,
						"maxRetries", r.maxRetries,
						"eventCount", eventCount,
						"firstEventNodeName", healthEvents.GetEvents()[0].GetNodeName(),
						"firstEventCheckName", healthEvents.GetEvents()[0].GetCheckName())
					r.ringBuffer.HealthMetricEleProcessingCompleted(item)
				}
			} else {
				span.SetAttributes(attribute.String("platform_connector.store.outcome", "success"))
				r.ringBuffer.HealthMetricEleProcessingCompleted(item)
			}
			span.End()
		}
	}
}

func (r *DatabaseStoreConnector) ShutdownRingBuffer() {
	if r.ringBuffer != nil {
		slog.Info("Shutting down database store connector ring buffer with drain")
		r.ringBuffer.ShutDownHealthMetricQueue()
		slog.Info("Database store connector ring buffer drained successfully")
	}
}

// Disconnect closes the database client connection
// Safe to call multiple times - will not error if already disconnected
func (r *DatabaseStoreConnector) Disconnect(ctx context.Context) error {
	if r.databaseClient == nil {
		return nil
	}

	err := r.databaseClient.Close(ctx)
	if err != nil {
		// Log but don't return error if already disconnected
		// This can happen in tests where mtest framework also disconnects
		slog.Warn("Error disconnecting database client (may already be disconnected)", "error", err)

		return nil
	}

	slog.Info("Successfully disconnected database client")

	return nil
}

func (r *DatabaseStoreConnector) insertHealthEvents(
	ctx context.Context,
	healthEvents *protos.HealthEvents,
) error {
	// Create root span for event reception and storage
	ctx, span := tracing.StartSpan(ctx, "platform_connector.receive_event")
	defer span.End()

	// Prepare all documents for batch insertion
	healthEventWithStatusList := make([]interface{}, 0, len(healthEvents.GetEvents()))

	for i, healthEvent := range healthEvents.GetEvents() {
		// CRITICAL FIX: Clone the HealthEvent to avoid pointer reuse issues with gRPC buffers
		// Without this clone, the healthEvent pointer may point to reused gRPC buffer memory
		// that gets overwritten by subsequent requests, causing data corruption in MongoDB.
		// This manifests as events having wrong isfatal/ishealthy/message values.
		clonedHealthEvent := proto.Clone(healthEvent).(*protos.HealthEvent)

		slog.Debug("Processing health event for insertion", "index", i, "nodeName", clonedHealthEvent.NodeName)

		healthEventWithStatusObj := model.HealthEventWithStatus{
			TraceID: span.SpanContext().TraceID().String(),
			SpanIDs: map[string]string{
				tracing.ServicePlatformConnector: tracing.SpanIDFromSpan(span),
			},
			CreatedAt:   time.Now().UTC(),
			HealthEvent: clonedHealthEvent,
			HealthEventStatus: &protos.HealthEventStatus{
				UserPodsEvictionStatus: &protos.OperationStatus{},
			},
		}
		healthEventWithStatusList = append(healthEventWithStatusList, healthEventWithStatusObj)

		// Add health event attributes to span (for first event in batch)
		if i == 0 {
			tracing.AddHealthEventAttributes(span, healthEventWithStatusObj.HealthEvent)
			span.SetAttributes(
				attribute.Int("platform_connector.grpc.events_count", len(healthEvents.GetEvents())),
			)
		}
	}

	slog.Debug("Inserting health events batch", "documentCount", len(healthEventWithStatusList))

	// Create child span for database operation
	ctx, dbSpan := tracing.StartSpan(ctx, "platform_connector.db.insert")
	defer dbSpan.End()

	dbSpan.SetAttributes(
		attribute.String("platform_connector.db.operation", "insert"),
		attribute.Int("platform_connector.db.document_count", len(healthEventWithStatusList)),
	)

	// Insert all documents in a single batch operation
	// This ensures MongoDB generates INSERT operations (not UPDATE) for change streams
	// Note: InsertMany is already atomic - either all documents are inserted or none are
	startTime := time.Now()
	_, err := r.databaseClient.InsertMany(ctx, healthEventWithStatusList)
	duration := time.Since(startTime)

	if err != nil {
		slog.Error("InsertMany failed", "error", err)
		tracing.RecordError(dbSpan, err)
		dbSpan.SetAttributes(
			attribute.String("platform_connector.error.type", "insert_many_failed"),
			attribute.String("platform_connector.error.message", err.Error()),
		)

		return fmt.Errorf("insertMany failed: %w", err)
	}

	dbSpan.SetAttributes(
		attribute.Float64("platform_connector.db.duration_ms", float64(duration.Nanoseconds())/1e6),
	)

	slog.Debug("InsertMany completed successfully")

	return nil
}

func GenerateRandomObjectID() string {
	return uuid.New().String()
}
