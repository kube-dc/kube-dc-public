package rke2

import (
	"strings"
	"testing"
)

// The embedded install-server.sh is a sanitized copy of the fleet's
// kube-dc-fleet/bootstrap/rke2/install-server.sh. This guard pins the
// invariants the Go engine depends on, so a bad re-sync fails the build.
//
// Real-infra token scanning is NOT duplicated here — the embedded script
// lives under cli/ on the public-mirror surface, so internal/lint's
// TestNoRealInfraReferences already scans it in the same `go test`
// gate. (Re-scanning here would also force the banned tokens to appear
// as literals in this file, which that very lint would then flag.)

func TestEmbeddedInstaller_EnvContractAndInvariants(t *testing.T) {
	s := string(installServerScript)
	if len(s) == 0 {
		t.Fatal("install-server.sh did not embed")
	}
	// Env-var interface the Go engine feeds (buildInstallEnv).
	for _, key := range []string{"RKE2_VERSION", "NODE_NAME", "NODE_IP", "EXTERNAL_IP", "DOMAIN", "POD_CIDR", "SERVICE_CIDR", "CLUSTER_DNS", "EMBEDDED_REGISTRY", "registries_has_mirror", "--embedded-registry=false"} {
		if !strings.Contains(s, key) {
			t.Errorf("installer no longer references env var %q — Go engine would pass an ignored value", key)
		}
	}
	// Config invariants the whole install depends on (findings 12/13).
	for _, inv := range []string{"cni: none", "advertise-address", "max-pods", "cluster-cidr", "service-cidr", "tls-san"} {
		if !strings.Contains(s, inv) {
			t.Errorf("installer missing required invariant %q", inv)
		}
	}
}

// TestEmbeddedInstaller_RKE2VersionMatchesConst guards the RKE2 version pin,
// which lives in THREE places that must agree: the Go const defaultRKE2Version
// and the RKE2_VERSION default in each embedded shell script. A bump that
// touches one but not the others silently installs a mixed/wrong RKE2 version —
// exactly the drift that made the v1.35→v1.36 bump an 8-file hand edit.
func TestEmbeddedInstaller_RKE2VersionMatchesConst(t *testing.T) {
	// Match an ACTIVE assignment line, anchored at line start after trimming
	// indentation — not strings.Contains, which a commented-out `# RKE2_VERSION=…`
	// or a secondary reference would satisfy while the real default drifted.
	want := `RKE2_VERSION="${RKE2_VERSION:-` + defaultRKE2Version + `}"`
	for name, script := range map[string][]byte{
		"install-server.sh": installServerScript,
		"install-agent.sh":  installAgentScript,
	} {
		found := false
		for _, line := range strings.Split(string(script), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s has no active RKE2_VERSION default matching defaultRKE2Version %q — bump the const AND both embedded scripts together", name, defaultRKE2Version)
		}
	}
}
