package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
)

// StarterReleaseBinding connects the release set to the actual selected
// starter sources and generated environment. It is not qualification evidence.
type StarterReleaseBinding struct {
	ReleaseSetSHA256 string            `json:"releaseSetSHA256"`
	Pins             map[string]string `json:"pins"`
	Files            map[string]string `json:"files"`
}

var releasePinKey = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func validateStarterReleaseBinding(binding *StarterReleaseBinding) error {
	if binding == nil || !hexSHA256.MatchString(binding.ReleaseSetSHA256) || len(binding.Files) < 3 || len(binding.Files) > 4096 || len(binding.Pins) == 0 || len(binding.Pins) > 4096 {
		return fmt.Errorf("incomplete starter release binding")
	}
	if binding.Files["bootstrap/product-release.yaml"] != binding.ReleaseSetSHA256 {
		return fmt.Errorf("release set differs from bound source")
	}
	for _, name := range []string{"bootstrap/release-pins.env", "bootstrap/ui-release.json"} {
		if !hexSHA256.MatchString(binding.Files[name]) {
			return fmt.Errorf("missing starter metadata binding")
		}
	}
	for name, hash := range binding.Files {
		if !hexSHA256.MatchString(hash) || path.Clean(name) != name || path.IsAbs(name) || name == "." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\\r\n\x00") {
			return fmt.Errorf("invalid bound starter path or checksum")
		}
	}
	for key, value := range binding.Pins {
		if !releasePinKey.MatchString(key) || secretKey(key) || strings.ContainsAny(value, "\r\n\x00") || len(value) > 4096 {
			return fmt.Errorf("invalid non-secret release pin")
		}
	}
	for _, component := range []struct{ key, prefix string }{
		{"KEYCLOAK_THEME_COMPONENT", "platform/keycloak/components/"},
		{"SERVICES_CATALOG_COMPONENT", "platform/kube-dc-services-catalog/components/"},
	} {
		name := binding.Pins[component.key]
		if !releaseIDPattern.MatchString(name) || !hexSHA256.MatchString(binding.Files[component.prefix+name+"/kustomization.yaml"]) {
			return fmt.Errorf("selected release component is unbound")
		}
	}
	return nil
}

// VerifyStarterReleaseBindings checks both retained source bytes and the
// effective environment produced by scaffold. Legacy records have no binding
// and retain the existing unresolved consumer/qualification gates.
func VerifyStarterReleaseBindings(ctx context.Context, c Compiled, directory string, environment map[string]string) error {
	if err := checkCompiled(c); err != nil {
		return err
	}
	data, err := readReleaseRecord(c.Spec.Release.RecordFile)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != c.ReleaseSHA256 {
		return fmt.Errorf("release changed before starter binding")
	}
	var record InstallerReleaseRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return err
	}
	binding := record.Artifacts.StarterBinding
	if binding == nil {
		return nil
	}
	if err := validateStarterReleaseBinding(binding); err != nil {
		return err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	var total int64
	for name, want := range binding.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return fmt.Errorf("bound starter source is missing or unsafe")
		}
		total += info.Size()
		if total > 64<<20 {
			return fmt.Errorf("starter binding exceeds source verification limit")
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, readErr := io.Copy(h, io.LimitReader(file, (4<<20)+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || n != info.Size() || hex.EncodeToString(h.Sum(nil)) != want {
			return fmt.Errorf("bound starter source changed")
		}
	}
	for key, value := range binding.Pins {
		if actual, found := environment[key]; !found || actual != value {
			return fmt.Errorf("generated release pin %s differs from selected starter", key)
		}
	}
	return nil
}
