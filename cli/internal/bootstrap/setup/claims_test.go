package setup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func claimFixture(t *testing.T) (HostClaim, string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Linux host protocol")
	}
	for _, tool := range []string{"bash", "flock", "sync"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(err)
		}
	}
	machine, err := os.ReadFile("/etc/machine-id")
	if err != nil {
		t.Skip(err)
	}
	return HostClaim{HostID: "server-1", MachineID: strings.TrimSpace(string(machine)), SessionID: strings.Repeat("1", 64), RunID: strings.Repeat("2", 64), InputHash: strings.Repeat("3", 64), ReviewHash: strings.Repeat("4", 64), ReleaseSHA256: strings.Repeat("5", 64)}, filepath.Join(t.TempDir(), "claims")
}
func localClaimCommand(t *testing.T, c HostClaim, action string, takeover bool, payload, base string) *exec.Cmd {
	t.Helper()
	script, err := hostClaimCommand(c, action, takeover, payload, base, os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return exec.CommandContext(ctx, "bash", "-c", strings.TrimPrefix(script, "sudo -n "))
}
func runClaim(t *testing.T, c HostClaim, action string, takeover bool, payload, base string) ([]byte, error) {
	t.Helper()
	return localClaimCommand(t, c, action, takeover, payload, base).CombinedOutput()
}
func requireClaim(t *testing.T, c HostClaim, action string, takeover bool, base string) HostClaim {
	t.Helper()
	body, err := runClaim(t, c, action, takeover, "", base)
	if err != nil {
		t.Fatalf("%s: %v %s", action, err, body)
	}
	got, err := parseHostClaim(body, c.HostID)
	if err != nil {
		t.Fatalf("parse %q: %v", body, err)
	}
	return got
}
func changeClaim(t *testing.T, base string, change func([]string)) {
	t.Helper()
	path := filepath.Join(base, "setup", "owner")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	change(fields)
	if err := os.WriteFile(path, []byte(strings.Join(fields, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestClaimsExcludeOtherRunAndRequireExplicitTakeover(t *testing.T) {
	c, base := claimFixture(t)
	got := requireClaim(t, c, "acquire", false, base)
	if got.State != "reserved" || got.ExpiresTick-got.ObservedTick != 1800 {
		t.Fatalf("%+v", got)
	}
	other := c
	other.RunID = strings.Repeat("a", 64)
	for _, action := range []string{"acquire", "check", "renew", "release", "execute"} {
		payload := ""
		if action == "execute" {
			payload = "touch " + quoteClaim(filepath.Join(base, "escaped"))
		}
		if _, err := runClaim(t, other, action, false, payload, base); err == nil {
			t.Fatalf("other run accepted %s", action)
		}
	}
	changeClaim(t, base, func(f []string) { f[8] = "0" })
	for _, action := range []string{"check", "renew"} {
		if _, err := runClaim(t, c, action, false, "", base); err == nil {
			t.Fatalf("expired %s", action)
		}
	}
	if _, err := runClaim(t, other, "acquire", false, "", base); err == nil {
		t.Fatal("implicit takeover")
	}
	requireClaim(t, other, "acquire", true, base)
	if _, err := runClaim(t, c, "release", false, "", base); err == nil {
		t.Fatal("stale cleanup released new run")
	}
	requireClaim(t, other, "release", false, base)
	requireClaim(t, c, "acquire", false, base)
}
func TestStartedClaimsNeverExpireIntoTakeover(t *testing.T) {
	c, base := claimFixture(t)
	requireClaim(t, c, "acquire", false, base)
	cmd := localClaimCommand(t, c, "execute", false, "cat", base)
	cmd.Stdin = strings.NewReader("upload\x00body\n")
	body, err := cmd.Output()
	if err != nil || string(body) != "upload\x00body\n" {
		t.Fatalf("stdin damaged %q %v", body, err)
	}
	got := requireClaim(t, c, "inspect", false, base)
	if got.State != "started" {
		t.Fatal(got)
	}
	changeClaim(t, base, func(f []string) { f[8] = "0" })
	if _, err := runClaim(t, c, "release", false, "", base); err == nil {
		t.Fatal("started released")
	}
	other := c
	other.RunID = strings.Repeat("a", 64)
	if _, err := runClaim(t, other, "acquire", true, "", base); err == nil {
		t.Fatal("started taken over")
	}
	if _, err := runClaim(t, c, "execute", false, "touch "+quoteClaim(filepath.Join(base, "escape")), base); err == nil {
		t.Fatal("expired write")
	}
	if _, err := os.Stat(filepath.Join(base, "escape")); !os.IsNotExist(err) {
		t.Fatal("payload ran")
	}
}
func TestClaimChildKeepsLock(t *testing.T) {
	c, base := claimFixture(t)
	requireClaim(t, c, "acquire", false, base)
	marker := filepath.Join(base, "child-running")
	cmd := localClaimCommand(t, c, "execute", false, "touch "+quoteClaim(marker)+"; sleep 1", base)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := runClaim(t, c, "renew", false, "", base); err == nil {
		t.Fatal("renew escaped child lock")
	}
	other := c
	other.RunID = strings.Repeat("a", 64)
	if _, err := runClaim(t, other, "acquire", true, "", base); err == nil {
		t.Fatal("parallel owner escaped child lock")
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	requireClaim(t, c, "renew", false, base)
}
func TestClaimConcurrentAcquisition(t *testing.T) {
	c, base := claimFixture(t)
	other := c
	other.RunID = strings.Repeat("a", 64)
	a, b := localClaimCommand(t, c, "acquire", false, "", base), localClaimCommand(t, other, "acquire", false, "", base)
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	ea, eb := a.Wait(), b.Wait()
	if (ea == nil) == (eb == nil) {
		t.Fatalf("expected exactly one owner: %v %v", ea, eb)
	}
}
func TestClaimsRejectChangedBootAndClock(t *testing.T) {
	c, base := claimFixture(t)
	got := requireClaim(t, c, "acquire", false, base)
	changeClaim(t, base, func(f []string) { f[8] = strconv.FormatInt(got.ObservedTick+7200, 10) })
	if _, err := runClaim(t, c, "check", false, "", base); err == nil {
		t.Fatal("clock regression accepted")
	}
	changeClaim(t, base, func(f []string) { f[2] = "00000000-0000-0000-0000-000000000000" })
	if _, err := runClaim(t, c, "check", false, "", base); err == nil {
		t.Fatal("changed boot accepted")
	}
	other := c
	other.RunID = strings.Repeat("a", 64)
	requireClaim(t, other, "acquire", true, base)
}
func TestClaimsRejectUnsafePathsAndWrongMachine(t *testing.T) {
	for _, kind := range []string{"base-symlink", "root-mode", "lock-symlink", "record-symlink", "record-hardlink", "wrong-machine"} {
		t.Run(kind, func(t *testing.T) {
			c, base := claimFixture(t)
			if kind == "wrong-machine" {
				c.MachineID = strings.Repeat("0", 32)
				if _, err := runClaim(t, c, "acquire", false, "", base); err == nil {
					t.Fatal("wrong identity accepted")
				}
				if _, err := os.Lstat(base); !os.IsNotExist(err) {
					t.Fatal("wrote before identity check")
				}
				return
			}
			requireClaim(t, c, "acquire", false, base)
			switch kind {
			case "base-symlink":
				os.Rename(base, base+"-old")
				os.Symlink(base+"-old", base)
			case "root-mode":
				os.Chmod(filepath.Join(base, "setup"), 0755)
			case "lock-symlink", "record-symlink":
				name := "owner"
				if kind == "lock-symlink" {
					name = "owner.lock"
				}
				p := filepath.Join(base, "setup", name)
				os.Rename(p, p+"-old")
				os.Symlink(p+"-old", p)
			case "record-hardlink":
				os.Link(filepath.Join(base, "setup", "owner"), filepath.Join(base, "copy"))
			}
			if _, err := runClaim(t, c, "check", false, "", base); err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
}

func TestClaimSupervisorRetainsLockAfterChildClosesDescriptors(t *testing.T) {
	for _, nestedSudo := range []bool{false, true} {
		t.Run(strconv.FormatBool(nestedSudo), func(t *testing.T) {
			if nestedSudo {
				if err := exec.Command("sudo", "-n", "true").Run(); err != nil {
					t.Skip("noninteractive sudo unavailable")
				}
			}
			c, base := claimFixture(t)
			requireClaim(t, c, "acquire", false, base)
			marker := filepath.Join(base, "running")
			payload := "exec 9>&-; touch " + quoteClaim(marker) + "; sleep 1"
			if nestedSudo {
				payload = "sudo -n bash -c " + quoteClaim(payload)
			}
			cmd := localClaimCommand(t, c, "execute", false, payload, base)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Wait() })
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, err := os.Stat(marker); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("child did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, err := runClaim(t, c, "renew", false, "", base); err == nil {
				t.Fatal("child descriptor close released supervisor lock")
			}
			if err := cmd.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestClaimMalformedRecordCannotAdmitPayload(t *testing.T) {
	for _, kind := range []string{"extra-line", "wrong-magic", "wrong-run", "wrong-input", "wrong-review", "wrong-release"} {
		t.Run(kind, func(t *testing.T) {
			c, base := claimFixture(t)
			requireClaim(t, c, "acquire", false, base)
			changeClaim(t, base, func(f []string) {
				switch kind {
				case "wrong-magic":
					f[0] = "OTHER"
				case "wrong-run":
					f[4] = strings.Repeat("a", 64)
				case "wrong-input":
					f[5] = strings.Repeat("a", 64)
				case "wrong-review":
					f[6] = strings.Repeat("a", 64)
				case "wrong-release":
					f[7] = strings.Repeat("a", 64)
				}
			})
			if kind == "extra-line" {
				f, err := os.OpenFile(filepath.Join(base, "setup", "owner"), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteString("extra\n")
				if err != nil {
					t.Fatal(err)
				}
				f.Close()
			}
			marker := filepath.Join(base, "escaped")
			if _, err := runClaim(t, c, "execute", false, "touch "+quoteClaim(marker), base); err == nil {
				t.Fatal("malformed record accepted")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("payload ran")
			}
		})
	}
}
