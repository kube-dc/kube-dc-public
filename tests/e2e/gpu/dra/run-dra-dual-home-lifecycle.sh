#!/usr/bin/env bash

# Guarded acceptance for the v0.5 dual-home + stable-v1 DRA intersection.
# It uses an existing explicitly named project namespace and its existing quota;
# it never creates, deletes, labels, or patches the namespace, NAD, or quota.

set -euo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../hardware" && pwd)/require-opt-in.sh"

for command in kubectl yq grep sed date seq mktemp; do
  command -v "${command}" >/dev/null 2>&1 || { echo "required command is missing: ${command}" >&2; exit 2; }
done
: "${KUBECONFIG:?set KUBECONFIG to the approved lab-cluster kubeconfig}"
: "${GPU_DRA_DUAL_HOME_NAMESPACE:?set GPU_DRA_DUAL_HOME_NAMESPACE to an approved existing dual-homed project namespace}"

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "${script_dir}/../../../.." && pwd)
k=(kubectl --kubeconfig "${KUBECONFIG}")
readonly_project_infrastructure=true
namespace=${GPU_DRA_DUAL_HOME_NAMESPACE}
class=${GPU_DRA_DEVICE_CLASS:-kube-dc-nvidia-v100-shared-8g}
driver=${GPU_DRA_DRIVER:-hami-core-gpu.project-hami.io}
driver_namespace=${GPU_DRA_DRIVER_NAMESPACE:-hami-system}
driver_daemonset=${GPU_DRA_DRIVER_DAEMONSET:-hami-dra-driver-kubelet-plugin}
quota_resource="${class}.deviceclass.resource.k8s.io/devices"
timeout=${GPU_DRA_TIMEOUT:-300s}
workload=dra-dual-home-test
failed_workload=dra-dual-home-cni-fail
lock_namespace=${GPU_HARDWARE_LOCK_NAMESPACE:-gpu-operator}
lock_name=${GPU_HARDWARE_LOCK_NAME:-kube-dc-gpu-hardware-suite-lock}
run_id="dra-dual-home-$(date +%s)-${RANDOM}"
lock_acquired=false
failed_objects_owned=false
workload_objects_owned=false
tmp_dir=$(mktemp -d)
baseline_used=""
message_source="${repo_root}/internal/webhook/core/pod_infra_attachment_webhook.go"
custom_network_message=$(sed -n 's/^[[:space:]]*PodInfraAttachmentCustomSecondaryNetworkMessage = "\(.*\)"$/\1/p' "${message_source}")
[[ -n "${custom_network_message}" ]] || {
  echo "cannot load PodInfraAttachmentCustomSecondaryNetworkMessage from ${message_source}" >&2
  exit 2
}

cleanup_objects() {
  local name=$1
  "${k[@]}" -n "${namespace}" delete deployment "${name}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  "${k[@]}" -n "${namespace}" delete resourceclaimtemplate "${name}-gpu" --ignore-not-found --wait=false >/dev/null 2>&1 || true
}

cleanup() {
  if [[ "${failed_objects_owned}" == true ]]; then
    cleanup_objects "${failed_workload}"
  fi
  if [[ "${workload_objects_owned}" == true ]]; then
    cleanup_objects "${workload}"
  fi
  if [[ "${lock_acquired}" == true ]]; then
    holder=$("${k[@]}" -n "${lock_namespace}" get configmap "${lock_name}" -o jsonpath='{.data.holder}' 2>/dev/null || true)
    [[ "${holder}" != "${run_id}" ]] || "${k[@]}" -n "${lock_namespace}" delete configmap "${lock_name}" --ignore-not-found >/dev/null 2>&1 || true
  fi
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

claim_fingerprint() {
  local excluded_workload=${1:-__none__}
  "${k[@]}" -n "${namespace}" get resourceclaims -o json | EXCLUDED_WORKLOAD="${excluded_workload}" yq e -r \
    '[.items[] | select((.metadata.labels."app.kubernetes.io/name" // "") != strenv(EXCLUDED_WORKLOAD)) | (.metadata.name + "=" + ((.status.allocation != null) | tostring))] | sort | join(",")' -
}

project_label_present=$("${k[@]}" get namespace "${namespace}" -o json | yq e '.metadata.labels | has("kube-dc.com/project")' -)
[[ "${project_label_present}" == "true" ]] || {
  echo "${namespace} is not a kube-dc project namespace" >&2; exit 2;
}
[[ "$("${k[@]}" get namespace "${namespace}" -o jsonpath='{.metadata.labels.network\.kube-dc\.com/infra-attachment}')" == "enabled" ]] || {
  echo "${namespace} is not opted into the v0.5 infra attachment" >&2; exit 2;
}
"${k[@]}" -n "${namespace}" get network-attachment-definition.k8s.cni.cncf.io default >/dev/null
"${k[@]}" get deviceclass "${class}" >/dev/null

driver_json=$("${k[@]}" -n "${driver_namespace}" get daemonset "${driver_daemonset}" -o json)
driver_desired=$(yq e '.status.desiredNumberScheduled // 0' <<<"${driver_json}")
driver_current=$(yq e '.status.currentNumberScheduled // 0' <<<"${driver_json}")
driver_ready=$(yq e '.status.numberReady // 0' <<<"${driver_json}")
[[ "${driver_desired}" -gt 0 && "${driver_current}" == "${driver_desired}" && "${driver_ready}" == "${driver_desired}" ]] || {
  echo "DRA driver ${driver_namespace}/${driver_daemonset} is not ready (desired=${driver_desired}, current=${driver_current}, ready=${driver_ready}); repair the driver before acceptance" >&2
  exit 1
}
shareable=$("${k[@]}" get resourceslices -o json | DRIVER="${driver}" yq e '[.items[] | select(.spec.driver == strenv(DRIVER)) | .spec.devices[]? | select(.allowMultipleAllocations == true)] | length' -)
[[ "${shareable}" -gt 0 ]] || {
  echo "DRA driver ${driver} publishes no shareable ResourceSlice devices; inspect stale/Unknown kubelet-plugin Pods before acceptance" >&2
  exit 1
}

lock_acquired=true
"${k[@]}" -n "${lock_namespace}" create configmap "${lock_name}" \
  --from-literal="holder=${run_id}" --from-literal="startedAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >/dev/null || {
  echo "GPU hardware suite lock is already held" >&2; exit 2;
}

for name in "${workload}" "${failed_workload}"; do
  existing_claims=$("${k[@]}" -n "${namespace}" get resourceclaims -l "app.kubernetes.io/name=${name}" -o json | yq e '.items | length' -)
  if "${k[@]}" -n "${namespace}" get deployment "${name}" >/dev/null 2>&1 ||
     "${k[@]}" -n "${namespace}" get resourceclaimtemplate "${name}-gpu" >/dev/null 2>&1 ||
     [[ "${existing_claims}" -gt 0 ]]; then
    echo "refusing to reuse existing managed test objects for ${name}" >&2
    exit 2
  fi
done

quota_json=$("${k[@]}" -n "${namespace}" get resourcequota -o json)
matching_quotas=$(RESOURCE="${quota_resource}" yq e '[.items[] | select(.status.hard[strenv(RESOURCE)] != null)] | length' <<<"${quota_json}")
[[ "${matching_quotas}" -gt 0 ]] || { echo "${namespace} has no DRA quota for ${class}" >&2; exit 2; }
available=$(RESOURCE="${quota_resource}" yq e '[.items[] | select(.status.hard[strenv(RESOURCE)] != null) | ((.status.hard[strenv(RESOURCE)] | tonumber) - ((.status.used[strenv(RESOURCE)] // "0") | tonumber))] | min' <<<"${quota_json}")
baseline_used=$(RESOURCE="${quota_resource}" yq e '[.items[] | select(.status.hard[strenv(RESOURCE)] != null) | ((.status.used[strenv(RESOURCE)] // "0") | tonumber)] | max' <<<"${quota_json}")
[[ "${available}" -ge 1 ]] || { echo "${namespace} has no free DRA entitlement (available=${available})" >&2; exit 2; }
baseline_claim_fingerprint=$(claim_fingerprint)

sed "s/dra-live-test/${failed_workload}/g" "${script_dir}/canonical-workload.yaml" >"${tmp_dir}/cni-fail.yaml"
yq e -i 'select(.kind != "Deployment") // (select(.kind == "Deployment") | .spec.template.metadata.annotations."k8s.v1.cni.cncf.io/networks" = "forbidden/custom")' "${tmp_dir}/cni-fail.yaml"
event_started_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
failed_objects_owned=true
"${k[@]}" -n "${namespace}" apply -f "${tmp_dir}/cni-fail.yaml" >/dev/null
denied=0
for _ in $(seq 1 30); do
  failed_rs_uid=$("${k[@]}" -n "${namespace}" get replicasets -l "app.kubernetes.io/name=${failed_workload}" -o json | yq e -r '.items[0].metadata.uid // ""' -)
  if [[ -n "${failed_rs_uid}" ]]; then
    denied=$("${k[@]}" -n "${namespace}" get events -o json | STARTED_AT="${event_started_at}" INVOLVED_UID="${failed_rs_uid}" MESSAGE="${custom_network_message}" yq e \
      --from-file "${script_dir}/current-denial-event.yq" -)
  fi
  [[ "${denied}" -gt 0 ]] && break
  sleep 1
done
[[ "${denied}" -gt 0 ]] || {
  echo "current ReplicaSet produced no post-${event_started_at} FailedCreate event containing the shared dual-home denial message" >&2
  exit 1
}
failed_pods=$("${k[@]}" -n "${namespace}" get pods -l "app.kubernetes.io/name=${failed_workload}" -o json | yq e '.items | length' -)
failed_claims=$("${k[@]}" -n "${namespace}" get resourceclaims -l "app.kubernetes.io/name=${failed_workload}" -o json | yq e '.items | length' -)
[[ "${failed_pods}" == "0" && "${failed_claims}" == "0" ]] || {
  echo "dual-home admission failure leaked a Pod or ResourceClaim" >&2; exit 1;
}
cleanup_objects "${failed_workload}"
printf 'PASS admission failure: conflicting networking denied before Pod/claim persistence\n'

sed "s/dra-live-test/${workload}/g" "${script_dir}/canonical-workload.yaml" >"${tmp_dir}/workload.yaml"
workload_objects_owned=true
"${k[@]}" -n "${namespace}" apply -f "${tmp_dir}/workload.yaml" >/dev/null
"${k[@]}" -n "${namespace}" rollout status "deployment/${workload}" --timeout="${timeout}" >/dev/null
pod_name=$("${k[@]}" -n "${namespace}" get pod -l "app.kubernetes.io/name=${workload}" -o jsonpath='{.items[0].metadata.name}')
[[ -n "${pod_name}" ]] || { echo "dual-home DRA Pod was not created" >&2; exit 1; }
if ! cuda_logs=$("${k[@]}" -n "${namespace}" logs "${pod_name}" 2>&1); then
  echo "cannot read CUDA workload logs from ${namespace}/${pod_name}: ${cuda_logs}" >&2
  exit 1
fi
if ! grep -E 'Tesla V100|NVIDIA V100' <<<"${cuda_logs}" >/dev/null; then
  echo "CUDA workload completed but did not identify the qualified NVIDIA V100" >&2
  exit 1
fi

pod_json=$("${k[@]}" -n "${namespace}" get pod "${pod_name}" -o json)
managed=$(yq e -r '.metadata.annotations."network.kube-dc.com/infra-attachment"' <<<"${pod_json}")
networks=$(yq e -r '.metadata.annotations."k8s.v1.cni.cncf.io/networks"' <<<"${pod_json}")
logical_switch=$(yq e -r '.metadata.annotations."ovn.kubernetes.io/logical_switch"' <<<"${pod_json}")
security_groups=$(yq e -r '.metadata.annotations."ovn.kubernetes.io/security_groups"' <<<"${pod_json}")
pod_ip=$(yq e -r '.status.podIP' <<<"${pod_json}")
network_count=$(yq e '.metadata.annotations."k8s.v1.cni.cncf.io/network-status" | from_json | length' <<<"${pod_json}")
infra_matches=$(POD_IP="${pod_ip}" yq e '[.metadata.annotations."k8s.v1.cni.cncf.io/network-status" | from_json | .[] | select(.interface == "eth0") | .ips[] | select(. == strenv(POD_IP))] | length' <<<"${pod_json}")
tenant_matches=$(NAMESPACE="${namespace}" yq e '[.metadata.annotations."k8s.v1.cni.cncf.io/network-status" | from_json | .[] | select(.name == (strenv(NAMESPACE) + "/default") and .interface == "net1" and (.gateway | length > 0))] | length' <<<"${pod_json}")
[[ "${managed}" == "enabled" && "${networks}" == "${namespace}/default" ]] || {
  echo "managed dual-home annotations are incomplete" >&2; exit 1;
}
[[ -n "${logical_switch}" && "${security_groups}" == *"${namespace}"* ]] || {
  echo "infra subnet/security-group attribution is incomplete" >&2; exit 1;
}
[[ "${network_count}" == "2" && "${infra_matches}" == "1" && "${tenant_matches}" == "1" ]] || {
  echo "expected eth0 infra PodIP plus net1 tenant default route, got ${network_count} network(s)" >&2; exit 1;
}

used=$("${k[@]}" -n "${namespace}" get resourcequota -o json | RESOURCE="${quota_resource}" yq e '[.items[] | select(.status.hard[strenv(RESOURCE)] != null) | ((.status.used[strenv(RESOURCE)] // "0") | tonumber)] | max' -)
allocated=$("${k[@]}" -n "${namespace}" get resourceclaims -l "app.kubernetes.io/name=${workload}" -o json | yq e '[.items[] | select(.status.allocation != null)] | length' -)
current_other_claims=$(claim_fingerprint "${workload}")
[[ "${current_other_claims}" == "${baseline_claim_fingerprint}" ]] || {
  echo "concurrent ResourceClaim activity invalidated the quota acceptance baseline; rerun in an idle project" >&2
  exit 2
}
[[ "${used}" == "$((baseline_used + 1))" && "${allocated}" == "1" ]] || {
  echo "expected one allocated dual-home DRA claim and quota increment, got used=${used}, claims=${allocated}" >&2; exit 1;
}
printf 'PASS lifecycle: CUDA, DRA allocation/quota, and both network attachments are healthy\n'

cleanup_objects "${workload}"
for _ in $(seq 1 60); do
  remaining_pods=$("${k[@]}" -n "${namespace}" get pods -l "app.kubernetes.io/name=${workload}" -o json | yq e '.items | length' -)
  remaining_claims=$("${k[@]}" -n "${namespace}" get resourceclaims -l "app.kubernetes.io/name=${workload}" -o json | yq e '.items | length' -)
  used=$("${k[@]}" -n "${namespace}" get resourcequota -o json | RESOURCE="${quota_resource}" yq e '[.items[] | select(.status.hard[strenv(RESOURCE)] != null) | ((.status.used[strenv(RESOURCE)] // "0") | tonumber)] | max' -)
  current_claim_fingerprint=$(claim_fingerprint "${workload}")
  [[ "${current_claim_fingerprint}" == "${baseline_claim_fingerprint}" ]] || {
    echo "concurrent ResourceClaim activity invalidated the cleanup baseline; rerun in an idle project" >&2
    exit 2
  }
  [[ "${remaining_pods}" == "0" && "${remaining_claims}" == "0" && "${used}" == "${baseline_used}" ]] && break
  sleep 1
done
[[ "${remaining_pods:-1}" == "0" && "${remaining_claims:-1}" == "0" && "${used:-}" == "${baseline_used}" ]] || {
  echo "cleanup did not restore the exact Pod/claim/quota baseline" >&2; exit 1;
}
printf 'PASS cleanup: Pod, ResourceClaim, and quota returned to the exact baseline\n'
