# Deploy MetalLB HA for Envoy Gateway

MetalLB provides a floating IP with automatic failover for the management cluster's Envoy Gateway ingress.

## Problem

Without MetalLB, the Envoy Gateway service uses a static `externalIPs` configuration. This approach:

- **No HA/failover** — if the node handling traffic goes down, the IP becomes unreachable
- **No proper LoadBalancer integration** — `externalIPs` bypasses kube-proxy load balancing
- **Manual configuration** — IP must be hardcoded in the service spec

## Architecture

```
                         Internet
                            │
                            ▼
                   X.X.X.X (floating IP)
                            │
           ┌────────────────┼────────────────┐
           │                │                │
      master-0          master-1        master-2
     (control-plane)   (control-plane)  (control-plane)
           │                │                │
           └────────────────┼────────────────┘
                            │
                   MetalLB L2 (ARP)
                   announces on public interface
                            │
                            ▼
                   Envoy Gateway Pod
                   (kube-proxy forwards)
```

### How It Works

1. MetalLB speaker runs as a DaemonSet on all control-plane nodes
2. One speaker "wins" the leader election for the floating IP
3. Winning speaker sends gratuitous ARP on the public interface to claim the IP
4. External traffic hits that node → kube-proxy → Envoy Gateway pod
5. If that node fails, another speaker takes over and sends new ARP

## Prerequisites

- Kubernetes cluster with control-plane nodes having public network access
- A subscribed/owned floating IP in the same L2 segment as the nodes
- Envoy Gateway deployed with a GatewayClass referencing an EnvoyProxy config

## Deployment

### 1. Install MetalLB via Helm

```bash
helm repo add metallb https://metallb.github.io/metallb
helm repo update
helm install metallb metallb/metallb \
  --namespace metallb-system \
  --create-namespace \
  --set loadBalancerClass=metallb \
  --wait
```

> **Critical**: The `loadBalancerClass=metallb` Helm value is required. See [Coexistence with kube-dc LB Controller](#coexistence-with-kube-dc-loadbalancer-controller).

### 2. Create IPAddressPool

```yaml
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: envoy-gateway-pool
  namespace: metallb-system
spec:
  addresses:
    - X.X.X.X/32  # Replace with your floating IP
  autoAssign: false
```

### 3. Create L2Advertisement

```yaml
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: envoy-gateway-l2
  namespace: metallb-system
spec:
  ipAddressPools:
    - envoy-gateway-pool
  interfaces:
    - ens3  # Replace with your public interface name
```

**Interface names by environment:**

| Environment | Interface | Notes |
|-------------|-----------|-------|
| CloudSigma (direct NIC) | `ens3` | Direct public NIC |
| Kube-OVN provider bridge | `br-ext-cloud` | OVS bridge for external network |

### 4. Update EnvoyProxy Configuration

`loadBalancerClass` must be set as a direct `envoyService` field, **not** in the StrategicMerge patch, because it is immutable once set on a Service.

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: custom-proxy-config
  namespace: envoy-gateway-system
spec:
  logging:
    level:
      default: warn
  provider:
    type: Kubernetes
    kubernetes:
      envoyService:
        externalTrafficPolicy: Cluster
        loadBalancerClass: metallb
        patch:
          type: StrategicMerge
          value:
            metadata:
              annotations:
                metallb.universe.tf/loadBalancerIPs: "X.X.X.X"
```

If updating an existing cluster, delete the Envoy Gateway service after changing the EnvoyProxy config — the Envoy Gateway controller will recreate it with the new `loadBalancerClass`:

```bash
kubectl delete svc -n envoy-gateway-system envoy-envoy-gateway-system-eg-<hash>
```

## Coexistence with kube-dc LoadBalancer Controller

MetalLB **must** be configured with `loadBalancerClass: metallb` to prevent interference with the kube-dc LoadBalancer controller.

### What happens without loadBalancerClass

- MetalLB tries to allocate IPs for **all** LoadBalancer services
- Fails with "no available IPs" for project services
- Blocks the kube-dc controller from setting external IPs via EIPs
- All project LoadBalancer services go to `<pending>`

### The fix has two parts

1. **Helm value** `loadBalancerClass: metallb` → adds `--lb-class=metallb` to MetalLB controller args
2. **EnvoyProxy** `envoyService.loadBalancerClass: metallb` → sets the field on the Envoy Gateway service at creation time

MetalLB then only handles services with `spec.loadBalancerClass: metallb`. All other LoadBalancer services are managed by the kube-dc controller via EIPs.

> **Note**: `spec.loadBalancerClass` is **immutable** on Kubernetes Services. It must be set at creation time via the EnvoyProxy `envoyService` field, not via StrategicMerge patch.

## Verification

**Check MetalLB pods:**
```bash
kubectl get pods -n metallb-system
# Expected: controller + speaker pods on each node
```

**Check IPAddressPool and L2Advertisement:**
```bash
kubectl get ipaddresspool,l2advertisement -n metallb-system
```

**Check Envoy Gateway service:**
```bash
kubectl get svc -n envoy-gateway-system
# Expected: EXTERNAL-IP shows X.X.X.X, loadBalancerClass: metallb
```

**Check MetalLB speaker logs:**
```bash
kubectl logs -n metallb-system -l app.kubernetes.io/component=speaker --tail=50 | grep serviceAnnounced
# Expected: "serviceAnnounced","ips":["X.X.X.X"],"protocol":"layer2"
```

**Test external connectivity:**
```bash
curl -v http://X.X.X.X
# Expected: HTTP response from Envoy Gateway
```

## Failover Testing

```bash
# Identify which node is announcing the IP
kubectl get pods -n metallb-system -l app.kubernetes.io/component=speaker -o wide

# Drain that node
kubectl drain <node-name> --ignore-daemonsets --delete-emptydir-data

# Watch for IP announcement to move
kubectl logs -n metallb-system -l app.kubernetes.io/component=speaker --tail=50 | grep serviceAnnounced

# Test connectivity still works
curl -v http://X.X.X.X

# Uncordon the node
kubectl uncordon <node-name>
```

## GitOps Integration (fleet)

The MetalLB HelmRelease itself is installed by Flux from the fleet. What you
add per cluster is the floating-IP pool + advertisement + the EnvoyProxy
patch that binds the front door to it — commit these under
`clusters/<name>/` in your fleet repo (plain manifests, no templating) so
they survive reconciles.

### IPAddressPool + L2Advertisement

```yaml
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata:
  name: envoy-gateway-pool
  namespace: metallb-system
spec:
  addresses:
    - 203.0.113.20/32          # your dedicated floating public IP
  autoAssign: false
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata:
  name: envoy-gateway-l2
  namespace: metallb-system
spec:
  ipAddressPools:
    - envoy-gateway-pool
  interfaces:
    - br-ext-cloud            # Kube-OVN provider bridge (or your L2 NIC)
```

### EnvoyProxy patch (bind the Gateway to the floating IP)

```yaml
apiVersion: gateway.envoyproxy.io/v1alpha1
kind: EnvoyProxy
metadata:
  name: custom-proxy-config
  namespace: envoy-gateway-system
spec:
  provider:
    type: Kubernetes
    kubernetes:
      envoyService:
        externalTrafficPolicy: Cluster
        loadBalancerClass: metallb
        patch:
          type: StrategicMerge
          value:
            metadata:
              annotations:
                metallb.universe.tf/loadBalancerIPs: 203.0.113.20
```

After Flux applies these, re-point the wildcard DNS (`*.example.com`) at the
floating IP so ingress fails over across control-plane nodes.

## CloudSigma Considerations

- **NIC Mode**: Master nodes' public NICs should be in **"manual" mode** via CloudSigma API, which allows traffic for all subscribed IPs on that NIC (including the floating IP). Without manual mode, CloudSigma may drop traffic for IPs not explicitly assigned.
- **IP Subscription**: The floating IP must be a subscribed (owned) CloudSigma IP.
- **L2 Segment**: For best results, the floating IP should be in the same L2 segment as the control-plane nodes.

## Rollback

If MetalLB doesn't work:
1. Uninstall MetalLB: `helm uninstall metallb -n metallb-system`
2. Re-apply `externalIPs` on the Envoy Gateway service
3. Consider using the CloudSigma CCM's LoadBalancer implementation instead
