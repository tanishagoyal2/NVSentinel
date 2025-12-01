# Syslog Health Monitor Support for Pre-installed Drivers

## Problem Statement

NVSentinel's Syslog Health Monitor analyzes system logs to detect XID errors and GPUs falling off the bus. 

In standard deployments, NVIDIA GPU Operator installs and manages the GPU driver through `nvidia-driver-daemonset` pods. The NVSentinel labeler watches these driver pods to set the `nvsentinel.dgxc.nvidia.com/driver.installed=true` label on nodes, which the Syslog Health Monitor DaemonSet uses as a node selector to ensure deployment only on GPU nodes with properly installed drivers.

However, in certain cloud environments—particularly GKE with pre-installed drivers in machine images, the GPU Operator is configured to **NOT install the driver** (`driver.enabled=false`). Instead, it only installs the NVIDIA Container Toolkit, Device Plugin, and DCGM components. In these environments:

1. No `nvidia-driver-daemonset` pods exist
2. The labeler cannot detect driver installation
3. The `nvsentinel.dgxc.nvidia.com/driver.installed=true` label is never applied
4. Syslog Health Monitor pods fail to schedule on GPU nodes
5. XID detection and GPU error monitoring is doesn't work

## Analysis and Findings

Investigation of GKE environments with pre-installed drivers revealed:

1. **GKE uses Google-managed driver installer pods**: In GKE Standard mode with pre-installed drivers, Google deploys `nvidia-driver-installer` DaemonSet pods in the `kube-system` namespace to manage GPU drivers.

2. **Consistent across node image types**: Both major GKE node image types have driver installer pods:
   - **Container-Optimized OS (COS)**: Uses [`nvidia-driver-installer`](https://github.com/GoogleCloudPlatform/container-engine-accelerators/blob/master/nvidia-driver-installer/cos/daemonset-preloaded.yaml) with preloaded drivers in the node image
   - **Ubuntu with containerd**: Uses [`nvidia-driver-installer`](https://github.com/GoogleCloudPlatform/container-engine-accelerators/blob/master/nvidia-driver-installer/ubuntu/daemonset.yaml) to install and manage NVIDIA drivers

3. **Pods confirm driver availability**: The presence of these installer pods in Ready state on a node indicates that:
   - GPU drivers are installed and functional on that node
   - The node is ready for GPU workloads
   - Syslog monitoring can access driver logs

## Proposed Solution

Extend the labeler to detect cloud provider driver installer pods (specifically `nvidia-driver-installer` in GKE) as a fallback mechanism when operator-managed driver pods are not present. Continue setting the `nvsentinel.dgxc.nvidia.com/driver.installed=true` label for consistency with the existing NVSentinel labeling architecture.

### Approach

- Watch for `nvidia-driver-installer` pods in `kube-system` namespace
- If no operator-managed driver pods exist on a node, check for CSP driver installer pods
- Set `driver.installed=true` label when installer pods are found in Ready state
- Maintains consistency with NVSentinel's existing labeling architecture

### Detection Logic

```
Node with GPU
    ↓
Labeler checks for driver installation
    ↓
┌─────────────────────────────────────┐
│ Are operator driver pods present?  │
│ (nvidia-driver-daemonset)          │
└─────────────────────────────────────┘
    ↓ YES                    ↓ NO
    ↓                        ↓
Set label             ┌──────────────────────────────────┐
driver.installed=true │ Are CSP installer pods present? │
                      │ (nvidia-driver-installer)       │
                      └──────────────────────────────────┘
                          ↓ YES              ↓ NO
                          ↓                  ↓
                   Set label          No label set
                   driver.installed=true
```

## Implementation

### Labeler Code Changes

**File**: `labeler/pkg/labeler/labeler.go`

1. **Add Configuration for Driver Installer Pods**:

- Add new command-line flag: `--driver-installer-app-label` (default: `nvidia-driver-installer`)
- Add new command-line flag: `--driver-installer-namespace` (default: `kube-system`)
- Update `NewLabeler()` to accept these parameters and create pod informer for installer pods

2. **Add Driver Installer Pod Indexer**:
   ```go
   // Add to indexers in NewLabeler
   NodeDriverInstallerIndex: func(obj any) ([]string, error) {
       pod, ok := obj.(*v1.Pod)
       if !ok {
           return nil, fmt.Errorf("object is not a pod")
       }
       if app, exists := pod.Labels["nvidia-driver-installer"]; exists && app == driverInstallerApp {
           return []string{pod.Spec.NodeName}, nil
       }
       return []string{}, nil
   }
   ```

3. **Update `getDriverLabelForNode()` with Fallback Logic**:
   ```go
   func (l *Labeler) getDriverLabelForNode(nodeName string) (string, error) {
       // First, check for operator-managed driver pods (existing logic)
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
               slog.Debug("Found operator-managed driver pod", "node", nodeName, "pod", pod.Name)
               return LabelValueTrue, nil
           }
       }
       
       // Fallback: Check for cloud provider driver installer pods
       installerObjs, err := l.driverInstallerInformer.GetIndexer().ByIndex(NodeDriverInstallerIndex, nodeName)
       if err != nil {
           return "", fmt.Errorf("failed to get driver installer pods: %w", err)
       }
       
       for _, obj := range installerObjs {
           pod, ok := obj.(*v1.Pod)
           if !ok {
               continue
           }
           if podutil.IsPodReady(pod) {
               slog.Info("Found cloud provider driver installer pod", 
                   "node", nodeName, 
                   "pod", pod.Name, 
                   "namespace", pod.Namespace,
                   "method", "preinstalled-driver")
               return LabelValueTrue, nil
           }
       }
       
       return "", nil
   }
   ```

4. **Add Event Handlers for Driver Installer Pods**:
- Register add/update/delete handlers for driver installer pod informer
- Trigger node label reconciliation when installer pods change state

### Helm Chart Updates

**File**: `distros/kubernetes/nvsentinel/charts/labeler/values.yaml`
   ```yaml
   labeler:
     # App label for operator-managed driver pods
     driverAppLabel: "nvidia-driver-daemonset"
     
     # App label for cloud provider driver installer pods (GKE, etc.)
     driverInstallerAppLabel: "nvidia-driver-installer"
     driverInstallerNamespace: "kube-system"
   ```

### RBAC Updates

**File**: `distros/kubernetes/nvsentinel/charts/labeler/templates/clusterrole.yaml`
   ```yaml
   - apiGroups: [""]
     resources: ["pods"]
     verbs: ["get", "list", "watch"]
     # Add note: Watching pods in kube-system namespace for driver installer detection
   ```

### Syslog Health Monitor Configuration

**No changes required**:
- Continues to use `nvsentinel.dgxc.nvidia.com/driver.installed=true` node selector
- Works seamlessly with updated labeler logic

## Summary

The labeler will detect driver installation through:
1. **Primary Method**: Operator-managed `nvidia-driver-daemonset` pods (existing behavior)
2. **Fallback Method**: Cloud provider installer pods in `kube-system` namespace:
   - GKE COS: `nvidia-driver-installer` with [daemonset-preloaded.yaml](https://github.com/GoogleCloudPlatform/container-engine-accelerators/blob/master/nvidia-driver-installer/cos/daemonset-preloaded.yaml)
   - GKE Ubuntu: `nvidia-driver-installer` with [daemonset.yaml](https://github.com/GoogleCloudPlatform/container-engine-accelerators/blob/master/nvidia-driver-installer/ubuntu/daemonset.yaml)

The `nvsentinel.dgxc.nvidia.com/driver.installed=true` label will be set when:
- Pod is scheduled and in Ready state (passing readiness probes)
- Pod has the configured app label `k8s-app=nvidia-driver-installer`

## References

1. [NVIDIA GPU Operator - GKE Documentation](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/google-gke.html#using-the-google-driver-installer)
2. [Google Container Engine Accelerators - Driver Installer DaemonSets](https://github.com/GoogleCloudPlatform/container-engine-accelerators/tree/master/nvidia-driver-installer)
3. [NVIDIA GPU Feature Discovery Documentation](https://github.com/NVIDIA/gpu-feature-discovery)
4. [GPU Operator Architecture](https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/latest/overview.html)

