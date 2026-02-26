# OpenTelemetry Tracing for NVSentinel

## Goal

To debug health events in NVSentinel, operators must manually correlate logs across multiple modules (platform-connector, fault-quarantine, node-drainer, fault-remediation, health-events-analyzer) to understand an event's complete lifecycle. OpenTelemetry traces eliminate this manual work by providing end-to-end visibility of each health event's journey through all modules in a single unified trace, making debugging faster and system behavior more transparent.

## Overview: Trace, Span, and Span Attributes

- **Trace** — A trace represents one request or workflow (e.g. one health event) as it moves through the system. It has a unique **trace ID** and is the top-level container for all related work. In NVSentinel, a single health event from ingestion through quarantine, drain, and remediation forms one trace.

- **Span** — A span is a single unit of work within a trace (e.g. "process event", "create remediation CR", "run log collector"). Each span has a name, start and end time, and can have parent and child spans. Spans are nested: one trace contains many spans, and spans can contain child spans. Together they form a timeline and call graph for the event.

- **Span attributes** — Key-value metadata attached to a span (e.g. `node.name`, `remediation.cr.name`, `event.processing_status`). They describe what happened during that span without needing to search logs. Attributes are used for filtering and grouping in tracing UIs (e.g. Grafana Tempo) and for understanding why a span succeeded or failed.

## How Can Traces Be Helpful?
- **Module-level performance**: How long does fault-quarantine take vs node-drainer vs fault-remediation?
- **Database query latency**: Time spent in MongoDB aggregation pipelines (health-events-analyzer)
- **Granular level performance**: Time spent in cordon, taint, drain, and CR creation operations
- **Event lifecycle tracking**: From detection → ingestion → quarantine → drain → remediation
- **Remediation status**: Was the last remediation succeeded or failed? 
- **Multi-module coordination**: See which modules are actively processing the same event like fault-quarantine and health-events-analyzer pick up event at the same time
- **Context preservation**: All relevant event metadata is attached to spans, eliminating the need to login to the cluster and to search logs in each module
- **Concurrent processing**: Understand how multiple events are processed simultaneously
- **Circuit breaker activity**: Monitor when circuit breaker is triped

## How Traces Are Different From Logs and Metrics?

Logs are discrete, unordered (or time-ordered) messages from each service. To follow one health event across platform-connector, fault-quarantine, node-drainer, fault-remediation, and health-events-analyzer, you must search by event ID or node name, align timestamps, and mentally reconstruct the flow.
Traces are structured around a single request or workflow: one trace ID ties together all spans from all modules for that event, with an explicit parent-child and timeline.

- **End-to-end view of one event** — One trace shows the full path of one health event (ingestion → quarantine → drain → remediation). With logs you must correlate multiple services by hand.
- **Timing and bottlenecks** — Spans have start/end times and nesting, so you see exactly where time was spent (e.g. DB query execution in health-events-analyzer vs draining operation in node-drainer). Logs give timestamps but not a single timeline or hierarchy.
- **Structured context without log parsing** — Span attributes (e.g. `node.name`, `drain.status`, `event.processing_status`) are queryable and filterable in the trace UI. No need to grep or parse log lines.
- **Cross-module flow** — Traces show which modules touched the same event and in what order. Logs are per-service; correlating across modules is manual.
- **Failure diagnosis** — A failed span is visible in the trace with status and attributes; you see the failing step and its parent path. With logs you must infer causality from messages and timestamps.
- **Performance and SLOs** — Trace-based latency percentiles and service maps are built-in. With logs you’d need custom metrics or log-based metrics to get the same view.

Logs are useful for detailed, free-form messages (e.g. stack traces, debug dumps). Traces complement them by giving a structured, request-scoped view of *where*, *what* and *how long* work happened across the breakfix pipeline.
Traces are not to replace logs — we are adding it as an additional feature alongside existing logging to improve debugging and analyze system performance.

Metrics (e.g. Prometheus) are aggregated over time: counters, gauges, and histograms that answer "how much?" and "how fast on average?" (e.g. `fault_quarantine_event_handling_duration_seconds`, event counts per module).
Traces are per-request: each health event gets one trace with spans across all modules, answering "what happened for this event?", "where did this event spend most time?" and "where this event handling failed and why?"

- **Granularity:** Metrics are aggregated over time (e.g. p50/p99 latency, rate per minute). Traces are per event: one trace per health event.
- **Question answered:** Metrics answer "How is the system behaving overall?" Traces answer "Why was this event slow or failed? Why was this node not remediated? "
- **Use case:** Metrics support dashboards, alerting, SLOs, and capacity planning. Traces support debugging a specific event and finding bottlenecks in a single flow.

## Architecture Diagram

The following diagram shows how traces flow from NVSentinel modules to the Alloy gateway and onward to Grafana dashboard.

```
┌─────────────────────────────────────────────────────────────────────────────-┐
│                         NVSentinel (trace producers)                         │
├────────────────────────────────────────────────────────────────────────────-─┤
│  platform-connector   fault-quarantine   node-drainer   fault-remediation    │
│  health-events-analyzer                                                      │
│         │                    │                │                │             │
│         │  OTLP (GRPC :4317) │                │                │             │
│         └────────────────────┴────────────────┴────────────────┴─────────────┤
│                                               │                              │
└────────────────────────────────────────-──────┼──────────────────────────────┘
                                               │
                                               ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  Alloy Gateway (dgxc-alloy-gateway.dgxc-alloy.svc.cluster.local:4317)        │
│  - Receives OTLP traces from all NVSentinel modules (no auth; in-cluster)    │
│  - Batches and forwards to Panoptes                                          │
│  - Authenticates to Panoptes via OAuth2 (panoptes secret in dgxc-alloy NS)   │
└──────────────────────────────────────────────┬───────────────────────────────┘
                                               │
                                               │ OTLP HTTP + OAuth2 (auth done here)
                                               ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│  Panoptes (otel-traces.us-east-1.aws.telemetry.dgxc.ngc.nvidia.com)          │
│  - Centralized trace storage                                                 │
└──────────────────────────────────────────────┬───────────────────────────────┘
                                               │
                                               ▼
┌──────────────────────────────────────────────────────────────────────────────-┐
│  Grafana Tempo → Grafana Dashboards (dashboards.telemetry.dgxc.ngc.nvidia.com)│
│  - Trace explorer, service map, latency analysis                              │
└──────────────────────────────────────────────────────────────────────────────-┘
```

**Trace flow summary:**

1. **Ingestion**: Each NVSentinel module exports spans via OTLP over gRPC to `dgxc-alloy-gateway.dgxc-alloy.svc.cluster.local:4317`. No authentication is required from NVSentinel to the gateway (in-cluster, `OTEL_EXPORTER_OTLP_INSECURE=true`).
2. **Gateway**: The Alloy gateway receives traces, batches them, and authenticates to Panoptes using OAuth2 (credentials from the `panoptes` secret in the `dgxc-alloy` namespace). 
3. **Backend**: Panoptes stores traces; Grafana Tempo is used as the datasource for querying and visualizing traces in Grafana.

## What Are We Planning to Track from Each Module?

### Fault-Quarantine Actions to Track

| What to Track? | Why? | Span Attributes |
|----------------|------|----------------|
| **Cordon + Taint Operation** | Determine if a health event resulted in quarantining a node (cordon, taint, or both). Critical for understanding what actions were taken. | `quarantine.action.cordon` (bool)<br>`quarantine.action.taint` (bool)<br>`event.processing_status = "Quarantined"` |
| **Unquarantine Operation** | Determine if a health event resulted in unquarantining a node | `quarantine.action.uncordon` (bool)<br>`taints.removed` (bool)<br>`event.processing_status = "UnQuarantined"` |
| **Event Processing Status** | Track event lifecycle from reception to completion. Helps identify bottlenecks, queue delays, and processing states. | `event.processing_status = "waiting_to_be_processed" → "processing"` |
| **Processing Errors** | Track all errors that occur during event processing. Essential for debugging, identifying failure patterns, and understanding error rates. | `event.processing_status = "error"`<br>`error.message` = error details |
| **Node Labels and Annotations** | Track when and what labels/annotations were applied on the node. Helps verify what metadata was set for the event and debug scheduling or visibility issues. | `quarantine.labels_applied` (bool)<br>`quarantine.annotations_applied` (bool)<br>`quarantine.cordon_reason` (list, optional) |
| **Ruleset Matched** | Track which ruleset(s) matched for the event. Explains why quarantine was triggered (which CEL rule matched); helps debug rule configuration, audit quarantine decisions, and filter traces by ruleset. | `quarantine.ruleset.matched` (string or list)<br>`quarantine.ruleset.names` (list, optional) |

### Node Drainer Actions to Track

| What to Track? | Why? | Span Attributes |
|----------------|------|----------------|
| **Drain Status** | Track final drain status (succeeded, failed, cancelled, already_drained). | `drain.status` = "succeeded", "failed", "cancelled", "already_drained", "in_progress" |
| **Timeout Eviction** | Track when pods are force-deleted after timeout. Important for understanding timeout-based drain operations and stuck pods. | `drain.forceDeletedPods` = ["pod-0", "pod-1"]<br>`drain.timeout_eviction_count` (int) |
| **Partial vs Full Drain** | Track whether a partial drain (specific GPU) or full drain (entire node) was executed. Critical for understanding drain scope. | `drain.scope` = "partial" or "full"<br>`drain.partial_drain.entity_type` (string)<br>`drain.partial_drain.entity_value` (string) |
| **Processing Errors** | Track all errors that occur during drain processing. Essential for debugging and identifying failure patterns. | `drain.status` = "error" or "failed"<br>`error.type` = specific error type<br>`error.message` (string)<br>Span status: `codes.Error` |
| **Custom Drain CR Status** | Track custom drain CR creation and completion status. Critical for custom drain workflows. | `drain.custom_cr.name` (string)<br>`drain.custom_cr.status` (string)<br>`drain.custom_cr.created` (bool) |

### Fault-Remediation Actions to Track

| What to Track? | Why? | Span Attributes |
|----------------|------|----------------|
| **Remediation Action Type** | Determine which remediation action was executed (create CR, skip, cancellation, etc.). Critical for understanding what actions were taken. | `remediation.action.type` = "create_cr", "skip", "cancellation" |
| **Event Skip Reasons** | Track why events are skipped (NONE action, already remediated, unsupported action). Important for understanding skipped remediations. | `remediation.action.skip` (bool)<br>`remediation.skip_reason` = "none_action", "already_remediated", "unsupported_action" |
| **Existing CR Detection** | Track when remediation is skipped because a maintenance CR already exists. Important for understanding duplicate prevention. | `remediation.action.skip_existing_cr` (bool)<br>`remediation.existing_cr.name` (string)<br>`remediation.existing_cr.status` (string) |
| **Remediation Status** | Track final remediation status (succeeded, failed). Essential for understanding outcomes. | `remediation.status` = "succeeded", "failed" |
| **Processing Errors** | Track all errors that occur during remediation processing. Essential for debugging and identifying failure patterns. | `remediation.status` = "error" or "failed"<br>`error.type` = specific error type<br>`error.message` (string)<br>Span status: `codes.Error` |

### Health-Events-Analyzer Actions to Track

| What to Track? | Why? | Span Attributes |
|----------------|------|----------------|
| **Ruleset Execution** | Track aggregation pipeline execution time and results. Essential for identifying slow queries and optimization opportunities. | `analyzer.mongo.pipeline.stages` (int)<br>`analyzer.mongo.pipeline.duration_ms` (float)<br>`analyzer.mongo.pipeline.documents_matched` (int) |
| **Event Publication** | Track when analyzer publishes new fatal events. Important for understanding event correlation and escalation. | `analyzer.event.published` (bool)<br>`analyzer.event.published_event_id` (string)<br>`analyzer.event.recommended_action` (string) |
| **XID Burst Detection** | Track XID burst detection operations including history clearing and burst detection. Critical for understanding XID error handling. | `analyzer.xid.burst_detected` (bool)<br>`analyzer.xid.burst_count` (int)<br>`analyzer.xid.history_cleared` (bool)<br>`analyzer.xid.node` (string) |
| **Processing Errors** | Track all errors during rule evaluation and event processing. Essential for debugging analyzer failures. | `analyzer.error.type` = "rule_evaluation_error", "pipeline_error", "publish_error"<br>`analyzer.error.message` (string)<br>Span status: `codes.Error` |

### Platform-Connector Actions to Track

| What to Track? | Why? | Span Attributes |
|----------------|------|----------------|
| **gRPC Event Reception** | Track when health events are received from monitors. Critical for understanding event ingestion. | `platform_connector.grpc.event_received` (bool)<br>`platform_connector.grpc.events_count` (int)<br>`platform_connector.grpc.duration_ms` (float) |
| **Event Persistence** | Track MongoDB write operations. Essential for understanding database performance. | `platform_connector.db.operation` = "insert", "update"<br>`platform_connector.db.duration_ms` (float)<br>`platform_connector.db.error` (string, if failed) |
| **Node Condition Updates** | Track Kubernetes node condition updates. | `platform_connector.k8s.node_condition.updated` (bool) |
| **Processing Errors** | Track errors during event ingestion and processing. Essential for debugging ingestion failures. | `platform_connector.error.type` = "grpc_error", "db_error", "k8s_error"<br>`platform_connector.error.message` (string)<br>Span status: `codes.Error` |

### Event Exporter Actions to Track

| What to Track? | Why? | Span Attributes |
|----------------|------|----------------|
| **Transform to CloudEvents** | Track transform success/failure and duration. Event-exporter converts health events to CloudEvents before publish; failures here block delivery to the sink. | `event_exporter.transform.success` (bool)<br>`event_exporter.transform.duration_ms` (float)<br>`event_exporter.transform.error` (string, if failed) |
| **Publish to Sink** | Track publish outcome, retry count, and latency. Essential to see if events reached the external sink (e.g. HTTP) and whether retries were needed. | `event_exporter.publish.status` = "success", "failure"<br>`event_exporter.publish.retry_count` (int)<br>`event_exporter.publish.duration_ms` (float)<br>`event_exporter.publish.error_type` (string, if failed; e.g. "max_retries_exceeded") |
| **Processing Errors** | Track unmarshal, transform, and publish errors. Essential for debugging why events were not exported. | `event_exporter.error.type` = "unmarshal_error", "transform_error", "publish_error"<br>`event_exporter.error.message` (string)<br>Span status: `codes.Error` |
| **Backfill** | Track when event-exporter runs a backfill (replay of historical events). Helps correlate export latency or load with backfill runs. | `event_exporter.backfill.in_progress` (bool)<br>`event_exporter.backfill.events_processed` (int)<br>`event_exporter.backfill.duration_ms` (float) |

### Janitor (GPUReset Controller) Actions to Track
The janitor reconciles GPUReset custom resources: it tears down GPU/managed services on the node, creates a reset Job, waits for job completion, then restores services. Tracking these actions helps debug why a reset succeeded, failed, or stalled.

| What to Track? | Why? | Span Attributes |
|----------------|------|----------------|
| **Reconcile Phase / Condition** | Track which phase the GPUReset is in (Pending, InProgress, Succeeded, Failed) and the current condition (Ready, ServicesTornDown, ResetJobCreated, ResetJobCompleted, ServicesRestored). Essential to see where a reset is stuck or why it failed. | `janitor.gpureset.name` (string)<br>`janitor.gpureset.phase` = "Pending", "InProgress", "Succeeded", "Failed", "Terminating", "Unknown"<br>`janitor.gpureset.condition` (string)<br>`janitor.gpureset.reason` (string) |
| **Node Lock** | Track when the controller acquires or releases the node lock (distributed lock). Explains ResourceContention and requeue behavior. | `janitor.node_lock.acquired` (bool)<br>`janitor.node_lock.node` (string) |
| **Service Teardown / Restore** | Track teardown and restore of GPU/managed services. | `janitor.services.teardown.success` (bool)<br>`janitor.services.teardown.duration_ms` (float)<br>`janitor.services.restore.success` (bool)<br>`janitor.services.restore.duration_ms` (float) |
| **Reset Job** | Track reset Job creation and completion. The Job runs the actual GPU reset on the node. | `janitor.reset_job.created` (bool)<br>`janitor.reset_job.name` (string)<br>`janitor.reset_job.completed` (bool)<br>`janitor.reset_job.failed` (bool) |
| **Completion / Failure Reason** | Track final outcome and failure reason (e.g. NodeNotFound, ResetJobFailed, ServiceTeardownTimeoutExceeded). Critical for understanding why a reset did not succeed. | `janitor.gpureset.completion_time` (string, optional)<br>`janitor.gpureset.failure_reason` (string; e.g. "ResetJobFailed", "ServiceRestoreFailed")<br>`event.processing_status` = "succeeded", "failed" |
| **Processing Errors** | Track errors during reconcile (e.g. job creation failure, drift detection). Essential for debugging janitor failures. | `janitor.error.type` = "job_creation_failed", "teardown_timeout", "restore_failed", "node_not_found"<br>`janitor.error.message` (string)<br>Span status: `codes.Error` |

### Janitor-Provider (CSP gRPC) Actions to Track

| What to Track? | Why? | Span Attributes |
|----------------|------|----------------|
| **SendRebootSignal** | Track when a reboot signal is sent to the CSP for a node. Essential to confirm the reboot was requested and to measure CSP API latency. | `janitor_provider.reboot.sent` (bool)<br>`janitor_provider.reboot.request_ref` (string)<br>`janitor_provider.reboot.node` (string)<br>`janitor_provider.reboot.duration_ms` (float) |
| **IsNodeReady** | Track node readiness checks after reboot. Helps debug "reset job ran but node never came back" scenarios. | `janitor_provider.node_ready.ready` (bool) |
| **SendTerminateSignal** | Track when a terminate signal is sent to the CSP. Used for node termination flows. | `janitor_provider.terminate.sent` (bool)<br>`janitor_provider.terminate.request_ref` (string) |
| **Processing Errors** | Track gRPC or CSP API errors (e.g. provider ID missing, CSP throttling). Essential for debugging provider failures. | `janitor_provider.error.type` = "grpc_error", "csp_api_error"<br>`janitor_provider.error.message` (string)<br>Span status: `codes.Error` |

## Trace Context Propagation

### Why Do We Need Context Propagation?

Without trace context propagation, each service would create its own trace with a different trace ID. You would see multiple unrelated traces (one per module) and would have to correlate logs, timestamps, and event IDs by hand to understand the full lifecycle of a health event. That makes it hard to see where time was spent, where failures occurred, or why an event was slow.

With context propagation, the trace ID is carried with the event (e.g. in metadata) from one module to the next. Every module that handles the same event continues the same trace and adds its own spans. The result is a single trace that shows the entire journey of the event—from ingestion through quarantine, drain, and remediation—so you can see the full timeline, spot bottlenecks, and debug failures in one place.

### Cross-Service Trace Continuity

NVSentinel uses distributed tracing to maintain trace context across all modules.

**Trace ID as Separate MongoDB Field (Recommended)**
```
Health Monitor (sends event, NO trace context)
    │
    │ [1. gRPC Call - just health event data]
    ▼
Platform Connector (creates ROOT trace, trace_id: abc123)
    │
    │ [2. MongoDB Storage - trace_id stored as separate top-level field]
    ▼
MongoDB (stores event with trace_id as separate field)
    │
    │ [3. MongoDB Change Streams - trace context extracted from top-level trace_id field]
    ├──► Fault Quarantine (continues trace abc123)
    ├──► Node Drainer (continues trace abc123)
    ├──► Fault Remediation (continues trace abc123)
    ├──► Health Events Analyzer (continues trace abc123)
    ├──► Event Exporter (continues trace abc123)
    ├──► Janitor (continues trace abc123)
    └──► Janitor provider (continues trace abc123)
```

**All modules share the same trace ID (`abc123`)** - this is only possible with context propagation at each step.

## Trace Context Propagation Options

Trace context is stored as a separate top-level field in the MongoDB document, keeping observability data separate from business logic.

**How it works technically**:
- Health monitor detects event and sends gRPC call (no trace context needed)
- Platform-connector receives the event via gRPC
- Platform-connector **creates a new trace** (trace_id: abc123) when receiving the event
- Platform-connector creates root span: `platform_connector.receive_event`
- Platform-connector stores event in MongoDB with `trace_id` as a **separate top-level field**:
  ```go
  healthEventWithStatus := model.HealthEventWithStatus{
      TraceID:           traceID,  // <-- NEW field
      CreatedAt:         time.Now().UTC(),
      HealthEvent:       clonedHealthEvent,
      HealthEventStatus: HealthEventStatus{},
  }
  ```
- MongoDB document structure:
  ```json
  {
    "_id": "...",
    "trace_id": "abc123",  // <-- top-level field
    "createdAt": "...",
    "healthevent": { ... },
    "healtheventstatus": { ... }
  }
  ```
- Downstream modules extract trace context from the top-level `trace_id` field when processing change streams
- Modules unmarshal into `HealthEventWithStatus` struct which includes `TraceID` field

**Required Code Changes**:
1. Add `TraceID` field to `HealthEventWithStatus` struct:
   ```go
   type HealthEventWithStatus struct {
       TraceID           string            `bson:"trace_id" json:"trace_id"`
       CreatedAt         time.Time         `bson:"createdAt"`
       HealthEvent       *protos.HealthEvent `bson:"healthevent,omitempty"`
       HealthEventStatus HealthEventStatus   `bson:"healtheventstatus"`
   }
   ```
2. Platform-connector generates trace ID and sets it when creating `HealthEventWithStatus`
3. All fault handling modules can access `healthEventWithStatus.TraceID` after unmarshaling


- **Separates observability from business logic** - trace_id is not mixed with health event data
- **Consistent with current unmarshaling approach** - modules already unmarshal into `HealthEventWithStatus` struct
- **No health monitor changes needed** - health monitors don't need to be OpenTelemetry-aware, any new custom/nvsentinel health monitor doesn't need to have changes for traces 

## Implementation Details

### Span Creation Strategy

**Where is the Trace ID Created?**

The trace ID creation depends on which trace context propagation option you choose:

#### Platform-Connector Creates Root Trace

**Platform-Connector** creates the root trace when it receives a health event via gRPC:
- **Root Span**: Created when platform-connector receives a health event via gRPC
- **Span name**: `platform_connector.receive_event`
- **Trace ID generated here** in platform-connector
- **No trace context propagation needed from health monitor**: Health monitor just sends the gRPC call with the health event
- Platform-connector stores trace ID as separate MongoDB field
- **Advantages**:
  - Simpler implementation (only platform-connector needs tracing)
  - Health monitors don't need OpenTelemetry instrumentation
  - Cleaner separation of observability from business logic
  - Consistent with current unmarshaling pattern

**(Platform-Connector creates trace, stored as separate field)**:
```
Health Monitor:
  1. Detect event
  2. Make gRPC call with:
     - Health event data (protobuf)

Platform Connector:
  1. Receive gRPC call
  2. Create NEW trace (trace_id: abc123)
  3. Create root span: "platform_connector.receive_event"
  4. Store event in MongoDB with trace_id as separate top-level field
  5. Start new trace (trace_id: abc123)
```

2. **Module Spans**: Each module creates spans for its processing:
   - **Fault-Quarantine**: `fault_quarantine.process_event`
   - **Node-Drainer**: `node_drainer.process_event`
   - **Fault-Remediation**: `fault_remediation.process_event`
   - **Health-Events-Analyzer**: `health_events_analyzer.process_event`
   - **Event Exporter**: `event_exporter.process_event`
   - **Janitor**: `janitor.process_event`
   - **Janitor Provider**: `janitor_provider.process_event`

3. **Operation Spans**: Within each module, create child spans for specific operations:
   - Database queries
   - Kubernetes API calls
   - Rule evaluations
   - CR creation


### Trace Export

- **OTLP Exporter**: Export traces via OTLP (OpenTelemetry Protocol) to Alloy gateway, which forwards to Panoptes (Tempo backend)
- **Batch Export**: Use batching to reduce overhead (handled by Alloy gateway)
- **Retry Logic**: Implement retry logic for failed exports (handled by Alloy gateway)

### Backend Integration: Alloy Gateway + Panoptes

NVSentinel uses **Grafana Alloy Gateway** as the OTLP receiver, which forwards traces to **Panoptes** (NVIDIA's centralized observability platform). This provides centralized trace collection and visualization in Grafana.

#### Architecture

```
NVSentinel Modules (platform-connector, fault-quarantine, etc.)
    │
    │ [OTLP gRPC :4317]
    ▼
Alloy Gateway (dgxc-alloy-gateway.dgxc-alloy.svc.cluster.local:4317)
    │
    │ [OTLP HTTP]
    ▼
Panoptes (otel-traces.us-east-1.aws.telemetry.dgxc.ngc.nvidia.com)
    │
    ▼
Grafana Tempo Datasource
    │
    ▼
Grafana Dashboards (dashboards.telemetry.dgxc.ngc.nvidia.com)
```

#### Configuration

**NVSentinel Module Configuration**:

Each NVSentinel module (platform-connector, fault-quarantine, node-drainer, fault-remediation, health-events-analyzer) should be configured with the following environment variables:

```yaml
env:
  # OTLP endpoint - Alloy gateway service (gRPC on 4317)
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "dgxc-alloy-gateway.dgxc-alloy.svc.cluster.local:4317"
  
  # Use insecure connection (ClusterIP service, no TLS)
  - name: OTEL_EXPORTER_OTLP_INSECURE
    value: "true"
  
  # Service name for trace identification
  - name: OTEL_SERVICE_NAME
    value: "platform-connector"  # or "fault-quarantine", "node-drainer", etc.
```

#### Alloy Gateway Configuration

The Alloy gateway needs to be configured to forward traces to Panoptes. This is typically managed by the observability team. The configuration should include:

1. **OTLP Receiver**: Configured to receive traces on port 4317 (gRPC); NVSentinel modules send traces via OTLP gRPC to this port
2. **Traces Processor**: Batch processing for traces
3. **Traces Exporter**: Forward traces to Panoptes traces endpoint:
   - Endpoint: `https://otel-traces.us-east-1.aws.telemetry.dgxc.ngc.nvidia.com`
   - Authentication: OAuth2 (using `panoptes` secret in `dgxc-alloy` namespace)

**Example Alloy Config Addition** (managed by observability team):

NVSentinel modules send traces via OTLP gRPC; the gateway must expose the gRPC receiver on port 4317.

```alloy
otelcol.receiver.otlp "input" {
  grpc {
    endpoint = "0.0.0.0:4317"  # NVSentinel modules send traces here (OTLP gRPC)
  }
  http {
    endpoint = "0.0.0.0:4318"
  }
  output {
    logs = [otelcol.exporter.otlphttp.panoptes_logs.input]
    metrics = [otelcol.exporter.otlphttp.panoptes_metrics.input]
    traces = [otelcol.processor.batch.traces.input]
  }
}

// ADD THIS
otelcol.processor.batch "traces" {
  timeout = "1s"
  send_batch_size = 512
  output {
    traces = [otelcol.exporter.otlphttp.panoptes_traces.input]  
  }
}

// ADD THIS
otelcol.auth.oauth2 "panoptes_traces" {
  client_id = convert.nonsensitive(remote.kubernetes.secret.panoptes.data["client_id"])
  client_secret = remote.kubernetes.secret.panoptes.data["client_secret"]
  token_url = convert.nonsensitive(remote.kubernetes.secret.panoptes.data["token_url"])
  scopes = ["api://dgxc-lgtm-otel-gateway-prod/.default"]
}

// ADD THIS
otelcol.exporter.otlphttp "panoptes_traces" {
  client {
    auth = otelcol.auth.oauth2.panoptes_traces.handler
    endpoint = "https://otel-traces.us-east-1.aws.telemetry.dgxc.ngc.nvidia.com"
  }
}
```

#### Viewing Traces

Once traces are flowing through Alloy to Panoptes, they can be viewed in Grafana:

- **Grafana URL**: `https://dashboards.telemetry.dgxc.ngc.nvidia.com`
- **Tempo Datasource**: Already configured
- **Trace Explorer**: Navigate to Explore → Select Tempo datasource → Search by trace ID or service name
