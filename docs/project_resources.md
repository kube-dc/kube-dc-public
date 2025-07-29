# Project Resources Documentation

This document provides a comprehensive overview of all resources created by the Kube-DC Project controller, their finalizers, ownership patterns, and deletion dependencies.

## Resource Creation Order

When a Project is created, resources are synchronized in this order:

1. **Namespace** - Project namespace (`{org}-{project}`)
2. **VPC** - Kube-OVN Virtual Private Cloud
3. **EIp (Default Gateway)** - External IP for project gateway
4. **Subnet** - Kube-OVN subnet for project pods
5. **NetworkAttachmentDefinition** - CNI network configuration
6. **OvnSnatRule** - SNAT rule for outbound traffic
7. **Secrets** - SSH keypairs and authorized keys
8. **RBAC** - Roles and RoleBindings
9. **VpcDns** - DNS configuration for VPC

## Detailed Resource Breakdown

### 1. Namespace
- **Resource**: `v1.Namespace`
- **Name**: `{organization}-{project}` (e.g., `shalb-envoy`)
- **Finalizer**: None (managed by Kubernetes)
- **Created by**: `NewProjectNamespace()` in `internal/project/res_namespace.go`
- **Dependencies**: None (first resource created)

### 2. VPC (Virtual Private Cloud)
- **Resource**: `kubeovn.io/v1.Vpc`
- **Name**: `{organization}-{project}` (e.g., `shalb-envoy`)
- **Finalizer**: `kubeovn.io/kube-ovn-controller`
- **Created by**: `NewProjectVpc()` in `internal/project/res_vpc.go`
- **Dependencies**: Namespace must exist
- **Configuration**:
  - Static routes to external subnets
  - Extra external subnets based on `egressNetworkType`

### 3. EIp (External IP - Default Gateway)
- **Resource**: `kube-dc.com/v1.EIp`
- **Name**: `default-gw`
- **Namespace**: `{organization}-{project}`
- **Finalizer**: `eip.kube-dc.com/finalizer`
- **Created by**: `NewProjectEip()` in `internal/project/res_eip_default.go`
- **Dependencies**: Namespace must exist
- **Ownership States**:
  - `Released`: No active owners (initial state)
  - `Shared`: Has SNAT rule and/or LoadBalancer services as owners
  - `Exclusive`: Used by FIp resources

### 4. OvnEip (Underlying OVN External IP)
- **Resource**: `kubeovn.io/v1.OvnEip`
- **Name**: `{organization}-{project}-{external-subnet}` (e.g., `shalb-envoy-ext-public`)
- **Finalizer**: `kubeovn.io/kube-ovn-controller`
- **Created by**: EIp controller via `NewOvEipRes()` in `internal/eip/ovn_eip_res.go`
- **Dependencies**: EIp must exist, external subnet must be available
- **Labels**:
  - `network.kube-dc.com/eip`: `{namespace}.{eip-name}`
- **Annotations**:
  - `kube-dc.com/ovn-eip-created-by-eip`: `{namespace}.{eip-name}`

### 5. Subnet
- **Resource**: `kubeovn.io/v1.Subnet`
- **Name**: `{organization}-{project}-default` (e.g., `shalb-envoy-default`)
- **Finalizer**: `kubeovn.io/kube-ovn-controller`
- **Created by**: `NewProjectSubnet()` in `internal/project/res_subnet.go`
- **Dependencies**: VPC must exist
- **Configuration**:
  - CIDR block from project spec
  - Associated with project VPC
  - Gateway IP (first IP in CIDR)

### 6. NetworkAttachmentDefinition
- **Resource**: `k8s.cni.cncf.io/v1.NetworkAttachmentDefinition`
- **Name**: `default`
- **Namespace**: `{organization}-{project}`
- **Finalizer**: `project.kube-dc.com/finalizer`
- **Created by**: `NewProjectNad()` in `internal/project/res_nad.go`
- **Dependencies**: Subnet must exist
- **Configuration**: Kube-OVN CNI configuration pointing to project subnet

### 7. OvnSnatRule
- **Resource**: `kubeovn.io/v1.OvnSnatRule`
- **Name**: `{organization}-{project}` (e.g., `shalb-envoy`)
- **Finalizer**: `kubeovn.io/kube-ovn-controller`
- **Created by**: `NewProjectSnat()` in `internal/project/res_snat.go`
- **Dependencies**: OvnEip must exist and have IP assigned
- **Configuration**:
  - Links project subnet to external IP
  - Enables outbound internet access for pods

### 8. Secrets

#### SSH Key Pair Secret
- **Resource**: `v1.Secret`
- **Name**: `ssh-keypair-default`
- **Namespace**: `{organization}-{project}`
- **Finalizer**: `project.kube-dc.com/finalizer`
- **Created by**: `NewProjectKeyPairSeret()` in `internal/project/res_secret.go`
- **Content**: Generated SSH public/private key pair

#### Authorized Keys Secret
- **Resource**: `v1.Secret`
- **Name**: `authorized-keys-default`
- **Namespace**: `{organization}-{project}`
- **Finalizer**: `project.kube-dc.com/finalizer`
- **Created by**: `NewProjectAuthKeySecret()` in `internal/project/res_secret.go`
- **Content**: SSH public keys for VM access

### 9. RBAC Resources

#### Role
- **Resource**: `rbac.authorization.k8s.io/v1.Role`
- **Name**: `admin`
- **Namespace**: `{organization}-{project}`
- **Finalizer**: `project.kube-dc.com/finalizer`
- **Created by**: `NewProjectRole()` in `internal/project/res_role.go`
- **Permissions**: Full access to project resources (pods, services, VMs, etc.)

#### RoleBinding
- **Resource**: `rbac.authorization.k8s.io/v1.RoleBinding`
- **Name**: `org-admin`
- **Namespace**: `{organization}-{project}`
- **Finalizer**: `project.kube-dc.com/finalizer`
- **Created by**: `NewProjectRoleBinding()` in `internal/project/res_role_binding.go`
- **Subject**: `{organization}:org-admin` group

### 10. VpcDns Service
- **Resource**: `v1.Service`
- **Name**: `slr-vpc-dns-{organization}-{project}`
- **Namespace**: `kube-system`
- **Finalizer**: None (managed by service controller)
- **Created by**: `NewProjectVpcDns()` in `internal/project/res_vpc_dns.go`
- **Purpose**: DNS resolution for VPC

## Finalizers Summary

| Resource Type | Finalizer | Controller |
|---------------|-----------|------------|
| Project | `project.kube-dc.com/finalizer` | kube-dc-manager |
| EIp | `eip.kube-dc.com/finalizer` | kube-dc-manager |
| OvnEip | `kubeovn.io/kube-ovn-controller` | kube-ovn-controller |
| Vpc | `kubeovn.io/kube-ovn-controller` | kube-ovn-controller |
| Subnet | `kubeovn.io/kube-ovn-controller` | kube-ovn-controller |
| OvnSnatRule | `kubeovn.io/kube-ovn-controller` | kube-ovn-controller |
| NetworkAttachmentDefinition | `project.kube-dc.com/finalizer` | kube-dc-manager |
| Secrets | `project.kube-dc.com/finalizer` | kube-dc-manager |
| Role | `project.kube-dc.com/finalizer` | kube-dc-manager |
| RoleBinding | `project.kube-dc.com/finalizer` | kube-dc-manager |

## Deletion Order and Dependencies

When a Project is deleted, resources must be removed in reverse dependency order:

### Phase 1: Application Resources
1. **Pods, Services, VMs** - User workloads (deleted by users/operators)
2. **FIp resources** - Floating IPs (if any exist)

### Phase 2: SNAT and Networking
3. **OvnSnatRule** - Must be deleted before OvnEip
4. **OvnEip** - Must be deleted before EIp and Subnet

### Phase 3: Project Infrastructure
5. **EIp** - External IP resource
6. **NetworkAttachmentDefinition** - CNI configuration
7. **Secrets** - SSH keys and authorized keys
8. **RBAC** - Roles and RoleBindings
9. **Subnet** - Must be deleted before VPC
10. **VPC** - Virtual Private Cloud
11. **VpcDns** - DNS service
12. **Namespace** - Project namespace (last)

## Common Deletion Issues

### Stuck Finalizers
- **OvnEip**: May get stuck if SNAT rule deletion fails
- **Subnet**: May get stuck if pods are still running
- **EIp**: May get stuck if project is deleted before EIp controller processes it

### Manual Cleanup Commands
```bash
# Remove stuck finalizers (use with caution)
kubectl patch ovn-snat-rule {name} -p '{"metadata":{"finalizers":null}}' --type=merge
kubectl patch ovn-eip {name} -p '{"metadata":{"finalizers":null}}' --type=merge
kubectl patch subnet {name} -p '{"metadata":{"finalizers":null}}' --type=merge
kubectl patch eip {name} -n {namespace} -p '{"metadata":{"finalizers":null}}' --type=merge
kubectl patch project {name} -n {org-namespace} -p '{"metadata":{"finalizers":null}}' --type=merge
```

## EIp Ownership Patterns

### Ownership States
- **Released**: `ownershipType: Released`, `owners: []` - No active users
- **Shared**: `ownershipType: Shared`, `owners: [...]` - Multiple users (SNAT + Services)
- **Exclusive**: `ownershipType: Exclusive`, `owners: [single]` - Single FIp owner

### Owner Types
- `Snat`: SNAT rule using the EIP for outbound traffic
- `ServiceLb`: LoadBalancer service using the EIP
- `FIp`: Floating IP using the EIP exclusively

### Ownership Transitions
```
Released → Shared (first owner added)
Shared → Released (last owner removed)
Released → Exclusive (FIp claims EIP)
Exclusive → Released (FIp releases EIP)
```

## Organization Limits

Organizations have a configurable limit on the number of ready projects they can contain (default: 3). This limit is enforced by the Project controller during reconciliation.

- **Configuration**: Set via `MasterConfig.OrganizationProjectsLimit`
- **Enforcement**: Projects exceeding the limit will not be reconciled until space becomes available
- **Status**: Projects blocked by limits show as not ready but remain in the cluster

### Enhanced Limit Enforcement (v0.1.31-dev1+)

The Project controller now provides comprehensive feedback when organization limits are hit:

**Detailed Logging:**
- Organization project status with ready/pending counts and project names
- Clear error messages with organization, namespace, and limit context
- Debug-level logs showing available slots and project lists

**Status Conditions:**
Projects blocked by limits receive a `LimitCheck` condition:
```yaml
status:
  ready: false
  conditions:
  - type: LimitCheck
    status: "False"
    reason: LimitExceeded
    message: "organization limit (3 projects) reached - ready projects: 3 (limit: 3)"
    lastTransitionTime: "2025-01-19T16:45:00Z"
```

**Automatic Retry:**
- Projects are automatically requeued every 30 seconds
- Reconciliation proceeds when limit space becomes available
- No manual intervention required

Projects can use different external network types:
- **cloud**: Uses `ext-cloud` subnet (default)
- **public**: Uses `ext-public` subnet (real public IPs)

The `egressNetworkType` in Project spec determines which external subnet is used for the default gateway EIP.
