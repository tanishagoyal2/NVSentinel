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

package exporter

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/nvidia/nvsentinel/commons/pkg/tracing"
	"github.com/nvidia/nvsentinel/data-models/pkg/model"
	pb "github.com/nvidia/nvsentinel/data-models/pkg/protos"
	"github.com/nvidia/nvsentinel/event-exporter/pkg/config"
	"github.com/nvidia/nvsentinel/event-exporter/pkg/metrics"
	"github.com/nvidia/nvsentinel/event-exporter/pkg/sink"
	"github.com/nvidia/nvsentinel/event-exporter/pkg/transformer"
	"github.com/nvidia/nvsentinel/store-client/pkg/client"
)

type HealthEventsExporter struct {
	cfg            *config.Config
	dbClient       client.DatabaseClient
	source         client.ChangeStreamWatcher
	transformer    transformer.EventTransformer
	sink           sink.EventSink
	hasResumeToken bool
	workers        int
}

func New(
	cfg *config.Config,
	dbClient client.DatabaseClient,
	source client.ChangeStreamWatcher,
	transformer transformer.EventTransformer,
	sink sink.EventSink,
	hasResumeToken bool,
	workers int,
) *HealthEventsExporter {
	return &HealthEventsExporter{
		cfg:            cfg,
		dbClient:       dbClient,
		source:         source,
		transformer:    transformer,
		sink:           sink,
		hasResumeToken: hasResumeToken,
		workers:        workers,
	}
}

func (e *HealthEventsExporter) Run(ctx context.Context) error {
	switch {
	case e.cfg.Exporter.Backfill.Enabled && !e.hasResumeToken:
		slog.Info("Backfill enabled and no resume token found - running historical backfill",
			"maxAge", e.cfg.Exporter.Backfill.GetMaxAge(),
			"maxEvents", e.cfg.Exporter.Backfill.MaxEvents)

		if err := e.runBackfill(ctx); err != nil {
			slog.Error("Failed to run backfill", "error", err)
			return fmt.Errorf("backfill: %w", err)
		}

		slog.Info("Backfill complete")
	case e.cfg.Exporter.Backfill.Enabled && e.hasResumeToken:
		slog.Info("Backfill enabled but resume token exists - skipping backfill (already initialized)")
	default:
		slog.Info("Backfill disabled in configuration")
	}

	slog.Info("Starting event stream")

	e.source.Start(ctx)

	return e.streamEvents(ctx)
}

func (e *HealthEventsExporter) runBackfill(ctx context.Context) error {
	ctx, span := tracing.StartSpan(ctx, "event_exporter.backfill")
	defer span.End()

	startTime := time.Now().UTC()
	backfillStart := startTime.Add(-e.cfg.Exporter.Backfill.GetMaxAge())

	span.SetAttributes(attribute.String("event_exporter.backfill.status", "in_progress"))
	slog.Info("Starting backfill", "backfillStart", backfillStart, "maxAge", e.cfg.Exporter.Backfill.GetMaxAge())

	metrics.BackfillInProgress.Set(1)
	defer metrics.BackfillInProgress.Set(0)

	cursor, err := e.queryBackfillEvents(ctx, backfillStart, startTime)
	if err != nil {
		span.SetAttributes(
			attribute.String("event_exporter.backfill.status", "failed"),
			attribute.String("event_exporter.error.type", "backfill_query_error"),
			attribute.String("event_exporter.error.message", err.Error()),
		)
		tracing.RecordError(span, err)
		slog.Error("Failed to query backfill events", "error", err)
		return fmt.Errorf("query backfill events: %w", err)
	}
	defer cursor.Close(ctx)

	count, err := e.processBackfillCursor(ctx, cursor)
	if err != nil {
		durationMs := time.Since(startTime).Seconds() * 1000
		span.SetAttributes(
			attribute.String("event_exporter.backfill.status", "partial_success"),
			attribute.Int("event_exporter.backfill.events_processed", count),
			attribute.Float64("event_exporter.backfill.duration_ms", durationMs),
			attribute.String("event_exporter.error.type", "backfill_process_error"),
			attribute.String("event_exporter.error.message", err.Error()),
		)
		tracing.RecordError(span, err)
		slog.Error("Failed to process backfill cursor", "error", err)
		return fmt.Errorf("process backfill cursor: %w", err)
	}

	durationMs := time.Since(startTime).Seconds() * 1000
	span.SetAttributes(
		attribute.String("event_exporter.backfill.status", "success"),
		attribute.Int("event_exporter.backfill.events_processed", count),
		attribute.Float64("event_exporter.backfill.duration_ms", durationMs),
	)

	metrics.BackfillDuration.Observe(time.Since(startTime).Seconds())
	slog.Info("Backfill complete", "events", count)

	return nil
}

func (e *HealthEventsExporter) queryBackfillEvents(ctx context.Context, start, end time.Time) (client.Cursor, error) {
	if e.cfg.Exporter.Backfill.MaxEvents <= 0 {
		return nil,
			fmt.Errorf("invalid MaxEvents value %d: must be positive", e.cfg.Exporter.Backfill.MaxEvents)
	}

	filter := map[string]any{
		"createdAt": map[string]any{
			"$gte": start,
			"$lt":  end,
		},
	}

	limit := int64(e.cfg.Exporter.Backfill.MaxEvents)
	opts := &client.FindOptions{
		Limit: &limit,
	}

	cursor, err := e.dbClient.Find(ctx, filter, opts)
	if err != nil {
		slog.Error("Failed to query events", "error", err)
		return nil, fmt.Errorf("query events: %w", err)
	}

	return cursor, nil
}

func (e *HealthEventsExporter) processBackfillCursor(ctx context.Context, cursor client.Cursor) (int, error) {
	var rateLimiter *time.Ticker

	useRateLimiting := e.cfg.Exporter.Backfill.RateLimit > 0

	if useRateLimiting {
		rateLimiter = time.NewTicker(time.Second / time.Duration(e.cfg.Exporter.Backfill.RateLimit))
		defer rateLimiter.Stop()
	} else {
		slog.Info("Rate limiting disabled for backfill (RateLimit=0)")
	}

	count := 0

	for cursor.Next(ctx) {
		if ctx.Err() != nil {
			slog.Error("Context done", "error", ctx.Err())
			return count, ctx.Err()
		}

		healthEvent, err := e.decodeBackfillEvent(cursor)
		if err != nil {
			slog.Error("Failed to decode backfill event", "error", err)
			continue
		}

		if healthEvent == nil {
			slog.Debug("Skipping nil health event")
			continue
		}

		if err := e.publishWithRetry(ctx, healthEvent); err != nil {
			slog.Error("Failed to publish backfill event", "error", err)
			return count, err
		}

		count++

		metrics.BackfillEventsProcessed.Inc()

		if err := e.waitForRateLimit(ctx, useRateLimiting, rateLimiter); err != nil {
			slog.Error("Failed to wait for rate limit", "error", err)
			return count, fmt.Errorf("wait for rate limit: %w", err)
		}
	}

	if err := cursor.Err(); err != nil {
		slog.Error("Failed to get cursor error", "error", err)
		return count, fmt.Errorf("cursor error: %w", err)
	}

	return count, nil
}

func (e *HealthEventsExporter) decodeBackfillEvent(cursor client.Cursor) (*pb.HealthEvent, error) {
	var healthEventWithStatus model.HealthEventWithStatus
	if err := cursor.Decode(&healthEventWithStatus); err != nil {
		slog.Warn("Failed to decode event", "error", err)
		return nil, err
	}

	if healthEventWithStatus.HealthEvent == nil {
		slog.Debug("Skipping nil health event")
		return nil, nil
	}

	return healthEventWithStatus.HealthEvent, nil
}

func (e *HealthEventsExporter) waitForRateLimit(
	ctx context.Context,
	useRateLimiting bool,
	rateLimiter *time.Ticker,
) error {
	if useRateLimiting {
		select {
		case <-ctx.Done():
			slog.Error("Context done", "error", ctx.Err())
			return ctx.Err()
		case <-rateLimiter.C:
		}
	} else if ctx.Err() != nil {
		slog.Error("Context done", "error", ctx.Err())
		return ctx.Err()
	}

	return nil
}

func (e *HealthEventsExporter) streamEvents(ctx context.Context) error {
	numWorkers := e.workers
	if numWorkers <= 0 {
		numWorkers = 1
	}

	slog.Info("Starting event stream", "workers", numWorkers)

	return e.streamEventsConcurrent(ctx, numWorkers)
}

// streamEventsConcurrent dispatches events to a worker pool for parallel publishing.
// Resume tokens are advanced in order via a sequenceTracker.
func (e *HealthEventsExporter) streamEventsConcurrent(ctx context.Context, numWorkers int) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	pool := newWorkerPool(numWorkers, e.processEvent, e.source, cancel)

	// Run the worker pool in a separate goroutine.
	// It blocks until all workers finish and the token writer drains.
	poolErrCh := make(chan error, 1)

	go func() {
		poolErrCh <- pool.run(ctx)
	}()

	// Consumer loop: read events, unmarshal, and dispatch to workers.
	var seq uint64

	consumeErr := e.consumeAndDispatch(ctx, pool, &seq)

	pool.closeDispatch()

	// Wait for the pool to finish processing in-flight items
	poolErr := <-poolErrCh

	// When the pool triggers cancellation (e.g., publish failure), consumeErr
	// is just context.Canceled — prefer poolErr which has the root cause.
	if poolErr != nil {
		return fmt.Errorf("worker pool: %w", poolErr)
	}

	if consumeErr != nil {
		return fmt.Errorf("event consumer: %w", consumeErr)
	}

	return nil
}

func (e *HealthEventsExporter) consumeAndDispatch(
	ctx context.Context, pool *workerPool, seq *uint64,
) error {
	for {
		select {
		case <-ctx.Done():
			slog.Info("Context done", "error", ctx.Err())
			return ctx.Err()

		case healthEvent, ok := <-e.source.Events():
			if !ok {
				slog.Info("Event channel closed, finishing in-flight events")
				return nil
			}

			metrics.EventsReceived.Inc()

			*seq++

			if !pool.dispatch(ctx, workItem{
				seq:         *seq,
				event:       healthEvent,
				resumeToken: healthEvent.GetResumeToken(),
			}) {
				return ctx.Err()
			}
		}
	}
}

// processEvent handles a single change stream event: unmarshal, skip if
// invalid, or publish. Returns nil for skipped events (unmarshal errors,
// nil documents) so the token writer advances the resume token. Returns
// a non-nil error only for fatal publish failures.
func (e *HealthEventsExporter) processEvent(ctx context.Context, rawEvent client.Event) error {
	healthEventWithStatus, err := unmarshalHealthEventWithStatus(rawEvent)
	if err != nil {
		slog.Warn("Failed to unmarshal event", "error", err)
		metrics.TransformErrors.Inc()

		return nil
	}

	if healthEventWithStatus.HealthEvent == nil {
		slog.Debug("Skipping nil health event")
		return nil
	}

	traceID := tracing.TraceIDFromMetadata(healthEventWithStatus.HealthEvent.GetMetadata())
	parentSpanID := tracing.ParentSpanID(healthEventWithStatus.SpanIDs, tracing.ServicePlatformConnector)
	ctx, span := tracing.StartSpanWithLinkFromTraceContext(ctx, traceID, parentSpanID, "event_exporter.process_event")
	defer span.End()

	return e.publishWithRetry(ctx, healthEventWithStatus.HealthEvent)
}

func (e *HealthEventsExporter) publishWithRetry(ctx context.Context, event *pb.HealthEvent) error {
	// CloudEvents transform span
	ctx, transformSpan := tracing.StartSpan(ctx, "event_exporter.transform")
	transformStart := time.Now()
	cloudEvent, transformErr := e.transformer.Transform(event)
	transformDurationMs := time.Since(transformStart).Seconds() * 1000
	if transformErr != nil {
		transformSpan.SetAttributes(
			attribute.Bool("event_exporter.transform.success", false),
			attribute.Float64("event_exporter.transform.duration_ms", transformDurationMs),
			attribute.String("event_exporter.transform.error", transformErr.Error()),
			attribute.String("event_exporter.error.type", "transform_error"),
			attribute.String("event_exporter.error.message", transformErr.Error()),
		)
		tracing.RecordError(transformSpan, transformErr)
		transformSpan.End()
		metrics.TransformErrors.Inc()
		slog.Error("Failed to transform event", "error", transformErr)
		return fmt.Errorf("transform event: %w", transformErr)
	}
	transformSpan.SetAttributes(
		attribute.Bool("event_exporter.transform.success", true),
		attribute.Float64("event_exporter.transform.duration_ms", transformDurationMs),
	)
	transformSpan.End()

	// Publish to sink span (covers full retry loop)
	ctx, publishSpan := tracing.StartSpan(ctx, "event_exporter.publish")
	publishStart := time.Now()
	attempt := 0

	backoffConfig := wait.Backoff{
		Steps:    e.cfg.Exporter.FailureHandling.MaxRetries + 1,
		Duration: e.cfg.Exporter.FailureHandling.GetInitialBackoff(),
		Factor:   e.cfg.Exporter.FailureHandling.BackoffMultiplier,
		Jitter:   0.1,
		Cap:      e.cfg.Exporter.FailureHandling.GetMaxBackoff(),
	}

	err := wait.ExponentialBackoffWithContext(ctx, backoffConfig, func(ctx context.Context) (bool, error) {
		if attempt > 0 {
			slog.Info("Publish failed, retrying", "attempt", attempt)
		}

		publishErr := e.sink.Publish(ctx, cloudEvent)
		if publishErr == nil {
			slog.Debug("Publish succeeded", "attempt", attempt)
			metrics.PublishDuration.Observe(time.Since(publishStart).Seconds())
			metrics.EventsPublished.WithLabelValues(metrics.StatusSuccess).Inc()
			return true, nil
		}

		attempt++
		slog.Warn("Publish failed, retrying",
			"attempt", attempt,
			"maxRetries", e.cfg.Exporter.FailureHandling.MaxRetries,
			"error", publishErr)
		return false, nil
	})

	durationMs := time.Since(publishStart).Seconds() * 1000
	publishSpan.SetAttributes(
		attribute.Int("event_exporter.publish.retry_count", attempt),
		attribute.Float64("event_exporter.publish.duration_ms", durationMs),
	)
	if err != nil {
		publishSpan.SetAttributes(
			attribute.String("event_exporter.publish.status", "failure"),
			attribute.String("event_exporter.publish.error_type", "max_retries_exceeded"),
			attribute.String("event_exporter.error.type", "publish_error"),
			attribute.String("event_exporter.error.message", err.Error()),
		)
		tracing.RecordError(publishSpan, err)
		publishSpan.End()
		metrics.PublishErrors.WithLabelValues("max_retries_exceeded").Inc()
		metrics.EventsPublished.WithLabelValues(metrics.StatusFailure).Inc()
		slog.Error("Publish failed after max retries", "error", err)
		return fmt.Errorf("publish failed after %d retries: %w", e.cfg.Exporter.FailureHandling.MaxRetries, err)
	}
	publishSpan.SetAttributes(attribute.String("event_exporter.publish.status", "success"))
	publishSpan.End()
	return nil
}

func unmarshalHealthEventWithStatus(event client.Event) (model.HealthEventWithStatus, error) {
	var healthEventWithStatus model.HealthEventWithStatus
	if err := event.UnmarshalDocument(&healthEventWithStatus); err != nil {
		slog.Error("Failed to unmarshal document", "error", err)
		return healthEventWithStatus, fmt.Errorf("unmarshal document: %w", err)
	}

	return healthEventWithStatus, nil
}
