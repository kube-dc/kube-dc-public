#!/usr/bin/env bash
# press-any-key.sh — get Windows Setup past the "Press any key to boot from CD or
# DVD" prompt on an unattended KubeVirt bake, and VERIFY that it worked.
#
# WHY THIS EXISTS: Microsoft's UEFI installer media waits for a physical keypress
# before booting. Nobody presses it in a VM, the prompt times out, and with a blank
# target disk the firmware lands on "No bootable option or device was found" — while
# Kubernetes cheerfully reports the VMI Running and Ready. That is how the first bake
# attempt (2026-08-07) burned an hour.
#
# THIS IS A FALLBACK, NOT THE PREFERRED FIX. I originally justified it by claiming
# efisys_noprompt.bin was absent from our ISO revision. That was wrong: the ISO has
# both efi/microsoft/boot/efisys_noprompt.bin and cdboot_noprompt.efi. Remastering the
# installer ISO with the no-prompt boot image removes the prompt — and this whole race
# — outright; see the recipe in build-vm.yaml. Keep this script for un-remastered
# media, but prefer media that never asks.
#
# WHY IT VERIFIES INSTEAD OF JUST TAPPING: the prompt lasts about five seconds, so a
# single tap is a race, and losing it is expensive and SILENT. Tap late and the next
# thing the firmware offers is "Press any key to enter the Boot Manager Menu" — the
# key then parks the VM in the edk2 menu, where it idles for hours looking perfectly
# healthy. On 2026-08-08 that cost four hours across two attempts, and the driver had
# logged "1 keypresses delivered" both times.
#
# So: tap, then look at the screen and say whether Setup actually started. If it did
# not, restart the VM and try again, up to ATTEMPTS times. Screen state is the only
# honest signal here — VMI phase, Ready and AgentConnected are identical whether
# Setup is installing or the firmware is sitting in a menu.
#
# Usage: ./press-any-key.sh <namespace> <vm-name> [seconds-per-attempt] [attempts]
set -uo pipefail
NS="${1:?usage: press-any-key.sh <namespace> <vm> [seconds] [attempts]}"
VM="${2:?usage: press-any-key.sh <namespace> <vm> [seconds] [attempts]}"
WINDOW="${3:-180}"        # how long to keep tapping within one attempt
ATTEMPTS="${4:-4}"        # VM restarts before giving up

HERE="$(cd "$(dirname "$0")" && pwd)"
# vncdotool lives in a venv (PEP 668 blocks `pip install --user` on this bastion).
if [ -z "${VNCDO:-}" ]; then
  for c in "$HERE/.venv/bin/vncdo" "$HOME/.venvs/vncdotool/bin/vncdo" "$(command -v vncdo || true)"; do
    [ -n "$c" ] && [ -x "$c" ] && VNCDO="$c" && break
  done
fi
if [ -z "${VNCDO:-}" ] || [ ! -x "$VNCDO" ]; then
  echo "vncdo not found. Create it once:"
  echo "  python3 -m venv $HERE/.venv && $HERE/.venv/bin/pip install vncdotool pillow"
  echo "(or export VNCDO=/path/to/vncdo)"
  exit 1
fi
PY="$HERE/.venv/bin/python"
[ -x "$PY" ] || PY="$(command -v python3)"
CLASSIFY="$HERE/classify-screen.py"
SHOT=$(mktemp -t kubedc-screen-XXXXXX.png)
trap 'rm -f "$SHOT"' EXIT

screen_state() {
  kubectl get --raw \
    "/apis/subresources.kubevirt.io/v1/namespaces/$NS/virtualmachineinstances/$VM/vnc/screenshot?moveCursor=false" \
    > "$SHOT" 2>/dev/null || { echo unknown; return; }
  "$PY" "$CLASSIFY" "$SHOT" 2>/dev/null || echo unknown
}

wait_running() {
  for _ in $(seq 1 90); do
    [ "$(kubectl -n "$NS" get vmi "$VM" -o jsonpath='{.status.phase}' 2>/dev/null)" = "Running" ] && return 0
    sleep 5
  done
  return 1
}

attempt() {
  local n="$1" port proxy end sent state
  echo "[keys] attempt $n/$ATTEMPTS"
  wait_running || { echo "[keys] VMI never reached Running"; return 1; }

  port=$(( 15900 + RANDOM % 400 ))
  "$(command -v virtctl)" vnc "$VM" -n "$NS" --proxy-only --port "$port" >/dev/null 2>&1 &
  proxy=$!
  sleep 4

  # Tap on a fixed cadence for the whole window. Do NOT try to be clever about aiming
  # a single key: the prompt lasts ~5s and opens at an unpredictable point after the
  # VMI reports Running, and four consecutive single-tap attempts on 2026-08-08 all
  # landed early and all missed. This cadence is the one that actually got four bakes
  # into Setup. The screen check below is what makes it safe — it stops the moment
  # Setup appears, so we never hammer keys into a running installer, and it bails out
  # the moment a tap lands in the Boot Manager instead.
  end=$(( $(date +%s) + WINDOW )); sent=0; i=0
  while [ "$(date +%s)" -lt "$end" ]; do
    "$VNCDO" -s "127.0.0.1::$port" key enter >/dev/null 2>&1 && sent=$((sent+1))
    i=$((i+1))
    if [ $(( i % 3 )) -eq 0 ]; then
      state=$(screen_state)
      case "$state" in
        setup)
          kill $proxy 2>/dev/null || true
          echo "[keys]   Setup is up after $sent taps — done"; return 0 ;;
        firmware)
          kill $proxy 2>/dev/null || true
          echo "[keys]   parked in the edk2 menu — restarting is cheaper than escaping"
          return 1 ;;
      esac
    fi
    sleep 3
  done
  kill $proxy 2>/dev/null || true
  echo "[keys]   $sent taps delivered, screen is '$(screen_state)' — treating as a miss"
  return 1
}

for i in $(seq 1 "$ATTEMPTS"); do
  if attempt "$i"; then
    echo "[keys] SUCCESS on attempt $i"
    exit 0
  fi
  if [ "$i" -lt "$ATTEMPTS" ]; then
    echo "[keys] restarting $NS/$VM and trying again"
    "$(command -v virtctl)" restart "$VM" -n "$NS" >/dev/null 2>&1
    sleep 20
  fi
done

echo "[keys] FAILED after $ATTEMPTS attempts — Setup never started"
exit 2
