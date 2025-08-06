#!/bin/bash

# Script to capture logs from kube-dc-manager and kube-ovn components during E2E tests
# Usage: ./capture-logs.sh [output-dir] [duration-seconds]

set -e

# Configuration
OUTPUT_DIR="${1:-./e2e-logs}"
DURATION="${2:-300}"  # Default 5 minutes
TIMESTAMP=$(date +"%Y%m%d-%H%M%S")
LOG_DIR="${OUTPUT_DIR}/logs-${TIMESTAMP}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${GREEN}🔍 Starting log capture for E2E test debugging${NC}"
echo -e "${BLUE}📁 Output directory: ${LOG_DIR}${NC}"
echo -e "${BLUE}⏱️  Duration: ${DURATION} seconds${NC}"

# Create output directory
mkdir -p "${LOG_DIR}"

# Function to capture logs from a deployment
capture_deployment_logs() {
    local namespace=$1
    local deployment=$2
    local output_file=$3
    
    echo -e "${YELLOW}📋 Capturing logs from ${namespace}/${deployment}...${NC}"
    
    # Get current logs
    kubectl logs -n "${namespace}" deployment/"${deployment}" --all-containers=true --timestamps=true > "${output_file}.current.log" 2>&1 || true
    
    # Follow logs in background
    kubectl logs -n "${namespace}" deployment/"${deployment}" --all-containers=true --timestamps=true --follow > "${output_file}.follow.log" 2>&1 &
    local follow_pid=$!
    
    echo "${follow_pid}" > "${output_file}.pid"
    echo -e "${GREEN}✅ Started following logs for ${namespace}/${deployment} (PID: ${follow_pid})${NC}"
}

# Function to capture logs from daemonset
capture_daemonset_logs() {
    local namespace=$1
    local daemonset=$2
    local output_file=$3
    
    echo -e "${YELLOW}📋 Capturing logs from ${namespace}/${daemonset} (DaemonSet)...${NC}"
    
    # Get current logs from all pods
    kubectl logs -n "${namespace}" daemonset/"${daemonset}" --all-containers=true --timestamps=true > "${output_file}.current.log" 2>&1 || true
    
    # Follow logs in background
    kubectl logs -n "${namespace}" daemonset/"${daemonset}" --all-containers=true --timestamps=true --follow > "${output_file}.follow.log" 2>&1 &
    local follow_pid=$!
    
    echo "${follow_pid}" > "${output_file}.pid"
    echo -e "${GREEN}✅ Started following logs for ${namespace}/${daemonset} (PID: ${follow_pid})${NC}"
}

# Function to capture resource states
capture_resource_states() {
    local output_file="${LOG_DIR}/resource-states.log"
    
    echo -e "${YELLOW}📊 Capturing resource states...${NC}"
    
    {
        echo "=== TIMESTAMP: $(date) ==="
        echo ""
        
        echo "=== ORGANIZATIONS ==="
        kubectl get organizations -A -o wide || true
        echo ""
        
        echo "=== PROJECTS ==="
        kubectl get projects -A -o wide || true
        echo ""
        
        echo "=== EIPs ==="
        kubectl get eip -A -o wide || true
        echo ""
        
        echo "=== FIPs ==="
        kubectl get fip -A -o wide || true
        echo ""
        
        echo "=== VPCs ==="
        kubectl get vpc -o wide || true
        echo ""
        
        echo "=== SUBNETS ==="
        kubectl get subnets -o wide || true
        echo ""
        
        echo "=== OVN EIPs ==="
        kubectl get ovn-eips -o wide || true
        echo ""
        
        echo "=== OVN FIPs ==="
        kubectl get ovn-fips -o wide || true
        echo ""
        
        echo "=== OVN SNAT RULES ==="
        kubectl get ovn-snat-rules -o wide || true
        echo ""
        
        echo "=== NAMESPACES (test-org*) ==="
        kubectl get namespaces | grep -E "(test-org|NAME)" || true
        echo ""
        
    } > "${output_file}"
    
    echo -e "${GREEN}✅ Resource states captured to ${output_file}${NC}"
}

# Function to monitor resource states continuously
monitor_resource_states() {
    local output_file="${LOG_DIR}/resource-states-continuous.log"
    
    echo -e "${YELLOW}📊 Starting continuous resource state monitoring...${NC}"
    
    while true; do
        {
            echo "=== TIMESTAMP: $(date) ==="
            echo ""
            
            echo "=== ORGANIZATIONS ==="
            kubectl get organizations -A -o wide 2>/dev/null || echo "Error getting organizations"
            echo ""
            
            echo "=== PROJECTS ==="
            kubectl get projects -A -o wide 2>/dev/null || echo "Error getting projects"
            echo ""
            
            echo "=== VPCs ==="
            kubectl get vpc -o wide 2>/dev/null || echo "Error getting VPCs"
            echo ""
            
            echo "=== SUBNETS ==="
            kubectl get subnets -o wide 2>/dev/null || echo "Error getting subnets"
            echo ""
            
            echo "=== OVN EIPs ==="
            kubectl get ovn-eips -o wide 2>/dev/null || echo "Error getting OVN EIPs"
            echo ""
            
            echo "=== EIPs ==="
            kubectl get eip -A -o wide 2>/dev/null || echo "Error getting EIPs"
            echo ""
            
            echo "=== STUCK RESOURCES (with deletionTimestamp) ==="
            kubectl get organizations,projects,eip,fip,vpc,subnets,ovn-eips,ovn-fips -A -o jsonpath='{range .items[?(@.metadata.deletionTimestamp)]}{.kind}{"/"}{.metadata.namespace}{"/"}{.metadata.name}{"\t"}{.metadata.deletionTimestamp}{"\n"}{end}' 2>/dev/null || echo "Error getting stuck resources"
            echo ""
            
            echo "=================================="
            echo ""
            
        } >> "${output_file}"
        
        sleep 10
    done &
    
    local monitor_pid=$!
    echo "${monitor_pid}" > "${LOG_DIR}/resource-monitor.pid"
    echo -e "${GREEN}✅ Started resource monitoring (PID: ${monitor_pid})${NC}"
}

# Cleanup function
cleanup() {
    echo -e "${YELLOW}🧹 Cleaning up background processes...${NC}"
    
    # Kill all background log following processes
    for pid_file in "${LOG_DIR}"/*.pid; do
        if [[ -f "${pid_file}" ]]; then
            local pid=$(cat "${pid_file}")
            if kill -0 "${pid}" 2>/dev/null; then
                echo -e "${YELLOW}🔪 Killing process ${pid}${NC}"
                kill "${pid}" 2>/dev/null || true
            fi
            rm -f "${pid_file}"
        fi
    done
    
    echo -e "${GREEN}✅ Cleanup completed${NC}"
    echo -e "${BLUE}📁 Logs saved in: ${LOG_DIR}${NC}"
}

# Set up signal handlers
trap cleanup EXIT INT TERM

# Main execution
echo -e "${GREEN}🚀 Starting log capture...${NC}"

# Capture initial resource states
capture_resource_states

# Start continuous resource monitoring
monitor_resource_states

# Capture kube-dc-manager logs
capture_deployment_logs "kube-dc" "kube-dc-manager" "${LOG_DIR}/kube-dc-manager"

# Capture kube-dc-backend logs (API server)
capture_deployment_logs "kube-dc" "kube-dc-backend" "${LOG_DIR}/kube-dc-backend"

# Capture kube-ovn controller logs
capture_deployment_logs "kube-system" "kube-ovn-controller" "${LOG_DIR}/kube-ovn-controller"

# Capture kube-ovn CNI logs (DaemonSet)
capture_daemonset_logs "kube-system" "kube-ovn-cni" "${LOG_DIR}/kube-ovn-cni"

# Capture ovn-central logs
capture_deployment_logs "kube-system" "ovn-central" "${LOG_DIR}/ovn-central"

# Capture additional kube-ovn components if they exist
echo -e "${YELLOW}🔍 Checking for additional kube-ovn components...${NC}"

# Check for kube-ovn-monitor
if kubectl get deployment -n kube-system kube-ovn-monitor >/dev/null 2>&1; then
    capture_deployment_logs "kube-system" "kube-ovn-monitor" "${LOG_DIR}/kube-ovn-monitor"
fi

# Check for kube-ovn-pinger (DaemonSet)
if kubectl get daemonset -n kube-system kube-ovn-pinger >/dev/null 2>&1; then
    capture_daemonset_logs "kube-system" "kube-ovn-pinger" "${LOG_DIR}/kube-ovn-pinger"
fi

# Check for ovs-ovn DaemonSet
if kubectl get daemonset -n kube-system ovs-ovn >/dev/null 2>&1; then
    capture_daemonset_logs "kube-system" "ovs-ovn" "${LOG_DIR}/ovs-ovn"
fi

echo -e "${GREEN}✅ All log capture processes started${NC}"
echo -e "${BLUE}📝 Log files will be saved to: ${LOG_DIR}${NC}"
echo -e "${YELLOW}⏳ Waiting for ${DURATION} seconds or until interrupted...${NC}"
echo -e "${YELLOW}💡 Press Ctrl+C to stop log capture early${NC}"

# Wait for specified duration or until interrupted
sleep "${DURATION}"

echo -e "${GREEN}🏁 Log capture duration completed${NC}"
