# Kube-DC E2E Test Manifests

This directory contains a complete set of Kubernetes manifests for end-to-end testing of Kube-DC functionality. These manifests can be applied manually for testing or used as examples for understanding Kube-DC resource configurations.

## Quick Start

### Apply All Manifests
```bash
# Apply all manifests
kubectl apply -f tests/e2e/manifests/
```

### Delete All Manifests
```bash
# Use helper script for proper cleanup
./delete.sh

# Or delete by directory
kubectl delete -f tests/e2e/manifests/
```

## Manifest Overview

### Infrastructure (01-03)
1. **01-namespace.yaml** - Creates the organization namespace `test-org-e2e-manual`
2. **02-organization.yaml** - Creates the Organization resource with Keycloak integration
3. **03-project.yaml** - Creates the Project resource with CIDR `10.150.0.0/16`

### Networking (04-05)
4. **04-eip.yaml** - Creates External IP resources for LoadBalancer services
5. **05-fip.yaml** - Creates Floating IP resources for VMs and pods

### Workloads (06-08)
6. **06-nginx-deployment.yaml** - Creates nginx deployment with configmap
7. **07-service-lb.yaml** - Creates LoadBalancer services with EIP binding
8. **08-vm-examples.yaml** - Creates VirtualMachines with DataVolumes and services

## Verification

### Check All E2E Test Resources
```bash
# Check organization and project
kubectl get organizations,projects -n test-org-e2e-manual

# Check all workload resources in one command
kubectl get eip,fip,deploy,pod,svc,vm,vmi,dv -n test-org-e2e-manual-test-project-e2e-manual
```

## Expected Results

After applying all manifests:
- ✅ Organization and Project ready with OVN networking
- ✅ EIPs and FIPs with external IP addresses assigned
- ✅ Nginx pods and LoadBalancer service with external IP
- ✅ VMs created (may take 5-10 minutes to download images and start)

## Troubleshooting

### Common Issues
1. **Organization not ready**: Check Keycloak is running and accessible
2. **Project not ready**: Check OVN controllers are running
3. **EIP/FIP not ready**: Check external network configuration
4. **Pods not starting**: Check project namespace has proper RBAC
5. **VMs not starting**: Check KubeVirt is installed and CDI is working
6. **LoadBalancer service external IP pending**: 
   - If using `bind-on-default-gw-eip: "true"` and multiple services compete for the same EIP
   - Solution: Use dedicated EIPs with `bind-on-eip: "specific-eip-name"`
   - May need to delete and recreate the service after changing annotations

### Cleanup Stuck Resources
```bash
# Force delete stuck namespaces
kubectl patch ns test-org-e2e-manual-test-project-e2e-manual -p '{"metadata":{"finalizers":null}}'
kubectl patch ns test-org-e2e-manual -p '{"metadata":{"finalizers":null}}'

# Force delete stuck projects
kubectl patch project test-project-e2e-manual -n test-org-e2e-manual -p '{"metadata":{"finalizers":null}}'

# Force delete stuck organizations
kubectl patch org test-org-e2e-manual -n test-org-e2e-manual -p '{"metadata":{"finalizers":null}}'
```

## Labels and Selectors

All resources are labeled with:
- `kube-dc.com/test: e2e-manual` - Identifies E2E test resources
- `environment: e2e-test` - Test environment marker

Use these labels to query or clean up test resources:
```bash
# Find all E2E test resources
kubectl get all -A -l kube-dc.com/test=e2e-manual

# Clean up by label (use with caution)
kubectl delete all -A -l kube-dc.com/test=e2e-manual
```
