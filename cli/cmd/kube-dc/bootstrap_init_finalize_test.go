package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

func TestWriteGPUInstallCompletionNeverClaimsAutomaticEntitlement(t *testing.T) {
	var out bytes.Buffer
	writeGPUInstallCompletion(&out)
	got := out.String()
	for _, want := range []string{"GPU platform installation is ready", "granted no billable tenant GPU quota", "GPU add-on", "bootstrap doctor", "Accelerators"} {
		if !strings.Contains(got, want) {
			t.Fatalf("completion missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[gpu]") {
		t.Fatalf("completion invented forbidden GPU log prefix:\n%s", got)
	}
}

// fakeGRF adapts a func to the minimal interface waitPodRunning needs.
type fakeGRF func(ctx context.Context, group, version, resource, namespace, name string, fields ...string) (string, error)

func (f fakeGRF) GetResourceFieldFirst(ctx context.Context, group, version, resource, namespace, name string, fields ...string) (string, error) {
	return f(ctx, group, version, resource, namespace, name, fields...)
}

func TestWaitPodRunning_ReturnsWhenRunning(t *testing.T) {
	// Speed up: timeAfter fires immediately so the poll loop doesn't
	// sleep in real time.
	origAfter := timeAfter
	timeAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	defer func() { timeAfter = origAfter }()

	calls := 0
	fake := fakeGRF(func(_ context.Context, _, _, _, _, _ string, _ ...string) (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("pods \"openbao-0\" not found")
		}
		return "Running", nil
	})

	if err := waitPodRunning(context.Background(), io.Discard, fake, "openbao", "openbao-0", time.Minute); err != nil {
		t.Fatalf("expected nil once Running, got %v", err)
	}
	if calls < 3 {
		t.Fatalf("expected the poll loop to retry until Running, got %d calls", calls)
	}
}

func TestWaitPodRunning_TimesOut(t *testing.T) {
	origAfter := timeAfter
	timeAfter = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	defer func() { timeAfter = origAfter }()

	fake := fakeGRF(func(_ context.Context, _, _, _, _, _ string, _ ...string) (string, error) {
		return "Pending", nil // never Running
	})

	// budget 0 → deadline is now, so the first not-Running poll times out.
	err := waitPodRunning(context.Background(), io.Discard, fake, "openbao", "openbao-0", 0)
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
}

func TestWaitPodRunning_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	fake := fakeGRF(func(_ context.Context, _, _, _, _, _ string, _ ...string) (string, error) {
		return "Pending", nil
	})
	if err := waitPodRunning(ctx, io.Discard, fake, "openbao", "openbao-0", time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestPostApplyOpenBaoInitOptionsCarriesSharesOutCustodyPath(t *testing.T) {
	opts := &clusterinit.InitOptions{
		Name:             "atlantis",
		Repo:             "/fleet",
		NoPush:           true,
		OpenBaoSharesOut: "/secure/off-git/atlantis-openbao-shares.yaml",
	}
	session := &bootstrap.Session{}
	got := postApplyOpenBaoInitOptions(opts, session, "token", io.Discard)
	if got.SharesOutPath != opts.OpenBaoSharesOut {
		t.Fatalf("automatic finalizer dropped --openbao-shares-out: got %q, want %q", got.SharesOutPath, opts.OpenBaoSharesOut)
	}
	if got.ClusterName != opts.Name || got.FleetRepo != opts.Repo || !got.NoPush {
		t.Fatalf("automatic finalizer options drifted: %+v", got)
	}
}

// TestPostApplyBreakGlassOptionsWiring guards the exact gap that left crk,
// jed and next with no recovery kubeconfig committed: `bootstrap init` must
// wire ClusterName/FleetRepo/NoPush straight through, and must pass the
// freshly-merged admin kubeconfig PATH explicitly rather than leaving Adopt
// to guess at the operator's ambient $KUBECONFIG.
func TestPostApplyBreakGlassOptionsWiring(t *testing.T) {
	opts := &clusterinit.InitOptions{
		Name:   "atlantis",
		Repo:   "/fleet",
		NoPush: true,
	}
	session := &bootstrap.Session{}

	t.Run("fetchVerified pins KubectlContext to the cluster name", func(t *testing.T) {
		got := postApplyBreakGlassOptions(opts, session, "/home/op/.kube/config", "token", true)
		if got.ClusterName != opts.Name || got.FleetRoot != opts.Repo || !got.NoPush {
			t.Fatalf("automatic finalizer options drifted: %+v", got)
		}
		if got.KubeconfigPath != "/home/op/.kube/config" {
			t.Fatalf("automatic finalizer dropped the fetched kubeconfig path: got %q", got.KubeconfigPath)
		}
		if got.KubectlContext != opts.Name {
			t.Fatalf("fetchVerified must pin KubectlContext to the cluster name (fetch-kubeconfig guarantees that context exists), got %q", got.KubectlContext)
		}
		if got.GitHubToken != "token" {
			t.Fatalf("automatic finalizer dropped the resolved GitHub token: got %q", got.GitHubToken)
		}
	})

	t.Run("NOT fetchVerified (e.g. --no-ssh) must NOT assert a context that may not exist or may address a different cluster", func(t *testing.T) {
		got := postApplyBreakGlassOptions(opts, session, "/home/op/.kube/config", "token", false)
		if got.KubectlContext != "" {
			t.Fatalf("must not pin KubectlContext without fetch evidence backing the guarantee, got %q", got.KubectlContext)
		}
		// Everything else still wires through normally.
		if got.ClusterName != opts.Name || got.KubeconfigPath != "/home/op/.kube/config" {
			t.Fatalf("unrelated fields drifted: %+v", got)
		}
	})
}

// TestFinalizeHintTargetsTheRightCluster guards against a re-run hint that
// applies cluster-admin RBAC to the WRONG cluster: when a real kubeconfig
// path is known, the printed break-glass command must be prefixed with
// KUBECONFIG=<path>, and --kube-context <name> is added ONLY when
// fetchVerified — appending it unconditionally would be worse than
// omitting it (a context named o.Name might not exist on the --no-ssh
// path, or might exist and address a completely different cluster). When
// no path can be trusted at all (the admin kubeconfig fetch itself
// failed), the hint must say so explicitly instead of silently omitting
// any targeting.
func TestFinalizeHintTargetsTheRightCluster(t *testing.T) {
	o := &clusterinit.InitOptions{Name: "jed", Repo: "/fleet"}

	t.Run("fetchVerified: kubeconfig path AND context are both pinned", func(t *testing.T) {
		var out bytes.Buffer
		finalizeHint(&out, o, "/home/op/.kube/config", true)
		got := out.String()
		want := "KUBECONFIG='/home/op/.kube/config' kube-dc bootstrap break-glass adopt jed --repo '/fleet' --kube-context 'jed'\n"
		if !strings.Contains(got, want) {
			t.Fatalf("missing the fully-targeted break-glass hint %q in:\n%s", want, got)
		}
	})

	t.Run("kubeconfig path known but NOT fetchVerified: file is printed, context is NOT", func(t *testing.T) {
		var out bytes.Buffer
		finalizeHint(&out, o, "/home/op/.kube/config", false)
		got := out.String()
		want := "KUBECONFIG='/home/op/.kube/config' kube-dc bootstrap break-glass adopt jed --repo '/fleet'\n"
		if !strings.Contains(got, want) {
			t.Fatalf("missing the file-only break-glass hint %q in:\n%s", want, got)
		}
		if strings.Contains(got, "--kube-context") {
			t.Fatalf("must not assert --kube-context without fetch evidence backing it:\n%s", got)
		}
	})

	t.Run("a path with a space or a literal $ survives copy-paste unmangled", func(t *testing.T) {
		var out bytes.Buffer
		finalizeHint(&out, o, "/home/op ops/.kube/$USER-config", true)
		got := out.String()
		want := "KUBECONFIG='/home/op ops/.kube/$USER-config' kube-dc bootstrap break-glass adopt jed --repo '/fleet' --kube-context 'jed'\n"
		if !strings.Contains(got, want) {
			t.Fatalf("path must be single-quoted so a space doesn't split the command and $USER doesn't shell-expand:\nwant substring %q\ngot:\n%s", want, got)
		}
	})

	t.Run("a repo path with a space is quoted too", func(t *testing.T) {
		var out bytes.Buffer
		finalizeHint(&out, &clusterinit.InitOptions{Name: "jed", Repo: "/home/op ops/fleet"}, "", false)
		got := out.String()
		if strings.Contains(got, "--repo /home/op ops/fleet") {
			t.Fatalf("unquoted repo path would split into multiple shell arguments:\n%s", got)
		}
		if !strings.Contains(got, "--repo '/home/op ops/fleet'") {
			t.Fatalf("missing quoted repo path in:\n%s", got)
		}
	})

	t.Run("no trustworthy path — hint says so instead of guessing", func(t *testing.T) {
		var out bytes.Buffer
		finalizeHint(&out, o, "", false)
		got := out.String()
		if strings.Contains(got, "KUBECONFIG=") {
			t.Fatalf("must not fabricate a KUBECONFIG= prefix with no known-good path:\n%s", got)
		}
		if !strings.Contains(got, "kube-dc bootstrap break-glass adopt jed --repo") {
			t.Fatalf("missing the break-glass command in:\n%s", got)
		}
		if !strings.Contains(got, "point --kube-context") && !strings.Contains(got, "$KUBECONFIG") {
			t.Fatalf("must warn the operator to target the right cluster themselves:\n%s", got)
		}
	})
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain path", "/home/op/.kube/config", "'/home/op/.kube/config'"},
		{"space", "/home/op ops/.kube/config", "'/home/op ops/.kube/config'"},
		{"literal dollar does not expand once quoted", "/home/$USER/.kube/config", "'/home/$USER/.kube/config'"},
		{"embedded single quote is escaped, not dropped", "/home/o'brien/.kube/config", `'/home/o'"'"'brien/.kube/config'`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := shellQuote(tc.in); got != tc.want {
				t.Fatalf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
