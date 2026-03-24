# GPU Reset Job Tracing

## Problem

The janitor GPU reset flow creates a Kubernetes `Job` that runs [`gpu-reset/gpu_reset.sh`](../../gpu-reset/gpu_reset.sh) on the target node. The janitor controller already emits spans for `janitor.gpureset.reconcile` and attributes such as `janitor.reset_job.created`, `janitor.reset_job.completed`, and `janitor.reset_job.failed` (see [028-otel-integration.md](028-otel-integration.md)).

From the trace alone, operators cannot see **what happened inside** the Job: target discovery, persistence-mode handling, `nvidia-smi --gpu-reset`, post-reset health check, etc. That work is opaque until someone reads container logs.

This document describes two lightweight ways to improve observability for that script: **curl + OTLP HTTP** (real spans) and **structured JSON logs with trace context** (correlation only).

## Trace context into the Job

Unlike janitor → janitor-provider (synchronous gRPC with automatic W3C propagation via `otelgrpc`), the GPU reset Job is a **separate process** in another pod. There is no shared `context.Context` or gRPC metadata.

To tie script output to the same distributed trace as the health event, the janitor controller would inject environment variables when building the Job in [`newGpuResetJob`](../../janitor/pkg/controller/gpureset_controller.go) (alongside the existing `NVIDIA_GPU_RESETS` variable):

| Variable | Purpose |
|----------|---------|
| `TRACEPARENT` | W3C trace context string so child work shares the same trace ID and parents under `janitor.gpureset.reconcile` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP gRPC endpoint for Go services; for HTTP-based approaches, a separate HTTP base URL may be required (e.g. `http://collector:4318`) |

**Note:** Implementation of controller injection and Helm wiring is out of scope for this design; it is the prerequisite for both approaches below.

## What is `traceparent`?

`traceparent` is the W3C Trace Context format OpenTelemetry uses by default for propagation (e.g. gRPC metadata, HTTP headers). It is a single string with four dash-separated fields:

```text
00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01
```

| Field | Meaning |
|-------|---------|
| Version | `00` (current standard) |
| Trace ID | 32 hex characters — identifies the whole distributed trace |
| Parent span ID | 16 hex characters — the span that should be the parent of new work in this process |
| Trace flags | e.g. `01` = sampled |

For the GPU reset Job, the controller would set `TRACEPARENT` from the active reconcile span so downstream spans or logs share the same **trace ID** as platform-connector → fault-remediation → janitor.

Environment variable propagation uses the same value as the HTTP header (see [W3C Trace Context](https://www.w3.org/TR/trace-context/)).

---

## Approach 1: curl + OTLP HTTP

### Idea

Post **OpenTelemetry Protocol (OTLP)** JSON payloads to the collector’s HTTP receiver, typically `POST /v1/traces` on port **4318** (gRPC OTLP is usually **4317**). Each phase of `gpu_reset.sh` can become one or more spans with `traceId`, `spanId`, `parentSpanId`, and nanosecond start/end times.

### Sketch

1. Parse `TRACEPARENT` to obtain trace ID and parent span ID.
2. Generate a new random 16-character hex **span ID** for each phase.
3. Record start/end times in nanoseconds (`date` / `awk` or similar).
4. `curl -X POST` with `Content-Type: application/json` body matching OTLP JSON encoding for traces.

Collectors must expose the **OTLP HTTP** receiver; the same endpoint used for Go (`OTEL_EXPORTER_OTLP_ENDPOINT` on 4317) is not interchangeable without configuration.

### Pros

- **Real spans** in Tempo/Grafana: waterfall, phase duration, parent-child under the janitor reconcile span.
- **No extra binary** beyond `curl` (often already in the image or easy to add).
- **Full control** over span names and attributes for TraceQL.

### Cons

- **Verbose and fragile**: OTLP JSON is nested; manual construction in bash is error-prone.
- **Manual IDs and timestamps**: must generate valid span IDs and nanosecond times correctly.
- **Nested phases**: each child span needs correct `parentSpanId` (often the previous phase’s span ID), reimplementing a small span stack in shell.
- **Endpoint mismatch**: must use HTTP OTLP URL (e.g. `:4318`) unless the collector maps HTTP and gRPC on one port.
- **Many HTTP requests** if each phase is a separate export; failures must not break `nvidia-smi` when using `set -e`.
- **Maintenance**: OTLP JSON shape changes are harder to track than SDK usage.

---

## Approach 2: Structured JSON logs with trace context

### Idea

Emit **one JSON object per log line** (or structured fields) including at least `trace_id` and `span_id` parsed from `TRACEPARENT`, plus human-readable `msg` and optional phase labels. Log shippers (Fluent Bit, Alloy, etc.) index these fields in Loki or similar; operators jump from a trace to logs filtered by `trace_id`.

This mirrors the **log–trace correlation** pattern described in [028-otel-integration.md](028-otel-integration.md) (OTel log appender / `trace_id` on log lines).

### Sketch

1. Parse `TRACEPARENT`: `trace_id` = second field, optional `parent_span_id` = third field.
2. Replace the existing `log()` helper to print JSON lines, e.g. `{"time":"...","level":"INFO","msg":"...","service":"gpu-reset","trace_id":"...","span_id":"..."}`.
3. Optionally add a `phase` field (`determine_targets`, `pre_reset_pm`, `reset`, `post_reset_pm`, `health_check`).

### Pros

- **Minimal change** to the script: mostly logging format.
- **No OTLP or span construction** in bash.
- **Safe**: logging failures should not affect GPU reset success.
- **Aligns with existing NVSentinel direction** for correlating logs with traces via `trace_id`.

### Cons

- **Not spans**: phases do not appear in the trace waterfall; the Job remains a single logical step in Tempo unless janitor adds more spans.
- **Weaker timing story**: duration is inferred from log timestamps, not structured span duration.
- **Two UIs**: operators use Tempo for the trace and Loki (or equivalent) for script detail, linked by `trace_id`.
- **No TraceQL on script phases** unless you also emit metrics or spans elsewhere.

---

## Comparison

| Aspect | curl + OTLP HTTP | JSON logs with trace context |
|--------|------------------|------------------------------|
| Appears in trace UI as spans | Yes | No |
| Phase duration / waterfall | Yes | Approximate via log times |
| Implementation risk in bash | Higher | Lower |
| New dependencies | `curl` + HTTP OTLP endpoint | None (or JSON tooling) |
| Correlates with same `trace_id` | Yes | Yes |
| Works if OTLP HTTP not exposed | No | Yes (logs only need stdout) |

---

## Recommendation

- Prefer **structured JSON logs with trace context** when the goal is **fast, low-risk correlation** between the health-event trace and on-node script output, without new collector ports or fragile OTLP JSON in shell.

- Prefer **curl + OTLP HTTP** when the team needs **first-class spans** for each phase inside the same trace and is willing to invest in careful shell helpers, tests, and collector HTTP configuration (or a small wrapper binary later).

A practical path is to **start with JSON logs**, then add OTLP HTTP or a dedicated tool (e.g. `otel-cli`) if operators still need span-level visibility in Grafana.

## References

- Script: [`gpu-reset/gpu_reset.sh`](../../gpu-reset/gpu_reset.sh)
- Job construction: [`janitor/pkg/controller/gpureset_controller.go`](../../janitor/pkg/controller/gpureset_controller.go) (`newGpuResetJob`)
- Image: [`gpu-reset/Dockerfile`](../../gpu-reset/Dockerfile)
- Broader tracing design: [028-otel-integration.md](028-otel-integration.md)
