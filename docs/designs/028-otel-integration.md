# OpenTelemetry Tracing for NVSentinel

## Goal

To debug health events in NVSentinel, operators must manually correlate logs across multiple modules (platform-connector, fault-quarantine, node-drainer, fault-remediation, health-events-analyzer) to understand an event's complete lifecycle. OpenTelemetry traces eliminate this manual work by providing end-to-end visibility of each health event's journey through all modules in a single unified trace, making debugging faster and system behavior more transparent.

## How Can Traces Be Helpful?
- **Module-level performance**: How long does fault-quarantine take vs node-drainer vs fault-remediation?
- **Database query latency**: Time spent in MongoDB aggregation pipelines (health-events-analyzer)
- **Granular level performance**: Time spent in cordon, taint, drain, and CR creation operations
- **Event lifecycle tracking**: From detection → ingestion → quarantine → drain → remediation
- **Multi-module coordination**: See which modules are actively processing the same event like fault-quarantine and health-events-analyzer pick up event at the same time
- **Resource contention**: Detect when multiple modules are competing for the same resources (K8s API, MongoDB)
- **Context preservation**: All relevant event metadata is attached to spans, eliminating the need to search logs
- **Concurrent processing**: Understand how multiple events are processed simultaneously
- **Circuit breaker activity**: Monitor when circuit breakers are triggered

## What Are We Planning to Track from Each Module?

### Fault-Quarantine Actions to Track

| What to Track? | Why? | Span Attributes |
|----------------|------|----------------|
| **Cordon + Taint Operation** | Determine if a health event resulted in quarantining a node (cordon, taint, or both). Critical for understanding what actions were taken. | `quarantine.action.cordon` (bool)<br>`quarantine.action.taint` (bool)<br>`event.processing_status = "Quarantined"` |
| **Unquarantine Operation** | Determine if a health event resulted in unquarantining a node | `quarantine.action.uncordon` (bool)<br>`taints.removed` (bool)<br>`event.processing_status = "UnQuarantined"` |
| **Event Processing Status** | Track event lifecycle from reception to completion. Helps identify bottlenecks, queue delays, and processing states. | `event.processing_status = "waiting_to_be_processed" → "processing"` |
| **Processing Errors** | Track all errors that occur during event processing. Essential for debugging, identifying failure patterns, and understanding error rates. | `event.processing_status = "error"`<br>`error.type` = specific error type |
| **Circuit Breaker State** | Track when circuit breakers are triggered to prevent excessive quarantine operations. Critical for understanding system protection mechanisms. | `quarantine.circuit_breaker.state` = "open", "closed", "half_open"<br>`quarantine.circuit_breaker.triggered` (bool) |

### Node Drainer Actions to Track

| What to Track? | Why? | Span Attributes |
|----------------|------|----------------|
| **Drain Status** | Track final drain status (succeeded, failed, cancelled, already_drained). Essential for understanding outcomes. | `drain.status` = "succeeded", "failed", "cancelled", "already_drained", "in_progress" |
| **Timeout Eviction** | Track when pods are force-deleted after timeout. Critical for understanding timeout-based drain operations and stuck pods. | `drain.forceDeletedPods` = ["pod-0", "pod-1"]<br>`drain.timeout_eviction_count` (int) |
| **Partial vs Full Drain** | Track whether a partial drain (specific GPU) or full drain (entire node) was executed. Critical for understanding drain scope. | `drain.scope` = "partial" or "full"<br>`drain.partial_drain.entity_type` (string)<br>`drain.partial_drain.entity_value` (string) |
| **Processing Errors** | Track all errors that occur during drain processing. Essential for debugging and identifying failure patterns. | `drain.status` = "error" or "failed"<br>`error.type` = specific error type<br>`error.message` (string)<br>Span status: `codes.Error` |
| **Custom Drain CR Status** | Track custom drain CR creation and completion status. Critical for custom drain workflows. | `drain.custom_cr.name` (string)<br>`drain.custom_cr.status` (string)<br>`drain.custom_cr.created` (bool) |

### Fault-Remediation Actions to Track

| What to Track? | Why? | Span Attributes |
|----------------|------|----------------|
| **Remediation Action Type** | Determine which remediation action was executed (create CR, skip, cancellation, etc.). Critical for understanding what actions were taken. | `remediation.action.type` = "create_cr", "skip", "cancellation" |
| **Event Skip Reasons** | Track why events are skipped (NONE action, already remediated, unsupported action). Important for understanding skipped remediations. | `remediation.action.skip` (bool)<br>`remediation.skip_reason` = "none_action", "already_remediated", "unsupported_action" |
| **Existing CR Detection** | Track when remediation is skipped because a maintenance CR already exists. Important for understanding duplicate prevention. | `remediation.action.skip_existing_cr` (bool)<br>`remediation.existing_cr.name` (string)<br>`remediation.existing_cr.status` (string) |
| **Remediation Status** | Track final remediation status (succeeded, failed). Essential for understanding outcomes. | `remediation.status` = "succeeded", "failed", "in_progress" |
| **Processing Errors** | Track all errors that occur during remediation processing. Essential for debugging and identifying failure patterns. | `remediation.status` = "error" or "failed"<br>`error.type` = specific error type<br>`error.message` (string)<br>Span status: `codes.Error` |
| **CR Creation Details** | Track maintenance CR creation including template rendering and API calls. Helps debug CR creation failures. | `remediation.cr.name` (string)<br>`remediation.cr.kind` (string)<br>`remediation.cr.api_version` (string)<br>`remediation.cr.template_rendered` (bool) |

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
| **Node Condition Updates** | Track Kubernetes node condition updates. Important for understanding K8s API performance. | `platform_connector.k8s.node_condition.updated` (bool)<br>`platform_connector.k8s.node_condition.duration_ms` (float) |
| **Processing Errors** | Track errors during event ingestion and processing. Essential for debugging ingestion failures. | `platform_connector.error.type` = "grpc_error", "db_error", "k8s_error"<br>`platform_connector.error.message` (string)<br>Span status: `codes.Error` |

## Trace Context Propagation

### Why Do We Need Context Propagation?

Without trace context propagation, each service in NVSentinel would create **separate, disconnected traces**. This would make it impossible to see the complete journey of a health event through the system. Here's why it's critical:

#### The Problem Without Context Propagation

Imagine a health event flows through NVSentinel like this:

```
GPU Monitor → Platform Connector → MongoDB → Fault Quarantine → Node Drainer → Fault Remediation
```

**Without context propagation**, you'd see:
- **Trace 1** (GPU Monitor): "Detected GPU error on node-123"
- **Trace 2** (Platform Connector): "Received event, stored in DB" 
- **Trace 3** (Fault Quarantine): "Quarantined node-123"
- **Trace 4** (Node Drainer): "Drained node-123"
- **Trace 5** (Fault Remediation): "Created remediation CR"

These are **5 separate traces with different trace IDs**. To debug why an event took 5 minutes, you'd need to:
1. Manually correlate logs across 5 different services
2. Match timestamps and event IDs
3. Hope the event IDs are consistent across services
4. Manually piece together the timeline

**This is exactly the problem we're trying to solve!**

#### The Solution With Context Propagation

**With context propagation**, you'd see:
- **Single Trace** (trace_id: `abc123`):
  - Span 1 (GPU Monitor): "Detected GPU error on node-123"
  - Span 2 (Platform Connector): "Received event, stored in DB" (child of Span 1)
  - Span 3 (Fault Quarantine): "Quarantined node-123" (child of Span 2)
  - Span 4 (Node Drainer): "Drained node-123" (child of Span 3)
  - Span 5 (Fault Remediation): "Created remediation CR" (child of Span 4)

All spans share the **same trace ID** (`abc123`), so you can:
- View the **entire event lifecycle in one trace**
- See **exactly where time was spent** (which span is longest)
- **Instantly identify bottlenecks** without manual correlation
- **Debug failures** by following the trace from start to failure point

#### Real-World Example

**Scenario**: A health event takes 5 minutes to complete, but you don't know why.

**Without context propagation**:
```
1. Check GPU Monitor logs → "Event detected at 10:00:00"
2. Check Platform Connector logs → "Event received at 10:00:01" 
3. Check Fault Quarantine logs → "Event processed at 10:02:30" (2.5 min delay!)
4. Check Node Drainer logs → "Event processed at 10:03:00"
5. Check Fault Remediation logs → "Event processed at 10:05:00"
6. Manually calculate: 2.5 min delay between Platform Connector and Fault Quarantine
```

**With context propagation**:
```
1. Open trace viewer, search for event ID
2. See single trace showing:
   - Platform Connector: 1 second
   - [GAP: 2.5 minutes] ← Immediately visible!
   - Fault Quarantine: 30 seconds
   - Node Drainer: 2 minutes
   - Fault Remediation: 30 seconds
3. Click on the gap → See it's waiting in MongoDB change stream queue
```

### Cross-Service Trace Continuity

NVSentinel uses distributed tracing to maintain trace context across all modules. There are two approaches for storing trace context:

**Option A: Trace ID in HealthEvent.metadata**
```
Health Monitor (creates ROOT trace, trace_id: abc123)
    │
    │ [1. gRPC Call - trace context in HealthEvent.metadata field]
    ▼
Platform Connector (extracts from event.metadata, continues trace abc123)
    │
    │ [2. MongoDB Storage - trace context already in event.metadata]
    ▼
MongoDB (stores event with trace context in healthevent.metadata)
    │
    │ [3. MongoDB Change Streams - trace context extracted from healthevent.metadata]
    ├──► Fault Quarantine (continues trace abc123)
    ├──► Node Drainer (continues trace abc123)
    ├──► Fault Remediation (continues trace abc123)
    └──► Health Events Analyzer (continues trace abc123)
```

**Option B: Trace ID as Separate MongoDB Field (Recommended)**
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
    └──► Health Events Analyzer (continues trace abc123)
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

**Pros**:
- **Separates observability from business logic** - trace_id is not mixed with health event data
- **Consistent with current unmarshaling approach** - modules already unmarshal into `HealthEventWithStatus` struct
- **Easier to query and index** - trace_id as top-level field can be indexed separately
- **No health monitor changes needed** - health monitors don't need to be OpenTelemetry-aware
- **Cleaner data model** - observability data separate from business data

**Cons**:
- Requires struct changes to `HealthEventWithStatus`
- Platform-connector must generate trace ID (loses detection phase visibility)
- Trace starts at ingestion, not detection

**Recommendation**: Use **Option B** for cleaner separation of concerns and consistency with the current unmarshaling pattern. The trade-off of losing detection phase visibility is acceptable for the benefits of cleaner architecture.

   **How it works technically**:
   - Health monitor creates a trace and span when detecting an event
   - Health monitor **adds trace ID to `HealthEvent.metadata`**:
     ```go
     healthEvent.Metadata["trace_id"] = traceID
     ```
   - Health monitor sends gRPC call with `HealthEvents` protobuf (trace context is already in the event)
   - Platform-connector receives the event and **extracts trace context from `metadata` field**
   - Platform-connector continues the same trace (same trace ID) and creates a child span
   - When storing in MongoDB, **trace context is already in the event metadata** - automatically persisted with the event
   - Downstream modules extract trace context from MongoDB document's `healthevent.metadata` field
   
   **Why this approach**:
   - **Simpler implementation** - no gRPC interceptors needed
   - **Trace context travels with the event** - automatically stored in MongoDB with the event
   - **No separate storage step** - trace context is already in the event data
   - Leverages existing `metadata` field (no protobuf schema changes needed)
   - Since trace context needs to survive MongoDB storage anyway, storing it in metadata from the start is the most efficient approach

2. **MongoDB Change Streams (CRITICAL)**: This is the **bridge** between platform-connector and all fault handling modules. Trace context is stored in health event documents when platform-connector persists events, and extracted when fault handling modules process events from change streams.

   **How trace context is stored in MongoDB**:
   
   **Option A (metadata field)**:
   - Trace context is already in `HealthEvent.metadata` field (added by health monitor)
   - When platform-connector stores the event, trace context is automatically persisted in MongoDB as part of the event document
   - MongoDB document structure: `healthevent.metadata.trace_id`
   
   **Option B (separate field - Recommended)**:
   - Platform-connector generates trace ID and stores it as a separate top-level field
   - MongoDB document structure: `trace_id` (top-level field, separate from `healthevent`)
   - Document structure:
     ```json
     {
       "_id": "...",
       "trace_id": "abc123",
       "createdAt": "...",
       "healthevent": { ... },
       "healtheventstatus": { ... }
     }
     ```
   
   **How fault handling modules extract trace context**:
   
   **Option A (metadata field)**:
   - When processing events from MongoDB change streams, modules extract trace ID from the document:
     - Extract from `healthevent.metadata.trace_id`
     - Access via: `healthEvent.Metadata["trace_id"]`
   
   **Option B (separate field - Recommended)**:
   - Modules unmarshal change stream events into `HealthEventWithStatus` struct (existing pattern)
   - Trace ID is automatically extracted as part of the struct: `healthEventWithStatus.TraceID`
   - Access via: `healthEventWithStatus.TraceID`
   
   Modules then continue the same trace (same trace ID) and create child spans
   
   This ensures:
   - Events maintain their trace context even when stored in MongoDB
   - **Fault-quarantine, node-drainer, fault-remediation, and health-events-analyzer can continue the same trace** (without this, each would create a new trace with a different trace ID)
   - End-to-end visibility from event detection → ingestion → quarantine → drain → remediation
   - **Without this propagation, you'd lose trace continuity** - the fault handling modules would appear as disconnected traces, defeating the purpose of distributed tracing

3. **Span Relationships**: 
   - **Parent-child relationships**: Each module creates child spans under the parent event trace
   - **Follows-from relationships**: When health-events-analyzer creates a new event, it creates a "follows-from" relationship to the original event trace


#### How Context Propagation Works

Trace context contains:
- **Trace ID**: Unique identifier for the entire request/event flow (this is all we need to continue the trace)

This context is:
1. **Added to HealthEvent.metadata** field by health monitors when creating events (only trace_id is needed)
2. **Stored in MongoDB documents** automatically when events are persisted (trace ID is part of the event)
3. **Extracted from MongoDB** when downstream modules process events from change streams
4. **Manually propagated** by adding trace ID to the event metadata field

**Note**: Only the trace ID is needed because:
- OpenTelemetry automatically generates new span IDs for child spans
- Parent-child relationships are handled automatically when creating spans with the same trace ID
- Trace flags (sampling) are not critical for basic trace continuation

### Trace ID Correlation

All spans for a single health event share the same trace ID, allowing:
- **Unified view**: See all processing steps for an event in a single trace view
- **Event correlation**: Link related events (e.g., original event and analyzer-generated event) through trace relationships
- **Cross-module debugging**: Follow an event through the entire pipeline without manually correlating logs

## Implementation Details

### Span Creation Strategy

**Where is the Trace ID Created?**

The trace ID creation depends on which trace context propagation option you choose:

#### Option A: Health Monitor Creates Root Trace

**Health Monitor** creates the root trace when it detects a health event:
- **Root Span**: Created in health monitor (e.g., `gpu_health_monitor.detect_event`)
- **Trace ID generated here** in the health monitor
- Trace context is propagated via grpc connection to platform-connector
- **Advantages**:
  - Complete end-to-end visibility from detection → ingestion → processing
  - Can see detection latency (time between actual fault and detection)
  - Better for understanding monitor performance
- **Disadvantages**:
  - Requires tracing instrumentation in all health monitors
  - Health monitors need to be OpenTelemetry-aware

#### Option B: Platform-Connector Creates Root Trace (Recommended)

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
- **Disadvantages**:
  - Loses visibility into detection phase
  - Can't see monitor-side latency or issues
  - Trace starts at ingestion, not detection

**(Platform-Connector creates trace, stored as separate field)**:
```
Health Monitor:
  1. Detect event
  2. Make gRPC call with:
     - Health event data (protobuf)
     - NO trace context ← NO PROPAGATION NEEDED

Platform Connector:
  1. Receive gRPC call
  2. Create NEW trace (trace_id: abc123) ← GENERATED HERE
  3. Create root span: "platform_connector.receive_event"
  4. Store event in MongoDB with trace_id as separate top-level field
  5. Start new trace (trace_id: abc123)
```

2. **Module Spans**: Each module creates spans for its processing:
   - **Fault-Quarantine**: `fault_quarantine.process_event`
   - **Node-Drainer**: `node_drainer.process_event`
   - **Fault-Remediation**: `fault_remediation.process_event`
   - **Health-Events-Analyzer**: `health_events_analyzer.process_event`

3. **Operation Spans**: Within each module, create child spans for specific operations:
   - Database queries
   - Kubernetes API calls
   - Rule evaluations
   - CR creation


### Trace Export

- **OTLP Exporter**: Export traces via OTLP (OpenTelemetry Protocol) to Alloy gateway, which forwards to Panoptes (Tempo backend)
- **Batch Export**: Use batching to reduce overhead (handled by Alloy gateway)
- **Retry Logic**: Implement retry logic for failed exports (handled by Alloy gateway)
- **Fallback**: Console exporter for local development and debugging (when `OTEL_EXPORTER_OTLP_ENDPOINT` is not set)

### Backend Integration: Alloy Gateway + Panoptes

NVSentinel uses **Grafana Alloy Gateway** as the OTLP receiver, which forwards traces to **Panoptes** (NVIDIA's centralized observability platform). This provides centralized trace collection and visualization in Grafana.

#### Architecture

```
NVSentinel Modules (platform-connector, fault-quarantine, etc.)
    │
    │ [OTLP gRPC/HTTP]
    ▼
Alloy Gateway (dgxc-alloy-gateway.dgxc-alloy.svc.cluster.local)
    │
    │ [OTLP HTTP with OAuth2]
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
  # OTLP endpoint - Alloy gateway service
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: "dgxc-alloy-gateway.dgxc-alloy.svc.cluster.local:4317"  # gRPC endpoint
    # OR use HTTP endpoint:
    # value: "http://dgxc-alloy-gateway.dgxc-alloy.svc.cluster.local:4318"
  
  # Use insecure connection (ClusterIP service, no TLS)
  - name: OTEL_EXPORTER_OTLP_INSECURE
    value: "true"
  
  # Enable tracing
  - name: OTEL_TRACES_ENABLED
    value: "true"
  
  # Service name for trace identification
  - name: OTEL_SERVICE_NAME
    value: "platform-connector"  # or "fault-quarantine", "node-drainer", etc.
```

**Note**: The exact port (4317 for gRPC or 4318 for HTTP) depends on your cluster's Alloy gateway configuration. Check the service definition to confirm which protocol is available.

#### Alloy Gateway Configuration

The Alloy gateway needs to be configured to forward traces to Panoptes. This is typically managed by the observability team. The configuration should include:

1. **OTLP Receiver**: Already configured to receive traces on ports 4317 (gRPC) and/or 4318 (HTTP)
2. **Traces Processor**: Batch processing for traces
3. **Traces Exporter**: Forward traces to Panoptes traces endpoint:
   - Endpoint: `https://otel-traces.us-east-1.aws.telemetry.dgxc.ngc.nvidia.com`
   - Authentication: OAuth2 (using `panoptes` secret in `dgxc-alloy` namespace)

**Example Alloy Config Addition** (managed by observability team):

```alloy
otelcol.receiver.otlp "input" {
  grpc {
    endpoint = "0.0.0.0:4317"
  }
  http {
    endpoint = "0.0.0.0:4318"
  }
  output {
    logs = [otelcol.exporter.otlphttp.panoptes_logs.input]
    metrics = [otelcol.exporter.otlphttp.panoptes_metrics.input]
    traces = [otelcol.processor.batch.traces.input]  // ADD THIS
  }
}

otelcol.processor.batch "traces" {
  timeout = "1s"
  send_batch_size = 512
  output {
    traces = [otelcol.exporter.otlphttp.panoptes_traces.input]  // ADD THIS
  }
}

otelcol.auth.oauth2 "panoptes_traces" {
  client_id = convert.nonsensitive(remote.kubernetes.secret.panoptes.data["client_id"])
  client_secret = remote.kubernetes.secret.panoptes.data["client_secret"]
  token_url = convert.nonsensitive(remote.kubernetes.secret.panoptes.data["token_url"])
  scopes = ["api://dgxc-lgtm-otel-gateway-prod/.default"]
}

otelcol.exporter.otlphttp "panoptes_traces" {
  client {
    auth = otelcol.auth.oauth2.panoptes_traces.handler
    endpoint = "https://otel-traces.us-east-1.aws.telemetry.dgxc.ngc.nvidia.com"
  }
  sending_queue {
    enabled = true
    sizer = "bytes"
    num_consumers = 32
    queue_size = 3221225472
    batch {
      sizer = "bytes"
      flush_timeout = "1s"
      min_size = 1048576  // 1MB
      max_size = 2097152  // 2MB
    }
  }
}
```

#### Viewing Traces

Once traces are flowing through Alloy to Panoptes, they can be viewed in Grafana:

- **Grafana URL**: `https://dashboards.telemetry.dgxc.ngc.nvidia.com`
- **Tempo Datasource**: Already configured (UID: `ce6qyv88u86bkc`)
- **Trace Explorer**: Navigate to Explore → Select Tempo datasource → Search by trace ID or service name

#### Benefits of Alloy Gateway Approach

- **Centralized Collection**: All traces flow through a single gateway
- **No Direct Credentials**: NVSentinel modules don't need Panoptes OAuth2 credentials
- **Consistent with Existing Infrastructure**: Uses the same observability pipeline as logs and metrics
- **Automatic Retry and Batching**: Alloy handles retries and batching automatically
- **Unified Dashboard**: Traces appear in the same Grafana instance as logs and metrics

## Integration with Existing Observability

### Metrics Integration

Traces complement existing Prometheus metrics:
- **Metrics**: Provide aggregated statistics (e.g., average processing time)
- **Traces**: Provide detailed per-event breakdowns (e.g., why this specific event was slow)

### Logs Integration

Traces enhance log analysis:
- **Logs**: Provide detailed text descriptions of what happened
- **Traces**: Provide structured context and timing information
- **Correlation**: Use trace IDs in logs to correlate log entries with trace spans

### Grafana Dashboards

Traces can be visualized in Grafana using Tempo:
- **Trace explorer**: Navigate individual traces
- **Service map**: Visualize service dependencies and interactions
- **Latency heatmaps**: Identify latency patterns
- **Error rate**: Track error rates by service

## Benefits Summary

1. **Faster Debugging**: Reduce time to identify root causes from hours to minutes
2. **Performance Insights**: Identify bottlenecks and optimization opportunities
3. **System Understanding**: Gain visibility into complex distributed workflows
4. **Proactive Monitoring**: Detect issues before they become critical
5. **Better SLOs**: Measure and improve service level objectives
6. **Reduced MTTR**: Faster mean time to resolution through better observability

## Next Steps

1. **Alloy Gateway Configuration**: Coordinate with observability team to add traces forwarding to Alloy gateway config (forward traces to Panoptes traces endpoint)
2. **Instrument Core Modules**: Add tracing to platform-connector, fault-quarantine, node-drainer, fault-remediation, and health-events-analyzer
3. **Configure OTLP Export**: Set environment variables in NVSentinel modules to send traces to Alloy gateway:
   - `OTEL_EXPORTER_OTLP_ENDPOINT=dgxc-alloy-gateway.dgxc-alloy.svc.cluster.local:4317`
   - `OTEL_EXPORTER_OTLP_INSECURE=true`
   - `OTEL_TRACES_ENABLED=true`
   - `OTEL_SERVICE_NAME=<module-name>`
4. **Trace Context Propagation**: Implement trace context storage and retrieval in MongoDB change streams (Option B: separate field)
5. **Grafana Dashboards**: Create dashboards for trace visualization and analysis in Grafana
6. **Documentation**: Create runbooks for common trace-based debugging scenarios
7. **Testing**: Validate trace completeness and accuracy in test environments

