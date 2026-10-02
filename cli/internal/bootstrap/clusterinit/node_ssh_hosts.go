package clusterinit

import (
	"fmt"
	"strings"
)

func validateNodeSSHHosts(primary string, hosts map[string]string) []string {
	var problems []string
	if primary != "" {
		if err := ValidateK8sNodeNameField(primary); err != nil {
			problems = append(problems, "primary node must be a Kubernetes node name")
		}
	}
	for _, node := range SpecOrderedKeys(hosts) {
		if err := ValidateK8sNodeNameField(node); err != nil {
			problems = append(problems, fmt.Sprintf("SSH target node %s must be a Kubernetes node name", node))
		}
		alias := hosts[node]
		if alias == "" || strings.ContainsAny(alias, " \t\r\n,=") || strings.HasPrefix(alias, "@") || strings.HasSuffix(alias, "@") {
			problems = append(problems, fmt.Sprintf("SSH target for node %s must be one user@host or host value", node))
		}
	}
	return problems
}

func validateNodeSSHHostKeys(pins map[string]string) []string {
	var problems []string
	for _, node := range SpecOrderedKeys(pins) {
		if err := ValidateK8sNodeNameField(node); err != nil {
			problems = append(problems, fmt.Sprintf("SSH key pin node %s must be a Kubernetes node name", node))
		}
		if err := ValidateSSHHostKeyField(pins[node]); err != nil || pins[node] == "" {
			problems = append(problems, fmt.Sprintf("SSH key pin for node %s must be a SHA256 fingerprint", node))
		}
	}
	return problems
}
