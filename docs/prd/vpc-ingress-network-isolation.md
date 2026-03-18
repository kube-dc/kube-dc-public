# VPC Ingress Network Isolation for Private-Link Clusters

## Status: Design (2026-03-17)
## Date: 2026-03-17
## Author: Auto-generated from architecture analysis + live testing

---

## 1. Problem Statement

### 1.1 Security Issue

In private-link cluster deployments, worker nodes connect to the management cluster's control plane via a **shared cloud VLAN** (e.g., `ext-cloud` subnet `100.64.0.0/16`). This VLAN is shared across all customer accounts — any device on the cloud VLAN can reach **any project's VPC** through its EIP.

**Concrete example:**
```
# Worker in customer-A's account (100.64.128.2) can reach customer-B's kube-apiserver:
curl -k https://100.64.0.22:6443/api/version
# Successfully reaches customer-B's control plane EIP
```

This is a **multi-tenant isolation violation** — a compromised or malicious worker in one customer's account could probe or attack control planes belonging to other customers on the same cloud VLAN.

### 1.2 Root Cause

All project VPCs share the same `ext-cloud` subnet for EIP allocation. The cloud VLAN is a flat L2 broadcast domain — every device on it can reach every EIP. There are **no inbound access controls** on the VPC routers filtering traffic by source IP.

Traffic flow (currently allowed, should be blocked):
```
Customer-A worker (100.64.128.2, cloud VLAN)
  → Customer-B's kube-apiserver EIP (100.64.0.22, ext-cloud)
  → DNAT → Customer-B's VPC → kube-apiserver pod
```

The existing egress firewall (PRD: `vpc-egress-network-isolation.md`) blocks **outbound** cross-project traffic from pods. But **inbound** traffic from the shared cloud VLAN to VPC EIPs is not filtered.

### 1.3 Scope of Exposure

| Source | Target | Exposed? |
|--------|--------|----------|
| Customer-A worker → Customer-B kube-apiserver EIP | Tenant CP API | **YES** |
| Customer-A worker → Customer-B etcd LB EIP | etcd data | **YES** |
| Customer-A NAT GW VM → any project EIP | All EIP services | **YES** |
| Any cloud VLAN device → any project EIP | All EIP services | **YES** |
| Internet → project EIP (no cloud VLAN) | N/A | Not in scope (no L2 path) |
| Management cluster pods → project EIP | Kamaji → etcd LB | **YES** — ovn-cluster LRP on ext-cloud is in cloud VLAN range |

### 1.4 Affected Deployments

Only projects using **shared external networks** (cloud VLANs) where external devices have direct L2 access to the EIP subnet. Typical scenario: **private-link clusters** where customer workers sit on the same cloud VLAN as the `ext-cloud` subnet.

Projects using only KubeVirt workers (internal to the management cluster) are not affected — their workers are inside the VPC overlay.

---

## 2. Connection Patterns Analysis

### 2.1 Pattern 1: Own Workers → Own Control Plane (MUST ALLOW)

```
Customer-A worker (100.64.128.2, cloud VLAN)
  → Customer-A kube-apiserver EIP (100.64.0.22)
  → DNAT → Customer-A VPC → kube-apiserver pod
```

**Decision: MUST be allowed.** Workers need to reach their own cluster's control plane via the EIP. The worker's cloud VLAN IP must be whitelisted.

### 2.2 Pattern 2: Customer NAT GW → Own Control Plane (MAY NEED ALLOW)

```
NAT GW VM (100.64.200.1, cloud VLAN, customer account)
  → Customer-A kube-apiserver EIP (100.64.0.22)
```

The NAT GW VM has a NIC on the cloud VLAN. In some configurations it may need to reach the control plane (monitoring, health checks). This IP should be configurable in the allowlist.

**Decision: Configurable.** Allow via per-project annotation if needed.

### 2.3 Pattern 3: Other Customer's Workers → This Project (MUST BLOCK)

```
Customer-B worker (100.64.128.4, cloud VLAN)
  → Customer-A kube-apiserver EIP (100.64.0.22)
```

**Decision: BLOCK.** This is the primary security vulnerability.

### 2.4 Pattern 4: Management Cluster Pods → Project VPC (MUST ALLOW)

```
Kamaji pod (ovn-default, 10.100.x.x, ovn-cluster VPC)
  → Project etcd LB EIP (100.64.0.x)
  → DNAT → Project VPC → etcd pod
```

**Decision: MUST be allowed.** Traffic from management cluster pods to project VPC EIPs is **routed through the ovn-cluster LRP (Logical Router Port) on ext-cloud**. The source IP at the project VPC router is the **ovn-cluster LRP IP** (e.g., `100.64.0.11` in ZRH), which falls within the cloud VLAN CIDR and **IS matched by the ingress deny rule**.

> **CRITICAL:** The ovn-cluster LRP IP **MUST** be included in `ingress_global_allowlist`. Without it, Kamaji cannot reach etcd LB EIPs to provision new tenant control planes — DataStore setup fails with `context deadline exceeded`.
>
> Find the LRP IP: `kubectl get ovn-eip ovn-cluster-ext-cloud -o jsonpath='{.status.v4Ip}'`

**Verified on ZRH (2026-03-17):** Creating a new cluster `case` after enabling ingress isolation caused Kamaji to fail with `dial tcp 100.64.0.19:32381: i/o timeout`. Adding `100.64.0.11` (ovn-cluster LRP) to the global allowlist immediately resolved the issue.

### 2.5 Pattern 5: Internet → Project EIP (NOT AFFECTED)

External internet traffic reaches EIPs through the cloud provider's routing infrastructure, not through the cloud VLAN L2 segment. The source IP is not in the `100.64.0.0/16` range.

**Decision: NOT affected.**

### 2.6 Pattern 6: Pods Inside VPC → Own EIP (NOT AFFECTED)

```
Pod (project subnet, 10.244.x.x) → own EIP (100.64.0.x)
  → VPC router → OVN LB → destination pod
```

Traffic from pods inside the VPC has `ip4.src` in the internal subnet range (e.g., `10.244.0.0/16`), not in the external subnet range. Not matched by ingress deny rules.

**Decision: NOT affected.**

---

## 3. Proposed Solution: VPC Ingress Policy Routes

### 3.1 Mechanism

Use the same **OVN Logical Router Policy Routes** mechanism as egress isolation, but matching on `ip4.src` (source IP) instead of `ip4.dst` (destination IP). Traffic from the shared external network is blocked by default; only whitelisted source IPs are allowed.

### 3.2 Rule Design

Each project VPC gets two layers of ingress policy routes:

**Layer 1: Allow whitelisted source IPs (dynamic, priority 32000)**
```yaml
# Allow traffic from project's own workers on cloud VLAN
- priority: 32000
  match: "ip4.src == 100.64.128.2"    # worker-1 cloud VLAN IP
  action: allow
- priority: 32000
  match: "ip4.src == 100.64.128.3"    # worker-2 cloud VLAN IP
  action: allow
- priority: 32000
  match: "ip4.src == 100.64.200.1"    # NAT GW VM (if needed)
  action: allow
```

**Layer 2: Deny all traffic from external subnets except gateway (static, priority 31500)**
```yaml
# Block all inbound traffic from ext-cloud except gateway
- priority: 31500
  match: "ip4.src == 100.64.0.0/16 && ip4.src != 100.64.0.1"
  action: drop
```

### 3.3 Priority Map

Complete priority hierarchy on a project VPC router:

| Priority | Action | Purpose | Manager |
|----------|--------|---------|---------|
| **32000** | **allow** | **Ingress: allow whitelisted source IPs** | **NEW: `IngressFirewallManager`** |
| **31500** | **drop** | **Ingress: block all traffic from external subnets (except GW)** | **NEW: `IngressFirewallManager`** |
| 31000 | allow | Internal subnet connectivity (kube-ovn built-in) | kube-ovn |
| 30010 | reroute | SvcLB backend → secondary ext network gateway | `PolicyRouteManager` (existing) |
| 30000 | reroute | FIP internal IP → secondary ext network gateway | `PolicyRouteManager` (existing) |
| 29500 | allow | Egress: allow traffic to project's own EIPs | `EgressFirewallManager` (existing) |
| 29000 | drop | Egress: block all traffic to external subnets (except GW) | `EgressFirewallManager` (existing) |

**Critical: ingress rules must be above 31000.** Kube-OVN injects a built-in `ip4.dst == <subnet-cidr>` allow rule at priority 31000 for each subnet. If the ingress deny is below 31000, the built-in allow rule takes precedence and the deny has no effect.

### 3.4 Example: cs-tsap-183e269d VPC After Implementation

```json
{
  "spec": {
    "policyRoutes": [
      // NEW: Ingress allow (management VPC — REQUIRED)
      {"priority": 32000, "action": "allow", "match": "ip4.src == 100.64.0.11"},
      // NEW: Ingress allow (own workers on cloud VLAN)
      {"priority": 32000, "action": "allow", "match": "ip4.src == 100.64.128.2"},
      {"priority": 32000, "action": "allow", "match": "ip4.src == 100.64.128.3"},
      // NEW: Ingress allow (NAT GW VM)
      {"priority": 32000, "action": "allow", "match": "ip4.src == 100.64.200.1"},
      // NEW: Ingress deny (block all other cloud VLAN sources)
      {"priority": 31500, "action": "drop", "match": "ip4.src == 100.64.0.0/16 && ip4.src != 100.64.0.1"},
      // EXISTING: kube-ovn subnet allow (built-in)
      // {"priority": 31000, "action": "allow", "match": "ip4.dst == 10.244.0.0/16"},
      // EXISTING: Egress allow/deny
      {"priority": 29500, "action": "allow", "match": "ip4.dst == 100.64.0.18"},
      {"priority": 29000, "action": "drop", "match": "ip4.dst == 100.64.0.0/16 && ip4.dst != 100.64.0.1 && ct.new"}
    ]
  }
}
```

### 3.5 Impact Summary

| Component | Affected? | Why |
|-----------|-----------|-----|
| **Management cluster pods (Kamaji, kube-dc, Envoy)** | **YES** | Routed via ovn-cluster LRP on ext-cloud; **MUST** be in `ingress_global_allowlist` |
| Pods inside the project VPC | No | `ip4.src` is project subnet (10.244.x.x), not external |
| Internet inbound to EIPs | No | Source is not in ext-cloud CIDR |
| Egress firewall rules (29000-29500) | No | Different priority range, unaffected |
| Projects without ingress isolation enabled | No | No rules added |

### 3.6 No `ct.new` Needed for Ingress Deny

Unlike egress deny rules (which require `&& ct.new` to avoid blocking LB return traffic), ingress deny rules do **not** need the conntrack condition. The ingress deny blocks new inbound connections from the cloud VLAN; reply traffic from the VPC back to the cloud VLAN uses `ip4.dst` (not `ip4.src`) and is handled by egress rules. There is no conntrack ambiguity.

---

## 4. Implementation Details

### 4.1 Feature Flag

Ingress network isolation is controlled by a boolean in the `master-config` Secret:

```json
{
  "ingress_network_isolation": true
}
```

**Go type** (`api/kube-dc.com/v1/types.go`):
```go
type MasterConfigSpec struct {
    // ...existing fields...
    IngressNetworkIsolation bool     `json:"ingress_network_isolation"`
    IngressGlobalAllowlist  []string `json:"ingress_global_allowlist"`
}
```

Disabled by default (opt-in). Only projects that use shared external networks with external devices (e.g., private-link clusters with cloud VLAN workers) benefit from this feature.

### 4.2 Allow Rule Sources (Layered Security Model)

Allow rules are collected from two sources, merged with deduplication:

| Source | Scope | Who can set | Location |
|--------|-------|-------------|----------|
| **Global allowlist** | All projects | Cluster admin only | `master-config` Secret: `ingress_global_allowlist` |
| **Per-project annotation** | Single project | System controllers (VAP-protected) | `network.kube-dc.com/ingress-allowlist` on Project |

**Security design:** The per-project allowlist annotation is protected by the same `ValidatingAdmissionPolicy` (`protect-kube-dc-resource-annotations`) that protects egress annotations. Only the kube-dc controller ServiceAccount and cluster admins can modify it.

**Annotation format** (comma-separated IPs/CIDRs):
```yaml
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: my-project
  namespace: my-org
  annotations:
    network.kube-dc.com/ingress-allowlist: "100.64.128.2,100.64.128.3,100.64.200.1"
```

**Annotation constant** (`api/kube-dc.com/v1/values.go`):
```go
IngressAllowlistAnnotation = "network.kube-dc.com/ingress-allowlist"
```

### 4.3 New File: `internal/utils/ingress_firewall.go`

New manager that handles ingress firewall policy routes. Mirrors `EgressFirewallManager` structure but matches on `ip4.src` instead of `ip4.dst`, and uses priorities 31500/32000.

```go
package utils

const (
    PolicyRoutePriorityIngressAllow int = 32000
    PolicyRoutePriorityIngressDeny  int = 31500
)

type IngressFirewallManager struct {
    cli     client.Client
    vpcName string
}
```

**Key methods:**

| Method | Purpose | When called |
|--------|---------|-------------|
| `SyncDenyRules(ctx)` | Ensures deny rules exist for all external subnets on the VPC | Project reconcile |
| `SyncAllAllowRules(ctx, addrs)` | Atomic full-replace of all allow rules at priority 32000 | Project reconcile |
| `GenerateDenyRoutes(ctx, cli)` | Returns deny route specs for embedding in new VPC | `GenerateProjectVpc()` |

**Helper functions:**

| Function | Purpose |
|----------|---------|
| `CollectIngressAllowAddresses(ctx, cli, ns, global, project)` | Merges global allowlist + per-project annotation with dedup |
| `ParseIngressAllowlistAnnotation(value)` | Parses comma-separated annotation into `[]string` |

**Deny rule format** — one rule per external subnet, blocks all traffic from that subnet except the gateway:
```
match: "ip4.src == 100.64.0.0/16 && ip4.src != 100.64.0.1"
action: drop
priority: 31500
```

**Allow rule format** — one rule per whitelisted source IP:
```
match: "ip4.src == 100.64.128.2"
action: allow
priority: 32000
```

**SyncDenyRules** and **SyncAllAllowRules** follow the same idempotent diff-and-patch pattern as `EgressFirewallManager`.

### 4.4 Changes to `internal/project/res_vpc.go`

In `GenerateProjectVpc()`, ingress deny rules are appended to new VPCs when the feature flag is enabled:

```go
if kubedccomv1.ConfigGlobal.MasterConfig != nil &&
   kubedccomv1.ConfigGlobal.MasterConfig.IngressNetworkIsolation {
    ingressDenyRoutes, err := utils.GenerateIngressDenyRoutes(ctx, cli)
    if err != nil {
        return nil, fmt.Errorf("failed to generate ingress deny routes: %w", err)
    }
    vpc.Spec.PolicyRoutes = append(vpc.Spec.PolicyRoutes, ingressDenyRoutes...)
}
```

### 4.5 Changes to `internal/project/project.go`

In `Sync()`, after egress firewall sync, sync ingress firewall:

```go
if kubedccomv1.ConfigGlobal.MasterConfig != nil &&
   kubedccomv1.ConfigGlobal.MasterConfig.IngressNetworkIsolation {
    vpcName := ProjectNamespaceName(p)
    if !utils.IsManagementVpc(vpcName) {
        ifm := utils.NewIngressFirewallManager(cli, vpcName)
        ifm.SyncDenyRules(ctx)

        ingressAllowlist := utils.ParseIngressAllowlistAnnotation(
            p.GetAnnotations()[kubedccomv1.IngressAllowlistAnnotation])
        allowAddrs := utils.CollectIngressAllowAddresses(
            kubedccomv1.ConfigGlobal.MasterConfig.IngressGlobalAllowlist,
            ingressAllowlist)
        ifm.SyncAllAllowRules(ctx, allowAddrs)
    }
}
```

### 4.6 Changes to `api/kube-dc.com/v1/types.go`

Added to `MasterConfigSpec`:

```go
IngressNetworkIsolation bool     `json:"ingress_network_isolation"`
IngressGlobalAllowlist  []string `json:"ingress_global_allowlist"`
```

### 4.7 Changes to `api/kube-dc.com/v1/values.go`

Added annotation key constant:

```go
IngressAllowlistAnnotation = "network.kube-dc.com/ingress-allowlist"
```

### 4.8 Helm Chart Defaults

Ingress network isolation is **disabled by default** (opt-in per deployment):

**`charts/kube-dc/values.yaml`:**
```yaml
manager:
  ingressNetworkIsolation: false
  ingressGlobalAllowlist: []
```

**`charts/kube-dc/templates/manager-secret.yaml`:**
```yaml
ingress_network_isolation: {{ .Values.manager.ingressNetworkIsolation | default false | quote }}
{{- if .Values.manager.ingressGlobalAllowlist }}
ingress_global_allowlist: {{ .Values.manager.ingressGlobalAllowlist | toJson | quote }}
{{- end }}
```

---

## 5. CloudSigma Integration: Auto-Whitelist Worker IPs

### 5.1 Overview

The generic ingress firewall mechanism in kube-dc provides the annotation-driven policy route management. The **CloudSigma-specific** logic for discovering worker cloud VLAN IPs and setting the annotation lives in the **kube-dc-k8-manager** (`kdccluster` controller).

This separation ensures:
- kube-dc remains infrastructure-agnostic
- CloudSigma-specific logic is co-located with other CloudSigma worker management
- Other infrastructure providers can implement their own IP discovery

### 5.2 Data Source: CAPI IPAddress Resources

When a CloudSigma worker is provisioned with a cloud VLAN NIC using `GlobalInClusterIPPool`, CAPI IPAM creates an `IPAddress.ipam.cluster.x-k8s.io` resource in the project namespace:

```yaml
apiVersion: ipam.cluster.x-k8s.io/v1beta1
kind: IPAddress
metadata:
  name: otel-worker-pool-1-d7mnr-zsddq-nic-0
  namespace: cs-tsap-183e269d
spec:
  address: 100.64.128.3
  poolRef:
    apiGroup: ipam.cluster.x-k8s.io
    kind: GlobalInClusterIPPool
    name: cloud-vlan-pool
  claimRef:
    name: otel-worker-pool-1-d7mnr-zsddq-nic-0
    namespace: cs-tsap-183e269d
```

**Key observation:** Each worker gets exactly one `IPAddress` per NIC from the pool. Filtering by `spec.poolRef.name == cloud-vlan-pool` yields exactly the cloud VLAN IPs for the project's workers.

### 5.3 Implementation in kube-dc-k8-manager

**File: `internal/controller/kdccluster_ingress_firewall.go`** (new)

During `KdcCluster` reconcile, for CloudSigma infrastructure provider clusters that have cloud VLAN worker pools:

```go
func (r *KdcClusterReconciler) reconcileIngressFirewall(ctx context.Context, kdc *kdcv1.KdcCluster) error {
    // Only for CloudSigma clusters with cloud VLAN workers
    if !hasCloudVLANWorkers(kdc) {
        return nil
    }

    // Collect all IPAddress resources from cloud-vlan-pool
    workerIPs, err := collectCloudVLANWorkerIPs(ctx, r.Client, kdc.Namespace)
    if err != nil {
        return err
    }

    // Build annotation value (comma-separated)
    allowlist := strings.Join(workerIPs, ",")

    // Set annotation on the Project resource
    project, err := loadProject(ctx, r.Client, kdc.Namespace)
    if err != nil {
        return err
    }

    if project.GetAnnotations()[kubedccomv1.IngressAllowlistAnnotation] != allowlist {
        annotations := project.GetAnnotations()
        if annotations == nil {
            annotations = map[string]string{}
        }
        annotations[kubedccomv1.IngressAllowlistAnnotation] = allowlist
        project.SetAnnotations(annotations)
        return r.Client.Update(ctx, project)
    }
    return nil
}

func collectCloudVLANWorkerIPs(ctx context.Context, cli client.Client, namespace string) ([]string, error) {
    var ipList ipamv1.IPAddressList
    if err := cli.List(ctx, &ipList, client.InNamespace(namespace)); err != nil {
        return nil, err
    }

    var ips []string
    for _, ip := range ipList.Items {
        if ip.Spec.PoolRef.Name == "cloud-vlan-pool" &&
           ip.Spec.PoolRef.Kind == "GlobalInClusterIPPool" {
            ips = append(ips, ip.Spec.Address)
        }
    }
    sort.Strings(ips)
    return ips, nil
}

func hasCloudVLANWorkers(kdc *kdcv1.KdcCluster) bool {
    for _, wp := range kdc.Spec.Workers {
        if wp.InfrastructureProvider != "cloudsigma" {
            continue
        }
        for _, nic := range wp.CloudSigma.NICs {
            if nic.IPPoolRef != nil &&
               nic.IPPoolRef.Name == "cloud-vlan-pool" {
                return true
            }
        }
    }
    return false
}
```

### 5.4 Reconcile Flow

```
KdcCluster reconcile (kube-dc-k8-manager)
  │
  ├─ reconcileCCM()
  ├─ reconcileWorkers()
  ├─ reconcileIngressFirewall()     ◄── NEW
  │    │
  │    ├─ hasCloudVLANWorkers(kdc)?
  │    │   └─ Check worker pools for ipPoolRef "cloud-vlan-pool"
  │    │
  │    ├─ collectCloudVLANWorkerIPs()
  │    │   └─ List IPAddress resources, filter by cloud-vlan-pool
  │    │
  │    └─ Update Project annotation: ingress-allowlist = "100.64.128.2,100.64.128.3"
  │
  └─ ...

Project reconcile (kube-dc controller)
  │
  ├─ Sync egress firewall (existing)
  ├─ Sync ingress firewall           ◄── NEW
  │    │
  │    ├─ Read ingress-allowlist annotation → ["100.64.128.2", "100.64.128.3"]
  │    ├─ Merge with global allowlist
  │    ├─ SyncDenyRules() → ip4.src == 100.64.0.0/16 ... drop @ 31500
  │    └─ SyncAllAllowRules() → ip4.src == 100.64.128.x allow @ 32000
  │
  └─ ...
```

### 5.5 Scaling Events

When workers scale up/down, the IPAddress resources change. The kdccluster reconciler re-collects IPs and updates the annotation. The kube-dc project reconciler detects the annotation change and syncs the policy routes.

| Event | kdccluster Controller | kube-dc Controller |
|-------|----------------------|-------------------|
| Worker added | New IPAddress created → annotation updated with new IP | New allow rule added at 32000 |
| Worker removed | IPAddress deleted → annotation updated without old IP | Old allow rule removed |
| Worker replaced (rolling update) | Both IPs briefly in annotation → old removed after cleanup | Temporary overlap, then old rule removed |
| Cluster deleted | Project cleanup removes annotation | Rules removed with VPC deletion |

### 5.6 Pool Name Configuration

The pool name `cloud-vlan-pool` is currently hardcoded in the `GlobalInClusterIPPool` definition. If deployments use different pool names, this should be configurable via the KdcCluster spec or a global config.

**Future enhancement:** Add a field to `MasterConfigSpec`:
```go
IngressIPPoolName string `json:"ingress_ip_pool_name"` // default: "cloud-vlan-pool"
```

---

## 6. Testing

### 6.1 Live Validation Results (2026-03-17)

Tested on `cs-tsap-183e269d` VPC with the `otel` KdcCluster (1 CloudSigma worker at `100.64.128.3`).

**Test 1: Priority 29999 (below kube-ovn's 31000 built-in allow)**
```
OVN rules:
  31000  ip4.dst == 10.244.0.0/16  allow   (kube-ovn built-in)
  29999  ip4.src == 100.64.128.3   drop    (test rule)

Result: Node STILL Ready — built-in allow at 31000 overrides deny at 29999
```

**Test 2: Priority 32000 (above kube-ovn's 31000 built-in allow)**
```
OVN rules:
  32000  ip4.src == 100.64.128.3   drop    (test rule)
  31000  ip4.dst == 10.244.0.0/16  allow   (kube-ovn built-in)

Result: Node went NotReady — kubelet heartbeat blocked ✅
```

**Test 3: Cleanup**
```
Removed policy route → Node back to Ready in ~30 seconds ✅
```

**Key findings:**
1. **`ip4.src` matching on VPC policy routes works** for filtering inbound traffic from the cloud VLAN
2. **Priority must be > 31000** to override kube-ovn's built-in subnet allow rule
3. **Recovery is fast** — removing the deny rule restores connectivity within seconds
4. **DNAT preserves source IP** — traffic entering the project VPC via EIP DNAT retains the original source address from the cloud VLAN

### 6.2 Integration Tests

1. **Ingress deny applied:** Project VPC gets deny rule for ext-cloud CIDR at priority 31500
2. **Worker whitelisted:** Own worker's cloud VLAN IP allowed at priority 32000
3. **Other cloud VLAN sources blocked:** Devices not in allowlist cannot reach project VPC via EIP
4. **Management cluster allowed:** Kamaji, kube-dc, Envoy reach project VPC via ovn-cluster LRP (must be in global allowlist)
5. **Own pods unaffected:** Pods inside VPC reach own EIPs (source is internal subnet)
6. **Egress firewall unaffected:** Egress deny/allow at 29000/29500 still works
7. **Worker scale-up:** New worker's IP added to allowlist within reconcile cycle
8. **Worker scale-down:** Removed worker's IP removed from allowlist

### 6.3 Verification Commands

```bash
# Check OVN policy routes on project VPC
KUBECONFIG=<mgmt> kubectl exec -n kube-system \
  $(kubectl get pods -n kube-system -l app=ovs -o name | head -1) \
  -c openvswitch -- ovn-nbctl lr-policy-list <vpc-name>

# Expected output:
#   32000  ip4.src == 100.64.128.2  allow
#   32000  ip4.src == 100.64.128.3  allow
#   31500  ip4.src == 100.64.0.0/16 && ip4.src != 100.64.0.1  drop
#   31000  ip4.dst == 10.244.0.0/16  allow  (kube-ovn built-in)
#   29500  ip4.dst == 100.64.0.18  allow   (egress)
#   29000  ip4.dst == 100.64.0.0/16 && ip4.dst != 100.64.0.1 && ct.new  drop  (egress)

# Check ingress annotation on Project
KUBECONFIG=<mgmt> kubectl get project <name> -n <org> \
  -o jsonpath='{.metadata.annotations.network\.kube-dc\.com/ingress-allowlist}'

# Check IPAddress resources in project namespace
KUBECONFIG=<mgmt> kubectl get ipaddresses.ipam.cluster.x-k8s.io -n <ns> \
  -o custom-columns='NAME:.metadata.name,IP:.spec.address,POOL:.spec.poolRef.name'
```

---

## 7. Alternatives Considered

### 7.1 Subnet ACLs for Ingress (Rejected)

Apply OVN ACLs on the project's subnet to filter by source IP.

**Pros:** Applied at logical switch level.
**Cons:** Same issues as egress — priority conflicts with NetworkPolicies, less visibility.

### 7.2 External Firewall on Cloud VLAN (Out of Scope)

Apply firewall rules at the CloudSigma infrastructure level on the VLAN.

**Pros:** Filters before traffic reaches OVN.
**Cons:** Requires CloudSigma API integration for VLAN-level ACLs; not available on all VLAN types; not managed by Kubernetes controllers.

### 7.3 Per-Project External Subnets (Future)

Give each project its own VLAN segment for EIP allocation.

**Pros:** Strongest isolation — no shared L2 domain.
**Cons:** Requires physical VLAN provisioning per project; limited VLAN IDs; massive infrastructure change.

### 7.4 Separate IngressFirewallManager vs Extending EgressFirewallManager (Chosen: Separate)

**Option A:** Extend `EgressFirewallManager` with ingress methods.
**Option B:** Create a separate `IngressFirewallManager`.

**Chosen: Option B.** Different priorities (31500/32000 vs 29000/29500), different match direction (`ip4.src` vs `ip4.dst`), different allow source collection logic (external worker IPs vs internal EIPs). Keeping them separate maintains clarity and avoids coupling.

---

## 8. Codebase Reference

### 8.1 Files to Modify/Create

| File | Change | Repository |
|------|--------|------------|
| `internal/utils/ingress_firewall.go` | **NEW** — `IngressFirewallManager`, helpers, constants | kube-dc |
| `api/kube-dc.com/v1/types.go` | Add `IngressNetworkIsolation` + `IngressGlobalAllowlist` to `MasterConfigSpec` | kube-dc |
| `api/kube-dc.com/v1/values.go` | Add `IngressAllowlistAnnotation` constant | kube-dc |
| `api/kube-dc.com/v1/helpers.go` | Parse `ingress_network_isolation` and `ingress_global_allowlist` from Secret | kube-dc |
| `internal/project/res_vpc.go` | Add ingress deny rules to `GenerateProjectVpc()` | kube-dc |
| `internal/project/project.go` | Sync ingress deny + allow rules on project reconcile | kube-dc |
| `charts/kube-dc/values.yaml` | Add `ingressNetworkIsolation` + `ingressGlobalAllowlist` defaults | kube-dc |
| `charts/kube-dc/templates/manager-secret.yaml` | Render ingress fields into master-config Secret | kube-dc |
| `internal/controller/kdccluster_ingress_firewall.go` | **NEW** — CloudSigma worker IP collection + Project annotation update | kube-dc-k8-manager |

### 8.2 Key Dependencies

| Dependency | Purpose | Version |
|-----------|---------|---------|
| `kubeovn.PolicyRouteActionAllow` | Allow action on policy routes | kube-ovn v1.14+ |
| `kubeovn.PolicyRouteActionDrop` | Drop action on policy routes | kube-ovn v1.14+ |
| `ipam.cluster.x-k8s.io/v1beta1.IPAddress` | CAPI IPAM IP addresses | CAPI IPAM v0.2+ |
| `protect-kube-dc-resource-annotations` VAP | Protects ingress-allowlist annotation | Deployed (2026-03-17) |

### 8.3 Existing Patterns Reused

| Pattern | From (Egress) | Adapted For (Ingress) |
|---------|--------------|----------------------|
| `SyncDenyRules` / `SyncAllAllowRules` | `EgressFirewallManager` | `IngressFirewallManager` |
| Per-project annotation allowlist | `egress-allowlist` | `ingress-allowlist` |
| Global allowlist in master-config | `egress_global_allowlist` | `ingress_global_allowlist` |
| Feature flag in master-config | `egress_network_isolation` | `ingress_network_isolation` |
| VAP annotation protection | Same VAP covers all annotations | Already deployed |
| `ListAllExternalSubnets()` | Lists subnets for deny rules | Reused as-is |

---

## 9. Effort Estimate

| Task | Effort | Risk |
|------|--------|------|
| `IngressFirewallManager` implementation (kube-dc) | 0.5 day | Low (mirrors egress) |
| Integration in `project.Sync()` + `res_vpc.go` | 0.5 day | Low |
| Feature flag + Helm chart + config parsing | 0.5 day | Low |
| CloudSigma IP collection in kdccluster controller (kube-dc-k8-manager) | 0.5 day | Medium |
| Manual testing on staging | 0.5 day | — |
| Integration tests | 0.5 day | — |
| **Total** | **~3 days** | — |

---

## 10. Open Questions

1. **Should ingress isolation be opt-in or opt-out?** **Proposed: opt-in (`false` by default).** Only deployments with shared cloud VLANs and external workers need this. Pure KubeVirt deployments have no external devices on the cloud VLAN.

2. **Pool name configuration.** Currently the pool name `cloud-vlan-pool` would be hardcoded. Should it be configurable per deployment via master-config or KdcCluster spec?

3. **NAT GW VM IP discovery.** The NAT GW VM (e.g., `100.64.200.1`) is not represented by a CAPI IPAddress resource. Its cloud VLAN IP must be added manually via the global allowlist or per-project annotation by the cluster admin. Future: auto-discover from KdcCluster spec if the DHCP NIC configuration includes a known gateway IP.

4. **Multiple external subnets.** If a deployment has multiple shared external subnets (e.g., `ext-cloud` + `ext-public`), deny rules should be generated for each. The existing `ListAllExternalSubnets()` helper handles this, same as egress.

5. **Rollback/cleanup.** When `ingress_network_isolation` is disabled, existing ingress policy routes remain on VPCs. Same consideration as egress — a future cleanup mechanism could remove stale rules.

6. ~~**Management cluster traffic assumed unaffected.**~~ **RESOLVED (2026-03-17).** Initially assumed management cluster pod traffic would use internal pod IPs (`10.100.x.x`) as source when reaching project EIPs. In practice, OVN routes this traffic through the **ovn-cluster LRP on ext-cloud**, so the source IP is the LRP address (e.g., `100.64.0.11`), which falls within the cloud VLAN CIDR and IS matched by the ingress deny rule. **Fix: the ovn-cluster LRP IP MUST be included in `ingress_global_allowlist`.** This is documented in the installer `stack.yaml` and values template. Find the LRP IP: `kubectl get ovn-eip ovn-cluster-ext-cloud -o jsonpath='{.status.v4Ip}'`
