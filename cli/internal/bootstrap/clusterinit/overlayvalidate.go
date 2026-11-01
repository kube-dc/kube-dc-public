package clusterinit

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// validateOverlayYAML parses every YAML document under the cluster overlay.
// SOPS files are YAML too, so they are checked as well.
func validateOverlayYAML(clusterDir string) error {
	return filepath.WalkDir(clusterDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !(strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		dec := yaml.NewDecoder(f)
		for {
			var node yaml.Node
			if err := dec.Decode(&node); errors.Is(err, io.EOF) {
				return nil
			} else if err != nil {
				rel, _ := filepath.Rel(filepath.Dir(clusterDir), path)
				return fmt.Errorf("generated overlay file %s is not valid YAML: %w", rel, err)
			}
		}
	})
}
