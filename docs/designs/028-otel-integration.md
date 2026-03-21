# OpenTelemetry Tracing for NVSentinel

## Goal

To debug health events in NVSentinel, operators must manually correlate logs across multiple modules (platform-connector, fault-quarantine, node-drainer, fault-remediation, health-events-analyzer) to understand an event's complete lifecycle. OpenTelemetry traces eliminate this manual work by providing end-to-end visibility of each health event's journey through all modules in a single unified trace, making debugging faster and system behavior more transparent.

## Overview: Trace, Span, and Span Attributes

- **Trace** — A trace represents one request or workflow (e.g. one health event) as it moves through the system. It has a unique **trace ID** and is the top-level container for all related work. In NVSentinel, a single health event from ingestion through quarantine, drain, and remediation forms one trace.

- **Span** — A span is a single unit of work within a trace (e.g. "process event", "create remediation CR", "run log collector"). Each span has a name, start and end time, and can have parent and child spans. Spans are nested: one trace contains many spans, and spans can contain child spans. Together they form a timeline and call graph for the event.

- **Span attributes** — Key-value metadata attached to a span (e.g. `node_drainer.node_name`, `fault_remediation.cr.name`, `janitor.gpureset.processing_status`). They describe what happened during that span without needing to search logs. Attributes are used for filtering and grouping in tracing UIs (e.g. Grafana Tempo) and for understanding why a span succeeded or failed.

- **Span links** — A span link connects spans that are causally related but not in a strict parent-child relationship. Links are the OpenTelemetry-standard way to model asynchronous fan-out/fan-in and queue/change-stream handoffs where work may run in parallel or outlive the trigger span.

## How Can Traces Be Helpful?
- **Module-level performance**: How long does fault-quarantine take vs node-drainer vs fault-remediation?
- **Database query latency**: Time spent in MongoDB aggregation pipelines (health-events-analyzer)
- **Granular level performance**: Time spent in cordon, taint, drain, and CR creation operations
- **Event lifecycle tracking**: From detection → ingestion → quarantine → drain → remediation
- **Remediation status**: Was the last remediation succeeded or failed? 
- **Multi-module coordination**: See which modules are actively processing the same event like fault-quarantine and health-events-analyzer pick up event at the same time
- **Context preservation**: All relevant event metadata is attached to spans, eliminating the need to login to the cluster and to search logs in each module
- **Concurrent processing**: Understand how multiple events are processed simultaneously
- **Circuit breaker activity**: Monitor when circuit breaker is tripped

## How Traces Are Different From Logs and Metrics?

Logs are discrete, unordered (or time-ordered) messages from each service. To follow one health event across platform-connector, fault-quarantine, node-drainer, fault-remediation, and health-events-analyzer, you must search by event ID or node name, align timestamps, and mentally reconstruct the flow.
Traces are structured around a single request or workflow: one trace ID ties together all spans from all modules for that event, with explicit relationships (parent-child for synchronous calls and links for asynchronous handoffs).

- **End-to-end view of one event** — One trace shows the full path of one health event (ingestion → quarantine → drain → remediation). With logs you must correlate multiple services by hand.
- **Timing and bottlenecks** — Spans have start/end times and nesting, so you see exactly where time was spent (e.g. DB query execution in health-events-analyzer vs draining operation in node-drainer). Logs give timestamps but not a single timeline or hierarchy.
- **Structured context without log parsing** — Span attributes (e.g. `node_drainer.node_name`, `node_drainer.drain.scope`, `janitor.gpureset.processing_status`) are queryable and filterable in the trace UI. No need to grep or parse log lines.
- **Cross-module flow** — Traces show which modules touched the same event and in what order. Logs are per-service; correlating across modules is manual.
- **Failure diagnosis** — A failed span is visible in the trace with status and attributes; you see the failing step and its parent path. With logs you must infer causality from messages and timestamps.
- **Performance and SLOs** — Trace-based latency percentiles and service maps are built-in. With logs you'd need custom metrics or log-based metrics to get the same view.

Logs are useful for detailed, free-form messages (e.g. stack traces, debug dumps). Traces complement them by giving a structured, request-scoped view of *where*, *what* and *how long* work happened across the breakfix pipeline.
Traces are not to replace logs — we are adding it as an additional feature alongside existing logging to improve debugging and analyze system performance.

Metrics (e.g. Prometheus) are aggregated over time: counters, gauges, and histograms that answer "how much?" and "how fast on average?" (e.g. `fault_quarantine_event_handling_duration_seconds`, event counts per module).
Traces are per-request: each health event gets one trace with spans across all modules, answering "what happened for this event?", "where did this event spend most time?" and "where this event handling failed and why?"

- **Granularity:** Metrics are aggregated over time (e.g. p50/p99 latency, rate per minute). Traces are per event: one trace per health event.
- **Question answered:** Metrics answer "How is the system behaving overall?" Traces answer "Why was this event slow or failed? Why was this node not remediated? "
- **Use case:** Metrics support dashboards, alerting, SLOs, and capacity planning. Traces support debugging a specific event and finding bottlenecks in a single flow.

## Architecture Diagram

The following diagram shows how traces flow from NVSentinel modules to a OpenTelemetry Collector. 
```
┌──────────────────────────────────────────────────────────────────────────────┐
│                         NVSentinel Namespace                                 │
├──────────────────────────────────────────────────────────────────────────────┤
│  platform-connector   fault-quarantine   node-drainer   fault-remediation    │
│  health-events-analyzer   janitor   janitor-provider   event-exporter        │
│         │                    │                │                │              │
│         │  OTLP (gRPC)       │                │                │              │
│         └────────────────────┴────────────────┴────────────────┘              │
│                                      │                                       │
└──────────────────────────────────────┼───────────────────────────────────────┘
                                       │
                                       │  global.tracing.endpoint
                                       │  (internal or external collector)
                                       ▼
              ┌───────────────────────────────────────────────────────────────┐
              │  OTel Collector (dgxc-alloy, Grafana alloy, OTEL collector etc.) │
              │  - Receives OTLP from NVSentinel modules                      │
              │  - Batches and forwards to backend (Tempo, Panoptes, etc.)     │
              └──────────────────────────────────┬────────────────────────────┘
                                                 │
                                  ┌──────────────┼──────────────┐
                                  ▼              ▼              ▼
                         ┌──────────────┐ ┌───────────┐ ┌─────────────────----┐
                         │  Jaeger      │ │  Tempo    │ │  Any Backend service│
                         └──────────────┘ └───────────┘ └─────────────────----┘
                                  │
                                  ▼
                         ┌──────────────────────────────────────────────────────┐
                         │  Grafana Dashboards                                   │
                         │  - Trace explorer, service map, latency analysis      │
                         └──────────────────────────────────────────────────────┘
```

**Trace flow summary:**

1. **Ingestion**: The OTel Collector (e.g. dgxc-alloy, Grafana Alloy, or the OpenTelemetry Collector) run separately. To connect NVSentinel to it, set `global.tracing.endpoint` to the collector's OTLP gRPC address. Helm injects this value into each module as `OTEL_EXPORTER_OTLP_ENDPOINT`, so every module exports spans via OTLP over gRPC to that collector. All NVSentinel traces therefore flow to the same collector for batching and forwarding.
2. **Collector**: The OTEL collector receives traces from NVSentinel modules, batches them, and forwards them to the chosen backend.
3. **Backend**: The backend (Tempo, Panoptes, Grafana Cloud, Jaeger, etc.) is where traces are exported and stored. The backend—or a UI connected to it (e.g. Grafana with a Tempo datasource, Jaeger UI)—is used to query and visualize traces.

**Integration with internal or external OTel Collector:**  
Updating `global.tracing.endpoint` is sufficient to integrate NVSentinel with any OTLP-capable collector. Point it at an in-cluster collector (e.g. `dgxc-alloy.observability.svc.cluster.local:4317`) or an external one (e.g. `otel-collector.nvsentinel.svc.local:4317`). No in-namespace collector is required.

## Span Naming and Attribute Conventions

All span names and span attributes are prefixed with the module name. This ensures every attribute is unambiguous in Grafana TraceQL queries (e.g. `{node_drainer.drain.scope="partial"}`) and prevents key collisions across modules.

### Span Types

Each module creates two categories of spans:

1. **Module root spans** — linked to the upstream service span via `StartSpanWithLinkFromTraceContext`. These are sibling root spans within the same trace, not children of the upstream span. This correctly models the async change-stream / queue boundaries between modules. Examples: `fault_quarantine.process_event`, `node_drainer.drain_session`, `fault_remediation.event_received`, `janitor.gpureset.reconcile`.

2. **Operation child spans** — created with `StartSpan(ctx, name)` as synchronous children of the module root span or another child span. Examples: `fault_quarantine.evaluate_rulesets`, `node_drainer.db.update_status`, `fault_remediation.log_collector`.

## What Will be Tracked from Each Module

### Fault-Quarantine

**Root span:** `fault_quarantine.process_event`

| What is tracked | Span / Attribute | Use case |
|-----------------|-----------------|-------------------|
| Cordon + taint applied | `fault_quarantine.apply_quarantine` child span; `fault_quarantine.action.cordon` (bool), `fault_quarantine.action.taint` (bool), `fault_quarantine.event.processing_status = "Quarantined"` | Did this health event lead to cordoning and tainting of the node? |
| Unquarantine | `fault_quarantine.perform_uncordon` child span; `fault_quarantine.event.processing_status = "UnQuarantined"` | Did this health event lead to uncordoning/untainting of the node? |
| Ruleset evaluation | `fault_quarantine.evaluate_rulesets` child span | Which rulesets were evaluated for this event? How long did evaluation take? |
| DB status write trigger span (used as link target by node-drainer) | `fault_quarantine.db.update_status`; `span_ids.fault_quarantine` written to MongoDB | When did fault-quarantine persist status so node-drainer could pick up this event? |
| Cancellation of quarantining events on manual uncordon/untaint | `fault_quarantine.cancel_latest_quarantining_events` root span (linked to `fault_quarantine.db.update_status` of the cancelled event) | Were quarantining events cancelled for this event due to manual uncordon/untaint? |
| Errors | `fault_quarantine.error.type`, `fault_quarantine.error.message` | What went wrong in fault-quarantine and why? |

### Node Drainer

**Root spans:** `node_drainer.enqueue_event` (change-stream entry) + `node_drainer.drain_session` (worker queue, long-lived for entire drain)

| What is tracked | Span / Attribute | Use case |
|-----------------|-----------------|-------------------|
| Initial status set to InProgress in MongoDB | `node_drainer.set_initial_status_and_enqueue` → `node_drainer.db.update_status` child span; `node_drainer.eviction_status = "InProgress"` | Did drain start for this event? When was status set to InProgress? |
| Final eviction status written to MongoDB | `node_drainer.update_user_pods_eviction_status` child span; `drain.status` = "Succeeded", "Failed", "Cancelled", "AlreadyDrained" | Did drain succeed, fail, get cancelled, or was the node already drained? |
| Drain scope (partial vs full) | `node_drainer.drain.scope` = "partial" or "full"; `node_drainer.partial_drain.entity_type`, `node_drainer.partial_drain.entity_value` — set on `drain_session` at session end | Was this a partial or full drain? What was the partial-drain target (entity type/value)? |
| Pods targeted at phase entry | `node_drainer.immediate_eviction_pods`, `node_drainer.allow_completion_pods`, `node_drainer.delete_after_timeout_pods` — set on `drain_session` at session end | Which pods were in each phase (immediate eviction, allow completion, delete after timeout)? |
| Phase durations (wall-clock) | `node_drainer.immediate_eviction_duration_s`, `node_drainer.allow_completion_duration_s`, `node_drainer.delete_after_timeout_duration_s` — set on `drain_session` at session end | How long did each drain phase take? |
| Force-deleted pods | `node_drainer.force_deleted_pods` (comma-separated), `node_drainer.pods_force_deleted_count` — set on `drain_session` | Which pods were force-deleted and how many? |
| Custom drain CR (Slinky) | `node_drainer.execute_custom_drain` child span; `node_drainer.custom_cr.name`, `node_drainer.custom_cr.created` (bool), `node_drainer.custom_cr.status` = "in_progress"/"completed"/"error", `node_drainer.custom_cr.deleted` (bool on cleanup) | Was a custom drain CR (e.g. Slinky) used? What was its status? |
| Errors | `node_drainer.error.type`, `node_drainer.error.message` | What went wrong in node-drainer and why? |

### Fault-Remediation

**Root span:** `fault_remediation.event_received` (session span, open for the duration of the event's remediation lifecycle)

| What is tracked | Span / Attribute | Use case |
|-----------------|-----------------|-------------------|
| Per-reconcile work | `fault_remediation.reconcile` child span | What did each reconcile cycle do for this event? |
| Log collector job | `fault_remediation.log_collector` child span; `fault_remediation.log_collector.node`, `fault_remediation.log_collector.event_id`, `fault_remediation.log_collector.job_name`, `fault_remediation.log_collector.outcome` = "success"/"failure"/"timeout", `fault_remediation.log_collector.duration_s` | Did the log collector run for this event? Did it succeed, fail, or timeout? How long did it take? |
| Maintenance CR creation | `fault_remediation.remediation_cr_created` child span; `fault_remediation.cr.name` | Was a remediation CR (e.g. GPUReset, RebootNode) created for this event? Which one? |
| Skip reasons | `fault_remediation.skip_event` child span; `fault_remediation.skip.reason` | Why was this event skipped (e.g. not ready, already remediating)? |
| Cancellation | `fault_remediation.cancellation_event` child span | Was remediation cancelled for this event? |
| Final remediation outcome | `fault_remediation.remediation_finished` span event on the reconcile span | Did remediation finish successfully or fail for this event? |
| Status update | `fault_remediation.remediation_status_updated` child span | When was remediation status last updated? |
| Errors | `fault_remediation.error.type`, `fault_remediation.error.message` | What went wrong in fault-remediation and why? |

### Health-Events-Analyzer

**Root span:** `analyzer.process_event` (linked to `platform_connector` span via `span_ids`)

| What is tracked | Span / Attribute | Use case |
|-----------------|-----------------|-------------------|
| Event processing (root) | `analyzer.process_event` root span (linked to platform_connector); `analyzer.event.node_name`, `analyzer.event.component_class`, `analyzer.event.agent`, `analyzer.event.is_healthy`, `analyzer.event.published` (bool), `analyzer.event.processing_duration_ms` (float) | Full lifecycle of one health event through the analyzer. How long did processing take? Was a new event published? |
| Event handling | `analyzer.handle_event` child span; `analyzer.rules.evaluated` (int), `analyzer.rules.matched` (int), `analyzer.rules.skipped` (int), `analyzer.event.published` (bool) | How many rules were evaluated, matched, and skipped for this event? |
| Rule evaluation | `analyzer.evaluate_rule` child span; `analyzer.rule.name`, `analyzer.rule.recommended_action`, `analyzer.rule.stages_count` (int), `analyzer.rule.matched` (bool), `analyzer.rule.evaluation_duration_ms` (float) | Which rule was evaluated? Did it match? How long did evaluation take? |
| Aggregation pipeline (MongoDB) | `analyzer.mongo.aggregate` child span; `analyzer.mongo.rule_name`, `analyzer.mongo.pipeline.stages` (int), `analyzer.mongo.pipeline.duration_ms` (float), `analyzer.mongo.pipeline.documents_matched` (int) | How many pipeline stages ran? How long did the pipeline take? How many documents matched? |
| Event publication (matched rule) | `analyzer.publish_matched_event` child span; `analyzer.event.rule_name`, `analyzer.event.recommended_action`, `analyzer.event.node_name`, `analyzer.event.published` (bool) | Was a matched event published? Which rule triggered it? |
| Publisher (gRPC) | `analyzer.publish` child span; `analyzer.publish.rule_name`, `analyzer.publish.recommended_action`, `analyzer.publish.node_name`, `analyzer.publish.is_fatal` (bool), `analyzer.publish.processing_strategy`, `analyzer.publish.success` (bool) | What was published to platform-connector? Was it fatal? |
| gRPC call with retry | `analyzer.grpc.publish` child span; `analyzer.grpc.retry_count` (int), `analyzer.grpc.duration_ms` (float), `analyzer.grpc.status` = "success"/"failure" | How many retries were needed for the gRPC call? What was the latency? |
| XID detector handling | `analyzer.xid.handle` child span; `analyzer.xid.node`, `analyzer.xid.component_class`, `analyzer.xid.is_healthy`, `analyzer.xid.history_cleared` (bool), `analyzer.xid.burst_detected` (bool) | Was XID history cleared? Was a burst detected? |
| XID burst detection | `analyzer.xid.burst_detection` child span; `analyzer.xid.node`, `analyzer.xid.error_code`, `analyzer.xid.burst_detected` (bool), `analyzer.xid.burst_count` (int), `analyzer.event.published` (bool), `analyzer.event.published_rule` | Was an XID burst detected? How many bursts? Which XID code? |
| Errors | `analyzer.error.type`, `analyzer.error.message` on relevant spans | What went wrong in the analyzer and why? |

### Platform-Connector

| What is tracked | Span Attributes | Use case |
|-----------------|----------------|-------------------|
| gRPC event reception | `platform_connector.grpc.event_received` (bool), `platform_connector.grpc.events_count` (int), `platform_connector.grpc.duration_ms` (float) | Was a health event received via gRPC? How many? How long did the call take? |
| MongoDB insert (writes `span_ids.platform_connector` and `trace_id` into `healthevent.metadata`) | `platform_connector.db.operation = "insert"`, `platform_connector.db.duration_ms` (float) | Was the event written to MongoDB? How long did the insert take? |
| Node condition updates | `platform_connector.k8s.node_condition.updated` (bool) | Were node conditions updated in Kubernetes for this event? |
| Errors | `platform_connector.error.type`, `platform_connector.error.message` | What went wrong in platform-connector and why? |

### Event Exporter

The `trace_id` stored in `healthevent.metadata` by platform-connector is automatically included in the exported CloudEvent because the CloudEvents transformer copies all health event metadata into the output. This allows linking an exported event in Kibana/Kratos back to the MongoDB document and its distributed trace — search by `metadata.trace_id` in Kibana to find the corresponding trace in Grafana/Tempo.

| What is tracked | Span Attributes | Use case |
|-----------------|----------------|-------------------|
| CloudEvents transform | `event_exporter.transform.success` (bool), `event_exporter.transform.duration_ms` (float), `event_exporter.transform.error` (string) | Did the CloudEvents transform succeed? How long did it take? Any error? |
| Publish to sink | `event_exporter.publish.status`, `event_exporter.publish.retry_count` (int), `event_exporter.publish.duration_ms` (float), `event_exporter.publish.error_type` | Did publish to the sink succeed? How many retries? How long did it take? |
| Backfill | `event_exporter.backfill.in_progress` (bool), `event_exporter.backfill.events_processed` (int), `event_exporter.backfill.duration_ms` (float) | Was backfill running? How many events were processed? How long did it take? |
| Errors | `event_exporter.error.type`, `event_exporter.error.message` | What went wrong in event-exporter and why? |

### Janitor (GPUReset Controller)

**Root span:** `janitor.gpureset.reconcile` (one per controller-runtime reconcile cycle, linked to `fault_remediation.reconcile` via CR annotation)

| What is tracked | Span Attributes | Use case |
|-----------------|----------------|-------------------|
| Reconcile phase and condition | `janitor.gpureset.name`, `janitor.gpureset.phase`, `janitor.gpureset.node`, `janitor.gpureset.condition`, `janitor.gpureset.reason` | What phase and condition is this GPUReset in? Which node? |
| GPU targets (UUIDs and PCI bus IDs) | `janitor.gpureset.gpu_uuids` (comma-separated), `janitor.gpureset.pci_bus_ids` (comma-separated) | Which GPU(s) are being reset (UUIDs and PCI bus IDs)? |
| Node lock | `janitor.node_lock.acquired` (bool), `janitor.node_lock.node` | Was the node lock acquired? Which node? |
| Service teardown / restore | `janitor.services.teardown.success` (bool), `janitor.services.restore.success` (bool) | Did service teardown and restore succeed? |
| Reset job | `janitor.reset_job.created` (bool), `janitor.reset_job.name`, `janitor.reset_job.completed` (bool), `janitor.reset_job.failed` (bool) | Was a reset job created? Did it complete or fail? |
| Final outcome + duration | `janitor.gpureset.processing_status` = "succeeded"/"failed", `janitor.gpureset.completion_time`, `janitor.gpureset.failure_reason`, `janitor.gpureset.duration_seconds` | Did GPU reset succeed or fail? How long did it take? What was the failure reason? |
| Errors | `janitor.error.type`, `janitor.error.message` | What went wrong in janitor (GPUReset) and why? |

### Janitor (RebootNode Controller)

**Root span:** `janitor.rebootnode.reconcile` (one per reconcile cycle, linked to `fault_remediation.reconcile` via CR annotation)

| What is tracked | Span Attributes | Use case |
|-----------------|----------------|-------------------|
| CR identity | `janitor.rebootnode.name`, `janitor.rebootnode.node` | Which RebootNode CR and node is this? |
| Reboot signal sent | `janitor.rebootnode.signal_sent` (bool), `janitor.rebootnode.request_ref` (CSP request ID) | Was a reboot signal sent to the CSP? What is the request ID? |
| Node ready outcome | `janitor.rebootnode.node_ready` (bool), `janitor.rebootnode.reason` = "Succeeded"/"Timeout"/"Failed" | Did the node become ready after reboot? Succeeded, timeout, or failed? |
| Time to node ready | `janitor.rebootnode.time_to_ready_seconds` (from CR creation to node ready — only set on success) | How long from CR creation to node ready? |
| Total duration | `janitor.rebootnode.duration_seconds` (from StartTime to completion) | How long did the full reboot flow take? |
| Final status | `janitor.rebootnode.status` = "succeeded"/"failed" | Did reboot succeed or fail? |
| Errors | `janitor.error.type`, `janitor.error.message` | What went wrong in janitor (RebootNode) and why? |

### Janitor (TerminateNode Controller)

**Root span:** `janitor.terminatenode.reconcile` (one per reconcile cycle, linked to `fault_remediation.reconcile` via CR annotation)

| What is tracked | Span Attributes | Use case |
|-----------------|----------------|-------------------|
| CR identity | `janitor.terminatenode.name`, `janitor.terminatenode.node` | Which TerminateNode CR and node is this? |
| Terminate signal sent | `janitor.terminatenode.signal_sent` (bool) | Was a terminate signal sent to the CSP? |
| Node terminated | `janitor.terminatenode.node_terminated` (bool), `janitor.terminatenode.node_deleted` (bool) | Was the node terminated and deleted from the cluster? |
| Total duration | `janitor.terminatenode.duration_seconds` (from CR creation to completion) | How long did termination take? |
| Final status | `janitor.terminatenode.status` = "succeeded"/"failed" | Did node termination succeed or fail? |
| Errors | `janitor.error.type`, `janitor.error.message` | What went wrong in janitor (TerminateNode) and why? |

### Janitor-Provider (CSP gRPC)

**Spans:** `janitor_provider.SendRebootSignal`, `janitor_provider.IsNodeReady`, `janitor_provider.SendTerminateSignal` — created as gRPC server handler spans; trace context is automatically propagated from janitor via `otelgrpc` W3C traceparent headers, so these appear as children of the janitor reconcile span in Grafana.

| What is tracked | Span Attributes | Use case |
|-----------------|----------------|-------------------|
| Reboot signal | `janitor_provider.reboot.sent` (bool), `janitor_provider.reboot.node`, `janitor_provider.reboot.request_ref`, `janitor_provider.reboot.duration_ms` | Was a reboot request sent to the CSP? Which node? How long did the call take? |
| Node ready check | `janitor_provider.node_ready.ready` (bool), `janitor_provider.reboot.node`, `janitor_provider.reboot.request_ref` | Did the CSP report the node as ready? For which reboot request? |
| Terminate signal | `janitor_provider.terminate.sent` (bool), `janitor_provider.terminate.node`, `janitor_provider.terminate.request_ref` | Was a terminate request sent to the CSP? Which node? |
| Errors | `janitor_provider.error.type` = "grpc_error"/"csp_api_error", `janitor_provider.error.message` | Did the failure come from gRPC or the CSP API? What was the error? |

## Trace Context Propagation

### Why Do We Need Context Propagation?

Without trace context propagation, each service would create its own trace with a different trace ID. You would see multiple unrelated traces (one per module) and would have to correlate logs, timestamps, and event IDs by hand to understand the full lifecycle of a health event. That makes it hard to see where time was spent, where failures occurred, or why an event was slow.

With context propagation, the trace ID is carried with the event (e.g. in metadata) from one module to the next. Every module that handles the same event continues the same trace and adds its own spans. The result is a single trace that shows the entire journey of the event—from ingestion through quarantine, drain, and remediation—so you can see the full timeline, spot bottlenecks, and debug failures in one place.

### Span Links for Asynchronous Boundaries

OpenTelemetry defines span links as the preferred way to model related work that does not have a strict parent-child nesting, especially for fan-out and asynchronous pipelines. See:

- [Creating links between traces (OpenTelemetry)](https://opentelemetry.io/docs/languages/dotnet/traces/links-creation/)

In NVSentinel, we use links for:

- **Async fan-out inside platform-connectors**: one ingestion operation fans out into store and Kubernetes processing paths that run independently.
- **Cross-module asynchronous handoffs**: downstream modules consume events from MongoDB change streams and should be causally related to the upstream module's trigger span, without forcing synchronous nesting.

Why we use links in this system:

- **Correct async semantics**: avoids modeling independent async work as if it were a blocking child call.
- **More accurate module timing**: module root spans represent their real processing duration.
- **Prevents misleading trace math artifacts**: avoids negative/self-time distortions that can happen when async child spans outlive parent spans.

### Cross-Service Trace Continuity via `trace_id` in MongoDB

Platform-connector creates the root trace when it receives a health event via gRPC and writes `trace_id` into the health event's `metadata` map (`healthevent.metadata.trace_id`). This single location is used by all downstream modules for trace continuity and also flows through to the event-exporter CloudEvent output (since the transformer copies all health event metadata).

```
Health Monitor (sends event, NO trace context)
    │
    │ [1. gRPC Call - just health event data]
    ▼
Platform Connector (creates ROOT trace, trace_id: abc123)
    │
    │ [2. MongoDB Storage - trace_id written into healthevent.metadata,
    │     span_ids stored as top-level field]
    ▼
MongoDB (stores event with trace_id in healthevent.metadata and span_ids)
    │
    │ [3. MongoDB Change Streams - trace_id extracted from healthevent.metadata]
    ├──► Fault Quarantine (links to platform_connector span, writes fault_quarantine span ID)
    ├──► Node Drainer (links to fault_quarantine span, writes node_drainer span ID)
    ├──► Fault Remediation (links to node_drainer span)
    ├──► Health Events Analyzer (continues trace abc123)
    └──► Event Exporter (continues trace abc123, trace_id included in exported CloudEvent via metadata)
```

**All modules share the same trace ID (`abc123`)** — this is only possible with context propagation at each step.

MongoDB document structure:

```json
{
  "_id": "...",
  "span_ids": {
    "platform_connector": "<span-id-of-platform_connector.db.insert>",
    "fault_quarantine":   "<span-id-of-fault_quarantine.db.update_status>",
    "node_drainer":       "<span-id-of-node_drainer active span>"
  },
  "createdAt": "...",
  "healthevent": {
    "metadata": {
      "trace_id": "abc123"
    },
    ...
  },
  "healtheventstatus": { ... }
}
```

The `HealthEventWithStatus` struct carries the trace context fields:

```go
type HealthEventWithStatus struct {
    SpanIDs           map[string]string `bson:"span_ids,omitempty" json:"span_ids,omitempty"`
    CreatedAt         time.Time         `bson:"createdAt"`
    HealthEvent       *protos.HealthEvent `bson:"healthevent,omitempty"`
    HealthEventStatus HealthEventStatus   `bson:"healtheventstatus"`
}
```

The `trace_id` is stored in `healthevent.metadata["trace_id"]` — inside the health event proto's metadata map. All downstream modules extract it using `tracing.TraceIDFromMetadata(healthEvent.GetMetadata())`. The CloudEvents transformer in event-exporter copies all health event metadata into the exported event, so the `trace_id` is automatically included in the Kibana/Kratos event, allowing operators to link an exported event back to the MongoDB document and its distributed trace.

**How platform-connector stores the trace_id:**

```go
clonedHealthEvent := proto.Clone(healthEvent).(*protos.HealthEvent)

if clonedHealthEvent.Metadata == nil {
    clonedHealthEvent.Metadata = make(map[string]string)
}
clonedHealthEvent.Metadata[tracing.MetadataKeyTraceID] = traceID

healthEventWithStatusObj := model.HealthEventWithStatus{
    SpanIDs: map[string]string{
        tracing.ServicePlatformConnector: tracing.SpanIDFromSpan(dbSpan),
    },
    CreatedAt:   time.Now().UTC(),
    HealthEvent: clonedHealthEvent,
    ...
}
```

### Trace Context via `span_ids` Map in MongoDB

Each module that writes to the MongoDB health event document also writes its own span ID into the `span_ids` map. This allows the next module in the pipeline to pick up the exact span to link against, creating an auditable causal chain without requiring synchronous gRPC calls between modules.


**How each module reads the trace_id and upstream span ID:**

```go
// All modules extract trace_id from the health event metadata using the shared helper:
traceID := tracing.TraceIDFromMetadata(healthEventWithStatus.HealthEvent.GetMetadata())

// fault-quarantine reads trace_id and platform_connector's span ID
parentSpanID := tracing.ParentSpanID(healthEventWithStatus.SpanIDs, tracing.ServicePlatformConnector)
ctx, span := tracing.StartSpanWithLinkFromTraceContext(ctx, traceID,
    parentSpanID, "fault_quarantine.process_event")

// node-drainer reads trace_id and fault_quarantine's span ID
parentSpanID := tracing.ParentSpanID(healthEventWithStatus.SpanIDs, tracing.ServiceFaultQuarantine)
ctx, span := tracing.StartSpanWithLinkFromTraceContext(ctx, traceID,
    parentSpanID, "node_drainer.enqueue_event")

// fault-remediation reads trace_id and node_drainer's span ID
parentSpanID := tracing.ParentSpanID(healthEventWithStatus.SpanIDs, tracing.ServiceNodeDrainer)
sessionCtx, session := r.startOrReuseEventSession(ctx, traceID, parentSpanID, ...)

// event-exporter — trace_id flows automatically via healthevent.metadata["trace_id"]
// The CloudEvents transformer copies all health event metadata into the exported event:
//   healthEventData["metadata"] = event.Metadata
// So the exported CloudEvent in Kibana/Kratos will contain metadata.trace_id,
// which can be used to look up the full distributed trace in Grafana/Tempo.
```

Service name constants are defined in `commons/pkg/tracing/tracing.go`:

```go
const (
    ServicePlatformConnector    = "platform_connector"
    ServiceFaultQuarantine      = "fault_quarantine"
    ServiceNodeDrainer          = "node_drainer"
    ServiceFaultRemediation     = "fault_remediation"
    ServiceHealthEventsAnalyzer = "health_events_analyzer"
    ServiceEventExporter        = "event_exporter"
)
```

### Trace Context in Janitor CR Annotations

Janitor cannot read MongoDB — it processes Kubernetes custom resources (`GPUReset`, `RebootNode`, `TerminateNode`). To carry trace context to janitor, fault-remediation writes the trace ID and its own span ID into the CR's annotations at creation time.

**Annotations written by fault-remediation** (via CR templates):

```yaml
metadata:
  annotations:
    nvsentinel.nvidia.com/trace-id: "{{ .TraceID }}"
    nvsentinel.nvidia.com/span-id:  "{{ .SpanID }}"
```

The `SpanID` written is the ID of the `fault_remediation.reconcile` span — the span that directly triggered the CR creation. This is the most precise causal reference: "janitor is processing what this fault-remediation reconcile requested."

CR templates with these annotations:
- `fault-remediation/pkg/reconciler/templates/rebootnode-template.yaml`
- `fault-remediation/pkg/reconciler/templates/gpureset-template.yaml`

> **Note**: `TerminateNode` CR templates do not currently embed trace/span annotations. Janitor's TerminateNode controller reads the annotations but will not have a valid span ID to link against.

**How janitor reads the annotations:**

```go
annotations := cr.GetAnnotations()
traceID := annotations["nvsentinel.nvidia.com/trace-id"]
spanID  := annotations["nvsentinel.nvidia.com/span-id"]
ctx, span := tracing.StartSpanWithLinkFromTraceContext(ctx, traceID, spanID, "janitor.rebootnode.reconcile")
```

**janitor-provider trace context** is automatically propagated from janitor via `otelgrpc` W3C traceparent headers injected on every gRPC call (janitor uses `grpc.WithStatsHandler(otelgrpc.NewClientHandler())`, janitor-provider uses `grpc.StatsHandler(otelgrpc.NewServerHandler())`). The `janitor_provider.SendRebootSignal` and `janitor_provider.IsNodeReady` spans therefore appear as children of `janitor.rebootnode.reconcile` in Grafana automatically.

## Implementation Details

### Span Creation Strategy

**Where is the Trace ID Created?**
Platform-Connector creates the root trace when it receives a health event via gRPC:

- **Root Span**: Created when platform-connector receives a health event via gRPC
- **Span name**: `platform_connector.receive_event`
- **Trace ID generated here** in platform-connector
- **No trace context propagation needed from health monitor**: Health monitor just sends the gRPC call with the health event
- Platform-connector writes `trace_id` into `healthevent.metadata["trace_id"]` and stores the initial `span_ids` map as a top-level field in MongoDB

```
Health Monitor:
  1. Detect event
  2. Make gRPC call with:
     - Health event data (protobuf)

Platform Connector:
  1. Receive gRPC call
  2. Create NEW trace (trace_id: abc123)
  3. Create root span: "platform_connector.receive_event"
  4. Write trace_id into healthevent.metadata["trace_id"]
  5. Store event in MongoDB with span_ids as top-level field
```

### Trace Export

- **OTLP Exporter**: Each NVSentinel module exports spans via OTLP gRPC to the endpoint set in `global.tracing.endpoint`. That endpoint is in-cluster OTel Collector which will be dgxc-alloy in our case. 
- **Batching and retry**: The OTel Collector is responsible for batching, retry, and forwarding to the final backend (Tempo, Jaeger, etc.).

### Backend Integration

NVSentinel does not deploy an OpenTelemetry Collector. Tracing is integrated by configuring a single endpoint: `global.tracing.endpoint` which will be OTEL collector endpoint. All NVSentinel modules sends traces to the configured endpoint. The collector then batches and forwards traces to the chosen backend (Tempo, Jaeger, Panoptes, etc.).

#### Architecture

```
NVSentinel Modules (platform-connector, fault-quarantine, etc.)
    │
    │ [OTLP gRPC to global.tracing.endpoint]
    ▼
 OTel Collector
    │
    │ [collector forwards to backend service]
    ▼
Backend (Tempo / Panoptes / Grafana Cloud / any OTLP endpoint)
    │
    ▼
Dashboard
```

#### Configuration

**Helm Values**:

Tracing is configured via `global.tracing` in `values.yaml`. Only the endpoint is required when tracing is enabled; there is no NVSentinel-managed collector.

```yaml
global:
  tracing:
    enabled: true       # Enables tracing and injects OTEL_* env vars into modules
    endpoint: ""        # Required when enabled: OTLP gRPC address of otel collector
                       # Examples: "dgxc-alloy.observability.svc.cluster.local:4317"
                       #           "otel-collector.my-namespace.svc.cluster.local:4317"
    insecure: true      # Set to false if the collector endpoint uses TLS
```

**NVSentinel Module Environment Variables** (injected by Helm templates):

```yaml
env:
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "<global.tracing.endpoint>"   
  - name: OTEL_EXPORTER_OTLP_INSECURE
    value: "<global.tracing.insecure>"
  - name: OTEL_TRACES_ENABLED
    value: "true"
  - name: OTEL_SERVICE_NAME
    value: "platform-connector"
```

## Log-Trace Correlation (OTel Log Appender)

An **OTel Log Appender** (or **log bridge**) is a mechanism that automatically attaches `trace_id` and `span_id` from the active span context to every log line. Without it, logs and traces are separate signals: you can see a trace in Grafana Tempo or logs in Loki/Kibana, but you cannot jump from a trace to the corresponding logs (or vice versa) for the same health event. With log-trace correlation enabled, operators can click a trace and see all log lines for that request, or filter logs by `trace_id` to find the associated trace.

OpenTelemetry defines two standard workflows for achieving this; the choice depends on how logs are collected (direct OTLP export vs. stdout/file with a log shipper).

### Custom slog Handler wrapper

In this workflow, logs continue to be written to stdout/stderr as JSON. A **custom `slog.Handler`** wraps the existing handler and injects `trace_id` and `span_id` from the active span (via `context.Context`) into each log record. A log shipper (FluentBit, Grafana Alloy, Promtail, etc.) tails container logs, parses the JSON (including the new fields), and forwards them to Loki or another backend. Correlation works because both traces (Tempo) and logs (Loki) share the same `trace_id`/`span_id` fields.

**Example handler wrapper:**

```go
import (
    "context"
    "log/slog"
    "go.opentelemetry.io/otel/trace"
)

type traceHandler struct {
    inner slog.Handler
}

func (h *traceHandler) Handle(ctx context.Context, r slog.Record) error {
    span := trace.SpanFromContext(ctx)
    if span.SpanContext().IsValid() {
        r.AddAttrs(
            slog.String("trace_id", span.SpanContext().TraceID().String()),
            slog.String("span_id", span.SpanContext().SpanID().String()),
        )
    }
    return h.inner.Handle(ctx, r)
}

func (h *traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
    return &traceHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *traceHandler) WithGroup(name string) slog.Handler {
    return &traceHandler{inner: h.inner.WithGroup(name)}
}

func (h *traceHandler) Enabled(ctx context.Context, level slog.Level) bool {
    return h.inner.Enabled(ctx, level)
}
```

Integration with NVSentinel's logger (`commons/pkg/logger/logger.go`): wrap the existing `slog.NewJSONHandler(...)` with this handler when creating the structured logger, so that all JSON log lines include `trace_id` and `span_id` when an active span exists in the context.

**Example log output:**

```json
{
  "time": "2026-03-18T10:00:00Z",
  "level": "INFO",
  "msg": "processing event",
  "module": "fault-quarantine",
  "trace_id": "abc123def456...",
  "span_id": "789ghi012..."
}
```

**Pros:** Lightweight; no new OTLP log export; works with existing Kubernetes log collection (stdout → shipper → Loki).  
**Cons:** Callers must use context-aware logging methods (see below).

## References
Traces[https://opentelemetry.io/docs/concepts/signals/traces/#consumer]
OpenTelemetry[https://opentelemetry.io/docs/]
OpenTelemetry Collector[https://github.com/open-telemetry/opentelemetry-collector]
Alloy Collector[https://grafana.com/oss/alloy-opentelemetry-collector/]
Tracing Guide[https://vfunction.com/blog/opentelemetry-tracing-guide/]
OTEL Logging[https://opentelemetry.io/docs/specs/otel/logs/]