package gputransition

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var nodeNamePattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9.]*[a-z0-9])?$`)

func ReadNodeMode(path, node string) (Mode, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read GPU node mode rule: %w", err)
	}
	_, _, mode, err := locateNodeRule(string(body), node)
	return mode, err
}

func WriteNodeMode(path, node string, from, to Mode) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read GPU node mode rule: %w", err)
	}
	start, end, current, err := locateNodeRule(string(body), node)
	if err != nil {
		return err
	}
	if current != from {
		return fmt.Errorf("GPU node mode rule drift: node %s is %q, want %q", node, current, from)
	}
	lines := strings.Split(string(body), "\n")
	workloadConfig := string(to)
	if to == ModePodHAMi {
		workloadConfig = "container"
	}
	replacements := map[string]string{
		"    - name:":                                     "    - name: " + node + "-" + string(to),
		"        kube-dc.com/gpu.workload-mode:":          "        kube-dc.com/gpu.workload-mode: " + string(to),
		"        kube-dc.com/gpu.expected-workload-mode:": "        kube-dc.com/gpu.expected-workload-mode: " + string(to),
		"        nvidia.com/gpu.workload.config:":         "        nvidia.com/gpu.workload.config: " + workloadConfig,
	}
	seen := map[string]bool{}
	for i := start; i < end; i++ {
		for prefix, replacement := range replacements {
			if strings.HasPrefix(lines[i], prefix) {
				lines[i] = replacement
				seen[prefix] = true
			}
		}
	}
	if len(seen) != len(replacements) {
		return fmt.Errorf("GPU node mode rule for %s is missing generated ownership labels", node)
	}
	return atomicWrite(path, []byte(strings.Join(lines, "\n")))
}

func locateNodeRule(body, node string) (int, int, Mode, error) {
	if !nodeNamePattern.MatchString(node) {
		return 0, 0, "", fmt.Errorf("invalid Kubernetes node name %q", node)
	}
	lines := strings.Split(body, "\n")
	valueLine := `              value: ["` + node + `"]`
	start, end := -1, -1
	for i, line := range lines {
		if line != valueLine {
			continue
		}
		blockStart := i
		for blockStart >= 0 && !strings.HasPrefix(lines[blockStart], "    - name:") {
			blockStart--
		}
		if blockStart < 0 || start >= 0 {
			return 0, 0, "", fmt.Errorf("GPU node mode rule for %s is ambiguous", node)
		}
		start = blockStart
		end = len(lines)
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "    - name:") {
				end = j
				break
			}
		}
	}
	if start < 0 {
		return 0, 0, "", fmt.Errorf("GPU node mode rule for %s is absent", node)
	}
	values := map[string]string{}
	for i := start; i < end; i++ {
		for _, key := range []string{"kube-dc.com/gpu.workload-mode", "kube-dc.com/gpu.expected-workload-mode", "nvidia.com/gpu.workload.config"} {
			prefix := "        " + key + ":"
			if strings.HasPrefix(lines[i], prefix) {
				values[key] = strings.TrimSpace(strings.TrimPrefix(lines[i], prefix))
			}
		}
	}
	mode := Mode(values["kube-dc.com/gpu.workload-mode"])
	expected := Mode(values["kube-dc.com/gpu.expected-workload-mode"])
	config := values["nvidia.com/gpu.workload.config"]
	wantConfig := string(mode)
	if mode == ModePodHAMi {
		wantConfig = "container"
	}
	if (mode != ModePodHAMi && mode != ModeVMPassthrough) || expected != mode || config != wantConfig {
		return 0, 0, "", fmt.Errorf("GPU node mode rule for %s has ownership label drift", node)
	}
	return start, end, mode, nil
}

func atomicWrite(path string, body []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create GPU node mode temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("write GPU node mode temp file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod GPU node mode temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close GPU node mode temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace GPU node mode rule: %w", err)
	}
	return nil
}
