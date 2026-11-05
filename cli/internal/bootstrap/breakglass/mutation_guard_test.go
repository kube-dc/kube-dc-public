package breakglass

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

func TestDirectWritesRecheckOwnershipBetweenCalls(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "calls")
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte("#!/bin/sh\necho write >> '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	lost := errors.New("ownership lost")
	calls := 0
	ctx := ports.WithMutationGuard(context.Background(), func(context.Context) error {
		calls++
		if calls > 1 {
			return lost
		}
		return nil
	})
	if err := kubectlApply(ctx, "/private/child", "cluster", "kind: List\n"); err != nil {
		t.Fatal(err)
	}
	if err := kubectlRun(ctx, "/private/child", "cluster", "delete", "secret", "anything"); !errors.Is(err, lost) {
		t.Fatalf("second write ignored lost ownership: %v", err)
	}
	body, err := os.ReadFile(marker)
	if err != nil || string(body) != "write\n" {
		t.Fatalf("unexpected subprocess writes: %q %v", body, err)
	}
	if err := sopsEncryptToFile(ctx, []byte("private"), filepath.Join(dir, "outside", "secret.enc.yaml")); !errors.Is(err, lost) {
		t.Fatalf("SOPS write ignored guard: %v", err)
	}
}
