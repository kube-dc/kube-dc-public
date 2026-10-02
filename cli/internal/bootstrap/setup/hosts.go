package setup

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

const HostInventorySchemaVersion = 1
const hostProbeTimeout = 30 * time.Second
const maxHostProbeParallelism = 4
const maxHostProbeOutput = 128 << 10

var machineIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
var osFactPattern = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)
var nicNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.:@-]+$`)

// HostInventory contains read-only SSH observations. It cannot authorize an
// install: profile floors, disk safety, host-key evidence, and live target
// identity still need qualification and review.
type HostInventory struct {
	SchemaVersion  int               `json:"schemaVersion"`
	State          string            `json:"state"`
	ReadyToInstall bool              `json:"readyToInstall"`
	InputHash      string            `json:"inputHash"`
	Hosts          []HostObservation `json:"hosts"`
	Checks         []HostCheck       `json:"checks,omitempty"`
	Unresolved     []string          `json:"unresolved"`
}

type HostObservation struct {
	ID         string      `json:"id"`
	Role       string      `json:"role"`
	SSHAlias   string      `json:"sshAlias"`
	State      string      `json:"state"`
	ObservedAt time.Time   `json:"observedAt"`
	Facts      HostFacts   `json:"facts"`
	Checks     []HostCheck `json:"checks"`
}

type HostFacts struct {
	MachineID             string                    `json:"machineId,omitempty"`
	HostKey               *ports.SSHHostKeyEvidence `json:"hostKey,omitempty"`
	OSID                  string                    `json:"osId,omitempty"`
	OSVersion             string                    `json:"osVersion,omitempty"`
	Architecture          string                    `json:"architecture,omitempty"`
	CPUs                  int                       `json:"cpus,omitempty"`
	MemoryKiB             uint64                    `json:"memoryKiB,omitempty"`
	FreeBytes             uint64                    `json:"freeBytes,omitempty"`
	ClockSynced           string                    `json:"clockSynced,omitempty"`
	PasswordlessSudo      string                    `json:"passwordlessSudo,omitempty"`
	KVMDevice             string                    `json:"kvmDevice,omitempty"`
	NICs                  []NICFact                 `json:"nics,omitempty"`
	ReservedTCPPortsInUse []int                     `json:"reservedTcpPortsInUse,omitempty"`
	SelectedDisk          *DiskFact                 `json:"selectedDisk,omitempty"`
	RKE2Server            string                    `json:"rke2Server,omitempty"`
	RKE2Agent             string                    `json:"rke2Agent,omitempty"`
	RKE2ServerLoad        string                    `json:"rke2ServerLoad,omitempty"`
	RKE2AgentLoad         string                    `json:"rke2AgentLoad,omitempty"`
	RKE2Files             string                    `json:"rke2Files,omitempty"`
}

type NICFact struct {
	Name      string   `json:"name"`
	MAC       string   `json:"mac,omitempty"`
	MTU       int      `json:"mtu,omitempty"`
	Addresses []string `json:"addresses,omitempty"`
	Prefixes  []string `json:"prefixes,omitempty"`
}

type HostCheck struct {
	ID         string    `json:"id"`
	Scope      string    `json:"scope"`
	Resource   string    `json:"resource"`
	Status     string    `json:"status"`
	Detail     string    `json:"detail"`
	NextAction string    `json:"nextAction,omitempty"`
	ObservedAt time.Time `json:"observedAt"`
}

// InspectHosts uses only fixed read commands. The production SSH adapter
// checks known_hosts strictly; this function never enrolls a new key.
func InspectHosts(ctx context.Context, c Compiled, ssh ports.CappedSSHClient) (HostInventory, error) {
	if err := checkCompiled(c); err != nil {
		return HostInventory{}, err
	}
	if ssh == nil {
		return HostInventory{}, fmt.Errorf("host inventory requires an SSH client")
	}
	hosts := append([]Host(nil), c.Spec.Hosts...)
	kvmRequired := !c.Init.NoKubeVirt && !c.Init.AllowNoKubevirtEligible
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })
	report := HostInventory{
		SchemaVersion:  HostInventorySchemaVersion,
		State:          "observed",
		ReadyToInstall: false,
		InputHash:      c.InputHash,
		Hosts:          make([]HostObservation, len(hosts)),
		Unresolved: []string{
			"qualified-release-and-profile",
			"operator-confirmed-host-key-fingerprints",
			"capacity-and-port-policy-and-recheck",
			"inter-host-and-external-connectivity",
			"live-target-identity",
			"exact-effects-diff",
		},
	}
	rawDisk, loopDisk := false, false
	for _, host := range hosts {
		if host.Disk != "" {
			if isLoopSelectedDevice(host.Disk) {
				loopDisk = true
			} else {
				rawDisk = true
			}
		}
	}
	if rawDisk {
		report.Unresolved = append(report.Unresolved, "selected-disk-stable-identity-and-recheck")
	}
	if loopDisk {
		report.Unresolved = append(report.Unresolved, "loop-backed-storage-qualification")
	}
	if kvmRequired {
		report.Unresolved = append(report.Unresolved, "kubevirt-runtime-eligibility")
	}
	jobs := make(chan int, len(hosts))
	for i := range hosts {
		jobs <- i
	}
	close(jobs)
	var wg sync.WaitGroup
	workers := maxHostProbeParallelism
	if len(hosts) < workers {
		workers = len(hosts)
	}
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				host := hosts[i]
				if err := ctx.Err(); err != nil {
					report.Hosts[i] = blockedHost(host, "connection", err.Error())
					continue
				}
				hostCtx, cancel := context.WithTimeout(ctx, hostProbeTimeout)
				probeHost := host
				probeHost.NIC = configuredHostNIC(c, host)
				report.Hosts[i] = inspectHost(hostCtx, ssh, probeHost, c.Spec.Target.Intent, kvmRequired)
				cancel()
			}
		}()
	}
	wg.Wait()
	seenMachines := map[string]string{}
	for i := range report.Hosts {
		observed := &report.Hosts[i]
		if observed.Facts.MachineID != "" {
			if first, exists := seenMachines[observed.Facts.MachineID]; exists {
				addHostCheck(observed, "machine-identity", "blocked", "same machine ID as "+first)
			} else {
				seenMachines[observed.Facts.MachineID] = observed.ID
			}
		}
		if observed.State == "blocked" {
			report.State = "blocked"
		}
	}
	allHostKeysPinned := len(report.Hosts) > 0
	for _, observed := range report.Hosts {
		pinned := false
		for _, check := range observed.Checks {
			if check.ID == "host-key" {
				if check.Status != "pass" {
					pinned = false
					break
				}
				pinned = true
			}
		}
		allHostKeysPinned = allHostKeysPinned && pinned
	}
	if allHostKeysPinned {
		for i, unresolved := range report.Unresolved {
			if unresolved == "operator-confirmed-host-key-fingerprints" {
				report.Unresolved = append(report.Unresolved[:i], report.Unresolved[i+1:]...)
				break
			}
		}
	}
	if kvmRequired {
		present, missing := 0, 0
		for _, observed := range report.Hosts {
			switch observed.Facts.KVMDevice {
			case "present":
				present++
			case "absent":
				missing++
			}
		}
		check := HostCheck{ID: "kvm-capacity", Scope: "cluster", Resource: c.Spec.Name, ObservedAt: time.Now().UTC()}
		if present != 0 {
			check.Status = "pass"
			check.Detail = fmt.Sprintf("%d host(s) expose /dev/kvm; Kubernetes eligibility still needs verification", present)
		} else {
			check.Status = "blocked"
			check.Detail = "no host with /dev/kvm was confirmed"
			if missing == len(report.Hosts) {
				check.Detail = "no selected host exposes /dev/kvm"
			}
			check.NextAction = "Enable hardware virtualization on at least one host and check /dev/kvm again."
			report.State = "blocked"
		}
		report.Checks = append(report.Checks, check)
	}
	return applyReleaseHostPolicy(c, report)
}

func inspectHost(ctx context.Context, ssh ports.CappedSSHClient, host Host, intent clusterinit.Mode, kvmRequired bool) HostObservation {
	return inspectHostWithPolicy(ctx, ssh, host, intent, kvmRequired, false)
}

func inspectHostWithPolicy(ctx context.Context, ssh ports.CappedSSHClient, host Host, intent clusterinit.Mode, kvmRequired, platformInit bool) HostObservation {
	environmentIntent := intent
	if platformInit {
		environmentIntent = clusterinit.ModeAdopt
	}
	result := HostObservation{ID: host.ID, Role: host.Role, SSHAlias: host.SSHAlias, State: "observed", ObservedAt: time.Now().UTC(), Checks: []HostCheck{}}
	endpoint := sshHost(host.SSHAlias)
	endpoint.ExpectedHostKeySHA256 = host.HostKeySHA256
	var verifiedKey ports.SSHHostKeyEvidence
	keyChanged := false
	read := func(id, command string) string {
		if keyChanged {
			return ""
		}
		var out []byte
		var err error
		if withKey, ok := ssh.(ports.CappedSSHHostKeyClient); ok {
			var key ports.SSHHostKeyEvidence
			out, key, err = withKey.RunCappedWithHostKey(ctx, endpoint, command, maxHostProbeOutput)
			if id == "machine-identity" {
				verifiedKey = key
			} else if verifiedKey.FingerprintSHA256 != "" && (err == nil || key.FingerprintSHA256 != "") {
				if key.FingerprintSHA256 != verifiedKey.FingerprintSHA256 || key.Algorithm != verifiedKey.Algorithm || key.Address != verifiedKey.Address {
					addHostCheck(&result, "host-key", "blocked", "target host key changed between inventory reads")
					keyChanged = true
					return ""
				}
			} else if key.FingerprintSHA256 != "" {
				addHostCheck(&result, "host-key", "blocked", "first SSH read had no host-key evidence")
				keyChanged = true
				return ""
			}
		} else {
			out, err = ssh.RunCapped(ctx, endpoint, command, maxHostProbeOutput)
		}
		if err != nil {
			addHostCheck(&result, id, "blocked", "remote read failed: "+safeHostText(err.Error()))
			return ""
		}
		if len(out) > maxHostProbeOutput {
			addHostCheck(&result, id, "blocked", "remote output exceeded the size limit")
			return ""
		}
		value := strings.TrimSpace(string(out))
		if value == "" {
			addHostCheck(&result, id, "blocked", "remote read returned no value")
		}
		return value
	}
	if value := read("machine-identity", "cat /etc/machine-id"); value != "" {
		if !machineIDPattern.MatchString(value) {
			addHostCheck(&result, "machine-identity", "blocked", "invalid machine ID")
		} else {
			result.Facts.MachineID = value
			addHostCheck(&result, "machine-identity", "pass", "machine ID observed")
		}
	}
	if verifiedKey.FingerprintSHA256 != "" {
		result.Facts.HostKey = &verifiedKey
	}
	switch {
	case verifiedKey.FingerprintSHA256 == "" && host.HostKeySHA256 != "":
		addHostCheck(&result, "host-key", "blocked", "SSH adapter did not return target host-key evidence")
		return result
	case verifiedKey.FingerprintSHA256 == "":
		addHostCheck(&result, "host-key", "unknown", "target host-key fingerprint was not captured")
	case host.HostKeySHA256 == "":
		addHostCheck(&result, "host-key", "unknown", "verified target fingerprint requires out-of-band review and a spec pin")
	case verifiedKey.FingerprintSHA256 != host.HostKeySHA256:
		addHostCheck(&result, "host-key", "blocked", "verified target fingerprint does not match the setup spec")
		return result
	default:
		addHostCheck(&result, "host-key", "pass", "verified target fingerprint matches the setup spec")
	}
	if value := read("operating-system", "cat /etc/os-release"); value != "" {
		result.Facts.OSID, result.Facts.OSVersion = parseOSRelease(value)
		if !osFactPattern.MatchString(result.Facts.OSID) || !osFactPattern.MatchString(result.Facts.OSVersion) {
			result.Facts.OSID, result.Facts.OSVersion = "", ""
			addHostCheck(&result, "operating-system", "blocked", "OS ID or version is missing or invalid")
		} else {
			addHostCheck(&result, "operating-system", "pass", result.Facts.OSID+" "+result.Facts.OSVersion)
		}
	}
	if value := read("architecture", "uname -m"); value != "" {
		if !osFactPattern.MatchString(value) {
			addHostCheck(&result, "architecture", "blocked", "invalid architecture value")
		} else {
			result.Facts.Architecture = value
			addHostCheck(&result, "architecture", "pass", value)
		}
	}
	if value := read("cpu", "getconf _NPROCESSORS_ONLN"); value != "" {
		count, err := strconv.Atoi(value)
		if err != nil || count < 1 {
			addHostCheck(&result, "cpu", "blocked", "invalid CPU count")
		} else {
			result.Facts.CPUs = count
			addHostCheck(&result, "cpu", "pass", value+" CPUs observed")
		}
	}
	if value := read("memory", "awk '/^MemTotal:/ {print $2}' /proc/meminfo"); value != "" {
		kib, err := strconv.ParseUint(value, 10, 64)
		if err != nil || kib == 0 {
			addHostCheck(&result, "memory", "blocked", "invalid memory size")
		} else {
			result.Facts.MemoryKiB = kib
			addHostCheck(&result, "memory", "pass", value+" KiB observed")
		}
	}
	if value := read("privilege", "if sudo -n true >/dev/null 2>&1; then printf yes; else printf no; fi"); value != "" {
		if value == "yes" {
			result.Facts.PasswordlessSudo = value
			addHostCheck(&result, "privilege", "pass", "passwordless sudo observed")
		} else if value == "no" {
			result.Facts.PasswordlessSudo = value
			addHostCheck(&result, "privilege", "blocked", "passwordless sudo is required by the installer")
		} else {
			addHostCheck(&result, "privilege", "blocked", "invalid sudo result")
		}
	}
	inspectHostEnvironment(&result, read, host, environmentIntent, kvmRequired)
	if value := read("network", "ip -j addr show"); value != "" {
		nics, err := parseNICFacts(value)
		if err != nil {
			addHostCheck(&result, "network", "blocked", "invalid IP address inventory")
		} else {
			result.Facts.NICs = nics
			addressPresent, nicPresent := host.ManagementAddress == "", host.NIC == ""
			for _, nic := range nics {
				if nic.Name == host.NIC {
					nicPresent = true
				}
				for _, address := range nic.Addresses {
					if net.ParseIP(address).Equal(net.ParseIP(host.ManagementAddress)) {
						addressPresent = true
					}
				}
			}
			if !addressPresent || !nicPresent {
				addHostCheck(&result, "network", "blocked", "selected management address or NIC is absent")
			} else {
				addHostCheck(&result, "network", "pass", "selected management address and NIC observed")
			}
		}
	}
	for _, service := range []struct{ id, command string }{
		{"rke2-server", "systemctl show -p LoadState -p ActiveState rke2-server"},
		{"rke2-agent", "systemctl show -p LoadState -p ActiveState rke2-agent"},
	} {
		output := read(service.id, service.command)
		if output == "" {
			continue
		}
		load, value := parseSystemdState(output)
		if service.id == "rke2-server" {
			result.Facts.RKE2Server = value
			result.Facts.RKE2ServerLoad = load
		} else {
			result.Facts.RKE2Agent = value
			result.Facts.RKE2AgentLoad = load
		}
		if (load != "loaded" && load != "not-found") || (value != "active" && value != "inactive" && value != "failed" && value != "activating" && value != "deactivating") {
			addHostCheck(&result, service.id, "blocked", "unknown service state")
		} else if value == "active" && ((service.id == "rke2-server" && host.Role != "server") || (service.id == "rke2-agent" && host.Role != "agent")) {
			addHostCheck(&result, service.id, "blocked", "active RKE2 role conflicts with selected host role")
		} else if environmentIntent == clusterinit.ModeInstall && load != "not-found" {
			addHostCheck(&result, service.id, "blocked", "RKE2 unit exists; use adopt or resume review")
		} else {
			addHostCheck(&result, service.id, "pass", "unit "+load+", service "+value)
		}
	}
	if value := read("rke2-files", "if test -e /etc/rancher/rke2/config.yaml || test -e /var/lib/rancher/rke2; then printf present; else printf absent; fi"); value != "" {
		result.Facts.RKE2Files = value
		if value != "present" && value != "absent" {
			addHostCheck(&result, "rke2-files", "blocked", "unknown RKE2 file state")
		} else if value == "present" && environmentIntent == clusterinit.ModeInstall {
			addHostCheck(&result, "rke2-files", "blocked", "RKE2 files exist; use adopt or resume review")
		} else {
			addHostCheck(&result, "rke2-files", "pass", "RKE2 files: "+value)
		}
	}
	if host.Disk != "" {
		inspectSelectedDisk(&result, read, host.Disk, intent)
	}
	return result
}

func parseSystemdState(output string) (load, active string) {
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "LoadState":
			load = value
		case "ActiveState":
			active = value
		}
	}
	return load, active
}

func blockedHost(host Host, id, detail string) HostObservation {
	now := time.Now().UTC()
	result := HostObservation{ID: host.ID, Role: host.Role, SSHAlias: host.SSHAlias, State: "blocked", ObservedAt: now}
	addHostCheck(&result, id, "blocked", detail)
	return result
}

func addHostCheck(host *HostObservation, id, status, detail string) {
	check := HostCheck{ID: id, Scope: "host", Resource: host.ID, Status: status, Detail: detail, ObservedAt: time.Now().UTC()}
	if id == "selected-disk" || id == "selected-disk-identity" {
		check.Scope = "disk"
		if host.Facts.SelectedDisk != nil {
			check.Resource = host.Facts.SelectedDisk.SelectedPath
		}
	}
	if id == "network" {
		check.Scope = "network-interface"
	}
	if status == "blocked" || status == "unknown" {
		check.NextAction = hostCheckNextAction(id)
	}
	host.Checks = append(host.Checks, check)
	if status == "blocked" {
		host.State = "blocked"
	}
}

func hostCheckNextAction(id string) string {
	switch id {
	case "connection":
		return "Check the SSH alias, credentials, host key, and network route. Then run the inventory again."
	case "host-key":
		return "Verify the target fingerprint outside this SSH connection, set hosts[].hostKeySHA256, and run the inventory again."
	case "selected-disk-identity":
		return "Select a /dev/disk/by-id/ path or a disk with a WWN. Recheck its identity before provisioning."
	case "machine-identity":
		return "Check /etc/machine-id. Use one distinct host for each inventory entry."
	case "operating-system", "architecture", "profile-os", "profile-architecture":
		return "Check the host OS and architecture. Use a host that the release profile supports."
	case "cpu", "memory", "free-space", "profile-cpu", "profile-memory", "profile-free-space":
		return "Check host capacity against a qualified release profile."
	case "privilege":
		return "Configure passwordless sudo for the SSH account. Then check access again."
	case "clock":
		return "Configure time synchronization on the host. Then check the clock again."
	case "kvm-device":
		return "Enable hardware virtualization on a selected host, or mark VMs out of scope in the setup configuration."
	case "tcp-ports":
		return "Inspect the TCP listeners. Stop conflicting services for a fresh install, or review adopt or resume intent."
	case "network":
		return "Check the management IP address and selected external network interface on this host."
	case "rke2-server", "rke2-agent", "rke2-files":
		return "Inspect the existing RKE2 state. Select adopt or resume if this host belongs to a cluster."
	case "selected-disk":
		return "Inspect the selected device. Choose an unused disk and check its identity again before provisioning."
	default:
		return "Check the host and run the inventory again."
	}
}

func sshHost(alias string) ports.SSHHost {
	host, _ := ports.ParseSSHHostTarget(alias) // Compiled specs validate aliases.
	return host
}

func parseOSRelease(data string) (id, version string) {
	for _, line := range strings.Split(data, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				continue
			}
			value = decoded
		}
		switch key {
		case "ID":
			id = value
		case "VERSION_ID":
			version = value
		}
	}
	return id, version
}

func parseNICFacts(data string) ([]NICFact, error) {
	var raw []struct {
		Name      string `json:"ifname"`
		MAC       string `json:"address"`
		MTU       int    `json:"mtu"`
		Addresses []struct {
			Local     string `json:"local"`
			PrefixLen *int   `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		return nil, err
	}
	nics := make([]NICFact, 0, len(raw))
	for _, nic := range raw {
		if !nicNamePattern.MatchString(nic.Name) {
			return nil, fmt.Errorf("invalid interface name")
		}
		fact := NICFact{Name: nic.Name, MTU: nic.MTU}
		if _, err := net.ParseMAC(nic.MAC); err == nil {
			fact.MAC = nic.MAC
		}
		for _, addr := range nic.Addresses {
			if net.ParseIP(addr.Local) != nil {
				fact.Addresses = append(fact.Addresses, addr.Local)
				if addr.PrefixLen != nil {
					bits := 128
					if net.ParseIP(addr.Local).To4() != nil {
						bits = 32
					}
					if *addr.PrefixLen < 0 || *addr.PrefixLen > bits {
						return nil, fmt.Errorf("invalid interface prefix")
					}
					fact.Prefixes = append(fact.Prefixes, fmt.Sprintf("%s/%d", addr.Local, *addr.PrefixLen))
				}
			}
		}
		sort.Strings(fact.Addresses)
		sort.Strings(fact.Prefixes)
		nics = append(nics, fact)
	}
	sort.Slice(nics, func(i, j int) bool { return nics[i].Name < nics[j].Name })
	return nics, nil
}

func safeHostText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(value))
}
