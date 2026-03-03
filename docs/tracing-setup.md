# Distributed Tracing Setup with Tempo

This document describes the setup and troubleshooting of distributed tracing for NVSentinel using Grafana Tempo.

## Overview

NVSentinel uses OpenTelemetry (OTEL) for distributed tracing across services. **Currently, only two services are configured to send traces to Tempo:**

### Services with Tracing Enabled

1. **platform-connectors** (DaemonSet)
   - Creates root traces when receiving health events via gRPC
   - Injects trace ID into health event metadata
   - Sends spans to Tempo via OTLP gRPC

2. **fault-quarantine** (Deployment)
   - Continues traces from platform-connectors by extracting trace IDs from event metadata
   - Processes events from MongoDB change streams
   - Sends spans to Tempo via OTLP gRPC

### Services NOT Currently Tracing

The following services do **not** send traces to Tempo (but could be added in the future):
- `node-drainer`
- `health-events-analyzer`
- `fault-remediation`
- `labeler`
- `kubernetes-object-monitor`
- `csp-health-monitor`
- `gpu-health-monitor`
- `syslog-health-monitor`
- `metadata-collector`
- `event-exporter`
- `preflight`
- `janitor`
- `janitor-provider`

**Note:** Adding tracing to additional services requires:
1. Adding `InitTracer` call in `main.go`
2. Adding OTEL environment variables to Helm templates
3. Adding spans to key operations

Traces are sent to Tempo via OTLP (OpenTelemetry Protocol) and visualized in Grafana.

## Architecture

```
Health Monitor → platform-connectors → MongoDB → fault-quarantine
                      ↓ (trace)                    ↓ (continues trace)
                    Tempo (OTLP) ←─────────────────┘
                         ↓
                     Grafana
```

## Installation

### Prerequisites

- Kubernetes cluster (Kind cluster in this setup)
- Helm 3.0+
- kubectl access to the cluster

### Step 1: Install Tempo via Helm

Values files are provided in the repo: `docs/tempo-simple-values.yaml` and `docs/grafana-values.yaml`.

```bash
# From repo root
cd /path/to/NVSentinel

# Add Grafana Helm repository
helm repo add grafana https://grafana.github.io/helm-charts
helm repo update

# Install Tempo (using repo values file)
helm install tempo grafana/tempo -n nvsentinel \
  --create-namespace \
  -f docs/tempo-simple-values.yaml \
  --wait \
  --timeout 3m
```

Alternatively, create the values file manually with the same content as `docs/tempo-simple-values.yaml` (e.g. `cat > /tmp/tempo-simple-values.yaml << 'EOF'` ... and use `-f /tmp/tempo-simple-values.yaml`).

### Step 2: Create Tempo Service (if not created by Helm)

If the Tempo service doesn't exist or is misconfigured, create it:

```bash
# Check if Tempo service exists
kubectl get svc tempo -n nvsentinel

# If service doesn't exist or endpoints are empty, create it:
cat > /tmp/tempo-service.yaml << 'EOF'
apiVersion: v1
kind: Service
metadata:
  name: tempo
  namespace: nvsentinel
spec:
  type: ClusterIP
  ports:
    - name: otlp-grpc
      port: 4317
      targetPort: 4317
    - name: otlp-http
      port: 4318
      targetPort: 4318
    - name: http
      port: 3200
      targetPort: 3200
  selector:
    app.kubernetes.io/name: tempo
    app.kubernetes.io/instance: tempo
EOF

kubectl apply -f /tmp/tempo-service.yaml

# Verify the service and endpoints
kubectl get svc tempo -n nvsentinel
kubectl get endpoints tempo -n nvsentinel
```

**Note:** The service selector should match the Tempo pod labels. Verify with:
```bash
kubectl get pods -n nvsentinel -l app.kubernetes.io/name=tempo --show-labels
```

### Step 3: Verify Tempo Installation

```bash
# Check Tempo pod status
kubectl get pods -n nvsentinel -l app.kubernetes.io/name=tempo

# Check if ingester joined the ring (should show ACTIVE state)
kubectl logs -n nvsentinel tempo-0 | grep -E "ACTIVE|JOINING|lifecycler"

# Verify Tempo is ready
kubectl exec -n nvsentinel tempo-0 -- wget -q -O- http://localhost:3200/ready
```

### Step 4: Install Grafana

```bash
# Install Grafana (using repo values file with Tempo datasource and node graph)
helm install grafana grafana/grafana -n nvsentinel \
  -f docs/grafana-values.yaml \
  --wait \
  --timeout 3m
```

### Step 5: Verify Grafana Installation

```bash
# Check Grafana pod status
kubectl get pods -n nvsentinel -l app.kubernetes.io/name=grafana

# Check Grafana service
kubectl get svc -n nvsentinel -l app.kubernetes.io/name=grafana

# Get admin password (default is "admin" as set in values file)
kubectl get secret --namespace nvsentinel grafana -o jsonpath="{.data.admin-password}" | base64 -d
echo ""
```

**Note:** If the password from the secret doesn't match "admin", you can reset it:
```bash
kubectl create secret generic grafana -n nvsentinel \
  --from-literal=admin-user=admin \
  --from-literal=admin-password=admin \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl rollout restart deployment grafana -n nvsentinel
```

### Step 6: Set Up Port-Forward for Grafana

```bash
# Get Grafana pod name
GRAFANA_POD=$(kubectl get pods -n nvsentinel -l app.kubernetes.io/name=grafana -o jsonpath='{.items[0].metadata.name}')

# Start port-forward (runs in background)
kubectl port-forward -n nvsentinel pod/$GRAFANA_POD 3000:3000 > /tmp/grafana-port-forward.log 2>&1 &

# Verify port-forward is running
ps aux | grep "kubectl port-forward.*grafana" | grep -v grep

# Test Grafana connectivity
curl http://localhost:3000/api/health
```

**Access Grafana:**
- **URL**: http://localhost:3000
- **Username**: `admin`
- **Password**: `admin` (or the password from the secret)

### Step 7: Verify Cluster Configuration

After installing Tempo and Grafana, verify the cluster setup:

```bash
# 1. Verify Tempo pod is running
kubectl get pods -n nvsentinel -l app.kubernetes.io/name=tempo

# 2. Verify Tempo service exists and has endpoints
kubectl get svc tempo -n nvsentinel
kubectl get endpoints tempo -n nvsentinel

# 3. Verify Grafana pod is running
kubectl get pods -n nvsentinel -l app.kubernetes.io/name=grafana

# 4. Verify Grafana service exists
kubectl get svc -n nvsentinel -l app.kubernetes.io/name=grafana

# 5. Test Tempo connectivity from within cluster
kubectl exec -n nvsentinel tempo-0 -- wget -q -O- http://localhost:3200/ready

# 6. Test Grafana can reach Tempo (from Grafana pod)
GRAFANA_POD=$(kubectl get pods -n nvsentinel -l app.kubernetes.io/name=grafana -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n nvsentinel $GRAFANA_POD -- wget -q -O- http://tempo.nvsentinel.svc.cluster.local:3200/ready

# 7. Verify Tempo ingester is ACTIVE
kubectl logs -n nvsentinel tempo-0 | grep -E "ACTIVE|changing instance state.*ACTIVE"
```

**Expected Output:**
- Tempo pod: `Running` (1/1)
- Tempo service: Should have endpoints pointing to tempo-0
- Grafana pod: `Running` (1/1)
- Tempo ready check: Should return `ready`
- Grafana to Tempo connectivity: Should return `ready`
- Ingester status: Should show `ACTIVE` state

### Step 8: Configure NVSentinel Services (and Tilt)

**When using the Tilt cluster:** Tempo and Grafana are installed automatically by the Tiltfile (`tilt/Tiltfile`). After `tilt up`, open Grafana at http://localhost:3000 (port-forward 3000:3000 is configured). No manual Helm install is needed.

Tracing is configured via Helm values in `values-tilt.yaml`:

```yaml
global:
  tracing:
    enabled: true
    endpoint: "tempo.nvsentinel.svc.cluster.local:4317"
    insecure: true
```

This automatically sets the following environment variables in both `platform-connectors` and `fault-quarantine`:
- `OTEL_EXPORTER_OTLP_ENDPOINT=tempo.nvsentinel.svc.cluster.local:4317`
- `OTEL_EXPORTER_OTLP_INSECURE=true`
- `OTEL_TRACES_ENABLED=true`

## Trace Context Propagation

### How It Works

1. **platform-connectors** receives a health event via gRPC
2. Creates a root span with trace ID
3. Extracts trace ID and stores it in `event.Metadata["trace_id"]`
4. Saves event to MongoDB with trace ID in metadata

5. **fault-quarantine** reads event from MongoDB change stream
6. Extracts `trace_id` from event metadata
7. Creates a span context with the same trace ID
8. Continues the trace by linking spans

### Code Implementation

**platform-connectors** (`platform-connectors/pkg/server/platform_connector_server.go`):
```go
// Extract trace ID and add to health events
traceID := span.SpanContext().TraceID().String()
if traceID != "" && traceID != "00000000000000000000000000000000" {
    for _, event := range he.Events {
        if event.Metadata == nil {
            event.Metadata = make(map[string]string)
        }
        event.Metadata["trace_id"] = traceID
    }
}
```

**fault-quarantine** (`fault-quarantine/pkg/eventwatcher/event_watcher.go`):
```go
// Extract trace ID from health event metadata
if traceIDStr, exists := healthEventWithStatus.HealthEvent.Metadata["trace_id"]; exists {
    traceIDBytes, err := hex.DecodeString(traceIDStr)
    if err == nil && len(traceIDBytes) == 16 {
        var traceID trace.TraceID
        copy(traceID[:], traceIDBytes)
        parentSpanContext = trace.NewSpanContext(trace.SpanContextConfig{
            TraceID:    traceID,
            SpanID:     trace.SpanID{},
            TraceFlags: trace.FlagsSampled,
            Remote:     false,
        })
        ctx = trace.ContextWithSpanContext(ctx, parentSpanContext)
    }
}
```

## Troubleshooting

### Issue 1: "Empty Ring" Error

**Error Message:**
```
error querying ingesters in Querier.SearchRecent: forIngesterRings: 
error getting replication set for ring (0): empty ring
```

**Root Cause:**
In monolithic mode (single pod), Tempo's ingester needs explicit ring configuration. The default Helm values don't configure the ingester ring properly.

**Solution:**
Add ingester ring configuration to Helm values:
```yaml
tempo:
  ingester:
    lifecycler:
      ring:
        kvstore:
          store: inmemory  # Required for monolithic mode
        replication_factor: 1
      min_ready_duration: 0s
      final_sleep: 0s
```

**Verification:**
```bash
# Check if ingester is ACTIVE
kubectl logs -n nvsentinel tempo-0 | grep "changing instance state.*ACTIVE"
```

### Issue 2: Conflicting Tempo Deployments

**Symptom:**
- Multiple Tempo pods running
- Traces not appearing in Grafana
- "DoBatch: InstancesCount <= 0" errors

**Root Cause:**
Both a Deployment (from Tilt) and StatefulSet (from Helm) were running simultaneously, causing conflicts.

**Solution:**
```bash
# Identify which is managed by Helm
kubectl get deployment tempo -n nvsentinel -o yaml | grep "managed-by"
kubectl get statefulset tempo -n nvsentinel -o yaml | grep "managed-by"

# Delete the non-Helm managed resource (usually Deployment)
kubectl delete deployment tempo -n nvsentinel
```

**Prevention:**
Ensure only one Tempo installation method is used:
- Either use Helm (recommended)
- Or use Tilt with manual Kubernetes manifests
- Don't mix both approaches

### Issue 3: Traces Not Appearing in Grafana

**Symptoms:**
- No traces visible in Grafana Tempo UI
- `inspected_traces=0` in Tempo logs
- "traces export: context deadline exceeded" in service logs

**Diagnosis Steps:**

1. **Check if traces are being created:**
   ```bash
   kubectl logs -n nvsentinel fault-quarantine-<pod> | grep "trace_id"
   ```

2. **Check if ingester is ACTIVE:**
   ```bash
   kubectl logs -n nvsentinel tempo-0 | grep "ACTIVE"
   ```

3. **Check if services can reach Tempo:**
   ```bash
   kubectl get svc tempo -n nvsentinel
   # Verify OTEL_EXPORTER_OTLP_ENDPOINT matches service name
   ```

4. **Check Tempo is receiving traces:**
   ```bash
   kubectl logs -n nvsentinel tempo-0 | grep -i "received\|trace\|span"
   ```

5. **Check if any traces exist in Tempo:**
   ```bash
   kubectl exec -n nvsentinel tempo-0 -- wget -q -O- "http://localhost:3200/api/search?q={}&limit=5"
   ```

**Common Fixes:**
- Ensure ingester is ACTIVE (wait 10-15 seconds after pod start)
- Verify only one Tempo instance is running
- Check OTEL environment variables are set correctly
- Ensure Tempo service has OTLP ports (4317, 4318) exposed
- Verify time range in Grafana covers when events were processed
- Check if tracing is enabled in `values-tilt.yaml` and services have been restarted

### Issue 4: Grafana "Bad Gateway" Error

**Symptoms:**
- Grafana shows "Bad Gateway" when querying Tempo
- Error: `lookup tempo on 10.96.0.10:53: server misbehaving`

**Solution:**
1. Verify Tempo service exists:
   ```bash
   kubectl get svc tempo -n nvsentinel
   kubectl get endpoints tempo -n nvsentinel
   ```

2. Update Grafana datasource to use full DNS name:
   ```yaml
   url: http://tempo.nvsentinel.svc.cluster.local:3200
   ```

3. Restart Grafana:
   ```bash
   kubectl rollout restart deployment grafana -n nvsentinel
   ```

4. Verify Tempo service selector matches pod labels:
   ```bash
   kubectl get svc tempo -n nvsentinel -o yaml | grep selector
   kubectl get pods -n nvsentinel -l app.kubernetes.io/name=tempo -o yaml | grep labels -A 5
   ```

### Issue 4b: "http2: frame too large" / "frame header looked like an HTTP/1.1 header"

**Symptoms:**
- Grafana (or another component) logs: `http2: failed reading the frame payload: http2: frame too large, note that the frame header looked like an HTTP/1.1 header`
- Or: `error reading server preface: ... connection error: desc = "..."`

**Cause:** A client is using **gRPC (HTTP/2)** to connect to Tempo on port **3200**. Port 3200 is Tempo's **HTTP API** (HTTP/1.1) for querying (e.g. `/ready`, `/api/search`). OTLP gRPC must use port **4317**.

**Port reference:**
| Port  | Protocol   | Use                          |
|-------|------------|------------------------------|
| 3200  | HTTP/1.1   | Tempo HTTP API (query, ready) |
| 4317  | gRPC (HTTP/2) | OTLP trace ingestion        |
| 4318  | HTTP/1.1   | OTLP HTTP ingestion          |

**Solution:**
1. **Grafana Tempo datasource:** Must use only HTTP and port 3200 (e.g. `url: http://tempo.nvsentinel.svc.cluster.local:3200`). Do not point any gRPC client at 3200.
2. **NVSentinel services (OTLP export):** Must use port **4317** for `OTEL_EXPORTER_OTLP_ENDPOINT` (e.g. `tempo.nvsentinel.svc.cluster.local:4317`). Check `values-tilt.yaml` and any Helm values for `global.tracing.endpoint`.
3. If the error is from `grafana-apiserver`, ensure Grafana's Tempo datasource is configured as HTTP only (no gRPC). Upgrading Grafana or the Tempo datasource plugin may fix incorrect gRPC usage against 3200.

### Issue 5: Port 3000 Already in Use

**Solution:**
```bash
# Kill existing port-forwards
pkill -f "port-forward.*grafana\|port-forward.*3000"

# Or free the port
lsof -ti:3000 | xargs kill -9
fuser -k 3000/tcp
```

### Issue 6: Grafana Can't Connect to Tempo

**Diagnosis:**
```bash
# Test connectivity from Grafana pod
GRAFANA_POD=$(kubectl get pods -n nvsentinel -l app.kubernetes.io/name=grafana -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n nvsentinel $GRAFANA_POD -- wget -q -O- http://tempo.nvsentinel.svc.cluster.local:3200/ready
```

**Solution:**
- Verify Tempo service selector matches pod labels
- Check Tempo pod is running: `kubectl get pods -n nvsentinel -l app.kubernetes.io/name=tempo`
- Verify service endpoints: `kubectl get endpoints tempo -n nvsentinel`
- Ensure Grafana datasource URL uses full DNS: `http://tempo.nvsentinel.svc.cluster.local:3200`

### Issue 7: Node Graph Not Visible in Grafana

**Symptoms:**
- Can see detailed spans with attributes in Grafana Tempo
- Node Graph tab is missing or not showing in trace view

**Root Cause:**
The Grafana Tempo datasource configuration is missing the `nodeGraph: enabled: true` setting.

**Solution:**
1. Update Grafana values file to include node graph configuration:
   ```yaml
   datasources:
     datasources.yaml:
       apiVersion: 1
       datasources:
         - name: Tempo
           type: tempo
           access: proxy
           url: http://tempo.nvsentinel.svc.cluster.local:3200
           isDefault: true
           jsonData:
             httpMethod: GET
             nodeGraph:
               enabled: true
   ```

2. Upgrade Grafana with the updated configuration:
   ```bash
   helm upgrade grafana grafana/grafana -n nvsentinel \
     -f /tmp/grafana-values.yaml \
     --wait \
     --timeout 3m
   ```

3. Restart Grafana to load the new configuration:
   ```bash
   kubectl rollout restart deployment grafana -n nvsentinel
   ```

4. Refresh your browser to load the updated datasource configuration

**Verification:**
```bash
# Check if nodeGraph is enabled in the datasource config
kubectl get configmap -n nvsentinel -l app.kubernetes.io/name=grafana \
  -o jsonpath='{.items[0].data.datasources\.yaml}' | grep -A 2 "nodeGraph"
```

**Note:** The node graph visualizes service dependencies and is most useful when:
- Multiple services are involved in the trace
- There are parent-child relationships between spans
- Traces include spans from different services (e.g., `platform-connectors` and `fault-quarantine`)

## Verification

### Trace setup complete

Tracing is **fully set up** when:

1. **Cluster:** Tempo and Grafana are installed (Steps 1–6), and NVSentinel is deployed with `global.tracing.enabled: true` (e.g. in `values-tilt.yaml`).
2. **Services:** `platform-connectors`, `fault-quarantine`, and `node-drainer` have OTEL env vars set (see checklist below) and send spans to Tempo.
3. **Grafana:** You can open Explore → Tempo, run a query (e.g. `{}` or by service name), and see traces with spans from the three services.

Use the **Quick Cluster Setup Checklist** at the end of this doc to verify each item.

### Check Trace Ingestion

```bash
# Query Tempo API directly (via port-forward)
kubectl port-forward -n nvsentinel svc/tempo 3200:3200 &
curl "http://localhost:3200/api/v2/search?q={}&limit=10"

# Or query from within the cluster
kubectl exec -n nvsentinel tempo-0 -- wget -q -O- "http://localhost:3200/api/search?q={}&limit=5"

# Check metrics
kubectl exec -n nvsentinel tempo-0 -- wget -q -O- http://localhost:3200/metrics | grep tempo_distributor
```

### Query Traces in Grafana

1. **Open Grafana:**
   - URL: http://localhost:3000 (after setting up port-forward)
   - Login with username: `admin`, password: `admin`

2. **Navigate to Explore:**
   - Click the compass icon (Explore) in the left sidebar
   - Select **Tempo** as the datasource from the dropdown

3. **Use TraceQL queries:**
   - All traces: `{}`
   - By event ID: `{.event.id="6991a0e46aabce6e223cc580"}` or `{.health_event.id="6991a0e46aabce6e223cc580"}`
   - By service: `{.service.name="platform-connectors"}`
   - By check name: `{.event.check_name="SysLogsXIDError"}`
   - By event consumer: `{.event.consumer="fault-quarantine"}`
   - Combined query (try both event ID attributes): `{.health_event.id="7b186d0f2e2dc23d015d30566df82d6a" || .event.id="7b186d0f2e2dc23d015d30566df82d6a"}`

4. **Adjust time range:**
   - Use the time range selector (top right) to cover when the event was processed
   - Default is "Last 1 hour", but you may need to expand it

5. **Click "Run query"** to search for traces

6. **View Node Graph:**
   - Click on a trace from the search results to open the trace detail view
   - In the trace view, look for tabs at the top: **Timeline**, **Node Graph**, **Table**, etc.
   - Click the **"Node Graph"** tab to see the service dependency graph
   - **Note:** The node graph visualizes relationships between services. It's most useful when:
     - Multiple services are involved in the trace (e.g., both `platform-connectors` and `fault-quarantine`)
     - There are parent-child relationships between spans
     - Spans have different `service.name` attributes
   - If you don't see the Node Graph tab, refresh your browser to load the updated datasource configuration

### Expected Trace Structure

A complete trace should show:
1. `HealthEventOccurredV1` (platform-connectors) - root span
2. `process_health_event` (platform-connectors) - child span
3. `processEventFromChangeStream` (fault-quarantine) - linked span
4. `ProcessEvent` (fault-quarantine) - child span
5. `applyQuarantine` (fault-quarantine) - child span (if quarantine occurs)

### Tracking Multiple Event Consumers

**Question:** Can we track when and which service picked up an event if multiple services consume the same event?

**Answer:** Yes! When multiple services consume the same event from MongoDB change streams, each service creates its own span in the same trace. This allows you to see:

1. **Which services processed the event:**
   - Each service adds `event.consumer` attribute (e.g., "fault-quarantine", "node-drainer", "health-events-analyzer")
   - Each service adds `service.name` attribute

2. **When each service consumed the event:**
   - Span start time is automatically recorded by OpenTelemetry
   - You can see the exact timestamp when each service started processing by looking at the span start time

3. **How to query in Grafana:**
   ```traceql
   # Find all services that processed a specific event
   {.event.id="6991a0e46aabce6e223cc580"}
   
   # Find events processed by multiple services (when both services have tracing enabled)
   {.event.consumer="fault-quarantine"} AND {.event.consumer="node-drainer"}
   
   # Filter by service name (automatically added by OpenTelemetry)
   {.service.name="fault-quarantine"}
   ```

**Example Trace with Multiple Consumers:**

When an event is consumed by both `fault-quarantine` and `node-drainer`, the trace will show:
```
HealthEventOccurredV1 (platform-connectors)
├── process_health_event (platform-connectors)
│   └── [event saved to MongoDB with trace_id in metadata]
│
├── processEventFromChangeStream (fault-quarantine)
│   ├── service.name: "fault-quarantine" (automatic)
│   ├── span start time: "2026-02-15T14:03:01.328Z" (automatic)
│   ├── event.consumer: "fault-quarantine" (explicit)
│   └── ProcessEvent (fault-quarantine)
│
└── processEventFromChangeStream (node-drainer)
    ├── service.name: "node-drainer" (automatic)
    ├── span start time: "2026-02-15T14:03:01.335Z" (automatic)
    ├── event.consumer: "node-drainer" (explicit)
    └── PreprocessAndEnqueueEvent (node-drainer)
```

**Key Points:**
- All services that consume the same event will appear in the **same trace** (same trace ID)
- Each service creates its own span with:
  - `service.name` - automatically added by OpenTelemetry tracer resource
  - Span start time - automatically recorded by OpenTelemetry
  - `event.consumer` - explicitly added to identify which service consumed the event
- You can see the order and timing of when each service processed the event by comparing span start times
- This works because all services extract the same `trace_id` from the event metadata

### How to Check Service Consumption Order

**Question:** How do I check if `fault-quarantine` consumed an event before `node-drainer`?

**Answer:** Compare the span start times in Grafana Tempo. Here are several methods:

#### Method 1: Visual Timeline in Grafana

1. Open Grafana → Explore → Tempo
2. Query by event ID: `{.event.id="6991a0e46aabce6e223cc580"}`
3. Click on a trace to view the timeline
4. Look at the visual timeline - spans are ordered by start time
5. Compare the `processEventFromChangeStream` spans:
   - If `fault-quarantine` span appears earlier (left side), it consumed first
   - If `node-drainer` span appears earlier, it consumed first

#### Method 2: Using TraceQL with Time Comparison

```traceql
# Find traces where both services processed the event
{.event.id="6991a0e46aabce6e223cc580"} AND {.service.name="fault-quarantine"} AND {.service.name="node-drainer"}

# Then in Grafana, you can:
# 1. View the trace timeline
# 2. Hover over each span to see exact start time
# 3. Compare the timestamps
```

#### Method 3: Using Tempo API (Programmatic)

```bash
# Get trace by event ID
TRACE_ID=$(curl -s "http://localhost:3200/api/v2/search?q={.event.id=\"6991a0e46aabce6e223cc580\"}&limit=1" | jq -r '.traces[0].traceID')

# Get full trace details
curl -s "http://localhost:3200/api/traces/$TRACE_ID" | jq '.resourceSpans[].scopeSpans[].spans[] | select(.name=="processEventFromChangeStream") | {service: .attributes[] | select(.key=="service.name") | .value.stringValue, startTime: .startTimeUnixNano}'
```

This will show:
```json
{
  "service": "fault-quarantine",
  "startTime": "1771164181328412879"
}
{
  "service": "node-drainer",
  "startTime": "1771164181335000000"
}
```

Lower `startTime` = consumed earlier.

#### Method 4: Using Grafana TraceQL with Duration

```traceql
# Find traces and compare relative start times
{.event.id="6991a0e46aabce6e223cc580"} 
# Then in the trace view, look at the "Start time" column or timeline
```

**Note:** Currently, only `fault-quarantine` has tracing enabled. To compare with `node-drainer`, you need to:
1. Add tracing initialization to `node-drainer/main.go`
2. Add OTEL environment variables to `node-drainer` Helm template
3. Add trace ID extraction in `node-drainer` event processing (similar to fault-quarantine)

#### Quick Answer: How to Check Order

**In Grafana Tempo UI:**
1. Query by event ID: `{.event.id="6991a0e46aabce6e223cc580"}`
2. Click on the trace to open the timeline view
3. Look at the horizontal timeline - spans are ordered by start time (left to right = earlier to later)
4. Find the `processEventFromChangeStream` spans for each service
5. The span that appears further left (earlier timestamp) consumed the event first

**Example:**
- If `fault-quarantine` span starts at `14:03:01.328Z` and `node-drainer` starts at `14:03:01.335Z`
- Then `fault-quarantine` consumed the event **7ms earlier** than `node-drainer`

**Using TraceQL:**
```traceql
# Find traces with both services
{.event.id="6991a0e46aabce6e223cc580"} AND {.service.name="fault-quarantine"} AND {.service.name="node-drainer"}
```

Then view the trace timeline to see the relative order.

## Configuration Reference

### Tempo Helm Values (Complete)

```yaml
tempo:
  ingestion:
    otlp:
      grpc:
        enabled: true
      http:
        enabled: true
  
  # Required for monolithic mode
  ingester:
    max_block_duration: 5m
    lifecycler:
      ring:
        kvstore:
          store: inmemory
        replication_factor: 1
      min_ready_duration: 0s
      final_sleep: 0s

service:
  type: ClusterIP
  ports:
    otlp-grpc:
      port: 4317
    otlp-http:
      port: 4318
    http:
      port: 3200
```

### NVSentinel Tracing Configuration

```yaml
# distros/kubernetes/nvsentinel/values-tilt.yaml
global:
  tracing:
    enabled: true
    endpoint: "tempo.nvsentinel.svc.cluster.local:4317"
    insecure: true
```

### Grafana Datasource Configuration (Complete)

```yaml
# Grafana Helm values file
grafana:
  adminUser: admin
  adminPassword: admin
  persistence:
    enabled: false
  service:
    type: ClusterIP
    port: 3000

datasources:
  datasources.yaml:
    apiVersion: 1
    datasources:
      - name: Tempo
        type: tempo
        access: proxy
        url: http://tempo.nvsentinel.svc.cluster.local:3200
        isDefault: true
        jsonData:
          httpMethod: GET
          nodeGraph:
            enabled: true  # Required to enable node graph visualization
```

## Key Learnings

1. **Monolithic Mode Requirements**: Tempo in single-pod mode requires explicit ingester ring configuration with `inmemory` KV store.

2. **Trace Context Propagation**: When crossing service boundaries via databases (not direct RPC), trace IDs must be explicitly propagated in event metadata.

3. **Avoid Deployment Conflicts**: Only use one deployment method (Helm or Tilt) to avoid resource conflicts.

4. **Ingester Ring Timing**: The ingester takes 10-15 seconds to join the ring and become ACTIVE. Traces won't be accepted until this happens.

5. **Service Discovery**: Use Kubernetes service DNS names (`tempo.nvsentinel.svc.cluster.local`) for reliable service discovery within the cluster.

6. **Selective Tracing**: Not all services need to send traces. Currently only `platform-connectors` and `fault-quarantine` are instrumented, which covers the main health event processing flow.

## References

- [Tempo Documentation](https://grafana.com/docs/tempo/latest/)
- [OpenTelemetry Go SDK](https://opentelemetry.io/docs/instrumentation/go/)
- [OTLP Protocol](https://opentelemetry.io/docs/specs/otlp/)

## Adding Tracing to Additional Services

If you want to add tracing to other services in the NVSentinel stack, follow these steps:

### Step 1: Add Tracing Initialization

Add to the service's `main.go`:

```go
import (
    "github.com/nvidia/nvsentinel/commons/pkg/tracing"
)

func main() {
    // ... existing initialization ...
    
    // Initialize tracing
    tracingCfg := tracing.LoadConfigFromEnv("your-service-name")
    shutdownTracer, err := tracing.InitTracer(tracingCfg)
    if err != nil {
        slog.Warn("Failed to initialize tracer", "error", err)
    } else {
        defer shutdownTracer()
    }
    
    // ... rest of main ...
}
```

### Step 2: Add OTEL Environment Variables to Helm Template

Add to the service's deployment/daemonset template (e.g., `charts/your-service/templates/deployment.yaml`):

```yaml
env:
  # ... existing env vars ...
  {{- if .Values.global.tracing.enabled }}
  - name: OTEL_EXPORTER_OTLP_ENDPOINT
    value: {{ .Values.global.tracing.endpoint | quote }}
  - name: OTEL_EXPORTER_OTLP_INSECURE
    value: {{ .Values.global.tracing.insecure | quote }}
  - name: OTEL_TRACES_ENABLED
    value: "true"
  {{- end }}
```

### Step 3: Add Spans to Key Operations

Add spans to important operations:

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/trace"
)

func yourFunction(ctx context.Context) {
    tracer := otel.Tracer("your-service-name")
    ctx, span := tracer.Start(ctx, "your-operation-name",
        trace.WithAttributes(
            attribute.String("key", "value"),
        ))
    defer span.End()
    
    // ... your code ...
}
```

### Step 4: Test

1. Deploy the updated service
2. Verify OTEL environment variables are set: `kubectl exec <pod> -- env | grep OTEL`
3. Check logs for tracer initialization: `kubectl logs <pod> | grep -i tracer`
4. Verify traces appear in Grafana

## Maintenance

### Upgrading Tempo

```bash
helm upgrade tempo grafana/tempo -n nvsentinel \
  -f /tmp/tempo-simple-values.yaml \
  --wait
```

### Viewing Tempo Logs

```bash
# All logs
kubectl logs -n nvsentinel tempo-0

# Filter for ring/ingester issues
kubectl logs -n nvsentinel tempo-0 | grep -E "ring|ingester|ACTIVE"

# Follow logs
kubectl logs -n nvsentinel tempo-0 -f
```

### Uninstalling Tempo and Grafana

```bash
# Uninstall Grafana
helm uninstall grafana -n nvsentinel

# Uninstall Tempo
helm uninstall tempo -n nvsentinel

# Optionally delete the Tempo service if it was manually created
kubectl delete svc tempo -n nvsentinel 2>/dev/null || true

# Clean up port-forwards
pkill -f "port-forward.*grafana\|port-forward.*tempo" 2>/dev/null || true
```

## Quick Cluster Setup Checklist

Use this checklist to verify your cluster setup is complete:

- [ ] Tempo pod is running: `kubectl get pods -n nvsentinel -l app.kubernetes.io/name=tempo`
- [ ] Tempo service exists: `kubectl get svc tempo -n nvsentinel`
- [ ] Tempo service has endpoints: `kubectl get endpoints tempo -n nvsentinel`
- [ ] Tempo ingester is ACTIVE: `kubectl logs -n nvsentinel tempo-0 | grep ACTIVE`
- [ ] Grafana pod is running: `kubectl get pods -n nvsentinel -l app.kubernetes.io/name=grafana`
- [ ] Grafana service exists: `kubectl get svc -n nvsentinel -l app.kubernetes.io/name=grafana`
- [ ] Grafana can reach Tempo: Test from Grafana pod to `http://tempo.nvsentinel.svc.cluster.local:3200/ready`
- [ ] Port-forward is running: `ps aux | grep "port-forward.*grafana"`
- [ ] Grafana is accessible: `curl http://localhost:3000/api/health`
- [ ] Tracing enabled in values-tilt.yaml: `grep -A 3 "tracing:" distros/kubernetes/nvsentinel/values-tilt.yaml`
- [ ] Services have OTEL env vars: Check fault-quarantine, platform-connectors, and node-drainer pods

---

**Last Updated:** 2026-02-17  
**Tested With:** Tempo 2.10.0, Grafana 12.3.1, Helm Chart 1.24.4

