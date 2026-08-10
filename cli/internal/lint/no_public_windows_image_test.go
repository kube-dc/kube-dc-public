package lint

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoPublicWindowsImagePath keeps the BYOL rule from being a convention that
// lives in somebody's head.
//
// An installed Windows image must not be published where the OS mirror publishes.
// That prefix is anonymously readable, and an installed Windows is a different
// thing from the Linux cloud images beside it: redistribution of Microsoft media is
// governed by its own licence terms whether or not a product key is baked in. Our
// golden carries no <ProductKey> — it is an unactivated Enterprise Evaluation build
// and the tenant applies their own licence — but "no product key" is not "free to
// redistribute anonymously", and those two are easy to conflate at 2am while making
// something work.
//
// So: any manifest that names a Windows *golden* (an installed disk image, not the
// installer ISO) must put it under a private prefix. Installer ISOs are exempt —
// they are unmodified Microsoft evaluation media fetched from Microsoft, and the
// mirror's whole job is to hold a local copy.
func TestNoPublicWindowsImagePath(t *testing.T) {
	roots := []string{"../../../hack", "../../../charts", "../../../examples", "../../../docs"}

	// A Windows image object under a bucket path. Captures the key so the exemption
	// check below can look at it.
	imageRef := regexp.MustCompile(`(?i)(cdi-os-images|\$\{?BUCKET_NAME\}?)/([A-Za-z0-9_./${}-]*windows[A-Za-z0-9_./${}-]*\.(qcow2|raw|vhdx?))`)

	for _, root := range roots {
		if _, err := os.Stat(root); err != nil {
			continue
		}
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".yaml", ".yml", ".sh", ".md", ".json":
			default:
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for _, m := range imageRef.FindAllStringSubmatch(string(b), -1) {
				key := m[2]
				if strings.Contains(key, "private/") {
					continue // correctly behind the private prefix
				}
				t.Errorf("%s publishes a Windows disk image to an anonymously readable path:\n"+
					"    %s\n"+
					"Installed Windows must go under a private prefix and be imported with a\n"+
					"secretRef. Installer ISOs are exempt; disk images are not.", path, m[0])
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}
