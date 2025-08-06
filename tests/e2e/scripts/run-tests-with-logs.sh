#!/bin/bash

# Script to run E2E tests with comprehensive log capture
# Usage: ./run-tests-with-logs.sh [test-type] [output-dir]
#   test-type: parallel, sequential, or specific test pattern
#   output-dir: directory to save logs (default: ./e2e-debug-logs)

set -e

# Configuration
# Strip -- prefix from argument if present
RAW_ARG="${1:-parallel}"
TEST_TYPE="${RAW_ARG#--}"
OUTPUT_DIR="${2:-./e2e-debug-logs}"
TIMESTAMP=$(date +"%Y%m%d-%H%M%S")
TEST_LOG_DIR="${OUTPUT_DIR}/test-run-${TIMESTAMP}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Get script directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"

echo -e "${GREEN}🧪 Running E2E tests with comprehensive log capture${NC}"
echo -e "${BLUE}📁 Test logs will be saved to: ${TEST_LOG_DIR}${NC}"
echo -e "${BLUE}🔧 Test type: ${TEST_TYPE}${NC}"

# Create output directory
mkdir -p "${TEST_LOG_DIR}"

# Function to run tests based on type
run_tests() {
    local test_cmd
    local log_file="${TEST_LOG_DIR}/test-output.log"
    
    case "${TEST_TYPE}" in
        "parallel")
            echo -e "${YELLOW}🏃‍♂️ Running tests in parallel mode${NC}"
            test_cmd="go test -v ./tests/e2e -timeout=20m -ginkgo.v -ginkgo.no-color"
            ;;
        "sequential")
            echo -e "${YELLOW}🚶‍♂️ Running tests in sequential mode${NC}"
            test_cmd="go test -v ./tests/e2e -timeout=20m -ginkgo.v -ginkgo.no-color -p=1"
            ;;
        "serial")
            echo -e "${YELLOW}🔄 Running tests with ginkgo serial flag${NC}"
            test_cmd="go test -v ./tests/e2e -timeout=20m -ginkgo.v -ginkgo.no-color --ginkgo.serial"
            ;;
        *)
            echo -e "${YELLOW}🎯 Running specific test pattern: ${TEST_TYPE}${NC}"
            test_cmd="go test -v ./tests/e2e -timeout=20m -ginkgo.v -ginkgo.no-color -ginkgo.focus=\"${TEST_TYPE}\""
            ;;
    esac
    
    echo -e "${BLUE}📝 Test command: ${test_cmd}${NC}"
    echo -e "${BLUE}📄 Test output will be saved to: ${log_file}${NC}"
    
    # Ensure the log directory exists before writing
    mkdir -p "$(dirname "${log_file}")"
    
    # Run tests and capture output
    cd "${PROJECT_ROOT}"
    echo "=== TEST EXECUTION STARTED AT $(date) ===" > "${log_file}"
    echo "Command: ${test_cmd}" >> "${log_file}"
    echo "=========================================" >> "${log_file}"
    echo "" >> "${log_file}"
    
    # Run the test command and capture both stdout and stderr
    if eval "${test_cmd}" 2>&1 | tee -a "${log_file}"; then
        echo -e "${GREEN}✅ Tests completed successfully${NC}"
        return 0
    else
        echo -e "${RED}❌ Tests failed${NC}"
        return 1
    fi
}

# Function to analyze logs for common issues
analyze_logs() {
    local log_capture_dir="${1}"
    local analysis_file="${TEST_LOG_DIR}/log-analysis.txt"
    
    echo -e "${YELLOW}🔍 Analyzing logs for common issues...${NC}"
    
    {
        echo "=== LOG ANALYSIS REPORT ==="
        echo "Generated at: $(date)"
        echo "Log directory: ${log_capture_dir}"
        echo ""
        
        echo "=== ERRORS IN KUBE-DC-MANAGER ==="
        if [[ -f "${log_capture_dir}/kube-dc-manager.follow.log" ]]; then
            grep -i "error\|panic\|fatal" "${log_capture_dir}/kube-dc-manager.follow.log" | tail -20 || echo "No errors found"
        else
            echo "kube-dc-manager logs not found"
        fi
        echo ""
        
        echo "=== ERRORS IN KUBE-DC-BACKEND ==="
        if [[ -f "${log_capture_dir}/kube-dc-backend.follow.log" ]]; then
            grep -i "error\|panic\|fatal" "${log_capture_dir}/kube-dc-backend.follow.log" | tail -20 || echo "No errors found"
        else
            echo "kube-dc-backend logs not found"
        fi
        echo ""
        
        echo "=== ERRORS IN KUBE-OVN-CONTROLLER ==="
        if [[ -f "${log_capture_dir}/kube-ovn-controller.follow.log" ]]; then
            grep -i "error\|panic\|fatal" "${log_capture_dir}/kube-ovn-controller.follow.log" | tail -20 || echo "No errors found"
        else
            echo "kube-ovn-controller logs not found"
        fi
        echo ""
        
        echo "=== FINALIZER ISSUES ==="
        if [[ -f "${log_capture_dir}/resource-states-continuous.log" ]]; then
            grep -A5 -B5 "finalizer" "${log_capture_dir}/resource-states-continuous.log" | tail -30 || echo "No finalizer issues found"
        fi
        echo ""
        
        echo "=== STUCK RESOURCES ==="
        if [[ -f "${log_capture_dir}/resource-states-continuous.log" ]]; then
            grep -A10 "STUCK RESOURCES" "${log_capture_dir}/resource-states-continuous.log" | tail -50 || echo "No stuck resources found"
        fi
        echo ""
        
        echo "=== TIMEOUT PATTERNS ==="
        if [[ -f "${TEST_LOG_DIR}/test-output.log" ]]; then
            grep -i "timeout\|timed out" "${TEST_LOG_DIR}/test-output.log" || echo "No timeout issues found"
        fi
        echo ""
        
        echo "=== RACE CONDITION INDICATORS ==="
        if [[ -f "${TEST_LOG_DIR}/test-output.log" ]]; then
            grep -i "already exists\|conflict\|resource version" "${TEST_LOG_DIR}/test-output.log" || echo "No race condition indicators found"
        fi
        echo ""
        
    } > "${analysis_file}"
    
    echo -e "${GREEN}✅ Log analysis saved to: ${analysis_file}${NC}"
}

# Cleanup function
cleanup() {
    echo -e "${YELLOW}🧹 Cleaning up...${NC}"
    
    # Stop log capture if it's running
    if [[ -n "${LOG_CAPTURE_PID}" ]] && kill -0 "${LOG_CAPTURE_PID}" 2>/dev/null; then
        echo -e "${YELLOW}🔪 Stopping log capture (PID: ${LOG_CAPTURE_PID})${NC}"
        kill "${LOG_CAPTURE_PID}" 2>/dev/null || true
        wait "${LOG_CAPTURE_PID}" 2>/dev/null || true
    fi
    
    echo -e "${GREEN}✅ Cleanup completed${NC}"
    echo -e "${BLUE}📁 All logs and analysis saved in: ${TEST_LOG_DIR}${NC}"
}

# Set up signal handlers
trap cleanup EXIT INT TERM

# Main execution
echo -e "${GREEN}🚀 Starting E2E test execution with log capture...${NC}"

# Create the full directory structure first
mkdir -p "${TEST_LOG_DIR}"

# Start log capture in background
echo -e "${YELLOW}📋 Starting log capture...${NC}"
"${SCRIPT_DIR}/capture-logs.sh" "${TEST_LOG_DIR}" 1800 &  # 30 minutes max
LOG_CAPTURE_PID=$!

# Give log capture a moment to start and create directories
sleep 5

# Run the tests
TEST_RESULT=0
run_tests || TEST_RESULT=$?

# Stop log capture
if [[ -n "${LOG_CAPTURE_PID}" ]] && kill -0 "${LOG_CAPTURE_PID}" 2>/dev/null; then
    echo -e "${YELLOW}🛑 Stopping log capture...${NC}"
    kill "${LOG_CAPTURE_PID}" 2>/dev/null || true
    wait "${LOG_CAPTURE_PID}" 2>/dev/null || true
fi

# Wait a moment for logs to flush
sleep 2

# Analyze logs
analyze_logs "${TEST_LOG_DIR}"

# Final summary
echo -e "${BLUE}📊 Test Execution Summary${NC}"
echo -e "${BLUE}========================${NC}"
echo -e "${BLUE}📁 Logs directory: ${TEST_LOG_DIR}${NC}"
echo -e "${BLUE}📄 Test output: ${TEST_LOG_DIR}/test-output.log${NC}"
echo -e "${BLUE}📋 Log analysis: ${TEST_LOG_DIR}/log-analysis.txt${NC}"
echo -e "${BLUE}🔍 Component logs: ${TEST_LOG_DIR}/*.log${NC}"

if [[ ${TEST_RESULT} -eq 0 ]]; then
    echo -e "${GREEN}✅ Tests completed successfully${NC}"
else
    echo -e "${RED}❌ Tests failed - check logs for details${NC}"
fi

exit ${TEST_RESULT}
