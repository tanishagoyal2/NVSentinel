# Implementation Plan: WORKFLOW_XID_13 and WORKFLOW_XID_31

## Problem Statement
XID 13 and 31 are non-fatal and were getting ignored. We need to make our system smart enough to make decisions based on the history of events. We already have WORKFLOW_XID_13 and WORKFLOW_XID_31 defined as investigatory actions for XID 13 and 31 but not implemented yet in our application. In this document we have mentioned all the changes that would be required to implement these workflows.

## XID 13
XID 13 (Graphics Engine Exception) logged for general user application faults. Typically this is an out-of-bounds error where the user has walked past the end of an array, but could also be an illegal instruction, illegal register, or other case.errors.

It can indicate different root causes depending on:
- Whether they repeat on the same hardware components (GPC/TPC/SM)
- Whether they occur in isolation or as part of a burst

A single recommended action is insufficient - we need to have context-aware decision making.

Workflow XID 13 is defined like this: 

```
Repeat TPC and GPC, diff SMs: RUN_DCGMEUD (possible HW issue); if pass RUN_FIELDDIAGS
Repeat TPC and GPC, single SM: RUN_DCGMEUD (possible HW issue); if pass RUN_FIELDDIAGS
Solo, no burst: CHECK_APP/CUDA
Not Repeat TPC and GPC: CHECK_APP/CUDA
Non-prod environment: CHECK_APP/CUDA
If known good APP and Solo: REPORT_ISSUE
```

We need to implement the following decision logic for XID 13 errors:

| Scenario | Recommended Action | Rationale |
|----------|-------------------|-----------|
| **Repeat TPC and GPC**| `RUN_DCGMEUD` (if pass: `RUN_FIELDDIAGS`) | Possible HW issue |
| **Solo, no burst** | `CHECK_APP_CUDA` | Single occurrence, likely transient |
| **Not Repeat TPC and GPC** | `CHECK_APP_CUDA` | Different locations, likely app issue |

### XID 13 Message Format

#### With Hardware Location (GPC/TPC/SM):
```
NVRM: Xid (PCI:0000:b5:00): 13, pid='<unknown>', name=<unknown>, Graphics SM Warp Exception on (GPC 1, TPC 3, SM 0): Out Of Range Address
```

#### Without Hardware Location:
```
NVRM: Xid (PCI:0002:00:00): 13, pid=2519562, name=python3, Graphics Exception: ChID 000c, Class 0000cbc0, Offset 00000000, Data 00000000
```
---

## XID 31

XID 31 (MMU Fault) errors indicate memory management issues. This event is logged when a fault is reported by the MMU, such as when an illegal address access is made by an applicable unit on the chip. Typically these are application-level bugs, but can also be driver bugs or hardware bugs.

The decision logic is based on:
- Whether the same physical GPU (tracked by GPU_UUID) experiences repeated MMU faults
- Whether errors occur across different GPUs (indicating app/CUDA issues)
- Whether it's an isolated occurrence or part of a pattern

Workflow XID 31 is defined like this:

```
Multiple runs needed to establish pattern
Repeat MMU faults to same GPU (via PCI-ID): RUN_DCGMEUD (possible HW issue); if pass RUN_FIELDDIAGS
Repeat MMU faults to diff GPU (via PCI-ID): CHECK_APP/CUDA
Solo, no burst: CHECK_APP/CUDA
If known good APP: REPORT_ISSUE
```

We need to implement the following decision logic for XID 31 errors:

| Scenario | Recommended Action | Rationale |
|----------|-------------------|-----------|
| **Repeat on same GPU** | `RUN_DCGMEUD` (if pass: `RUN_FIELDDIAGS`) | Same physical GPU = HW issue |
| **Repeat on different GPUs** | `CHECK_APP_CUDA` | Multiple GPUs affected = app issue |
| **Solo, no burst** | `CHECK_APP/CUDA` | Single occurrence, likely transient |

### XID 31 Message Formats

#### MMU Fault with Engine Details
```
NVRM: Xid (PCI:0000:b5:00): 31, pid=2079991, name=pt_main_thread, Ch 00000007, intr 00000000. 
MMU Fault: ENGINE GRAPHICS GPCCLIENT_T1_6 faulted @ 0x7f5a_e7504000. 
Fault is of type FAULT_PDE ACCESS_TYPE_VIRT_READ
```

**To determine if an MMU fault occurred on the same GPU, we can track the GPU_UUID which is added by the syslog health monitor to the health event.**

---

## Architecture Decision

We will split the implementation across two modules:

**Syslog Health Monitor:** Fetches GPC, TPC and SM info required for XID 13

**Health Events Analyzer:** Provides the correct recommended action by checking the history of XIDs

```
┌────────────────────────────────────────────────────────────┐
│  Syslog Health Monitor                                     │
│  Role: Detection & Data Extraction                         │
│  - Parse XID 13 from journal                               │
│  - Extract: PCI, GPC, TPC, SM from message                 │
│  - Send raw HealthEvent with metadata containing           |
|      TPC, GPC AND SM│                                      |
│  - NO decision logic or history tracking                   │
└────────────────┬───────────────────────────────────────────┘
                 │ gRPC
                 ↓
┌────────────────────────────────────────────────────────────┐
│  Platform Connector                                        │
│  - Store in MongoDB                                        │
└────────────────┬───────────────────────────────────────────┘
                 │ MongoDB Change Stream
                 ↓
┌────────────────────────────────────────────────────────────┐
│  Health Events Analyzer                                    │
│  Role: Pattern Analysis & Intelligent Action               │
│  - Watch for XID 13 events                                 │
│  - Query MongoDB for historical patterns                   │
│  - Analyze: Repeat vs Solo, Same HW vs Different HW        │
│  - Generate correlated event with smart action             │
└────────────────────────────────────────────────────────────┘
```
---

## Decision Tree

### XID 13

```
┌─────────────────────────────────────────┐
│ New XID 13 Event Arrives                │
│ Example: GPC:0, TPC:1, SM:0             │
└─────────────────┬───────────────────────┘
                  │
                  ↓
      ┌───────────────────────────┐
      │ Query last 24 hour of      │
      │ XID 13 events on same GPU │
      └───────────┬───────────────┘
                  │
                  ↓
                  │
      ┌───────────────────────────────┐
      │ Decision 1:                   │
      │ Check if occurred with        │
      │ SAME GPC and TPC              │
      └───────┬───────────────────────┘
              │
              └─ Found >=2 repeats? ─→ RUN_FIELDDIAG
              |
      ┌───────────────────────────────┐
      │ Decision 2:                   │
      │ Check if occurred with        │
      │ DIFFERENT GPC and TPC         │
      └───────┬───────────────────────┘
              │
              ├─ Found >=2 repeats? ─→ CHECK_APP_CUDA
              │ 
              │
              ↓ No repeats on different GPC/TPC
              │
      ┌───────────────────────────────┐
      │ Decision 3:                   │
      │ Check if 13 occurred one time │
      │ in latest burst               │
      └───────┬───────────────────────┘
              │
              ├─ Found 1 count in latest burst? ─→ CHECK_APP_CUDA

```

### XID 31

```
┌─────────────────────────────────────────┐
│ New XID 31 Event Arrives                │
│ GPU_UUID: xyz, PCI: abc                 │
└─────────────────┬───────────────────────┘
                  │
                  ↓
      ┌───────────────────────────┐
      │ Query last 24 hour of      │
      │ XID 31 events             │
      └───────────┬───────────────┘
                  │
                  ↓
      ┌───────────────────────────────┐
      │ Decision 1:                   │
      │ Check if repeated on          │
      │ same GPU_UUID                 │
      └───────┬───────────────────────┘
              │
              ├─ Found >=2 repeats? ─→ RUN_DCGMEUD
              │  
              |
      ┌───────────────────────────────┐
      │ Decision 2:                   │
      │ Check if repeated on          │
      │ same GPU_UUID                 │
      └───────┬───────────────────────┘
              │
              ├─ Found >=2 repeats? ─→ CHECK_APP_CUDA
              │  
              │
              ↓ Continue to burst check
              │
      ┌───────────────────────────────┐
      │ Decision 2:                   │
      │ Check if 31 occurred one time │
      │ in latest burst               │
      └───────┬───────────────────────┘
              │
              └─ Found 1 count in latest burst? ─→ CHECK_APP_CUDA
                 
```


## Part 1: Syslog Health Monitor Changes

### Responsibility
**Extract and forward GPC/TPC/SM information and store it in Health Event metadata**

---

### 1.1 Extend Parser Data Structure

**File:** `/health-monitors/syslog-health-monitor/pkg/xid/parser/parser.go`

**Change:** Add metadata field to `XIDDetails` struct to store hardware location details like GPC and TPC

```go
type XIDDetails struct {
	Context             string `json:"context"`
	DecodedXIDStr       string `json:"decoded_xid_string"`
	Driver              string `json:"driver"`
	InvestigatoryAction string `json:"investigatory_action"`
	Machine             string `json:"machine"`
	Mnemonic            string `json:"mnemonic"`
	Name                string `json:"name"`
	Number              int    `json:"number"`
	PCIE                string `json:"pcie_bdf"`
	Resolution          string `json:"resolution"`

	// Add new metadata field to extract extra info like hardware location
	Metadata            map[string]string `json:"metadata"`
}
```
---

### 1.2 Parse GPC/TPC/SM from API Response
We need to update the parsing logic to fetch GPC, TPC and SM info and add them to XIDDetails metadata

**File:** `/health-monitors/syslog-health-monitor/pkg/xid/parser/sidecar.go`

**Change:** Extract hardware location from the `context` field after API response

```go
func (p *SidecarParser) Parse(message string) (*Response, error) {
	// ... existing API call and unmarshal code ...
	
	err = json.Unmarshal(bodyBytes, &xidResp)
	if err != nil {
		slog.Error("Error decoding XID response", "error", err.Error())
		metrics.XidProcessingErrors.WithLabelValues("response_decoding_error", p.nodeName).Inc()
		return nil, fmt.Errorf("error decoding xid response: %w", err)
	}

	// NEW: Extract GPC/TPC/SM from context if present
	if xidResp.Success && xidResp.Result.Context != "" {
		gpc, tpc, sm := extractHardwareLocation(xidResp.Result.Context)
		if gpc != "" && tpc != "" && sm != "" {
			xidResp.Result.Metadata = map[string]string{
				"GPC": gpc,
				"TPC": tpc,
				"SM":  sm,
			}
		}
	}

	return &xidResp, nil
}
```

**Add new helper function:**

```go
// extractHardwareLocation extracts GPC, TPC, SM values from XID message
// Example: "(GPC 1, TPC 3, SM 0)" -> gpc="1", tpc="3", sm="0"
// Returns empty strings if pattern not found
func extractHardwareLocation(message string) (gpc, tpc, sm string) {
	// Regex pattern: (GPC 1, TPC 3, SM 0)
	re := regexp.MustCompile(`\(GPC\s+(\d+),\s*TPC\s+(\d+),\s*SM\s+(\d+)\)`)
	matches := re.FindStringSubmatch(message)
	
	if len(matches) != 4 {
		return "", "", ""
	}
	
	return matches[1], matches[2], matches[3]
}
```

---

### 1.3 Include GPC/TPC/SM in HealthEvent Metadata

**File:** `/health-monitors/syslog-health-monitor/pkg/xid/xid_handler.go`

**Change:** Add GPC/TPC/SM to the metadata map in `createHealthEventFromResponse` method

```go
func (xidHandler *XIDHandler) createHealthEventFromResponse(
	xidResp *parser.Response, 
	originalMessage string,
) *pb.HealthEvents {
	normPCI := xidHandler.normalizePCI(xidResp.Result.PCIE)
	
	// ... existing code to get GPU UUID, etc. ...
	
	metadata := make(map[string]string)
	metadata["pci_address"] = xidResp.Result.PCIE
	metadata["xid_code"] = fmt.Sprintf("%d", xidResp.Result.Number)
	metadata["decoded_xid"] = xidResp.Result.DecodedXIDStr
	metadata["mnemonic"] = xidResp.Result.Mnemonic
	
	if gpuUUID != "" {
		metadata["gpu_uuid"] = gpuUUID
	}
	
	// NEW: Add hardware location information
	metadata["gpc"] = xidResp.Result.Metadata["GPC"]  
	metadata["tpc"] = xidResp.Result.Metadata["TPC"]
	metadata["sm"] = xidResp.Result.Metadata["SM"]
	
	// ... rest of the method ...
}
```

For the XID 13 journal entry:
```
NVRM: Xid (PCI:0009:01:00): 13, Graphics SM Warp Exception on (GPC 0, TPC 3, SM 0): Out Of Range Address
```

The HealthEvent will have data like this:

```javascript
{
  _id: ObjectId("..."),
  healthevent: {
    errorcode: ["13"],
    checkName: "SysLogsXIDError",
    entitiesimpacted: [
      {entitytype: "PCI", entityvalue: "0009:01:00"},
      {entitytype: "GPU_UUID", entityvalue: "GPU-abc123"}
    ],
    metadata: {
      "GPC": "0",
      "TPC": "3",
      "SM": "0"
    },
    generatedtimestamp: {seconds: 1699900000},
    recommendedAction: "RESTART_APP"
  }
}
```
---

## Part 2: Health Events Analyzer Changes

### Responsibility
**Analyze XID 13 patterns and determine intelligent recommended actions based on the history of events**

### 2.1 Create XID 13 Analysis Rules
We need to add 3 new rules in the health-events-analyzer config to handle the following scenarios and provide better investigatory actions:

| Pipeline Condition Check | Recommended Action |
|--------------------------|-------------------|
| **Repeat TPC and GPC** | `RUN_FIELDDIAG` |
| **Solo, no burst** | `CHECK_APP_CUDA` |
| **Not Repeat TPC and GPC** | `CHECK_APP_CUDA` |


### 2.2 Create XID 31 Analysis Rules

We need to add 3 new rules in the health-events-analyzer config to handle the following scenarios and provide better investigatory actions:

| Pipeline Condition Check | Recommended Action |
|--------------------------|-------------------|
| **Repeat on Same GPU** | `RUN_DCGMEUD` |
| **Solo, no burst** | `CHECK_APP_CUDA` |
| **Repeat on diff GPU** | `CHECK_APP_CUDA` |

**Since "solo, no burst" is common for both XIDs, we can define a single rule for them. Therefore, a total of 5 new rules will be added to the health-events-analyzer.**

---

## Platform Connector Changes

### Add New Recommended Action Enum
We are currently handling the following action enums:

```go
RecommendedAction_NONE            RecommendedAction = 0
RecommendedAction_COMPONENT_RESET RecommendedAction = 2
RecommendedAction_CONTACT_SUPPORT RecommendedAction = 5
RecommendedAction_RESTART_VM      RecommendedAction = 15
RecommendedAction_RESTART_BM      RecommendedAction = 24
RecommendedAction_REPLACE_VM      RecommendedAction = 25
RecommendedAction_UNKNOWN         RecommendedAction = 99
```
With the implementation of WORKFLOW_XID_13 and WORKFLOW_XID_31, we need to add the following new action enums:

```go
RecommendedAction_CHECK_APP_CUDA       RecommendedAction = 26
RecommendedAction_RUN_FIELDDIAG        RecommendedAction = 6
RecommendedAction_RUN_DCGMEUD          RecommendedAction = 27
```
---
