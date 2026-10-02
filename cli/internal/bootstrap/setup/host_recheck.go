package setup

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// RecheckFreshHost reads a selected host again immediately before an RKE2
// write. It does not create a host claim or authorize the write. A caller must
// already hold the reviewed plan and qualified readiness for the whole run.
func RecheckFreshHost(ctx context.Context, c Compiled, host Host, reviewed HostObservation, ssh ports.CappedSSHClient) error {
	if err := checkCompiled(c); err != nil {
		return err
	}
	if c.Spec.Target.Intent != clusterinit.ModeInstall || ssh == nil {
		return fmt.Errorf("fresh-host recheck requires install intent and a bounded SSH client")
	}
	selected := false
	for _, candidate := range c.Spec.Hosts {
		if candidate.ID == host.ID && candidate == host {
			selected = true
			break
		}
	}
	if !selected || reviewed.ID != host.ID || reviewed.Role != host.Role || reviewed.SSHAlias != host.SSHAlias {
		return fmt.Errorf("host %s does not match the reviewed target", host.ID)
	}
	if reviewed.ObservedAt.IsZero() || time.Since(reviewed.ObservedAt) > maxHostInventoryAge || reviewed.ObservedAt.After(time.Now().Add(time.Minute)) {
		return fmt.Errorf("host %s review has expired", host.ID)
	}
	probeCtx, cancel := context.WithTimeout(ctx, hostProbeTimeout)
	defer cancel()
	probeHost := host
	probeHost.NIC = configuredHostNIC(c, host)
	current := inspectHost(probeCtx, ssh, probeHost, clusterinit.ModeInstall, !c.Init.NoKubeVirt && !c.Init.AllowNoKubevirtEligible)
	if err := probeCtx.Err(); err != nil {
		return fmt.Errorf("host %s recheck stopped: %w", host.ID, err)
	}
	qualified, err := applyReleaseHostPolicy(c, HostInventory{Hosts: []HostObservation{current}})
	if err != nil {
		return fmt.Errorf("host %s release profile changed: %w", host.ID, err)
	}
	current = qualified.Hosts[0]
	if current.State != "observed" {
		return fmt.Errorf("host %s failed the live pre-write checks", host.ID)
	}
	for _, id := range []string{"profile-os", "profile-architecture", "profile-cpu", "profile-memory", "profile-free-space"} {
		if hostCheckStatusValue(current, id) != "pass" {
			return fmt.Errorf("host %s lacks a passing %s profile check", host.ID, id)
		}
	}
	for _, check := range current.Checks {
		if check.Status == "pass" || check.Status == "not-applicable" {
			continue
		}
		if check.ID == "free-space" && check.Status == "unknown" && qualifiedFreeSpace(current) {
			continue
		}
		if check.ID == "kvm-device" && check.Status == "unknown" && current.Facts.KVMDevice == "absent" && reviewed.Facts.KVMDevice == "absent" {
			continue
		}
		return fmt.Errorf("host %s has an unresolved live %s check", host.ID, check.ID)
	}
	old, live := reviewed.Facts, current.Facts
	if old.MachineID == "" || old.MachineID != live.MachineID || old.HostKey == nil || live.HostKey == nil ||
		old.HostKey.Address != live.HostKey.Address || old.HostKey.Algorithm != live.HostKey.Algorithm || old.HostKey.FingerprintSHA256 != live.HostKey.FingerprintSHA256 {
		return fmt.Errorf("host %s machine or SSH identity changed", host.ID)
	}
	if old.OSID != live.OSID || old.OSVersion != live.OSVersion || old.Architecture != live.Architecture ||
		old.CPUs != live.CPUs || old.MemoryKiB != live.MemoryKiB || old.KVMDevice != live.KVMDevice ||
		live.PasswordlessSudo != "yes" || live.ClockSynced != "yes" {
		return fmt.Errorf("host %s environment changed since review", host.ID)
	}
	if live.RKE2Files != "absent" || live.RKE2ServerLoad != "not-found" || live.RKE2AgentLoad != "not-found" ||
		len(live.ReservedTCPPortsInUse) != 0 {
		return fmt.Errorf("host %s is no longer a fresh RKE2 target", host.ID)
	}
	oldManagement, oldKnown := managementNIC(old.NICs, host.ManagementAddress)
	liveManagement, liveKnown := managementNIC(live.NICs, host.ManagementAddress)
	if !oldKnown || !liveKnown || oldManagement != liveManagement {
		return fmt.Errorf("host %s management address or interface changed", host.ID)
	}
	if !reflect.DeepEqual(old.NICs, live.NICs) {
		return fmt.Errorf("host %s network addresses or prefixes changed", host.ID)
	}
	nicName := probeHost.NIC
	if nicName == "" {
		var found bool
		nicName, found = managementNIC(old.NICs, host.ManagementAddress)
		if !found {
			return fmt.Errorf("host %s has no unique reviewed management interface", host.ID)
		}
		liveName, liveFound := managementNIC(live.NICs, host.ManagementAddress)
		if !liveFound || liveName != nicName {
			return fmt.Errorf("host %s management interface changed since review", host.ID)
		}
	}
	oldNIC, oldFound := selectedNIC(old.NICs, nicName)
	liveNIC, liveFound := selectedNIC(live.NICs, nicName)
	if !oldFound || !liveFound || oldNIC.MAC == "" || oldNIC.MAC != liveNIC.MAC || oldNIC.MTU != liveNIC.MTU {
		return fmt.Errorf("host %s selected network interface changed", host.ID)
	}
	if host.Disk != "" && (old.SelectedDisk == nil || live.SelectedDisk == nil || !reflect.DeepEqual(*old.SelectedDisk, *live.SelectedDisk)) {
		return fmt.Errorf("host %s selected disk changed since review", host.ID)
	}
	return nil
}

func hostCheckStatusValue(host HostObservation, id string) string {
	for _, check := range host.Checks {
		if check.ID == id {
			return check.Status
		}
	}
	return ""
}

func selectedNIC(nics []NICFact, name string) (NICFact, bool) {
	for _, nic := range nics {
		if nic.Name == name {
			return nic, true
		}
	}
	return NICFact{}, false
}

func configuredHostNIC(c Compiled, host Host) string {
	if host.NIC != "" {
		return host.NIC
	}
	if nic := c.Init.NodeNICs[host.ID]; nic != "" {
		return nic
	}
	return c.Init.Sets["EXT_NET_INTERFACE"]
}

func managementNIC(nics []NICFact, address string) (string, bool) {
	want := net.ParseIP(address)
	if want == nil {
		return "", false
	}
	selected := ""
	for _, nic := range nics {
		for _, address := range nic.Addresses {
			if want.Equal(net.ParseIP(address)) {
				if selected != "" {
					return "", false
				}
				selected = nic.Name
			}
		}
	}
	return selected, selected != ""
}
