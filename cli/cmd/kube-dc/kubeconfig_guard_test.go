package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKubeconfigTarget(t *testing.T) {
	home, _ := os.UserHomeDir()
	def := filepath.Join(home, ".kube", "config")

	cases := []struct {
		name string
		env  string
		want string
	}{
		{"unset → default", "", def},
		{"single path", "/tmp/x.yaml", "/tmp/x.yaml"},
		{"colon list, first wins", "/tmp/a:/tmp/b", "/tmp/a"},
		{"empty leading segment skipped", ":/tmp/c", "/tmp/c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KUBECONFIG", tc.env)
			got := kubeconfigTarget()
			if got != tc.want {
				t.Errorf("kubeconfigTarget() = %q, want %q", got, tc.want)
			}
		})
	}
}
