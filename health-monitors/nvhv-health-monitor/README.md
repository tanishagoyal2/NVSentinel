# nvhv-health-monitor

Polls NVHV (`health-validation serve`, loopback only) for a node verdict over
gRPC and publishes NVSentinel health events. See ADR-076
(`docs/designs/076-integration-with-nvhv.md`).

## Behavior

- Every `--poll-interval` (default 15s) it calls `VerdictService/Evaluate`.
- Each non-PASS, non-UNKNOWN check result becomes an **unhealthy** event with
  processing strategy `STORE_ONLY`. No healthy/recovery events are sent, so the
  events are for observability and never drive remediation.
- It remembers what it reported and publishes only new or changed faults. A
  missing or UNKNOWN check never clears a fault; a PASS result forgets it
  locally so it is reported again if it returns.
- A failed publish leaves the record unchanged; the next poll retries.
- If NVHV stays unreachable for `--unreachable-threshold` (default 5m) it sends
  one fatal `NVHVUnreachable` event (`CONTACT_SUPPORT`), once per outage.

Events go through the shared `healthpub` client: the node-local platform
connector socket, or a central connector when `HEALTH_PUBLISH_TARGET` is set.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--nvhv-address` | `127.0.0.1:18090` | NVHV gRPC address |
| `--poll-interval` | `15s` | Poll interval |
| `--request-timeout` | poll interval | Timeout of one NVHV call; may not exceed the poll interval |
| `--unreachable-threshold` | `5m` | Outage length before `NVHVUnreachable` |
| `--node-name` | `$NODE_NAME` | Node name |
| `--metrics-port` | `2112` | Prometheus `/metrics` and `/healthz` port |
| `--platform-connector-socket` | `unix:///var/run/nvsentinel.sock` | Platform connector target |
| `--platform-connector-token-path` | empty | Projected ServiceAccount token |

## Metrics

| Metric | Meaning |
|---|---|
| `nvhv_health_monitor_unreachable{node}` | 1 while calls to NVHV fail, 0 once it answers; alert on it staying 1 |
| `nvhv_health_monitor_evaluate_failures_total{node,code}` | Failed `Evaluate` calls by gRPC status code |
| `nvhv_health_monitor_events_published_total{node,check,is_fatal}` | Events delivered to the platform connector |
| `nvhv_health_monitor_publish_failures_total{node}` | Event batches the platform connector did not accept |

`/healthz` fails only if the poll loop stops completing polls; NVHV being down
does not fail it.

## Development

```bash
make -C health-monitors/nvhv-health-monitor proto   # regenerate pkg/verdictpb
cd health-monitors/nvhv-health-monitor && go test ./...
```

`proto/health/v1/verdict.proto` is a copy of NVHV's verdict proto; update it
together with the generated code when NVHV's API changes.
