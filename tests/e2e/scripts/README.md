# E2E Test Debugging Scripts

This directory contains advanced debugging scripts for Kube-DC end-to-end tests. These scripts provide comprehensive log capture, resource monitoring, and automated analysis to help identify and resolve test failures.

## ✅ Current Status: ALL TESTS PASSING

The E2E test suite is now fully stabilized with all 7 tests passing consistently:
- **Test Success Rate:** 100% (7/7 tests passing)
- **Runtime:** ~4.3 minutes (255 seconds) for complete suite
- **Parallel Execution:** ✅ Fully supported with proper resource isolation
- **Root Cause Resolution:** Stuck EIP finalizer deadlocks resolved

## Scripts Overview

### `run-tests-with-logs.sh`
Main script that runs E2E tests with comprehensive log capture and analysis.

### `capture-logs.sh`
Standalone log capture script for manual debugging sessions.

### `README.md`
This documentation file.

## Detailed Script Documentation

### 1. `run-tests-with-logs.sh`

Runs E2E tests with comprehensive log capture, resource monitoring, and automated analysis.

**Usage:**
```bash
./run-tests-with-logs.sh [test-type] [output-dir]
```

**Test Types:**
- `--parallel` or `parallel`: Run tests in parallel (default, recommended)
- `--sequential` or `sequential`: Run tests sequentially
- `--serial` or `serial`: Run tests with ginkgo.serial flag
- `"test pattern"`: Run specific test matching the pattern

**Features:**
- ✅ **Comprehensive Log Capture:** All critical components (kube-dc-manager, kube-dc-backend, kube-ovn-controller, etc.)
- ✅ **Resource State Monitoring:** Continuous monitoring of stuck resources and finalizers
- ✅ **Automated Analysis:** Identifies timeouts, race conditions, and finalizer issues
- ✅ **Structured Output:** Organized logs with timestamps and component separation

**Example:**
```bash
# Run all tests in parallel with log capture
./run-tests-with-logs.sh --parallel

# Run specific test with debugging
./run-tests-with-logs.sh "Should create all required OVN objects"
```

### 2. `capture-logs.sh`

Standalone log capture for manual debugging sessions.

**Usage:**
```bash
./capture-logs.sh [output-dir] [duration-seconds]
```

**Parameters:**
- `output-dir`: Directory to save logs (default: `./e2e-logs`)
- `duration-seconds`: How long to capture logs (default: 300 seconds)

**What it captures:**
- **kube-dc-manager** logs from `kube-dc` namespace
- **kube-ovn-controller** logs from `kube-system` namespace  
- **kube-ovn-cni** logs from `kube-system` namespace (DaemonSet)
- **ovn-central** logs from `kube-system` namespace
- **Resource states** (Organizations, Projects, EIPs, FIPs, VPCs, Subnets)
- **Continuous monitoring** of resource states every 10 seconds
- **Stuck resources** with deletion timestamps

**Example:**
```bash
# Capture logs for 10 minutes
./capture-logs.sh ./debug-logs 600
```

### 2. `run-tests-with-logs.sh`

Runs E2E tests with comprehensive log capture and analysis.

**Usage:**
```bash
./run-tests-with-logs.sh [test-type] [output-dir]
```

**Test Types:**
- `parallel`: Run tests in parallel (default)
- `sequential`: Run tests sequentially (`-p=1`)
- `serial`: Run tests with ginkgo serial flag
- `"pattern"`: Run specific test pattern (e.g., `"Should create"`)

**Parameters:**
- `test-type`: Type of test execution (default: `parallel`)
- `output-dir`: Directory to save all logs (default: `./e2e-debug-logs`)

**What it does:**
1. Starts log capture in background
2. Runs E2E tests with specified mode
3. Captures test output
4. Stops log capture
5. Analyzes logs for common issues
6. Generates summary report

**Examples:**
```bash
# Run parallel tests with log capture
./run-tests-with-logs.sh parallel

# Run sequential tests
./run-tests-with-logs.sh sequential

# Run specific test pattern
./run-tests-with-logs.sh "Should create all required OVN objects"

# Save to custom directory
./run-tests-with-logs.sh parallel ./my-debug-logs
```

## Output Structure

Both scripts create timestamped directories with the following structure:

```
e2e-debug-logs/
├── test-run-20250121-125500/
│   ├── test-output.log                    # Test execution output
│   ├── log-analysis.txt                   # Automated log analysis
│   ├── kube-dc-manager.current.log        # Current kube-dc-manager logs
│   ├── kube-dc-manager.follow.log         # Streaming kube-dc-manager logs
│   ├── kube-ovn-controller.current.log    # Current kube-ovn-controller logs
│   ├── kube-ovn-controller.follow.log     # Streaming kube-ovn-controller logs
│   ├── kube-ovn-cni.current.log          # Current kube-ovn-cni logs
│   ├── kube-ovn-cni.follow.log           # Streaming kube-ovn-cni logs
│   ├── ovn-central.current.log           # Current ovn-central logs
│   ├── ovn-central.follow.log            # Streaming ovn-central logs
│   ├── resource-states.log               # Initial resource snapshot
│   └── resource-states-continuous.log    # Continuous resource monitoring
```

## Log Analysis

The `run-tests-with-logs.sh` script automatically analyzes logs for:

- **Errors and panics** in kube-dc-manager
- **Errors and panics** in kube-ovn-controller
- **Finalizer issues** preventing resource deletion
- **Stuck resources** with deletion timestamps
- **Timeout patterns** in test execution
- **Race condition indicators** (conflicts, resource version issues)

## Debugging Race Conditions

To debug race conditions between parallel tests:

1. **Run parallel tests with logs:**
   ```bash
   ./run-tests-with-logs.sh parallel
   ```

2. **Compare with sequential run:**
   ```bash
   ./run-tests-with-logs.sh sequential
   ```

3. **Check the analysis report:**
   ```bash
   cat ./e2e-debug-logs/test-run-*/log-analysis.txt
   ```

4. **Look for patterns in continuous monitoring:**
   ```bash
   grep -A10 "STUCK RESOURCES" ./e2e-debug-logs/test-run-*/resource-states-continuous.log
   ```

## Common Issues to Look For

### 1. Controller Conflicts
- Multiple simultaneous operations on same external network
- VPC/Subnet creation conflicts
- SNAT rule conflicts

### 2. Finalizer Deadlocks
- Resources stuck with finalizers
- Controller unable to process deletion
- Circular dependencies

### 3. Resource Cleanup Issues
- Previous test resources not fully cleaned up
- Namespace deletion hanging
- OVN resources not properly removed

### 4. Timing Issues
- Tests starting before cleanup completes
- Controller processing delays
- Network resource allocation delays

## Tips

- **Run during off-peak hours** to reduce cluster load
- **Check cluster resources** before running tests
- **Monitor disk space** as logs can be large
- **Use specific test patterns** to isolate issues
- **Compare parallel vs sequential** results to identify race conditions

## Troubleshooting

If scripts fail to capture logs:

1. **Check permissions:**
   ```bash
   chmod +x tests/e2e/scripts/*.sh
   ```

2. **Verify kubectl access:**
   ```bash
   kubectl get pods -n kube-dc
   kubectl get pods -n kube-system | grep ovn
   ```

3. **Check component names:**
   ```bash
   kubectl get deployments -n kube-dc
   kubectl get deployments -n kube-system | grep ovn
   ```

4. **Manual log capture:**
   ```bash
   kubectl logs -n kube-dc deployment/kube-dc-manager --follow
   kubectl logs -n kube-system deployment/kube-ovn-controller --follow
   ```
