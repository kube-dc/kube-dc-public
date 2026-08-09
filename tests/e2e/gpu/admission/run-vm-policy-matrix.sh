#!/usr/bin/env bash

# Exercises installed kube-dc KubeVirt GPU policies with server-side dry-run.
# No VM or VMI is persisted and no passthrough-capable node is required.

set -uo pipefail

for command in kubectl yq grep; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "required command is missing: ${command}" >&2
    exit 2
  fi
done

: "${KUBECONFIG:?set KUBECONFIG to the test-cluster kubeconfig}"
: "${GPU_ADMISSION_NAMESPACE:?set GPU_ADMISSION_NAMESPACE to a kube-dc project namespace}"

if [[ ! "${GPU_ADMISSION_NAMESPACE}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]]; then
  echo "GPU_ADMISSION_NAMESPACE is not a valid DNS label" >&2
  exit 2
fi

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
vm_fixture="${script_dir}/valid-gpu-vm.yaml"
vmi_fixture="${script_dir}/valid-gpu-vmi.yaml"
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
  local output

  output=$(render_fixture "${file}" "${expression}" | \
    kubectl "${kubectl_args[@]}" create --dry-run=server -o name -f - 2>&1)
  if [[ $? -eq 0 ]]; then
    printf 'PASS allow: %s\n' "${label}"
  else
    printf 'FAIL allow: %s :: %s\n' "${label}" "${output}" >&2
    failures=$((failures + 1))
  fi
}

deny_case() {
  local label=$1
  local file=$2
  local expression=$3
  local expected=$4
  local output
  local status

  output=$(render_fixture "${file}" "${expression}" | \
    kubectl "${kubectl_args[@]}" create --dry-run=server -o name -f - 2>&1)
  status=$?
  if [[ ${status} -ne 0 ]] && printf '%s' "${output}" | grep -Fq "${expected}"; then
    printf 'PASS deny:  %s\n' "${label}"
  else
    printf 'FAIL deny:  %s :: status=%s output=%s\n' \
      "${label}" "${status}" "${output}" >&2
    failures=$((failures + 1))
  fi
}

profile_required='GPU VMs require an enabled kube-dc GPU profile label.'
template_required='The GPU profile label is unknown, missing from the VMI template, or has no GPU device.'
vmi_profile_required='GPU VMIs require an enabled kube-dc GPU profile label.'
vmi_label_shape='The GPU profile label is unknown or the VMI has no GPU device.'
device_mismatch='Every GPU device must match the selected kube-dc GPU profile.'
host_device_path='Project virtual machines must use domain.devices.gpus; generic hostDevices are not allowed.'
migration='GPU virtual machines must explicitly use evictionStrategy: None.'
node_name='GPU virtual machines cannot select an internal node name.'
vm_node_name="${node_name}"
vmi_node_name="${node_name}"

allow_case vm-valid "${vm_fixture}" '.'
allow_case vm-plain "${vm_fixture}" \
  'del(.metadata.labels) | del(.spec.template.metadata.labels) | del(.spec.template.spec.domain.devices.gpus)'
allow_case vm-product-selector "${vm_fixture}" \
  '.spec.template.spec.nodeSelector = {"kube-dc.com/gpu.workload-mode":"vm-passthrough"}'
allow_case vm-product-affinity "${vm_fixture}" \
  '.spec.template.spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms = [{"matchExpressions":[{"key":"kube-dc.com/gpu.workload-mode","operator":"In","values":["vm-passthrough"]}]}]'
allow_case vm-pod-anti-affinity "${vm_fixture}" \
  '.spec.template.spec.affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution = [{"weight":100,"podAffinityTerm":{"labelSelector":{"matchLabels":{"app":"gpu"}},"topologyKey":"kubernetes.io/hostname"}}]'
allow_case vmi-valid "${vmi_fixture}" '.'
allow_case vmi-plain "${vmi_fixture}" \
  'del(.metadata.labels) | del(.spec.domain.devices.gpus)'
allow_case vmi-product-selector "${vmi_fixture}" \
  '.spec.nodeSelector = {"kube-dc.com/gpu.workload-mode":"vm-passthrough"}'
allow_case vmi-product-affinity "${vmi_fixture}" \
  '.spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms = [{"matchExpressions":[{"key":"kube-dc.com/gpu.workload-mode","operator":"In","values":["vm-passthrough"]}]}]'
allow_case vmi-pod-anti-affinity "${vmi_fixture}" \
  '.spec.affinity.podAntiAffinity.preferredDuringSchedulingIgnoredDuringExecution = [{"weight":100,"podAffinityTerm":{"labelSelector":{"matchLabels":{"app":"gpu"}},"topologyKey":"kubernetes.io/hostname"}}]'
allow_case vmi-explicit-none-eviction-strategy "${vmi_fixture}" \
  '.spec.evictionStrategy = "None"'

deny_case vm-missing-profile "${vm_fixture}" 'del(.metadata.labels)' "${profile_required}"
deny_case vm-unknown-profile "${vm_fixture}" \
  '.metadata.labels."kube-dc.com/gpu-profile" = "unknown"' "${profile_required}"
deny_case vm-missing-template-profile "${vm_fixture}" \
  'del(.spec.template.metadata.labels)' "${template_required}"
deny_case vm-mismatched-template-profile "${vm_fixture}" \
  '.spec.template.metadata.labels."kube-dc.com/gpu-profile" = "unknown"' "${template_required}"
deny_case vm-label-without-gpu "${vm_fixture}" \
  'del(.spec.template.spec.domain.devices.gpus)' "${template_required}"
deny_case vm-unknown-device "${vm_fixture}" \
  '.spec.template.spec.domain.devices.gpus[0].deviceName = "nvidia.com/unknown"' "${device_mismatch}"
deny_case vm-unentitled-mdev "${vm_fixture}" \
  '.spec.template.spec.domain.devices.gpus[0].deviceName = "nvidia.com/GRID_V100-8C"' "${device_mismatch}"
deny_case vm-mixed-devices "${vm_fixture}" \
  '.spec.template.spec.domain.devices.gpus += [{"name":"gpu1","deviceName":"nvidia.com/unknown"}]' "${device_mismatch}"
deny_case vm-host-device-alias "${vm_fixture}" \
  '.spec.template.spec.domain.devices.hostDevices = [{"name":"gpu0","deviceName":"nvidia.com/GV100GL_TESLA_V100_PCIE_32GB"}]' "${host_device_path}"
deny_case vm-uncataloged-host-device-without-profile "${vm_fixture}" \
  'del(.metadata.labels) | del(.spec.template.metadata.labels) | del(.spec.template.spec.domain.devices.gpus) | .spec.template.spec.domain.devices.hostDevices = [{"name":"device0","deviceName":"example.com/future-permitted-device"}]' "${host_device_path}"
deny_case vm-missing-eviction-strategy "${vm_fixture}" \
  'del(.spec.template.spec.evictionStrategy)' "${migration}"
deny_case vm-external-eviction-strategy "${vm_fixture}" \
  '.spec.template.spec.evictionStrategy = "External"' "${migration}"
deny_case vm-live-migrate "${vm_fixture}" \
  '.spec.template.spec.evictionStrategy = "LiveMigrate"' "${migration}"
deny_case vm-live-migrate-if-possible "${vm_fixture}" \
  '.spec.template.spec.evictionStrategy = "LiveMigrateIfPossible"' "${migration}"
deny_case vm-hostname-selector "${vm_fixture}" \
  '.spec.template.spec.nodeSelector = {"kubernetes.io/hostname":"gpu-worker"}' "${vm_node_name}"
deny_case vm-beta-hostname-selector "${vm_fixture}" \
  '.spec.template.spec.nodeSelector = {"beta.kubernetes.io/hostname":"gpu-worker"}' "${vm_node_name}"
deny_case vm-required-node-affinity "${vm_fixture}" \
  '.spec.template.spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms = [{"matchExpressions":[{"key":"kubernetes.io/hostname","operator":"In","values":["gpu-worker"]}]}]' "${vm_node_name}"
deny_case vm-node-match-field "${vm_fixture}" \
  '.spec.template.spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms = [{"matchFields":[{"key":"metadata.name","operator":"In","values":["gpu-worker"]}]}]' "${vm_node_name}"
deny_case vm-preferred-node-affinity "${vm_fixture}" \
  '.spec.template.spec.affinity.nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution = [{"weight":100,"preference":{"matchExpressions":[{"key":"kubernetes.io/hostname","operator":"In","values":["gpu-worker"]}]}}]' "${vm_node_name}"

deny_case vmi-missing-profile "${vmi_fixture}" 'del(.metadata.labels)' "${vmi_profile_required}"
deny_case vmi-unknown-profile "${vmi_fixture}" \
  '.metadata.labels."kube-dc.com/gpu-profile" = "unknown"' "${vmi_profile_required}"
deny_case vmi-label-without-gpu "${vmi_fixture}" \
  'del(.spec.domain.devices.gpus)' "${vmi_label_shape}"
deny_case vmi-unknown-device "${vmi_fixture}" \
  '.spec.domain.devices.gpus[0].deviceName = "nvidia.com/unknown"' "${device_mismatch}"
deny_case vmi-host-device-alias "${vmi_fixture}" \
  '.spec.domain.devices.hostDevices = [{"name":"gpu0","deviceName":"nvidia.com/GV100GL_TESLA_V100_PCIE_32GB"}]' "${host_device_path}"
deny_case vmi-uncataloged-host-device-without-profile "${vmi_fixture}" \
  'del(.metadata.labels) | del(.spec.domain.devices.gpus) | .spec.domain.devices.hostDevices = [{"name":"device0","deviceName":"example.com/future-permitted-device"}]' "${host_device_path}"
deny_case vmi-external-eviction-strategy "${vmi_fixture}" \
  '.spec.evictionStrategy = "External"' "${migration}"
deny_case vmi-live-migrate "${vmi_fixture}" \
  '.spec.evictionStrategy = "LiveMigrate"' "${migration}"
deny_case vmi-live-migrate-if-possible "${vmi_fixture}" \
  '.spec.evictionStrategy = "LiveMigrateIfPossible"' "${migration}"
deny_case vmi-hostname-selector "${vmi_fixture}" \
  '.spec.nodeSelector = {"kubernetes.io/hostname":"gpu-worker"}' "${vmi_node_name}"
deny_case vmi-beta-hostname-selector "${vmi_fixture}" \
  '.spec.nodeSelector = {"beta.kubernetes.io/hostname":"gpu-worker"}' "${vmi_node_name}"
deny_case vmi-required-node-affinity "${vmi_fixture}" \
  '.spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms = [{"matchExpressions":[{"key":"kubernetes.io/hostname","operator":"In","values":["gpu-worker"]}]}]' "${vmi_node_name}"
deny_case vmi-node-match-field "${vmi_fixture}" \
  '.spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms = [{"matchFields":[{"key":"metadata.name","operator":"In","values":["gpu-worker"]}]}]' "${vmi_node_name}"
deny_case vmi-preferred-node-affinity "${vmi_fixture}" \
  '.spec.affinity.nodeAffinity.preferredDuringSchedulingIgnoredDuringExecution = [{"weight":100,"preference":{"matchExpressions":[{"key":"kubernetes.io/hostname","operator":"In","values":["gpu-worker"]}]}}]' "${vmi_node_name}"

printf 'TOTAL FAILURES: %d\n' "${failures}"
exit "${failures}"
