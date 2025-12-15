# Implement Dry Run Mode for Health Checks

## Overview 

Currently, all health monitors publish events without dry-run mode, which causes other modules to perform operations that change the cluster state:
1. Node conditions are created/updated
2. Quarantine labels and annotations are applied (even if FQM is running in dry-run mode), creating confusion about why the node was not cordoned or why the remediation was not performed
3. Nodes are drained by NDR
4. Nodes are remediated by FRM

We want to prevent affecting the cluster state and instead export events only for observability purposes. This is why we need dry-run mode for health events.

This feature will be implemented in all health monitors:
1. GPU Health Monitor
2. Syslog Health Monitor
3. CSP Health Monitor
4. Kubernetes Object Monitor

When a health monitor is running with dry-run enabled, all published health events (fatal, non-fatal, healthy, unhealthy) will have the dry-run flag set to `true`.

### Dry Run Events Behavior

Dry run events WILL be:
- Stored in database (for analysis)
- Exported as metrics (for observability)
- Exported by event exporter (for external monitoring)

Dry run events will NOT trigger:
- Node condition creation/updates (Platform Connector skips)
- Kubernetes event creation (Platform Connector skips)
- Node quarantine (FQM skips - no taint, labels, or annotations)
- Node draining (NDR won't see them - filtered by pipeline)
- Remediation CR creation (FRM won't see them - filtered by pipeline)
- Pattern analysis (HEA skips - no aggregated fatal events published)

---

## Flow Diagrams

```plaintext
┌─────────────────────────────────────────────────────────────────┐
│ Health Monitor                                                  │
│ - Detects issue (XID error, CSP maintenance, etc.)              │
│ - Reads --dry-run flag                                          │
│ - Creates HealthEvent with dryRun = true                        │
└────────────────────────┬────────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────────────────┐
│ Platform Connector                                              │
│ - Receives event via gRPC                                       │
│ - Runs pipeline transformers (overrides, metadata)              │
│ - Checks dryRun flag                                            │
│ -    SKIP node conditions (dry run = true)                      │
│ -    SKIP Kubernetes events (dry run = true)                    │
│ -    Stores in database (for observability)                     │
└────────────────────────┬────────────────────────────────────────┘
                         ↓
┌─────────────────────────────────────────────────────────────────┐
│ Database (MongoDB/PostgreSQL)                                   │
│ - Event stored with dryRun = true                               │
│ - Change stream triggered                                       │
└──┬──────────────────────┬────────────────────┬──────────────────┘
   ↓                      ↓                    ↓
┌──────────────┐  ┌──────────────┐  ┌─────────────────┐
│ HEA          │  │ FQM          │  │ Event Exporter  │
│    SKIP      │  │    SKIP      │  │  Process event  │
│ - Check      │  │ - Check      │  │ - Transform     │
│   dryRun     │  │   dryRun     │  │   to CloudEvent │
│   flag       │  │   flag       │  │ - Include       │
│ - Skip       │  │ - Skip       │  │   dryRun field  │
│   pattern    │  │   quarantine │  │ - Publish       │
│   analysis   │  │ - NO taint   │  │   event         │
│              │  │ - NO labels/ │  │                 │
│              │  │   annotations│  │                 │
│              │  │ - NO status  │  │                 │
│              │  │   update     │  │                 │
└──────────────┘  └──────────────┘  └─────────────────┘
                         ↓
                  Database NOT Updated
                  (nodeQuarantined = null)
                         ↓
          ┌──────────────┴──────────────┐
          ↓                             ↓
┌─────────────────┐          ┌─────────────────┐
│ NDR             │          │     FRM         |
│will not receive │          │will not receive │
│    event        │          │    event        │
└─────────────────┘          └─────────────────┘
```
---

## Code Changes
### Step 1: Update Protobuf Definition

**File:** `data-models/protobufs/health_event.proto`

```protobuf
message HealthEvent {
  uint32 version = 1;
  string agent = 2;
  string componentClass = 3;
  string checkName = 4;
  bool isFatal = 5;
  bool isHealthy = 6;
  string message = 7;
  RecommendedAction recommendedAction = 8;
  repeated string errorCode = 9;
  repeated Entity entitiesImpacted = 10;
  map<string, string> metadata = 11;
  google.protobuf.Timestamp generatedTimestamp = 12;
  string nodeName = 13;
  BehaviourOverrides quarantineOverrides = 14;
  BehaviourOverrides drainOverrides = 15;
  
  // NEW: Dry run mode flag
  // If true, event is for observability only - no cluster resources should be modified
  bool dryRun = 16;
}
```

**Commands:**
```bash
cd data-models
make generate  # Regenerate Go code from protobuf
```

---

### Step 2: Health Monitor Implementation

**Syslog health monitor**

Health monitors read the `--dry-run` flag and pass it to **all handlers**. Each handler sets the flag in health events before publishing.

**File:** `health-monitors/syslog-health-monitor/main.go`

```go
// Parse the flag
flag.Parse()
globalDryRun := *dryRunFlag

// Pass dryRun flag to ALL handler initializations
xidHandler := xid.NewXIDHandler(nodeName, agentName, componentClass, 
    checkName, xidAnalyserEndpoint, metadataPath, globalDryRun)

sxidHandler := sxid.NewSXIDHandler(nodeName, agentName, componentClass, 
    checkName, metadataPath, globalDryRun)

gpuFallenHandler := gpufallen.NewGPUFallenHandler(nodeName, agentName, 
    componentClass, checkName, globalDryRun)
```

Update each handler to accept and use the dryRun flag:

File: `health-monitors/syslog-health-monitor/pkg/xid/xid_handler.go` (and similar for SXID, GPUFallen)

```go
type XIDHandler struct {
	// ... existing fields ...
	dryRun bool  // NEW: Store dry run flag
}

func NewXIDHandler(..., dryRun bool) (*XIDHandler, error) {
	return &XIDHandler{
		// ... existing fields ...
		dryRun: dryRun,  // NEW: Store flag
	}, nil
}

func (h *XIDHandler) createHealthEvent(xid int, message string) *pb.HealthEvent {
	event := &pb.HealthEvent{
		// ... all existing fields ...
		DryRun: h.dryRun,  // NEW: Set flag in event
	}
	return event
}
```

Apply to all handlers: XIDHandler, SXIDHandler, GPUFallenHandler (syslog-health-monitor)

**Kubernetes Object Monitor**

File: `health-monitors/kubernetes-object-monitor/pkg/publisher/publisher.go`

```go
type Publisher struct {
	pcClient pb.PlatformConnectorClient
	dryRun   bool  // NEW: Store dry run flag
}

func New(client pb.PlatformConnectorClient, dryRun bool) *Publisher {
	return &Publisher{
		pcClient: client,
		dryRun:   dryRun,  // NEW: Store flag
	}
}

func (p *Publisher) PublishHealthEvent(ctx context.Context,
	policy *config.Policy, nodeName string, isHealthy bool) error {
	event := &pb.HealthEvent{
		// ... all existing fields ...
		DryRun: p.dryRun,  // NEW: Set flag in ALL events
	}
	
	healthEvents := &pb.HealthEvents{
		Version: 1,
		Events:  []*pb.HealthEvent{event},
	}
	
	return p.sendWithRetry(ctx, healthEvents)
}
```

**CSP Health Monitor**
File: `health-monitors/csp-health-monitor/pkg/triggerengine/trigger.go`

```go
type Engine struct {
	store      datastore.Store
	udsClient  pb.PlatformConnectorClient
	config     *config.Config
	dryRun     bool  // NEW: Store dry run flag
	// ... other fields ...
}

func NewEngine(cfg *config.Config, store datastore.Store, 
	udsClient pb.PlatformConnectorClient, k8sClient kubernetes.Interface, 
	dryRun bool) *Engine {
	return &Engine{
		// ... existing fields ...
		dryRun: dryRun,  // NEW: Store flag
	}
}

func (e *Engine) mapMaintenanceEventToHealthEvent(
	event model.MaintenanceEvent, isHealthy, isFatal bool, message string,
) (*pb.HealthEvent, error) {
	healthEvent := &pb.HealthEvent{
		Agent:             "csp-health-monitor",
		// ... all existing fields ...
		DryRun:            e.dryRun,  // NEW: Set flag in event
	}
	return healthEvent, nil
}
```

**GPU Health Monitor**

File: `health-monitors/gpu-health-monitor/gpu_health_monitor/cli.py`

```python
@click.command()
# ... existing options ...
@click.option("--dry-run", type=bool, default=False, 
              help="Run in dry run/audit mode", required=False)
def cli(dcgm_addr, config_file, port, verbose, state_file, 
        dcgm_k8s_service_enabled, metadata_path, dry_run):
    # ... existing code ...
    
    # Pass dry_run to event processor
    event_processor = platform_connector.PlatformConnectorEventProcessor(
        socket_path=platform_connector_config["SocketPath"],
        node_name=node_name,
        # ... other params ...
        dry_run=dry_run  # NEW: Pass flag
    )
```

File: `health-monitors/gpu-health-monitor/gpu_health_monitor/platform_connector/platform_connector.py`

```python
class PlatformConnectorEventProcessor:
    def __init__(self, socket_path, node_name, exit, dcgm_errors_info_dict,
                 state_file_path, dcgm_health_conditions_categorization_mapping_config,
                 metadata_path, dry_run=False):  # NEW: Add parameter
        # ... existing fields ...
        self._dry_run = dry_run  # NEW: Store flag
    
    def health_event_occurred(self, health_details, gpu_ids, serials):
        # Creates HealthEvents in multiple places - add dryRun to ALL:
        
        health_event = platformconnector_pb2.HealthEvent(
            version=self._version,
            agent=self._agent,
            # ... all existing fields ...
            dryRun=self._dry_run,  # NEW: Add to ALL HealthEvent creations
        )
```

GPU Health Monitor creates HealthEvent objects in 4 different places (lines ~104, 204, 267, 361). Add `dryRun=self._dry_run` to all 4 locations.

### Step 3: Platform Connector (Kubernetes)

Platform connector receives health events BEFORE they're stored in the database and:
- Creates/updates **node conditions** on Kubernetes nodes
- Creates **Kubernetes events** for unhealthy, non-fatal events
- Store events in mongodb

For events running in dry-run mode, the platform connector should:
1. Store events in database (needed for observability)
2. Not create node conditions
3. Not create Kubernetes events

File: `platform-connectors/pkg/connectors/kubernetes/process_node_events.go`

Add skip logic in `processHealthEvents()` method:

```go
func (r *K8sConnector) processHealthEvents(ctx context.Context, healthEvents *protos.HealthEvents) error {
	var nodeConditions []corev1.NodeCondition

	// NEW: Filter out dry run events - they should not modify node conditions or create K8s events
	var nonDryRunEvents []*protos.HealthEvent
	for _, healthEvent := range healthEvents.Events {
		if healthEvent.DryRun {
			slog.Info("Skipping dry run event (audit mode) - no node conditions or K8s events will be created",
				"node", healthEvent.NodeName,
				"checkName", healthEvent.CheckName)
			metrics.DryRunEventsSkipped.Inc()  // Add this metric
			continue  // Skip this event
		}
		nonDryRunEvents = append(nonDryRunEvents, healthEvent)
	}

	// ... existing code
}
```

File: `platform-connectors/pkg/connectors/kubernetes/metrics.go`

```go
var (
	// ... existing metrics ...
	
	// NEW: Track dry run events skipped by platform connector
	DryRunEventsSkipped = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "nvsentinel_platform_connector_dry_run_events_skipped_total",
			Help: "Total number of dry run/audit events skipped (no node conditions/events created)",
		},
	)
)
```

---

### Step 4: Event Exporter
External systems need to know which events are dry run for proper monitoring.

File: `event-exporter/pkg/transformer/cloudevents.go`

Update `ToCloudEvent()` function to add new field `dryRun` so that from the metrics we can get to know which events were published in dry run mode:

```go
func ToCloudEvent(event *pb.HealthEvent, metadata map[string]string) (*CloudEvent, error) {
	// ... existing timestamp, entities code ...

	healthEventData := map[string]any{
		"version":            event.Version,
		"agent":              event.Agent,
		"componentClass":     event.ComponentClass,
		"checkName":          event.CheckName,
		"isFatal":            event.IsFatal,
		"isHealthy":          event.IsHealthy,
		"message":            event.Message,
		"recommendedAction":  event.RecommendedAction.String(),
		"errorCode":          errorCodes,
		"entitiesImpacted":   entities,
		"generatedTimestamp": timestamp,
		"nodeName":           event.NodeName,
		"dryRun":             event.DryRun,  // NEW: Include dry run flag
	}

	// ... rest of method
}
```

File: `event-exporter/pkg/metrics/metrics.go`

```go
var (
	// ... existing metrics ...
	
	// NEW: Track dry run events exported
	DryRunEventsExported = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "nvsentinel_event_exporter_dry_run_events_exported_total",
			Help: "Total number of dry run/audit events exported (for observability)",
		},
	)
)
```

---

### Step 5: Fault Quarantine Manager (FQM)

Update this module to skip processing dry-run events so that:
- Node is not cordoned
- Annotations and labels are not applied
- `nodeQuarantined` status is not set (remains null)

**Why skipping `nodeQuarantined` update matters:** FRM and NDR use pipeline filters that match on `nodeQuarantined` status. If this field is not set, their pipelines will exclude the event, and they won't see it.

File: `fault-quarantine/pkg/reconciler/reconciler.go`

Add skip logic in `ProcessEvent()` method:

```go
func (r *Reconciler) ProcessEvent(
	ctx context.Context,
	event *model.HealthEventWithStatus,
	ruleSetEvals []evaluator.RuleSetEvaluatorIface,
	rulesetsConfig rulesetsConfig,
) *model.Status {
	if shouldHalt := r.checkCircuitBreakerAndHalt(ctx); shouldHalt {
		return nil
	}

	// NEW: Skip dry run events and update the metric
	if event.HealthEvent != nil && event.HealthEvent.DryRun {
		slog.Info("Skipping dry run event (audit mode) - no quarantine action will be taken",
			"node", event.HealthEvent.NodeName,
			"checkName", event.HealthEvent.CheckName,
			"agent", event.HealthEvent.Agent)
		metrics.DryRunEventsSkipped.Inc()
		return nil  // No quarantine status set
	}

	slog.Debug("Processing event", "checkName", event.HealthEvent.CheckName)
	isNodeQuarantined := r.handleEvent(ctx, event, ruleSetEvals, rulesetsConfig)
	// ... rest of method
}
```
File: `fault-quarantine/pkg/metrics/metrics.go`

```go
var (
	// ... existing metrics ...
	
	// NEW: Track dry run events that were skipped
	DryRunEventsSkipped = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "nvsentinel_fault_quarantine_dry_run_events_skipped_total",
			Help: "Total number of dry run/audit events skipped by fault quarantine manager",
		},
	)
)
```

**Note:** We don't need skip logic in NDR and FRM modules as they won't receive these events. Since FQM doesn't set the `nodeQuarantined` field for dry run events, the pipeline filters in NDR and FRM will exclude these events from their change streams. 

---

### Step 6: Health Events Analyzer (HEA)

Health Events Analyzer should skip processing dry run events so that:
- No pipeline queries run for pattern detection
- No aggregated fatal events are published

File: `health-events-analyzer/pkg/reconciler/reconciler.go`

Add skip logic in `processHealthEvent()` method (around line 136):

```go
func (r *Reconciler) processHealthEvent(ctx context.Context, event *datamodels.HealthEventWithStatus) error {
	// ... existing logic

	// NEW: Skip dry run events - no pattern analysis or event publishing
	if event.HealthEvent.DryRun {
		slog.Info("Skipping dry run event (audit mode) - no pattern analysis will occur",
			"node", labelValue,
			"checkName", event.HealthEvent.CheckName)
		metrics.DryRunEventsSkipped.Inc()
		return nil  // Skip processing, no aggregated event published
	}

	// ... rest of method
}
```

File: `health-events-analyzer/pkg/reconciler/metrics.go`

```go
var (
	// ... existing metrics ...
	
	// NEW: Track dry run events skipped
	DryRunEventsSkipped = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "nvsentinel_health_events_analyzer_dry_run_events_skipped_total",
			Help: "Total number of dry run/audit events skipped by health events analyzer",
		},
	)
)
```

---

### Step 7: Configuration Changes

Health monitors need to set the `dryRun` flag based on configuration. The default value for dryRun will be false that means by default all health checks will run in standard mode where all clusters operations will be performed.

Add to Helm values.yaml (for each health monitor):

```yaml
syslogHealthMonitor:
    dryRun: false  # Set to true to make ALL checks run in dry run mode
cspHealthMonitor:
    dryRun: false  # Set to true to make ALL checks run in dry run mode
kubernetesObjectMonitor:
    dryRun: false  # Set to true to make ALL checks run in dry run mode
gpuHealthMonitor:
    dryRun: false  # Set to true to make ALL checks run in dry run mode
```

Update deployment template (add to container args in `_helpers.tpl` or `deployment.yaml`):

```yaml
args:
  # ... existing args ...
  {{- if .Values.dryRun }}
  - "--dry-run"
  {{- end }}
```

Add command-line flag in health monitor with default value set to `false` (main.go):

```go
dryRunFlag = flag.Bool("dry-run", false, "Run in dry run/audit mode")
```
---