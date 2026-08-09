#!/usr/bin/env bash

# Reversible lab-only reproduction of B-013. The test keeps the DaemonSet UID
# stable, creates one current-owner ImagePullBackOff Pod on a Ready GPU node,
# waits for the production stale threshold, proves the CLI chooses that exact
# Pod, restores the reviewed image in the OnDelete template, and only then
# executes the separately emitted deletion. Claim/quota fingerprints must be
# identical before and after the exercise.
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
class=${GPU_DRA_DEVICE_CLASS:-kube-dc-nvidia-v100-shared-8g}
flux_namespace=${GPU_DRA_FLUX_NAMESPACE:-flux-system}
flux_name=${GPU_DRA_FLUX_KUSTOMIZATION:-hami}
flux_parent_name=${GPU_DRA_FLUX_PARENT_KUSTOMIZATION:-flux-system}
lock_namespace=${GPU_HARDWARE_LOCK_NAMESPACE:-gpu-operator}
lock_name=${GPU_HARDWARE_LOCK_NAME:-kube-dc-gpu-hardware-suite-lock}
timeout=${GPU_DRA_TIMEOUT:-300s}
flux_stabilize_seconds=${GPU_DRA_FLUX_STABILIZE_SECONDS:-30}
flux_restore_attempts=${GPU_DRA_FLUX_RESTORE_ATTEMPTS:-30}
postflight_attempts=${GPU_DRA_POSTFLIGHT_ATTEMPTS:-90}
bad_image=${GPU_DRA_STALE_BAD_IMAGE:-docker.io/library/busybox:gpu-dra-stale-recovery-does-not-exist}
prometheus_proxy=${GPU_PROMETHEUS_PROXY:-/api/v1/namespaces/monitoring/services/http:prom-operator-prometheus:9090/proxy}
driver_alert_query='ALERTS%7Balertname%3D%22KubeDcGPUDRADriverUnavailable%22%2Calertstate%3D%22firing%22%7D'
stale_alert_query='ALERTS%7Balertname%3D%22KubeDcGPUDRAStaleDriverPod%22%2Calertstate%3D%22firing%22%7D'
run_id="dra-stale-recovery-$(date +%s)-${RANDOM}"
tmp_dir=$(mktemp -d)
lock_acquired=false
flux_changed=false
flux_parent_changed=false
driver_changed=false
original_main_image=""
original_init_image=""
original_strategy=""

claim_fingerprint() {
  "${k[@]}" get resourceclaims -A -o json | yq e -r \
    '[.items[] | (.metadata.namespace + "/" + .metadata.name + "=" + ((.status.allocation != null) | tostring))] | sort | join(",")' -
}

quota_fingerprint() {
  "${k[@]}" get resourcequota -A -o json | RESOURCE="${class}.deviceclass.resource.k8s.io/devices" yq e -r \
    '[.items[] | select(.status.hard[strenv(RESOURCE)] != null) | (.metadata.namespace + "/" + .metadata.name + "=" + (.status.hard[strenv(RESOURCE)] | tostring) + "/" + ((.status.used[strenv(RESOURCE)] // "0") | tostring))] | sort | join(",")' -
}

prometheus_result_count() {
  "${k[@]}" get --raw "${prometheus_proxy}/api/v1/query?query=$1" | yq e '.data.result | length' -
}

resume_flux_owner() {
  local name=$1
  for _ in $(seq 1 "${flux_restore_attempts}"); do
    if "${k[@]}" -n "${flux_namespace}" patch kustomization "${name}" --type=merge -p='{"spec":{"suspend":false}}' >/dev/null 2>&1 &&
       [[ "$("${k[@]}" -n "${flux_namespace}" get kustomization "${name}" -o jsonpath='{.spec.suspend}' 2>/dev/null)" != "true" ]]; then
      return 0
    fi
    sleep 1
  done
  echo "failed to resume Flux Kustomization ${flux_namespace}/${name}" >&2
  return 1
}

restore_driver() {
  local restore_failed=false
  if [[ "${driver_changed}" == true ]]; then
    if [[ -n "${original_main_image}" && -n "${original_init_image}" ]] &&
       "${k[@]}" -n "${driver_namespace}" set image daemonset/"${driver_name}" "gpus=${original_main_image}" "init-container=${original_init_image}" >/dev/null 2>&1 &&
       { [[ "${original_strategy}" != "RollingUpdate" ]] || "${k[@]}" -n "${driver_namespace}" patch daemonset "${driver_name}" --type=merge -p '{"spec":{"updateStrategy":{"type":"RollingUpdate"}}}' >/dev/null 2>&1; }; then
      bad_pod=$("${k[@]}" -n "${driver_namespace}" get pods -l app.kubernetes.io/name=hami-dra-driver -o json | yq e -r '.items[] | select(.status.containerStatuses[]?.state.waiting.reason == "ImagePullBackOff" or .status.initContainerStatuses[]?.state.waiting.reason == "ImagePullBackOff") | .metadata.name' - | head -1)
      if [[ -z "${bad_pod}" ]] || "${k[@]}" -n "${driver_namespace}" delete pod "${bad_pod}" --wait=false >/dev/null 2>&1; then
        driver_changed=false
      else
        echo "failed to delete restored HAMi DRA failure-injection Pod" >&2
        restore_failed=true
      fi
    else
      echo "failed to restore HAMi DRA driver image or update strategy" >&2
      restore_failed=true
    fi
  fi
  if [[ "${flux_changed}" == true ]]; then
    if resume_flux_owner "${flux_name}"; then
      flux_changed=false
    else
      restore_failed=true
    fi
  fi
  if [[ "${flux_parent_changed}" == true ]]; then
    if resume_flux_owner "${flux_parent_name}"; then
      flux_parent_changed=false
    else
      restore_failed=true
    fi
  fi
  [[ "${restore_failed}" == false ]]
}

suspend_flux_owners() {
  # The root Flux Kustomization declares the child HAMi Kustomization. Merely
  # suspending the child is racy: a root reconciliation restores suspend=false
  # and repairs the injected DaemonSet before the alert persistence window.
  if [[ "${flux_parent_name}" != "${flux_name}" ]]; then
    parent_suspended=$("${k[@]}" -n "${flux_namespace}" get kustomization "${flux_parent_name}" -o jsonpath='{.spec.suspend}')
    if [[ "${parent_suspended}" != "true" ]]; then
      flux_parent_changed=true
      "${k[@]}" -n "${flux_namespace}" patch kustomization "${flux_parent_name}" --type=merge -p='{"spec":{"suspend":true}}' >/dev/null
    fi
  fi

  flux_suspended=$("${k[@]}" -n "${flux_namespace}" get kustomization "${flux_name}" -o jsonpath='{.spec.suspend}')
  if [[ "${flux_suspended}" != "true" ]]; then
    flux_changed=true
    "${k[@]}" -n "${flux_namespace}" patch kustomization "${flux_name}" --type=merge -p='{"spec":{"suspend":true}}' >/dev/null
  fi

  # Reassert the child through a bounded quiet period after suspending the
  # parent. This drains a parent reconciliation that was already in flight
  # when spec.suspend changed and could otherwise restore the child once.
  for _ in $(seq 1 "${flux_stabilize_seconds}"); do
    if [[ "${flux_parent_name}" != "${flux_name}" && "$("${k[@]}" -n "${flux_namespace}" get kustomization "${flux_parent_name}" -o jsonpath='{.spec.suspend}')" != "true" ]]; then
      echo "Flux parent ${flux_namespace}/${flux_parent_name} resumed during outage isolation" >&2
      return 1
    fi
    if [[ "$("${k[@]}" -n "${flux_namespace}" get kustomization "${flux_name}" -o jsonpath='{.spec.suspend}')" != "true" ]]; then
      "${k[@]}" -n "${flux_namespace}" patch kustomization "${flux_name}" --type=merge -p='{"spec":{"suspend":true}}' >/dev/null
    fi
    sleep 1
  done
  [[ "$("${k[@]}" -n "${flux_namespace}" get kustomization "${flux_name}" -o jsonpath='{.spec.suspend}')" == "true" ]] || {
    echo "failed to suspend the Flux owner chain for ${flux_namespace}/${flux_name}" >&2
    return 1
  }
}

cleanup() {
  local original_status=$?
  local restore_status=0
  restore_driver || restore_status=$?
  if [[ "${lock_acquired}" == true ]]; then
    if [[ "${restore_status}" != 0 ]]; then
      echo "retaining hardware lock ${lock_namespace}/${lock_name}: restoration is incomplete" >&2
    else
      holder=$("${k[@]}" -n "${lock_namespace}" get configmap "${lock_name}" -o jsonpath='{.data.holder}' 2>/dev/null || true)
      [[ "${holder}" != "${run_id}" ]] || "${k[@]}" -n "${lock_namespace}" delete configmap "${lock_name}" --ignore-not-found >/dev/null 2>&1 || true
    fi
  fi
  rm -rf "${tmp_dir}"
  if [[ "${restore_status}" != 0 ]]; then
    return "${restore_status}"
  fi
  return "${original_status}"
}

trap cleanup EXIT

"${k[@]}" -n "${driver_namespace}" rollout status daemonset/"${driver_name}" --timeout="${timeout}" >/dev/null
"${KUBE_DC_CLI}" bootstrap gpu dra postflight --kubeconfig "${KUBECONFIG}" >/dev/null
"${KUBE_DC_CLI}" bootstrap gpu dra rollback-plan --kubeconfig "${KUBECONFIG}" >/dev/null
baseline_claims=$(claim_fingerprint)
baseline_quota=$(quota_fingerprint)
baseline_slices=$("${k[@]}" get resourceslices -o json | DRIVER="${driver}" yq e '[.items[] | select(.spec.driver == strenv(DRIVER))] | length' -)
[[ "${baseline_slices}" -gt 0 ]] || { echo "no baseline DRA ResourceSlice exists" >&2; exit 1; }

lock_acquired=true
"${k[@]}" -n "${lock_namespace}" create configmap "${lock_name}" \
  --from-literal="holder=${run_id}" --from-literal="startedAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >/dev/null || {
  echo "GPU hardware suite lock is already held" >&2; exit 2;
}

driver_json=$("${k[@]}" -n "${driver_namespace}" get daemonset "${driver_name}" -o json)
driver_uid=$(yq e -r '.metadata.uid' <<<"${driver_json}")
original_strategy=$(yq e -r '.spec.updateStrategy.type' <<<"${driver_json}")
original_main_image=$(yq e -r '.spec.template.spec.containers[] | select(.name == "gpus") | .image' <<<"${driver_json}")
original_init_image=$(yq e -r '.spec.template.spec.initContainers[] | select(.name == "init-container") | .image' <<<"${driver_json}")
[[ -n "${driver_uid}" && -n "${original_main_image}" && -n "${original_init_image}" ]] || {
  echo "cannot establish exact driver UID/image baseline" >&2; exit 2;
}
[[ "${original_strategy}" == "RollingUpdate" ]] || {
  echo "refusing to mutate a driver whose update strategy is ${original_strategy}" >&2; exit 2;
}

suspend_flux_owners
driver_changed=true
"${k[@]}" -n "${driver_namespace}" patch daemonset "${driver_name}" --type=merge -p='{"spec":{"updateStrategy":{"type":"OnDelete"}}}' >/dev/null
"${k[@]}" -n "${driver_namespace}" set image daemonset/"${driver_name}" "gpus=${bad_image}" "init-container=${bad_image}" >/dev/null

old_pod=$("${k[@]}" -n "${driver_namespace}" get pods -l hami-dra-driver-component=kubelet-plugin -o jsonpath='{.items[0].metadata.name}')
"${k[@]}" -n "${driver_namespace}" delete pod "${old_pod}" --wait=true --timeout="${timeout}" >/dev/null
for _ in $(seq 1 120); do
  candidate=$("${k[@]}" -n "${driver_namespace}" get pods -l hami-dra-driver-component=kubelet-plugin -o json | \
    DS_UID="${driver_uid}" yq e -r '.items[] | select(.metadata.ownerReferences[]? | .uid == strenv(DS_UID)) | .metadata.name' - | head -1)
  # An init-container pull failure leaves the main container waiting with
  # PodInitializing. Select the actual pull error across both status lists
  # instead of letting that non-null main-container reason mask the init error.
  reason=$("${k[@]}" -n "${driver_namespace}" get pod "${candidate:-missing}" -o json 2>/dev/null | \
    yq e -r '[.status.initContainerStatuses[]?.state.waiting.reason, .status.containerStatuses[]?.state.waiting.reason] | map(select(. == "ImagePullBackOff" or . == "ErrImagePull")) | .[0] // ""' - || true)
  [[ -n "${candidate}" && ( "${reason}" == "ImagePullBackOff" || "${reason}" == "ErrImagePull" ) ]] && break
  sleep 1
done
[[ -n "${candidate:-}" && ( "${reason:-}" == "ImagePullBackOff" || "${reason:-}" == "ErrImagePull" ) ]] || {
  echo "failed to create the exact current-DaemonSet-owned unready Pod" >&2; exit 1;
}

for _ in $(seq 1 120); do
  slices=$("${k[@]}" get resourceslices -o json | DRIVER="${driver}" yq e '[.items[] | select(.spec.driver == strenv(DRIVER))] | length' -)
  [[ "${slices}" == "0" ]] && break
  sleep 1
done
[[ "${slices:-1}" == "0" ]] || { echo "DRA inventory did not become empty" >&2; exit 1; }
printf 'PASS injection: current driver Pod %s is unready and inventory is empty\n' "${candidate}"

driver_alert_fired=false
stale_alert_fired=false
candidate_ready_transition=$("${k[@]}" -n "${driver_namespace}" get pod "${candidate}" -o jsonpath='{.status.conditions[?(@.type=="Ready")].lastTransitionTime}')
[[ -n "${candidate_ready_transition}" ]] || { echo "candidate Pod has no PodReady transition timestamp" >&2; exit 1; }
# Anchor the deadline to the API-stamped PodReady=False transition used by the
# manager and CLI, plus the ten-minute qualification and two-minute alert dwell.
alert_deadline=$(( $(date -u -d "${candidate_ready_transition} + 16 minutes" +%s) ))
while (( $(date +%s) <= alert_deadline )); do
  if ! "${k[@]}" -n "${driver_namespace}" get pod "${candidate}" >/dev/null 2>&1; then
    echo "injected driver Pod disappeared before recovery; verify the Flux owner chain" >&2
    exit 1
  fi
  [[ "$(prometheus_result_count "${driver_alert_query}")" == "0" ]] || driver_alert_fired=true
  [[ "$(prometheus_result_count "${stale_alert_query}")" == "0" ]] || stale_alert_fired=true
  [[ "${driver_alert_fired}" == true && "${stale_alert_fired}" == true ]] && break
  sleep 5
done
[[ "${driver_alert_fired}" == true && "${stale_alert_fired}" == true ]] || {
  echo "DRA driver/stale-Pod alerts did not both fire within sixteen minutes of the PodReady=False transition" >&2; exit 1;
}
printf 'PASS detection: driver-unavailable and stale-driver-Pod alerts are firing\n'

"${k[@]}" -n "${driver_namespace}" set image daemonset/"${driver_name}" \
  "gpus=${original_main_image}" "init-container=${original_init_image}" >/dev/null

"${KUBE_DC_CLI}" bootstrap gpu dra recovery-plan --kubeconfig "${KUBECONFIG}" \
  --min-driver-unready-age=10m >"${tmp_dir}/recovery-plan.txt"
grep -F "Candidate: ${driver_namespace}/${candidate}" "${tmp_dir}/recovery-plan.txt" >/dev/null || {
  echo "recovery planner did not select the exact injected Pod" >&2; exit 1;
}
grep -F "delete pod ${candidate} --wait=false" "${tmp_dir}/recovery-plan.txt" >/dev/null || {
  echo "recovery planner did not emit the exact reviewed deletion" >&2; exit 1;
}
"${k[@]}" -n "${driver_namespace}" delete pod "${candidate}" --wait=false >/dev/null
"${k[@]}" -n "${driver_namespace}" patch daemonset "${driver_name}" --type=merge -p='{"spec":{"updateStrategy":{"type":"RollingUpdate"}}}' >/dev/null
driver_changed=false
restore_driver

"${k[@]}" -n "${driver_namespace}" rollout status daemonset/"${driver_name}" --timeout="${timeout}" >/dev/null
for _ in $(seq 1 120); do
  slices=$("${k[@]}" get resourceslices -o json | DRIVER="${driver}" yq e '[.items[] | select(.spec.driver == strenv(DRIVER))] | length' -)
  [[ "${slices}" == "${baseline_slices}" ]] && break
  sleep 1
done
[[ "${slices:-0}" == "${baseline_slices}" ]] || { echo "DRA ResourceSlice baseline did not recover" >&2; exit 1; }
postflight_ready=false
# The outage is deliberately longer than the ten-minute allocation-canary
# freshness gate. After driver recovery, wait through one */5 schedule plus
# normal claim/Pod completion instead of treating the expected stale canary as
# a failed driver recovery. Preserve the last report for actionable failure.
for _ in $(seq 1 "${postflight_attempts}"); do
  if "${KUBE_DC_CLI}" bootstrap gpu dra postflight --kubeconfig "${KUBECONFIG}" >"${tmp_dir}/postflight-recovery.txt" 2>&1; then
    postflight_ready=true
    break
  fi
  sleep 5
done
if [[ "${postflight_ready}" != true ]]; then
  cat "${tmp_dir}/postflight-recovery.txt" >&2
  echo "DRA postflight did not recover after the allocation-canary window" >&2
  exit 1
fi
[[ "$(claim_fingerprint)" == "${baseline_claims}" ]] || { echo "ResourceClaim baseline drifted" >&2; exit 1; }
[[ "$(quota_fingerprint)" == "${baseline_quota}" ]] || { echo "DRA quota baseline drifted" >&2; exit 1; }
alerts_cleared=false
for _ in $(seq 1 60); do
  if [[ "$(prometheus_result_count "${driver_alert_query}")" == "0" && "$(prometheus_result_count "${stale_alert_query}")" == "0" ]]; then
    alerts_cleared=true
    break
  fi
  sleep 5
done
[[ "${alerts_cleared}" == true ]] || { echo "DRA driver alerts did not clear within five minutes" >&2; exit 1; }
printf 'PASS recovery: driver, inventory, claims, quota, and postflight returned to baseline\n'
