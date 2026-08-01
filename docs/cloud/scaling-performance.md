# Scaling and Performance

Capacity planning starts with your workload, not a plan-name-to-user-count
formula. Request rate, cache behavior, database queries, payload size, and
latency targets can change resource needs by orders of magnitude. Kube-DC does
not assign a guaranteed number of users or requests per second to a plan.

Use this guide to measure a baseline, add headroom, and scale the correct layer.

## Start with the Effective Quota

An Organization's CPU, memory, storage, Pod, public IPv4, and object-storage
quota is shared by all its Projects. Open **Manage Organization -> Billing** for
the current limits and usage. Plan names and values can vary by installation.

A Project can have a smaller cap, but it cannot exceed the Organization's
remaining quota. Keep capacity for rollouts, failed-node recovery, certificate
solver Pods, Jobs, and temporary scaling.

See [Billing and Usage](billing-usage.md).

## Define the Target

Write down the workload objective before choosing a size:

- latency target, such as p95 response time
- sustained and peak request rate
- concurrent background jobs
- data size and expected growth
- recovery time and availability requirement
- acceptable CPU throttling and memory pressure

A production estimate should come from a representative load test. Treat a
development benchmark as a comparison point, not a capacity promise.

## Size Containers Deliberately

Resource **requests** reserve scheduling capacity and are the basis for CPU
autoscaling. **Limits** bound consumption; a container that exceeds its memory
limit can be restarted.

Begin with measured values, then inspect actual use:

```bash
kubectl top pods
kubectl get pods
kubectl describe pod <pod-name>
```

Look for sustained CPU near the request, memory growth, restarts, throttling,
and Pods that remain Pending. Adjust one variable at a time and repeat the same
test.

The Project LimitRange supplies defaults when a container omits resources, but
those defaults are a safety net rather than workload sizing.

## Horizontal Pod Autoscaling

Projects support the HorizontalPodAutoscaler. HPA is not created automatically
for every Helm chart; define it for a stateless workload after setting realistic
resource requests.

```yaml
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata:
  name: api
  namespace: acme-production
spec:
  scaleTargetRef:
    apiVersion: apps/v1
    kind: Deployment
    name: api
  minReplicas: 2
  maxReplicas: 6
  metrics:
  - type: Resource
    resource:
      name: cpu
      target:
        type: Utilization
        averageUtilization: 70
```

HPA can add replicas only while the Organization and Project have quota. It
also cannot repair a bottleneck in a database, storage path, external API, or
single-threaded application.

:::tip Test the whole scaling path
Generate a controlled load, watch HPA and Pod readiness, then confirm the
Gateway or LoadBalancer routes traffic to the new replicas. Also test scale-down
behavior and connection draining.
:::

## Keep Applications Horizontally Scalable

Replicas help only when they can serve independently:

- keep session state outside the Pod
- make Jobs idempotent
- use readiness probes that represent real serving health
- spread replicas when the storage and topology allow it
- define a PodDisruptionBudget for applications that need controlled eviction
- use image digests and predictable startup times

A ReadWriteOnce volume can constrain replicas to one node. Choose storage and
application architecture together.

## Size the Data Layer Separately

A managed database does not scale automatically with an application Deployment.
Measure query latency, connections, working-set memory, storage growth, and
backup duration. Add replicas for the availability model described in
[Managed Databases](managed-databases.md); one replica is not highly available.

For application files, decide whether block storage or
[Object Storage](object-storage.md) matches the access pattern. Storage class,
volume mode, and access mode affect both performance and placement.

## Virtual Machines

For a VM, measure guest CPU, memory pressure, disk latency, and network
throughput. Increasing vCPU or memory consumes Organization quota and may
require a restart. Keep enough capacity to reschedule important VMs after host
maintenance.

Use [VM Lifecycle](vm-lifecycle.md) for supported resize and restart behavior.

## Managed Clusters

Managed Cluster control planes and workers consume the parent Project's quota.
Worker autoscaling can add a node only when the parent Project has capacity for
the worker VM. Application availability during upgrades or worker replacement
still depends on replicas, disruption budgets, and storage topology.

See [Cluster Management](cluster-management.md).

## Network Performance

Use a Gateway route for HTTP or HTTPS and a LoadBalancer Service for selected
TCP or UDP ports. A public IP does not increase application throughput by
itself, and multiple public IPs do not remove an application bottleneck.

Measure from the client path that matters, including TLS, DNS, payload size, and
upstream dependencies. See [Service Exposure](service-exposure.md).

## A Repeatable Test Loop

1. Record the current manifest, image digest, dataset, and quota.
2. Warm the application if production traffic will hit a warm cache.
3. Increase load gradually and hold each level long enough to stabilize.
4. Capture latency, errors, saturation, restarts, database metrics, and storage latency.
5. Find the first limiting layer and change only that layer.
6. Repeat the test and document the new safe operating point.
7. Reserve rollout and failure headroom below the measured maximum.

## Production Checklist

- Capacity is based on a reproducible test, not a generic user estimate.
- Requests, limits, probes, and HPA behavior are verified.
- The database and storage path were included in the test.
- Quota includes rollout and recovery headroom.
- At least one failure or restart scenario was exercised.
- Backup restore time fits the recovery objective.
- Alerts are tied to an owner and an action.

## Next Steps

- [Deploy Your First Application](deploy-first-app.md)
- [Billing and Usage](billing-usage.md)
- [Managed Databases](managed-databases.md)
- [Data Protection and Recovery](backups-snapshots.md)
