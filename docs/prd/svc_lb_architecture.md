# Service LoadBalancer Architecture in Kube-DC

**Status:** ✅ Documented & Misconceptions Resolved  
**Last Updated:** 2025-12-02  

## Summary

Service LoadBalancers in kube-dc work **without requiring VPC policy routes or NAT rules**. The load balancer VIPs are directly accessible through the OVN router's external interface.

## How It Works

### 1. Network Topology

```
External Network (168.119.17.48/28)
         ↓
OVN Router Port: shalb-dev-ext-public
  MAC: ae:d6:b9:e1:76:78
  Network: 168.119.17.51/28
         ↓
OVN Load Balancer VIPs (168.119.17.51-.63)
         ↓
Backend Pods/VMs in Project VPC
```

### 2. EIP Allocation for Service LoadBalancers

When a Service of type `LoadBalancer` is created:

1. **kube-dc Service Controller** (`internal/controller/core/service_controller.go`):
   - Detects the LoadBalancer service
   - Creates an `EIp` resource

2. **kube-dc EIP Controller** (`internal/eip/ovn_eip_res.go`):
   - Creates an `OvnEip` resource
   - Allocates an IP from the external subnet (e.g., `ext-public`)
   - Type: `lrp` (Logical Router Port)

3. **Kube-OVN** (upstream controller):
   - Does NOT create NAT rules for `lrp` type EIPs when used with load balancers
   - The IP becomes part of the router's external interface subnet

### 3. Load Balancer VIP Configuration

**kube-dc Service LB Controller** (`internal/service_lb/service_lb.go`):

```go
// Creates OVN load balancer
vipKey := fmt.Sprintf("%s:%d", r.ipAddress, port.Port)  // e.g., "168.119.17.55:80"
backends := "10.1.0.40:31416,10.1.0.41:31416"  // Pod/VM IPs:ports

// Attaches to BOTH router and logical switch
ovsCli.LogicalRouterUpdateLoadBalancers(r.projectRouter.Name, ...)  // shalb-dev
ovsCli.LogicalSwitchUpdateLoadBalancers(project.SubnetName(r.project), ...)  // shalb-dev-default
```

### 4. Traffic Flow

#### External Client → Service

```
1. Client sends packet to 168.119.17.55:80
2. ARP resolution: Who has 168.119.17.55?
3. OVN router responds: ae:d6:b9:e1:76:78 (my MAC)
4. Packet arrives at router's external port
5. OVN load balancer intercepts (VIP match)
6. Packet DNAT'd to backend: 10.1.0.40:31416
7. Response SNAT'd back through VIP
8. Client receives response from 168.119.17.55:80
```

#### Internal Pod → Service (ClusterIP)

Normal kube-proxy/Cilium routing within the cluster.

## Key Differences from FIP

| Feature | Service LoadBalancer (lrp EIP) | Floating IP (FIP) |
|---------|-------------------------------|-------------------|
| **OvnEip Type** | `lrp` | `lrp` |
| **NAT Rules** | None (uses LB VIP) | `dnat_and_snat` |
| **Policy Routes** | Not needed | Required for outbound |
| **Annotation** | `ovn.kubernetes.io/vpc_nat` NOT needed | `ovn.kubernetes.io/vpc_nat: {vpc}-{fip-name}` required |
| **Use Case** | Inbound load balancing | Bidirectional NAT to VM/Pod |
| **Status.nat** | `""` (empty) | `fip` |

## Verification Commands

### Check Service LoadBalancer

```bash
# 1. Verify Service has external IP
kubectl get svc -n {namespace} {service-name}

# 2. Check EIP resource
kubectl get eip -n {namespace} | grep slb-

# 3. Verify OvnEip (no vpc_nat needed)
EIP_NAME=$(kubectl get eip -n {namespace} {eip-name} -o jsonpath='{.status.ovnEIpRef}')
kubectl get ovn-eip $EIP_NAME -o yaml

# 4. Check OVN load balancer
kubectl exec -n kube-system ovn-central-xxx -- ovn-nbctl lb-list | grep {namespace}

# 5. Verify router attachment
kubectl exec -n kube-system ovn-central-xxx -- ovn-nbctl lr-lb-list {namespace}

# 6. Check ARP resolution (from gateway/bastion)
arp -n | grep {external-ip}
# Should show router MAC: ae:d6:b9:e1:76:78 (for shalb-dev)

# 7. Test connectivity
curl http://{external-ip}:{port}
```

### Check VPC Router Configuration

```bash
# Show router ports and subnet
kubectl exec -n kube-system ovn-central-xxx -- ovn-nbctl show {vpc-name}

# Example output:
router 9cae37d3-65ae-46e9-ad95-a0ebf58108d9 (shalb-dev)
    port shalb-dev-ext-public
        mac: "ae:d6:b9:e1:76:78"
        networks: ["168.119.17.51/28"]  # ← All EIPs in this range
        gateway chassis: [...]
```

## Common Misconceptions (Resolved)

> **Note:** As of 2025-12-02, the codebase has been cleaned up to remove sources of these misconceptions.

### ❌ Misconception 1: Policy Routes Required

**FALSE:** Service LoadBalancers do NOT require VPC policy routes.

Policy routes (like `ip4.src==168.119.17.X reroute 168.119.17.49`) are only needed for:
- Outbound traffic from VMs/Pods with dedicated EIPs
- FIP resources that need bidirectional NAT

Service LoadBalancers use the router's external interface directly.

**Status:** ✅ No incorrect references found in codebase.

### ❌ Misconception 2: vpc_nat Annotation Required

**FALSE:** The `ovn.kubernetes.io/vpc_nat` annotation is NOT required for Service LoadBalancer OvnEips.

This annotation is only needed for:
- FIP resources (creates DNAT/SNAT rules)
- Resources that need VPC-level NAT management

Service LoadBalancers work through the OVN load balancer VIP mechanism.

**Status:** ✅ kube-dc controllers verified - no `vpc_nat` usage for Service LBs.

### ❌ Misconception 3: Kyverno Policy is Mandatory

**FALSE:** The Kyverno policies for Service LoadBalancers were NOT required.

**Status:** ✅ **REMOVED** on 2025-12-02:
- Deleted `installer/kube-dc/templates/kube-dc/kyverno/mutate-tenant-svc-lb.yaml`
- Deleted `installer/kube-dc/templates/kube-dc/kyverno/mutate-svc-lb-dep.yaml`
- Removed unnecessary OVN annotations from `examples/capi-cluster/addons.yaml`

These policies set annotations (`ovn.kubernetes.io/vpc`, etc.) that were NOT read by kube-dc controllers.

## Troubleshooting

### Service Not Accessible Externally

1. **Check if EIP is allocated:**
   ```bash
   kubectl get svc -n {namespace} {service-name}
   # STATUS should show EXTERNAL-IP
   ```

2. **Verify OVN load balancer exists:**
   ```bash
   kubectl exec -n kube-system ovn-central-xxx -- ovn-nbctl lb-list | grep {svc-name}
   ```

3. **Check backend endpoints:**
   ```bash
   kubectl get endpoints -n {namespace} {service-name}
   # Should list pod/VM IPs
   ```

4. **Verify router attachment:**
   ```bash
   kubectl exec -n kube-system ovn-central-xxx -- ovn-nbctl lr-lb-list {vpc-name}
   # Should list the load balancer
   ```

5. **Test from within cluster first:**
   ```bash
   kubectl run test --image=curlimages/curl --rm -i -n {namespace} \
     -- curl http://{external-ip}:{port}
   ```

6. **Check ARP resolution (from external host):**
   ```bash
   # On gateway/bastion
   arp -n | grep {external-ip}
   # Should show router MAC
   ```

### Service Works Internally but Not Externally

**Possible causes:**

1. **Firewall rules** on external gateway/firewall
2. **Network routing** - external network may not route to EIP subnet
3. **Testing from wrong location** - if testing from bastion/gateway that has the subnet assigned locally, connections will be routed locally

**Solution:**
- Test from a truly external client (your laptop, different server)
- Check firewall rules on the physical network infrastructure
- Verify routing table on the internet gateway

### Slow Initial Connection

If first connection fails but subsequent ones work:
- **ARP cache warming** - first packet triggers ARP resolution
- **Wait 5-10 seconds** and try again
- Check ARP cache: `arp -n | grep {external-ip}`

## Code References

### Service LB Controller
- **Main controller:** `/home/voa/projects/kube-dc/internal/controller/core/service_controller.go`
- **Load balancer logic:** `/home/voa/projects/kube-dc/internal/service_lb/service_lb.go`
- **EIP management:** `/home/voa/projects/kube-dc/internal/service_lb/eip_res.go`

### EIP Controller
- **OvnEip creation:** `/home/voa/projects/kube-dc/internal/eip/ovn_eip_res.go`
- **Lines 134-150:** OvnEip resource generation (NO vpc_nat annotation needed)

## Related Documentation

- **Service LB Tutorial:** `/home/voa/projects/kube-dc/docs/tutorial-ip-and-lb.md`
- **Networking Architecture:** `/home/voa/projects/kube-dc/docs/architecture-networking.md`
- **FIP Resources:** `/home/voa/projects/kube-dc/internal/fip/res_fip.go`
