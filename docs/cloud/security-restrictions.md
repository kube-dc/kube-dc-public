# Security Restrictions

Kube-DC enforces security boundaries in every Project to protect the underlying infrastructure and ensure tenant isolation. This page explains what restrictions apply to your workloads and how to work within them.

## Pod Security

To prevent workloads from compromising the shared infrastructure, certain Pod configurations are restricted in all Project backing namespaces.

### What's Restricted

| Feature | Why it's blocked | What to use instead |
|---------|-----------------|-------------------|
| **Host path volumes** | Grants direct access to the node's filesystem, allowing reads/writes to host data | Use **PersistentVolumeClaims** for persistent storage, **ConfigMaps** or **Secrets** for configuration, **emptyDir** for temporary scratch space |
| **Privileged containers** | Grants full root-level access to the host, bypassing all security boundaries | Run your container with standard (unprivileged) settings — most applications work without privileges |
| **Host networking** | Exposes the node's network interfaces and bypasses network isolation | Use standard pod networking with **Services** (ClusterIP, LoadBalancer) to expose your workloads |
| **Host PID** | Allows visibility into all processes on the node, including other tenants | Use standard process isolation — your container sees only its own processes |
| **Host IPC** | Shares the node's inter-process communication namespace | Use standard IPC — containers within the same pod can already communicate via shared volumes or localhost |

### What You Can Do

These restrictions protect the shared platform while preserving the common
workflows a Project is designed for:

- **Deploy containers** — Pods, Deployments, StatefulSets, DaemonSets, Jobs *(CronJobs are read-only in projects — use a [Managed Cluster](provisioning-cluster.md) for scheduled workloads)*
- **Manage storage** — PersistentVolumeClaims (block storage), emptyDir, ConfigMaps, Secrets
- **Expose services** — LoadBalancer, ClusterIP, Ingress, Gateway API routes
- **Create virtual machines** — KubeVirt VMs with full lifecycle management (start, stop, restart, migrate, console, VNC)
- **Managed Clusters** — Create and scale separate Kubernetes clusters within your Project
- **View logs** — `kubectl logs` works normally for all your pods
- **Monitor resources** — `kubectl top pods` for resource usage metrics
- **Manage certificates** — Request `ManagedCertificate` resources; Project admins
  can also create namespaced cert-manager Issuers and Certificates for routes
- **Configure autoscaling** — HorizontalPodAutoscalers for automatic scaling
- **Backups** — Use the backup controls provided by supported managed services, such as [Managed Databases](managed-databases.md) and [Managed Clusters](cluster-management.md)

:::tip
If you receive a message like *"hostPath volumes are not allowed in project
namespaces"* when creating a Pod, replace the restricted feature with one of the
alternatives above.
:::

:::info Need cluster-scoped control?
A Project is backed by one governed namespace on the shared platform. Project
users cannot create namespaces, CRDs, ClusterRoles, or StorageClasses, and
CronJobs are read-only. NetworkPolicy is not offered as a Project self-service
control. Use a [Managed Cluster](provisioning-cluster.md) when an application
needs its own Kubernetes API, operators, CRDs, multiple namespaces, or
cluster-scoped administration.
:::

## Resource Annotations

System-managed annotations on **Projects**, **Organizations**, and **Organization Groups** are read-only for users. These annotations are used internally for billing, network configuration, and controller state.

**What this means:**
- You **cannot** add, change, or remove annotations on these resources
- With the appropriate Organization role, you can still create and delete
  Projects, manage Organization Groups, and update their supported specs

:::info
If you need an annotation changed (for example, a network configuration override), contact your platform administrator.
:::

## Network Isolation

Every project operates within its own isolated virtual network (VPC). This provides strong security boundaries between projects.

### Project-Level Isolation

- **No direct private cross-Project traffic by default** — Workloads communicate across Projects only through an explicitly exposed endpoint and any required platform allowlist
- **Overlapping IPs** — Different Projects can use the same internal IP ranges without conflict
- **Project-owned addresses** — EIPs and FIPs belong to one Project; an EIP can be cloud-internal or public

### Egress Restrictions

Egress isolation prevents a Project from reaching another tenant through the
platform's shared external segments. Normal internet egress still uses the
Project gateway, while Project-owned EIPs and configured allowlist destinations
remain reachable.

If a required private or platform address is blocked, contact your platform
administrator with the destination IP or CIDR and the business reason for an
allowlist exception.

## RBAC and Access Control

Access to resources within a project is controlled by your assigned role. See [User and Group Management](team-management.md) for details on roles and permissions.

### Role Summary

| Role | Create workloads | Manage RBAC | Read raw Kubernetes Secrets | VM console |
|---|---|---|---|---|
| `admin` | Yes | Yes | Yes | Yes |
| `developer` | Yes | No | Yes | Yes |
| `project-manager` | No | No | Yes | Yes |
| `user` | No | No | No | No |

This table covers raw Kubernetes `Secret` access. [Secrets Manager](secrets-manager.md)
has a separate permission model for `ManagedSecret` metadata and stored values.

### Shell Access

**Containers:** `kubectl exec` and `kubectl attach` are **blocked in Project backing namespaces by design**. Standard project roles do not grant them, and an admission policy enforces the restriction even for custom roles that would add the permission.

For administrative tasks against your application's filesystem, run a **Job** that mounts the same volume (see the WP-CLI and backup Jobs in [Deploy a Full WordPress Stack](deploy-wordpress-stack.md)) — Jobs are first-class in Projects and cover install, migration and backup workflows.

**For virtual machines**, use the **VM console** or **VNC** — available to Admin, Developer, and Project Manager roles:

```bash
# Access VM console
virtctl console my-vm

# Access VM VNC
virtctl vnc my-vm
```

:::info
For your own VMs, use the console or VNC access provided by KubeVirt. For containers, use `kubectl logs` and Jobs.
:::

## Resource Quotas

Each project may have resource quotas that limit the total CPU, memory, and storage your workloads can consume. If you hit a quota limit, you'll see an error like:

```
Error from server (Forbidden): pods "my-pod" is forbidden: exceeded quota
```

To check your project's current quota usage:

```bash
kubectl get resourcequota -n <backing-namespace>
```

If you need more resources, contact your organization administrator to adjust the project quota, or reduce usage by scaling down or deleting unused workloads.

## Troubleshooting

**"hostPath volumes are not allowed in project namespaces"**

Your pod spec uses a `hostPath` volume. Replace it with a `PersistentVolumeClaim` or `emptyDir`:

```yaml
# Instead of this:
volumes:
  - name: data
    hostPath:
      path: /data

---
# Use this:
volumes:
  - name: data
    persistentVolumeClaim:
      claimName: my-pvc

---
# Or for temporary scratch space:
volumes:
  - name: data
    emptyDir: {}
```

**"Privileged containers are not allowed in project namespaces"**

Your container has `securityContext.privileged: true`. Remove this setting — most applications run fine without privileges:

```yaml
# Remove this from your container spec:
securityContext:
  privileged: true    # ← delete this line
```

If the application genuinely needs elevated capabilities, check if a specific Linux capability (e.g., `NET_ADMIN`) is sufficient instead — though note that in Project backing namespaces, running without special capabilities is strongly recommended.

**"hostNetwork is not allowed in project namespaces"**

Your pod uses `hostNetwork: true`. Use a `Service` to expose your workload instead:

```yaml
# Instead of hostNetwork, expose via a Service:
apiVersion: v1
kind: Service
metadata:
  name: my-service
spec:
  type: LoadBalancer    # use ClusterIP for Project-internal access
  selector:
    app: my-app
  ports:
    - port: 80
      targetPort: 8080
```

**"hostPID is not allowed in project namespaces"**

Your pod uses `hostPID: true`. Remove this setting — standard process isolation means your container sees only its own processes, which is sufficient for most workloads.

**"hostIPC is not allowed in project namespaces"**

Your pod uses `hostIPC: true`. Remove this setting — containers within the same pod can communicate via shared volumes or localhost without host IPC.

**"Modifying annotations on this resource is not allowed"**

You attempted to change an annotation on a Project, Organization, or OrganizationGroup. These annotations are managed by the platform and cannot be modified by users. Contact your platform administrator if a change is needed.

**Cannot reach an external service**

Your project may have egress network isolation enabled. This restricts outbound traffic to your project's own external IPs. Contact your platform administrator to add the target IP or CIDR range to your project's egress allowlist.

**"exceeded quota"**

Your project has reached its resource limit. Check current usage with `kubectl get resourcequota -n <namespace>` and free up resources or request a quota increase from your organization administrator.

## Next Steps

- [Core Concepts](core-concepts.md) — Understand the Kube-DC hierarchy
- [User and Group Management](team-management.md) — Manage roles and permissions
- [Deploy Your First Application](deploy-first-app.md) — Get started with workloads
- [Networking Overview](networking-overview.md) — Learn about project networking and external IPs
- [Service Exposure](service-exposure.md) — Expose workloads via LoadBalancer, Ingress, and Gateway API
