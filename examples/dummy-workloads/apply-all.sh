#!/bin/bash

# Script to apply all dummy workload resources to a Kubernetes namespace
# Usage: ./apply-all.sh [namespace]

NAMESPACE=${1:-default}

echo "Applying dummy workloads to namespace: $NAMESPACE"
echo "================================================"

# Apply Deployments
echo ""
echo "Creating Deployments..."
kubectl apply -f deployment.yaml -n $NAMESPACE

# Apply DaemonSets
echo ""
echo "Creating DaemonSets..."
kubectl apply -f daemonset.yaml -n $NAMESPACE

# Apply Jobs
echo ""
echo "Creating Jobs..."
kubectl apply -f job.yaml -n $NAMESPACE

# Apply StatefulSets
echo ""
echo "Creating StatefulSets..."
kubectl apply -f statefulset.yaml -n $NAMESPACE

# Apply ReplicaSets
echo ""
echo "Creating ReplicaSets..."
kubectl apply -f replicaset.yaml -n $NAMESPACE

echo ""
echo "================================================"
echo "All resources applied successfully!"
echo ""
echo "To view the resources, run:"
echo "  kubectl get deployments -n $NAMESPACE"
echo "  kubectl get daemonsets -n $NAMESPACE"
echo "  kubectl get jobs -n $NAMESPACE"
echo "  kubectl get statefulsets -n $NAMESPACE"
echo "  kubectl get replicasets -n $NAMESPACE"
