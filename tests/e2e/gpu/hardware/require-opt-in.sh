#!/usr/bin/env bash

# Real-GPU suites mutate scheduling, quota, plugin processes, or PCI bindings.
# Sourcing this guard is mandatory even when a kubeconfig is already present.
if [[ "${KUBE_DC_GPU_HARDWARE_TESTS:-}" != "acknowledge-hardware-mutation" ]]; then
  echo "GPU hardware tests are opt-in; set KUBE_DC_GPU_HARDWARE_TESTS=acknowledge-hardware-mutation" >&2
  exit 64
fi

if [[ -z "${GPU_HARDWARE_TARGET:-}" ]]; then
  echo "GPU_HARDWARE_TARGET must name the approved lab cluster" >&2
  exit 64
fi
