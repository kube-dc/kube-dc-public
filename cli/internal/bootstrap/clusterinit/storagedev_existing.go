package clusterinit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
	"gopkg.in/yaml.v3"
)

// RequireUnchangedRawOSDSelection permits an adopt/resume operation to keep
// the raw devices already named by its Fleet overlay. Existing Ceph OSDs are
// in use by definition and must never be subjected to an empty-disk gate.
// This is a configuration match, not proof of Ceph ownership.
func RequireUnchangedRawOSDSelection(envPath string, spec ObjectStorageSpec) error {
	requestedRaw := len(spec.RawOSDDevices()) > 0
	env, err := config.LoadEnv(envPath)
	if err != nil {
		if !requestedRaw && errors.Is(err, os.ErrNotExist) {
			// The existing-cluster boundary reports a missing overlay. Do not
			// replace its diagnostic when no raw OSD was selected.
			return nil
		}
		return fmt.Errorf("read existing raw OSD selection: %w", err)
	}
	overlayPath := filepath.Join(filepath.Dir(envPath), "object-storage", "kustomization.yaml")
	if !requestedRaw {
		// A proposed PVC, disabled, or loop-backed mode must not silently
		// abandon the raw OSDs named by an existing overlay. Ignore stale
		// CEPH_* keys only when the active Kustomization uses the PVC mode.
		if !hasRawOSDKeys(env) {
			return nil
		}
		mode, err := readOverlayStorageMode(overlayPath)
		if err != nil {
			return err
		}
		if mode == RookCephLocal || mode == RookCephMultiNode {
			return fmt.Errorf("existing Fleet overlay selects raw OSDs; changing their storage mode or devices requires a separate storage migration")
		}
		return nil
	}
	if mode, found := env.Get("OBJECT_STORAGE_MODE"); found && mode != string(spec.Mode) {
		return fmt.Errorf("existing Fleet overlay does not match selected OBJECT_STORAGE_MODE; review the existing storage before continuing")
	}
	// Older scaffold outputs did not write OBJECT_STORAGE_MODE. The live
	// kustomization resource, rather than its comment, identifies the selected
	// Fleet mode. Read it for both old and new overlays so one stale env value
	// cannot certify a different data-plane mode.
	if err := requireOverlayStorageMode(overlayPath, spec.Mode); err != nil {
		return err
	}
	needed := map[string]string{}
	switch spec.Mode {
	case RookCephLocal:
		needed["CEPH_LOCAL_OSD_NODE"] = spec.OSDNode
		needed["CEPH_LOCAL_OSD_DEVICE"] = spec.OSDDevice
	case RookCephMultiNode:
		for _, pair := range objectStorageEnvKeys("", spec) {
			if strings.HasPrefix(pair[0], "CEPH_NODE_") {
				needed[pair[0]] = pair[1]
			}
		}
		// An omitted slot is also a storage change. Checking only requested
		// slots would silently accept removal of an existing OSD.
		for i := 1; i <= 3; i++ {
			name := fmt.Sprintf("CEPH_NODE_%d", i)
			for _, key := range []string{name, name + "_DEVICE"} {
				if _, wanted := needed[key]; !wanted {
					if current, found := env.Get(key); found && current != "" {
						return fmt.Errorf("existing Fleet overlay has an extra raw OSD setting %s; review the existing storage before continuing", key)
					}
				}
			}
		}
	default:
		return fmt.Errorf("raw OSD selection has unsupported storage mode")
	}
	for key, wanted := range needed {
		actual, found := env.Get(key)
		if !found || strings.TrimPrefix(actual, "/dev/") != strings.TrimPrefix(wanted, "/dev/") {
			return fmt.Errorf("existing Fleet overlay does not match selected raw OSD setting %s; review the existing storage before continuing", key)
		}
	}
	return nil
}

func hasRawOSDKeys(env *config.Env) bool {
	keys := []string{"CEPH_LOCAL_OSD_DEVICE", "CEPH_NODE_1_DEVICE", "CEPH_NODE_2_DEVICE", "CEPH_NODE_3_DEVICE"}
	for _, key := range keys {
		if value, _ := env.Get(key); value != "" && !loopDeviceRegex.MatchString(strings.TrimPrefix(value, "/dev/")) {
			return true
		}
	}
	return false
}

func requireOverlayStorageMode(path string, want RookMode) error {
	found, err := readOverlayStorageMode(path)
	if err != nil {
		return err
	}
	if found != want {
		return fmt.Errorf("existing object-storage overlay does not select %s; review the existing storage before continuing", want)
	}
	return nil
}

func readOverlayStorageMode(path string) (RookMode, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read existing object-storage overlay: %w", err)
	}
	var overlay struct {
		Resources []string `yaml:"resources"`
	}
	if err := yaml.Unmarshal(body, &overlay); err != nil {
		return "", fmt.Errorf("parse existing object-storage overlay: %w", err)
	}
	found := ""
	for _, resource := range overlay.Resources {
		for _, mode := range []RookMode{RookCephLocal, RookCephMultiNode, RookCephPVC} {
			if strings.HasSuffix(filepath.ToSlash(filepath.Clean(resource)), "/infrastructure/object-storage/modes/"+string(mode)) {
				if found != "" {
					return "", fmt.Errorf("existing object-storage overlay selects more than one Fleet mode")
				}
				found = string(mode)
			}
		}
	}
	if found == "" {
		return "", fmt.Errorf("existing object-storage overlay does not select a known Fleet mode")
	}
	return RookMode(found), nil
}
