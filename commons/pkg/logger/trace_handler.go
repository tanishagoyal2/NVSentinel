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
	"context"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel/trace"
)

type TraceContextHandler struct {
	inner slog.Handler
}

// NewTraceContextHandler returns a handler that delegates to inner after
// optionally attaching trace_id and span_id from ctx.
func NewTraceContextHandler(inner slog.Handler) *TraceContextHandler {
	return &TraceContextHandler{inner: inner}
}

func (h *TraceContextHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *TraceContextHandler) Handle(ctx context.Context, r slog.Record) error {
	span := trace.SpanFromContext(ctx)
	if span.SpanContext().IsValid() {
		r.AddAttrs(
			slog.String("trace_id", span.SpanContext().TraceID().String()),
			slog.String("span_id", span.SpanContext().SpanID().String()),
		)
	}

	return h.inner.Handle(ctx, r)
}

func (h *TraceContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceContextHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *TraceContextHandler) WithGroup(name string) slog.Handler {
	return &TraceContextHandler{inner: h.inner.WithGroup(name)}
}

// NewStructuredLoggerWithTraceCorrelation is like NewStructuredLogger but wraps
// the JSON handler with TraceContextHandler so slog.InfoContext(ctx, ...) lines
// include trace_id and span_id when ctx carries an active span.
func NewStructuredLoggerWithTraceCorrelation(module, version, level string) *slog.Logger {
	lev := ParseLogLevel(level)
	addSource := lev <= slog.LevelDebug

	jsonHandler := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level:     lev,
		AddSource: addSource,
	})

	return slog.New(NewTraceContextHandler(jsonHandler)).With("module", module, "version", version)
}
