# Runbook: MongoDB Connection Error

## Overview

MongoDB is used to persist health events that are created by health monitors like syslog-health-monitor, gpu-health-monitor etc for 30 days. The fault-handling modules(like fault-quarantine, node-drainer, fault-remediation) connect to MongoDB to process these events and cordon/uncordon/remediate the node. When MongoDB pods crash or fail to start, all event processing in NVSentinel stops.

## Common Causes

MongoDB connection failure can occur due to the following reasons:

### 1. **TLS Certificates Not Installed or Expired**
   - Missing `mongodb-tls-secret` or `mongo-app-client-cert-secret`
   - Expired certificates

### 2. **Initialization Job Not Completed**
   - `create-mongodb-database` job failed or still running
   - Job creates required database, collections, and indexes
   - Without successful completion, MongoDB pod can't initialize properly

### 3. **Persistent Volume Issues**
   - PersistentVolumeClaim (PVC) not bound
   - Storage class not available
   - Disk full on the underlying storage
   - Volume mount failures

### 4. **Resource Constraints**
   - Out of Memory (OOM) - pod killed by kubelet
   - Node running out of resources

## Quick Diagnosis

Run these commands to identify the issue:

```bash
# 1. Check pod status
kubectl -n nvsentinel get pods -l app.kubernetes.io/name=mongodb

# Common statuses and their meanings:
# - Pending → Scheduling issue (resources, node selector, tolerations)
# - ImagePullBackOff → Can't pull the image
# - CrashLoopBackOff → Pod starts but crashes repeatedly
# - Error → Container exited with non-zero code
# - OOMKilled → Ran out of memory
# - Init:Error → Init container failed

# 2. Check pod events for errors
kubectl -n nvsentinel describe pod -l app.kubernetes.io/name=mongodb | grep -A 20 "Events:"

# 3. Check pod logs
kubectl -n nvsentinel logs -l app.kubernetes.io/name=mongodb --tail=100

# 4. Check PVC status
kubectl -n nvsentinel get pvc | grep mongodb

# 5. Check if initialization job completed
kubectl -n nvsentinel get jobs | grep mongodb
kubectl -n nvsentinel get jobs create-mongodb-database -o yaml | grep -A 5 "status:"

# 6. Check certificates exist
kubectl -n nvsentinel get secrets | grep mongo

# 7. Check StatefulSet status
kubectl -n nvsentinel get statefulset mongodb -o wide
```

## Detailed Troubleshooting

### Issue 1: TLS Certificate Problems

**Diagnosis:**
```bash
# Check if secrets exist
kubectl -n nvsentinel get secret mongodb-tls-secret -o yaml
kubectl -n nvsentinel get secret mongo-app-client-cert-secret -o yaml

# Check certificate expiration
kubectl -n nvsentinel get secret mongodb-tls-secret -o jsonpath='{.data.tls\.crt}' | base64 -d | openssl x509 -noout -dates

# Check logs for certificate errors
kubectl -n nvsentinel logs -l app.kubernetes.io/name=mongodb | grep -i "certificate\|tls\|ssl"
```

**Solution:**
```bash
# check certificate resources
kubectl -n nvsentinel get certificates

# Delete and recreate the pod to use new certs
kubectl -n nvsentinel delete pod -l app.kubernetes.io/name=mongodb
```

### Issue 2: Initialization Job Failed

**Diagnosis:**
```bash
# Check job status
kubectl -n nvsentinel get job create-mongodb-database

# Check job logs
kubectl -n nvsentinel logs job/create-mongodb-database

# Check if job completed successfully
kubectl -n nvsentinel get job create-mongodb-database -o jsonpath='{.status.succeeded}'
# Should return "1" if successful
```

**Solution:**
```bash
# If job failed, delete and re-run the job
kubectl get job create-mongodb-database -n nvsentinel -o yaml > create-mongodb-database.yaml
kubectl delete job -n nvsentinel create-mongodb-database
kubectl apply -f create-mongodb-database.yaml
```

### Issue 3: Persistent Volume Issues

**Diagnosis:**
```bash
# Check PVC status
kubectl -n nvsentinel get pvc | grep mongodb
# Status should be "Bound", not "Pending"

# Check PVC details
kubectl -n nvsentinel describe pvc -l app.kubernetes.io/name=mongodb

# Check if storage class exists
kubectl get storageclass

# Check PV details
kubectl get pv | grep mongodb
```

**Solution:**
```bash
# If PVC is Pending, check storage class availability
kubectl describe storageclass standard-rwo

# If disk is full, consider expanding the PVC
kubectl -n nvsentinel edit pvc datadir-mongodb-0
# Increase storage size 
```

### Issue 4: Out of Memory (OOM)

**Diagnosis:**
```bash
# Check if pod was OOMKilled
kubectl -n nvsentinel get pod -l app.kubernetes.io/name=mongodb -o jsonpath='{.items[*].status.containerStatuses[*].lastState.terminated.reason}'

# Check memory limits
kubectl -n nvsentinel get statefulset mongodb -o yaml | grep -A 5 "resources:"

# Check node memory
kubectl top nodes
```

**Solution:**
```bash
# Increase memory limits
kubectl -n nvsentinel edit statefulset mongodb
# Update resources.limits.memory to higher value (e.g., 2Gi → 4Gi)
```
