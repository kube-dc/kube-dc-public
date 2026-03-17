# VPC Egress Network Isolation for EIP/LoadBalancer Services

## Status: Implemented (2026-03-17)
## Date: 2026-03-16
## Author: Auto-generated from architecture analysis

---

## 1. Problem Statement

### 1.1 Security Issue

In the current Kube-DC deployment, **any pod in any project VPC can access LoadBalancer VIPs and EIPs belonging to other projects** via the shared external subnets (`ext-cloud`, `ext-public`).

**Concrete example:**
```
# From a pod in shalb-docs namespace (different project):
curl -k https://100.65.0.148:6443/api/version
# Successfully reaches dev-cp Kamaji API server in shalb-jumbolot project
```

This is a **multi-tenant isolation violation** — projects should not be able to reach each other's services via external IPs.

### 1.2 Root Cause

All project VPC routers connect to the same shared external subnets:
- `ext-cloud`: `100.65.0.0/16`, gateway `100.65.0.1`
- `ext-public`: `91.224.11.0/24`, gateway `91.224.11.1`

When a pod targets an external IP (e.g., `100.65.0.148`), the traffic flows:

```
Pod (shalb-docs, 10.0.0.x)
  → shalb-docs VPC router
  → static route 0.0.0.0/0 → 100.65.0.1
  → SNAT to project's EIP (e.g., 100.65.0.206)
  → ext-cloud L2 network
  → shalb-jumbolot VPC router (owns 100.65.0.148)
  → OVN LB DNAT → backend pod (10.0.0.y)
  → Response returns via ext-cloud
```

There are **no access controls** preventing cross-project traffic on the external subnets. The existing VPC policy routes (priority 30000/30010) only handle **return traffic rerouting** for secondary external networks — they do not perform ingress filtering.

### 1.3 Scope of Exposure

| Resource Type | Example | Exposed? |
|--------------|---------|----------|
| Kamaji tenant CP API | `100.65.0.148:6443` | **YES** — full K8s API access |
| etcd LB | `100.65.0.115:32382` | **YES** — etcd data access |
| Application LBs (nginx, etc.) | `100.65.0.115:80` | **YES** |
| FIP-exposed VMs | `91.224.11.5` | **YES** — direct VM access |
| Envoy Gateway routes | `*.kube-dc.cloud` | Not affected (external ingress) |

---

## 2. Connection Patterns Analysis

### 2.1 Pattern 1: Kamaji Controller → Tenant etcd (Cross-VPC, MUST WORK)

**Flow:**
```
Kamaji pod (kamaji-system, ovn-default subnet, 10.100.1.60)
  → DNS: dev-etcd-etcd-lb-ext.shalb-jumbolot.svc.cluster.local
  → resolves to: 100.65.0.115 (ext-cloud LB VIP)
  → ovn-cluster VPC router → ext-cloud → shalb-jumbolot VPC router
  → OVN LB → etcd pod (10.0.0.156)
```

**Key facts:**
- Kamaji runs in `kamaji-system` namespace on `ovn-default` subnet (10.100.0.0/16)
- Part of `ovn-cluster` VPC (the management VPC)
- Connects via `-ext` headless services that resolve to LB VIPs
- DataStore endpoints: `dev-etcd-etcd-lb-ext.shalb-jumbolot.svc.cluster.local:32382`

**Decision: NOT affected by isolation.** The `ovn-cluster` VPC is the management plane and will NOT get egress firewall rules.

### 2.2 Pattern 2: Envoy Gateway → Backend Services (Cross-VPC, MUST WORK)

**Flow:**
```
Internet → DNS (dev-cp-shalb-jumbolot.kube-dc.cloud)
  → Envoy pod (envoy-gateway-system, ovn-default, 10.100.0.183)
  → Backend CRD endpoint: 100.65.0.148:6443 (ext-cloud EIP)
  → ovn-cluster VPC router → ext-cloud → shalb-jumbolot VPC router
  → OVN LB → dev-cp pod
```

**Key facts:**
- Envoy pods run on `ovn-default` (10.100.0.0/16) in `ovn-cluster` VPC
- `Backend` CRDs (Envoy Gateway API) store raw ext-cloud EIP addresses as endpoints:
  ```yaml
  # Backend in shalb-jumbolot namespace
  spec:
    endpoints:
    - ip:
        address: 100.65.0.148
        port: 6443
  ```
- Created by `GatewayBackendManager` in `internal/service_lb/gateway_backend.go`
- `TLSRoute`/`HTTPRoute` resources reference these Backends

**Decision: NOT affected by isolation.** Envoy runs in `ovn-cluster` VPC.

### 2.3 Pattern 3: Worker VMs → Own Cluster API (Intra-Project, MUST WORK)

**Flow:**
```
Worker VM (shalb-jumbolot-default, 10.0.0.213)
  → kubeconfig server: https://100.65.0.148:6443
  → shalb-jumbolot VPC router
  → OVN LB matches VIP → dev-cp pod (10.0.0.217)
```

**Key facts:**
- Worker VMs (KubeVirt) live on the project's default subnet (e.g., `shalb-jumbolot-default`)
- Their kubeconfig points to the cluster's LB VIP on ext-cloud
- They MUST reach their own project's LB VIPs for kubelet, kube-proxy, etc.
- Current live example:
  ```
  dev-workers VMs: 10.0.0.211, 10.0.0.213, 10.0.0.215
  dev-cp LB VIP: 100.65.0.148:6443
  ```

**Decision: MUST be allowed.** Project's own EIP addresses must be whitelisted in the egress firewall.

### 2.4 Pattern 4: Cross-Project Pod → Other Project's LB (MUST BLOCK)

**Flow (currently works, should be blocked):**
```
Pod in shalb-docs (10.0.0.x)
  → dst: 100.65.0.148 (shalb-jumbolot's LB VIP)
  → shalb-docs VPC router → SNAT → ext-cloud → shalb-jumbolot router
  → OVN LB → dev-cp pod
```

**Decision: BLOCK.** This is the primary security vulnerability.

### 2.5 Pattern 5: Pods → Internet via SNAT (MUST WORK)

**Flow:**
```
Pod (project subnet, 10.0.x.x)
  → dst: 8.8.8.8 (any internet address)
  → VPC router → static route 0.0.0.0/0 → 100.65.0.1 (gateway)
  → SNAT → ext-cloud gateway → internet
```

**Decision: MUST work.** The ext-cloud gateway IP (`100.65.0.1`) must always be reachable.

### 2.6 Pattern 6: External Clients → LB VIPs (Inbound, MUST WORK)

**Flow:**
```
External client → ext-cloud/ext-public → VPC router LB → backend pod
```

**Decision: NOT affected.** Inbound traffic is not controlled by VPC egress policy routes.

### 2.7 Pattern 7: kube-dc-k8-manager and CAPI Controllers (Cross-VPC, MUST WORK)

**Flow:**
```
kube-dc-k8-manager (kube-dc-system, ovn-default, ovn-cluster VPC)
  → creates KamajiControlPlane, DataStore resources
  → Kamaji reads DataStore endpoints → connects to etcd LB VIP
```

**Key facts:**
- All management controllers run in `ovn-cluster` VPC namespaces
- `kube-dc-system`, `kamaji-system`, `capi-system`, `envoy-gateway-system` are all on `ovn-default`

**Decision: NOT affected.** All management controllers are in `ovn-cluster` VPC.

---

## 3. Proposed Solution: VPC Egress Policy Routes

### 3.1 Mechanism

Use **OVN Logical Router Policy Routes** with `drop` and `allow` actions on each project VPC router. Kube-OVN v1.14+ (used in this project) supports three policy route actions:

```go
// From kubeovn/kube-ovn/pkg/apis/kubeovn/v1/vpc.go
PolicyRouteActionAllow   = PolicyRouteAction("allow")
PolicyRouteActionDrop    = PolicyRouteAction("drop")
PolicyRouteActionReroute = PolicyRouteAction("reroute")
```

These are exposed via the `Vpc.Spec.PolicyRoutes` CRD field, already used extensively in the codebase:
- `internal/utils/policy_route.go` — `PolicyRouteManager` for CRUD operations
- `internal/service_lb/service_lb.go` — SvcLB reroute routes at priority 30010
- `internal/controller/kube-dc.com/fip_controller.go` — FIP reroute routes at priority 30000

### 3.2 Rule Design

Each project VPC gets two layers of policy routes:

**Layer 1: Allow own EIPs (dynamic, priority 29500)**
```yaml
# Allow traffic to project's own EIP addresses
- priority: 29500
  match: "ip4.dst == 100.65.0.148"   # dev-cp LB VIP
  action: allow
- priority: 29500
  match: "ip4.dst == 100.65.0.115"   # shared default-gw EIP (etcd, nginx LBs)
  action: allow
- priority: 29500
  match: "ip4.dst == 91.224.11.15"   # nginx-lb on ext-public
  action: allow
```

**Layer 2: Deny all external subnets except gateways (static, priority 29000)**
```yaml
# Block all ext-cloud traffic except gateway (needed for SNAT/internet)
- priority: 29000
  match: "ip4.dst == 100.65.0.0/16 && ip4.dst != 100.65.0.1"
  action: drop

# Block all ext-public traffic except gateway
- priority: 29000
  match: "ip4.dst == 91.224.11.0/24 && ip4.dst != 91.224.11.1"
  action: drop
```

### 3.3 Priority Map

Complete priority hierarchy on a project VPC router:

| Priority | Action | Purpose | Manager |
|----------|--------|---------|---------|
| 31000 | allow | Internal subnet connectivity (kube-ovn default) | kube-ovn |
| 30010 | reroute | SvcLB backend → secondary ext network gateway | `PolicyRouteManager` (existing) |
| 30000 | reroute | FIP internal IP → secondary ext network gateway | `PolicyRouteManager` (existing) |
| **29500** | **allow** | **Allow traffic to project's own EIPs** | **NEW: `EgressFirewallManager`** |
| **29000** | **drop** | **Block all traffic to external subnets (except GW)** | **NEW: `EgressFirewallManager`** |

Policy routes are evaluated highest priority first. A packet destined to a project's own EIP matches the `allow` at 29500 before hitting the `drop` at 29000.

### 3.4 Example: shalb-jumbolot VPC After Implementation

```json
{
  "spec": {
    "policyRoutes": [
      // Existing: SvcLB reroute for secondary network
      {"priority": 30010, "action": "reroute", "match": "ip4.src == 10.0.0.148", "nextHopIP": "91.224.11.1"},
      // Existing: FIP reroute for secondary network
      {"priority": 30000, "action": "reroute", "match": "ip4.src == 10.0.0.153", "nextHopIP": "91.224.11.1"},
      // NEW: Allow own EIPs
      {"priority": 29500, "action": "allow", "match": "ip4.dst == 100.65.0.115"},
      {"priority": 29500, "action": "allow", "match": "ip4.dst == 100.65.0.141"},
      {"priority": 29500, "action": "allow", "match": "ip4.dst == 100.65.0.148"},
      {"priority": 29500, "action": "allow", "match": "ip4.dst == 100.65.0.168"},
      {"priority": 29500, "action": "allow", "match": "ip4.dst == 91.224.11.15"},
      // NEW: Block external subnets
      {"priority": 29000, "action": "drop", "match": "ip4.dst == 100.65.0.0/16 && ip4.dst != 100.65.0.1"},
      {"priority": 29000, "action": "drop", "match": "ip4.dst == 91.224.11.0/24 && ip4.dst != 91.224.11.1"}
    ]
  }
}
```

### 3.5 What Is NOT Affected

| Component | VPC | Gets Rules? | Why |
|-----------|-----|-------------|-----|
| Kamaji controller | `ovn-cluster` | No | Management VPC — unrestricted |
| kube-dc-k8-manager | `ovn-cluster` | No | Management VPC — unrestricted |
| Envoy Gateway | `ovn-cluster` | No | Management VPC — unrestricted |
| svc-lb controller | `ovn-cluster` | No | Management VPC — unrestricted |
| External clients (internet) | N/A | No | Inbound traffic, not egress |

Only **project VPCs** (e.g., `shalb-jumbolot`, `shalb-docs`, `test-jump`) get egress firewall rules.

---

## 4. Implementation Details

### 4.1 Feature Flag

Egress network isolation is controlled by a boolean in the `master-config` Secret (only cluster admins can modify):

```json
{
  "egress_network_isolation": true
}
```

**Go type** (`api/kube-dc.com/v1/types.go`):
```go
type MasterConfigSpec struct {
    // ...
    EgressNetworkIsolation bool     `json:"egress_network_isolation"`
    EgressGlobalAllowlist  []string `json:"egress_global_allowlist"`
}
```

Enabled by default in both the Helm chart (`charts/kube-dc/values.yaml`) and installer chart. When `false`, no deny/allow rules are added.

### 4.2 Allow Rule Sources (Layered Security Model)

Allow rules are collected from three sources, merged with deduplication:

| Source | Scope | Who can set | Location |
|--------|-------|-------------|----------|
| **Auto-collected EIPs** | Per-project | Controller (automatic) | All `EIp.Status.IpAddress` in project namespace |
| **Global allowlist** | All projects | Cluster admin only | `master-config` Secret: `egress_global_allowlist` |
| **Per-project annotation** | Single project | Cluster admin (VAP-protected) | `network.kube-dc.com/egress-allowlist` on Project |

**Security design:** The per-project allowlist is intentionally NOT a CRD spec field. If it were on `ProjectSpec`, org admins could whitelist any EIP address (including other projects' EIPs), defeating the isolation. Instead, it is an **annotation** protected by a native Kubernetes `ValidatingAdmissionPolicy` (VAP) that blocks all users except the kube-dc controller ServiceAccount from modifying any annotations on Project resources.

**Annotation format** (comma-separated IPs/CIDRs):
```yaml
apiVersion: kube-dc.com/v1
kind: Project
metadata:
  name: my-project
  namespace: my-org
  annotations:
    network.kube-dc.com/egress-allowlist: "100.65.0.200,10.8.0.0/24"
```

**Annotation constant** (`api/kube-dc.com/v1/values.go`):
```go
EgressAllowlistAnnotation = "network.kube-dc.com/egress-allowlist"
```

### 4.3 ValidatingAdmissionPolicy for Annotation Protection

**Implemented 2026-03-17.** Instead of Kyverno, a native Kubernetes `ValidatingAdmissionPolicy` (VAP) protects all annotations on Project, Organization, and OrganizationGroup resources. VAP runs in-process in the API server — no external webhook dependency.

The policy is deployed via Helm chart template `charts/kube-dc/templates/vap-protect-annotations.yaml`:

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata:
  name: protect-kube-dc-resource-annotations
spec:
  failurePolicy: Fail
  matchConstraints:
    resourceRules:
      - apiGroups: ["kube-dc.com"]
        apiVersions: ["*"]
        operations: ["UPDATE"]
        resources: ["projects", "organizations", "organizationgroups"]
  matchConditions:
    - name: exclude-system-service-accounts
      expression: >-
        !request.userInfo.username.startsWith('system:serviceaccount:kube-dc:')
    - name: exclude-cluster-admins
      expression: >-
        !('system:masters' in request.userInfo.groups)
  validations:
    - expression: >-
        (has(object.metadata.annotations) ? object.metadata.annotations : {}) ==
        (has(oldObject.metadata.annotations) ? oldObject.metadata.annotations : {})
      message: "Modifying annotations on this resource is not allowed. Only system controllers can change annotations."
```

This covers the `network.kube-dc.com/egress-allowlist` annotation along with all other annotations (billing, reconcile triggers, etc.). Users can still create/delete projects and update spec fields — only annotation modifications are blocked.

### 4.4 New File: `internal/utils/egress_firewall.go`

New manager that handles egress firewall policy routes. Separate from the existing `PolicyRouteManager` because the egress firewall has different semantics (match on destination IP, allow/drop actions, no next-hop gateway).

```go
package utils

const (
    PolicyRoutePriorityEgressAllow int = 29500
    PolicyRoutePriorityEgressDeny  int = 29000
    ManagementVpcName                  = "ovn-cluster"
)

type EgressFirewallManager struct {
    cli     client.Client
    vpcName string
}
```

**Key methods:**

| Method | Purpose | When called |
|--------|---------|-------------|
| `SyncDenyRules(ctx)` | Ensures deny rules exist for all external subnets on the VPC | Project reconcile |
| `SyncAllowRule(ctx, eipAddr)` | Adds a single allow rule for one EIP | Not used directly (prefer SyncAllAllowRules) |
| `DeleteAllowRule(ctx, eipAddr)` | Removes a single allow rule | Not used directly |
| `SyncAllAllowRules(ctx, addrs)` | Atomic full-replace of all allow rules at priority 29500 | Project, LB, FIP reconcile |
| `GenerateDenyRoutes(ctx, cli)` | Returns deny route specs for embedding in new VPC | `GenerateProjectVpc()` |

**Helper functions:**

| Function | Purpose |
|----------|---------|
| `IsManagementVpc(name)` | Returns true for `ovn-cluster` — never gets rules |
| `ListAllExternalSubnets(ctx, cli)` | Lists all subnets with `cloud` or `public` external network type labels |
| `CollectProjectEipAddresses(ctx, cli, ns)` | Enumerates all `EIp.Status.IpAddress` in a namespace |
| `CollectProjectAllowAddresses(ctx, cli, ns, global, project)` | Merges EIPs + global allowlist + per-project annotation with dedup |
| `ParseEgressAllowlistAnnotation(value)` | Parses comma-separated annotation into `[]string` |

**Deny rule format** — one rule per external subnet, blocks **new connections** except the gateway:
```
match: "ip4.dst == 100.65.0.0/16 && ip4.dst != 100.65.0.1 && ct.new"
action: drop
priority: 29000
```

**Why `ct.new`?** OVN's logical router pipeline runs conntrack before evaluating policy routes. Without `ct.new`, deny rules also block **return traffic** from OVN Load Balancer backends. For example, when Kamaji (on `ovn-cluster`) connects to a project's etcd LB, the LB DNATs the packet to the etcd pod. The response from the etcd pod has a destination of Kamaji's SNAT address (in the ext-cloud range), which matches the deny rule and gets dropped. Adding `&& ct.new` ensures only **new connection attempts** (SYN packets) are blocked — established/reply traffic (`ct.est`/`ct.rpl`) from LB return paths passes through.

Conntrack states in OVN policy route matches:
- `ct.new` — first packet of a new connection
- `ct.est` — packet belonging to an established connection
- `ct.rpl` — reply direction of an established connection

**Allow rule format** — one rule per whitelisted IP/CIDR:
```
match: "ip4.dst == 100.65.0.148"
action: allow
priority: 29500
```

**SyncDenyRules** checks existing deny rules and only patches the VPC if rules are missing, avoiding unnecessary API calls. **SyncAllAllowRules** does a full diff of existing vs desired allow rules and only patches if there are changes.

### 4.5 Changes to `internal/project/res_vpc.go`

In `GenerateProjectVpc()`, deny rules are appended to new VPCs when the feature flag is enabled:

```go
if kubedccomv1.ConfigGlobal.MasterConfig != nil &&
   kubedccomv1.ConfigGlobal.MasterConfig.EgressNetworkIsolation {
    denyRoutes, err := utils.GenerateDenyRoutes(ctx, cli)
    if err != nil {
        return nil, fmt.Errorf("failed to generate egress deny routes: %w", err)
    }
    vpc.Spec.PolicyRoutes = append(vpc.Spec.PolicyRoutes, denyRoutes...)
}
```

### 4.6 Changes to `internal/project/project.go`

In `Sync()`, after EIP creation, syncs both deny rules (idempotent) and the full set of allow rules (EIPs + global allowlist + per-project annotation):

```go
if kubedccomv1.ConfigGlobal.MasterConfig != nil &&
   kubedccomv1.ConfigGlobal.MasterConfig.EgressNetworkIsolation {
    vpcName := ProjectNamespaceName(p)
    if !utils.IsManagementVpc(vpcName) {
        efm := utils.NewEgressFirewallManager(cli, vpcName)
        efm.SyncDenyRules(ctx)

        projectAllowlist := utils.ParseEgressAllowlistAnnotation(
            p.GetAnnotations()[kubedccomv1.EgressAllowlistAnnotation])
        allowAddrs, err := utils.CollectProjectAllowAddresses(ctx, cli, vpcName,
            kubedccomv1.ConfigGlobal.MasterConfig.EgressGlobalAllowlist, projectAllowlist)
        efm.SyncAllAllowRules(ctx, allowAddrs)
    }
}
```

This handles:
- **New projects**: deny rules embedded in VPC spec + allow rules on first reconcile
- **Existing projects**: deny rules added on next reconcile (migration)
- **Allowlist changes**: re-synced on every project reconcile

### 4.7 Changes to `internal/controller/core/service_controller.go`

A `syncEgressFirewallAllowRules(ctx, svc)` helper is called in both `reconcileSync()` (after LB status update) and `reconcileDelete()` (after EIP deletion). It:

1. Loads the Project via `LoadProjectByNamespace()` to read the annotation
2. Calls `CollectProjectAllowAddresses()` to merge all three sources
3. Calls `SyncAllAllowRules()` for atomic full-replace

This design uses the "collect-all-then-sync" approach rather than single add/remove, which correctly handles shared EIPs (multiple services using the same EIP address).

### 4.8 Changes to `internal/controller/kube-dc.com/fip_controller.go`

Same pattern as the service controller — a `syncEgressFirewallAllowRules(ctx, fipObj)` helper called in both `reconcileSync()` (after FIP status update) and `reconcileDelete()` (after EIP deletion). Uses `fipObj.GetProject()` to load the project's annotation.

### 4.9 Changes to `api/kube-dc.com/v1/types.go`

Added to `MasterConfigSpec`:

```go
// Feature flag
EgressNetworkIsolation bool     `json:"egress_network_isolation"`
// Global allowlist — shared infra IPs all projects can reach
EgressGlobalAllowlist  []string `json:"egress_global_allowlist"`
```

### 4.10 Changes to `api/kube-dc.com/v1/values.go`

Added annotation key constant:

```go
EgressAllowlistAnnotation = "network.kube-dc.com/egress-allowlist"
```

### 4.11 Changes to `api/kube-dc.com/v1/helpers.go`

Kubernetes secrets store all values as strings. The `JSONCopy` helper (JSON marshal → unmarshal) cannot decode string `"true"` into a Go `bool` field, and cannot decode a JSON string into `[]string`. Manual post-processing was added in `ReadConfig()`:

```go
secretMap := SecretDataToMap(authSecret.Data)
utils.JSONCopy(secretMap, ConfigGlobal.MasterConfig)
// K8s secrets store all values as strings — manually parse non-string types
if secretMap["egress_network_isolation"] == "true" {
    ConfigGlobal.MasterConfig.EgressNetworkIsolation = true
}
if raw, ok := secretMap["egress_global_allowlist"]; ok && raw != "" {
    var allowlist []string
    if err := json.Unmarshal([]byte(raw), &allowlist); err == nil {
        ConfigGlobal.MasterConfig.EgressGlobalAllowlist = allowlist
    }
}
```

### 4.12 Helm Chart Defaults

Egress network isolation is **enabled by default** in both charts.

**`charts/kube-dc/values.yaml`:**
```yaml
manager:
  egressNetworkIsolation: true
  egressGlobalAllowlist: []
```

**`charts/kube-dc/templates/manager-secret.yaml`:**
```yaml
egress_network_isolation: {{ .Values.manager.egressNetworkIsolation | default true | quote }}
{{- if .Values.manager.egressGlobalAllowlist }}
egress_global_allowlist: {{ .Values.manager.egressGlobalAllowlist | toJson | quote }}
{{- end }}
```

**`installer/kube-dc/templates/kube-dc/kube-dc/values.yaml`:**
```yaml
egressNetworkIsolation: {{ .variables.cluster_config.egress_network_isolation | default true }}
```

To disable, set `manager.egressNetworkIsolation: false` in Helm values or `cluster_config.egress_network_isolation: false` in installer variables.

### 4.13 No Changes to `internal/utils/policy_route.go`

The existing `PolicyRouteManager` is kept as-is. It is tightly coupled to `reroute` action semantics (match on source IP, requires next-hop gateway). The egress firewall has different semantics (match on destination, allow/drop actions, no gateway), so a separate `EgressFirewallManager` is used.

---

## 5. Migration Strategy

### 5.1 Existing Projects (Implemented)

Migration is fully **controller-driven** — no manual steps required.

When `egress_network_isolation` is set to `true` in `master-config`:
1. **On next project reconcile:** `SyncDenyRules()` detects missing deny rules and adds them to the VPC
2. **On next LB/FIP reconcile:** `SyncAllAllowRules()` collects all EIPs and adds allow rules
3. **Default EIP allow:** Added during project reconcile via `CollectProjectAllowAddresses()`

All existing projects get deny + allow rules on their next reconcile cycle with no manual intervention.

### 5.2 EIP Address Sharing (Implemented)

Multiple LB services can share the same EIP (e.g., `default-gw` EIP `100.65.0.115` is shared by `a135-etcd-etcd-lb`, `boom-etcd-etcd-lb`, `dev-etcd-etcd-lb`, and `nginx`).

**Solution:** The "collect-all-then-sync" approach. On every LB, FIP, and project reconcile, ALL EIP addresses for the project namespace are collected and merged with the global + per-project allowlists, then `SyncAllAllowRules()` does an atomic full-replace of all allow rules at priority 29500.

This correctly handles:
- **Shared EIPs:** Deduplication ensures one rule per unique address
- **EIP deletion:** When a service is deleted and its EIP freed, the next reconcile collects the remaining EIPs — the freed EIP is no longer in the list and its allow rule is removed
- **EIP IP change:** Old address removed, new address added automatically
- **Additions and removals are atomic** — no race conditions between concurrent reconciles

### 5.3 Rollout Safety (Implemented)

**Feature flag** in `master-config` Secret:

```json
{
  "egress_network_isolation": true
}
```

- **Default: `true`** — enabled by default in Helm chart and installer chart
- **Disable:** Set to `false` — new projects won't get rules (existing rules remain on VPCs until manually cleaned up or a cleanup mechanism is added)
- **Rollback:** Set to `false` and restart controller

### 5.4 Allowlist Configuration

**Global allowlist** — add to `master-config` Secret for IPs/CIDRs ALL projects need:

```json
{
  "egress_network_isolation": true,
  "egress_global_allowlist": ["100.65.0.200", "10.8.0.0/24"]
}
```

**Per-project allowlist** — set annotation on the Project resource (cluster admin only, VAP-protected):

```bash
kubectl annotate project my-project -n my-org \
  network.kube-dc.com/egress-allowlist="100.65.0.200,10.8.0.0/24"
```

Changes take effect on the next project, LB, or FIP reconcile.

---

## 6. Testing Plan

### 6.1 Manual Validation (Quick Win)

Test the mechanism on a single VPC before implementing:

```bash
# 1. Get current EIPs for shalb-docs
KUBECONFIG=/home/voa/.kube/cloud_kubeconfig_tunnel \
  kubectl get eip -n shalb-docs -o jsonpath='{range .items[*]}{.status.ipAddress}{"\n"}{end}'

# 2. Patch shalb-docs VPC to add deny + allow rules
KUBECONFIG=/home/voa/.kube/cloud_kubeconfig_tunnel \
  kubectl patch vpc shalb-docs --type=json -p='[
    {"op": "add", "path": "/spec/policyRoutes/-", "value": {"priority": 29500, "action": "allow", "match": "ip4.dst == 100.65.0.206"}},
    {"op": "add", "path": "/spec/policyRoutes/-", "value": {"priority": 29500, "action": "allow", "match": "ip4.dst == 100.65.0.133"}},
    {"op": "add", "path": "/spec/policyRoutes/-", "value": {"priority": 29000, "action": "drop", "match": "ip4.dst == 100.65.0.0/16 && ip4.dst != 100.65.0.1"}},
    {"op": "add", "path": "/spec/policyRoutes/-", "value": {"priority": 29000, "action": "drop", "match": "ip4.dst == 91.224.11.0/24 && ip4.dst != 91.224.11.1"}}
  ]'

# 3. Test from shalb-docs pod:
# SHOULD FAIL (blocked — shalb-jumbolot's LB VIP):
curl -k --connect-timeout 5 https://100.65.0.148:6443/api/version

# SHOULD WORK (own LB VIP):
curl --connect-timeout 5 http://100.65.0.206:80

# SHOULD WORK (internet via SNAT):
curl --connect-timeout 5 https://example.com

# 4. Test from management (kamaji-system) — SHOULD STILL WORK:
# Kamaji → shalb-jumbolot etcd via ext-cloud
```

### 6.2 Integration Tests

1. **New project creation:** Verify deny rules are added to VPC on project creation
2. **LB service creation:** Verify allow rule added for new LB VIP
3. **LB service deletion:** Verify allow rule removed (if no other service uses that EIP)
4. **FIP creation/deletion:** Same as LB
5. **Shared EIP:** Verify allow rule persists when one of multiple services sharing an EIP is deleted
6. **Cross-project access blocked:** Pod in project A cannot reach project B's LB VIP
7. **Own project access works:** Pod/VM in project A CAN reach project A's LB VIPs
8. **Internet access works:** Pod can reach external internet via SNAT
9. **Management access works:** Kamaji, envoy, kube-dc controllers can reach all LB VIPs

### 6.3 Live Validation Results (2026-03-16)

Rules were applied to `shalb-docs` VPC and tested against multiple targets. Rules applied:

```json
[
  {"priority": 29500, "action": "allow", "match": "ip4.dst == 100.65.0.203"},
  {"priority": 29500, "action": "allow", "match": "ip4.dst == 100.65.0.206"},
  {"priority": 29500, "action": "allow", "match": "ip4.dst == 100.65.0.144"},
  {"priority": 29500, "action": "allow", "match": "ip4.dst == 100.65.0.133"},
  {"priority": 29500, "action": "allow", "match": "ip4.dst == 91.224.11.9"},
  {"priority": 29000, "action": "drop", "match": "ip4.dst == 100.65.0.0/16 && ip4.dst != 100.65.0.1"},
  {"priority": 29000, "action": "drop", "match": "ip4.dst == 91.224.11.0/24 && ip4.dst != 91.224.11.1"}
]
```

**Test results from `shalb-docs` wordpress pod:**

| Test | Target | Before Rules | After Rules | Expected | Result |
|------|--------|-------------|-------------|----------|--------|
| Cross-project: dev-cp | `100.65.0.148:6443` | HTTP 403 (0.17s) | Timeout (5s) | Blocked | ✅ PASS |
| Cross-project: boom-cp | `100.65.0.168:6443` | HTTP 403 (0.06s) | Timeout (5s) | Blocked | ✅ PASS |
| Cross-project: a135-cp | `100.65.0.141:6443` | HTTP 403 (0.17s) | Timeout (5s) | Blocked | ✅ PASS |
| Own project: wordpress | `100.65.0.133:80` | HTTP 200 (0.33s) | HTTP 200 (0.37s) | Allowed | ✅ PASS |
| Internet: example.com | `example.com:80` | HTTP 200 (0.05s) | HTTP 200 (0.11s) | Allowed | ✅ PASS |
| Internet: google.com | `google.com:443` | — | HTTP 301 (0.11s) | Allowed | ✅ PASS |

**Test results from `kamaji-system` (management VPC, ovn-cluster) — no rules applied:**

| Test | Target | Result | Expected | Status |
|------|--------|--------|----------|--------|
| Mgmt → shalb-jumbolot dev-cp | `100.65.0.148:6443` | HTTP 403 (2.7s) | Allowed | ✅ PASS |
| Mgmt → shalb-jumbolot boom-cp | `100.65.0.168:6443` | HTTP 403 (0.04s) | Allowed | ✅ PASS |
| Mgmt → shalb-jumbolot etcd LB | `100.65.0.115:32382` | TCP connected (exit 55 — TLS client cert required) | Allowed | ✅ PASS |

**Test results from `shalb-jumbolot` (no rules applied, self-access):**

| Test | Target | Result | Expected | Status |
|------|--------|--------|----------|--------|
| Own: dev-cp | `100.65.0.148:6443` | HTTP 403 (0.02s) | Allowed | ✅ PASS |
| Own: boom-cp | `100.65.0.168:6443` | HTTP 403 (0.09s) | Allowed | ✅ PASS |
| Own: nginx (shared EIP) | `100.65.0.115:80` | HTTP 200 (0.003s) | Allowed | ✅ PASS |
| Own: nginx-lb (ext-public) | `91.224.11.15:80` | HTTP 200 (0.006s) | Allowed | ✅ PASS |

**Cleanup:** Rules were removed from `shalb-docs` VPC and cross-project access was confirmed restored (HTTP 403 to `100.65.0.148:6443` again in 0.23s).

**Key findings:**
1. **Cross-project blocking works immediately** — kube-ovn reconciles policy routes within seconds
2. **Own project LB access preserved** — allow rules at priority 29500 correctly override deny rules at 29000
3. **Internet access unaffected** — gateway exception (`ip4.dst != 100.65.0.1`) works correctly
4. **Management VPC unaffected** — `ovn-cluster` has no egress firewall rules, full access preserved
5. **Self-access within project works** — shalb-jumbolot pods reach own LB VIPs (cloud and public)
6. **OVN LB DNAT order confirmed** — inbound LB traffic from management VPC to shalb-jumbolot is NOT affected by shalb-docs rules (rules only affect shalb-docs router egress)

### 6.4 Conntrack Fix — LB Return Traffic (2026-03-17)

**Problem discovered:** After deploying deny rules to all project VPCs, Kamaji could no longer reach project etcd LBs. The deny rules blocked **return traffic** from LB backends because the response destination (Kamaji's SNAT address on ext-cloud) matched the deny rule.

**Root cause:** The deny match `ip4.dst == 100.65.0.0/16 && ip4.dst != 100.65.0.1` blocks ALL egress to the external subnet, including OVN LB response packets destined to management VPC clients.

**Fix:** Added `&& ct.new` to deny rule match (`buildDenyMatch()` in `egress_firewall.go`). Only new connections are denied; established/reply traffic passes through.

**Verification (debug pods on live cluster):**

| Test | Source | Target | Result |
|------|--------|--------|--------|
| LB return traffic | `kamaji-system` (ovn-cluster) | `100.65.0.203:32380` (fok-etcd LB) | ✅ TCP connected (exit 56 = TLS cert) |
| Own EIP access | `shalb-docs` pod | `100.65.0.133` (wordpress) | ✅ HTTP 200 |
| Cross-project blocked | `shalb-docs` pod | `100.65.0.148` (shalb-jumbolot) | ❌ Timeout (correct) |
| Internet access | `shalb-docs` pod | `1.1.1.1` | ✅ Connected |

**Live VPC rules after fix (all project VPCs updated by controller):**
```
allow 29500 ip4.dst == 100.65.0.203
allow 29500 ip4.dst == 100.65.0.133
drop  29000 ip4.dst == 100.65.0.0/16 && ip4.dst != 100.65.0.1 && ct.new
drop  29000 ip4.dst == 91.224.11.0/24 && ip4.dst != 91.224.11.1 && ct.new
```

The `fok` KdcCluster in `shalb-docs` successfully provisioned after the fix — Kamaji connected to etcd LB, created the TenantControlPlane, and worker VMs started importing.

### 6.5 Edge Cases

- **New external subnet added:** If a new external subnet (e.g., `ext-private`) is added to the system, existing project VPCs need a new deny rule. The project reconciler should detect missing deny rules.
- **EIP IP change:** If an EIP's IP address changes (reallocation), the old allow rule must be removed and new one added. The `SyncAllAllowRules` approach handles this naturally.
- **Project with no LB services:** Only deny rules and default EIP allow rule exist. Internet access still works via gateway.

---

## 7. Codebase Reference

### 7.1 Files Modified

| File | Change | Status |
|------|--------|--------|
| `internal/utils/egress_firewall.go` | **NEW** — `EgressFirewallManager`, helpers, constants | ✅ Implemented |
| `api/kube-dc.com/v1/types.go` | `EgressNetworkIsolation` + `EgressGlobalAllowlist` in `MasterConfigSpec` | ✅ Implemented |
| `api/kube-dc.com/v1/values.go` | `EgressAllowlistAnnotation` constant | ✅ Implemented |
| `internal/project/res_vpc.go` | Add deny rules to `GenerateProjectVpc()` for new VPCs | ✅ Implemented |
| `internal/project/project.go` | Sync deny rules + allow rules (EIPs + global + annotation) on reconcile | ✅ Implemented |
| `internal/controller/core/service_controller.go` | `syncEgressFirewallAllowRules()` on LB sync/delete | ✅ Implemented |
| `internal/controller/kube-dc.com/fip_controller.go` | `syncEgressFirewallAllowRules()` on FIP sync/delete | ✅ Implemented |
| `api/kube-dc.com/v1/helpers.go` | Manual string→bool/JSON parsing for secret fields | ✅ Implemented |
| `charts/kube-dc/values.yaml` | `egressNetworkIsolation: true` default | ✅ Implemented |
| `charts/kube-dc/templates/manager-secret.yaml` | Render egress fields into master-config Secret | ✅ Implemented |
| `installer/.../kube-dc/values.yaml` | `egressNetworkIsolation: true` default for installer | ✅ Implemented |
| `internal/utils/policy_route.go` | No changes needed (separate manager) | N/A |

### 7.2 Key Dependencies

| Dependency | Purpose | Version |
|-----------|---------|---------|
| `kubeovn.PolicyRouteActionAllow` | Allow action on policy routes | kube-ovn v1.14+ |
| `kubeovn.PolicyRouteActionDrop` | Drop action on policy routes | kube-ovn v1.14+ |
| `kubeovn.Vpc.Spec.PolicyRoutes` | VPC policy route CRD field | kube-ovn v1.12+ |
| `internal/objmgr.RetryHandler` | VPC patching with retry | existing |
| `internal/utils.ResourcesProcessor` | External subnet enumeration | existing |

### 7.3 Live Cluster State (Reference)

**External subnets:**
```
ext-cloud:  cidr=100.65.0.0/16  gw=100.65.0.1   labels={network.kube-dc.com/default-external: true, network.kube-dc.com/external-network-type: cloud}
ext-public: cidr=91.224.11.0/24 gw=91.224.11.1   labels={network.kube-dc.com/external-network-type: public}
```

**shalb-jumbolot EIPs:**
```
default-gw:             100.65.0.115   (shared by etcd LBs + nginx)
slb-dev-cp-0fdz1:       100.65.0.148   (dev-cp LB)
slb-boom-cp-pysix:      100.65.0.168   (boom-cp LB)
slb-a135-cp-cmqip:      100.65.0.141   (a135-cp LB)
test:                   91.224.11.15   (nginx-lb on ext-public)
fip-fip-ubuntu-mmpqr:   91.224.11.5    (FIP for ubuntu VM)
```

**shalb-jumbolot existing policy routes (pre-implementation):**
```json
[
  {"priority": 30010, "action": "reroute", "match": "ip4.src == 10.0.0.148", "nextHopIP": "91.224.11.1"},
  {"priority": 30000, "action": "reroute", "match": "ip4.src == 10.0.0.153", "nextHopIP": "91.224.11.1"}
]
```

**Management VPC (`ovn-cluster`) — NOT modified:**
```json
{
  "policyRoutes": [
    {"priority": 31000, "action": "allow", "match": "ip4.dst == 100.65.0.0/16"}
  ],
  "staticRoutes": [
    {"cidr": "100.65.0.0/16", "nextHopIP": "100.65.0.1", "policy": "policyDst"}
  ]
}
```

---

## 8. Alternatives Considered

### 8.1 Subnet ACLs (Rejected)

Apply OVN ACLs on project default subnets to block egress to external CIDRs.

**Pros:** Applied at logical switch level (before routing).
**Cons:**
- Kube-OVN warns about priority conflicts when mixing Subnet ACLs with NetworkPolicies
- Both use OVN ACLs with overlapping priority ranges
- Less visibility (no CRD-level status)
- More complex match expressions needed

### 8.2 SecurityGroups (Rejected)

Use Kube-OVN `SecurityGroup` CRD with per-pod annotations.

**Pros:** Standard cloud security model.
**Cons:**
- Only applied to pod logical switch ports, not router ports
- Cannot intercept traffic before it reaches the OVN LB on the router
- Would need annotation on every pod — not scalable

### 8.3 Kubernetes NetworkPolicy (Rejected)

Standard K8s NetworkPolicy for egress filtering.

**Pros:** Standard K8s API.
**Cons:**
- Traffic to LB VIPs hits the OVN LB on the router before reaching pods
- NetworkPolicy only filters at pod's logical switch port (post-LB)
- Cannot block traffic to external IPs at all — designed for pod-to-pod

### 8.4 Subnet Isolation (`private: true`) (Rejected)

Set `private: true` on project subnets.

**Pros:** Simple on/off.
**Cons:** Blocks ALL cross-subnet traffic including the gateway — breaks SNAT/internet access.

### 8.5 Per-Project External Subnets (Future Consideration)

Give each project its own VLAN/external subnet.

**Pros:** Strongest isolation, no ACL management.
**Cons:** Requires physical VLAN provisioning per project, limited VLAN IDs, massive infrastructure change.

---

## 9. Effort Estimate

| Task | Effort | Risk |
|------|--------|------|
| `EgressFirewallManager` implementation | 1 day | Low |
| Integration in `project.Sync()` (deny + allow for default EIP) | 0.5 day | Medium |
| Integration in `service_controller.go` (LB allow rules) | 0.5 day | Medium |
| Integration in `fip_controller.go` (FIP allow rules) | 0.5 day | Medium |
| Migration logic for existing projects | 0.5 day | Medium |
| Feature flag support | 0.5 day | Low |
| Manual testing on staging | 1 day | — |
| Integration tests | 1 day | — |
| **Total** | **~5 days** | — |

---

## 10. Open Questions

1. ~~**Should the feature be opt-in or opt-out?**~~ **RESOLVED:** Enabled by default (`true`) in both Helm chart and installer chart. Can be disabled by setting `egressNetworkIsolation: false`.

2. ~~**Should isolation be configurable per-project?**~~ **RESOLVED:** No per-project toggle. Isolation is global (all projects or none). Per-project **exceptions** are handled via the `network.kube-dc.com/egress-allowlist` annotation (VAP-protected, cluster admin only).

3. ~~**How to handle public EIPs on ext-public?**~~ **RESOLVED:** `ListAllExternalSubnets()` discovers all subnets labeled `cloud` or `public`. Deny rules are generated for each. Allow rules work with any IP/CIDR regardless of which external network it's on.

4. ~~**What about customer VLANs?**~~ **RESOLVED:** Customer VLAN subnets (e.g., `10.8.0.0/24`) don't have the `network.kube-dc.com/external-network-type` label, so they are not included in deny rules. No additional configuration needed.

5. **Cleanup on rollback:** When `egress_network_isolation` is set back to `false`, existing deny/allow rules remain on VPCs. A future enhancement could add cleanup logic to remove stale egress firewall rules when the feature is disabled.

6. **New external subnet added:** If a new external subnet is added to the cluster, existing project VPCs won't have a deny rule for it until their next reconcile. `SyncDenyRules()` handles this automatically — it detects missing deny rules and adds them.
