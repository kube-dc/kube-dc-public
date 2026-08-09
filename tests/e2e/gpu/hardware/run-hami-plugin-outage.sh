#!/usr/bin/env bash

# Approved-lab failure injection: freeze HAMi's real device-plugin process,
# prove the allocation canary and alert detect cached-but-dead capacity, resume
# the process, and prove allocation plus alert recovery.

set -euo pipefail

source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/require-opt-in.sh"

for command in kubectl yq awk grep seq date sleep; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "required command is missing: ${command}" >&2
    exit 2
  fi
done

: "${KUBECONFIG:?set KUBECONFIG to the approved lab-cluster kubeconfig}"

kubectl_args=(--kubeconfig "${KUBECONFIG}")
run_id="hami-outage-$(date +%s)-${RANDOM}"
lock_namespace=${GPU_HARDWARE_LOCK_NAMESPACE:-gpu-operator}
lock_name=${GPU_HARDWARE_LOCK_NAME:-kube-dc-gpu-hardware-suite-lock}
canary_name=${GPU_HARDWARE_CANARY_NAME:-hami-device-plugin-canary}
expected_shares=${GPU_HARDWARE_EXPECTED_SHARES:-20}
prometheus_proxy=${GPU_PROMETHEUS_PROXY:-/api/v1/namespaces/monitoring/services/http:prom-operator-prometheus:9090/proxy}
alert_query='ALERTS%7Balertname%3D%22KubeDcHamiAllocationCanaryStuck%22%2Calertstate%3D%22firing%22%7D'
outage_job="hami-device-plugin-canary-$(date +%s)"
recovery_job="hami-outage-recovery-$(date +%s)"
canary_was_suspended=
plugin_pod=
container_id=
plugin_frozen=false
lock_acquired=false

prometheus_result_count() {
  kubectl "${kubectl_args[@]}" get --raw \
    "${prometheus_proxy}/api/v1/query?query=$1" | yq e '.data.result | length' -
}

signal_plugin() {
  local signal=$1
  kubectl "${kubectl_args[@]}" -n "${lock_namespace}" exec "${plugin_pod}" -c device-plugin -- \
    sh -ec '
      expected=$1
      signal=$2
      pids=$(pgrep -f "^nvidia-device-plugin( |$)")
      set -- $pids
      [ "$#" -eq 1 ]
      pid=$1
      [ "$pid" -ne 1 ]
      grep -q "cri-containerd-${expected}.scope" "/proc/$pid/cgroup"
      kill "-${signal}" "$pid"
      grep -E "^(Name|State|Pid|PPid):" "/proc/$pid/status"
    ' sh "${container_id}" "${signal}"
}

preflight() {
  active_holders=$(kubectl "${kubectl_args[@]}" get pods -A -o json | yq e \
    '[.items[] | select(.status.phase != "Succeeded" and .status.phase != "Failed") | select([.spec.containers[]? | select(.resources.requests."nvidia.com/gpu" != null)] | length > 0)] | length' -)
  shares=$(kubectl "${kubectl_args[@]}" get nodes -o json | yq e -r \
    '.items[] | select(.metadata.labels."kube-dc.com/gpu.expected-workload-mode" == "pod-hami") | (.status.allocatable."nvidia.com/gpu" // "0")' - | \
    awk '{ total += $1 } END { print total + 0 }')
  wrong_mode=$(kubectl "${kubectl_args[@]}" get nodes -o json | yq e \
    '[.items[] | select(.metadata.labels."kube-dc.com/gpu.expected-workload-mode" == "pod-hami") | select(.metadata.labels."kube-dc.com/gpu.workload-mode" != "pod-hami")] | length' -)
  if [[ "${active_holders}" != 0 || "${shares}" != "${expected_shares}" || "${wrong_mode}" != 0 ]]; then
    echo "preflight failed: activeHolders=${active_holders} shares=${shares}/${expected_shares} wrongMode=${wrong_mode}" >&2
    return 1
  fi
  if [[ "$(prometheus_result_count "${alert_query}")" != 0 ]]; then
    echo "allocation-canary-stuck alert is already firing" >&2
    return 1
  fi
  plugin_count=$(kubectl "${kubectl_args[@]}" -n "${lock_namespace}" get pods \
    -l app.kubernetes.io/component=hami-device-plugin -o json | \
    yq e '[.items[] | select(.status.phase == "Running")] | length' -)
  if [[ "${plugin_count}" != 1 ]]; then
    echo "expected exactly one running HAMi device-plugin Pod, got ${plugin_count}" >&2
    return 1
  fi
  plugin_pod=$(kubectl "${kubectl_args[@]}" -n "${lock_namespace}" get pods \
    -l app.kubernetes.io/component=hami-device-plugin -o json | \
    yq e -r '.items[] | select(.status.phase == "Running") | .metadata.name' -)
  container_id=$(kubectl "${kubectl_args[@]}" -n "${lock_namespace}" get pod "${plugin_pod}" \
    -o jsonpath='{.status.containerStatuses[?(@.name=="device-plugin")].containerID}')
  container_id=${container_id#*://}
  [[ -n "${container_id}" ]] || { echo "device-plugin container ID is empty" >&2; return 1; }
}

cleanup() {
  if [[ "${plugin_frozen}" == true && -n "${plugin_pod}" && -n "${container_id}" ]]; then
    if signal_plugin CONT >/dev/null 2>&1; then
      plugin_frozen=false
    else
      echo "warning: CONT failed; deleting ${plugin_pod} so the DaemonSet can recover" >&2
      kubectl "${kubectl_args[@]}" -n "${lock_namespace}" delete pod "${plugin_pod}" \
        --wait=false >/dev/null 2>&1 || true
    fi
  fi

  kubectl "${kubectl_args[@]}" -n "${lock_namespace}" delete job \
    "${outage_job}" "${recovery_job}" --ignore-not-found --wait=false >/dev/null 2>&1 || true

  if [[ -n "${canary_was_suspended}" ]]; then
    kubectl "${kubectl_args[@]}" -n "${lock_namespace}" patch cronjob "${canary_name}" \
      --type=merge -p "{\"spec\":{\"suspend\":${canary_was_suspended}}}" >/dev/null 2>&1 || true
  fi

  if [[ "${lock_acquired}" == true ]]; then
    holder=$(kubectl "${kubectl_args[@]}" -n "${lock_namespace}" get configmap "${lock_name}" \
      -o jsonpath='{.data.holder}' 2>/dev/null || true)
    if [[ "${holder}" == "${run_id}" ]]; then
      kubectl "${kubectl_args[@]}" -n "${lock_namespace}" delete configmap "${lock_name}" \
        --ignore-not-found >/dev/null 2>&1 || true
    fi
  fi
}
trap cleanup EXIT

preflight

if ! kubectl "${kubectl_args[@]}" -n "${lock_namespace}" create configmap "${lock_name}" \
  --from-literal="holder=${run_id}" --from-literal="startedAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >/dev/null; then
  echo "GPU hardware suite lock is already held" >&2
  exit 2
fi
lock_acquired=true
preflight

canary_was_suspended=$(kubectl "${kubectl_args[@]}" -n "${lock_namespace}" get cronjob "${canary_name}" \
  -o jsonpath='{.spec.suspend}')
[[ -n "${canary_was_suspended}" ]] || canary_was_suspended=false
kubectl "${kubectl_args[@]}" -n "${lock_namespace}" patch cronjob "${canary_name}" \
  --type=merge -p '{"spec":{"suspend":true}}' >/dev/null

plugin_frozen=true
freeze_status=$(signal_plugin STOP)
if ! grep -Eq '^State:[[:space:]]+T' <<<"${freeze_status}"; then
  echo "device plugin did not enter stopped state: ${freeze_status}" >&2
  exit 1
fi
printf 'PASS injection: exact device-plugin process is stopped; allocatable remains cached at %s\n' "${shares}"

kubectl "${kubectl_args[@]}" -n "${lock_namespace}" create job \
  --from="cronjob/${canary_name}" "${outage_job}" >/dev/null
for attempt in $(seq 1 60); do
  active=$(kubectl "${kubectl_args[@]}" -n "${lock_namespace}" get job "${outage_job}" \
    -o jsonpath='{.status.active}' 2>/dev/null || true)
  [[ "${active}" == 1 ]] && break
  sleep 1
done
if [[ "${active}" != 1 ]]; then
  echo "outage canary did not become active" >&2
  exit 1
fi

alert_fired=false
for attempt in $(seq 1 60); do
  if [[ "$(prometheus_result_count "${alert_query}")" -gt 0 ]]; then
    alert_fired=true
    break
  fi
  sleep 5
done
if [[ "${alert_fired}" != true ]]; then
  echo "KubeDcHamiAllocationCanaryStuck did not fire within five minutes" >&2
  exit 1
fi
printf 'PASS detection: allocation canary stayed active and KubeDcHamiAllocationCanaryStuck fired\n'

resume_status=$(signal_plugin CONT)
plugin_frozen=false
if ! grep -Eq '^State:[[:space:]]+[RS]' <<<"${resume_status}"; then
  echo "device plugin did not resume: ${resume_status}" >&2
  exit 1
fi
kubectl "${kubectl_args[@]}" -n "${lock_namespace}" wait --for=condition=complete \
  "job/${outage_job}" --timeout=180s >/dev/null

kubectl "${kubectl_args[@]}" -n "${lock_namespace}" create job \
  --from="cronjob/${canary_name}" "${recovery_job}" >/dev/null
kubectl "${kubectl_args[@]}" -n "${lock_namespace}" wait --for=condition=complete \
  "job/${recovery_job}" --timeout=180s >/dev/null

alert_cleared=false
for attempt in $(seq 1 60); do
  if [[ "$(prometheus_result_count "${alert_query}")" == 0 ]]; then
    alert_cleared=true
    break
  fi
  sleep 5
done
if [[ "${alert_cleared}" != true ]]; then
  echo "KubeDcHamiAllocationCanaryStuck did not clear within five minutes" >&2
  exit 1
fi

final_shares=$(kubectl "${kubectl_args[@]}" get nodes -o json | yq e -r \
  '.items[] | select(.metadata.labels."kube-dc.com/gpu.expected-workload-mode" == "pod-hami") | (.status.allocatable."nvidia.com/gpu" // "0")' - | \
  awk '{ total += $1 } END { print total + 0 }')
if [[ "${final_shares}" != "${expected_shares}" ]]; then
  echo "allocatable shares did not recover: ${final_shares}/${expected_shares}" >&2
  exit 1
fi
printf 'PASS recovery: blocked and fresh canaries completed, alert cleared, shares=%s\n' "${final_shares}"
