# Kube-DC End-to-End Test Plan

This document outlines the comprehensive testing strategy for Kube-DC, including both automated and manual end-to-end tests with advanced debugging capabilities.

## 1. Automated End-to-End Tests

The project includes a comprehensive suite of automated end-to-end tests written in Go using the Ginkgo framework. These tests validate core controller functionality, resource lifecycle management, and workload deployment capabilities. All tests are designed to be isolated and can run independently or in parallel.

**Current Test Suite Status: ✅ ALL TESTS PASSING**
- **Organization Tests:** 1 test validating Keycloak integration and organization lifecycle
- **Project Tests:** 2 tests validating project creation, OVN resource management, and deletion cascade
- **Workload Tests:** 4 tests validating EIP, FIP, pod deployment, and VM infrastructure
- **Total:** 7 comprehensive E2E tests covering all major Kube-DC functionality
- **Runtime:** ~4.3 minutes (255 seconds) for complete suite
- **Parallel Execution:** ✅ Fully supported with proper resource isolation

### Running the Tests

#### Using Makefile (Recommended)
```bash
# Run all E2E tests
make test-e2e

# Run specific test by focus pattern
make test-e2e-focus FOCUS="Should create all required OVN objects"
make test-e2e-focus FOCUS="Should properly delete Project"

# Run unit tests
make test
# or
make test-unit

# Run both unit and E2E tests
make test-all

# Generate coverage report
make test-coverage
make test-coverage-html  # Generates coverage.html

# For Kind clusters
make test-e2e-kind
```

#### Using Go directly
```bash
# Run all E2E tests
go test -v ./tests/e2e -timeout=15m

# Run specific test by focus
go test -v ./tests/e2e -ginkgo.focus="Should create all required OVN objects" -timeout=10m

# Run unit tests
go test $(go list ./... | grep -v /e2e) -coverprofile cover.out
```

#### Using Advanced Log Capture Scripts (Recommended for Debugging)

For comprehensive debugging and log analysis, use the enhanced test scripts:

```bash
# Navigate to scripts directory
cd tests/e2e/scripts

# Run tests with comprehensive log capture
./run-tests-with-logs.sh --parallel     # Parallel execution (default)
./run-tests-with-logs.sh --sequential   # Sequential execution
./run-tests-with-logs.sh --serial       # Serial execution with ginkgo.serial
./run-tests-with-logs.sh "test pattern" # Run specific test pattern

# Manual log capture only (for live debugging)
./capture-logs.sh ./debug-logs 1800     # Capture logs for 30 minutes
```

**Log Capture Features:**
- ✅ **Comprehensive Component Logs:** kube-dc-manager, kube-dc-backend, kube-ovn-controller, kube-ovn-cni, ovn-central
- ✅ **Resource State Monitoring:** Continuous monitoring of stuck resources and finalizers
- ✅ **Automated Log Analysis:** Identifies common issues (timeouts, race conditions, finalizer problems)
- ✅ **Structured Output:** Organized logs with timestamps and component separation
- ✅ **Test Integration:** Seamless integration with E2E test execution

**Output Structure:**
```
e2e-debug-logs/
└── test-run-YYYYMMDD-HHMMSS/
    ├── test-output.log              # Complete test execution log
    ├── log-analysis.txt            # Automated issue analysis
    └── logs-YYYYMMDD-HHMMSS/
        ├── kube-dc-manager.follow.log
        ├── kube-dc-backend.follow.log
        ├── kube-ovn-controller.follow.log
        ├── resource-states.log
        └── resource-states-continuous.log
```

### Organization Controller Test

**File:** `tests/e2e/organization_test.go`  
**Test Name:** "Should create an Organization and dependent resources successfully"  
**Duration:** ~30-40 seconds  
**Purpose:** Validates Organization resource lifecycle and Keycloak integration

**What it tests:**
1. **Namespace Management:** Creates organization namespace (`test-org-e2e`)
2. **Organization Creation:** Creates Organization resource in its own namespace
3. **Controller Reconciliation:** Waits for `status.ready` to become `true` (2-minute timeout)
4. **Keycloak Integration:** 
   - Verifies `realm-access` secret creation with `url`, `user`, `password` keys
   - Authenticates with Keycloak using provided credentials
   - Validates realm configuration (enabled, correct settings)
   - Confirms `kube-dc` client exists and is enabled
   - Verifies admin user configuration and group membership
5. **Cleanup:** Automatically deletes Organization and namespace

**Key Validations:**
- Keycloak realm is properly configured
- Admin user has correct email and is in `org-admin` group
- All authentication flows work correctly

### Project Controller Tests

**File:** `tests/e2e/project_test.go`  
**Tests:** 2 comprehensive tests for creation and deletion scenarios  
**Duration:** ~30-45 seconds each  
**Purpose:** Validates complete Project resource lifecycle and OVN integration

#### Test 1: "Should create all required OVN objects and resources for a Project"

**Organization:** `test-org-creation-e2e`  
**Project:** `test-project-e2e`  
**CIDR:** `10.100.0.0/16`

**What it tests:**
1. **Prerequisites:** Creates organization namespace and Organization resource
2. **Project Creation:** Creates Project with `EgressNetworkType: cloud`
3. **Namespace Verification:** Confirms project namespace (`test-org-creation-e2e-test-project-e2e`) creation
4. **Project Status:** Waits for Project `status.ready` to become `true`
5. **EIp Resource:** Verifies `default-gw` EIp creation with correct `ChildRef`
6. **OvnEip Resource:** Confirms OvnEip creation (`{project-namespace}-ext-cloud`)
7. **SNAT Rule:** Validates OvnSnatRule creation and OvnEip reference
8. **VPC Resource:** Verifies VPC creation with correct namespace association
9. **Subnet Resource:** Confirms Subnet creation with correct CIDR (`10.100.0.0/16`)
10. **NetworkAttachmentDefinition:** Validates NAD creation with kube-ovn CNI config
11. **RBAC Resources:** Verifies Role (`admin`) and RoleBinding (`org-admin`) creation
12. **SSH Keys:** Confirms `authorized-keys-default` and `ssh-keypair-default` secrets

#### Test 2: "Should properly delete Project and clean up all resources in correct order"

**Organization:** `test-org-deletion-e2e`  
**Purpose:** Validates complete deletion cascade and finalizer handling

**What it tests:**
1. **Setup:** Creates complete Project infrastructure using helper function
2. **Pre-deletion Verification:** Confirms all resources exist before deletion
3. **Deletion Sequence Validation:**
   - Deletes Project resource
   - Monitors SNAT rule deletion (should auto-delete OvnEip)
   - Verifies OvnEip cleanup after SNAT rule removal
   - Confirms EIp deletion after OvnEip cleanup
   - Validates Subnet and VPC deletion
   - Verifies project namespace cleanup
4. **Finalizer Handling:** Ensures proper finalizer removal and no stuck resources
5. **Cleanup Verification:** Confirms all resources are properly removed

**Key Features:**
- **Deletion Order Verification:** Tests the critical dependency chain
- **Timeout Handling:** Uses appropriate timeouts for each deletion phase
- **Race Condition Prevention:** Uses separate organization names for isolation
- **Robust Cleanup:** Includes comprehensive cleanup functions

### Test Infrastructure

**Test Isolation:**
- Each test uses unique organization names to prevent conflicts
- Comprehensive cleanup functions ensure no resource leakage
- Proper timeout handling prevents indefinite waits

**Helper Functions:**
- `cleanupTestResources()`: Robust cleanup with finalizer removal
- `setupProjectForDeletion()`: Creates complete Project infrastructure
- `stringPointer()`: Utility for string pointer creation

**Recent Fixes Applied:**
- ✅ Organization Role finalizer deadlock resolved (OwnerReference implementation)
- ✅ EIp controller OvnEip deletion logic fixed (label-based deletion)
- ✅ WaitDelete function NotFound error handling improved
- ✅ Race conditions between tests eliminated

**Test Results:**
- ✅ **All tests pass individually and together**
- ✅ **Total runtime:** ~77 seconds for all 3 tests
- ✅ **No finalizer deadlocks or stuck resources**
- ✅ **Complete deletion cascade working correctly**
- ✅ **Proper resource isolation between test runs**

### Workload Controller Tests

**File:** `tests/e2e/workload_test.go`  
**Tests:** 4 comprehensive workload deployment tests  
**Duration:** ~3-4 minutes total  
**Purpose:** Validates workload deployment capabilities within projects including EIP, FIP, pods, and VM infrastructure

**Test Infrastructure:**
- **Organization:** `test-org-workload-e2e`
- **Project:** `test-project-workload`  
- **CIDR:** `10.200.0.0/16`
- **Project Namespace:** `test-org-workload-e2e-test-project-workload`

#### Test 1: "Should create and manage EIP resources successfully"

**What it tests:**
1. **EIP Creation:** Creates custom EIP resource (`test-workload-eip`) in project namespace
2. **EIP Configuration:** Validates EIP spec with correct `ChildRef` pointing to OvnEip
3. **EIP Status:** Waits for EIP `status.ready` to become `true`
4. **External IP Assignment:** Verifies EIP gets external IP address in `status.ipAddress`
5. **OvnEip Integration:** Confirms underlying OvnEip resource is created and linked
6. **Resource Cleanup:** Ensures EIP and associated resources are properly deleted

**Key Validations:**
- EIP controller creates and manages OvnEip resources correctly
- External IP allocation works within project networking
- Proper cleanup prevents resource leaks

#### Test 2: "Should create and manage FIP resources successfully"

**What it tests:**
1. **FIP Creation:** Creates custom FIP resource (`test-workload-fip`) in project namespace
2. **FIP Configuration:** Validates FIP spec with IP address assignment
3. **FIP Status:** Waits for FIP `status.ready` to become `true`
4. **Floating IP Assignment:** Verifies FIP gets external IP in `status.externalIP`
5. **Network Integration:** Confirms FIP integrates with project networking
6. **Resource Cleanup:** Ensures FIP resources are properly deleted

**Key Validations:**
- FIP controller manages floating IP allocation correctly
- Floating IPs are properly assigned and accessible
- Clean deletion cascade works for FIP resources

#### Test 3: "Should deploy nginx pod with LoadBalancer service successfully"

**What it tests:**
1. **Pod Deployment:** Creates nginx deployment with 1 replica in project namespace
2. **Pod Readiness:** Waits for pod to reach `Running` state with proper IP assignment
3. **LoadBalancer Service:** Creates service with `type: LoadBalancer` and EIP binding annotation
4. **Service Annotation:** Uses `service.nlb.kube-dc.com/bind-on-default-gw-eip: "true"` for EIP binding
5. **External IP Assignment:** Verifies service gets external IP from project's default EIP
6. **Service Connectivity:** Validates service exposes nginx on port 80 with external access
7. **Resource Cleanup:** Ensures deployment and service are properly deleted

**Key Validations:**
- Pod deployment works correctly in project namespaces
- LoadBalancer services integrate with Kube-DC EIP system
- Service annotations properly bind to project EIPs
- External connectivity is established for workloads

#### Test 4: "Should verify VM and workload deployment capabilities"

**What it tests:**
1. **NetworkAttachmentDefinition:** Verifies NAD exists in project namespace for VM networking
2. **NAD Configuration:** Validates NAD has correct kube-ovn CNI configuration
3. **NAD Project Reference:** Confirms NAD references correct project namespace
4. **Test Pod Deployment:** Creates simple busybox pod to validate workload deployment
5. **Pod Networking:** Verifies pod gets IP address and runs successfully
6. **Project Isolation:** Confirms workloads run in isolated project environment
7. **Resource Cleanup:** Ensures test pod is properly deleted

**Key Validations:**
- VM networking infrastructure (NAD) is properly configured
- Project namespaces support both pod and VM workloads
- Network isolation works correctly for project workloads
- Basic workload deployment patterns function as expected

**Test Results (Latest Run):**
- ✅ **All 4 workload tests pass successfully** (Total: ~2.1 minutes)
- ✅ **EIP test:** 31.998 seconds - Creates EIP with external IP `100.65.0.216`
- ✅ **FIP test:** 32.073 seconds - Creates FIP with external IP `100.65.0.218`  
- ✅ **Pod + Service test:** 38.092 seconds - Deploys nginx with LoadBalancer service on IP `100.65.0.220`
- ✅ **VM capability test:** ~14 seconds - Verifies NetworkAttachmentDefinition and workload deployment with pod IP `10.200.0.3`
- ✅ **Proper resource cleanup and isolation**

**Test Manifests:**
Workload test manifests are located in `tests/e2e/manifests/` and can be used for both automated testing and manual verification:
- `01-namespace.yaml` - Organization namespace
- `02-organization.yaml` - Organization resource with Keycloak integration
- `03-project.yaml` - Project resource with networking (CIDR: 10.150.0.0/16)
- `04-eip.yaml` - External IP resources for LoadBalancer services
- `05-fip.yaml` - Floating IP resources for VMs and pods
- `06-nginx-deployment.yaml` - Nginx deployment with configmap
- `07-service-lb.yaml` - LoadBalancer services with EIP binding annotations
- `08-vm-examples.yaml` - VirtualMachine examples with DataVolumes and services
- `README.md` - Comprehensive documentation and usage guide
- `apply.sh` / `delete.sh` - Helper scripts for easy deployment

### Manual Testing with Manifests

For manual end-to-end testing, you can apply all manifests at once:

```bash
# Apply all E2E test resources
cd tests/e2e/manifests
./apply.sh

# Or apply manually
kubectl apply -f tests/e2e/manifests/

# Verify deployment
kubectl get organizations,projects -n test-org-e2e-manual
kubectl get eip,fip,deploy,pod,svc,vm,vmi,dv -n test-org-e2e-manual-test-project-e2e-manual

# Clean up when done
./delete.sh
# Or delete manually
kubectl delete -f tests/e2e/manifests/
```

This provides a complete E2E test environment with:
- ✅ Organization with Keycloak integration
- ✅ Project with OVN networking (VPC, Subnet, NAD, RBAC)
- ✅ External and Floating IP management
- ✅ Pod workloads with LoadBalancer services
- ✅ VM workloads with SSH and web access
- ✅ Proper resource cleanup and deletion cascade

---

## 2. Manual End-to-End Testing

### Using Test Manifests

For manual validation and debugging, you can use the comprehensive test manifests located in `tests/e2e/manifests/`. These manifests provide a complete E2E test environment that mirrors the automated tests.

#### Quick Start

```bash
# Navigate to manifests directory
cd tests/e2e/manifests

# Deploy complete E2E test environment
./apply.sh

# Verify deployment
kubectl get organizations,projects -n test-org-e2e-manual
kubectl get eip,fip,deploy,pod,svc,vm,vmi,dv -n test-org-e2e-manual-test-project-e2e-manual

# Clean up when done
./delete.sh
```

#### Manual Step-by-Step Deployment

```bash
# 1. Create organization namespace and organization
kubectl apply -f 01-namespace.yaml
kubectl apply -f 02-organization.yaml

# 2. Wait for organization to be ready
kubectl wait --for=condition=Ready organization/test-org-e2e-manual -n test-org-e2e-manual --timeout=120s

# 3. Create project with networking
kubectl apply -f 03-project.yaml

# 4. Wait for project to be ready
kubectl wait --for=condition=Ready project/test-project-e2e-manual -n test-org-e2e-manual --timeout=300s

# 5. Deploy networking resources
kubectl apply -f 04-eip.yaml
kubectl apply -f 05-fip.yaml

# 6. Deploy workloads
kubectl apply -f 06-nginx-deployment.yaml
kubectl apply -f 07-service-lb.yaml
kubectl apply -f 08-vm-examples.yaml
```

### Prerequisites

- A running Kube-DC cluster with all controllers deployed
- `kubectl` installed and configured with cluster-admin privileges
- The Kube-DC repository cloned locally
- Sufficient cluster resources for VM workloads (if testing VMs)

### Verification Commands

```bash
# Check organization status
kubectl get organizations -n test-org-e2e-manual -o wide
kubectl describe organization test-org-e2e-manual -n test-org-e2e-manual

# Check project status and OVN resources
kubectl get projects -n test-org-e2e-manual -o wide
kubectl get vpc,subnets,ovn-eips,ovn-snat-rules --all-namespaces | grep test-org-e2e-manual

# Check networking resources
kubectl get eip,fip -n test-org-e2e-manual-test-project-e2e-manual -o wide

# Check workload deployment
kubectl get deploy,pod,svc -n test-org-e2e-manual-test-project-e2e-manual -o wide

# Check VM resources (if deployed)
kubectl get vm,vmi,dv -n test-org-e2e-manual-test-project-e2e-manual -o wide
```

### Expected Results

**Organization Resources:**
- Organization `test-org-e2e-manual` should have `status.ready: true`
- Keycloak realm should be created and accessible
- Organization namespace should exist and be active

**Project Resources:**
- Project `test-project-e2e-manual` should have `status.ready: true`
- Project namespace `test-org-e2e-manual-test-project-e2e-manual` should exist
- VPC, Subnet, and NetworkAttachmentDefinition should be created
- Default gateway EIP should be assigned an external IP

**Networking Resources:**
- EIP resources should have external IP addresses assigned
- FIP resources should have both internal and external IPs
- LoadBalancer services should get external IPs from EIP pool

**Workload Resources:**
- Nginx deployment should have all replicas running
- Pods should have IPs from the project CIDR (10.150.0.0/16)
- Services should have external IPs and be accessible

## 3. Troubleshooting and Debugging

### Common Issues and Solutions

#### Stuck Resources with Finalizers

```bash
# Check for stuck resources
kubectl get organizations,projects,eips,fips --all-namespaces -o custom-columns="KIND:.kind,NAMESPACE:.metadata.namespace,NAME:.metadata.name,FINALIZERS:.metadata.finalizers,DELETION:.metadata.deletionTimestamp" | grep -v "null.*null"

# Check for stuck OVN resources
kubectl get ovn-eips,subnets,vpc -o custom-columns="KIND:.kind,NAME:.metadata.name,FINALIZERS:.metadata.finalizers,DELETION:.metadata.deletionTimestamp" | grep -v "null.*null"

# Force remove finalizers (use with caution)
kubectl patch <resource-type> <resource-name> -n <namespace> -p '{"metadata":{"finalizers":null}}' --type=merge
```

#### Controller Issues

```bash
# Check controller logs
kubectl logs -n kube-dc deployment/kube-dc-manager --follow
kubectl logs -n kube-system deployment/kube-ovn-controller --follow

# Check controller status
kubectl get deployments -n kube-dc
kubectl get deployments -n kube-system | grep ovn

# Restart controllers if needed
kubectl rollout restart deployment/kube-dc-manager -n kube-dc
kubectl rollout restart deployment/kube-ovn-controller -n kube-system
```

#### Resource State Monitoring

```bash
# Monitor resource creation/deletion
watch "kubectl get organizations,projects,eips,fips --all-namespaces"
watch "kubectl get ovn-eips,subnets,vpc --all-namespaces"

# Check for error events
kubectl get events --all-namespaces --sort-by='.lastTimestamp' | grep -i error
```

### Using Log Capture Scripts for Debugging

For comprehensive debugging of test failures, use the log capture scripts:

```bash
# Capture logs during manual testing
cd tests/e2e/scripts
./capture-logs.sh ./manual-test-logs 1800  # 30 minutes

# In another terminal, run your manual tests
cd tests/e2e/manifests
./apply.sh

# Check captured logs
ls -la manual-test-logs/
tail -f manual-test-logs/kube-dc-manager.follow.log
```

## 4. Success Criteria

### Automated Tests
The automated E2E test suite is considered successful when:
- ✅ **All 7 tests pass** without failures or timeouts
- ✅ **Resource lifecycle validation** - All resources are created, become ready, and are properly cleaned up
- ✅ **Controller integration** - All controllers (kube-dc-manager, kube-ovn-controller) function correctly
- ✅ **Networking validation** - EIP, FIP, and LoadBalancer services work as expected
- ✅ **Parallel execution** - Tests can run concurrently without race conditions
- ✅ **Performance targets** - Complete test suite finishes within 5 minutes

### Manual Testing
Manual testing is considered successful when:
- ✅ **Resource deployment** - All manifests apply without errors
- ✅ **Status verification** - Organizations and Projects reach `Ready` state
- ✅ **Networking functionality** - External IPs are assigned and services are accessible
- ✅ **Workload deployment** - Pods and VMs deploy successfully in project namespaces
- ✅ **Clean deletion** - All resources are removed without stuck finalizers

### Key Performance Indicators
- **Test Success Rate:** 100% (7/7 tests passing)
- **Average Test Runtime:** ~4.3 minutes for full suite
- **Resource Creation Time:** Organizations ~5s, Projects ~30s
- **IP Assignment Time:** EIPs and FIPs ready within 2-3 seconds
- **Cleanup Efficiency:** No stuck resources or finalizers after test completion

### Troubleshooting Resources
- **Log Capture Scripts:** Available in `tests/e2e/scripts/` for comprehensive debugging
- **Automated Analysis:** Log analysis identifies common issues (timeouts, finalizers, race conditions)
- **Manual Commands:** Comprehensive troubleshooting commands provided above
- **Documentation:** Complete test plan with expected results and verification steps
