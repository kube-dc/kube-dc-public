package clusterinit

import (
	"strings"
	"testing"
)

// patchPlatformDependsOn does line-surgery on the platform.yaml that
// add-cluster.sh generates, and errors with "file shape drifted" if the shape
// changes. Scaffolding Tenant Networking v2 changed exactly that shape: the
// dependsOn block gained a `- name: tenant-net-v2` entry preceded by an
// explanatory comment block.
//
// A comment between list items is precisely the thing a line scanner mishandles,
// and this parser runs as a Scaffold step against the freshly written file — so
// a mismatch fails the install midway, after the repo has already been written.
// The existing unit tests use synthetic fixtures, so they would not have caught
// it; this pins the REAL generated shape.
const platformWithTenantNetV2 = `apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: platform
  namespace: flux-system
spec:
  dependsOn:
    - name: infra-core
    # tenant-net-v2 creates the infra-net Subnet. This edge is load-bearing:
    # platform brings up kube-dc-manager, which labels every project namespace
    # network.kube-dc.com/infra-attachment=enabled. The pod webhook selects on
    # that label with failurePolicy=Fail and its readiness check fails closed
    # when the subnet is missing.
    - name: tenant-net-v2
  interval: 10m
  path: ./platform
  prune: false
`

func TestPatchPlatformDependsOn_HandlesTenantNetV2Shape(t *testing.T) {
	out, changed, err := patchPlatformDependsOn(strings.Split(platformWithTenantNetV2, "\n"))
	if err != nil {
		t.Fatalf("parser rejected the shape add-cluster.sh now generates: %v", err)
	}
	if !changed {
		t.Fatal("expected the object-storage dependency to be added")
	}
	joined := strings.Join(out, "\n")
	for _, want := range []string{
		"- name: infra-core",
		"- name: tenant-net-v2",
		"- name: infra-object-storage",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("%q missing after patch:\n%s", want, joined)
		}
	}
	// The comment block must survive: it documents why the ordering edge exists,
	// and silently dropping it would strip the rationale from every new cluster.
	if !strings.Contains(joined, "load-bearing") {
		t.Fatalf("the dependsOn comment block was dropped:\n%s", joined)
	}
}

// Idempotence must still hold on the new shape — Scaffold steps re-run.
func TestPatchPlatformDependsOn_IdempotentOnTenantNetV2Shape(t *testing.T) {
	once, _, err := patchPlatformDependsOn(strings.Split(platformWithTenantNetV2, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, changed, err := patchPlatformDependsOn(once)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("second pass modified the file again — not idempotent")
	}
}
