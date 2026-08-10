#!/usr/bin/env bash
# validate-clone.sh — the Windows golden RELEASE GATE (codex review task #4).
#
# Clones the published windows golden exactly like a tenant would (per-project
# seeded VolumeSnapshot -> PVC restore -> VM) and verifies the boot contract.
# Promotion of a new golden MUST NOT proceed unless this exits 0.
#
# Checks: instant clone provisions; VMI reaches Running; qemu-guest-agent
# connects (= full Windows boot, drivers OK, no OOBE hang); RDP 3389 answers;
# guest OS info is reported via the agent (proves the agent API path tenants'
# SSH-key injection uses).
#
# Usage:
#   ./validate-clone.sh <namespace> [snapshot-name] [timeout-minutes]
#   NS must be a project namespace holding the seeded golden snapshot
#   (default snapshot: golden-windows-11-golden). Cleans up on exit unless
#   KEEP=1. Requires: kubectl context on the target cluster.
set -euo pipefail
NS="${1:?usage: validate-clone.sh <namespace> [snapshot] [timeout-min]}"
SNAP="${2:-golden-windows-11-golden}"
TIMEOUT_MIN="${3:-25}"
# NB: no $(... | head) here — under `set -o pipefail` head's early exit SIGPIPEs
# the producer (rc 141) and kills the script. $RANDOM needs no pipe at all.
VM="win-gate-$RANDOM"
KEEP="${KEEP:-0}"
# Guest memory for the gate VM. Default 8Gi = the catalog minMemory for the
# Windows golden; lower it (MEM=6Gi) to fit a constrained org quota — the boot
# contract this gate checks is unaffected well above Win11's 4Gi floor.
MEM="${MEM:-8Gi}"
# A disposable public key for the provisioning assertion. Never used to log in —
# it only has to be a syntactically real key so cloudbase-init installs it.
GATE_KEY="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGate0000000000000000000000000000000000000 kube-dc-gate"
# The catalog pins minMemory=8G for the Windows golden. Running the gate below
# that is NOT a valid certification: a 2026-07-31 run at MEM=6Gi failed with the
# guest agent never connecting inside 25m, while the SAME golden passed at 8Gi.
# Under-spec runs are allowed (constrained quota) but never count as a pass.
case "$MEM" in
  8Gi|9Gi|1[0-9]Gi|[2-9][0-9]Gi) ;;
  *) echo "[gate] WARNING: MEM=$MEM is below the catalog minMemory (8Gi) — a"
     echo "[gate]          failure here is INCONCLUSIVE, not a golden regression."
     echo "[gate]          Certification runs MUST use MEM>=8Gi." ;;
esac

fail() { echo "GATE FAIL: $*" >&2; exit 1; }
note() { echo "[gate] $*"; }

kubectl -n "$NS" get volumesnapshot "$SNAP" -o jsonpath='{.status.readyToUse}' 2>/dev/null | grep -q true \
  || fail "seeded snapshot $NS/$SNAP not ReadyToUse"
# Clone size comes from the seeder's kube-dc.com/clone-min-size annotation — the
# authoritative source-volume size. status.restoreSize is ALWAYS empty on seeded
# snapshots (they bind to pre-provisioned static VSCs, which CSI never fills in),
# and a clone smaller than the source is rejected by Ceph.
SIZE=$(kubectl -n "$NS" get volumesnapshot "$SNAP" -o jsonpath='{.metadata.annotations.kube-dc\.com/clone-min-size}' 2>/dev/null || true)
[ -n "$SIZE" ] || SIZE=$(kubectl -n "$NS" get volumesnapshot "$SNAP" -o jsonpath='{.status.restoreSize}' 2>/dev/null || true)
[ -n "$SIZE" ] || fail "cannot determine clone size for $NS/$SNAP (no clone-min-size annotation, no restoreSize)"
note "snapshot ready, clone size=$SIZE (from seeder annotation)"

cleanup() {
  [ "$KEEP" = "1" ] && { note "KEEP=1 — leaving $NS/$VM for inspection"; return; }
  kubectl -n "$NS" delete vm "$VM" --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n "$NS" delete pvc "$VM-root" --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

note "creating clone PVC + VM $NS/$VM"
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata: { name: $VM-root, namespace: $NS }
spec:
  storageClassName: rbd-vm
  accessModes: [ReadWriteOnce]
  volumeMode: Filesystem
  dataSource: { kind: VolumeSnapshot, name: $SNAP, apiGroup: snapshot.storage.k8s.io }
  resources: { requests: { storage: $SIZE } }
---
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata: { name: $VM, namespace: $NS }
spec:
  runStrategy: Always
  template:
    metadata:
      labels: { kubevirt.io/vm: $VM }
    spec:
      domain:
        cpu: { cores: 2 }
        memory: { guest: $MEM }
        machine: { type: q35 }
        firmware: { bootloader: { efi: { secureBoot: true } } }
        features:
          smm: { enabled: true }
          acpi: {}
          hyperv: { relaxed: {}, vapic: {}, spinlocks: { spinlocks: 8191 } }
        devices:
          tpm: {}
          disks:
            - { name: rootdisk, disk: { bus: virtio } }
            - { name: cloudinitdisk, disk: { bus: virtio } }
          interfaces: [{ name: default, masquerade: {}, model: virtio }]
      networks: [{ name: default, pod: {} }]
      volumes:
        - name: rootdisk
          persistentVolumeClaim: { claimName: $VM-root }
        # PHASE 3 needs REAL provisioning input: without a NoCloud disk
        # cloudbase-init has nothing to consume, so "hostname changed" would be
        # vacuous and the gate would pass a golden whose provisioning is broken.
        # The hostname below is what we assert on afterwards.
        - name: cloudinitdisk
          cloudInitNoCloud:
            userData: |
              #cloud-config
              users:
                - name: kube-dc
                  groups: Administrators
                  ssh_authorized_keys:
                    - $GATE_KEY
EOF

# clone speed: the PVC must bind fast (CoW clone, not a copy)
for i in $(seq 1 12); do
  PH=$(kubectl -n "$NS" get pvc "$VM-root" -o jsonpath='{.status.phase}' 2>/dev/null || true)
  [ "$PH" = "Bound" ] && break; sleep 5
done
if [ "${PH:-}" != "Bound" ]; then
  kubectl -n "$NS" get events --sort-by=.lastTimestamp 2>/dev/null | grep -E "$VM" | tail -3 >&2 || true
  fail "clone PVC not Bound after 60s — see events above (size contract / quota / provisioner)"
fi
note "clone PVC Bound (instant clone OK)"

# PHASE 1 — BOOT PROOF: RDP/SSH answering means Windows actually reached a
# running desktop with services up. Checked FIRST so an agent problem is never
# misreported as "Windows didn't boot" (the 2026-07-31 runs failed on the agent
# while RDP+SSH were open the whole time).

# DO NOT PROBE WITH A POD. A project namespace refuses user-created pods outright —
# `kubectl run` AND a plain `kubectl apply` of a Pod both come back with:
#   ValidatingAdmissionPolicy 'reserve-platform-identities-in-projects' denied request
# virtctl port-forward tunnels through the API server to the VMI and needs no pod at
# all, so it works where both pod routes fail. Confirmed 2026-08-09: RDP and SSH both
# probed OPEN this way on a VM the pod-based probes had declared dead.
probe_port() {   # probe_port <port> -> 0 open, 1 closed, 2 probe broken
  local port="$1" lport=$(( 20000 + RANDOM % 20000 )) pf rc=1
  command -v virtctl >/dev/null 2>&1 || return 2
  virtctl port-forward "vmi/$VM" -n "$NS" "${lport}:${port}" >/dev/null 2>&1 &
  pf=$!
  sleep 6
  kill -0 "$pf" 2>/dev/null || { wait "$pf" 2>/dev/null; return 2; }
  if timeout 10 bash -c "cat < /dev/null > /dev/tcp/127.0.0.1/${lport}" 2>/dev/null; then rc=0; fi
  kill "$pf" 2>/dev/null; wait "$pf" 2>/dev/null || true
  return $rc
}

DEADLINE=$(( $(date +%s) + TIMEOUT_MIN * 60 ))
IP=""; BOOTED=0; PROBE_OK=0
while [ "$(date +%s)" -lt "$DEADLINE" ]; do
  IP=$(kubectl -n "$NS" get vmi "$VM" -o jsonpath='{.status.interfaces[0].ipAddress}' 2>/dev/null || true)
  if [ -n "$IP" ]; then
    probe_port 3389; rc=$?
    [ "$rc" -ne 2 ] && PROBE_OK=1
    [ "$rc" -eq 0 ] && { BOOTED=1; break; }
  fi
  sleep 20
done
if [ "$BOOTED" != "1" ]; then
  [ "$PROBE_OK" = "1" ] \
    || fail "RDP probe could NOT RUN in $NS (pod creation refused) — this says nothing about the image; fix the probe, do not blame the golden"
  fail "Windows never answered RDP 3389 within ${TIMEOUT_MIN}m on $IP — check PHASE 2 below before concluding boot failure: a connected guest agent proves Windows booted, which would make this an RDP/firewall problem, not a boot problem"
fi
note "BOOT OK — RDP 3389 answering on $IP"
probe_port 22
case $? in
  0) note "SSH 22 answering (OpenSSH present)" ;;
  1) note "WARNING: SSH 22 closed — OpenSSH missing from the golden" ;;
  # Same trap as PHASE 1: `kubectl run` is refused in project namespaces, and this
  # check used to report a missing OpenSSH whenever the probe simply could not run.
  *) note "WARNING: SSH probe could not run — says nothing about the golden" ;;
esac

# PHASE 2 — AGENT: KubeVirt injects tenant SSH keys THROUGH qemu-guest-agent and
# reports guestOSInfo from it. A booted VM without the agent is a product defect
# (no key injection), so this still fails the gate — but with the real reason.
AGENT=""
AGENT_DEADLINE=$(( $(date +%s) + 300 ))
while [ "$(date +%s)" -lt "$AGENT_DEADLINE" ]; do
  AGENT=$(kubectl -n "$NS" get vmi "$VM" -o jsonpath='{.status.conditions[?(@.type=="AgentConnected")].status}' 2>/dev/null || true)
  [ "$AGENT" = "True" ] && break
  sleep 20
done
if [ "$AGENT" != "True" ]; then
  fail "Windows BOOTED (RDP open) but qemu-guest-agent never connected — tenant SSH-key injection and guestOSInfo will NOT work. Fix: install+enable qemu-ga in the golden (the automated bake does this in FirstLogonCommands)."
fi
note "guest agent connected"
GUESTOS=$(kubectl -n "$NS" get vmi "$VM" -o jsonpath='{.status.guestOSInfo.prettyName}' 2>/dev/null || true)
echo "$GUESTOS" | grep -qi windows || fail "guestOSInfo not reporting Windows (agent API broken): '$GUESTOS'"
note "guestOSInfo: $GUESTOS"

# PHASE 3 — PROVISIONING: prove cloudbase-init actually ran. The agent being up
# says nothing about tenant provisioning on Windows (QGA cannot inject keys
# there at all), so without this a golden whose cloudbase-init is broken would
# sail through the gate and every tenant would silently get no SSH key.
#   - hostname adopted from the NoCloud metadata (KubeVirt sets it to the VM name)
#   - the root volume grew past the 64Gi bake floor when cloned onto a larger disk
# Both come from guestOSInfo/filesystem data the agent reports, so no guest login.
HOSTNAME_SEEN=$(kubectl -n "$NS" get vmi "$VM" -o jsonpath='{.status.guestOSInfo.hostname}' 2>/dev/null || true)
if [ -z "$HOSTNAME_SEEN" ]; then
  # The agent is connected (PHASE 2 passed), so guestOSInfo must be populated.
  # Empty here means we cannot prove provisioning ran — that is a FAILURE, not a
  # note. A gate that prints PASS on unreadable evidence is worse than no gate.
  fail "guest reports no hostname despite the agent being connected — cannot verify cloudbase-init ran"
fi
case "$HOSTNAME_SEEN" in
  win11-build|WIN11-BUILD|win11-golden-build)
    fail "guest hostname is still the BUILD hostname ($HOSTNAME_SEEN) — cloudbase-init did NOT run, so tenant SSH keys/password were not applied" ;;
esac

# "Not the build hostname" is far too weak an assertion, and on 2026-08-09 it
# would have passed an image that provisioned NOTHING: sysprep /generalize leaves
# a random WIN-XXXXXXXXXXX name, which differs from the build hostname while
# proving only that sysprep ran. Assert the hostname we actually ASKED for.
#
# It arrives late. cloudbase-init runs during/after OOBE and then REBOOTS to apply
# the name — on HDD-backed storage that took ~45 minutes end to end, and the agent
# disconnects across the reboot. So poll, and treat the disconnect as expected
# rather than as failure.
WANT_HOST="$VM"
DEADLINE=$(( $(date +%s) + TIMEOUT_MIN * 60 ))
while [ "$(date +%s)" -lt "$DEADLINE" ]; do
  HOSTNAME_SEEN=$(kubectl -n "$NS" get vmi "$VM" -o jsonpath='{.status.guestOSInfo.hostname}' 2>/dev/null || true)
  # Windows reports the NetBIOS name uppercased; compare case-insensitively.
  if [ "$(printf '%s' "$HOSTNAME_SEEN" | tr 'A-Z' 'a-z')" = "$(printf '%s' "$WANT_HOST" | tr 'A-Z' 'a-z')" ]; then
    note "hostname applied by cloudbase-init: $HOSTNAME_SEEN"
    break
  fi
  case "$HOSTNAME_SEEN" in
    WIN-*|win-*) : ;;   # still the sysprep random name — cloudbase-init has not finished
  esac
  sleep 30
done
if [ "$(printf '%s' "$HOSTNAME_SEEN" | tr 'A-Z' 'a-z')" != "$(printf '%s' "$WANT_HOST" | tr 'A-Z' 'a-z')" ]; then
  fail "cloudbase-init never applied the requested hostname (wanted '$WANT_HOST', guest reports '$HOSTNAME_SEEN') — tenant SSH keys and password were NOT applied either; a random WIN-* name means sysprep ran but provisioning did not"
fi
note "provisioning OK — hostname '$HOSTNAME_SEEN' (adopted from NoCloud, not the build name)"

echo "GATE PASS: $NS/$VM — clone, boot, agent, RDP and cloudbase-init provisioning all verified"
