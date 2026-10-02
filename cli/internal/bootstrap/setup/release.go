package setup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"oras.land/oras-go/v2/registry"
)

const ReleaseRecordSchemaVersion = 1
const maxReleaseRecordBytes = 4 << 20

// InstallerReleaseRecord is separate from Fleet's UI rollout records. Its
// artifact fields describe one compatible installer set, not a live pin state.
type InstallerReleaseRecord struct {
	SchemaVersion int              `json:"schemaVersion"`
	Kind          string           `json:"kind"`
	ReleaseID     string           `json:"releaseId"`
	Artifacts     ReleaseArtifacts `json:"artifacts"`
	Profiles      []ReleaseProfile `json:"profiles"`
}

type ReleaseArtifacts struct {
	CLI           ReleaseFile                `json:"cli"`
	StarterRef    string                     `json:"starterRef"`
	RKE2Version   string                     `json:"rke2Version"`
	PlatformChart ReleaseFile                `json:"platformChart"`
	BackendImage  string                     `json:"backendImage"`
	FrontendImage string                     `json:"frontendImage"`
	AdminImage    string                     `json:"adminImage"`
	ThemeArchives map[string]string          `json:"themeArchives"`
	RKE2Installer ReleaseDownload            `json:"rke2Installer,omitempty"`
	RKE2Archives  map[string]ReleaseDownload `json:"rke2Archives,omitempty"`
	Images        map[string]string          `json:"images,omitempty"`
	Files         map[string]ReleaseFile     `json:"files,omitempty"`
}

type ReleaseDownload struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type ReleaseFile struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type ReleaseProfile struct {
	ID                   string               `json:"id"`
	SupportedOS          []string             `json:"supportedOS"`
	Architectures        []string             `json:"architectures"`
	MinServers           int                  `json:"minServers"`
	MaxServers           int                  `json:"maxServers"`
	MinAgents            int                  `json:"minAgents"`
	MaxAgents            int                  `json:"maxAgents"`
	MinCPUPerHost        int                  `json:"minCPUPerHost"`
	MinMemoryKiBPerHost  uint64               `json:"minMemoryKiBPerHost"`
	MinFreeBytesPerHost  uint64               `json:"minFreeBytesPerHost"`
	NetworkPresets       []string             `json:"networkPresets"`
	ObjectStorageModes   []string             `json:"objectStorageModes"`
	RequiredTools        []string             `json:"requiredTools"`
	RequiredCapabilities []string             `json:"requiredCapabilities"`
	AcceptanceChecks     []string             `json:"acceptanceChecks"`
	Availability         string               `json:"availability"`
	Qualification        ReleaseQualification `json:"qualification"`
}

// Qualification names a result and a digest for external evidence. This
// local check does not verify the evidence or certify the profile.
type ReleaseQualification struct {
	RunID          string `json:"runId"`
	Result         string `json:"result"`
	EvidenceSHA256 string `json:"evidenceSHA256"`
}

type ReleaseCheck struct {
	ID         string `json:"id"`
	Status     string `json:"status"`
	Detail     string `json:"detail"`
	NextAction string `json:"nextAction,omitempty"`
}

// ReleaseReport records local consistency only. It never authorizes apply.
type ReleaseReport struct {
	SchemaVersion  int            `json:"schemaVersion"`
	State          string         `json:"state"`
	RecordComplete bool           `json:"recordComplete"`
	ReadyToApply   bool           `json:"readyToApply"`
	ReleaseID      string         `json:"releaseId,omitempty"`
	Profile        string         `json:"profile"`
	ReleaseSHA256  string         `json:"releaseSHA256"`
	InputHash      string         `json:"inputHash"`
	Checks         []ReleaseCheck `json:"checks"`
	Unresolved     []string       `json:"unresolved"`
}

var hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)
var digestImage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*@sha256:[0-9a-f]{64}$`)
var releaseIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var supportedOSPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]+:[A-Za-z0-9._+-]+$`)

func readReleaseRecord(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxReleaseRecordBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxReleaseRecordBytes {
		return nil, fmt.Errorf("record exceeds %d bytes", maxReleaseRecordBytes)
	}
	return data, nil
}

// InspectRelease checks the record bound by Compile. It fails closed if the
// file changed after compilation. External evidence and live pins remain open.
func InspectRelease(c Compiled) (ReleaseReport, error) {
	if err := checkCompiled(c); err != nil {
		return ReleaseReport{}, err
	}
	report := ReleaseReport{
		SchemaVersion: ReleaseRecordSchemaVersion,
		State:         "blocked", Profile: c.Spec.Profile,
		ReleaseSHA256: c.ReleaseSHA256, InputHash: c.InputHash,
		Checks:     []ReleaseCheck{},
		Unresolved: []string{"independent-qualification-evidence", "artifact-bytes-and-live-pins", "live-host-and-target-checks", "exact-effects-diff"},
	}
	data, err := readReleaseRecord(c.Spec.Release.RecordFile)
	if err != nil {
		return report, fmt.Errorf("release.recordFile: %w", err)
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != c.ReleaseSHA256 {
		return report, fmt.Errorf("release.recordFile changed; validate the setup specification again")
	}
	var record InstallerReleaseRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return report, fmt.Errorf("release.recordFile: %w", err)
	}
	if releaseIDPattern.MatchString(record.ReleaseID) {
		report.ReleaseID = record.ReleaseID
	}
	add := func(id string, ok bool, detail, next string) {
		status := "pass"
		if !ok {
			status = "blocked"
		}
		report.Checks = append(report.Checks, ReleaseCheck{ID: id, Status: status, Detail: detail, NextAction: next})
	}
	add("kind", record.Kind == "kube-dc-installer-release" && record.SchemaVersion == ReleaseRecordSchemaVersion && releaseIDPattern.MatchString(record.ReleaseID), "versioned installer record", "Use an installer release record with schemaVersion 1, kind kube-dc-installer-release, and a safe releaseId.")
	if record.Kind == "kube-dc-installer-release" {
		if unknown := unknownFields(data, reflect.TypeOf(InstallerReleaseRecord{}), ""); len(unknown) != 0 {
			add("schema-fields", false, "unknown field paths: "+strconv.Quote(strings.Join(unknown, ", ")), "Remove unsupported fields or use a supported schema version.")
		}
	}
	a := record.Artifacts
	add("artifacts.cli", a.CLI.Version != "" && hexSHA256.MatchString(a.CLI.SHA256), "CLI version and SHA-256", "Record the CLI version and binary SHA-256.")
	add("artifacts.starterRef", a.StarterRef == c.Spec.Release.StarterRef && clusterinit.ValidateStarterOCIRef(a.StarterRef) == nil, "starter digest matches the setup specification", "Pin the same immutable starter digest in the record and setup specification.")
	add("artifacts.rke2Version", a.RKE2Version != "" && a.RKE2Version == c.Spec.Release.RKE2Version, "RKE2 version matches the setup specification", "Use one RKE2 version in the record and setup specification.")
	add("artifacts.platformChart", a.PlatformChart.Version != "" && hexSHA256.MatchString(a.PlatformChart.SHA256), "platform chart version and SHA-256", "Record the chart version and archive SHA-256.")
	for _, image := range []struct{ id, ref string }{{"backendImage", a.BackendImage}, {"frontendImage", a.FrontendImage}, {"adminImage", a.AdminImage}} {
		add("artifacts."+image.id, validDigestImage(image.ref), "immutable image digest", "Record a valid image repository and @sha256: digest. Do not include a tag.")
	}
	for _, name := range []string{"kube-dc-theme.jar", "kube-dc-theme-provider.jar"} {
		add("artifacts.themeArchives."+name, hexSHA256.MatchString(a.ThemeArchives[name]), "theme archive SHA-256", "Record the published archive SHA-256.")
	}
	profileCount := 0
	seen := map[string]bool{}
	for _, profile := range record.Profiles {
		if seen[profile.ID] || !profileID.MatchString(profile.ID) {
			add("profiles.unique", false, "duplicate or invalid profile ID", "Use one entry per versioned profile ID.")
		}
		seen[profile.ID] = true
		if profile.ID != c.Spec.Profile {
			continue
		}
		profileCount++
		checkReleaseProfile(&report, c, profile)
	}
	add("profiles.selected", profileCount == 1, "selected profile appears once", "Add exactly one descriptor for the selected profile.")
	blocked := false
	for _, check := range report.Checks {
		if check.Status == "blocked" {
			blocked = true
			break
		}
	}
	if !blocked {
		report.State = "recorded"
		report.RecordComplete = true
	}
	return report, nil
}

func validDigestImage(ref string) bool {
	if !digestImage.MatchString(ref) {
		return false
	}
	parsed, err := registry.ParseReference(ref)
	return err == nil && parsed.String() == ref && parsed.ValidateReferenceAsDigest() == nil
}

// applyReleaseHostPolicy checks observed facts against a complete local
// profile descriptor. A missing or incomplete record leaves the inventory
// usable, but the unresolved release gate remains in the report.
func applyReleaseHostPolicy(c Compiled, inventory HostInventory) (HostInventory, error) {
	release, err := InspectRelease(c)
	if err != nil || !release.RecordComplete {
		return inventory, err
	}
	data, err := readReleaseRecord(c.Spec.Release.RecordFile)
	if err != nil {
		return inventory, err
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != c.ReleaseSHA256 {
		return inventory, fmt.Errorf("release.recordFile changed; validate the setup specification again")
	}
	var record InstallerReleaseRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return inventory, err
	}
	for _, profile := range record.Profiles {
		if profile.ID != c.Spec.Profile {
			continue
		}
		for i := range inventory.Hosts {
			host := &inventory.Hosts[i]
			facts := host.Facts
			observedOS := facts.OSID + ":" + facts.OSVersion
			profileHostCheck(host, "profile-os", contains(profile.SupportedOS, observedOS), "OS "+observedOS+"; supported: "+strings.Join(profile.SupportedOS, ", "))
			profileHostCheck(host, "profile-architecture", contains(profile.Architectures, facts.Architecture), "architecture "+facts.Architecture+"; supported: "+strings.Join(profile.Architectures, ", "))
			profileHostCheck(host, "profile-cpu", facts.CPUs >= profile.MinCPUPerHost, fmt.Sprintf("%d CPUs; profile minimum %d", facts.CPUs, profile.MinCPUPerHost))
			profileHostCheck(host, "profile-memory", facts.MemoryKiB >= profile.MinMemoryKiBPerHost, fmt.Sprintf("%d KiB memory; profile minimum %d KiB", facts.MemoryKiB, profile.MinMemoryKiBPerHost))
			profileHostCheck(host, "profile-free-space", facts.FreeBytes >= profile.MinFreeBytesPerHost, fmt.Sprintf("%d bytes free; profile minimum %d bytes", facts.FreeBytes, profile.MinFreeBytesPerHost))
			if host.State == "blocked" {
				inventory.State = "blocked"
			}
		}
		return inventory, nil
	}
	return inventory, fmt.Errorf("selected profile is absent from complete release record")
}

func profileHostCheck(host *HostObservation, id string, pass bool, detail string) {
	status := "blocked"
	if pass {
		status = "pass"
	}
	addHostCheck(host, id, status, detail)
}

func checkReleaseProfile(report *ReleaseReport, c Compiled, p ReleaseProfile) {
	add := func(id string, ok bool, detail, next string) {
		status := "pass"
		if !ok {
			status = "blocked"
		}
		report.Checks = append(report.Checks, ReleaseCheck{ID: "profile." + id, Status: status, Detail: detail, NextAction: next})
	}
	servers, agents := 0, 0
	for _, host := range c.Spec.Hosts {
		if host.Role == "server" {
			servers++
		} else {
			agents++
		}
	}
	add("topology", p.MinServers > 0 && p.MaxServers >= p.MinServers && servers >= p.MinServers && servers <= p.MaxServers && p.MinAgents >= 0 && p.MaxAgents >= p.MinAgents && agents >= p.MinAgents && agents <= p.MaxAgents, "selected host roles fit profile limits", "Check the server and agent count against the profile.")
	osSupported := cleanValues(p.SupportedOS)
	for _, value := range p.SupportedOS {
		osSupported = osSupported && supportedOSPattern.MatchString(value)
	}
	add("capacity", osSupported && cleanValues(p.Architectures) && p.MinCPUPerHost > 0 && p.MinMemoryKiBPerHost > 0 && p.MinFreeBytesPerHost > 0, "OS, architecture, and per-host floors declared", "Record measured host support and capacity floors.")
	add("network", contains(p.NetworkPresets, string(c.Init.Preset)), "selected network preset is listed", "Select a supported network preset or qualify it for the profile.")
	add("storage", contains(p.ObjectStorageModes, string(c.Init.RookMode)), "selected object storage mode is listed", "Select a supported storage mode or qualify it for the profile.")
	add("tools", len(p.RequiredTools) > 0 && cleanIDs(p.RequiredTools), "required workstation tools declared", "List required workstation tool IDs.")
	add("acceptance", len(p.AcceptanceChecks) > 0 && cleanIDs(p.AcceptanceChecks), "acceptance checks declared", "List the required acceptance check IDs.")
	add("availability", strings.TrimSpace(p.Availability) != "", "availability statement declared", "State the availability limits of this profile.")
	add("capabilities", len(p.RequiredCapabilities) > 0 && cleanIDs(p.RequiredCapabilities) && covers(c.Spec.Verification.Capabilities, p.RequiredCapabilities), "setup includes required capability checks", "Add the profile's required capabilities to verification.capabilities.")
	q := p.Qualification
	add("qualification", q.RunID != "" && q.Result == "passed" && hexSHA256.MatchString(q.EvidenceSHA256), "qualification result and evidence digest recorded", "Record a passed run ID and evidence SHA-256. Verify that evidence independently before release.")
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func cleanValues(values []string) bool {
	if len(values) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if value == "" || len(value) > 128 || strings.TrimSpace(value) != value || strings.IndexFunc(value, unicode.IsControl) >= 0 || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func cleanIDs(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if !capabilityID.MatchString(value) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func covers(selected, required []string) bool {
	missing := map[string]bool{}
	for _, id := range required {
		missing[id] = true
	}
	for _, id := range selected {
		delete(missing, id)
	}
	return len(missing) == 0
}
