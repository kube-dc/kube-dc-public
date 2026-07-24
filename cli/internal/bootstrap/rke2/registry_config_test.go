package rke2

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedRegistryValidator_RejectsEmptyMirrorConfig(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		ok   bool
	}{
		{name: "wildcard mirror", yaml: "mirrors:\n  \"*\":\n", ok: true},
		{name: "named mirror", yaml: "mirrors:\n  docker.io:\n    endpoint:\n      - https://mirror.example.com\n", ok: true},
		{name: "inline mirror", yaml: "mirrors: {\"*\": {}}\n", ok: true},
		{name: "configs only", yaml: "configs:\n  registry.example.com:\n    auth:\n      username: user\n", ok: false},
		{name: "empty mirrors", yaml: "mirrors: {}\nconfigs:\n  registry.example.com: {}\n", ok: false},
		{name: "comment only", yaml: "mirrors: # no mirrors yet\nconfigs: {}\n", ok: false},
	}

	for scriptName, script := range map[string][]byte{
		"server": installServerScript,
		"agent":  installAgentScript,
	} {
		t.Run(scriptName, func(t *testing.T) {
			source := string(script)
			start := strings.Index(source, "registries_has_mirror() {")
			if start < 0 {
				t.Fatal("cannot find registries_has_mirror in embedded installer")
			}
			endMarker := "\n}\n"
			end := strings.Index(source[start:], endMarker)
			if end < 0 {
				t.Fatal("cannot extract registries_has_mirror from embedded installer")
			}
			fn := source[start : start+end+2]
			runner := filepath.Join(t.TempDir(), "validate.sh")
			if err := os.WriteFile(runner, []byte("#!/usr/bin/env bash\nset -euo pipefail\n"+fn+"\nregistries_has_mirror \"$1\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					config := filepath.Join(t.TempDir(), "registries.yaml")
					if err := os.WriteFile(config, []byte(tc.yaml), 0o600); err != nil {
						t.Fatal(err)
					}
					err := exec.Command("bash", runner, config).Run()
					if (err == nil) != tc.ok {
						t.Fatalf("validator success=%v, want %v for:\n%s", err == nil, tc.ok, tc.yaml)
					}
				})
			}
		})
	}
}
