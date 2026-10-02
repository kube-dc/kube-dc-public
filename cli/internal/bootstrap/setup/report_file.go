package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SaveReadinessReport writes a read-only report with private file permissions.
// It replaces an existing regular file atomically and never follows a final
// symlink. A report is evidence for review, not an installation approval.
func SaveReadinessReport(path string, report ReadinessReport) error {
	if path == "" {
		return fmt.Errorf("report path is required")
	}
	if report.SchemaVersion != ReadinessSchemaVersion || report.InputHash == "" || report.PlanHash == "" {
		return fmt.Errorf("cannot save an incomplete setup report")
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("report path must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect report path: %w", err)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("encode setup report: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create report directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".kube-dc-setup-report-*")
	if err != nil {
		return fmt.Errorf("create temporary report: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write setup report: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync setup report: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close setup report: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace setup report: %w", err)
	}
	return nil
}
