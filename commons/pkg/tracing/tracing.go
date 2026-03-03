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

package tracing

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	tracerProvider *sdktrace.TracerProvider
	tracer         trace.Tracer
)

// InitTracing initializes OpenTelemetry tracing with OTLP exporter
// Falls back to console exporter if OTEL_EXPORTER_OTLP_ENDPOINT is not set
func InitTracing(serviceName string) error {
	// Get service name from environment or use provided
	if serviceName == "" {
		serviceName = os.Getenv("OTEL_SERVICE_NAME")
		if serviceName == "" {
			serviceName = "nvsentinel"
		}
	}

	// Create resource with service name
	res, err := resource.New(
		context.Background(),
		resource.WithAttributes(
			attribute.String("service.name", serviceName),
		),
	)
	if err != nil {
		return fmt.Errorf("failed to create resource: %w", err)
	}

	// Check if OTLP endpoint is configured
	otlpEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	otlpInsecure := os.Getenv("OTEL_EXPORTER_OTLP_INSECURE") == "true"

	var exporter sdktrace.SpanExporter

	if otlpEndpoint != "" {
		// Use OTLP exporter
		opts := []otlptracegrpc.Option{
			otlptracegrpc.WithEndpoint(otlpEndpoint),
		}

		if otlpInsecure {
			opts = append(opts, otlptracegrpc.WithTLSCredentials(insecure.NewCredentials()))
		}

		otlpExporter, err := otlptracegrpc.New(context.Background(), opts...)
		if err != nil {
			return fmt.Errorf("failed to create OTLP exporter: %w", err)
		}
		exporter = otlpExporter
		slog.Info("Initialized OTLP trace exporter", "endpoint", otlpEndpoint, "insecure", otlpInsecure)
	} else {
		// Fallback to console exporter for local development
		consoleExporter, err := stdouttrace.New(
			stdouttrace.WithPrettyPrint(),
		)
		if err != nil {
			return fmt.Errorf("failed to create console exporter: %w", err)
		}
		exporter = consoleExporter
		slog.Info("OTEL_EXPORTER_OTLP_ENDPOINT not set, using console exporter for traces")
	}

	// Create tracer provider
	tracerProvider = sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
	)

	// Set global tracer provider
	otel.SetTracerProvider(tracerProvider)

	// Set global propagator
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	// Create tracer
	tracer = otel.Tracer(serviceName)

	slog.Info("OpenTelemetry tracing initialized", "service", serviceName)

	return nil
}

// ShutdownTracing shuts down the tracer provider
func ShutdownTracing(ctx context.Context) error {
	if tracerProvider != nil {
		return tracerProvider.Shutdown(ctx)
	}
	return nil
}

// GetTracer returns the global tracer
func GetTracer() trace.Tracer {
	if tracer == nil {
		// Return a no-op tracer if not initialized
		return trace.NewNoopTracerProvider().Tracer("noop")
	}
	return tracer
}

// StartSpan starts a new span with the given name and options
func StartSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return GetTracer().Start(ctx, name, opts...)
}

// StartSpanFromTraceID starts a new span that belongs to an existing trace.
// When only traceID is provided, the span is placed under the same trace but
// without a parent-child link (sibling root span). For proper parent-child
// relationships, use StartSpanFromTraceContext with both traceID and spanID.
func StartSpanFromTraceID(ctx context.Context, traceID string, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return StartSpanFromTraceContext(ctx, traceID, "", name, opts...)
}

// StartSpanFromTraceContext starts a new span from an existing trace ID and
// optional parent span ID. This is the primary mechanism for continuing traces
// across service boundaries that communicate via database or CR annotations.
//
// When both traceID and parentSpanID are provided, the new span becomes a
// child of the remote parent, forming a proper parent-child relationship in
// the trace tree.
//
// When only traceID is provided (parentSpanID is empty), the span is placed
// under the same trace with a Span Link back to the trace origin, but without
// a direct parent-child relationship.
func StartSpanFromTraceContext(ctx context.Context, traceID, parentSpanID, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if traceID == "" {
		return StartSpan(ctx, name, opts...)
	}

	tid, err := trace.TraceIDFromHex(traceID)
	if err != nil {
		slog.Warn("Failed to parse trace ID, starting new trace", "traceID", traceID, "error", err)
		return StartSpan(ctx, name, opts...)
	}

	if parentSpanID != "" {
		sid, err := trace.SpanIDFromHex(parentSpanID)
		if err != nil {
			slog.Warn("Failed to parse parent span ID, falling back to trace-only",
				"traceID", traceID, "spanID", parentSpanID, "error", err)
		} else {
			// Full parent context: creates a proper parent-child relationship
			parentCtx := trace.NewSpanContext(trace.SpanContextConfig{
				TraceID:    tid,
				SpanID:     sid,
				TraceFlags: trace.FlagsSampled,
				Remote:     true,
			})
			ctx = trace.ContextWithSpanContext(ctx, parentCtx)
			return StartSpan(ctx, name, opts...)
		}
	}

	// Trace-only context: same trace but no parent-child link.
	// Add a Span Link to the trace origin so backends can correlate.
	remoteCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	ctx = trace.ContextWithSpanContext(ctx, remoteCtx)
	return StartSpan(ctx, name, opts...)
}

// SpanIDFromSpan returns the hex-encoded span ID of the given span,
// suitable for storing in a database or CR annotation for downstream
// services to use as a parent span ID.
func SpanIDFromSpan(span trace.Span) string {
	if span == nil {
		return ""
	}
	sc := span.SpanContext()
	if !sc.HasSpanID() {
		return ""
	}
	return sc.SpanID().String()
}

// Service name constants for the span_ids map. Each service writes its own key
// and reads its parent's key to establish parent-child trace relationships.
//
// Pipeline topology:
//
//	PlatformConnector ──┬── FaultQuarantine → NodeDrainer → FaultRemediation → Janitor
//	                    ├── HealthEventsAnalyzer
//	                    └── EventExporter
const (
	ServicePlatformConnector    = "platform_connector"
	ServiceFaultQuarantine      = "fault_quarantine"
	ServiceNodeDrainer          = "node_drainer"
	ServiceFaultRemediation     = "fault_remediation"
	ServiceHealthEventsAnalyzer = "health_events_analyzer"
	ServiceEventExporter        = "event_exporter"
)

// ParentSpanID looks up the parent service's span ID from the span_ids map.
// Returns empty string if the map is nil or the key is missing (graceful fallback
// to trace-only mode without parent-child link).
func ParentSpanID(spanIDs map[string]string, parentService string) string {
	if spanIDs == nil {
		return ""
	}
	return spanIDs[parentService]
}

// SetSpanAttributes sets attributes on a span
func SetSpanAttributes(span trace.Span, attrs ...attribute.KeyValue) {
	span.SetAttributes(attrs...)
}

// RecordError records an error on a span
func RecordError(span trace.Span, err error, opts ...trace.EventOption) {
	span.RecordError(err, opts...)
	span.SetStatus(codes.Error, err.Error())
}

// SpanFromContext extracts the span from a context
func SpanFromContext(ctx context.Context) trace.Span {
	return trace.SpanFromContext(ctx)
}

// Operation status values for fault_quarantine.operation.status and similar attributes.
// Use these for consistent filtering in Grafana/TraceQL (e.g. {.fault_quarantine.operation.status="error"}).
const (
	OperationStatusSuccess   = "success"
	OperationStatusError     = "error"
	OperationStatusThrottled = "throttled"
	OperationStatusCancelled = "cancelled"
	OperationStatusSkipped   = "skipped"
)

// SetOperationStatus sets the standard operation status attribute on a span.
// status should be one of OperationStatus* constants.
func SetOperationStatus(span trace.Span, status string, service string) {
	if span == nil {
		return
	}
	span.SetAttributes(attribute.String(fmt.Sprintf("%s.operation.status", service), status))
}
