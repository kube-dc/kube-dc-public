#!/bin/bash

# Script to delete all dummy workload resources from a Kubernetes namespace
# Usage: ./delete-all.sh [namespace]

NAMESPACE=${1:-default}

echo "Deleting dummy workloads from namespace: $NAMESPACE"
echo "================================================"

# Delete Ingresses
echo ""
echo "Deleting Ingresses..."
kubectl delete -f ingress.yaml -n $NAMESPACE

# Delete Services
echo ""
echo "Deleting Services..."
kubectl delete -f service.yaml -n $NAMESPACE

# Delete ReplicaSets
echo ""
echo "Deleting ReplicaSets..."
kubectl delete -f replicaset.yaml -n $NAMESPACE

# Delete StatefulSets
echo ""
echo "Deleting StatefulSets..."
kubectl delete -f statefulset.yaml -n $NAMESPACE

# Delete Jobs
echo ""
echo "Deleting Jobs..."
kubectl delete -f job.yaml -n $NAMESPACE

# Delete DaemonSets
echo ""
echo "Deleting DaemonSets..."
kubectl delete -f daemonset.yaml -n $NAMESPACE

# Delete Deployments
echo ""
echo "Deleting Deployments..."
kubectl delete -f deployment.yaml -n $NAMESPACE

echo ""
echo "================================================"
echo "All resources deleted successfully!"
