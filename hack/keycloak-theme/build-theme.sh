#!/usr/bin/env bash
# Compatibility entry point. Theme source and artifact generation belong to kube-dc/ui.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ui_root="${KUBE_DC_UI_DIR:-$root/../kube-dc-ui}"
[[ -f "$ui_root/keycloak/scripts/build.py" ]] || {
  echo 'Clone https://github.com/kube-dc/ui as ../kube-dc-ui or set KUBE_DC_UI_DIR.' >&2
  exit 1
}
exec python3 "$ui_root/keycloak/scripts/build.py" "$@"
