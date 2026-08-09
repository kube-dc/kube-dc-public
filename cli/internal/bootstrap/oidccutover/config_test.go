package oidccutover

import (
	"strings"
	"testing"
)

// The fresh-install shape: `bootstrap install` writes no kube-apiserver-arg at
// all, so the block has to be created.
func TestPatchConfig_CreatesBlockOnAFreshInstall(t *testing.T) {
	body := "cni: none\ncluster-cidr: \"10.1.0.0/16\"\nkube-scheduler-arg:\n  - bind-address=0.0.0.0\n"
	out, changed, err := PatchConfig(body)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !IsCutOver(out) {
		t.Error("patched config must report as cut over")
	}
	if strings.Count(out, apiserverArgKey+":") != 1 {
		t.Errorf("exactly one %s block expected:\n%s", apiserverArgKey, out)
	}
	for _, flag := range ManagedFlags() {
		if !strings.Contains(out, "- "+flag) {
			t.Errorf("missing %q:\n%s", flag, out)
		}
	}
	// Pre-existing keys must survive untouched.
	for _, keep := range []string{"cni: none", "kube-scheduler-arg:", "- bind-address=0.0.0.0"} {
		if !strings.Contains(out, keep) {
			t.Errorf("patch dropped %q:\n%s", keep, out)
		}
	}
}

// THE HAZARD the runbook's `tee -a` carries: appending a second
// kube-apiserver-arg block when one exists yields duplicate YAML keys, and RKE2
// then honours one and silently discards the other. Merge into the existing
// block instead, preserving what is already there.
func TestPatchConfig_MergesIntoAnExistingBlockWithoutDuplicatingTheKey(t *testing.T) {
	body := strings.Join([]string{
		"kube-apiserver-arg:",
		"  - audit-log-path=/var/log/audit.log",
		"  - audit-log-maxage=30",
		"kube-controller-manager-arg:",
		"  - bind-address=0.0.0.0",
		"",
	}, "\n")
	out, changed, err := PatchConfig(body)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if got := strings.Count(out, apiserverArgKey+":"); got != 1 {
		t.Fatalf("the key must appear ONCE, got %d:\n%s", got, out)
	}
	// The operator's own apiserver flags must still be in the block.
	for _, keep := range []string{"- audit-log-path=/var/log/audit.log", "- audit-log-maxage=30"} {
		if !strings.Contains(out, keep) {
			t.Errorf("merge dropped a pre-existing apiserver flag %q:\n%s", keep, out)
		}
	}
	// And the new flags must be INSIDE that block, i.e. before the next
	// top-level key.
	nextKey := strings.Index(out, "kube-controller-manager-arg:")
	for _, flag := range ManagedFlags() {
		at := strings.Index(out, flag)
		if at < 0 || at > nextKey {
			t.Errorf("%q must land inside the apiserver block, before the next top-level key:\n%s", flag, out)
		}
	}
}

// Idempotence: a re-run must not restart an apiserver for nothing.
func TestPatchConfig_IsANoOpWhenAlreadyDone(t *testing.T) {
	body := "kube-apiserver-arg:\n  - " + webhookFlag + "\n  - " + cacheTTLFlag + "\n"
	out, changed, err := PatchConfig(body)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Errorf("an already-cut-over config must report changed=false, got:\n%s", out)
	}
	if out != body {
		t.Errorf("no-op must return the input verbatim:\n%s", out)
	}
}

// A half-done node (webhook flag present, TTL missing) must gain only what it
// lacks — not a second copy of what it has.
func TestPatchConfig_AddsOnlyTheMissingFlag(t *testing.T) {
	body := "kube-apiserver-arg:\n  - " + webhookFlag + "\n"
	out, changed, err := PatchConfig(body)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if got := strings.Count(out, webhookFlag); got != 1 {
		t.Errorf("webhook flag duplicated %d times:\n%s", got, out)
	}
	if !strings.Contains(out, cacheTTLFlag) {
		t.Errorf("missing TTL flag:\n%s", out)
	}
}

// The retired structured-authn flag must go in the SAME edit. Leaving both
// configured means tokens keep resolving through a file that no longer tracks
// the Organization CRs.
func TestPatchConfig_DropsTheRetiredAuthConfigFlag(t *testing.T) {
	body := strings.Join([]string{
		"kube-apiserver-arg:",
		"  - authentication-config=/etc/rancher/auth-conf.yaml",
		"  - audit-log-maxage=30",
		"",
	}, "\n")
	out, changed, err := PatchConfig(body)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if strings.Contains(out, "authentication-config=") {
		t.Errorf("the retired flag must be removed:\n%s", out)
	}
	if !strings.Contains(out, "- audit-log-maxage=30") {
		t.Errorf("removal must not take neighbouring flags with it:\n%s", out)
	}
	if !IsCutOver(out) {
		t.Error("must still be cut over")
	}
}

// Removing the legacy flag is itself a change even when both managed flags are
// already present — otherwise the node would be reported done while still
// resolving tokens through the retired path.
func TestPatchConfig_LegacyRemovalAloneCountsAsAChange(t *testing.T) {
	body := "kube-apiserver-arg:\n  - authentication-config=/etc/rancher/auth-conf.yaml\n  - " +
		webhookFlag + "\n  - " + cacheTTLFlag + "\n"
	out, changed, err := PatchConfig(body)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("dropping the retired flag must count as a change (it needs a restart)")
	}
	if strings.Contains(out, "authentication-config=") {
		t.Errorf("retired flag survived:\n%s", out)
	}
}

// An already-broken file (two top-level blocks from a previous blind append)
// must be refused, not "fixed" by guessing which block is authoritative.
func TestPatchConfig_RefusesADuplicatedKey(t *testing.T) {
	body := "kube-apiserver-arg:\n  - a=1\nfoo: bar\nkube-apiserver-arg:\n  - b=2\n"
	if _, _, err := PatchConfig(body); err == nil {
		t.Fatal("a config with two top-level kube-apiserver-arg keys must be refused")
	} else if !strings.Contains(err.Error(), "twice") {
		t.Errorf("the error should say the key is declared twice, got: %v", err)
	}
}

// A NESTED key of the same name is not the apiserver's flag list. Injecting
// flags there would corrupt an unrelated structure and leave the apiserver
// cert-only.
func TestPatchConfig_IgnoresANestedKeyOfTheSameName(t *testing.T) {
	body := "some-tool:\n  kube-apiserver-arg:\n    - not-ours=1\n"
	out, changed, err := PatchConfig(body)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.Contains(out, "    - not-ours=1") {
		t.Errorf("the nested structure must be untouched:\n%s", out)
	}
	// A real top-level block must have been created.
	lines := strings.Split(out, "\n")
	found := false
	for _, l := range lines {
		if l == apiserverArgKey+":" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a NEW top-level block:\n%s", out)
	}
}

// A commented-out flag must never read as done — that would skip a node that is
// still cert-only, producing exactly the intermittent-401 split this command
// exists to prevent.
func TestIsCutOver_IgnoresComments(t *testing.T) {
	if IsCutOver("# kube-apiserver-arg:\n#   - " + webhookFlag + "\n") {
		t.Error("a commented-out flag must not count as cut over")
	}
	if IsCutOver("kube-apiserver-arg:\n  - audit-log-maxage=30  # " + webhookFlag + "\n") {
		t.Error("the flag inside a trailing comment must not count as cut over")
	}
	if !IsCutOver("kube-apiserver-arg:\n  - " + webhookFlag + "  # wired 2026-08-07\n") {
		t.Error("a live flag with a trailing comment must count as cut over")
	}
}

// The file's own list style is preserved (4-space item indent stays 4-space).
func TestPatchConfig_KeepsTheFilesIndentStyle(t *testing.T) {
	body := "kube-apiserver-arg:\n    - audit-log-maxage=30\n"
	out, _, err := PatchConfig(body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "    - "+webhookFlag) {
		t.Errorf("expected 4-space item indent to be reused:\n%s", out)
	}
}

// Comments must survive: this file ships with substantial explanatory prose and
// a YAML round-trip would drop all of it.
func TestPatchConfig_PreservesComments(t *testing.T) {
	body := strings.Join([]string{
		"# Memory reservation protects kubelet/containerd/etcd from kernel OOM.",
		"kubelet-arg:",
		"  - system-reserved=memory=2Gi",
		"# cni is none because kube-ovn is installed later by Flux",
		"cni: none",
		"",
	}, "\n")
	out, _, err := PatchConfig(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, comment := range []string{
		"# Memory reservation protects kubelet/containerd/etcd from kernel OOM.",
		"# cni is none because kube-ovn is installed later by Flux",
	} {
		if !strings.Contains(out, comment) {
			t.Errorf("comment lost: %q\n%s", comment, out)
		}
	}
}

// The REAL shape RKE2 writes, captured from a live production control-plane
// node: the sequence is flush with its parent key. This must read as
// already-wired, not as an empty list needing both flags appended again.
func TestPatchConfig_HandlesRKE2sOwnFlushSequenceStyle(t *testing.T) {
	body := strings.Join([]string{
		"cluster-dns: 10.101.0.11",
		"cni: none",
		"kube-apiserver-arg:",
		"- " + webhookFlag,
		"- " + cacheTTLFlag,
		"kubelet-arg:",
		"- eviction-hard=memory.available<500Mi,nodefs.available<10%",
		"node-ip: 192.168.0.11",
		"",
	}, "\n")
	out, changed, err := PatchConfig(body)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Errorf("an already-wired node in RKE2's own style must be a no-op, got:\n%s", out)
	}
	if got := strings.Count(out, webhookFlag); got != 1 {
		t.Errorf("flag must not be duplicated (count=%d):\n%s", got, out)
	}
}

// Same flush style, but genuinely missing the flags: they must be inserted
// INSIDE the block (before the next top-level key) and match its indent style.
func TestPatchConfig_InsertsIntoAFlushSequenceBlock(t *testing.T) {
	body := strings.Join([]string{
		"kube-apiserver-arg:",
		"- audit-log-maxage=30",
		"kubelet-arg:",
		"- eviction-hard=memory.available<500Mi",
		"",
	}, "\n")
	out, changed, err := PatchConfig(body)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if strings.Count(out, apiserverArgKey+":") != 1 {
		t.Errorf("must not create a second block:\n%s", out)
	}
	nextKey := strings.Index(out, "kubelet-arg:")
	for _, flag := range ManagedFlags() {
		at := strings.Index(out, flag)
		if at < 0 || at > nextKey {
			t.Errorf("%q must be inserted inside the apiserver block:\n%s", flag, out)
		}
		// Flush style, so no leading spaces on the inserted item.
		if !strings.Contains(out, "\n- "+flag) {
			t.Errorf("inserted item must match the file's flush style:\n%s", out)
		}
	}
	if !strings.Contains(out, "- audit-log-maxage=30") {
		t.Errorf("pre-existing flag lost:\n%s", out)
	}
}

// A line transform can only safely edit a BLOCK sequence. Anything with a value
// on the key's own line — an inline sequence, a flow mapping, an anchor — must
// be refused, because inserting "- " items after a scalar produces YAML that
// does not parse and the node then fails to start.
func TestPatchConfig_RefusesNonBlockSequenceForms(t *testing.T) {
	for _, body := range []string{
		"kube-apiserver-arg: [audit-log-maxage=30]\n",
		"kube-apiserver-arg: {a: b}\n",
		"kube-apiserver-arg: &anchor\n- a=1\n",
	} {
		_, _, err := PatchConfig(body)
		if err == nil {
			t.Errorf("must refuse a non-block form rather than corrupt it: %q", body)
			continue
		}
		if !strings.Contains(err.Error(), "block sequence") {
			t.Errorf("the refusal must explain the requirement, got: %v", err)
		}
	}
}

// A QUOTED key is the same key. Not recognising it meant appending a second
// mapping key, which RKE2 resolves by silently discarding one block.
func TestPatchConfig_RecognisesAQuotedKey(t *testing.T) {
	body := "\"kube-apiserver-arg\":\n  - audit-log-maxage=30\n"
	out, changed, err := PatchConfig(body)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	// Exactly one key, counting both spellings.
	if n := strings.Count(out, apiserverArgKey); n != 1 {
		t.Errorf("the key must appear once, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "- audit-log-maxage=30") {
		t.Errorf("pre-existing flag lost:\n%s", out)
	}
}
