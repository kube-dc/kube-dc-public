#!/usr/bin/env bash
# vm-bench — repeatable DataVolume provisioning benchmarks for the
# vm-startup-acceleration work (docs/prd/vm-startup-implementation-plan.md M0-T02).
#
# Times DataVolume create -> Succeeded for the paths compared in the PRD:
#   http           CDI http import (in-cluster S3 mirror or any URL)
#   registry-node  CDI registry import with pullMethod: node (cold vs warm is
#                  determined by whether the image is already in the scheduled
#                  node's containerd cache — the result row prints the node)
#   clone          CDI host-assisted clone from an existing PVC
#
# Uses the current KUBECONFIG context. All cluster-specific values are flags —
# no real endpoints are baked in (this file is mirrored to the public repo).
#
# Examples:
#   ./bench.sh --ns vm-bench --mode registry-node \
#       --image "docker://quay.io/containerdisks/ubuntu:24.04@sha256:..." --runs 2
#   ./bench.sh --ns vm-bench --mode http --url "https://s3.example.com/cdi-os-images/ubuntu/24.04/latest/img"
#   ./bench.sh --ns vm-bench --mode clone --source-pvc golden-ubuntu
#   ./bench.sh --ns vm-bench --cleanup
set -euo pipefail

NS=vm-bench SC=local-path SIZE=20Gi MODE="" URL="" IMAGE="" SRC_PVC="" RUNS=1 KEEP=0 CLEANUP=0 TIMEOUT=900
while [[ $# -gt 0 ]]; do
  case "$1" in
    --ns) NS=$2; shift 2;;
    --sc) SC=$2; shift 2;;
    --size) SIZE=$2; shift 2;;
    --mode) MODE=$2; shift 2;;
    --url) URL=$2; shift 2;;
    --image) IMAGE=$2; shift 2;;
    --source-pvc) SRC_PVC=$2; shift 2;;
    --runs) RUNS=$2; shift 2;;
    --timeout) TIMEOUT=$2; shift 2;;
    --keep) KEEP=1; shift;;
    --cleanup) CLEANUP=1; shift;;
    *) echo "unknown flag: $1" >&2; exit 2;;
  esac
done

if [[ $CLEANUP -eq 1 ]]; then
  kubectl delete dv -n "$NS" -l vm-bench=true --ignore-not-found
  echo "cleanup: deleted vm-bench DataVolumes in $NS"
  exit 0
fi

[[ -n "$MODE" ]] || { echo "--mode required (http|registry-node|clone)" >&2; exit 2; }
kubectl get ns "$NS" >/dev/null 2>&1 || kubectl create ns "$NS"

source_block() {
  case "$MODE" in
    http)
      [[ -n "$URL" ]] || { echo "--url required for http" >&2; exit 2; }
      printf 'http:\n      url: "%s"' "$URL";;
    registry-node)
      [[ -n "$IMAGE" ]] || { echo "--image required for registry-node" >&2; exit 2; }
      [[ "$IMAGE" == *"@sha256:"* ]] || echo "WARN: image ref is not digest-pinned (rollout gate!)" >&2
      printf 'registry:\n      url: "%s"\n      pullMethod: node' "$IMAGE";;
    clone)
      [[ -n "$SRC_PVC" ]] || { echo "--source-pvc required for clone" >&2; exit 2; }
      printf 'pvc:\n      name: "%s"\n      namespace: "%s"' "$SRC_PVC" "$NS";;
    *) echo "unknown mode: $MODE" >&2; exit 2;;
  esac
}

echo "mode=$MODE ns=$NS sc=$SC size=$SIZE runs=$RUNS"
echo "run  name                     result   seconds  node"
for i in $(seq 1 "$RUNS"); do
  name="bench-${MODE//[^a-z0-9]/-}-$(date +%s)-$i"
  kubectl apply -f - >/dev/null <<EOF
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: $name
  namespace: $NS
  labels: { vm-bench: "true" }
  annotations:
    cdi.kubevirt.io/storage.bind.immediate.requested: "true"
spec:
  source:
    $(source_block)
  pvc:
    accessModes: [ReadWriteOnce]
    resources: { requests: { storage: $SIZE } }
    storageClassName: $SC
EOF
  start=$(date +%s); result=timeout; node="-"
  while (( $(date +%s) - start < TIMEOUT )); do
    phase=$(kubectl get dv "$name" -n "$NS" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    n=$(kubectl get pods -n "$NS" -o jsonpath="{range .items[?(@.metadata.labels.cdi\.kubevirt\.io=='importer')]}{.spec.nodeName}{end}" 2>/dev/null || true)
    [[ -n "$n" ]] && node="$n"
    if [[ "$phase" == "Succeeded" ]]; then result=ok; break; fi
    if [[ "$phase" == "Failed" ]]; then result=failed; break; fi
    sleep 2
  done
  dur=$(( $(date +%s) - start ))
  pvnode=$(kubectl get pv "$(kubectl get pvc "$name" -n "$NS" -o jsonpath='{.spec.volumeName}' 2>/dev/null)" \
      -o jsonpath='{.spec.nodeAffinity.required.nodeSelectorTerms[0].matchExpressions[0].values[0]}' 2>/dev/null || true)
  [[ -n "$pvnode" ]] && node="$pvnode"
  printf '%-4s %-24s %-8s %-8s %s\n' "$i" "$name" "$result" "$dur" "$node"
  [[ $KEEP -eq 1 ]] || kubectl delete dv "$name" -n "$NS" --wait=false >/dev/null
done
