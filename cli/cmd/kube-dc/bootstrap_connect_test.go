package main

import (
	"bytes"
	"testing"
)

func TestBootstrapConnect_RequiresExactlyOneHost(t *testing.T) {
	for _, args := range [][]string{
		nil,        // no host
		{"a", "b"}, // too many
	} {
		var buf bytes.Buffer
		repo := ""
		cmd := bootstrapConnectCmd(&repo)
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("args %v should be rejected (ExactArgs 1)", args)
		}
	}
}

func TestBootstrapConnect_HasReachFlags(t *testing.T) {
	repo := ""
	cmd := bootstrapConnectCmd(&repo)
	for _, f := range []string{"ssh-jump", "ssh-accept-new-host-keys"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("connect missing --%s", f)
		}
	}
}

func TestBootstrapFetchKubeconfig_HasReachFlags(t *testing.T) {
	repo := ""
	cmd := bootstrapFetchKubeconfigCmd(&repo)
	for _, f := range []string{"ssh-jump", "ssh-accept-new-host-keys"} {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("fetch-kubeconfig missing --%s (parity with install/remove-node)", f)
		}
	}
}
