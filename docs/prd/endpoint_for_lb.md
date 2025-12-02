# PRD: Automatic External Endpoints for LoadBalancer Services

**Status:** ✅ Implemented  
**Version:** v0.1.34-dev1  
**Date:** 2025-11-19  

## Problem Statement

When using LoadBalancer services in kube-dc multi-tenant VPC environments, external clients (such as Kamaji controllers, CI/CD systems, or other tenants) need stable DNS-based access to these services. Currently, users must:

1. Manually discover the LoadBalancer's external IP address
2. Hardcode IPs in configurations, certificates, and kubeconfigs
3. Manually create and maintain Service/Endpoints pairs for external access
4. Track and update these endpoints whenever LoadBalancer IPs change

This creates operational burden and increases the risk of configuration drift, especially in multi-tenant scenarios where LoadBalancers are frequently created, updated, or recreated.

### Real-World Impact

**Scenario: Kamaji Multi-Tenant Setup**
- Kamaji controller runs in `kamaji-system` namespace
- Tenant control planes run in tenant VPC namespaces (e.g., `shalb-envoy`)
- etcd cluster exposed via LoadBalancer with external IP `168.119.17.55`
- Kamaji **cannot** reach `etcd.shalb-envoy.svc.cluster.local` (internal ClusterIP) due to network isolation
- Must use external IP `168.119.17.55:2379` in DataStore configuration
- When LoadBalancer is recreated, IP changes, breaking all references

**Current Workaround:**
```yaml
# Manual Service + Endpoints creation
apiVersion: v1
kind: Service
metadata:
  name: etcd-lb-ext
  namespace: shalb-envoy
spec:
  type: ClusterIP
  clusterIP: None
  ports:
  - port: 2379
---
apiVersion: v1
kind: Endpoints
metadata:
  name: etcd-lb-ext
  namespace: shalb-envoy
subsets:
  - addresses:
      - ip: 168.119.17.55  # Must be manually updated!
    ports:
      - port: 2379
```

Then Kamaji can use: `etcd-lb-ext.shalb-envoy.svc.cluster.local:2379`

## Implemented Solution

**Automatically create and manage external endpoints for every LoadBalancer service** managed by kube-dc. The service controller:

1. **Creates** a headless Service + Endpoints pair when a LoadBalancer is created
2. **Updates** the Endpoints IP when the LoadBalancer's external IP changes
3. **Deletes** the external endpoint pair when the LoadBalancer is deleted
4. **Supports multiple endpoints** if a LoadBalancer has multiple external IPs

### Naming Convention

For a LoadBalancer service named `<service-name>`, create:
- **Service**: `<service-name>-ext` (headless ClusterIP: None)
- **Endpoints**: `<service-name>-ext` (pointing to LoadBalancer external IP)

Examples:
- `etcd-lb` → `etcd-lb-ext.shalb-envoy.svc.cluster.local`
- `cluster-a-cp` → `cluster-a-cp-ext.shalb-envoy.svc.cluster.local`
- `api-gateway` → `api-gateway-ext.tenant-ns.svc.cluster.local`

### Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│ kube-dc Service Controller (internal/controller/core/service_controller.go) │
└─────────────────────────────────────────────────────────────────┘
                              │
                              ├─ Reconcile LoadBalancer Service
                              │
                ┌─────────────┴────────────────┐
                │                              │
                ▼                              ▼
    ┌────────────────────┐        ┌────────────────────────┐
    │ EIP Management     │        │ External Endpoint Mgmt │
    │ (existing)         │        │ (NEW)                  │
    ├────────────────────┤        ├────────────────────────┤
    │ - Create EIP       │        │ - Create Service-ext   │
    │ - Bind to LB       │        │ - Create Endpoints     │
    │ - Update status    │        │ - Update on IP change  │
    │ - Delete on remove │        │ - Delete on LB delete  │
    └────────────────────┘        └────────────────────────┘
                │                              │
                └──────────────┬───────────────┘
                               ▼
                    LoadBalancer gets external IP
                               │
                ┌──────────────┴──────────────┐
                ▼                             ▼
    ┌─────────────────────┐      ┌─────────────────────┐
    │ Service: etcd-lb    │      │ Service: etcd-lb-ext│
    │ Type: LoadBalancer  │      │ Type: ClusterIP     │
    │ ExternalIP:         │      │ ClusterIP: None     │
    │   168.119.17.55     │      │                     │
    └─────────────────────┘      └─────────────────────┘
                                           │
                                           ▼
                              ┌──────────────────────────┐
                              │ Endpoints: etcd-lb-ext   │
                              │ IP: 168.119.17.55        │
                              │ Port: 2379               │
                              └──────────────────────────┘
```

## Technical Specification

### 1. Service Resource Structure

```yaml
apiVersion: v1
kind: Service
metadata:
  name: <service-name>-ext
  namespace: <service-namespace>
  labels:
    kube-dc.com/managed-by: service-lb-controller
    kube-dc.com/source-service: <service-name>
    kube-dc.com/endpoint-type: external
  ownerReferences:
  - apiVersion: v1
    kind: Service
    name: <service-name>
    uid: <service-uid>
    controller: true
    blockOwnerDeletion: true
spec:
  type: ClusterIP
  clusterIP: None  # Headless service
  ports:
  - name: <port-name>
    port: <port>
    protocol: <protocol>
  # Copy all ports from source LoadBalancer service
```

### 2. Endpoints Resource Structure

```yaml
apiVersion: v1
kind: Endpoints
metadata:
  name: <service-name>-ext
  namespace: <service-namespace>
  labels:
    kube-dc.com/managed-by: service-lb-controller
    kube-dc.com/source-service: <service-name>
    kube-dc.com/endpoint-type: external
  ownerReferences:
  - apiVersion: v1
    kind: Service
    name: <service-name>-ext
    uid: <service-ext-uid>
    controller: true
    blockOwnerDeletion: true
subsets:
  - addresses:
      - ip: <loadbalancer-external-ip-1>
      - ip: <loadbalancer-external-ip-2>  # If multiple IPs
    ports:
      - name: <port-name>
        port: <port>
        protocol: <protocol>
```

### 3. Controller Logic (Implemented)

#### File: `internal/service_lb/external_endpoint.go`

```go
package servicelb

import (
    "context"
    "fmt"

    corev1 "k8s.io/api/core/v1"
    metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
    "sigs.k8s.io/controller-runtime/pkg/client"
    "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
    ExternalEndpointSuffix = "-ext"
    ManagedByLabel         = "kube-dc.com/managed-by"
    SourceServiceLabel     = "kube-dc.com/source-service"
    EndpointTypeLabel      = "kube-dc.com/endpoint-type"
    ControllerName         = "service-lb-controller"
)

// ExternalEndpointManager manages external endpoint resources for LoadBalancer services
type ExternalEndpointManager struct {
    client.Client
    Service *corev1.Service
}

// Sync creates or updates external Service and Endpoints
func (m *ExternalEndpointManager) Sync(ctx context.Context) error {
    if m.Service.Spec.Type != corev1.ServiceTypeLoadBalancer {
        return nil // Only for LoadBalancer services
    }

    externalIPs := m.getExternalIPs()
    if len(externalIPs) == 0 {
        // LoadBalancer not ready yet, skip
        return nil
    }

    // Create or update external Service
    if err := m.syncExternalService(ctx); err != nil {
        return fmt.Errorf("failed to sync external service: %w", err)
    }

    // Create or update Endpoints
    if err := m.syncEndpoints(ctx, externalIPs); err != nil {
        return fmt.Errorf("failed to sync endpoints: %w", err)
    }

    return nil
}

// Delete removes external Service and Endpoints
func (m *ExternalEndpointManager) Delete(ctx context.Context) error {
    extSvcName := m.getExternalServiceName()

    // Delete external Service (Endpoints will be cascaded via ownerReference)
    extSvc := &corev1.Service{
        ObjectMeta: metav1.ObjectMeta{
            Name:      extSvcName,
            Namespace: m.Service.Namespace,
        },
    }

    if err := m.Client.Delete(ctx, extSvc); client.IgnoreNotFound(err) != nil {
        return fmt.Errorf("failed to delete external service: %w", err)
    }

    return nil
}

func (m *ExternalEndpointManager) getExternalServiceName() string {
    return m.Service.Name + ExternalEndpointSuffix
}

func (m *ExternalEndpointManager) getExternalIPs() []string {
    ips := []string{}
    for _, ingress := range m.Service.Status.LoadBalancer.Ingress {
        if ingress.IP != "" {
            ips = append(ips, ingress.IP)
        }
    }
    return ips
}

func (m *ExternalEndpointManager) syncExternalService(ctx context.Context) error {
    extSvcName := m.getExternalServiceName()
    extSvc := &corev1.Service{
        ObjectMeta: metav1.ObjectMeta{
            Name:      extSvcName,
            Namespace: m.Service.Namespace,
        },
    }

    _, err := controllerutil.CreateOrUpdate(ctx, m.Client, extSvc, func() error {
        // Set labels
        if extSvc.Labels == nil {
            extSvc.Labels = make(map[string]string)
        }
        extSvc.Labels[ManagedByLabel] = ControllerName
        extSvc.Labels[SourceServiceLabel] = m.Service.Name
        extSvc.Labels[EndpointTypeLabel] = "external"

        // Set controller reference (ensures garbage collection)
        if err := controllerutil.SetControllerReference(m.Service, extSvc, m.Scheme()); err != nil {
            return err
        }

        // Configure as headless service
        extSvc.Spec.Type = corev1.ServiceTypeClusterIP
        extSvc.Spec.ClusterIP = corev1.ClusterIPNone

        // Copy ports from source service
        extSvc.Spec.Ports = []corev1.ServicePort{}
        for _, port := range m.Service.Spec.Ports {
            extSvc.Spec.Ports = append(extSvc.Spec.Ports, corev1.ServicePort{
                Name:     port.Name,
                Port:     port.Port,
                Protocol: port.Protocol,
            })
        }

        return nil
    })

    return err
}

func (m *ExternalEndpointManager) syncEndpoints(ctx context.Context, externalIPs []string) error {
    extSvcName := m.getExternalServiceName()
    endpoints := &corev1.Endpoints{
        ObjectMeta: metav1.ObjectMeta{
            Name:      extSvcName,
            Namespace: m.Service.Namespace,
        },
    }

    _, err := controllerutil.CreateOrUpdate(ctx, m.Client, endpoints, func() error {
        // Set labels
        if endpoints.Labels == nil {
            endpoints.Labels = make(map[string]string)
        }
        endpoints.Labels[ManagedByLabel] = ControllerName
        endpoints.Labels[SourceServiceLabel] = m.Service.Name
        endpoints.Labels[EndpointTypeLabel] = "external"

        // Get external service for owner reference
        extSvc := &corev1.Service{}
        if err := m.Client.Get(ctx, client.ObjectKey{
            Name:      extSvcName,
            Namespace: m.Service.Namespace,
        }, extSvc); err != nil {
            return fmt.Errorf("failed to get external service: %w", err)
        }

        // Set controller reference to external service
        if err := controllerutil.SetControllerReference(extSvc, endpoints, m.Scheme()); err != nil {
            return err
        }

        // Build addresses
        addresses := []corev1.EndpointAddress{}
        for _, ip := range externalIPs {
            addresses = append(addresses, corev1.EndpointAddress{
                IP: ip,
            })
        }

        // Build ports
        ports := []corev1.EndpointPort{}
        for _, port := range m.Service.Spec.Ports {
            ports = append(ports, corev1.EndpointPort{
                Name:     port.Name,
                Port:     port.Port,
                Protocol: port.Protocol,
            })
        }

        // Set subsets
        endpoints.Subsets = []corev1.EndpointSubset{
            {
                Addresses: addresses,
                Ports:     ports,
            },
        }

        return nil
    })

    return err
}
```

#### Integration in `service_controller.go`

```go
// In reconcileSync function, after EIP and LoadBalancer sync:

func (r *ServiceReconciler) reconcileSync(ctx context.Context, req ctrl.Request, svc *corev1.Service, endpoints *corev1.Endpoints, project *kubedccomv1.Project) (ctrl.Result, error) {
    log := log.FromContext(ctx).WithName("Sync:").WithValues("ServiceLoadBalancer", req.Name)
    
    // ... existing EIP sync ...
    
    // ... existing LoadBalancer sync ...
    
    // ... existing external IP status update ...
    
    // NEW: Sync external endpoints for cross-VPC access
    extEndpointMgr := &serviceLb.ExternalEndpointManager{
        Client:  r.Client,
        Service: svc,
    }
    if err := extEndpointMgr.Sync(ctx); err != nil {
        log.Error(err, "Failed to sync external endpoints")
        // Don't fail the reconciliation, just log the error
    }
    
    return ctrl.Result{}, nil
}

// In reconcileDelete function:

func (r *ServiceReconciler) reconcileDelete(ctx context.Context, req ctrl.Request, svc *corev1.Service, endpoints *corev1.Endpoints, project *kubedccomv1.Project) (ctrl.Result, error) {
    log := log.FromContext(ctx).WithName("Delete:").WithValues("ServiceLoadBalancer", req.Name)
    
    // NEW: Delete external endpoints first
    extEndpointMgr := &serviceLb.ExternalEndpointManager{
        Client:  r.Client,
        Service: svc,
    }
    if err := extEndpointMgr.Delete(ctx); err != nil {
        log.Error(err, "Failed to delete external endpoints")
    }
    
    // ... existing EIP and LoadBalancer delete logic ...
    
    return ctrl.Result{}, nil
}
```

### 4. RBAC Permissions

Add to `config/rbac/role.yaml`:

```yaml
- apiGroups: [""]
  resources: ["services", "endpoints"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```

## Benefits

### 1. Operational Simplicity
- **Zero manual intervention**: Endpoints created/updated automatically
- **Self-healing**: Endpoints always reflect current LoadBalancer IPs
- **Consistent naming**: Predictable `-ext` suffix convention

### 2. Multi-Tenant Support
- **Cross-VPC access**: External endpoints work across network boundaries
- **Stable DNS**: Use `<service>-ext.namespace.svc.cluster.local` in all configs
- **No IP hardcoding**: Certificates, kubeconfigs, and DataStores use DNS names

### 3. Resilience
- **Owner references**: Automatic cleanup when services are deleted
- **Reconciliation**: Controller ensures consistency even after disruptions
- **Multiple IPs**: Supports LoadBalancers with multiple external IPs

## Use Cases

### 1. Kamaji Multi-Tenant Control Planes ⭐ PRIMARY USE CASE

**Problem:** Kamaji controller in `kamaji-system` cannot reach etcd in tenant VPC via ClusterIP.

**Current (manual):**
```yaml
apiVersion: kamaji.clastix.io/v1alpha1
kind: DataStore
metadata:
  name: shalb-envoy-etcd
spec:
  driver: etcd
  endpoints:
  - 168.119.17.55:2379  # ❌ Hardcoded IP - breaks when service recreated
```

**With auto-managed endpoints:**
```yaml
apiVersion: kamaji.clastix.io/v1alpha1
kind: DataStore
metadata:
  name: shalb-envoy-etcd
spec:
  driver: etcd
  endpoints:
  - etcd-lb-ext.shalb-envoy.svc.cluster.local:2379  # ✅ Stable DNS name
  tlsConfig:
    # ... certificates with DNS SANs (not IP SANs)
```

**How it works:**
1. LoadBalancer `etcd-lb` gets external IP `168.119.17.55`
2. Controller auto-creates Service `etcd-lb-ext` (headless)
3. Controller auto-creates Endpoints `etcd-lb-ext` pointing to `168.119.17.55`
4. Kamaji resolves `etcd-lb-ext.shalb-envoy.svc.cluster.local` → `168.119.17.55`
5. When IP changes, controller updates Endpoints automatically
6. **No DataStore configuration change needed!**

### 2. Cross-Tenant API Access
```yaml
# Tenant A accessing Tenant B's API
apiVersion: v1
kind: ConfigMap
metadata:
  name: tenant-b-access
  namespace: tenant-a
data:
  api-endpoint: https://api-gateway-ext.tenant-b.svc.cluster.local:8443
```

### 3. CI/CD Integration
```bash
# CI pipeline can use stable DNS names
kubectl --kubeconfig=/tmp/kubeconfig \
  --server=https://cluster-ext.tenant-prod.svc.cluster.local:6443 \
  get nodes
```

## Testing Strategy

### Unit Tests
- Test Service creation with correct naming and labels
- Test Endpoints creation with correct IPs and ports
- Test update when LoadBalancer IP changes
- Test deletion and cleanup
- Test multiple external IPs

### Integration Tests
1. Create LoadBalancer service
2. Verify `-ext` Service and Endpoints are created
3. Verify DNS resolves to external IP
4. Update LoadBalancer (trigger IP change)
5. Verify Endpoints updated with new IP
6. Delete LoadBalancer
7. Verify `-ext` resources cleaned up

### E2E Tests
- Deploy Kamaji with multi-tenant setup
- Verify etcd DataStore works with `-ext` endpoint
- Verify TenantControlPlane can access etcd
- Simulate IP change and verify automatic reconciliation

## Implementation Status

### Phase 1: Core Implementation ✅ Complete
- [x] Create `external_endpoint.go` with manager logic
- [x] Integrate into `service_controller.go`
- [x] Add RBAC permissions for endpoints
- [x] Documentation in `docs/tutorial-ip-and-lb.md`
- [x] Documentation in `docs/architecture-networking.md`

### Phase 2: Deployment & Testing ✅ Complete
- [x] Deploy to staging environment (v0.1.34-dev1)
- [x] Verified 10+ LoadBalancer services automatically got external endpoints
- [x] DNS resolution tested and working

### Test Results (2025-11-19)

```bash
# All LoadBalancer services automatically got -ext endpoints:
$ kubectl get endpoints -A --selector=kube-dc.com/managed-by=service-lb-controller
NAMESPACE     NAME                                     ENDPOINTS
shalb-dev     etcd-lb-ext                              168.119.17.51:2379
shalb-dev     kamaji-demo-cp-ext                       168.119.17.59:6443
shalb-dev     debug-net-lb-ext                         168.119.17.51:80,168.119.17.51:443
shalb-envoy   cluster-a-cp-ext                         168.119.17.53:6443
shalb-envoy   etcd-lb-ext                              168.119.17.55:2379,168.119.17.55:6443
...

# DNS resolution verified:
$ kubectl run -it --rm debug --image=busybox -- nslookup etcd-lb-ext.shalb-dev.svc.cluster.local
Name:   etcd-lb-ext.shalb-dev.svc.cluster.local
Address: 168.119.17.51
```

## Backwards Compatibility

This feature is **fully backwards compatible**:
- Existing LoadBalancer services continue to work unchanged
- External endpoints are **additive** (new resources only)
- No breaking changes to existing APIs or configurations
- Users can opt-out by deleting the `-ext` resources (controller will recreate, but won't affect original service)

## Success Metrics

- **Automation rate**: 100% of LoadBalancer services have external endpoints
- **Manual interventions**: Reduce IP update operations to zero
- **Reconciliation time**: External endpoints updated within 10 seconds of IP change
- **Error rate**: < 0.1% endpoint sync failures

## Future Enhancements

1. **Configurable naming**: Annotation to customize `-ext` suffix
2. **Selective enablement**: Annotation to opt-in/opt-out per service
3. **External DNS integration**: Automatically create DNS records
4. **Metrics**: Prometheus metrics for endpoint sync operations
5. **Webhook validation**: Prevent manual modification of managed resources
6. **Kamaji DataStore CRD enhancement**: Add `externalNetworkType` field to Kamaji DataStore CRD to allow users to specify which external network type (`cloud` or `public`) to use when connecting via external endpoints. This ensures proper network routing and IP allocation matching the infrastructure requirements

## Related Files

- `internal/service_lb/external_endpoint.go` - External endpoint manager implementation
- `internal/controller/core/service_controller.go` - Service controller integration
- `docs/tutorial-ip-and-lb.md` - User documentation
- `docs/architecture-networking.md` - Architecture documentation

## Related Documentation

- Kamaji Multi-Tenant Architecture: `/examples/kamaji-capi/mt/README.md`
- Service LoadBalancer Architecture: `/docs/prd/svc_lb_architecture.md`
- EIP Management: `/internal/eip/`
