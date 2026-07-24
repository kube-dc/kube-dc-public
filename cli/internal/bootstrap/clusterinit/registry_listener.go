/*
Copyright Kube-DC 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package clusterinit

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

const registryListenerMarker = "# image-accel: registry-depot Gateway listener (do not duplicate)"

const registryListenerPatchEntry = `    ` + registryListenerMarker + `
    - target:
        kind: Gateway
        name: eg
        namespace: envoy-gateway-system
      patch: |
        - op: add
          path: /spec/listeners/-
          value:
            name: https-registry
            protocol: HTTPS
            port: 443
            hostname: "${REGISTRY_HOSTNAME:=registry.${DOMAIN}}"
            tls:
              mode: Terminate
              certificateRefs:
                - kind: Secret
                  name: registry-server-tls
                  namespace: kube-dc
            allowedRoutes:
              namespaces:
                from: All`

// ensureRegistryDepotListener adds the listener consumed by
// platform/registry-depot/exposure.yaml to the per-cluster platform Flux
// Kustomization. Keeping it here makes registry-depot genuinely optional:
// clusters without the addon do not carry a listener whose Certificate and
// Secret can never become ready.
func ensureRegistryDepotListener(clusterDir string, out io.Writer) error {
	path := filepath.Join(clusterDir, "platform.yaml")
	if err := patchFileLines(path, patchPlatformRegistryListener); err != nil {
		return fmt.Errorf("image-accel: patch platform Gateway listener: %w", err)
	}
	fmt.Fprintln(out, "[scaffold] registry-depot Gateway listener wired (https-registry)")
	return nil
}

// patchPlatformRegistryListener composes with an existing spec.patches list
// instead of assuming it is the last key. This matters when object-storage or
// single-IP-NAT already added their own platform patches earlier in scaffold.
func patchPlatformRegistryListener(lines []string) ([]string, bool, error) {
	for _, line := range lines {
		if strings.TrimSpace(line) == registryListenerMarker {
			return lines, false, nil
		}
	}

	patches := -1
	for i, line := range lines {
		if line == "  patches:" {
			patches = i
			break
		}
	}

	entry := strings.Split(registryListenerPatchEntry, "\n")
	if patches >= 0 {
		// Insert before the next two-space-indented spec sibling, or at
		// EOF when patches is the final key. Four-or-more-space lines are
		// part of the patches list; blank lines and comments stay with it.
		insert := len(lines)
		for i := patches + 1; i < len(lines); i++ {
			trimmed := strings.TrimSpace(lines[i])
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if strings.HasPrefix(lines[i], "  ") && !strings.HasPrefix(lines[i], "    ") {
				insert = i
				break
			}
		}
		result := make([]string, 0, len(lines)+len(entry))
		result = append(result, lines[:insert]...)
		result = append(result, entry...)
		result = append(result, lines[insert:]...)
		return result, true, nil
	}

	// The add-cluster-generated Flux Kustomization has spec as its final
	// root key. Append a new spec.patches list after its last real line.
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	result := make([]string, 0, end+len(entry)+2)
	result = append(result, lines[:end]...)
	result = append(result, "  patches:")
	result = append(result, entry...)
	result = append(result, "")
	return result, true, nil
}
