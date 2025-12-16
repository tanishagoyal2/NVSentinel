# Implement Dry Run Mode for Health Checks

## Overview 

Currently, all health monitors publish events without dry-run mode, which causes other modules to perform operations that change the cluster state:
1. Node conditions are created/updated
2. Quarantine labels and annotations are applied (even if fault quarantine module is running in dry-run mode), creating confusion about why the node was not cordoned or why the remediation was not performed
3. Nodes are drained by node drainer module
4. Nodes are remediated by fault remediation

We want to prevent affecting the cluster state and instead export events only for observability purposes. This is why we need dry-run mode for health events.

This feature will be implemented in all health monitors:
1. GPU Health Monitor
2. Syslog Health Monitor
3. CSP Health Monitor
4. Kubernetes Object Monitor
5. Health events analyzer

When a health monitor is running with dry-run enabled, all published health events (fatal, non-fatal, healthy, unhealthy) will have the dry-run flag set to `true`.

### Dry Run Events Behavior

Dry run events WILL be:
- Stored in database (for analysis)
- Exported as metrics (for observability)
- Exported by event exporter (for external monitoring)

Dry run events will NOT trigger:
- Node condition creation/updates (Platform Connector skips)
- Kubernetes event creation (Platform Connector skips)
- Node quarantine (fault-quarantine skips - no taint, labels, or annotations)
- Node draining (node-drainer module won't receive them - filtered by pipeline)
- Remediation CR creation (fault-remediation module won't receive them - filtered by pipeline)
- Pattern analysis (health-events-analyzer ignores incoming `dryRun=true` events for pattern detection and don't consider dryRun enabled events while running the query)

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
┌────────────────────────┐  ┌────────────────────────┐  ┌────────────────────────┐
│ Health Events Analyzer │  │ Fault Quarantine       │  │ Event Exporter         │
│ Filter + Process       │  │ Filter                 │  │ Process event          │
│ - Change-stream filter │  │ - Change-stream filter │  │ - Transform            │
│   excludes dryRun=true │  │   excludes dryRun=true │  │   to CloudEvent        │
│ - Rule queries exclude │  │ - NO cordon/taints     │  │ - Include dryRun       │
│   dryRun=true events   │  │ - NO labels/           │  │   field                │
│ - If running in dryRun │  │   annotations          │  │ - Publish event        │
│   mode: publish events │  │ - NO status update     │  │                        │
│   with dryRun=true     │  │                        │  │                        │
└────────────────────────┘  └────────────────────────┘  └────────────────────────┘
                         ↓
                  Database NOT Updated
                  (nodeQuarantined = null)
                         ↓
          ┌──────────────┴──────────────┐
          ↓                             ↓
┌────────────────────────┐  ┌────────────────────────┐
│ Node Drainer Module    │  │ Fault remediation      │
│                        │  │ Module                 │
│ will not receive event │  │ will not receive event │
└────────────────────────┘  └────────────────────────┘
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

GPU Health Monitor creates HealthEvent objects in 4 different places in this file. Add `dryRun=self._dry_run` to all 4 locations.

### Step 3: Platform Connector (Kubernetes)

Platform connector receives health events through the gRPC connection:
- Store events in the database (MongoDB/PostgreSQL) for observability
- Create/update **node conditions** on Kubernetes nodes
- Create **Kubernetes events** for unhealthy, non-fatal events

For events running in dry-run mode, the platform connector should:
1. Store events in database (needed for observability)
2. Not create node conditions
3. Not create Kubernetes events

File: `platform-connectors/pkg/connectors/kubernetes/process_node_events.go`

Add skip logic in `processHealthEvents()` method to skip adding node condition/event:

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
			continue  // Skip this event
		}
		nonDryRunEvents = append(nonDryRunEvents, healthEvent)
	}

	// ... existing code
}
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
---

### Step 5: Store Client 

We need new methods in mongodb and postgres pipeline builder:
1. `BuildNonDryRunHealthEventInsertsPipeline` which filters `dryRun=false` inserted events for fault-quarantine module
2. `BuildNonDryRunNonFatalUnhealthyInsertsPipeline` which filters `dryRun=false`, non-fatal and unhealthy inserted events for health-events-analyzer module.

File: `store-client/pkg/client/mongodb_pipeline_builder.go`

```go
// BuildNonDryRunHealthEventInsertsPipeline creates a pipeline that watches for all non-dry-run health event inserts.
func (b *MongoDBPipelineBuilder) BuildNonDryRunHealthEventInsertsPipeline() datastore.Pipeline {
	return datastore.ToPipeline(
		datastore.D(
			datastore.E("$match", datastore.D(
				datastore.E("operationType", datastore.D(
					datastore.E("$in", datastore.A("insert")),
				)),
				datastore.E("fullDocument.healthevent.dryrun", false),
			)),
		),
	)
}

// BuildNonDryRunNonFatalUnhealthyInsertsPipeline creates a pipeline for non-fatal unhealthy events
// excluding dry-run (audit) events.
func (b *MongoDBPipelineBuilder) BuildNonDryRunNonFatalUnhealthyInsertsPipeline() datastore.Pipeline {
	return datastore.ToPipeline(
		datastore.D(
			datastore.E("$match", datastore.D(
				datastore.E("operationType", "insert"),
				datastore.E("fullDocument.healthevent.agent", datastore.D(datastore.E("$ne", "health-events-analyzer"))),
				datastore.E("fullDocument.healthevent.ishealthy", false),
				datastore.E("fullDocument.healthevent.dryrun", false),
			)),
		),
	)
}

```

Same method required in postgres builder

File: `store-client/pkg/client/postgresql_pipeline_builder.go`

```go
// BuildNonDryRunHealthEventInsertsPipeline creates a pipeline that watches for health event inserts
// excluding dry-run (audit) events.
func (b *PostgreSQLPipelineBuilder) BuildNonDryRunHealthEventInsertsPipeline() datastore.Pipeline {
	return datastore.ToPipeline(
		datastore.D(
			datastore.E("$match", datastore.D(
				datastore.E("operationType", datastore.D(
					datastore.E("$in", datastore.A("insert")),
				)),
				datastore.E("fullDocument.healthevent.dryrun", false),
			)),
		),
	)
}

// BuildNonDryRunNonFatalUnhealthyInsertsPipeline creates a pipeline for non-fatal unhealthy events
// excluding dry-run (audit) events.
func (b *PostgreSQLPipelineBuilder) BuildNonDryRunNonFatalUnhealthyInsertsPipeline() datastore.Pipeline {
	return datastore.ToPipeline(
		datastore.D(
			datastore.E("$match", datastore.D(
				datastore.E("operationType", datastore.D(datastore.E("$in", datastore.A("insert", "update")))),
				datastore.E("fullDocument.healthevent.agent", datastore.D(datastore.E("$ne", "health-events-analyzer"))),
				datastore.E("fullDocument.healthevent.ishealthy", false),
				datastore.E("fullDocument.healthevent.dryrun", false),
			)),
		),
	)
}

``` 

---

### Step 5: Fault Quarantine Module

Update this module to skip processing dry-run events so that:
- Node is not cordoned
- Annotations and labels are not applied
- `nodeQuarantined` status is not set (remains null)

**Why skipping `nodeQuarantined` update matters:** fault-remediation and node-drainer module use pipeline filters that match on `nodeQuarantined` status. If this field is not set, their pipelines will exclude the event, and they won't see it.

File: `fault-quarantine/pkg/initializer/init.go`

Update the DB change stream pipeline to exclude dry-run events (so fault-quarantine never receives them):

```go
func InitializeAll(ctx context.Context, params InitializationParams) (*Components, error) {
	// ...rest of the code
	builder := client.GetPipelineBuilder()
	
	// call new created method which filters inserted events with dryRun false
	pipeline := builder.BuildNonDryRunHealthEventInsertsPipeline() 

	// ...rest of the code
}
```


**Note:** We don't need skip logic in node-drainer and fault-remediation modules as they won't receive these events. Since fault-quarantine won't act on dry-run events, it won't set `nodeQuarantined`, so downstream pipeline filters will exclude them.

---

### Step 6: Health Events Analyzer

Health Events Analyzer has two distinct dry-run concerns:

1. **Incoming/historic events**: if an incoming (or historical) health event has `dryRun=true`, it should be ignored
   by the analyzer for pattern detection (no queries/state transitions triggered by audit-only events).
2. **Analyzer’s own dry-run mode**: if the analyzer itself is running with `--dry-run`, then any aggregated health events
   it publishes must have `dryRun=true` enabled.

**Skip processing dry run enabled events in events-analyzer** 

Update the change-stream pipeline to exclude `dryRun=true` events, so the analyzer does not receive dry-run events for pattern detection.

File: `health-events-analyzer/main.go`

```go
func createPipeline() interface{} {
	builder := client.GetPipelineBuilder()
	// use new helper method which filter the fatal events with dryRun=false
	return builder.BuildNonDryRunNonFatalUnhealthyInsertsPipeline()
}
```

**Ignore dryRun marked event during mongo query**

Update the default pipeline query to exclude `dryRun=true` events. We need this condition for every rule that's why we are adding it at code level instead of keeping it at config file level.  

File: `health-events-analyzer/pkg/reconciler/reconciler.go`

```go
func (r *Reconciler) getPipelineStages(
	rule config.HealthEventsAnalyzerRule,
	healthEventWithStatus datamodels.HealthEventWithStatus,
) ([]map[string]interface{}, error) {
	// CRITICAL: Always start with agent filter to exclude events from health-events-analyzer itself
	// This prevents the analyzer from matching its own generated events, which would cause
	// infinite loops and incorrect rule evaluations
	pipeline := []map[string]interface{}{
		{
			"$match": map[string]interface{}{
				"healthevent.agent":  map[string]interface{}{"$ne": "health-events-analyzer"},
				"healthevent.dryrun": map[string]interface{}{"$eq": false}, // Add dryRun check by default for all queries
			},
		},
	}
}
```

**health-events-analyzer running in dryRun mode**

Update the publish function to set the `dryRun` field based on the mode that health-events-analyzer is running in.

File: `health-events-analyzer/pkg/publisher/publisher.go`

```go
func (p *PublisherConfig) Publish(ctx context.Context, event *protos.HealthEvent,
	recommendedAction protos.RecommendedAction, ruleName string, message string) error {
	newEvent := proto.Clone(event).(*protos.HealthEvent)

	newEvent.Agent = "health-events-analyzer"
	newEvent.CheckName = ruleName
	newEvent.RecommendedAction = recommendedAction
	newEvent.IsHealthy = false
	newEvent.Message = message

	newEvent.DryRun = p.dryRun // update dryRun flag

	// ...rest of the code
}
```

---

### Step 7: Configuration Changes

Health monitors need to set the `dryRun` flag based on configuration. The default value for dryRun will be false that means by default all health checks will run in standard mode where all clusters operations will be performed.

Add to Helm values.yaml (for each health monitor):

```yaml
syslogHealthMonitor:
    dryRun: true  # Set to true to make all checks run in dry run mode
cspHealthMonitor:
    dryRun: true  # Set to true to make all checks run in dry run mode
kubernetesObjectMonitor:
    dryRun: true  # Set to true to make all checks run in dry run mode
gpuHealthMonitor:
    dryRun: true  # Set to true to make all checks run in dry run mode
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