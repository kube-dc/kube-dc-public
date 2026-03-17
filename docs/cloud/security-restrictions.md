# Security Restrictions

Kube-DC enforces security boundaries in every project to protect the underlying infrastructure and ensure tenant isolation. This page explains what restrictions apply to your workloads and how to work within them.

## Pod Security

To prevent workloads from compromising the shared infrastructure, certain pod configurations are restricted in all project namespaces.

### What's Restricted

| Feature | Why it's blocked | What to use instead |
|---------|-----------------|-------------------|
| **Host path volumes** | Grants direct access to the node's filesystem, allowing reads/writes to host data | Use **PersistentVolumeClaims** for persistent storage, **ConfigMaps** or **Secrets** for configuration, **emptyDir** for temporary scratch space |
| **Privileged containers** | Grants full root-level access to the host, bypassing all security boundaries | Run your container with standard (unprivileged) settings — most applications work without privileges |
| **Host networking** | Exposes the node's network interfaces and bypasses network isolation | Use standard pod networking with **Services** (ClusterIP, LoadBalancer) to expose your workloads |
| **Host PID** | Allows visibility into all processes on the node, including other tenants | Use standard process isolation — your container sees only its own processes |
| **Host IPC** | Shares the node's inter-process communication namespace | Use standard IPC — containers within the same pod can already communicate via shared volumes or localhost |

### What You Can Do

These restrictions only apply to dangerous host-level features. Normal Kubernetes workflows are fully supported:

- **Deploy containers** — Pods, Deployments, StatefulSets, DaemonSets, Jobs, CronJobs
- **Manage storage** — PersistentVolumeClaims (block storage), emptyDir, ConfigMaps, Secrets
- **Expose services** — LoadBalancer, ClusterIP, Ingress, Gateway API routes
- **Create virtual machines** — KubeVirt VMs with full lifecycle management (start, stop, restart, migrate, console, VNC)
- **Manage Kubernetes clusters** — Create and scale KDC clusters within your project
- **View logs** — `kubectl logs` works normally for all your pods
- **Monitor resources** — `kubectl top pods` for resource usage metrics
- **Manage certificates** — Create cert-manager Issuers and Certificates
- **Configure autoscaling** — HorizontalPodAutoscalers for automatic scaling
- **Backups** — Velero backup schedules and restores

:::tip
If you receive a message like *"hostPath volumes are not allowed in project namespaces"* when creating a pod, review your pod spec and replace the restricted feature with one of the alternatives listed above.
:::

## Resource Annotations

System-managed annotations on **Projects**, **Organizations**, and **Organization Groups** are read-only for users. These annotations are used internally for billing, network configuration, and controller state.

**What this means:**
- You **cannot** add, change, or remove annotations on these resources
- You **can** still create and delete Projects, manage Organization Groups, and update resource specs normally

:::info
If you need an annotation changed (for example, a network configuration override), contact your platform administrator.
:::

## Network Isolation

Every project operates within its own isolated virtual network (VPC). This provides strong security boundaries between projects.

### Project-Level Isolation

- **No cross-project traffic** — VMs and pods in one project cannot communicate with workloads in another project, even within the same organization
- **Overlapping IPs** — You can use the same internal IP ranges in different projects without conflict
- **Dedicated external IPs** — Each project gets its own External IPs (EIPs) and Floating IPs (FIPs) for public connectivity

### Egress Restrictions

Your project's outbound (egress) traffic may be restricted to only reach your project's own external IP addresses. This prevents one project from sending traffic through another project's network resources.

If you need to reach an external IP address or service that is being blocked:
- Check that your project has an **External IP (EIP)** assigned for the target network
- Contact your platform administrator to request an **egress allowlist exception** for specific IP addresses or CIDR ranges

## RBAC and Access Control

Access to resources within a project is controlled by your assigned role. See [User and Group Management](team-management.md) for details on roles and permissions.

### Role Summary

| Role | Can create workloads | Can manage RBAC | Can manage secrets | Can access VM console |
|------|---------------------|----------------|-------------------|---------------------|
| **Admin** | ✅ | ✅ | ✅ | ✅ |
| **Developer** | ✅ | ❌ | Read-only | ✅ |
| **Project Manager** | ❌ (read-only) | ❌ | Read-only | ✅ |
| **User** | ❌ (read-only) | ❌ | ❌ | ❌ |

### Shell Access

None of the standard roles include `kubectl exec` or `kubectl attach` permissions. This means you cannot open a shell inside running containers via `kubectl exec`.

**For virtual machines**, use the **VM console** or **VNC** instead — these are available to Admin, Developer, and Project Manager roles:

```bash
# Access VM console
virtctl console my-vm

# Access VM VNC
virtctl vnc my-vm
```

:::info
Shell access restrictions protect sensitive credentials and runtime state inside system containers. For your own VMs, use the console or VNC access provided by KubeVirt.
:::

## Resource Quotas

Each project may have resource quotas that limit the total CPU, memory, and storage your workloads can consume. If you hit a quota limit, you'll see an error like:

```
Error from server (Forbidden): pods "my-pod" is forbidden: exceeded quota
```

To check your project's current quota usage:

```bash
kubectl get resourcequota -n <project-namespace>
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

# Use this:
volumes:
  - name: data
    persistentVolumeClaim:
      claimName: my-pvc

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

If the application genuinely needs elevated capabilities, check if a specific Linux capability (e.g., `NET_ADMIN`) is sufficient instead — though note that in project namespaces, running without special capabilities is strongly recommended.

**"hostNetwork is not allowed in project namespaces"**

Your pod uses `hostNetwork: true`. Use a `Service` to expose your workload instead:

```yaml
# Instead of hostNetwork, expose via a Service:
apiVersion: v1
kind: Service
metadata:
  name: my-service
spec:
  type: LoadBalancer    # or ClusterIP, NodePort
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
