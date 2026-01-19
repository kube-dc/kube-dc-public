# Auth Config Sync to Control Plane Nodes

## Problem Statement

The kube-dc manager writes OIDC authentication configuration (`auth-conf.yaml`) to the host filesystem. This configuration is read by the Kubernetes API server for OIDC authentication. However, in a multi-master setup, the manager runs on only one node (via leader election), leaving other control plane nodes without the updated auth configuration.

### Current Behavior
- Manager writes `auth-conf.yaml` to `/etc/rancher/auth-conf.yaml` on its local node
- Other control plane nodes have stale or missing auth configuration
- Users authenticating against API servers on other nodes may fail or see outdated realm configurations

### Impact
- Authentication failures after organization creation/deletion
- Inconsistent login behavior depending on which API server handles the request
- Delayed propagation of OIDC configuration changes

## Requirements

### Functional Requirements

| ID | Requirement | Priority |
|----|-------------|----------|
| FR-1 | Auth config must be synchronized to ALL control plane nodes | High |
| FR-2 | Sync must occur within 2 seconds of configuration change | High |
| FR-3 | Solution must work with existing leader election model | High |
| FR-4 | Solution must handle node additions/removals gracefully | Medium |
| FR-5 | Solution must be deployed via existing Helm chart | Medium |

### Non-Functional Requirements

| ID | Requirement | Priority |
|----|-------------|----------|
| NFR-1 | Minimal resource overhead (< 50m CPU, < 32Mi memory per node) | Medium |
| NFR-2 | No external dependencies (SSH, shared storage) | High |
| NFR-3 | Solution must be idempotent and crash-safe | High |

## Solution Design

### Architecture

```
┌─────────────────────────────────────────────────────────────────────────┐
│                        Control Plane Node 1                              │
│  ┌──────────────┐    writes    ┌──────────────────┐                     │
│  │   Manager    │─────────────►│   ConfigMap      │                     │
│  │   (Leader)   │              │ kube-dc-auth-cfg │                     │
│  └──────────────┘              └────────┬─────────┘                     │
│                                         │                                │
│  ┌──────────────┐    mounts    ┌────────▼─────────┐    writes           │
│  │  auth-sync   │◄─────────────│  ConfigMap Vol   │───────────►/etc/... │
│  │  DaemonSet   │   inotify    └──────────────────┘                     │
│  └──────────────┘                                                        │
└─────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────┐
│                        Control Plane Node 2                              │
│                                                                          │
│  ┌──────────────┐    mounts    ┌──────────────────┐    writes           │
│  │  auth-sync   │◄─────────────│  ConfigMap Vol   │───────────►/etc/... │
│  │  DaemonSet   │   inotify    └──────────────────┘                     │
│  └──────────────┘                                                        │
└─────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────┐
│                        Control Plane Node 3                              │
│                                                                          │
│  ┌──────────────┐    mounts    ┌──────────────────┐    writes           │
│  │  auth-sync   │◄─────────────│  ConfigMap Vol   │───────────►/etc/... │
│  │  DaemonSet   │   inotify    └──────────────────┘                     │
│  └──────────────┘                                                        │
└─────────────────────────────────────────────────────────────────────────┘
```

### Components

#### 1. Manager (Modified)
- **Change**: After writing `auth-conf.yaml` to local file, also write to ConfigMap
- **ConfigMap Name**: `kube-dc-auth-config`
- **ConfigMap Key**: `auth-conf.yaml`
- **Namespace**: `kube-dc` (same as manager)

#### 2. Auth Config Sync DaemonSet (New)
- **Name**: `kube-dc-auth-config-sync`
- **Runs on**: Control plane nodes only (`node-role.kubernetes.io/control-plane`)
- **Image**: `busybox:1.36` (lightweight, has inotifyd)
- **Function**: Watch ConfigMap volume mount, copy to host path on change

### Data Flow

1. Organization created/updated/deleted
2. Manager reconciles and updates `auth-conf.yaml`
3. Manager writes content to local file (existing behavior)
4. Manager writes content to ConfigMap `kube-dc-auth-config`
5. Kubernetes propagates ConfigMap to all volume mounts (~1-2 seconds)
6. DaemonSet pod on each node detects hash change (polling every 2s)
7. DaemonSet copies file to host path `/etc/rancher/auth-conf.yaml` with 0666 permissions
8. API server reloads OIDC config (Kubernetes 1.30+ dynamic reload)

### Timing Analysis

| Step | Duration |
|------|----------|
| Manager writes ConfigMap | < 100ms |
| Kubelet syncs ConfigMap to volume | ~1-2 seconds |
| Polling detects change | < 2 seconds |
| **Total end-to-end** | **~1-2 seconds** |

## Implementation Plan

### Phase 1: Modify Manager

1. Add Kubernetes client to `KubeAuthClient` struct
2. Create `saveToConfigMap()` method
3. Update `SaveAuthFile()` to also call `saveToConfigMap()`
4. Handle ConfigMap create/update logic

**Files to modify:**
- `internal/organization/client_kube_auth.go`
- `internal/organization/res_kube_auth.go`
- `internal/organization/organization.go`

### Phase 2: Create DaemonSet

1. Create Helm template for DaemonSet
2. Add values.yaml configuration
3. Configure:
   - nodeSelector for control-plane nodes
   - tolerations for control-plane taints
   - hostPath volume mount
   - ConfigMap volume mount
   - inotifyd watch script

**Files to create/modify:**
- `charts/kube-dc/templates/auth-config-sync-daemonset.yaml` (new)
- `charts/kube-dc/values.yaml`

### Phase 3: Testing

1. Deploy to test cluster with 3 control plane nodes
2. Create new organization
3. Verify auth config appears on all nodes within 2 seconds
4. Delete organization
5. Verify auth config updated on all nodes
6. Test node failure/recovery scenarios

## DaemonSet Specification

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: kube-dc-auth-config-sync
spec:
  selector:
    matchLabels:
      app: kube-dc-auth-config-sync
  template:
    spec:
      nodeSelector:
        node-role.kubernetes.io/control-plane: ""
      tolerations:
      - key: node-role.kubernetes.io/control-plane
        operator: Exists
        effect: NoSchedule
      - key: node-role.kubernetes.io/master
        operator: Exists
        effect: NoSchedule
      containers:
      - name: sync
        image: busybox:1.36
        command: ["/bin/sh", "-c"]
        args:
        - |
          echo "Starting auth config sync daemon..."
          TARGET="/etc/rancher/auth-conf.yaml"
          SOURCE="/config/auth-conf.yaml"
          LAST_HASH=""
          
          sync_file() {
            if [ -f "$SOURCE" ]; then
              CURRENT_HASH=$(md5sum "$SOURCE" 2>/dev/null | cut -d' ' -f1)
              if [ "$CURRENT_HASH" != "$LAST_HASH" ]; then
                cp "$SOURCE" "$TARGET"
                chmod 0666 "$TARGET"  # Allow non-root manager to write
                LAST_HASH="$CURRENT_HASH"
                echo "$(date): Synced $SOURCE -> $TARGET (hash: $CURRENT_HASH)"
              fi
            fi
          }
          
          # Initial sync
          sync_file
          
          # Poll for changes every 2 seconds
          # (ConfigMap symlink updates don't trigger inotify reliably)
          echo "Watching for changes..."
          while true; do
            sync_file
            sleep 2
          done
        securityContext:
          privileged: true
          runAsUser: 0
        volumeMounts:
        - name: config
          mountPath: /config
        - name: host-path
          mountPath: /etc/rancher
        resources:
          limits:
            cpu: 50m
            memory: 32Mi
          requests:
            cpu: 10m
            memory: 16Mi
      volumes:
      - name: config
        configMap:
          name: kube-dc-auth-config
          optional: true
      - name: host-path
        hostPath:
          path: /etc/rancher
          type: DirectoryOrCreate
```

## Rollback Plan

If issues occur:
1. Delete the DaemonSet: `kubectl delete daemonset kube-dc-auth-config-sync -n kube-dc`
2. Manager continues writing to local file (existing behavior)
3. Manually copy auth config to other nodes if needed

## Success Criteria

- [x] Auth config synced to all control plane nodes within 2-4 seconds
- [x] No authentication failures after organization changes
- [x] DaemonSet uses < 50m CPU, < 32Mi memory per pod
- [x] Solution works with 1, 3, or more control plane nodes
- [x] Helm chart upgrade is seamless

## Single Master Compatibility

For single master installations, the solution works identically:
- Manager writes to local file (existing behavior)
- Manager writes to ConfigMap (new)
- DaemonSet copies ConfigMap to host path (overwrites same file)

This is intentionally redundant but safe - the same deployment works for any number of masters without conditional logic. When additional masters are added, sync works automatically.

## Open Questions

1. **ConfigMap size limit**: Auth config with 100+ organizations - will it exceed 1MB limit?
   - *Estimate*: ~500 bytes per organization = 50KB for 100 orgs. Well within limit.

2. **Initial bootstrap**: How to handle first deployment when ConfigMap doesn't exist?
   - *Solution*: DaemonSet uses `optional: true` for ConfigMap mount, waits for it to appear.

## References

- [Kubernetes 1.30 Structured Authentication](https://kubernetes.io/docs/reference/access-authn-authz/authentication/#using-authentication-configuration)
- [Kubernetes ConfigMap Updates](https://kubernetes.io/docs/concepts/configuration/configmap/#mounted-configmaps-are-updated-automatically)
- [DaemonSet Documentation](https://kubernetes.io/docs/concepts/workloads/controllers/daemonset/)
