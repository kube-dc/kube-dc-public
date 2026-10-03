package setup

import (
	"context"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

const hostClaimBase = "/var/lib/kube-dc"
const hostClaimTTL = 30 * time.Minute

var claimBootPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func validClaimMachine(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == 16 && value == strings.ToLower(value)
}

// HostClaim is a reservation and fencing identity, not installation readiness.
// Monotonic expiry is meaningful only on the recorded host boot.
type HostClaim struct {
	HostID         string `json:"hostId"`
	MachineID      string `json:"machineId"`
	BootID         string `json:"bootId"`
	ObservedBootID string `json:"observedBootId"`
	SessionID      string `json:"sessionId"`
	RunID          string `json:"runId"`
	InputHash      string `json:"inputHash"`
	ReviewHash     string `json:"reviewHash"`
	ReleaseSHA256  string `json:"releaseSHA256"`
	ExpiresTick    int64  `json:"expiresTick"`
	ObservedTick   int64  `json:"observedTick"`
	State          string `json:"state"`
}

type HostClaims struct {
	compiled         Compiled
	review           SafetyReview
	sessionID, runID string
	ssh              ports.GuardedPutSSHClient
}

func NewHostClaims(c Compiled, review SafetyReview, sessionID, runID string, ssh ports.GuardedPutSSHClient) (*HostClaims, error) {
	if err := validateSafetyReview(c, review); err != nil {
		return nil, err
	}
	if !validSessionHash(sessionID) || !validSessionHash(runID) || ssh == nil {
		return nil, fmt.Errorf("claims require a session, fresh run identity, and guarded SSH")
	}
	return &HostClaims{compiled: c, review: review, sessionID: sessionID, runID: runID, ssh: ssh}, nil
}

func (p *HostClaims) expected(host Host) (HostClaim, ports.SSHHost, error) {
	if err := checkCompiled(p.compiled); err != nil {
		return HostClaim{}, ports.SSHHost{}, err
	}
	if p.review.Hash != safetyReviewHash(p.review) {
		return HostClaim{}, ports.SSHHost{}, fmt.Errorf("claim review changed")
	}
	selected := false
	for _, h := range p.compiled.Spec.Hosts {
		selected = selected || h == host
	}
	observed, ok := reviewedHost(p.review.Hosts, host.ID)
	if !selected || !ok || observed.Facts.HostKey == nil || observed.Facts.HostKey.FingerprintSHA256 != host.HostKeySHA256 {
		return HostClaim{}, ports.SSHHost{}, fmt.Errorf("claim target differs from review")
	}
	claim := HostClaim{HostID: host.ID, MachineID: observed.Facts.MachineID, SessionID: p.sessionID, RunID: p.runID, InputHash: p.compiled.InputHash, ReviewHash: p.review.Hash, ReleaseSHA256: p.compiled.ReleaseSHA256}
	endpoint := sshHost(host.SSHAlias)
	endpoint.ExpectedHostKeySHA256 = host.HostKeySHA256
	return claim, endpoint, nil
}

func (p *HostClaims) command(ctx context.Context, host Host, action string, takeover bool) (HostClaim, error) {
	claim, endpoint, err := p.expected(host)
	if err != nil {
		return claim, err
	}
	command, err := hostClaimCommand(claim, action, takeover, "", hostClaimBase, 0)
	if err != nil {
		return claim, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	body, key, err := p.ssh.RunCappedWithHostKey(ctx, endpoint, command, 4096)
	if err != nil || key.FingerprintSHA256 != host.HostKeySHA256 {
		return claim, fmt.Errorf("host %s claim %s failed; inspect ownership before retry", host.ID, action)
	}
	got, err := parseHostClaim(body, host.ID)
	if err != nil || got.MachineID != claim.MachineID {
		return claim, fmt.Errorf("host claim evidence is invalid")
	}
	if action != "inspect" && (got.SessionID != claim.SessionID || got.RunID != claim.RunID || got.InputHash != claim.InputHash || got.ReviewHash != claim.ReviewHash || got.ReleaseSHA256 != claim.ReleaseSHA256) {
		return claim, fmt.Errorf("host claim owner changed")
	}
	return got, nil
}

func (p *HostClaims) Acquire(ctx context.Context, host Host, takeover bool) (HostClaim, error) {
	// Reserved claims have never admitted an effect. Started claims cannot be
	// taken over even when expired: all partial effects need reconciliation.
	if err := RecheckSafetyHost(ctx, p.compiled, p.review, host, p.ssh); err != nil {
		return HostClaim{}, err
	}
	return p.command(ctx, host, "acquire", takeover)
}
func (p *HostClaims) Inspect(ctx context.Context, host Host) (HostClaim, error) {
	return p.command(ctx, host, "inspect", false)
}
func (p *HostClaims) Check(ctx context.Context, host Host) (HostClaim, error) {
	return p.command(ctx, host, "check", false)
}
func (p *HostClaims) Renew(ctx context.Context, host Host) (HostClaim, error) {
	return p.command(ctx, host, "renew", false)
}
func (p *HostClaims) Release(ctx context.Context, host Host) (HostClaim, error) {
	return p.command(ctx, host, "release", false)
}

func quoteClaim(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

// Base and uid are private test seams. Production uses the fixed root-owned
// path and UID 0. The supervisor holds fd 9 while the actual child runs.
func hostClaimCommand(claim HostClaim, action string, takeover bool, payload, base string, uid int) (string, error) {
	for _, value := range []string{claim.SessionID, claim.RunID, claim.InputHash, claim.ReviewHash, claim.ReleaseSHA256} {
		if !validSessionHash(value) {
			return "", fmt.Errorf("invalid claim binding")
		}
	}
	if !validClaimMachine(claim.MachineID) {
		return "", fmt.Errorf("invalid claimed machine identity")
	}
	if action != "acquire" && action != "inspect" && action != "check" && action != "renew" && action != "release" && action != "execute" {
		return "", fmt.Errorf("unsupported claim action")
	}
	if action == "execute" && payload == "" {
		return "", fmt.Errorf("guarded command is empty")
	}
	script := `set -euo pipefail
umask 077
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
for tool in flock stat sync mktemp mv; do command -v "$tool" >/dev/null; done
test "$(cat /etc/machine-id)" = ` + quoteClaim(claim.MachineID) + `
base=` + quoteClaim(base) + `
root="$base/setup"
private_dir() { test ! -L "$1" && test -d "$1" && test "$(stat -c '%u:%a' "$1")" = '` + strconv.Itoa(uid) + `:700'; }
if [ ! -e "$base" ]; then
  test ` + quoteClaim(action) + ` = acquire
  mkdir -m 700 "$base" 2>/dev/null || test -d "$base"
fi
test ! -L "$base" && test -d "$base" && test "$(stat -c %u "$base")" = ` + strconv.Itoa(uid) + ` || exit 1
test "$(( 8#$(stat -c %a "$base") & 022 ))" = 0
if [ ! -e "$root" ]; then
  test ` + quoteClaim(action) + ` = acquire
  mkdir -m 700 "$root" 2>/dev/null || test -d "$root"
fi
private_dir "$root"
lock="$root/owner.lock"
record="$root/owner"
if [ ! -e "$lock" ]; then
  test ` + quoteClaim(action) + ` = acquire
  (set -o noclobber; : > "$lock") 2>/dev/null || test -f "$lock"
fi
test ! -L "$lock" && test -f "$lock" && test "$(stat -c '%u:%a:%h' "$lock")" = '` + strconv.Itoa(uid) + `:600:1' || exit 1
exec 9<> "$lock"
flock -x -n 9
test "$(stat -Lc '%d:%i' /proc/$$/fd/9)" = "$(stat -c '%d:%i' "$lock")"
boot=$(cat /proc/sys/kernel/random/boot_id)
read -r up _ < /proc/uptime
now=${up%%.*}
read_record() {
  test ! -L "$record" && test -f "$record" && test "$(stat -c '%u:%a:%h' "$record")" = '` + strconv.Itoa(uid) + `:600:1' || exit 1
  test "$(stat -c %s "$record")" -le 1024
  mapfile -t fields < "$record"
  test "${#fields[@]}" = 10 && test "${fields[0]}" = KDC_CLAIM_V1 || exit 1
  [[ ${fields[1]} =~ ^[a-f0-9]{32}$ ]] && [[ ${fields[2]} =~ ^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$ ]] || exit 1
  for index in 3 4 5 6 7; do [[ ${fields[$index]} =~ ^[a-f0-9]{64}$ ]]; done
  [[ ${fields[8]} =~ ^[0-9]{1,12}$ ]]
  [[ ${fields[9]} = reserved || ${fields[9]} = started || ${fields[9]} = released ]]
  test "${fields[1]}" = ` + quoteClaim(claim.MachineID) + `
}
write_record() {
  tmp=$(mktemp "$root/.owner.XXXXXX")
  printf '%s\n' "${fields[@]}" > "$tmp"
  chmod 600 "$tmp"
  sync -- "$tmp"
  mv -T "$tmp" "$record"
  sync -- "$root"
  sync -- "$base"
  sync -- "$base/.."
}
owned() {
  test "${fields[2]}" = "$boot"
  test "${fields[3]}" = ` + quoteClaim(claim.SessionID) + ` && test "${fields[4]}" = ` + quoteClaim(claim.RunID) + ` || exit 1
  test "${fields[5]}" = ` + quoteClaim(claim.InputHash) + ` && test "${fields[6]}" = ` + quoteClaim(claim.ReviewHash) + ` && test "${fields[7]}" = ` + quoteClaim(claim.ReleaseSHA256) + ` || exit 1
}
live() { test "$now" -lt "${fields[8]}" && test "${fields[8]}" -le "$((now+3600))"; }
if [ ` + quoteClaim(action) + ` = acquire ]; then
  available=true
  if [ -e "$record" ] || [ -L "$record" ]; then
    read_record
    if [ "${fields[9]}" != released ]; then
      available=false
      if [ ` + quoteClaim(strconv.FormatBool(takeover)) + ` = true ] && [ "${fields[9]}" = reserved ] && { [ "${fields[2]}" != "$boot" ] || [ "${fields[8]}" -le "$now" ]; }; then available=true; fi
    fi
  fi
  test "$available" = true
  fields=(KDC_CLAIM_V1 ` + quoteClaim(claim.MachineID) + ` "$boot" ` + quoteClaim(claim.SessionID) + ` ` + quoteClaim(claim.RunID) + ` ` + quoteClaim(claim.InputHash) + ` ` + quoteClaim(claim.ReviewHash) + ` ` + quoteClaim(claim.ReleaseSHA256) + ` "$((now+` + strconv.FormatInt(int64(hostClaimTTL/time.Second), 10) + `))" reserved)
  write_record
else
  read_record
  if [ ` + quoteClaim(action) + ` != inspect ]; then
    owned
    test "${fields[9]}" != released
    if [ ` + quoteClaim(action) + ` = release ]; then
      test "${fields[9]}" = reserved
      fields[9]=released; write_record
    else
      live
      if [ ` + quoteClaim(action) + ` = renew ]; then fields[8]=$((now+` + strconv.FormatInt(int64(hostClaimTTL/time.Second), 10) + `)); write_record; fi
      if [ ` + quoteClaim(action) + ` = execute ]; then
        fields[9]=started; write_record
        trap '' HUP INT TERM
        bash -o pipefail -c ` + quoteClaim(payload) + ` <&0 &
        child=$!
        result=0; wait "$child" || result=$?
        exit "$result"
      fi
    fi
  fi
fi
printf '%s\n' "${fields[@]}" "$now" "$boot"`
	return "sudo -n bash -o pipefail -c " + quoteClaim(script), nil
}

func parseHostClaim(body []byte, host string) (HostClaim, error) {
	var c HostClaim
	fields := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if len(fields) != 12 || fields[0] != "KDC_CLAIM_V1" {
		return c, fmt.Errorf("invalid claim record")
	}
	c = HostClaim{HostID: host, MachineID: fields[1], BootID: fields[2], SessionID: fields[3], RunID: fields[4], InputHash: fields[5], ReviewHash: fields[6], ReleaseSHA256: fields[7], State: fields[9], ObservedBootID: fields[11]}
	var err error
	c.ExpiresTick, err = strconv.ParseInt(fields[8], 10, 64)
	if err != nil || c.ExpiresTick < 0 {
		return c, fmt.Errorf("invalid claim expiry")
	}
	c.ObservedTick, err = strconv.ParseInt(fields[10], 10, 64)
	if err != nil || c.ObservedTick < 0 {
		return c, fmt.Errorf("invalid claim time")
	}
	for _, hash := range []string{c.SessionID, c.RunID, c.InputHash, c.ReviewHash, c.ReleaseSHA256} {
		if !validSessionHash(hash) {
			return c, fmt.Errorf("invalid claim identity")
		}
	}
	if !validClaimMachine(c.MachineID) || (!claimBootPattern.MatchString(c.BootID) || !claimBootPattern.MatchString(c.ObservedBootID)) || (c.State != "reserved" && c.State != "started" && c.State != "released") {
		return c, fmt.Errorf("invalid claim state")
	}
	return c, nil
}

// claimedSSH fences *all* Run/Put calls, including staging and cleanup, while
// bounded inventory reads use the original pinned transport.
type claimedSSH struct {
	*HostClaims
	mu     sync.Mutex
	failed map[ports.SSHHost]bool
}

func (p *HostClaims) GuardedSSH() ports.CappedSSHHostKeyClient {
	return &claimedSSH{HostClaims: p, failed: map[ports.SSHHost]bool{}}
}
func (s *claimedSSH) binding(host ports.SSHHost) (HostClaim, error) {
	s.mu.Lock()
	failed := s.failed[host]
	s.mu.Unlock()
	if failed {
		return HostClaim{}, fmt.Errorf("remote outcome is uncertain; further operations are blocked")
	}
	for _, h := range s.compiled.Spec.Hosts {
		_, endpoint, err := s.expected(h)
		if err == nil && endpoint == host {
			claim, _, err := s.expected(h)
			return claim, err
		}
	}
	return HostClaim{}, fmt.Errorf("write endpoint is not claimed")
}
func (s *claimedSSH) Run(ctx context.Context, host ports.SSHHost, command string) ([]byte, error) {
	claim, err := s.binding(host)
	if err != nil {
		return nil, err
	}
	guarded, err := hostClaimCommand(claim, "execute", false, command, hostClaimBase, 0)
	if err != nil {
		return nil, err
	}
	body, err := s.ssh.Run(ctx, host, guarded)
	if err != nil {
		s.fail(host)
		return nil, fmt.Errorf("claimed remote operation failed; inspect its outcome")
	}
	return body, nil
}
func (s *claimedSSH) Put(ctx context.Context, host ports.SSHHost, path string, body []byte, mode uint32) error {
	claim, err := s.binding(host)
	if err != nil {
		return err
	}
	err = s.ssh.PutGuarded(ctx, host, path, body, mode, func(command string) (string, error) {
		return hostClaimCommand(claim, "execute", false, command, hostClaimBase, 0)
	})
	if err != nil {
		s.fail(host)
		return fmt.Errorf("claimed upload failed; inspect its outcome")
	}
	return nil
}
func (s *claimedSSH) Fetch(ctx context.Context, h ports.SSHHost, p string) ([]byte, error) {
	if _, err := s.binding(h); err != nil {
		return nil, err
	}
	return s.ssh.Fetch(ctx, h, p)
}
func (s *claimedSSH) RunCapped(ctx context.Context, h ports.SSHHost, c string, n int) ([]byte, error) {
	if _, err := s.binding(h); err != nil {
		return nil, err
	}
	return s.ssh.RunCapped(ctx, h, c, n)
}
func (s *claimedSSH) RunCappedWithHostKey(ctx context.Context, h ports.SSHHost, c string, n int) ([]byte, ports.SSHHostKeyEvidence, error) {
	if _, err := s.binding(h); err != nil {
		return nil, ports.SSHHostKeyEvidence{}, err
	}
	return s.ssh.RunCappedWithHostKey(ctx, h, c, n)
}

func (s *claimedSSH) fail(host ports.SSHHost) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failed[host] = true
}
