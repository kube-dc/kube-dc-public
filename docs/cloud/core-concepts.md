# Core Concepts

Before you deploy resources, it helps to understand how Kube-DC is organized. The platform uses a strict hierarchy to ensure security and billing accuracy.

<div style={{width: '100%', maxWidth: 'none'}}>
<img src={require('./images/core-concept.png').default} alt="Kube-DC Hierarchy" style={{width: '100%', display: 'block'}} />
</div>

## The Hierarchy

Kube-DC is structured in three levels:

### Organization (The Tenant)

An **Organization** is your company or team account — the top-level entity in Kube-DC.

- **Billing** and **User Management** happen at this level
- Invite multiple users to your Organization and assign them different roles (e.g., Administrator, Developer)
- Isolated SSO realm with Keycloak-based authentication

### Project (The Workspace)

**Projects** act as isolated containers for your infrastructure. You might create separate projects for different environments (e.g., production, staging, testing).

- Each Project gets its own **completely isolated network**
- Dedicated Kubernetes namespace
- Quotas (limits on CPU/RAM) can be applied specifically to a Project
- RBAC-controlled access

### Resources (The Workloads)

**Resources** are the actual items you deploy inside a Project:

- **Virtual Machines** — Full VMs powered by KubeVirt
- **Containers** — Standard Kubernetes pods and deployments
- **Services** — Expose your workloads via LoadBalancer, NodePort, or Ingress
- **Storage** — Persistent volumes (block) and S3 buckets (object)
- **Networking** — External IPs (EIP), Floating IPs (FIP), and NAT gateways

## Network Isolation (SDN)

Security in Kube-DC is built around **Network Isolation**.

### Project-Level Isolation

Every Project you create is assigned its own private network (VXLAN/VLAN). Projects can also obtain **external IPs** from the provider network — either assigned directly to VMs or via LoadBalancers for Kubernetes workloads.

### No Traffic Leaks

By default, a Virtual Machine in "Project A" **cannot communicate** with a Virtual Machine in "Project B", even if they belong to the same Organization. This effectively prevents development environments from impacting production.

### Overlapping IPs

Because networks are isolated, you can use the same internal IP ranges (CIDRs) in different projects without conflict — similar to AWS VPCs.

## Kubernetes-Native

Every resource in Kube-DC is a Kubernetes Custom Resource. This means you can manage your entire infrastructure using:

- `kubectl` CLI
- Helm charts
- Terraform (Kubernetes provider)
- GitOps (ArgoCD, Flux)
- The Kube-DC Web UI

## Next Steps

- [Creating Your First Project](first-project.md)
- [Deploy Your First Application](deploy-first-app.md)
