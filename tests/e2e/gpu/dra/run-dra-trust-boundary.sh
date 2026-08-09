#!/usr/bin/env bash

# Guarded adversarial acceptance for the B-013 DRA trust boundary.
# It creates an exact-name/exact-label HAMi lookalike in a temporary project
# namespace and proves that neither manager metrics nor operator CLI state trust
# it. The real driver, ResourceSlices, claims, quotas, and product gates are
# never mutated.
set -euo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../hardware" && pwd)/require-opt-in.sh"

for command in kubectl yq grep date seq mktemp; do
  command -v "${command}" >/dev/null 2>&1 || { echo "required command is missing: ${command}" >&2; exit 2; }
done
: "${KUBECONFIG:?set KUBECONFIG to the approved lab-cluster kubeconfig}"
: "${KUBE_DC_CLI:?set KUBE_DC_CLI to the reviewed kube-dc CLI binary}"
[[ -x "${KUBE_DC_CLI}" ]] || { echo "KUBE_DC_CLI is not executable: ${KUBE_DC_CLI}" >&2; exit 2; }

k=(kubectl --kubeconfig "${KUBECONFIG}")
driver_namespace=${GPU_DRA_DRIVER_NAMESPACE:-hami-system}
driver_name=${GPU_DRA_DRIVER_DAEMONSET:-hami-dra-driver-kubelet-plugin}
driver=${GPU_DRA_DRIVER:-hami-core-gpu.project-hami.io}
lock_namespace=${GPU_HARDWARE_LOCK_NAMESPACE:-gpu-operator}
lock_name=${GPU_HARDWARE_LOCK_NAME:-kube-dc-gpu-hardware-suite-lock}
prometheus_proxy=${GPU_PROMETHEUS_PROXY:-/api/v1/namespaces/monitoring/services/http:prom-operator-prometheus:9090/proxy}
observe_seconds=${GPU_DRA_TRUST_OBSERVE_SECONDS:-75}
poll_seconds=${GPU_DRA_TRUST_POLL_SECONDS:-5}
timeout=${GPU_DRA_TIMEOUT:-300s}
run_id="dra-trust-$(date +%s)-${RANDOM}"
namespace=${GPU_DRA_TRUST_NAMESPACE:-${run_id}}
forgery_name=hami-dra-driver-kubelet-plugin
bad_image=docker.io/library/busybox:gpu-dra-tenant-forgery-does-not-exist
tmp_dir=$(mktemp -d)
lock_acquired=false
namespace_cleanup_armed=false

[[ "${observe_seconds}" =~ ^[0-9]+$ && "${observe_seconds}" -ge 65 && "${observe_seconds}" -le 75 ]] || {
  echo "GPU_DRA_TRUST_OBSERVE_SECONDS must be an integer from 65 through 75 to span collection and scrape without reaching the 2m critical-alert dwell" >&2
  exit 2
}
[[ "${poll_seconds}" =~ ^[0-9]+$ && "${poll_seconds}" -gt 0 ]] || {
  echo "GPU_DRA_TRUST_POLL_SECONDS must be a positive integer" >&2
  exit 2
}

metric_value() {
  local metric=$1 response count
  response=$("${k[@]}" get --raw "${prometheus_proxy}/api/v1/query?query=${metric}")
  [[ "$(yq e -r '.status' <<<"${response}")" == "success" ]] || {
    echo "Prometheus query failed for ${metric}" >&2
    return 1
  }
  count=$(yq e '.data.result | length' <<<"${response}")
  [[ "${count}" == "1" ]] || {
    echo "expected exactly one global ${metric} series, got ${count}; deploy the corrected manager before this acceptance" >&2
    return 1
  }
  [[ "$(yq e '.data.result[0].metric | has("profile")' <<<"${response}")" == "false" ]] || {
    echo "global ${metric} still carries a profile label; deploy the corrected manager before this acceptance" >&2
    return 1
  }
  yq e -r '.data.result[0].value[1] | tonumber' <<<"${response}"
}

driver_metric_fingerprint() {
  local desired ready stale discovery
  desired=$(metric_value kube_dc_gpu_dra_driver_desired)
  ready=$(metric_value kube_dc_gpu_dra_driver_ready)
  stale=$(metric_value kube_dc_gpu_dra_unready_driver_pods)
  discovery=$(metric_value kube_dc_gpu_dra_driver_discovery_success)
  printf 'desired=%s,ready=%s,stale=%s,discovery=%s' "${desired}" "${ready}" "${stale}" "${discovery}"
}

claim_fingerprint() {
  "${k[@]}" get resourceclaims -A -o json | yq e -r \
    '[.items[] | (.metadata.namespace + "/" + .metadata.name + "=" + ((.status.allocation != null) | tostring))] | sort | join(",")' -
}

inventory_fingerprint() {
  "${k[@]}" get resourceslices -o json | DRIVER="${driver}" yq e -r \
    '[.items[] | select(.spec.driver == strenv(DRIVER))] as $slices | (($slices | length) | tostring) + "/" + ([$slices[] | .spec.devices[]?] | length | tostring)' -
}

remove_forgery() {
  local owner
  if [[ "${namespace_cleanup_armed}" != true ]]; then
    return 0
  fi
  if ! "${k[@]}" get namespace "${namespace}" >/dev/null 2>&1; then
    namespace_cleanup_armed=false
    return 0
  fi
  owner=$("${k[@]}" get namespace "${namespace}" -o jsonpath='{.metadata.labels.kube-dc\.com/gpu-test-run}' 2>/dev/null || true)
  if [[ "${owner}" != "${run_id}" ]]; then
    echo "refusing to delete namespace ${namespace}: ownership label is ${owner:-missing}, want ${run_id}" >&2
    return 1
  fi
  "${k[@]}" delete namespace "${namespace}" --wait=true --timeout="${timeout}" >/dev/null
  namespace_cleanup_armed=false
}

cleanup() {
  local original_status=$? cleanup_status=0 holder
  remove_forgery || cleanup_status=$?
  if [[ "${lock_acquired}" == true ]]; then
    if [[ "${cleanup_status}" != 0 ]]; then
      echo "retaining hardware lock ${lock_namespace}/${lock_name}: forgery cleanup is incomplete" >&2
    else
      holder=$("${k[@]}" -n "${lock_namespace}" get configmap "${lock_name}" -o jsonpath='{.data.holder}' 2>/dev/null || true)
      if [[ "${holder}" == "${run_id}" ]]; then
        "${k[@]}" -n "${lock_namespace}" delete configmap "${lock_name}" --ignore-not-found >/dev/null 2>&1 || cleanup_status=$?
      fi
    fi
  fi
  rm -rf "${tmp_dir}"
  if [[ "${cleanup_status}" != 0 ]]; then
    return "${cleanup_status}"
  fi
  return "${original_status}"
}
trap cleanup EXIT

"${k[@]}" -n "${driver_namespace}" rollout status daemonset/"${driver_name}" --timeout="${timeout}" >/dev/null
"${k[@]}" get namespace "${namespace}" >/dev/null 2>&1 && {
  echo "refusing to reuse namespace ${namespace}" >&2
  exit 2
}
target_node=$("${k[@]}" get nodes -o json | yq e -r \
  '[.items[] | select(.spec.unschedulable != true) | select([.spec.taints[]? | select(.effect == "NoSchedule" or .effect == "NoExecute")] | length == 0) | select([.status.conditions[] | select(.type == "Ready" and .status == "True")] | length > 0) | .metadata.name] | .[0] // ""' -)
[[ -n "${target_node}" ]] || { echo "no schedulable Ready node exists for the tenant forgery fixture" >&2; exit 2; }

lock_acquired=true
"${k[@]}" -n "${lock_namespace}" create configmap "${lock_name}" \
  --from-literal="holder=${run_id}" --from-literal="startedAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >/dev/null || {
  echo "GPU hardware suite lock is already held" >&2
  exit 2
}

# Recheck every load-bearing baseline after serialization.
"${k[@]}" get namespace "${namespace}" >/dev/null 2>&1 && {
  echo "namespace ${namespace} appeared after lock acquisition; refusing to continue" >&2
  exit 2
}
"${KUBE_DC_CLI}" bootstrap gpu dra postflight --kubeconfig "${KUBECONFIG}" >"${tmp_dir}/baseline-postflight.txt"
"${KUBE_DC_CLI}" bootstrap gpu dra rollback-plan --kubeconfig "${KUBECONFIG}" >"${tmp_dir}/baseline-rollback.txt"
baseline_claims=$(claim_fingerprint)
baseline_inventory=$(inventory_fingerprint)
baseline_metrics=$(driver_metric_fingerprint)
sleep "${poll_seconds}"
[[ "$(driver_metric_fingerprint)" == "${baseline_metrics}" ]] || {
  echo "DRA driver metrics were not stable before the forgery injection" >&2
  exit 1
}
baseline_desired=$(metric_value kube_dc_gpu_dra_driver_desired)
baseline_ready=$(metric_value kube_dc_gpu_dra_driver_ready)
baseline_stale=$(metric_value kube_dc_gpu_dra_unready_driver_pods)
baseline_discovery=$(metric_value kube_dc_gpu_dra_driver_discovery_success)
[[ "${baseline_desired}" -gt 0 && "${baseline_ready}" == "${baseline_desired}" &&
   "${baseline_stale}" == "0" && "${baseline_discovery}" == "1" ]] || {
  echo "DRA global driver baseline is unhealthy: ${baseline_metrics}" >&2
  exit 1
}

namespace_cleanup_armed=true
"${k[@]}" create -f - >/dev/null <<YAML
apiVersion: v1
kind: Namespace
metadata:
  name: ${namespace}
  labels:
    kube-dc.com/project: ""
    kube-dc.com/gpu-test-run: "${run_id}"
    pod-security.kubernetes.io/enforce: restricted
    pod-security.kubernetes.io/enforce-version: latest
---
apiVersion: apps/v1
kind: DaemonSet
metadata:
  namespace: ${namespace}
  name: ${forgery_name}
  labels:
    app.kubernetes.io/name: hami-dra-driver
    app.kubernetes.io/component: kubelet-plugin
    kube-dc.com/gpu-test-run: "${run_id}"
spec:
  selector:
    matchLabels:
      kube-dc.com/gpu-test-run: "${run_id}"
  template:
    metadata:
      labels:
        app.kubernetes.io/name: hami-dra-driver
        hami-dra-driver-component: kubelet-plugin
        kube-dc.com/gpu-test-run: "${run_id}"
    spec:
      nodeSelector:
        kubernetes.io/hostname: "${target_node}"
      containers:
        - name: gpus
          image: ${bad_image}
          command: ["sh", "-c", "sleep 3600"]
          securityContext:
            allowPrivilegeEscalation: false
            capabilities:
              drop: ["ALL"]
            runAsNonRoot: true
            runAsUser: 65532
            seccompProfile:
              type: RuntimeDefault
YAML

for _ in $(seq 1 30); do
  forgery_pod=$("${k[@]}" -n "${namespace}" get pods -l "kube-dc.com/gpu-test-run=${run_id}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
  forgery_desired=$("${k[@]}" -n "${namespace}" get daemonset "${forgery_name}" -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null || true)
  pull_reason=$("${k[@]}" -n "${namespace}" get pod "${forgery_pod:-missing}" -o json 2>/dev/null | \
    yq e -r '.status.containerStatuses[]?.state.waiting.reason // ""' - | grep -E '^(ImagePullBackOff|ErrImagePull)$' | head -1 || true)
  [[ "${forgery_desired:-0}" -gt 0 && -n "${forgery_pod}" && -n "${pull_reason}" ]] && break
  sleep 1
done
[[ "${forgery_desired:-0}" -gt 0 && -n "${forgery_pod:-}" && -n "${pull_reason:-}" ]] || {
  echo "failed to create a scheduled, unready tenant HAMi lookalike" >&2
  exit 1
}
printf 'PASS fixture: tenant lookalike %s/%s is unready on one Ready node\n' "${namespace}" "${forgery_pod}"

elapsed=0
while [[ "${elapsed}" -lt "${observe_seconds}" ]]; do
  current_metrics=$(driver_metric_fingerprint)
  [[ "${current_metrics}" == "${baseline_metrics}" ]] || {
    echo "tenant forgery changed trusted DRA metrics: baseline=${baseline_metrics} current=${current_metrics}" >&2
    exit 1
  }
  sleep "${poll_seconds}"
  elapsed=$((elapsed + poll_seconds))
done
printf 'PASS manager: global driver metrics stayed unchanged for %ss\n' "${observe_seconds}"

"${KUBE_DC_CLI}" bootstrap gpu dra postflight --kubeconfig "${KUBECONFIG}" >"${tmp_dir}/forgery-postflight.txt"
if grep -F "${namespace}/" "${tmp_dir}/forgery-postflight.txt" >/dev/null ||
   grep -F "${forgery_pod}" "${tmp_dir}/forgery-postflight.txt" >/dev/null; then
  echo "CLI postflight trusted or disclosed the tenant forgery" >&2
  exit 1
fi
"${KUBE_DC_CLI}" bootstrap gpu dra rollback-plan --kubeconfig "${KUBECONFIG}" >"${tmp_dir}/forgery-rollback.txt"
if "${KUBE_DC_CLI}" bootstrap gpu dra recovery-plan --kubeconfig "${KUBECONFIG}" \
  --min-driver-unready-age=1s >"${tmp_dir}/forgery-recovery.txt" 2>&1; then
  echo "healthy inventory unexpectedly produced a recovery candidate" >&2
  exit 1
fi
if grep -F "${namespace}/" "${tmp_dir}/forgery-recovery.txt" >/dev/null ||
   grep -F "${forgery_pod}" "${tmp_dir}/forgery-recovery.txt" >/dev/null ||
   grep -q '^Candidate:' "${tmp_dir}/forgery-recovery.txt"; then
  echo "recovery-plan named a tenant Pod" >&2
  exit 1
fi
printf 'PASS CLI: postflight, rollback, and recovery planning ignored the tenant lookalike\n'

forgery_claims=$("${k[@]}" -n "${namespace}" get resourceclaims -o json | yq e '.items | length' -)
[[ "${forgery_claims}" == "0" ]] || {
  echo "tenant forgery unexpectedly created ${forgery_claims} ResourceClaims" >&2
  exit 1
}
[[ "$(inventory_fingerprint)" == "${baseline_inventory}" ]] || {
  echo "trusted DRA inventory changed during trust-boundary acceptance" >&2
  exit 1
}
remove_forgery
for _ in $(seq 1 30); do
  current_claims=$(claim_fingerprint)
  [[ "${current_claims}" == "${baseline_claims}" ]] && break
  sleep 2
done
[[ "${current_claims:-}" == "${baseline_claims}" ]] || {
  echo "ResourceClaim fingerprint did not return to baseline after forgery cleanup" >&2
  exit 1
}
[[ "$(driver_metric_fingerprint)" == "${baseline_metrics}" ]] || {
  echo "driver metrics did not retain their baseline after forgery cleanup" >&2
  exit 1
}
"${KUBE_DC_CLI}" bootstrap gpu dra postflight --kubeconfig "${KUBECONFIG}" >/dev/null
"${KUBE_DC_CLI}" bootstrap gpu dra rollback-plan --kubeconfig "${KUBECONFIG}" >/dev/null
printf 'PASS cleanup: namespace removed; claims, inventory, metrics, and CLI gates remain at baseline\n'

