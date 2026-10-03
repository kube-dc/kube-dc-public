package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// CheckCLIArtifactBytes compares the CLI binary used for this check with the
// digest in a compiled release record. It checks one local artifact only; the
// other bytes, live pins, and qualification evidence remain open.
func CheckCLIArtifactBytes(c Compiled, report ReleaseReport, binaryPath string) (ReleaseReport, error) {
	if err := checkCompiled(c); err != nil {
		return ReleaseReport{}, err
	}
	if report.InputHash != c.InputHash || report.ReleaseSHA256 != c.ReleaseSHA256 {
		return ReleaseReport{}, fmt.Errorf("release report and setup inputs differ; run the release check again")
	}
	data, err := readReleaseRecord(c.Spec.Release.RecordFile)
	if err != nil {
		return ReleaseReport{}, err
	}
	recordHash := sha256.Sum256(data)
	if hex.EncodeToString(recordHash[:]) != c.ReleaseSHA256 {
		return ReleaseReport{}, fmt.Errorf("release record changed; validate the setup specification again")
	}
	var record InstallerReleaseRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return ReleaseReport{}, fmt.Errorf("release record is invalid: %w", err)
	}
	check := ReleaseCheck{ID: "artifacts.cliBytes", Status: "blocked", Detail: "running CLI binary could not be verified",
		NextAction: "Run the release check on a supported Linux workstation with the selected CLI binary."}
	if binaryPath != "" {
		file, err := os.Open(binaryPath)
		if err == nil {
			hash := sha256.New()
			_, err = io.Copy(hash, file)
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				if hex.EncodeToString(hash.Sum(nil)) == record.Artifacts.CLI.SHA256 && hexSHA256.MatchString(record.Artifacts.CLI.SHA256) {
					check.Status = "pass"
					check.Detail = "running CLI binary SHA-256 matches the release record"
					check.NextAction = ""
				} else {
					check.Detail = "running CLI binary SHA-256 does not match the release record"
					check.NextAction = "Run the CLI binary named by the selected installer release record."
				}
			}
		}
	}
	report.Checks = append(report.Checks, check)
	if check.Status == "blocked" {
		report.State = "blocked"
	}
	return report, nil
}
