# Syslog Health Monitor Support for Pre-installed Drivers

## Problem Statement

NVSentinel's Syslog Health Monitor analyzes system logs to detect XID errors and GPUs falling off the bus. 

In standard deployments, NVIDIA GPU Operator installs and manages the GPU driver through `nvidia-driver-daemonset` pods. The NVSentinel labeler watches these driver pods to set the `nvsentinel.dgxc.nvidia.com/driver.installed=true` label on nodes, which the Syslog Health Monitor DaemonSet uses as a node selector to ensure deployment only on GPU nodes with properly installed drivers.

However, in certain cloud environments—particularly GKE with pre-installed drivers in machine images, the GPU Operator is configured to **NOT install the driver** (`driver.enabled=false`). Instead, it only installs the NVIDIA Container Toolkit, Device Plugin, and DCGM components. In these environments:

1. No `nvidia-driver-daemonset` pods exist
2. The labeler cannot detect driver installation
3. The `nvsentinel.dgxc.nvidia.com/driver.installed=true` label is never applied
4. Syslog Health Monitor pods fail to schedule on GPU nodes
5. XID detection and GPU error monitoring doesn't work

## Analysis and Findings

Investigation of GKE environments with pre-installed drivers revealed:

1. **GKE uses Google-managed driver installer pods**: In GKE Standard mode with the "[Google Driver Installer](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/google-gke.html#using-the-google-driver-installer)" approach, Google deploys `nvidia-driver-installer` DaemonSet pods in the `kube-system` namespace to install and manage GPU drivers at runtime.
   - Note: This is different from the "NVIDIA Driver Manager" approach where GPU Operator manages drivers via `nvidia-driver-daemonset`

2. **Consistent across standard GKE node image types**: Both major GKE node image types use driver installer pods:
   - **Container-Optimized OS (COS)**: Standard GKE image; drivers installed at runtime by [`nvidia-driver-installer`](https://github.com/GoogleCloudPlatform/container-engine-accelerators/blob/master/nvidia-driver-installer/cos/daemonset-preloaded.yaml) pods
   - **Ubuntu with containerd**: Standard GKE image; drivers installed at runtime by [`nvidia-driver-installer`](https://github.com/GoogleCloudPlatform/container-engine-accelerators/blob/master/nvidia-driver-installer/ubuntu/daemonset.yaml) pods

3. **Custom machine images with pre-baked drivers**: Some enterprises create custom GKE node images (based on GCP Accelerator-Optimized images, Deep Learning VMs, or customized standard images) where NVIDIA drivers are pre-installed during image creation. In these cases:
   - No driver installer pods run (drivers already in the image)
   - GPU Feature Discovery still detects functional drivers

4. **Pods confirm driver availability**: The presence of device plugin pods in Ready state on a node indicates that:
   - GPU drivers are installed and functional on that node
   - The node is ready for GPU workloads
   - Syslog monitoring can access driver logs

## Proposed Solution

We propose extending the labeler to detect when GPU drivers are installed on nodes, regardless of the installation method. Two approaches are evaluated below, with **Approach 2 (Container Toolkit Detection)** recommended for use.

### Approach 1: GKE Driver Installer Pod Detection

Extend the labeler to detect GKE driver installer pods as a fallback when operator-managed driver pods are not present.

**Detection tiers**:
- **Tier 1**: Watch for `nvidia-driver-daemonset` pods in `gpu-operator` namespace (existing behavior)
- **Tier 2**: Watch for `nvidia-driver-installer` pods in `kube-system` namespace

**What this approach detects**:
- Operator-managed driver installations (Tier 1)
- GKE Container-Optimized OS (COS) and Ubuntu images with runtime driver installation (Tier 2)

**Cons**:
- Doesn't handle custom machine images with pre-baked drivers (no installer pods run)
- Requires watching `kube-system` namespace (additional RBAC)
- GKE-specific detection logic

### Approach 2: Container Toolkit Pod Detection (Recommended)

Extend the labeler to watch for **NVIDIA Container Toolkit pods** as a universal indicator that drivers are installed.

**Detection tiers**:
- **Tier 1**: Watch for `nvidia-driver-daemonset` pods in `gpu-operator` namespace (existing behavior for operator-managed drivers)
- **Tier 2**: Watch for `nvidia-container-toolkit-daemonset` pods in `gpu-operator` namespace

**What this approach detects**:
- Operator-managed driver installations (Tier 1)
- Any environment where GPU Operator deploys Container Toolkit (Tier 2):
  - GKE COS/Ubuntu images with `nvidia-driver-installer`
  - Custom machine images with pre-baked drivers
  - Any other pre-installed driver scenario

---

**Why Container Toolkit over Other Detection Methods?**

Container Toolkit is the optimal detection mechanism because:

1. **Deploys After Driver Installation**: GPU Operator deploys Container Toolkit **only after** drivers are confirmed to be present:
   - In operator-managed scenarios: After `nvidia-driver-daemonset` completes
   - In pre-installed driver scenarios: GPU Operator detects existing drivers and deploys Container Toolkit
   
2. **Universal Across Installation Methods**: Container Toolkit is deployed by GPU Operator in **all driver scenarios**:
   - Operator-managed drivers (`driver.enabled=true`)
   - Pre-installed drivers (`driver.enabled=false`)
   - Custom machine images with pre-baked drivers
   - GKE COS/Ubuntu images (after `nvidia-driver-installer` completes)

3. **Container Toolkit Pod Running**: Indicates drivers are installed, GPU containers can be configured, syslog monitoring can access driver logs

**Approach 2 (Container Toolkit Detection)** is recommended approach due to the presence of container toolkit pods in all types of images.

### Detection Logic (Approach 2 - Container Toolkit)

```
Node with GPU
    ↓
Labeler checks for driver installation
    ↓
┌────────────────────────────────────────────┐
│ 1. Are operator driver pods present?      │
│    (nvidia-driver-daemonset)              │
│    [Operator-managed installation]        │
└────────────────────────────────────────────┘
    ↓ YES                        ↓ NO
    ↓                            ↓
Set label                 ┌─────────────────────────────────────────┐
driver.installed=true     │ 2. Is Container Toolkit pod Running?    │
                          │    (nvidia-container-toolkit-daemonset) │
                          │    [Indicates drivers present]          │
                          └─────────────────────────────────────────┘
                              ↓ YES              ↓ NO
                              ↓                  ↓
                       Set label          No label set
                       driver.installed   (No drivers
                       =true              detected)
```

**What each tier detects**:
- **Tier 1**: GPU Operator with `driver.enabled=true` - operator installs and manages drivers via `nvidia-driver-daemonset` pods
- **Tier 2**: Container Toolkit presence - GPU Operator deploys this **only after** drivers are present (works for ALL driver sources):
  * Operator-managed drivers
  * GKE installer-managed drivers (COS/Ubuntu)
  * Custom machine images with pre-baked drivers
  * Any pre-installed driver scenario

## Implementation (Approach 2 - Container Toolkit)

This section describes the implementation for the recommended Approach 2 using Container Toolkit pod detection.

### Labeler Code Changes

**File**: `labeler/pkg/labeler/labeler.go`

1. **Add Configuration for Container Toolkit Pods**:
   - Add new command-line flag: `--container-toolkit-app-label` (default: `nvidia-container-toolkit-daemonset`)
   - Add new command-line flag: `--container-toolkit-namespace` (default: `gpu-operator`)
   - Update `NewLabeler()` to accept these parameters and create pod informer for container toolkit pods

2. **Add Container Toolkit Pod Indexer**:
   ```go
   // Add to indexers in NewLabeler
   const NodeContainerToolkitIndex = "nodeContainerToolkit"
   
   NodeContainerToolkitIndex: func(obj any) ([]string, error) {
       pod, ok := obj.(*v1.Pod)
       if !ok {
           return nil, fmt.Errorf("object is not a pod")
       }
       if app, exists := pod.Labels["app"]; exists && app == containerToolkitApp {
           return []string{pod.Spec.NodeName}, nil
       }
       return []string{}, nil
   }
   ```

3. **Update `getDriverLabelForNode()` with Two-Tier Detection**:
   ```go
   func (l *Labeler) getDriverLabelForNode(nodeName string) (string, error) {
       // Tier 1: Check for operator-managed driver pods (existing logic)
       objs, err := l.podInformer.GetIndexer().ByIndex(NodeDriverIndex, nodeName)
       if err != nil {
           return "", fmt.Errorf("failed to get driver pods: %w", err)
       }
       
       for _, obj := range objs {
           pod, ok := obj.(*v1.Pod)
           if !ok {
               continue
           }
           if podutil.IsPodReady(pod) {
               slog.Debug("Found operator-managed driver pod", 
                   "node", nodeName, 
                   "pod", pod.Name,
                   "method", "tier1-operator-driver",
                   "note", "Driver installed by GPU Operator")
               return LabelValueTrue, nil
           }
       }
       
       // Tier 2: Check for Container Toolkit pods (indicates drivers present)
       containerToolkitObjs, err := l.containerToolkitInformer.GetIndexer().ByIndex(NodeContainerToolkitIndex, nodeName)
       if err != nil {
           return "", fmt.Errorf("failed to get container toolkit pods: %w", err)
       }
       
       for _, obj := range containerToolkitObjs {
           pod, ok := obj.(*v1.Pod)
           if !ok {
               continue
           }
           if podutil.IsPodReady(pod) {
               slog.Info("Found Container Toolkit pod (indicates drivers installed)", 
                   "node", nodeName, 
                   "pod", pod.Name, 
                   "namespace", pod.Namespace,
                   "method", "tier2-container-toolkit",
                   "note", "Container Toolkit presence indicates drivers are installed (operator/GKE/pre-baked)")
               return LabelValueTrue, nil
           }
       }
       
       slog.Debug("No driver installation detected for node", 
           "node", nodeName,
           "note", "Neither operator driver pods nor container toolkit pods are running")
       return "", nil
   }
   ```

4. **Add Event Handlers for Container Toolkit Pods**:
   - Register add/update/delete handlers for container toolkit pod informer
   - Trigger node label reconciliation when container toolkit pods change state
   - Monitor for pod readiness (indicates drivers and runtime configured)

5. **Handle Label Persistence**:
   - The `driver.installed` label persists once set, even if container toolkit pods are later deleted or crash
   - Label removal only happens when:
     * The node is deleted entirely
     * Operator explicitly uninstalls GPU components (driver daemonset removed)
   - Rationale: Driver installation is persistent; pod deletion/crashes don't uninstall drivers
   - **Critical for syslog monitoring**: We want to keep monitoring even if container toolkit pods restart

### Helm Chart Updates

**File**: `distros/kubernetes/nvsentinel/charts/labeler/values.yaml`

```yaml
labeler:
  # App label for operator-managed driver pods (Tier 1)
  driverAppLabel: "nvidia-driver-daemonset"
  
  # App label for Container Toolkit pods (Tier 2)
  containerToolkitAppLabel: "nvidia-container-toolkit-daemonset"
```

**Note**: Event handlers for Container Toolkit pods and operator driver pods should both trigger node label reconciliation.

### Syslog Health Monitor

**No changes required**:
- Continues to use `nvsentinel.dgxc.nvidia.com/driver.installed=true` node selector
- Works seamlessly with updated labeler logic

## Summary

The labeler will detect driver installation through a **two-tier detection mechanism** (Approach 2):

1. **Primary Method (Tier 1)**: Operator-managed `nvidia-driver-daemonset` pods in `gpu-operator` namespace
   - Standard GPU Operator installations with `driver.enabled=true`
   - Detects when operator installs drivers
   
2. **Secondary Method (Tier 2)**: NVIDIA Container Toolkit `nvidia-container-toolkit-daemonset` pods in `gpu-operator` namespace
   - Universal fallback that works for **all driver sources**:
     * Operator-managed drivers (after `nvidia-driver-daemonset` completes)
     * GKE installer-managed drivers (after `nvidia-driver-installer` in `kube-system` completes)
     * Custom machine images with pre-baked drivers (GPU Operator detects existing drivers and deploys Container Toolkit)
   - Indicates drivers are present and container runtime is configured

The `nvsentinel.dgxc.nvidia.com/driver.installed=true` label will be set when **either tier** detects driver installation.

## References

1. [NVIDIA GPU Operator - GKE with Google Driver Installer](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/google-gke.html#using-the-google-driver-installer)
2. [GPU Operator with Preinstalled Drivers Support](https://developer.nvidia.com/blog/adding-mig-preinstalled-drivers-and-more-to-nvidia-gpu-operator/)
3. [NVIDIA GPU Feature Discovery](https://github.com/NVIDIA/gpu-feature-discovery)
4. [GPU Operator Architecture](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/overview.html)
5. [NVIDIA Precompiled Driver Containers](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/precompiled-drivers.html)

