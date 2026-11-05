package clusterinit

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// KeyCephDevices carries topology that the legacy one-device-per-host slots
// cannot express. Older CLIs reject this orchestration key instead of selecting
// a smaller set of disks. Generated Fleet overlays use ordinary device slots.
const KeyCephDevices = InitPrefix + "CEPH_DEVICES"

type cephDevicesInput struct {
	Version int                 `json:"version"`
	Nodes   map[string][]string `json:"nodes"`
}

func parseCephDevices(raw string) (map[string][]string, error) {
	var input cephDevicesInput
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&input); err != nil || input.Version != 1 || len(input.Nodes) == 0 {
		return nil, fmt.Errorf("%s requires version 1 and a nodes map of device lists", KeyCephDevices)
	}
	// Unmarshal also rejects trailing JSON values.
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return nil, fmt.Errorf("%s must contain one JSON object", KeyCephDevices)
	}
	if err := validateCephDeviceMap(input.Nodes); err != nil {
		return nil, err
	}
	return canonicalCephDevices(nil, input.Nodes), nil
}

func canonicalCephDevices(legacy map[string]string, selected map[string][]string) map[string][]string {
	out := map[string][]string{}
	for node, device := range legacy {
		out[node] = []string{strings.TrimPrefix(strings.TrimSpace(device), "/dev/")}
	}
	for node, devices := range selected {
		out[node] = nil
		for _, device := range devices {
			out[node] = append(out[node], strings.TrimPrefix(strings.TrimSpace(device), "/dev/"))
		}
		sort.Strings(out[node])
	}
	return out
}

func validateCephDeviceMap(nodes map[string][]string) error {
	count := 0
	for _, node := range cephNodeNames(nodes) {
		if !k8sNodeNameRegex.MatchString(node) || len(nodes[node]) == 0 {
			return fmt.Errorf("%s entry is not a valid Kubernetes node name or has no devices", KeyCephDevices)
		}
		seen := map[string]bool{}
		for _, device := range nodes[node] {
			device = strings.TrimPrefix(strings.TrimSpace(device), "/dev/")
			if !deviceNameRegex.MatchString(device) || strings.HasPrefix(device, "/") || device == "." || device == ".." || strings.HasPrefix(device, "../") || filepath.Clean(device) != device || loopDeviceRegex.MatchString(device) || seen[device] {
				return fmt.Errorf("%s entry is not a valid device name or repeats a raw device path", KeyCephDevices)
			}
			seen[device] = true
			count++
		}
	}
	if count > MaxCephSlots {
		return fmt.Errorf("%s supports at most %d selected devices", KeyCephDevices, MaxCephSlots)
	}
	return nil
}

// ImportCephDevices validates the versioned CLI/config representation.
func (o *InitOptions) ImportCephDevices(raw string) error {
	devices, err := parseCephDevices(raw)
	if err == nil {
		o.CephDevices = devices
	}
	return err
}

// DeviceSets returns a copy of the complete per-host raw device selection.
func (s ObjectStorageSpec) DeviceSets() map[string][]string {
	return canonicalCephDevices(s.CephNodes, s.CephDevices)
}

// SetCephDevices uses legacy slots for a single disk per host and a versioned
// topology for larger selections. It replaces the complete previous selection.
func (o *InitOptions) SetCephDevices(nodes map[string][]string) {
	o.CephNodes, o.CephDevices = nil, nil
	for _, devices := range nodes {
		if len(devices) > 1 {
			o.CephDevices = canonicalCephDevices(nil, nodes)
			return
		}
	}
	o.CephNodes = map[string]string{}
	for node, devices := range nodes {
		if len(devices) == 1 {
			o.CephNodes[node] = strings.TrimPrefix(strings.TrimSpace(devices[0]), "/dev/")
		}
	}
}

func (s ObjectStorageSpec) deviceSlots() [][2]string {
	nodes := s.DeviceSets()
	var out [][2]string
	for _, node := range cephNodeNames(nodes) {
		for _, device := range nodes[node] {
			out = append(out, [2]string{node, device})
		}
	}
	return out
}

func cephNodeNames(nodes map[string][]string) []string {
	names := make([]string, 0, len(nodes))
	for name := range nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ValidateDeviceListField accepts the same paths as raw-disk CLI inputs.
func ValidateDeviceListField(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return validateCephDeviceMap(map[string][]string{"selected-node": strings.Split(raw, ",")})
}

var cephSlotKey = regexp.MustCompile(`^CEPH_NODE_([0-9]+)(_DEVICE)?$`)

func validateCephInputs(values map[string]string) error {
	var selected map[string][]string
	if raw := values[KeyCephDevices]; raw != "" {
		var err error
		selected, err = parseCephDevices(raw)
		if err != nil {
			return err
		}
	}
	for key := range values {
		if !strings.HasPrefix(key, "CEPH_NODE_") {
			continue
		}
		match := cephSlotKey.FindStringSubmatch(key)
		if match == nil {
			return fmt.Errorf("unknown storage slot key %s", key)
		}
		n, _ := strconv.Atoi(match[1])
		if n < 1 || n > MaxCephSlots || strconv.Itoa(n) != match[1] {
			return fmt.Errorf("CEPH_NODE slots must be numbered 1 to %d", MaxCephSlots)
		}
	}
	seen := map[string]bool{}
	expected := (ObjectStorageSpec{CephDevices: selected}).deviceSlots()
	for i := 1; i <= MaxCephSlots; i++ {
		key := fmt.Sprintf("CEPH_NODE_%d", i)
		node := strings.TrimSpace(values[key])
		dev := strings.TrimPrefix(strings.TrimSpace(values[key+"_DEVICE"]), "/dev/")
		if node == "" && dev == "" {
			continue
		}
		if node == "" || dev == "" {
			return fmt.Errorf("%s requires both host and device", key)
		}
		if len(selected) > 0 {
			if i > len(expected) || expected[i-1] != [2]string{node, dev} {
				return fmt.Errorf("legacy slots conflict with %s", KeyCephDevices)
			}
		} else if seen[node] {
			return fmt.Errorf("repeated storage host in legacy slots; use the versioned %s device list", KeyCephDevices)
		}
		seen[node] = true
	}
	return nil
}
