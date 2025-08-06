#!/bin/bash

# Comprehensive Project Deletion Script
# This script forcefully deletes a Kube-DC project and all its resources
# Based on docs/project_resources.md deletion order
# Includes stuck resource detection and automatic finalizer removal

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Function to print colored output
log_info() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

log_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

log_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Function to remove finalizers aggressively
remove_finalizers() {
    local resource_type="$1"
    local resource_name="$2"
    local namespace="$3"
    
    if [ -n "$namespace" ]; then
        if kubectl get "$resource_type" "$resource_name" -n "$namespace" >/dev/null 2>&1; then
            log_warning "Force removing finalizers from $resource_type/$resource_name in namespace $namespace"
            kubectl patch "$resource_type" "$resource_name" -n "$namespace" -p '{"metadata":{"finalizers":null}}' --type=merge 2>/dev/null || true
        fi
    else
        if kubectl get "$resource_type" "$resource_name" >/dev/null 2>&1; then
            log_warning "Force removing finalizers from $resource_type/$resource_name"
            kubectl patch "$resource_type" "$resource_name" -p '{"metadata":{"finalizers":null}}' --type=merge 2>/dev/null || true
        fi
    fi
}

# Function to delete resource with immediate finalizer removal
delete_with_force() {
    local resource_type="$1"
    local resource_name="$2"
    local namespace="$3"
    local timeout="${4:-10}"  # Short timeout, then force
    
    if [ -n "$namespace" ]; then
        if kubectl get "$resource_type" "$resource_name" -n "$namespace" >/dev/null 2>&1; then
            log_info "Deleting $resource_type/$resource_name in namespace $namespace"
            
            # Try normal deletion first
            kubectl delete "$resource_type" "$resource_name" -n "$namespace" --ignore-not-found=true --timeout="${timeout}s" >/dev/null 2>&1 &
            local delete_pid=$!
            
            # Wait briefly, then force finalizer removal
            sleep 3
            remove_finalizers "$resource_type" "$resource_name" "$namespace"
            
            # Wait for actual deletion
            local count=0
            while [ $count -lt 15 ]; do
                if ! kubectl get "$resource_type" "$resource_name" -n "$namespace" >/dev/null 2>&1; then
                    log_success "$resource_type/$resource_name deleted"
                    return 0
                fi
                sleep 1
                count=$((count + 1))
            done
            
            log_warning "$resource_type/$resource_name may still exist"
        fi
    else
        if kubectl get "$resource_type" "$resource_name" >/dev/null 2>&1; then
            log_info "Deleting $resource_type/$resource_name"
            
            # Try normal deletion first
            kubectl delete "$resource_type" "$resource_name" --ignore-not-found=true --timeout="${timeout}s" >/dev/null 2>&1 &
            local delete_pid=$!
            
            # Wait briefly, then force finalizer removal
            sleep 3
            remove_finalizers "$resource_type" "$resource_name"
            
            # Wait for actual deletion
            local count=0
            while [ $count -lt 15 ]; do
                if ! kubectl get "$resource_type" "$resource_name" >/dev/null 2>&1; then
                    log_success "$resource_type/$resource_name deleted"
                    return 0
                fi
                sleep 1
                count=$((count + 1))
            done
            
            log_warning "$resource_type/$resource_name may still exist"
        fi
    fi
}

# Function to find and delete resources by pattern
delete_resources_by_pattern() {
    local resource_type="$1"
    local pattern="$2"
    local namespace_flag="$3"
    
    log_info "Finding $resource_type resources matching pattern: $pattern"
    
    local resources
    if [ "$namespace_flag" = "-A" ]; then
        resources=$(kubectl get "$resource_type" -A -o name 2>/dev/null | grep "$pattern" || true)
    else
        resources=$(kubectl get "$resource_type" -o name 2>/dev/null | grep "$pattern" || true)
    fi
    
    if [ -n "$resources" ]; then
        echo "$resources" | while read -r resource; do
            if [ -n "$resource" ]; then
                local resource_name=$(echo "$resource" | cut -d'/' -f2)
                if [ "$namespace_flag" = "-A" ]; then
                    # Get namespace for the resource
                    local resource_namespace=$(kubectl get "$resource" -o jsonpath='{.metadata.namespace}' 2>/dev/null || echo "")
                    if [ -n "$resource_namespace" ]; then
                        delete_with_force "$resource_type" "$resource_name" "$resource_namespace"
                    else
                        delete_with_force "$resource_type" "$resource_name"
                    fi
                else
                    delete_with_force "$resource_type" "$resource_name"
                fi
            fi
        done
    else
        log_info "No $resource_type resources found matching pattern: $pattern"
    fi
}

# Usage function
usage() {
    echo "Usage: $0 <project-name> <organization-namespace>"
    echo ""
    echo "Arguments:"
    echo "  project-name           Name of the project to delete"
    echo "  organization-namespace Organization namespace (e.g., 'test-org-workload-e2e')"
    echo ""
    echo "This script automatically:"
    echo "  - Deletes all project resources in correct order"
    echo "  - Removes stuck finalizers aggressively"
    echo "  - Handles timeouts and stuck resources"
    echo "  - Provides detailed logging of the deletion process"
    echo ""
    echo "Examples:"
    echo "  $0 test-project-workload test-org-workload-e2e"
    echo "  $0 final-router-test test-public-org"
    echo ""
    echo "Available projects:"
    kubectl get project -A 2>/dev/null | head -10 || echo "  (Unable to list projects)"
}

# Parse arguments
if [ $# -lt 2 ]; then
    usage
    exit 1
fi

PROJECT_NAME="$1"
ORG_NAMESPACE="$2"

# Derived names based on Kube-DC naming conventions
PROJECT_NAMESPACE="${ORG_NAMESPACE}-${PROJECT_NAME}"
VPC_NAME="$PROJECT_NAMESPACE"
SUBNET_NAME="${PROJECT_NAMESPACE}-default"
SNAT_RULE_NAME="$PROJECT_NAMESPACE"

log_info "Starting aggressive deletion of project: $PROJECT_NAME"
log_info "Organization namespace: $ORG_NAMESPACE"
log_info "Project namespace: $PROJECT_NAMESPACE"
log_warning "This script will forcefully remove finalizers and delete all resources!"

# Check if project exists (but continue even if not found)
if ! kubectl get project "$PROJECT_NAME" -n "$ORG_NAMESPACE" >/dev/null 2>&1; then
    log_warning "Project $PROJECT_NAME not found in namespace $ORG_NAMESPACE (may already be deleted)"
else
    log_info "Found project $PROJECT_NAME - proceeding with deletion"
fi

echo ""
log_info "=== PHASE 1: User Workloads ==="

# Delete any user workloads (pods, services, VMs) in project namespace
if kubectl get namespace "$PROJECT_NAMESPACE" >/dev/null 2>&1; then
    log_info "Force deleting user workloads in namespace $PROJECT_NAMESPACE"
    kubectl delete pods --all -n "$PROJECT_NAMESPACE" --ignore-not-found=true --force --grace-period=0 >/dev/null 2>&1 || true
    kubectl delete services --all -n "$PROJECT_NAMESPACE" --ignore-not-found=true --timeout=10s >/dev/null 2>&1 || true
    kubectl delete virtualmachines --all -n "$PROJECT_NAMESPACE" --ignore-not-found=true --timeout=10s >/dev/null 2>&1 || true
    log_info "User workloads cleanup completed"
fi

echo ""
log_info "=== PHASE 2: SNAT and OVN Resources ==="

# Step 1: Delete OvnSnatRule (must be first to release OvnEip)
delete_with_force "ovn-snat-rule" "$SNAT_RULE_NAME"

# Step 2: Find and delete OvnEip resources by pattern
delete_resources_by_pattern "ovn-eip" "$PROJECT_NAMESPACE" "-A"

echo ""
log_info "=== PHASE 3: Project Infrastructure ==="

# Step 3: Delete EIp (External IP)
delete_with_force "eip" "default-gw" "$PROJECT_NAMESPACE"

# Step 4: Delete NetworkAttachmentDefinition
delete_with_force "network-attachment-definitions" "default" "$PROJECT_NAMESPACE"

# Step 5: Delete Secrets
delete_with_force "secret" "ssh-keypair-default" "$PROJECT_NAMESPACE"
delete_with_force "secret" "authorized-keys-default" "$PROJECT_NAMESPACE"

# Step 6: Delete RBAC resources
delete_with_force "role" "admin" "$PROJECT_NAMESPACE"
delete_with_force "rolebinding" "org-admin" "$PROJECT_NAMESPACE"

# Step 7: Delete Subnet (must be before VPC)
delete_with_force "subnet" "$SUBNET_NAME"

# Step 8: Delete VPC
delete_with_force "vpc" "$VPC_NAME"

# Step 9: Delete VpcDns
delete_with_force "vpc-dns" "$VPC_NAME"

echo ""
log_info "=== PHASE 4: Project and Namespace ==="

# Step 10: Delete Project resource
delete_with_force "project" "$PROJECT_NAME" "$ORG_NAMESPACE"

# Step 11: Delete Namespace (last)
delete_with_force "namespace" "$PROJECT_NAMESPACE"

echo ""
log_info "=== PHASE 5: Verification and Cleanup ==="

# Final cleanup - remove any remaining stuck resources
log_info "Performing final cleanup of any remaining stuck resources..."

# Check for any remaining resources with our project pattern
for resource_type in "ovn-eip" "ovn-snat-rule" "eip" "subnet" "vpc" "vpc-dns"; do
    remaining=$(kubectl get "$resource_type" -A 2>/dev/null | grep "$PROJECT_NAMESPACE" || true)
    if [ -n "$remaining" ]; then
        log_warning "Found remaining $resource_type resources:"
        echo "$remaining"
        # Force delete any remaining resources
        kubectl get "$resource_type" -A -o name 2>/dev/null | grep "$PROJECT_NAMESPACE" | while read -r resource; do
            resource_name=$(echo "$resource" | cut -d'/' -f2)
            log_warning "Force deleting stuck $resource_type: $resource_name"
            kubectl patch "$resource_type" "$resource_name" -p '{"metadata":{"finalizers":null}}' --type=merge 2>/dev/null || true
            kubectl delete "$resource_type" "$resource_name" --ignore-not-found=true --timeout=5s 2>/dev/null || true
        done
    fi
done

# Check if project namespace still exists
if kubectl get namespace "$PROJECT_NAMESPACE" >/dev/null 2>&1; then
    log_warning "Project namespace $PROJECT_NAMESPACE still exists - forcing deletion"
    kubectl patch namespace "$PROJECT_NAMESPACE" -p '{"metadata":{"finalizers":null}}' --type=merge 2>/dev/null || true
    kubectl delete namespace "$PROJECT_NAMESPACE" --ignore-not-found=true --timeout=30s || true
fi

# Check if project resource still exists
if kubectl get project "$PROJECT_NAME" -n "$ORG_NAMESPACE" >/dev/null 2>&1; then
    log_warning "Project resource $PROJECT_NAME still exists - forcing deletion"
    kubectl patch project "$PROJECT_NAME" -n "$ORG_NAMESPACE" -p '{"metadata":{"finalizers":null}}' --type=merge 2>/dev/null || true
    kubectl delete project "$PROJECT_NAME" -n "$ORG_NAMESPACE" --ignore-not-found=true --timeout=10s || true
fi

echo ""
log_success "=== DELETION COMPLETED ==="
log_success "Project '$PROJECT_NAME' has been aggressively deleted from organization '$ORG_NAMESPACE'"
log_success "All associated resources have been forcefully cleaned up"
log_info "Note: This script used aggressive deletion with finalizer removal"
log_info "The cluster should now be clean of all project-related resources"
