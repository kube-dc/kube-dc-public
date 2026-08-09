#!/usr/bin/env bash

# Exercises the installed kube-dc HAMi policy and HAMi mutation webhook without
# persisting Pods. The caller must already be authorized to create Pods in the
# selected project namespace.

set -uo pipefail

for command in kubectl yq; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "required command is missing: ${command}" >&2
    exit 2
  fi
done

: "${KUBECONFIG:?set KUBECONFIG to the test-cluster kubeconfig}"
: "${GPU_ADMISSION_NAMESPACE:?set GPU_ADMISSION_NAMESPACE to a kube-dc project namespace}"
: "${GPU_ADMISSION_SCHEDULER:=hami-scheduler}"

if [[ ! "${GPU_ADMISSION_NAMESPACE}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]]; then
  echo "GPU_ADMISSION_NAMESPACE is not a valid DNS label" >&2
  exit 2
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
valid_fixture="${script_dir}/valid-gpu-pod.yaml"
plain_fixture="${script_dir}/plain-pod.yaml"
kubectl_args=(--kubeconfig "${KUBECONFIG}")
if [[ -n "${GPU_ADMISSION_AS:-}" ]]; then
  kubectl_args+=(--as="${GPU_ADMISSION_AS}")
fi

failures=0

render_fixture() {
  local file=$1
  local expression=$2
  GPU_ADMISSION_NAMESPACE="${GPU_ADMISSION_NAMESPACE}" yq e \
    '(.metadata.namespace = strenv(GPU_ADMISSION_NAMESPACE)) | ('"${expression}"')' \
    "${file}"
}

allow_case() {
  local label=$1
  local file=$2
  local expression=$3
  local expected_scheduler=$4
  local output
  local status

  output=$(render_fixture "${file}" "${expression}" | \
    kubectl "${kubectl_args[@]}" create --dry-run=server -o yaml -f - 2>&1)
  status=$?
  if [[ ${status} -eq 0 ]]; then
    local scheduler
    scheduler=$(printf '%s\n' "${output}" | yq e '.spec.schedulerName // "default-scheduler"' -)
    if [[ "${scheduler}" == "${expected_scheduler}" ]]; then
      printf 'PASS allow: %-24s scheduler=%s\n' "${label}" "${scheduler}"
    else
      printf 'FAIL allow: %s :: expected scheduler=%s, got=%s\n' \
        "${label}" "${expected_scheduler}" "${scheduler}" >&2
      failures=$((failures + 1))
    fi
  else
    printf 'FAIL allow: %s :: %s\n' "${label}" "${output}" >&2
    failures=$((failures + 1))
  fi
}

deny_case() {
  local label=$1
  local file=$2
  local expression=$3
  local output
  local status

  output=$(render_fixture "${file}" "${expression}" | \
    kubectl "${kubectl_args[@]}" create --dry-run=server -o name -f - 2>&1)
  status=$?
  if [[ ${status} -ne 0 ]]; then
    printf 'PASS deny:  %s\n' "${label}"
  else
    printf 'FAIL deny:  %s (admitted)\n' "${label}" >&2
    failures=$((failures + 1))
  fi
}

deny_ephemeral_attach_case() {
  local pod=${GPU_ADMISSION_EPHEMERAL_POD:-}
  local output
  local status

  if [[ -z "${pod}" ]]; then
    printf 'SKIP deny:  ephemeral-attach (set GPU_ADMISSION_EPHEMERAL_POD)\n'
    return
  fi
  if [[ ! "${pod}" =~ ^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$ ]]; then
    printf 'FAIL deny:  ephemeral-attach :: invalid Pod name\n' >&2
    failures=$((failures + 1))
    return
  fi

  output=$(kubectl "${kubectl_args[@]}" -n "${GPU_ADMISSION_NAMESPACE}" get pod "${pod}" -o json | \
    yq e -o=json '.spec.ephemeralContainers = ((.spec.ephemeralContainers // []) + [{"name":"gpu-policy-debug","image":"registry.k8s.io/pause:3.10","envFrom":[{"configMapRef":{"name":"cuda-settings"}}]}]) | del(.status)' - | \
    kubectl "${kubectl_args[@]}" replace \
      --raw "/api/v1/namespaces/${GPU_ADMISSION_NAMESPACE}/pods/${pod}/ephemeralcontainers?dryRun=All" \
      -f - 2>&1)
  status=$?
  if [[ ${status} -ne 0 ]] && printf '%s' "${output}" | \
    grep -q 'envFrom is not allowed for shared GPU Pods'; then
    printf 'PASS deny:  ephemeral-attach\n'
  else
    printf 'FAIL deny:  ephemeral-attach :: status=%s output=%s\n' \
      "${status}" "${output}" >&2
    failures=$((failures + 1))
  fi
}

allow_scheduled_update_case() {
  local pod=${GPU_ADMISSION_EPHEMERAL_POD:-}
  local output

  if [[ -z "${pod}" ]]; then
    printf 'SKIP allow: scheduled-pod-update (set GPU_ADMISSION_EPHEMERAL_POD)\n'
    return
  fi
  output=$(kubectl "${kubectl_args[@]}" -n "${GPU_ADMISSION_NAMESPACE}" patch pod "${pod}" \
    --type=merge --dry-run=server \
    -p '{"metadata":{"annotations":{"kube-dc.com/gpu-policy-update-test":"true"}}}' \
    -o name 2>&1)
  if [[ $? -eq 0 ]]; then
    printf 'PASS allow: scheduled-pod-update\n'
  else
    printf 'FAIL allow: scheduled-pod-update :: %s\n' "${output}" >&2
    failures=$((failures + 1))
  fi
}

allow_case plain "${plain_fixture}" '.' default-scheduler
allow_case valid "${valid_fixture}" '.' "${GPU_ADMISSION_SCHEDULER}"
allow_case semantic-quantity "${valid_fixture}" \
  '.spec.containers[0].resources.requests."nvidia.com/gpumem" = "1Ki"' \
  "${GPU_ADMISSION_SCHEDULER}"
allow_case gpu-plus-sidecar "${valid_fixture}" \
  '.spec.containers += [{"name":"sidecar","image":"registry.k8s.io/pause:3.10","securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]' \
  "${GPU_ADMISSION_SCHEDULER}"

deny_case missing-profile-label "${valid_fixture}" 'del(.metadata.labels)'
deny_case unknown-profile "${valid_fixture}" \
  '.metadata.labels."kube-dc.com/gpu-profile" = "unknown"'
deny_case label-without-resources "${plain_fixture}" \
  '.metadata.labels."kube-dc.com/gpu-profile" = "nvidia-v100-hami"'
deny_case request-limit-mismatch "${valid_fixture}" \
  '.spec.containers[0].resources.requests."nvidia.com/gpumem" = "2048"'
deny_case memory-below-min "${valid_fixture}" \
  '.spec.containers[0].resources.requests."nvidia.com/gpumem" = "512" | .spec.containers[0].resources.limits."nvidia.com/gpumem" = "512"'
deny_case memory-wrong-step "${valid_fixture}" \
  '.spec.containers[0].resources.requests."nvidia.com/gpumem" = "1536" | .spec.containers[0].resources.limits."nvidia.com/gpumem" = "1536"'
deny_case memory-above-device "${valid_fixture}" \
  '.spec.containers[0].resources.requests."nvidia.com/gpumem" = "33792" | .spec.containers[0].resources.limits."nvidia.com/gpumem" = "33792"'
deny_case core-wrong-step "${valid_fixture}" \
  '.spec.containers[0].resources.requests."nvidia.com/gpucores" = "12" | .spec.containers[0].resources.limits."nvidia.com/gpucores" = "12"'
deny_case zero-count "${valid_fixture}" \
  '.spec.containers[0].resources.requests."nvidia.com/gpu" = "0" | .spec.containers[0].resources.limits."nvidia.com/gpu" = "0"'
deny_case multiple-devices "${valid_fixture}" \
  '.spec.containers[0].resources.requests."nvidia.com/gpu" = "2" | .spec.containers[0].resources.limits."nvidia.com/gpu" = "2"'
deny_case missing-core "${valid_fixture}" \
  'del(.spec.containers[0].resources.requests."nvidia.com/gpucores") | del(.spec.containers[0].resources.limits."nvidia.com/gpucores")'
deny_case memory-percentage "${valid_fixture}" \
  '.spec.containers[0].resources.requests."nvidia.com/gpumem-percentage" = "50" | .spec.containers[0].resources.limits."nvidia.com/gpumem-percentage" = "50"'
deny_case hami-priority "${valid_fixture}" \
  '.spec.containers[0].resources.requests."nvidia.com/priority" = "1" | .spec.containers[0].resources.limits."nvidia.com/priority" = "1"'
deny_case custom-scheduler "${valid_fixture}" '.spec.schedulerName = "other-scheduler"'
deny_case node-selector "${valid_fixture}" \
  '.spec.nodeSelector = {"kubernetes.io/hostname":"gpu-worker"}'
deny_case node-affinity "${valid_fixture}" \
  '.spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms = [{"matchExpressions":[{"key":"kubernetes.io/hostname","operator":"In","values":["gpu-worker"]}]}]'
deny_case runtime-class "${valid_fixture}" '.spec.runtimeClassName = "nvidia"'
deny_case host-network "${valid_fixture}" '.spec.hostNetwork = true'
deny_case host-pid "${valid_fixture}" '.spec.hostPID = true'
deny_case host-ipc "${valid_fixture}" '.spec.hostIPC = true'
deny_case host-path "${valid_fixture}" \
  '.spec.volumes = [{"name":"host","hostPath":{"path":"/dev"}}]'
deny_case privileged "${valid_fixture}" \
  '.spec.containers[0].securityContext.privileged = true'
deny_case privilege-escalation "${valid_fixture}" \
  '.spec.containers[0].securityContext.allowPrivilegeEscalation = true'
deny_case added-capability "${valid_fixture}" \
  '.spec.containers[0].securityContext.capabilities.add = ["SYS_ADMIN"]'
deny_case volume-device "${valid_fixture}" \
  '.spec.volumes = [{"name":"block","persistentVolumeClaim":{"claimName":"block-device"}}] | .spec.containers[0].volumeDevices = [{"name":"block","devicePath":"/dev/xvda"}]'
deny_case cuda-disable-empty "${valid_fixture}" \
  '.spec.containers[0].env = [{"name":"CUDA_DISABLE_CONTROL","value":""}]'
deny_case cuda-disable-false "${valid_fixture}" \
  '.spec.containers[0].env = [{"name":"CUDA_DISABLE_CONTROL","value":"false"}]'
deny_case cuda-disable-valuefrom "${valid_fixture}" \
  '.spec.containers[0].env = [{"name":"CUDA_DISABLE_CONTROL","valueFrom":{"fieldRef":{"fieldPath":"status.podIP"}}}]'
deny_case cuda-disable-envfrom "${valid_fixture}" \
  '.spec.containers[0].envFrom = [{"configMapRef":{"name":"cuda-settings"}}]'
deny_case init-container-gpu "${valid_fixture}" \
  '.spec.initContainers = [{"name":"init","image":"registry.k8s.io/pause:3.10","resources":.spec.containers[0].resources}]'
allow_scheduled_update_case
deny_ephemeral_attach_case
deny_case webhook-opt-out "${valid_fixture}" \
  '.metadata.labels."hami.io/webhook" = "ignore"'

printf 'TOTAL FAILURES: %d\n' "${failures}"
exit "${failures}"
