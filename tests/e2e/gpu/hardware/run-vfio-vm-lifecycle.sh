#!/usr/bin/env bash

# Destructive only to a new disposable namespace. Proves whole-device KubeVirt
# allocation, stop/start release, two-device exclusivity, queue recovery, and
# live-migration denial on an explicitly approved VFIO lab node.

set -euo pipefail

source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/require-opt-in.sh"

for command in kubectl virtctl yq grep seq date awk; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "required command is missing: ${command}" >&2
    exit 2
  fi
done

: "${KUBECONFIG:?set KUBECONFIG to the approved lab-cluster kubeconfig}"

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
kubectl_args=(--kubeconfig "${KUBECONFIG}")
virtctl_args=(--kubeconfig "${KUBECONFIG}")
run_id="vfio-vm-$(date +%s)-${RANDOM}"
namespace=${GPU_VFIO_NAMESPACE:-${run_id}}
lock_namespace=${GPU_VFIO_LOCK_NAMESPACE:-gpu-operator}
lock_name=${GPU_VFIO_LOCK_NAME:-kube-dc-gpu-hardware-suite-lock}
profile_id=${GPU_VFIO_PROFILE_ID:-nvidia-v100-passthrough}
resource_name=${GPU_VFIO_RESOURCE_NAME:-nvidia.com/GV100GL_TESLA_V100_PCIE_32GB}
expected_devices=${GPU_VFIO_EXPECTED_DEVICES:-2}
timeout=${GPU_VFIO_TIMEOUT:-300s}
lock_acquired=false
namespace_created=false

if [[ ! "${namespace}" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]] || (( ${#namespace} > 63 )); then
  echo "GPU_VFIO_NAMESPACE must be a valid DNS label of at most 63 characters" >&2
  exit 2
fi

cleanup() {
  local namespace_deleted=true
  local holder

  if [[ "${namespace_created}" == true ]]; then
    namespace_deleted=false
    for vm in v100-third v100-second v100-first; do
      virtctl "${virtctl_args[@]}" stop "${vm}" -n "${namespace}" >/dev/null 2>&1 || true
    done
    kubectl "${kubectl_args[@]}" delete namespace "${namespace}" \
      --ignore-not-found --wait=false >/dev/null 2>&1 || true

    for attempt in $(seq 1 180); do
      if ! kubectl "${kubectl_args[@]}" get namespace "${namespace}" >/dev/null 2>&1; then
        namespace_deleted=true
        break
      fi
      sleep 1
    done
  fi

  if [[ "${lock_acquired}" == true ]]; then
    holder=$(kubectl "${kubectl_args[@]}" -n "${lock_namespace}" get configmap "${lock_name}" \
      -o jsonpath='{.data.holder}' 2>/dev/null || true)
    if [[ "${holder}" == "${run_id}" && "${namespace_deleted}" == true ]]; then
      kubectl "${kubectl_args[@]}" -n "${lock_namespace}" delete configmap "${lock_name}" \
        --ignore-not-found >/dev/null 2>&1 || true
    elif [[ "${holder}" == "${run_id}" ]]; then
      echo "cleanup did not finish; retaining hardware lock ${lock_namespace}/${lock_name} for holder ${run_id}" >&2
    fi
  fi
}
trap cleanup EXIT

if kubectl "${kubectl_args[@]}" get namespace "${namespace}" >/dev/null 2>&1; then
  echo "refusing to reuse existing namespace ${namespace}" >&2
  exit 2
fi

if ! kubectl "${kubectl_args[@]}" -n "${lock_namespace}" create configmap "${lock_name}" \
  --from-literal="holder=${run_id}" --from-literal="startedAt=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >/dev/null; then
  echo "GPU hardware suite lock is already held:" >&2
  kubectl "${kubectl_args[@]}" -n "${lock_namespace}" get configmap "${lock_name}" -o yaml >&2 || true
  exit 2
fi
lock_acquired=true

node_json=$(kubectl "${kubectl_args[@]}" get nodes -o json)
expected_nodes=$(printf '%s' "${node_json}" | yq e \
  '[.items[] | select(.metadata.labels."kube-dc.com/gpu.expected-workload-mode" == "vm-passthrough")] | length' -)
wrong_mode=$(printf '%s' "${node_json}" | yq e \
  '[.items[] | select(.metadata.labels."kube-dc.com/gpu.expected-workload-mode" == "vm-passthrough") | select(.metadata.labels."kube-dc.com/gpu.workload-mode" != "vm-passthrough")] | length' -)
allocatable=$(RESOURCE_NAME="${resource_name}" printf '%s' "${node_json}" | \
  RESOURCE_NAME="${resource_name}" yq e '.items[].status.allocatable[strenv(RESOURCE_NAME)] // "0"' - | \
  awk '{ total += $1 } END { print total + 0 }')
active_holders=$(RESOURCE_NAME="${resource_name}" kubectl "${kubectl_args[@]}" get pods -A -o json | \
  RESOURCE_NAME="${resource_name}" yq e \
    '[.items[] | select(.status.phase != "Succeeded" and .status.phase != "Failed") | select([.spec.containers[]? | select(.resources.requests[strenv(RESOURCE_NAME)] != null)] | length > 0)] | length' -)

allowlisted=$(RESOURCE_NAME="${resource_name}" kubectl "${kubectl_args[@]}" get kubevirt -A -o json | \
  RESOURCE_NAME="${resource_name}" yq e \
    '[.items[].spec.configuration.permittedHostDevices.pciHostDevices[]? | select(.resourceName == strenv(RESOURCE_NAME) and .pciVendorSelector == "10DE:1DB6" and .externalResourceProvider == true)] | length' -)

if [[ "${expected_nodes}" != 1 || "${wrong_mode}" != 0 || "${allocatable}" != "${expected_devices}" || \
      "${active_holders}" != 0 || "${allowlisted}" != 1 ]]; then
  echo "preflight failed: expectedNodes=${expected_nodes} wrongMode=${wrong_mode} allocatable=${allocatable} expectedDevices=${expected_devices} activeHolders=${active_holders} allowlisted=${allowlisted}" >&2
  exit 1
fi
printf 'PASS preflight: mode=vm-passthrough resource=%s allocatable=%s activeHolders=0\n' \
  "${resource_name}" "${allocatable}"

kubectl "${kubectl_args[@]}" create namespace "${namespace}" >/dev/null
namespace_created=true
kubectl "${kubectl_args[@]}" label namespace "${namespace}" kube-dc.com/project= >/dev/null

create_vm() {
  local name=$1
  VM_NAME="${name}" GPU_VFIO_NAMESPACE="${namespace}" GPU_PROFILE_ID="${profile_id}" \
  GPU_RESOURCE_NAME="${resource_name}" yq ea '
    select(.kind == "VirtualMachine") |
    .metadata.name = strenv(VM_NAME) |
    .metadata.namespace = strenv(GPU_VFIO_NAMESPACE) |
    .metadata.labels."app.kubernetes.io/name" = strenv(VM_NAME) |
    .metadata.labels."kube-dc.com/gpu-profile" = strenv(GPU_PROFILE_ID) |
    .spec.template.metadata.labels."app.kubernetes.io/name" = strenv(VM_NAME) |
    .spec.template.metadata.labels."kube-dc.com/gpu-profile" = strenv(GPU_PROFILE_ID) |
    .spec.template.spec.domain.devices.gpus[0].deviceName = strenv(GPU_RESOURCE_NAME)
  ' "${script_dir}/v100-passthrough-vm.yaml" | kubectl "${kubectl_args[@]}" create -f - >/dev/null
}

wait_ready() {
  kubectl "${kubectl_args[@]}" -n "${namespace}" wait --for=condition=Ready \
    "vmi/$1" --timeout="${timeout}" >/dev/null
}

wait_vmi_deleted() {
  local name=$1
  for attempt in $(seq 1 120); do
    if ! kubectl "${kubectl_args[@]}" -n "${namespace}" get vmi "${name}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "VMI ${name} was not deleted" >&2
  return 1
}

launcher_for() {
  local name=$1
  kubectl "${kubectl_args[@]}" -n "${namespace}" get pods \
    -l "kubevirt.io=virt-launcher,vm.kubevirt.io/name=${name}" \
    -o jsonpath='{.items[0].metadata.name}'
}

assert_running_gpu() {
  local name=$1
  local pod
  local request
  local owner
  local live_migratable
  local migration_reason
  local domain_name
  local domain_xml

  pod=$(launcher_for "${name}")
  request=$(RESOURCE_NAME="${resource_name}" kubectl "${kubectl_args[@]}" -n "${namespace}" get pod "${pod}" -o json | \
    RESOURCE_NAME="${resource_name}" yq e \
      '.spec.containers[] | .resources.requests[strenv(RESOURCE_NAME)] // "0"' - | \
    awk '{ total += $1 } END { print total + 0 }')
  owner=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get pod "${pod}" \
    -o jsonpath='{.metadata.ownerReferences[?(@.controller==true)].kind}')
  live_migratable=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get vmi "${name}" \
    -o jsonpath='{.status.conditions[?(@.type=="LiveMigratable")].status}')
  migration_reason=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get vmi "${name}" \
    -o jsonpath='{.status.conditions[?(@.type=="LiveMigratable")].reason}')
  domain_xml=
  for attempt in $(seq 1 30); do
    domain_name=$(kubectl "${kubectl_args[@]}" -n "${namespace}" exec "${pod}" -c compute -- \
      virsh list --name 2>/dev/null | awk 'NF { print; exit }' || true)
    if [[ -n "${domain_name}" ]]; then
      if domain_xml=$(kubectl "${kubectl_args[@]}" -n "${namespace}" exec "${pod}" -c compute -- \
        virsh dumpxml "${domain_name}" 2>/dev/null); then
        break
      fi
    fi
    sleep 1
  done

  [[ "${request}" == 1 ]] || { echo "${name}: launcher request=${request}, expected 1" >&2; return 1; }
  [[ "${owner}" == VirtualMachineInstance ]] || { echo "${name}: controller owner=${owner}" >&2; return 1; }
  [[ -n "${domain_xml}" ]] || { echo "${name}: libvirt domain was not observable" >&2; return 1; }
  [[ "${live_migratable}" == False && "${migration_reason}" == HostDeviceNotLiveMigratable ]] || {
    echo "${name}: unexpected migration condition ${live_migratable}/${migration_reason}" >&2; return 1;
  }
  grep -Fq "<driver name='vfio'/>" <<<"${domain_xml}" || {
    echo "${name}: libvirt domain has no vfio hostdev driver" >&2; return 1;
  }
  grep -Fq "<alias name='ua-gpu-gpu0'/>" <<<"${domain_xml}" || {
    echo "${name}: libvirt domain has no catalogued GPU alias" >&2; return 1;
  }
}

create_vm v100-first
virtctl "${virtctl_args[@]}" start v100-first -n "${namespace}" >/dev/null
wait_ready v100-first
first_uid=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get vmi v100-first -o jsonpath='{.metadata.uid}')
assert_running_gpu v100-first
printf 'PASS attach: VMI Ready with VFIO hostdev and non-migratable condition\n'

virtctl "${virtctl_args[@]}" stop v100-first -n "${namespace}" >/dev/null
wait_vmi_deleted v100-first
virtctl "${virtctl_args[@]}" start v100-first -n "${namespace}" >/dev/null
wait_ready v100-first
second_uid=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get vmi v100-first -o jsonpath='{.metadata.uid}')
[[ "${first_uid}" != "${second_uid}" ]] || { echo "stop/start reused VMI UID ${first_uid}" >&2; exit 1; }
assert_running_gpu v100-first
printf 'PASS lifecycle: stop released device; start created Ready VMI uid=%s\n' "${second_uid}"

create_vm v100-second
create_vm v100-third
virtctl "${virtctl_args[@]}" start v100-second -n "${namespace}" >/dev/null
wait_ready v100-second
assert_running_gpu v100-second
virtctl "${virtctl_args[@]}" start v100-third -n "${namespace}" >/dev/null

queued=false
queue_message=
for attempt in $(seq 1 60); do
  third_pod=$(launcher_for v100-third 2>/dev/null || true)
  if [[ -n "${third_pod}" ]]; then
    queue_message=$(kubectl "${kubectl_args[@]}" -n "${namespace}" get pod "${third_pod}" -o json | \
      yq e -r '.status.conditions[]? | select(.type == "PodScheduled" and .status == "False") | .message' -)
    if grep -Fq "Insufficient ${resource_name}" <<<"${queue_message}"; then
      queued=true
      break
    fi
  fi
  sleep 1
done
[[ "${queued}" == true ]] || { echo "third VM did not queue for ${resource_name}: ${queue_message}" >&2; exit 1; }
printf 'PASS exclusivity: two VMs consumed two devices; third request queued\n'

if migration_output=$(virtctl "${virtctl_args[@]}" migrate v100-first -n "${namespace}" 2>&1); then
  echo "GPU VMI migration was unexpectedly admitted" >&2
  exit 1
elif ! grep -Fq 'HostDeviceNotLiveMigratable' <<<"${migration_output}"; then
  echo "migration failed for an unexpected reason: ${migration_output}" >&2
  exit 1
fi
printf 'PASS migration: live migration denied with HostDeviceNotLiveMigratable\n'

virtctl "${virtctl_args[@]}" stop v100-first -n "${namespace}" >/dev/null
wait_vmi_deleted v100-first
wait_ready v100-third
assert_running_gpu v100-third
printf 'PASS queue: released device assigned to the waiting third VM\n'

printf 'PASS cleanup: namespace=%s will be removed\n' "${namespace}"
