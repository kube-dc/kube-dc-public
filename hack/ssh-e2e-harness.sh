#!/usr/bin/env bash
# Disposable SSH harness for the CLI git adapter's end-to-end specs.
#
# Stands up a throwaway sshd + ssh-agent + two bare repositories in a
# scratch directory, prints the environment the specs need, and exits.
# Nothing here touches the developer's ~/.ssh, any fleet repository, any
# real remote, or any cluster: the sshd runs on 127.0.0.1 with its own
# host key and its own authorized_keys, and HOME is redirected into the
# scratch tree so go-git reads the harness known_hosts and key files
# rather than the real ones.
#
#   eval "$(hack/ssh-e2e-harness.sh up)"
#   go -C cli test -count=1 -v -run TestSSHE2E ./internal/bootstrap/adapters/git/
#   hack/ssh-e2e-harness.sh down
#
# Two bare repos exist so the specs can prove *direction*: origin is
# rewritten to fetch from A and push to B, and a wrong choice writes to
# the wrong server where the assertions can see it.
#
# CREDENTIAL MODES. A setup with the accepted key in BOTH the agent and a
# key file is the realistic developer configuration, but it cannot
# distinguish "the agent worked" from "the key file worked" -- a build
# that ignored key files entirely would still pass. SSH_E2E_MODE narrows
# each run to one usable source:
#
#   both       (default) accepted key in the agent AND on disk
#   agent      accepted key ONLY in the agent; no key file on disk
#   keyfile    accepted key ONLY on disk; no agent at all
#   wrongagent agent holds a key the server REJECTS; accepted key on disk
#   emptyagent agent reachable but holds nothing; accepted key on disk
#
# The last two are the P1b regression: a reachable agent must not
# suppress the key-file fallback. Run the whole matrix with:
#
#   hack/ssh-e2e-harness.sh matrix
#
# TEARDOWN SAFETY. `down` recursively deletes its root and kills PIDs, so
# it refuses to act on anything it did not create:
#
#   - the root must carry an ownership marker written by `up`
#   - SSH_E2E_ROOT must be absolute, canonical, non-system, and not $HOME
#   - a recorded PID is killed only if its start time still matches AND
#     its binary is the expected one AND its argv names this root
#
# `up` is failure-atomic: the cleanup trap is installed before anything
# is spawned, so an orderly failure part-way through cannot strand a root
# or an orphaned sshd.
#
# SIGKILL is the exception -- no trap can run. For that case a supervisor
# must know the root BEFORE `up` starts:
#
#   root="$(hack/ssh-e2e-harness.sh newroot)"
#   SSH_E2E_ROOT="$root" hack/ssh-e2e-harness.sh up
#   ...
#   hack/ssh-e2e-harness.sh down "$root"    # works even if up was killed
#
# `matrix` does exactly this. A plain `up` with no explicit root prints
# its root only on success, so if you SIGKILL it there is nothing to pass
# to `down`; teardown then needs the path from `ls -d $TMPDIR/kubedc-ssh-e2e.*`.
# Prefer the newroot form in scripts and CI.
#
# `hack/ssh-e2e-harness.sh selftest` exercises the teardown-safety
# guarantees adversarially (forged/empty/garbage PID records, argv prefix
# attacks, unmarked and system roots, shell-injection payloads).
set -euo pipefail

MARKER_NAME=".kubedc-ssh-e2e-harness"
MARKER_MAGIC="kubedc-ssh-e2e-harness-v1"
PORT="${SSH_E2E_PORT:-2222}"
SSHD_BIN="${SSHD_BIN:-/usr/sbin/sshd}"
MODE="${SSH_E2E_MODE:-both}"

die() { echo "ssh-e2e-harness: $*" >&2; exit 1; }

# emit_export prints a shell-safe `export NAME=VALUE` line. printf '%q'
# produces a valid shell literal for any byte, so a value containing
# quotes or spaces cannot break out into executable text when the output
# is sourced -- which matrix does.
emit_export() { printf 'export %s=%q\n' "$1" "$2"; }

# resolve_root prints the canonical path of $1 without requiring it to
# exist, and refuses paths that must never be handed to `rm -rf`.
resolve_root() {
  local p="$1" canon
  case "$p" in
    /*) ;;
    *)  die "SSH_E2E_ROOT must be absolute, got '$p'" ;;
  esac
  # Canonicalise so symlink games cannot smuggle in a different target.
  canon="$(readlink -m -- "$p")" || die "cannot resolve '$p'"
  case "$canon" in
    /|/bin|/boot|/dev|/etc|/home|/lib|/opt|/proc|/root|/run|/sbin|/srv|/sys|/usr|/var)
      die "refusing to use '$canon' as a scratch root" ;;
  esac
  [ "$(dirname -- "$canon")" = "/" ] && die "refusing to use top-level '$canon' as a scratch root"
  [ "$canon" = "$HOME" ] && die "refusing to use \$HOME as a scratch root"
  # Reject whitespace and quoting characters. This is not about shell
  # safety -- emit_export and the literal trap body handle any byte -- it
  # is about IDENTITY. sshd rewrites its process title by joining argv
  # with spaces, so for a root containing whitespace the title is
  # ambiguous and no exact-token match can confirm that a PID is ours.
  # Since that check is what gates killing a process, a root we cannot
  # verify must be refused rather than half-supported.
  case "$canon" in
    *[[:space:]]*|*\'*|*\"*|*\\*)
      die "root '$canon' contains whitespace or quoting characters; process identity cannot be verified for such a path -- use a plain path (unset SSH_E2E_ROOT for an mktemp root)" ;;
  esac
  printf '%s\n' "$canon"
}

# assert_ours refuses to touch a directory this harness did not create.
assert_ours() {
  local root="$1"
  [ -d "$root" ] || die "no such harness root: $root"
  [ -f "$root/$MARKER_NAME" ] || die "refusing to touch '$root': no $MARKER_NAME marker (not created by this harness)"
  grep -qxF "$MARKER_MAGIC" "$root/$MARKER_NAME" 2>/dev/null \
    || die "refusing to touch '$root': marker present but not ours"
}

# record_pid stores ONLY the PID and its start time. Everything used to
# decide whether the process is still ours is derived at teardown from
# the trusted root, never from this file: a PID file is mutable, so any
# identity claim it carries is attacker-controlled. An earlier version
# recorded the command name here and compared /proc against it, which any
# correctly-formed forged record naming a live process satisfied.
record_pid() { # file, pid
  local f="$1" pid="$2"
  printf '%s %s\n' "$pid" "$(awk '{print $22}' "/proc/$pid/stat")" > "$f"
}

# pid_is_ours verifies a recorded PID is still the process `up` started:
#
#   1. start time matches   -- a recycled PID cannot reproduce it
#      (field 22 of /proc/<pid>/stat)
#   2. the binary is the    -- $want_exe is passed by the CALLER as a
#      expected one            literal, never read from the PID file
#   3. argv contains an     -- an EXACT argument, compared whole against
#      exact expected           NUL-delimited argv. Substring matching
#      argument                 was not enough: root /tmp/foo matched an
#                               unrelated agent on /tmp/foobar.sock.
#
# (2) and (3) together mean a forged PID file cannot nominate an
# unrelated process: the victim would already have to be the right binary
# AND already have been started with our exact config/socket argument.
pid_is_ours() { # pid, want_start, want_exe, want_arg
  local pid="$1" want_start="$2" want_exe="$3" want_arg="$4" start exe arg
  [ -n "$pid" ] && [ -d "/proc/$pid" ] || return 1
  start="$(awk '{print $22}' "/proc/$pid/stat" 2>/dev/null)" || return 1
  [ "$start" = "$want_start" ] || return 1
  # Prefer the exe link. ssh-agent is setgid, so /proc/<pid>/exe is
  # unreadable even to its owner and readlink yields nothing -- fall back
  # to comm there. Both are compared against $want_exe, the caller's
  # literal; the point is that the expected identity never comes from the
  # mutable PID file.
  exe="$(readlink -f "/proc/$pid/exe" 2>/dev/null || true)"
  if [ -n "$exe" ]; then
    [ "$(basename "$exe")" = "$want_exe" ] || return 1
  else
    [ "$(tr -d '\0' < "/proc/$pid/comm" 2>/dev/null)" = "$want_exe" ] || return 1
  fi
  # Walk NUL-delimited argv and require an EXACT match for $want_arg --
  # never a substring, or root /tmp/foo would match an unrelated agent
  # on /tmp/foobar.sock.
  #
  # Two shapes have to be handled. Normally each argument is its own
  # NUL-delimited element (ssh-agent: "-a", "<root>/agent.sock"). But
  # sshd rewrites its process title, collapsing everything into ONE
  # element: "sshd: /usr/sbin/sshd -f <cfg> -E <log> [listener] 0 of
  # 10-100 startups". So each element is also split on whitespace and
  # matched token-by-token. Both comparisons are whole-value equality,
  # so the prefix attack stays closed either way.
  local toks tok
  while IFS= read -r -d '' arg; do
    [ "$arg" = "$want_arg" ] && return 0
    read -ra toks <<< "$arg"   # splits on IFS, no globbing
    for tok in "${toks[@]}"; do
      [ "$tok" = "$want_arg" ] && return 0
    done
  done < "/proc/$pid/cmdline"
  return 1
}

# kill_recorded returns NONZERO on an identity mismatch and leaves the
# PID record in place.
#
# It used to log the mismatch, delete the record and return success, so
# down_at went on to delete the root and the matrix stayed green while
# the process was still running -- a leak reported as a clean teardown.
# A mismatch means either a forged record or a process we have lost track
# of; both need the root kept for investigation, so the failure has to
# propagate all the way out of `down`.
kill_recorded() { # file, want_exe, want_arg, label
  local f="$1" want_exe="$2" want_arg="$3" label="$4" pid start
  # No record at all is legitimate (that mode never spawned this
  # process); the unrecorded sweep still covers anything that WAS
  # spawned. But a record that EXISTS and cannot be read is a different
  # thing entirely: it used to `return 0`, so an empty or truncated
  # agent.pid reported a clean teardown while the agent kept running and
  # the root was deleted underneath it. Unreadable means "we have lost
  # track of a process we started" -- that has to fail.
  [ -f "$f" ] || return 0
  read -r pid start < "$f" || true
  if [ -z "$pid" ] || [ -z "$start" ]; then
    echo "ssh-e2e-harness: $label record $f is empty or malformed" >&2
    echo "ssh-e2e-harness:   cannot verify the process it refers to; root PRESERVED" >&2
    return 1
  fi
  case "$pid$start" in
    *[!0-9]*)
      echo "ssh-e2e-harness: $label record $f is not numeric (pid=$pid start=$start)" >&2
      echo "ssh-e2e-harness:   refusing to act on it; root PRESERVED" >&2
      return 1 ;;
  esac
  if pid_is_ours "$pid" "$start" "$want_exe" "$want_arg"; then
    kill "$pid" 2>/dev/null || true
    rm -f "$f"
    return 0
  fi
  echo "ssh-e2e-harness: REFUSING to kill $label pid $pid: identity mismatch" >&2
  echo "ssh-e2e-harness:   expected exe=$want_exe arg=$want_arg" >&2
  echo "ssh-e2e-harness:   record and root PRESERVED for investigation" >&2
  return 1
}

# up_failed tears down a partially-built harness. Invoked from the ERR
# and EXIT traps armed by `up`; a no-op once `up` has completed.
up_failed() {
  local root="$1"
  [ "${UP_COMPLETE:-0}" = "1" ] && return 0
  # Both ERR and EXIT fire on a failing command; disarm immediately so
  # cleanup runs exactly once.
  trap - ERR EXIT
  UP_COMPLETE=1
  echo "ssh-e2e-harness: setup failed, cleaning up $root" >&2
  down_at "$root" || echo "ssh-e2e-harness: WARNING cleanup of $root incomplete; inspect it by hand" >&2
}

# newroot creates and marks an empty harness root, prints its path, and
# exits. A supervising caller (matrix, CI) allocates the root FIRST and
# passes it to `up` via SSH_E2E_ROOT, so it still knows what to tear down
# if `up` is SIGKILLed mid-spawn -- the one failure mode traps cannot
# cover. Without this, a killed `up` left a root whose name had never
# been revealed and which nothing could therefore reap.
newroot() {
  local root
  root="$(mktemp -d "${TMPDIR:-/tmp}/kubedc-ssh-e2e.XXXXXXXX")"
  printf '%s\n' "$MARKER_MAGIC" > "$root/$MARKER_NAME"
  chmod 700 "$root"
  printf '%s\n' "$root"
}

up() {
  local root
  if [ -n "${SSH_E2E_ROOT:-}" ]; then
    root="$(resolve_root "$SSH_E2E_ROOT")"
    # Only reuse a path we already own; never adopt a stranger's dir.
    [ -e "$root" ] && assert_ours "$root" && down_at "$root"
    mkdir -p "$root"
  else
    root="$(mktemp -d "${TMPDIR:-/tmp}/kubedc-ssh-e2e.XXXXXXXX")"
  fi
  printf '%s\n' "$MARKER_MAGIC" > "$root/$MARKER_NAME"
  chmod 700 "$root"
  mkdir -p "$root/etc"

  # FAILURE ATOMICITY. Everything below can fail: ssh-keygen, sshd,
  # ssh-keyscan, git, ssh-agent. Publish the state pointer and arm the
  # cleanup trap NOW, while the root is still empty, so no later failure
  # can strand a root or -- worse -- a live sshd that `down` has no way
  # to find. Previously the pointer was written last, so any failure
  # after sshd started orphaned the daemon.
  UP_COMPLETE=0
  # The trap body is a LITERAL single-quoted string that reads the root
  # from a variable at fire time. Interpolating the path into the trap
  # text meant a root containing an apostrophe (possible via an explicit
  # SSH_E2E_ROOT or an exotic TMPDIR) closed the quote and turned the
  # rest of the path into executable shell.
  UP_ROOT="$root"
  trap 'up_failed "$UP_ROOT"' ERR EXIT

  # The agent socket ALWAYS lives under the root. A fallback location
  # outside it would break the argv-names-root identity check, and
  # cleaning it up meant globbing every kubedc-e2e-*.sock in TMPDIR --
  # which would pull the socket out from under a concurrent run. Unix
  # socket paths cap out near 100 bytes, so an over-long root is refused
  # up front with actionable guidance instead.
  local agent_sock="$root/agent.sock"
  if [ "${#agent_sock}" -gt 90 ]; then
    die "root '$root' is too long for a unix socket (${#agent_sock} > 90 chars); use a shorter SSH_E2E_ROOT, or unset it to get an mktemp root"
  fi

  mkdir -p "$root/home/.ssh"
  chmod 700 "$root/home/.ssh"

  ssh-keygen -q -t ed25519 -N '' -f "$root/etc/hostkey" -C kubedc-e2e-host
  ssh-keygen -q -t ed25519 -N '' -f "$root/etc/accepted" -C kubedc-e2e-accepted
  ssh-keygen -q -t ed25519 -N '' -f "$root/etc/rejected" -C kubedc-e2e-rejected-decoy
  # ONLY the accepted key is authorized server-side.
  cp "$root/etc/accepted.pub" "$root/etc/authorized_keys"
  chmod 600 "$root/etc/authorized_keys" "$root/etc/hostkey" \
            "$root/etc/accepted" "$root/etc/rejected"

  # Place the accepted key on disk under a DEFAULT filename (so go-git's
  # key-file signers find it) only in the modes that should have it.
  case "$MODE" in
    both|keyfile|wrongagent|emptyagent)
      cp "$root/etc/accepted" "$root/home/.ssh/id_ed25519"
      cp "$root/etc/accepted.pub" "$root/home/.ssh/id_ed25519.pub"
      chmod 600 "$root/home/.ssh/id_ed25519" ;;
    agent) ;; # deliberately no key file on disk
    *) die "unknown SSH_E2E_MODE '$MODE'" ;;
  esac

  # Paths are double-quoted: sshd_config splits unquoted values on
  # whitespace, so a root containing a space or apostrophe made sshd fail
  # to start. Quoting keeps the harness working on exotic roots, which is
  # exactly where the shell-quoting guarantees need proving.
  cat > "$root/etc/sshd_config" <<EOF
Port $PORT
ListenAddress 127.0.0.1
HostKey "$root/etc/hostkey"
AuthorizedKeysFile "$root/etc/authorized_keys"
PidFile "$root/etc/sshd.rawpid"
StrictModes no
UsePAM no
PasswordAuthentication no
KbdInteractiveAuthentication no
PubkeyAuthentication yes
Subsystem sftp /usr/lib/openssh/sftp-server
EOF

  "$SSHD_BIN" -f "$root/etc/sshd_config" -E "$root/etc/sshd.log"
  local waited=0
  while [ ! -s "$root/etc/sshd.rawpid" ] && [ "$waited" -lt 50 ]; do
    sleep 0.1; waited=$((waited+1))
  done
  [ -s "$root/etc/sshd.rawpid" ] || die "sshd failed to start; see $root/etc/sshd.log"
  record_pid "$root/etc/sshd.pid" "$(cat "$root/etc/sshd.rawpid")"
  ssh-keyscan -p "$PORT" -t ed25519 127.0.0.1 2>/dev/null > "$root/home/.ssh/known_hosts"
  [ -s "$root/home/.ssh/known_hosts" ] || die "ssh-keyscan produced no host key on port $PORT"

  # A and B start identical. `git init --bare` points HEAD at master, so
  # retarget it or go-git's clone fails with "reference not found".
  local seed="$root/seed"
  git init -q "$seed"
  git -C "$seed" config user.email e2e@example.invalid
  git -C "$seed" config user.name "E2E Harness"
  echo v1 > "$seed/file.txt"
  git -C "$seed" add .
  git -C "$seed" commit -q -m initial
  git -C "$seed" branch -M main
  local r
  for r in repoA repoB; do
    git init -q --bare "$root/$r.git"
    git -C "$seed" push -q "$root/$r.git" main
    git --git-dir="$root/$r.git" symbolic-ref HEAD refs/heads/main
  done

  # Agent, per mode. Carry the socket as a VALUE; the export text is
  # built once, safely, at emit time.
  local agent_sock_out=""
  case "$MODE" in
    keyfile) ;; # no agent at all
    both|agent|wrongagent|emptyagent)
      rm -f "$agent_sock"
      local apid
      # Land the agent's stdout on disk BEFORE parsing it: that file is
      # what reap_unrecorded uses if we are interrupted between the spawn
      # and record_pid below. Piping straight into sed left a window in
      # which the PID existed only inside a subshell.
      ssh-agent -a "$agent_sock" > "$root/etc/agent.raw"
      apid="$(sed -n 's/.*SSH_AGENT_PID=\([0-9]*\).*/\1/p' "$root/etc/agent.raw")"
      [ -n "$apid" ] || die "ssh-agent failed to start"
      record_pid "$root/etc/agent.pid" "$apid"
      # agent.raw is deliberately KEPT until teardown. Deleting it here
      # left the unrecorded sweep with no fallback, so a damaged
      # agent.pid meant the agent could not be found at all.
      case "$MODE" in
        both|agent)  SSH_AUTH_SOCK="$agent_sock" ssh-add "$root/etc/accepted" 2>/dev/null ;;
        wrongagent)  SSH_AUTH_SOCK="$agent_sock" ssh-add "$root/etc/rejected" 2>/dev/null ;;
        emptyagent)  ;; # reachable, holds nothing
      esac
      agent_sock_out="$agent_sock" ;;
  esac

  UP_COMPLETE=1
  trap - ERR EXIT

  local user; user="$(whoami)"

  # Emit exports with printf '%q', which produces a shell-safe literal
  # for ANY byte in the value. The previous heredoc wrapped raw paths in
  # single quotes, so a root containing an apostrophe closed the quote
  # early and the remainder became executable shell in whatever process
  # sourced this output -- and matrix sources it.
  emit_export SSH_E2E_ROOT             "$root"
  emit_export KUBEDC_SSH_E2E           1
  emit_export KUBEDC_SSH_E2E_MODE      "$MODE"
  emit_export KUBEDC_SSH_E2E_HOME      "$root/home"
  emit_export KUBEDC_SSH_E2E_REPO_A    "ssh://$user@127.0.0.1:$PORT$root/repoA.git"
  emit_export KUBEDC_SSH_E2E_REPO_B    "ssh://$user@127.0.0.1:$PORT$root/repoB.git"
  emit_export KUBEDC_SSH_E2E_BARE_A    "$root/repoA.git"
  emit_export KUBEDC_SSH_E2E_BARE_B    "$root/repoB.git"
  emit_export KUBEDC_SSH_E2E_ACCEPTED_PUB "$root/etc/accepted.pub"
  emit_export KUBEDC_SSH_E2E_REJECTED_PUB "$root/etc/rejected.pub"
  if [ -n "$agent_sock_out" ]; then
    emit_export SSH_AUTH_SOCK "$agent_sock_out"
  else
    printf 'unset SSH_AUTH_SOCK\n'
  fi
}

down_at() {
  local root="$1" failed=0
  assert_ours "$root"

  # Reap the recorded processes. The expected argv element is derived
  # from the root, so it is exactly what `up` passed when spawning.
  kill_recorded "$root/etc/sshd.pid"  sshd      "$root/etc/sshd_config" sshd      || failed=1
  kill_recorded "$root/etc/agent.pid" ssh-agent "$root/agent.sock"      ssh-agent || failed=1

  # Sweep the spawn windows: sshd writes its own PidFile, and `up`
  # redirects ssh-agent's stdout to a file, so a process spawned but not
  # yet recorded is still discoverable. Without this, an interrupt in
  # those few milliseconds orphaned a daemon nothing could find.
  reap_unrecorded "$root" || failed=1

  if [ "$failed" -ne 0 ]; then
    echo "ssh-e2e-harness: NOT deleting $root -- a process could not be verified/reaped" >&2
    return 1
  fi
  # No socket glob: the agent socket lives inside $root and goes with it.
  rm -rf -- "$root"
}

# reap_unrecorded closes the window between spawning a process and
# recording its PID. sshd's own PidFile and the captured ssh-agent stdout
# are written by those processes/redirections themselves, so they exist
# even when `up` was interrupted before record_pid ran.
#
# Start time is unavailable on this path, so identity rests on the exact
# exe + exact argv argument. That argument contains this instance's
# unique mktemp root, which no unrelated process can hold.
reap_unrecorded() { # root
  local root="$1" pid rc=0
  if [ -s "$root/etc/sshd.rawpid" ]; then
    pid="$(cat "$root/etc/sshd.rawpid")"
    if pid_is_ours "$pid" "$(awk '{print $22}' "/proc/$pid/stat" 2>/dev/null)" sshd "$root/etc/sshd_config"; then
      kill "$pid" 2>/dev/null || true
    elif [ -d "/proc/$pid" ]; then
      echo "ssh-e2e-harness: unrecorded sshd pid $pid does not match this root; leaving it" >&2
      rc=1
    fi
    rm -f "$root/etc/sshd.rawpid"
  fi
  if [ -s "$root/etc/agent.raw" ]; then
    pid="$(sed -n 's/.*SSH_AGENT_PID=\([0-9]*\).*/\1/p' "$root/etc/agent.raw")"
    if [ -n "$pid" ]; then
      if pid_is_ours "$pid" "$(awk '{print $22}' "/proc/$pid/stat" 2>/dev/null)" ssh-agent "$root/agent.sock"; then
        kill "$pid" 2>/dev/null || true
      elif [ -d "/proc/$pid" ]; then
        echo "ssh-e2e-harness: unrecorded ssh-agent pid $pid does not match this root; leaving it" >&2
        rc=1
      fi
    fi
    rm -f "$root/etc/agent.raw"
  fi
  return "$rc"
}

# down tears down ONE instance, identified by its root.
#
# The root arrives as an argument or as SSH_E2E_ROOT (which `up` prints
# as an export, so `eval "$(... up)"` puts it in the caller's
# environment). There is deliberately no shared state file: a single
# global pointer meant two concurrent runs overwrote each other's, and
# whichever called `down` second tore down a stranger's harness.
down() {
  local root="${1:-${SSH_E2E_ROOT:-}}"
  if [ -z "$root" ]; then
    echo "ssh-e2e-harness: no instance to tear down." >&2
    echo "ssh-e2e-harness: pass the root as an argument, or eval the output of \`up\` first (it exports SSH_E2E_ROOT)." >&2
    return 2
  fi
  root="$(resolve_root "$root")"
  down_at "$root"
  echo "ssh e2e harness torn down: $root" >&2
}

# matrix runs the E2E specs once per credential mode. Each mode is a
# distinct assertion about WHICH credential source carried the handshake,
# which a single combined setup cannot make.
matrix() {
  local repo_root mode rc=0 tmp_env
  repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  tmp_env="$(mktemp -d "${TMPDIR:-/tmp}/kubedc-ssh-e2e-env.XXXXXXXX")"
  trap 'rm -rf "$tmp_env"' RETURN
  for mode in both agent keyfile wrongagent emptyagent; do
    echo "=== SSH_E2E_MODE=$mode ==="
    # Allocate the root BEFORE launching up, so this loop can tear it
    # down even if up never returns (SIGKILL, OOM). Learning the root
    # only from up's output meant a killed setup stranded a root nobody
    # could name, which made the unrecorded-process sweep unreachable.
    local env_file="$tmp_env/$mode.env" root
    root="$("${BASH_SOURCE[0]}" newroot)"
    if ! SSH_E2E_ROOT="$root" SSH_E2E_MODE="$mode" "${BASH_SOURCE[0]}" up > "$env_file"; then
      echo "ssh-e2e-harness: SETUP FAILED for mode=$mode (root $root)" >&2
      # up's own trap cleans up on an orderly failure; on a kill it never
      # ran, so sweep here too. Either way the root is known.
      "${BASH_SOURCE[0]}" down "$root" >/dev/null 2>&1 || \
        echo "ssh-e2e-harness: root $root preserved for investigation" >&2
      rc=1
      continue
    fi
    (
      # shellcheck disable=SC1090
      . "$env_file"
      go -C "$repo_root/cli" test -count=1 -run TestSSHE2E ./internal/bootstrap/adapters/git/
    ) || rc=1
    # A failed teardown leaves a live sshd and a scratch root behind.
    # Swallowing it turned a real leak into a green gate.
    if ! "${BASH_SOURCE[0]}" down "$root"; then
      echo "ssh-e2e-harness: TEARDOWN FAILED for mode=$mode (root $root preserved)" >&2
      rc=1
    fi
  done
  return "$rc"
}

# selftest exercises the teardown-safety guarantees adversarially. These
# are the behaviours that decide whether a leak is reported or hidden, so
# they need to be checked on every change rather than by hand once. Each
# case asserts BOTH that the harness refuses the unsafe action AND that
# the thing it was protecting is still alive/intact afterwards.
selftest() {
  local pass=0 fail=0 root victim rc alive exists root_env
  local self="${BASH_SOURCE[0]}"
  root_env="$(mktemp)"
  trap 'rm -f "$root_env"' RETURN

  ok()   { echo "  PASS  $1"; pass=$((pass+1)); }
  bad()  { echo "  FAIL  $1"; fail=$((fail+1)); }
  check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (got '$2', want '$3')"; fi; }

  echo "== empty PID record: must fail and preserve =="
  root="$(newroot)"; mkdir -p "$root/etc"
  sleep 60 & victim=$!
  : > "$root/etc/agent.pid"            # exists but empty
  rc=0; "$self" down "$root" >/dev/null 2>&1 || rc=$?
  alive=no; kill -0 "$victim" 2>/dev/null && alive=yes
  exists=no; [ -d "$root" ] && exists=yes
  check "empty record -> down fails"        "$rc"     "1"
  check "empty record -> root preserved"    "$exists" "yes"
  kill "$victim" 2>/dev/null || true; rm -rf "$root"

  echo "== non-numeric PID record: must fail and preserve =="
  root="$(newroot)"; mkdir -p "$root/etc"
  printf 'notapid notatime\n' > "$root/etc/sshd.pid"
  rc=0; "$self" down "$root" >/dev/null 2>&1 || rc=$?
  exists=no; [ -d "$root" ] && exists=yes
  check "garbage record -> down fails"      "$rc"     "1"
  check "garbage record -> root preserved"  "$exists" "yes"
  rm -rf "$root"

  echo "== missing record, live unrecorded agent: must be swept =="
  root="$(newroot)"; mkdir -p "$root/etc"
  rm -f "$root/agent.sock"
  ssh-agent -a "$root/agent.sock" > "$root/etc/agent.raw" 2>/dev/null
  victim="$(sed -n 's/.*SSH_AGENT_PID=\([0-9]*\).*/\1/p' "$root/etc/agent.raw")"
  # No agent.pid at all -- only the raw startup output the sweep reads.
  rc=0; "$self" down "$root" >/dev/null 2>&1 || rc=$?
  sleep 0.3
  alive=no; kill -0 "$victim" 2>/dev/null && alive=yes
  check "unrecorded agent -> reaped"        "$alive"  "no"
  check "unrecorded agent -> down succeeds" "$rc"     "0"
  kill "$victim" 2>/dev/null || true; rm -rf "$root"

  echo "== truthful forged record naming an unrelated process: refuse =="
  root="$(newroot)"; mkdir -p "$root/etc"
  sleep 60 & victim=$!
  printf '%s %s\n' "$victim" "$(awk '{print $22}' "/proc/$victim/stat")" > "$root/etc/sshd.pid"
  rc=0; "$self" down "$root" >/dev/null 2>&1 || rc=$?
  alive=no; kill -0 "$victim" 2>/dev/null && alive=yes
  check "forged record -> victim survives"  "$alive"  "yes"
  check "forged record -> down fails"       "$rc"     "1"
  kill "$victim" 2>/dev/null || true; rm -rf "$root"

  echo "== argv prefix attack: root /tmp/X vs agent on /tmp/Xsuffix.sock =="
  root="$(newroot)"; mkdir -p "$root/etc"
  rm -f "${root}suffix.sock"
  ssh-agent -a "${root}suffix.sock" > "$root/etc/other.raw" 2>/dev/null
  victim="$(sed -n 's/.*SSH_AGENT_PID=\([0-9]*\).*/\1/p' "$root/etc/other.raw")"
  printf '%s %s\n' "$victim" "$(awk '{print $22}' "/proc/$victim/stat")" > "$root/etc/agent.pid"
  rm -f "$root/etc/other.raw"
  rc=0; "$self" down "$root" >/dev/null 2>&1 || rc=$?
  alive=no; kill -0 "$victim" 2>/dev/null && alive=yes
  check "prefix attack -> agent survives"   "$alive"  "yes"
  check "prefix attack -> down fails"       "$rc"     "1"
  kill "$victim" 2>/dev/null || true; rm -f "${root}suffix.sock"; rm -rf "$root"

  echo "== unmarked / system / relative roots: refuse =="
  victim="$(mktemp -d)"; echo precious > "$victim/keep"
  rc=0; "$self" down "$victim" >/dev/null 2>&1 || rc=$?
  check "unmarked dir -> refused"           "$rc"     "1"
  check "unmarked dir -> contents intact"   "$([ -f "$victim/keep" ] && echo yes || echo no)" "yes"
  rm -rf "$victim"
  for bad_root in "$HOME" / /etc ./relative; do
    rc=0; "$self" down "$bad_root" >/dev/null 2>&1 || rc=$?
    check "refuses root '$bad_root'"        "$rc"     "1"
  done

  echo "== hostile root characters: refused, and serialization stays safe =="
  # Roots with whitespace/quotes are refused because sshd's process-title
  # rewriting makes identity unverifiable for them (see resolve_root).
  for hostile in "${TMPDIR:-/tmp}/kubedc-e2e-it's a test" \
                 "${TMPDIR:-/tmp}/kubedc-e2e-two words" \
                 "${TMPDIR:-/tmp}/kubedc-e2e-quote\"d"; do
    rc=0; SSH_E2E_ROOT="$hostile" "$self" up >/dev/null 2>&1 || rc=$?
    check "refuses hostile root" "$rc" "1"
    [ -e "$hostile" ] && bad "hostile root was created anyway: $hostile"
  done

  # Serialization itself must still be injection-proof: emit_export is
  # what matrix sources, so a value containing quotes must round-trip
  # exactly rather than terminating the literal and becoming code. The
  # canary file must NOT exist afterwards.
  local canary="${TMPDIR:-/tmp}/kubedc-e2e-injection-canary.$$"
  rm -f "$canary"
  local evil="/tmp/x'; touch $canary; echo '"
  emit_export EVIL_VALUE "$evil" > "$root_env"
  ( set -e; . "$root_env"; [ "$EVIL_VALUE" = "$evil" ] ) \
    && ok "emit_export round-trips a quote-injection payload verbatim" \
    || bad "emit_export mangled a quote-injection payload"
  if [ -e "$canary" ]; then
    bad "emit_export EXECUTED injected shell (canary created)"
    rm -f "$canary"
  else
    ok "emit_export executed nothing (no canary)"
  fi

  echo
  echo "selftest: $pass passed, $fail failed"
  [ "$fail" -eq 0 ]
}

case "${1:-up}" in
  up)      up ;;
  newroot) newroot ;;
  down)     shift || true; down "$@" ;;
  selftest) selftest ;;
  matrix) matrix ;;
  *)       echo "usage: $0 [up|newroot|down [root]|matrix|selftest]" >&2; exit 2 ;;
esac
