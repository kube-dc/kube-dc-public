# Enabling Cloud Network Access for Management Cluster Components

## Problem Statement

The management cluster has two external networks available:
- **ext-public** (`168.119.17.48/28`) - Public internet-facing IPs on VLAN 4011
- **ext-cloud** (`100.65.0.0/16`) - Private cloud network on VLAN 4013

By default, pods running in the `ovn-default` subnet (e.g., `kamaji-system`, `kube-system`) cannot reach services exposed on the `ext-cloud` network, even though both subnets are within the same `ovn-cluster` VPC.

This causes issues when:
- Kamaji controller needs to connect to dedicated etcd datastores exposed via LoadBalancer on ext-cloud
- Any management component needs to access services on the cloud network

## Root Cause

The `ovn-default` subnet has `natOutgoing: true`, which means outbound traffic is NAT'd through the node's external interface (join network). However:

1. Traffic to `ext-cloud` (100.65.0.x) is routed via the physical VLAN 4013
2. Return traffic from ext-cloud doesn't know how to reach the OVN internal network (10.100.0.0/16)
3. No SNAT is configured for traffic from `ovn-default` to `ext-cloud`

## Solution

Three configurations were required on the `ovn-cluster` VPC:

### 1. Static Route to ext-cloud Gateway

```yaml
# Added to vpc/ovn-cluster spec.staticRoutes
- cidr: 100.65.0.0/16
  nextHopIP: 100.65.0.1  # ext-cloud gateway
  policy: policyDst
```

This tells the OVN router where to send traffic destined for ext-cloud.

### 2. Policy Route to Allow Traffic

```yaml
# Added to vpc/ovn-cluster spec.policyRoutes  
- action: allow
  match: ip4.dst == 100.65.0.0/16
  priority: 31000
```

Without this, traffic to ext-cloud would be dropped by default policies that only allow traffic to known internal subnets.

### 3. SNAT Rule for Return Traffic

```yaml
apiVersion: kubeovn.io/v1
kind: OvnSnatRule
metadata:
  name: ovn-cluster-to-ext-cloud
spec:
  ovnEip: ovn-cluster-ext-cloud  # Uses 100.65.0.101
  vpcSubnet: ovn-default
```

This NATs traffic from `10.100.0.0/16` to `100.65.0.101`, allowing return traffic to find its way back.

## Current Configuration

### VPC ovn-cluster

```yaml
apiVersion: kubeovn.io/v1
kind: Vpc
metadata:
  name: ovn-cluster
spec:
  enableExternal: true
  staticRoutes:
  - cidr: 100.65.0.0/16
    nextHopIP: 100.65.0.1
    policy: policyDst
  policyRoutes:
  - action: allow
    match: ip4.dst == 100.65.0.0/16
    priority: 31000
```

### OvnSnatRule

```yaml
apiVersion: kubeovn.io/v1
kind: OvnSnatRule
metadata:
  name: ovn-cluster-to-ext-cloud
spec:
  ovnEip: ovn-cluster-ext-cloud
  vpcSubnet: ovn-default
status:
  ready: true
  v4Eip: 100.65.0.101
  v4IpCidr: 10.100.0.0/16
  vpc: ovn-cluster
```

## Verification

Test connectivity from kamaji-system to ext-cloud:

```bash
# Create test service on ext-cloud
kubectl -n shalb-demo run nginx-test --image=nginx:alpine
kubectl -n shalb-demo expose pod nginx-test --port=8080 --target-port=80 --type=LoadBalancer \
  --dry-run=client -o yaml | \
  kubectl annotate -f - service.nlb.kube-dc.com/bind-on-eip=default-gw --local -o yaml | \
  kubectl apply -f -

# Test from kamaji-system
kubectl -n kamaji-system run test --image=busybox --rm -it --restart=Never -- \
  wget -qO- --timeout=5 http://100.65.0.102:8080

# Cleanup
kubectl -n shalb-demo delete pod nginx-test svc nginx-test
```

## OVN Commands for Debugging

```bash
# View routes on ovn-cluster router
kubectl ko nbctl lr-route-list ovn-cluster

# View policy routes
kubectl ko nbctl lr-policy-list ovn-cluster

# View NAT rules
kubectl ko nbctl lr-nat-list ovn-cluster

# Trace packet path
kubectl ko trace <namespace>/<pod> <dest-ip> tcp <port>

# View router ports
kubectl ko nbctl show ovn-cluster
```

## Project VPCs and ext-cloud Access

### Existing Project VPCs

Project VPCs (like `shalb-demo`, `shalb-dev`) are **isolated** from `ovn-cluster` VPC. They have their own configuration:

| VPC | ext-cloud Access | Configuration |
|-----|------------------|---------------|
| `shalb-dev` | ✅ Yes | `extraExternalSubnets: ["ext-cloud", "ext-public"]` |
| `shalb-demo` | ✅ Yes | Default gateway on ext-cloud (`100.65.0.1`) |
| `shalb-envoy` | ❌ No (public only) | `extraExternalSubnets: ["ext-public"]` |

### Creating New VPCs with ext-cloud Access

When creating a new project VPC that needs ext-cloud access:

#### Option 1: Set ext-cloud as Default Gateway (Recommended)

```yaml
apiVersion: kubeovn.io/v1
kind: Vpc
metadata:
  name: my-project
  annotations:
    network.kube-dc.com/default-gw-subnet-name: ext-cloud
spec:
  enableExternal: true
  namespaces:
  - my-project
  staticRoutes:
  - cidr: 0.0.0.0/0
    nextHopIP: 100.65.0.1  # ext-cloud gateway
    policy: policyDst
```

This routes all external traffic through ext-cloud.

#### Option 2: Add ext-cloud as Extra External Subnet

```yaml
apiVersion: kubeovn.io/v1
kind: Vpc
metadata:
  name: my-project
spec:
  enableExternal: true
  extraExternalSubnets:
  - ext-cloud
  - ext-public  # Optional: include both
  namespaces:
  - my-project
```

This allows the VPC to use both networks.

### Key Points for New VPCs

1. **No changes needed to ovn-cluster VPC** - The configuration above is global and allows `ovn-default` pods to reach ext-cloud
2. **Project VPCs are independent** - Each project VPC needs its own routing configuration
3. **SNAT is automatic** - When using EIP resources, SNAT is configured automatically
4. **extraExternalSubnets** - This setting allows the VPC to connect to external subnets on physical VLANs

## Network Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────┐
│                        Physical Network                          │
│  ┌──────────────────┐                 ┌──────────────────┐      │
│  │   VLAN 4011      │                 │   VLAN 4013      │      │
│  │   ext-public     │                 │   ext-cloud      │      │
│  │ 168.119.17.48/28 │                 │ 100.65.0.0/16    │      │
│  └────────┬─────────┘                 └────────┬─────────┘      │
└───────────┼─────────────────────────────────────┼───────────────┘
            │                                     │
┌───────────┴─────────────────────────────────────┴───────────────┐
│                       OVN Logical Network                        │
│                                                                  │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │                    ovn-cluster VPC                          │ │
│  │  ┌──────────────┐    ┌──────────────┐    ┌──────────────┐  │ │
│  │  │  ovn-default │    │   ext-cloud  │    │  ext-public  │  │ │
│  │  │10.100.0.0/16 │───▶│100.65.0.0/16 │    │168.119.17/28 │  │ │
│  │  │              │    │              │    │              │  │ │
│  │  │ kamaji-sys   │    │  SNAT via    │    │              │  │ │
│  │  │ kube-system  │    │ 100.65.0.101 │    │              │  │ │
│  │  └──────────────┘    └──────────────┘    └──────────────┘  │ │
│  └────────────────────────────────────────────────────────────┘ │
│                                                                  │
│  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐  │
│  │  shalb-dev VPC  │  │ shalb-demo VPC  │  │ shalb-envoy VPC │  │
│  │  10.1.0.0/16    │  │  10.0.10.0/24   │  │  10.0.40.0/24   │  │
│  │                 │  │                 │  │                 │  │
│  │ ext-cloud: ✅   │  │ ext-cloud: ✅   │  │ ext-cloud: ❌   │  │
│  │ ext-public: ✅  │  │ ext-public: ❌  │  │ ext-public: ✅  │  │
│  └─────────────────┘  └─────────────────┘  └─────────────────┘  │
└──────────────────────────────────────────────────────────────────┘
```

## Troubleshooting

### Symptom: Timeout when accessing ext-cloud from kamaji-system

1. **Check static route exists:**
   ```bash
   kubectl ko nbctl lr-route-list ovn-cluster | grep 100.65
   ```

2. **Check policy route allows traffic:**
   ```bash
   kubectl ko nbctl lr-policy-list ovn-cluster | grep 100.65
   ```

3. **Check SNAT rule exists:**
   ```bash
   kubectl ko nbctl lr-nat-list ovn-cluster
   kubectl get ovn-snat-rules ovn-cluster-to-ext-cloud
   ```

4. **Trace the packet:**
   ```bash
   kubectl ko trace kamaji-system/<pod-name> 100.65.0.102 tcp 8080
   ```

### Symptom: Project VPC cannot reach ext-cloud

1. **Check VPC has extraExternalSubnets or staticRoute:**
   ```bash
   kubectl get vpc <vpc-name> -o yaml
   ```

2. **Verify EIP and SNAT are configured:**
   ```bash
   kubectl get ovn-eip,ovn-snat-rules | grep <vpc-name>
   ```

## References

- [Kube-OVN VPC Documentation](https://kubeovn.github.io/docs/v1.12.x/en/guide/vpc/)
- [Kube-OVN External Gateway](https://kubeovn.github.io/docs/v1.12.x/en/advance/external-gateway/)
