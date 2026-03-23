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
	"fmt"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// HTTPClientSpanNameForMethod returns a span name http.client.<method> with the HTTP
// method lowercased (e.g. http.client.get, http.client.patch). Empty or whitespace-only
// method yields http.client.request.
func HTTPClientSpanNameForMethod(method string) string {
	m := strings.ToLower(strings.TrimSpace(method))
	if m == "" {
		return "http.client.request"
	}
	return fmt.Sprintf("http.client.%s", m)
}

// conditionalHTTPTracingRT wraps an http.RoundTripper and creates a child OpenTelemetry
// span per outbound HTTP request only when req.Context() already carries a valid span
// (ongoing trace). Otherwise the request passes through unchanged.
type conditionalHTTPTracingRT struct {
	next http.RoundTripper
}

// NewConditionalHTTPTracingRoundTripper returns an http.RoundTripper for use with
// client-go *rest.Config.Wrap, audit logging, or any HTTP client. Spans are created only
// when the incoming context has an active trace (see HTTPClientSpanNameForMethod).
//
// Span attributes follow OpenTelemetry HTTP client conventions where applicable:
// http.request.method, url.full (when URL is present), server.address (when host is present),
// and http.response.status_code. Full URLs improve debuggability but add more cardinality
// than host-only attributes.
func NewConditionalHTTPTracingRoundTripper(next http.RoundTripper) http.RoundTripper {
	if next == nil {
		next = http.DefaultTransport
	}
	return &conditionalHTTPTracingRT{next: next}
}

func (t *conditionalHTTPTracingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return t.next.RoundTrip(req)
	}

	parent := trace.SpanFromContext(req.Context())
	if !parent.SpanContext().IsValid() {
		return t.next.RoundTrip(req)
	}

	spanName := HTTPClientSpanNameForMethod(req.Method)
	ctx, span := StartSpan(req.Context(), spanName)
	defer span.End()

	req = req.WithContext(ctx)

	resp, err := t.next.RoundTrip(req)

	attrs := []attribute.KeyValue{
		attribute.String("http.request.method", req.Method),
	}
	if req.URL != nil {
		attrs = append(attrs, attribute.String("url.full", req.URL.String()))
		if req.URL.Host != "" {
			attrs = append(attrs, attribute.String("server.address", req.URL.Host))
		}
	}
	if resp != nil {
		attrs = append(attrs, attribute.Int("http.response.status_code", resp.StatusCode))
		if resp.StatusCode >= 400 {
			span.SetStatus(codes.Error, fmt.Sprintf("HTTP %d", resp.StatusCode))
		}
	}
	SetSpanAttributes(span, attrs...)

	if err != nil {
		RecordError(span, err)
	}

	return resp, err
}
