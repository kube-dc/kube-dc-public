package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startedHandoffClaim(t *testing.T) (HostClaim, string) {
	t.Helper()
	c, base := claimFixture(t)
	requireClaim(t, c, "acquire", false, base)
	if body, err := runClaim(t, c, "execute", false, "true", base); err != nil {
		t.Fatalf("start: %s %v", body, err)
	}
	c = requireClaim(t, c, "inspect", false, base)
	c.HandoffID, c.SourceRunID = strings.Repeat("a", 64), c.RunID
	return c, base
}

func TestClaimHandoffRetainsExclusionAndRejectsOldPayloads(t *testing.T) {
	c, base := startedHandoffClaim(t)
	parked := requireClaim(t, c, "park", false, base)
	if parked.State != "handoff" || parked.HandoffID != c.HandoffID || parked.SourceRunID != c.RunID {
		t.Fatal(parked)
	}
	for _, action := range []string{"execute", "renew", "check", "release", "acquire"} {
		payload := ""
		if action == "execute" {
			payload = "touch " + quoteClaim(filepath.Join(base, "escaped"))
		}
		if _, err := runClaim(t, c, action, true, payload, base); err == nil {
			t.Fatalf("parked predecessor accepted %s", action)
		}
	}
	other := c
	other.RunID = strings.Repeat("b", 64)
	other.HandoffEpoch = 1
	pending := requireClaim(t, other, "transfer", false, base)
	if pending.State != "handoff" || pending.RunID != other.RunID {
		t.Fatal(pending)
	}
	c.HandoffEpoch = 1
	if _, err := runClaim(t, c, "activate", false, "", base); err == nil {
		t.Fatal("predecessor activated recipient controls")
	}
	active := requireClaim(t, other, "activate", false, base)
	if active.State != "started" || active.HandoffID != "" || active.RunID != other.RunID {
		t.Fatal(active)
	}
	for _, action := range []string{"park", "transfer", "activate"} {
		if _, err := runClaim(t, c, action, false, "", base); err == nil {
			t.Fatalf("consumed handoff accepted %s", action)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "escaped")); !os.IsNotExist(err) {
		t.Fatal("predecessor payload escaped")
	}
}

func TestClaimHandoffCannotUpgradeLegacyOrForgetFailedExecutor(t *testing.T) {
	for _, kind := range []string{"legacy", "failed"} {
		t.Run(kind, func(t *testing.T) {
			c, base := claimFixture(t)
			requireClaim(t, c, "acquire", false, base)
			if kind == "legacy" {
				path := filepath.Join(base, "setup", "owner")
				body, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				fields := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")[:10]
				fields[0] = "KDC_CLAIM_V1"
				if err := os.WriteFile(path, []byte(strings.Join(fields, "\n")+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if body, err := runClaim(t, c, "execute", false, "true", base); err != nil {
					t.Fatalf("legacy execute: %s %v", body, err)
				}
			} else {
				if _, err := runClaim(t, c, "execute", false, "exit 1", base); err == nil {
					t.Fatal("failure ignored")
				}
			}
			c = requireClaim(t, c, "inspect", false, base)
			c.HandoffID, c.SourceRunID = strings.Repeat("a", 64), c.RunID
			if _, err := runClaim(t, c, "park", false, "", base); err == nil {
				t.Fatal("unproven executor became transferable")
			}
			if kind == "failed" {
				if _, err := runClaim(t, c, "execute", false, "true", base); err == nil {
					t.Fatal("failed executor fence cleared by a later command")
				}
			}
		})
	}
}

func TestClaimSystemdContainsDetachedWriter(t *testing.T) {
	if os.Getenv("KUBE_DC_TEST_SYSTEMD") != "1" {
		t.Skip("set KUBE_DC_TEST_SYSTEMD=1 for the local Linux cgroup smoke")
	}
	if err := exec.Command("sudo", "-n", "systemctl", "is-system-running").Run(); err != nil {
		// Degraded is a running manager too. A read-only version call verifies
		// noninteractive privilege without assuming other local services healthy.
		if err := exec.Command("sudo", "-n", "systemctl", "show", "--property=Version").Run(); err != nil {
			t.Fatal("local systemd manager unavailable")
		}
	}
	c, base := claimFixture(t)
	requireClaim(t, c, "acquire", false, base)
	escape := filepath.Join(base, "detached-write")
	payload := "setsid bash -c " + quoteClaim("sleep 1; touch "+quoteClaim(escape)) + " </dev/null >/dev/null 2>&1 &\nsleep 0.1\n"
	if body, err := runClaim(t, c, "execute", false, payload, base); err != nil {
		t.Fatalf("contained command: %v %s", err, body)
	}
	got := requireClaim(t, c, "inspect", false, base)
	if got.ExecutorProtocol != "systemd-v1" || got.ExecutorUnit == "" {
		t.Fatal("no durable contained terminal result")
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(escape); !os.IsNotExist(err) {
		t.Fatal("setsid descendant retained write authority after successful command")
	}
	got.HandoffID, got.SourceRunID = strings.Repeat("a", 64), got.RunID
	requireClaim(t, got, "park", false, base)
}

func TestClaimSystemdTimeoutCannotEstablishQuiescence(t *testing.T) {
	if os.Getenv("KUBE_DC_TEST_SYSTEMD") != "1" {
		t.Skip("set KUBE_DC_TEST_SYSTEMD=1 for the local Linux cgroup smoke")
	}
	c, base := claimFixture(t)
	requireClaim(t, c, "acquire", false, base)
	// A detached child which ignores TERM forces systemd's stop timeout. Even
	// after SIGKILL empties the group, failure is never positive handoff proof.
	payload := "setsid bash -c 'trap \"\" TERM; sleep 30' </dev/null >/dev/null 2>&1 &\nsleep 0.1\n"
	if body, err := runClaim(t, c, "execute", false, payload, base); err == nil {
		t.Fatalf("stop timeout became successful evidence: %s", body)
	}
	got := requireClaim(t, c, "inspect", false, base)
	if got.ExecutorProtocol != "systemd-running" {
		t.Fatal("timeout cleared executor fence")
	}
	got.HandoffID, got.SourceRunID = strings.Repeat("a", 64), got.RunID
	if _, err := runClaim(t, got, "park", false, "", base); err == nil {
		t.Fatal("timeout established handoff authority")
	}
}

func TestClaimHandoffEpochRejectsDelayedContenders(t *testing.T) {
	c, base := startedHandoffClaim(t)
	requireClaim(t, c, "park", false, base)
	loser := c
	loser.RunID = strings.Repeat("b", 64)
	loser.HandoffEpoch = 1
	requireClaim(t, loser, "transfer", false, base)
	winner := c
	winner.RunID = strings.Repeat("c", 64)
	winner.HandoffEpoch = 2
	requireClaim(t, winner, "transfer", false, base)
	for _, action := range []string{"transfer", "activate"} {
		if _, err := runClaim(t, loser, action, false, "", base); err == nil {
			t.Fatalf("delayed contender accepted %s", action)
		}
	}
	requireClaim(t, winner, "activate", false, base)
}
