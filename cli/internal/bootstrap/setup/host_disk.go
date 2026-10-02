package setup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

var selectedLoopDevice = regexp.MustCompile(`^/dev/loop[0-9]+$`)
var diskDeviceNumber = regexp.MustCompile(`^[0-9]+:[0-9]+$`)
var diskIdentifier = regexp.MustCompile(`^[A-Za-z0-9._:+-]{1,128}$`)
var diskHolderName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func isLoopSelectedDevice(path string) bool { return selectedLoopDevice.MatchString(path) }

// DiskFact records the selected device's observed state. A clean observation
// never permits erasure, and live identity must be checked again before use.
type DiskFact struct {
	SelectedPath      string `json:"selectedPath"`
	ResolvedPath      string `json:"resolvedPath,omitempty"`
	DeviceNumber      string `json:"deviceNumber,omitempty"`
	StableID          string `json:"stableId,omitempty"`
	WWN               string `json:"wwn,omitempty"`
	Serial            string `json:"serial,omitempty"`
	SizeBytes         uint64 `json:"sizeBytes,omitempty"`
	HolderCount       int    `json:"holderCount,omitempty"`
	ReadOnly          bool   `json:"readOnly,omitempty"`
	HasChildren       bool   `json:"hasChildren,omitempty"`
	HasMounts         bool   `json:"hasMounts,omitempty"`
	HasFilesystem     bool   `json:"hasFilesystem,omitempty"`
	HasPartitionTable bool   `json:"hasPartitionTable,omitempty"`
	SignatureCount    int    `json:"signatureCount,omitempty"`
}

func inspectSelectedDisk(result *HostObservation, read hostRead, selected string, intent clusterinit.Mode) {
	fact := &DiskFact{SelectedPath: selected}
	result.Facts.SelectedDisk = fact
	if !validSelectedDevicePath(selected) {
		addHostCheck(result, "selected-disk", "blocked", "selected device path is invalid")
		return
	}
	if isLoopSelectedDevice(selected) {
		addHostCheck(result, "selected-disk", "not-applicable", "the Fleet storage mode creates this loop-backed device; raw-disk checks do not apply")
		return
	}
	quoted := "'" + selected + "'" // validSelectedDevicePath excludes apostrophes.
	resolved := read("selected-disk", "readlink -e -- "+quoted)
	if resolved == "" {
		return
	}
	if !validSelectedDevicePath(resolved) {
		addHostCheck(result, "selected-disk", "blocked", "selected device resolves outside /dev or has an invalid path")
		return
	}
	fact.ResolvedPath = resolved
	quotedResolved := "'" + resolved + "'"
	output := read("selected-disk", "sudo -n lsblk -J -T -b -p -o PATH,TYPE,SIZE,FSTYPE,PTTYPE,MOUNTPOINTS,RO,MAJ:MIN,WWN,SERIAL -- "+quotedResolved)
	if output == "" {
		return
	}
	parsed, err := parseSelectedDisk(output, resolved)
	if err != nil {
		addHostCheck(result, "selected-disk", "blocked", "selected device inventory is invalid")
		return
	}
	*fact = parsed
	fact.SelectedPath = selected
	fact.ResolvedPath = resolved
	if strings.HasPrefix(selected, "/dev/disk/by-id/") {
		fact.StableID = "path:" + selected
	} else if fact.WWN != "" {
		fact.StableID = "wwn:" + fact.WWN
	}
	if fact.StableID == "" {
		addHostCheck(result, "selected-disk-identity", "unknown", "no by-id path or WWN identifies this disk")
	} else {
		addHostCheck(result, "selected-disk-identity", "pass", "stable ID observed; recheck it before provisioning")
	}
	holders := read("selected-disk", "sudo -n find /sys/dev/block/"+fact.DeviceNumber+"/holders -mindepth 1 -maxdepth 1 -printf '%f\\n' && printf 'KDC-HOLDERS-END\\n'")
	if holders == "" {
		return
	}
	holderCount, err := parseDiskHolderCount(holders)
	if err != nil {
		addHostCheck(result, "selected-disk", "blocked", "selected device holder probe is invalid")
		return
	}
	fact.HolderCount = holderCount
	signatures := read("selected-disk", "sudo -n wipefs -n --noheadings -O TYPE -- "+quotedResolved+" && printf 'KDC-WIPEFS-END\\n'")
	if signatures == "" {
		return
	}
	count, err := parseWipefsSignatureCount(signatures)
	if err != nil {
		addHostCheck(result, "selected-disk", "blocked", "selected device signature probe is invalid")
		return
	}
	fact.SignatureCount = count
	inUse := fact.HasFilesystem || fact.HasPartitionTable || fact.HasChildren || fact.HasMounts || holderCount != 0 || count != 0
	switch {
	case intent == clusterinit.ModeAdopt || intent == clusterinit.ModeResume:
		addHostCheck(result, "selected-disk", "not-applicable", "fresh-install disk emptiness does not apply to this target intent; verify existing storage ownership")
	case intent == clusterinit.ModeAuto && (fact.ReadOnly || inUse):
		addHostCheck(result, "selected-disk", "unknown", "selected disk is read-only or in use; resolve target mode before installation review")
	case fact.ReadOnly:
		addHostCheck(result, "selected-disk", "blocked", "selected device is read-only")
	case inUse:
		addHostCheck(result, "selected-disk", "blocked", "selected device has a filesystem, partition table, child device, holder, mount, or signature")
	default:
		addHostCheck(result, "selected-disk", "pass", "no filesystem, partition table, child device, holder, mount, or signature was reported; recheck before provisioning")
	}
}

type rawDiskNode struct {
	Path         string          `json:"path"`
	Type         string          `json:"type"`
	Size         json.RawMessage `json:"size"`
	FileSystem   string          `json:"fstype"`
	Partition    string          `json:"pttype"`
	Mountpoints  json.RawMessage `json:"mountpoints"`
	ReadOnly     json.RawMessage `json:"ro"`
	DeviceNumber string          `json:"maj:min"`
	WWN          string          `json:"wwn"`
	Serial       string          `json:"serial"`
	Model        string          `json:"model"`
	Rotational   json.RawMessage `json:"rota"`
	Transport    string          `json:"tran"`
	Children     []rawDiskNode   `json:"children"`
}

func parseSelectedDisk(output, resolved string) (DiskFact, error) {
	var inventory struct {
		BlockDevices []rawDiskNode `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(output), &inventory); err != nil || len(inventory.BlockDevices) != 1 {
		return DiskFact{}, fmt.Errorf("expected one block device")
	}
	device := inventory.BlockDevices[0]
	if device.Path != resolved || device.Type != "disk" {
		return DiskFact{}, fmt.Errorf("selected path is not a disk")
	}
	if !diskDeviceNumber.MatchString(device.DeviceNumber) {
		return DiskFact{}, fmt.Errorf("selected disk identity is invalid")
	}
	wwn := device.WWN
	if !diskIdentifier.MatchString(wwn) {
		wwn = ""
	}
	serial := device.Serial
	if !safeDiskSerial(serial) {
		serial = ""
	}
	size, err := parseDiskSize(device.Size)
	if err != nil || size == 0 {
		return DiskFact{}, fmt.Errorf("selected disk size is invalid")
	}
	readOnly, err := parseDiskReadOnly(device.ReadOnly)
	if err != nil {
		return DiskFact{}, err
	}
	mounted, err := parseDiskMounts(device.Mountpoints)
	if err != nil {
		return DiskFact{}, err
	}
	return DiskFact{
		DeviceNumber:      device.DeviceNumber,
		WWN:               wwn,
		Serial:            serial,
		SizeBytes:         size,
		ReadOnly:          readOnly,
		HasChildren:       len(device.Children) != 0,
		HasMounts:         mounted,
		HasFilesystem:     device.FileSystem != "",
		HasPartitionTable: device.Partition != "",
	}, nil
}

func safeDiskSerial(serial string) bool {
	if len(serial) > 256 || !utf8.ValidString(serial) {
		return false
	}
	return strings.IndexFunc(serial, func(r rune) bool { return !unicode.IsPrint(r) }) < 0
}

func parseDiskHolderCount(output string) (int, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 || lines[len(lines)-1] != "KDC-HOLDERS-END" || len(lines) > 129 {
		return 0, fmt.Errorf("invalid holder-probe terminator or count")
	}
	for _, holder := range lines[:len(lines)-1] {
		if !diskHolderName.MatchString(holder) {
			return 0, fmt.Errorf("invalid holder name")
		}
	}
	return len(lines) - 1, nil
}

func parseDiskSize(raw json.RawMessage) (uint64, error) {
	value := string(bytes.TrimSpace(raw))
	if strings.HasPrefix(value, `"`) {
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return 0, err
		}
		value = decoded
	}
	return strconv.ParseUint(value, 10, 64)
}

func parseDiskReadOnly(raw json.RawMessage) (bool, error) {
	switch string(bytes.TrimSpace(raw)) {
	case "true", "1", `"1"`:
		return true, nil
	case "false", "0", `"0"`:
		return false, nil
	default:
		return false, fmt.Errorf("unknown read-only state")
	}
}

func parseDiskMounts(raw json.RawMessage) (bool, error) {
	value := bytes.TrimSpace(raw)
	if bytes.Equal(value, []byte("null")) {
		return false, nil
	}
	var points []*string
	if err := json.Unmarshal(value, &points); err != nil {
		return false, err
	}
	for _, point := range points {
		if point != nil && *point != "" {
			return true, nil
		}
	}
	return false, nil
}

func parseWipefsSignatureCount(output string) (int, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 || lines[len(lines)-1] != "KDC-WIPEFS-END" {
		return 0, fmt.Errorf("missing signature-probe terminator")
	}
	count := 0
	for _, line := range lines[:len(lines)-1] {
		if strings.TrimSpace(line) == "" {
			return 0, fmt.Errorf("invalid signature-probe output")
		}
		count++
	}
	return count, nil
}
