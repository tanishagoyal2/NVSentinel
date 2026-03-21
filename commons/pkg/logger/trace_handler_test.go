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

package logger

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestTraceContextHandler_WithActiveSpan(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	log := slog.New(NewTraceContextHandler(inner))

	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := tp.Tracer("test")

	ctx, span := tracer.Start(context.Background(), "test-op")
	log.InfoContext(ctx, "correlated message", "key", "value")
	span.End()

	out := buf.String()
	if !strings.Contains(out, `"trace_id"`) {
		t.Fatalf("expected trace_id in output, got: %s", out)
	}
	if !strings.Contains(out, `"span_id"`) {
		t.Fatalf("expected span_id in output, got: %s", out)
	}
	if !strings.Contains(out, "correlated message") {
		t.Fatalf("expected message in output, got: %s", out)
	}
}

func TestTraceContextHandler_WithoutSpan(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	log := slog.New(NewTraceContextHandler(inner))

	log.InfoContext(context.Background(), "no span context")

	out := buf.String()
	if strings.Contains(out, `"trace_id"`) {
		t.Fatalf("did not expect trace_id without span, got: %s", out)
	}
}

func TestNewStructuredLoggerWithTraceCorrelation(t *testing.T) {
	lg := NewStructuredLoggerWithTraceCorrelation("mod", "v1", "info")
	if lg == nil {
		t.Fatal("expected non-nil logger")
	}
	lg.Info("smoke test")
}
