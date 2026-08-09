#!/usr/bin/env bash
set -euo pipefail

: "${MOCK_KUBECTL_LOG:?}"
: "${MOCK_KUBECTL_MODE:?}"
: "${MOCK_KUBECTL_HOLDER:?}"
printf '%s\n' "$*" >>"${MOCK_KUBECTL_LOG}"
args=" $* "

if [[ "${args}" == *" get namespace "* && "${args}" == *"jsonpath="* ]]; then
  printf 'enabled'
elif [[ "${args}" == *" get namespace "* && "${args}" == *" -o json "* ]]; then
  printf '{"metadata":{"labels":{"kube-dc.com/project":""}}}'
elif [[ "${args}" == *" get network-attachment-definition.k8s.cni.cncf.io default "* ]]; then
  :
elif [[ "${args}" == *" get deviceclass "* ]]; then
  :
elif [[ "${args}" == *" get daemonset hami-dra-driver-kubelet-plugin "* ]]; then
  printf '{"status":{"desiredNumberScheduled":1,"currentNumberScheduled":1,"numberReady":1}}'
elif [[ "${args}" == *" get resourceslices "* ]]; then
  printf '{"items":[{"spec":{"driver":"hami-core-gpu.project-hami.io","devices":[{"allowMultipleAllocations":true}]}}]}'
elif [[ "${args}" == *" create configmap "* ]]; then
  if [[ "${MOCK_KUBECTL_MODE}" == "lock-held" ]]; then
    exit 1
  fi
  for arg in "$@"; do
    if [[ "${arg}" == --from-literal=holder=* ]]; then
      printf '%s' "${arg#--from-literal=holder=}" >"${MOCK_KUBECTL_HOLDER}"
    fi
  done
elif [[ "${args}" == *" get resourceclaims "* && "${args}" == *" -o json "* ]]; then
  printf '{"items":[]}'
elif [[ "${args}" == *" get deployment dra-dual-home-test "* && "${MOCK_KUBECTL_MODE}" == "reuse" ]]; then
  :
elif [[ "${args}" == *" get configmap "* && "${args}" == *"jsonpath="* ]]; then
  [[ -f "${MOCK_KUBECTL_HOLDER}" ]] && cat "${MOCK_KUBECTL_HOLDER}"
elif [[ "${args}" == *" delete configmap "* ]]; then
  :
else
  echo "unexpected mock kubectl invocation: $*" >&2
  exit 99
fi
