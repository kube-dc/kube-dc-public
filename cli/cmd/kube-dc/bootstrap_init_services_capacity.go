package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// deriveServicesStorageBudget reads the storage already selected for this
// installation. Unknown capacity stays unknown; explicit enablement then
// fails in ResolveManagedServices, while auto stays off with a plan warning.
func deriveServicesStorageBudget(ctx context.Context, o *clusterinit.InitOptions) error {
	if o.InstallationKind != "kube-dc" || o.ServicesStorageBudget != "" || o.ManagedServicesMode == "off" || o.Mode != clusterinit.ModeInstall {
		return nil
	}
	if o.RookMode != clusterinit.RookCephLocal && o.RookMode != clusterinit.RookCephMultiNode {
		return nil // PVC capacity is derived without SSH by ResolveManagedServices.
	}
	if o.SSHHost == "" || o.NoSSH {
		return nil
	}
	sshClient, err := bootstrap.NewSSHOnly()
	if err != nil {
		return nil
	}
	budget, err := probeServicesBudget(ctx, o, sshClient)
	if err != nil {
		if o.ManagedServicesMode == "on" {
			return fmt.Errorf("managed-services capacity probe: %w; specify --services-storage-budget after verifying usable capacity", err)
		}
		return nil
	}
	o.ServicesStorageBudget = budget
	return nil
}

// verifyServicesDatabaseClass refuses to treat a named class as expandable
// until the current kubeconfig targets this installation and Kubernetes says
// allowVolumeExpansion=true. A missing cluster keeps auto mode off; explicit
// on reports the missing qualification through ResolveManagedServices.
func verifyServicesDatabaseClass(ctx context.Context, o *clusterinit.InitOptions) error {
	if o.InstallationKind != "kube-dc" || o.ServicesDatabaseClass == "" || o.ManagedServicesMode == "off" {
		return nil
	}
	if _, targets := clusterinit.KubeconfigTargetsCluster(o.Domain, o.NodeExternalIP); !targets {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	output, err := exec.CommandContext(probeCtx, "kubectl", "--request-timeout=5s", "get", "storageclass", o.ServicesDatabaseClass, "-o", "json").Output()
	if err != nil {
		if o.ManagedServicesMode == "on" {
			return fmt.Errorf("cannot verify --services-database-class %q on the target cluster: %w", o.ServicesDatabaseClass, err)
		}
		return nil
	}
	var storageClass struct {
		AllowVolumeExpansion bool `json:"allowVolumeExpansion"`
	}
	if err := json.Unmarshal(output, &storageClass); err != nil {
		return fmt.Errorf("parse target StorageClass %q: %w", o.ServicesDatabaseClass, err)
	}
	if !storageClass.AllowVolumeExpansion {
		if o.ManagedServicesMode == "on" {
			return fmt.Errorf("--services-database-class %q does not allow volume expansion", o.ServicesDatabaseClass)
		}
		return nil
	}
	o.ServicesClassExpansionVerified = true
	return nil
}

func probeServicesBudget(ctx context.Context, o *clusterinit.InitOptions, sshClient ports.SSHClient) (string, error) {
	const gib = uint64(1 << 30)
	var rawBytes uint64
	read := func(node, command string) (uint64, error) {
		target, err := rawOSDTarget(o, node)
		if err != nil {
			return 0, err
		}
		probeCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		defer cancel()
		output, err := sshClient.Run(probeCtx, target, command)
		if err != nil {
			return 0, err
		}
		fields := strings.Fields(string(output))
		if len(fields) == 0 {
			return 0, fmt.Errorf("empty storage-capacity response from %s", node)
		}
		amount, err := strconv.ParseUint(fields[len(fields)-1], 10, 64)
		if err != nil || amount == 0 {
			return 0, fmt.Errorf("invalid storage-capacity response from %s", node)
		}
		return amount, nil
	}
	if o.RookMode == clusterinit.RookCephLocal && (o.RookOSDDevice == "" || strings.HasPrefix(o.RookOSDDevice, "loop")) {
		backingFile := o.Sets["CEPH_LOCAL_OSD_BACKING_FILE"]
		if backingFile == "" {
			backingFile = "/var/lib/ceph-osd-block.img"
		}
		if !filepath.IsAbs(backingFile) || strings.IndexFunc(backingFile, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' || r == '/')
		}) >= 0 {
			return "", fmt.Errorf("invalid local OSD backing-file path")
		}
		available, err := read(o.RookOSDNode, "df -B1 --output=avail -- "+filepath.Dir(filepath.Clean(backingFile)))
		if err != nil {
			return "", err
		}
		nominalGiB := o.RookOSDSizeGB
		if nominalGiB == 0 {
			nominalGiB = 500
		}
		if nominalGiB < 1 || uint64(nominalGiB) > math.MaxUint64/gib {
			return "", fmt.Errorf("invalid loop-backed OSD size")
		}
		rawBytes = min(uint64(nominalGiB)*gib, available)
	} else {
		for _, disk := range o.ObjectStorage().RawOSDDevices() {
			device := strings.TrimPrefix(disk[1], "/dev/")
			if device == "" || strings.IndexFunc(device, func(r rune) bool {
				return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
			}) >= 0 {
				return "", fmt.Errorf("invalid OSD device name")
			}
			bytes, err := read(disk[0], "sudo -n lsblk -bndo SIZE -- /dev/"+device)
			if err != nil {
				return "", err
			}
			if rawBytes > math.MaxUint64-bytes {
				return "", fmt.Errorf("raw OSD capacity exceeds supported range")
			}
			rawBytes += bytes
		}
	}
	replicas := o.ObjectStorage().ReplicationSize
	if rawBytes == 0 || replicas < 1 {
		return "", fmt.Errorf("raw OSD capacity or replication is unknown")
	}
	budgetGiB := rawBytes / uint64(replicas) / 4 / gib
	if budgetGiB == 0 || budgetGiB > math.MaxInt64 {
		return "", fmt.Errorf("usable OSD capacity is outside supported range")
	}
	return fmt.Sprintf("%dGi", budgetGiB), nil
}
