package setup

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
)

// Host probes use fixed commands. A result is only an observation; profile
// capacity and live target checks must still run before an install can begin.
type hostRead func(id, command string) string

const freeSpaceCommand = "df -B1 -P /var/lib"
const clockCommand = "timedatectl show -p NTPSynchronized --value"
const listeningPortsCommand = "ss -H -lnt '( sport = :2379 or sport = :2380 or sport = :5001 or sport = :6443 or sport = :9345 or sport = :10250 )' && printf 'KDC-SS-END\\n'"
const kvmDeviceCommand = "if test -c /dev/kvm; then printf present; else printf absent; fi"

func inspectHostEnvironment(result *HostObservation, read hostRead, host Host, intent clusterinit.Mode, kvmRequired bool) {
	if value := read("free-space", freeSpaceCommand); value != "" {
		free, err := parseFreeBytes(value)
		if err != nil {
			addHostCheck(result, "free-space", "blocked", "cannot read free space on /var/lib")
		} else {
			result.Facts.FreeBytes = free
			if free == 0 {
				addHostCheck(result, "free-space", "blocked", "no free space on /var/lib")
			} else {
				addHostCheck(result, "free-space", "unknown", fmt.Sprintf("%.1f GiB free on /var/lib; profile floor is not qualified", float64(free)/(1<<30)))
			}
		}
	}
	if value := read("clock", clockCommand); value != "" {
		if value != "yes" && value != "no" {
			addHostCheck(result, "clock", "blocked", "clock synchronization result is invalid")
		} else {
			result.Facts.ClockSynced = value
			if value == "no" {
				addHostCheck(result, "clock", "blocked", "host clock is not synchronized")
			} else {
				addHostCheck(result, "clock", "pass", "host clock is synchronized")
			}
		}
	}
	if value := read("kvm-device", kvmDeviceCommand); value != "" {
		if value != "present" && value != "absent" {
			addHostCheck(result, "kvm-device", "blocked", "KVM device result is invalid")
		} else {
			result.Facts.KVMDevice = value
			switch {
			case value == "present":
				addHostCheck(result, "kvm-device", "pass", "/dev/kvm is present")
			case kvmRequired:
				addHostCheck(result, "kvm-device", "unknown", "/dev/kvm is absent on this host; another host can provide VM capacity")
			default:
				addHostCheck(result, "kvm-device", "not-applicable", "/dev/kvm is absent; VMs are outside the selected setup scope")
			}
		}
	}
	if value := read("tcp-ports", listeningPortsCommand); value != "" {
		ports, err := parseListeningTCPPorts(value)
		if err != nil {
			addHostCheck(result, "tcp-ports", "blocked", "cannot read TCP listeners")
		} else {
			result.Facts.ReservedTCPPortsInUse = ports
			required := requiredHostPorts(host.Role)
			var occupied []string
			for _, port := range required {
				index := sort.SearchInts(ports, port)
				if index < len(ports) && ports[index] == port {
					occupied = append(occupied, strconv.Itoa(port))
				}
			}
			switch {
			case intent == clusterinit.ModeInstall && len(occupied) != 0:
				addHostCheck(result, "tcp-ports", "blocked", "required TCP ports have listeners: "+strings.Join(occupied, ", "))
			case intent == clusterinit.ModeAuto && len(occupied) != 0:
				addHostCheck(result, "tcp-ports", "unknown", "required TCP ports have listeners: "+strings.Join(occupied, ", ")+"; target mode is not resolved")
			case intent == clusterinit.ModeAdopt || intent == clusterinit.ModeResume:
				addHostCheck(result, "tcp-ports", "not-applicable", "fresh-install port check does not apply to this target intent")
			default:
				addHostCheck(result, "tcp-ports", "pass", "required TCP ports have no listeners")
			}
		}
	}
}

func parseFreeBytes(output string) (uint64, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		return 0, fmt.Errorf("expected one filesystem row")
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 6 {
		return 0, fmt.Errorf("incomplete filesystem row")
	}
	return strconv.ParseUint(fields[len(fields)-3], 10, 64)
}

func parseListeningTCPPorts(output string) ([]int, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 || lines[len(lines)-1] != "KDC-SS-END" {
		return nil, fmt.Errorf("missing TCP-listener terminator")
	}
	seen := make(map[int]bool, len(lines))
	for _, line := range lines[:len(lines)-1] {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "LISTEN" {
			return nil, fmt.Errorf("invalid TCP-listener row")
		}
		local := fields[3]
		colon := strings.LastIndexByte(local, ':')
		if colon == -1 {
			return nil, fmt.Errorf("TCP-listener address has no port")
		}
		port, err := strconv.Atoi(local[colon+1:])
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid TCP-listener port")
		}
		reserved := requiredHostPorts("server")
		index := sort.SearchInts(reserved, port)
		if index == len(reserved) || reserved[index] != port {
			return nil, fmt.Errorf("TCP-listener output includes an unexpected port")
		}
		seen[port] = true
	}
	ports := make([]int, 0, len(seen))
	for port := range seen {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports, nil
}

func requiredHostPorts(role string) []int {
	if role == "server" {
		return []int{2379, 2380, 5001, 6443, 9345, 10250}
	}
	return []int{5001, 10250}
}
