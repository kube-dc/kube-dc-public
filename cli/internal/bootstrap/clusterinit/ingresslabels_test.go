package clusterinit

import (
	"context"
	"strings"
	"testing"
)

// fakeNodeLabeler is a minimal stand-in: the real mock is scenario-driven and this test
// only needs the two node methods.
type fakeNodeLabeler struct {
	labels map[string]map[string]string
	writes []string
	setErr error
	// dropWrites reproduces a patch that reports success without landing, which is the
	// failure this step's verify-after-write exists to catch.
	dropWrites bool
}

func (f *fakeNodeLabeler) NodeInternalIPs(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}

func (f *fakeNodeLabeler) NodeLabels(_ context.Context) (map[string]map[string]string, error) {
	out := map[string]map[string]string{}
	for n, l := range f.labels {
		cp := map[string]string{}
		for k, v := range l {
			cp[k] = v
		}
		out[n] = cp
	}
	return out, nil
}

func (f *fakeNodeLabeler) SetNodeLabel(_ context.Context, node, key, value string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.writes = append(f.writes, node+":"+key+"="+value)
	if f.dropWrites {
		return nil
	}
	if f.labels[node] == nil {
		f.labels[node] = map[string]string{}
	}
	f.labels[node][key] = value
	return nil
}

// ensure runs the step against the fake through the same code path as production by
// using the narrow interface EnsureIngressNodeLabels needs.
func ensure(t *testing.T, f *fakeNodeLabeler, nodes []string) error {
	t.Helper()
	return ensureIngressNodeLabelsFor(context.Background(), f, nodes, DefaultIngressNodeLabel, nil)
}

func TestEnsureIngressNodeLabels_EmptySetRefused(t *testing.T) {
	f := &fakeNodeLabeler{labels: map[string]map[string]string{"a": {}}}
	err := ensure(t, f, nil)
	if err == nil {
		t.Fatal("expected refusal on an empty ingress set: unlabelled means no front door")
	}
	if len(f.writes) != 0 {
		t.Fatalf("must not write anything: %v", f.writes)
	}
}

func TestEnsureIngressNodeLabels_UnknownNodeRefusedBeforeAnyWrite(t *testing.T) {
	f := &fakeNodeLabeler{labels: map[string]map[string]string{"a": {}, "b": {}}}
	err := ensure(t, f, []string{"a", "typo"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found refusal, got %v", err)
	}
	// The point of validating up front: a typo must not half-label the cluster.
	if len(f.writes) != 0 {
		t.Fatalf("must not write before validating the whole plan: %v", f.writes)
	}
}

func TestEnsureIngressNodeLabels_ForeignLabelRefused(t *testing.T) {
	f := &fakeNodeLabeler{labels: map[string]map[string]string{
		"a":     {},
		"other": {DefaultIngressNodeLabel: "true"},
	}}
	err := ensure(t, f, []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "outside the planned ingress set") {
		t.Fatalf("expected refusal when an unplanned node already carries the label, got %v", err)
	}
	if len(f.writes) != 0 {
		t.Fatalf("must not write: %v", f.writes)
	}
}

func TestEnsureIngressNodeLabels_AppliesAndIsIdempotent(t *testing.T) {
	f := &fakeNodeLabeler{labels: map[string]map[string]string{
		"a": {},
		"b": {DefaultIngressNodeLabel: "true"},
	}}
	if err := ensure(t, f, []string{"a", "b"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(f.writes) != 1 || f.writes[0] != "a:"+DefaultIngressNodeLabel+"=true" {
		t.Fatalf("expected exactly one write for the unlabelled node, got %v", f.writes)
	}
	// Second run must be a no-op.
	f.writes = nil
	if err := ensure(t, f, []string{"a", "b"}); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(f.writes) != 0 {
		t.Fatalf("expected idempotence, got writes %v", f.writes)
	}
}

func TestEnsureIngressNodeLabels_VerifiesAfterWrite(t *testing.T) {
	// A patch that returns success without landing is precisely the silent failure this
	// step guards: the label is the front door's only placement precondition.
	f := &fakeNodeLabeler{
		labels:     map[string]map[string]string{"a": {}},
		dropWrites: true,
	}
	err := ensure(t, f, []string{"a"})
	if err == nil || !strings.Contains(err.Error(), "does not show it") {
		t.Fatalf("expected verify-after-write failure, got %v", err)
	}
}
