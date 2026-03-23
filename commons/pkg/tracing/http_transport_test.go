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
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestConditionalHTTPTracingRoundTripper_SetsURLFullAndStatus(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
	})

	inner := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusTeapot,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	})

	rt := NewConditionalHTTPTracingRoundTripper(inner)

	tr := otel.Tracer("test")
	ctx, parent := tr.Start(context.Background(), "parent")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://kubernetes.default.svc/api/v1/nodes/mynode", nil)
	require.NoError(t, err)

	resp, err := rt.RoundTrip(req)
	require.NoError(t, err)
	require.NotNil(t, resp)
	_ = resp.Body.Close()

	parent.End()

	spans := recorder.Ended()
	require.Len(t, spans, 2, "parent + http.client span")

	var attrs []attribute.KeyValue
	for _, s := range spans {
		if s.Name() == "http.client.get" {
			attrs = s.Attributes()
			break
		}
	}
	require.NotEmpty(t, attrs, "expected http.client.get child span with attributes")
	method, ok := stringAttr(attrs, "http.request.method")
	require.True(t, ok)
	assert.Equal(t, http.MethodGet, method)

	statusCode, ok := intAttr(attrs, "http.response.status_code")
	require.True(t, ok)
	assert.Equal(t, http.StatusTeapot, statusCode)

	fullURL, ok := stringAttr(attrs, "url.full")
	require.True(t, ok, "url.full missing")
	assert.Contains(t, fullURL, "/api/v1/nodes/mynode")

	_, hasHost := stringAttr(attrs, "server.address")
	assert.True(t, hasHost)
}

func stringAttr(attrs []attribute.KeyValue, key string) (string, bool) {
	k := attribute.Key(key)
	for _, kv := range attrs {
		if kv.Key == k {
			return kv.Value.AsString(), true
		}
	}

	return "", false
}

func intAttr(attrs []attribute.KeyValue, key string) (int, bool) {
	k := attribute.Key(key)
	for _, kv := range attrs {
		if kv.Key == k {
			return int(kv.Value.AsInt64()), true
		}
	}

	return 0, false
}

func TestHTTPClientSpanNameForMethod(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "http.client.get", HTTPClientSpanNameForMethod(http.MethodGet))
	assert.Equal(t, "http.client.get", HTTPClientSpanNameForMethod("get"))
	assert.Equal(t, "http.client.patch", HTTPClientSpanNameForMethod(http.MethodPatch))
	assert.Equal(t, "http.client.put", HTTPClientSpanNameForMethod(http.MethodPut))
	assert.Equal(t, "http.client.post", HTTPClientSpanNameForMethod(http.MethodPost))
	assert.Equal(t, "http.client.delete", HTTPClientSpanNameForMethod(http.MethodDelete))
	assert.Equal(t, "http.client.head", HTTPClientSpanNameForMethod(http.MethodHead))
	assert.Equal(t, "http.client.options", HTTPClientSpanNameForMethod(http.MethodOptions))
	assert.Equal(t, "http.client.trace", HTTPClientSpanNameForMethod("TRACE"))
	assert.Equal(t, "http.client.request", HTTPClientSpanNameForMethod(""))
}
