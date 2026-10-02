package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/rke2"
)

const SafetyReviewSchemaVersion = 1

// SafetyReview binds prepared bytes and read-only evidence. Its hash detects
// accidental edits; it is not a signature, target claim, or apply authorization.
type SafetyReview struct {
	SchemaVersion  int                          `json:"schemaVersion"`
	ReadyToApply   bool                         `json:"readyToApply"`
	InputHash      string                       `json:"inputHash"`
	ReleaseSHA256  string                       `json:"releaseSHA256"`
	CreatedAt      time.Time                    `json:"createdAt"`
	Git            GitBinding                   `json:"git"`
	Artifacts      ArtifactProof                `json:"artifacts"`
	Prepared       clusterinit.PreparedScaffold `json:"prepared"`
	Environment    map[string]string            `json:"environment"`
	Hosts          HostInventory                `json:"hosts"`
	Network        map[string]NetworkSafety     `json:"network"`
	Storage        []StorageIdentity            `json:"storage"`
	HostEffects    []ReviewedHostEffect         `json:"hostEffects"`
	PackageChanges []string                     `json:"packageChanges"`
	Verification   SpecVerificationEffect       `json:"verification"`
	Unresolved     []string                     `json:"unresolved"`
	Hash           string                       `json:"hash"`
}

type ReviewedHostEffect struct {
	Host         string            `json:"host"`
	Script       string            `json:"script"`
	ScriptSHA256 string            `json:"scriptSHA256"`
	ScriptBody   string            `json:"scriptBody"`
	Environment  map[string]string `json:"environment"`
	Changes      []string          `json:"changes"`
}

// Until verification drivers allocate owned resources, the review explicitly
// binds an empty write list and blocks that stage. It must not invent a diff.
type SpecVerificationEffect struct {
	Requested   Verification `json:"requested"`
	Resources   []string     `json:"resources"`
	Implemented bool         `json:"implemented"`
}

type SafetyReviewOptions struct {
	ArtifactCache string
	CLIPath       string
	Directory     string
	Scaffold      clusterinit.ScaffoldOptions
	Runner        func(string) ports.ScriptRunner
	SSH           ports.CappedSSHHostKeyClient
}

// CreateSafetyReview performs live reads and isolated local generation. It
// changes neither the selected Git checkout nor any host or Kubernetes object.
func CreateSafetyReview(ctx context.Context, c Compiled, o SafetyReviewOptions) (SafetyReview, error) {
	var review SafetyReview
	if err := checkCompiled(c); err != nil {
		return review, err
	}
	if c.Spec.Target.Intent != clusterinit.ModeInstall || o.SSH == nil || o.Scaffold.Plan == nil {
		return review, fmt.Errorf("safety review requires explicit fresh-install intent, a plan, and pinned SSH")
	}
	if err := clusterinit.VerifyApplyPlanInput(o.Scaffold.Plan, &c.Init); err != nil {
		return review, err
	}
	git, err := InspectGitBinding(ctx, c.Init.Repo)
	if err != nil {
		return review, err
	}
	if o.Scaffold.FleetRepo != git.Directory {
		return review, fmt.Errorf("scaffold and reviewed Git checkout differ")
	}
	directory, err := privateReviewPath(o.Directory, git.Directory)
	if err != nil {
		return review, err
	}
	sourceBaseline, err := clusterinit.SnapshotScaffoldSource(git.Directory)
	if err != nil {
		return review, err
	}
	artifacts, err := VerifyArtifactCache(ctx, c, o.ArtifactCache, o.CLIPath)
	if err != nil {
		return review, err
	}
	if err := BindStarterSource(artifacts, c.Spec.Release.StarterRef, git.Directory); err != nil {
		return review, err
	}
	hosts, err := InspectHosts(ctx, c, o.SSH)
	if err != nil {
		return review, err
	}
	if len(checkHostInventoryBinding(c.Spec.Hosts, hosts, time.Now().UTC())) != 0 {
		return review, fmt.Errorf("host evidence is incomplete or stale")
	}
	network := map[string]NetworkSafety{}
	for _, host := range c.Spec.Hosts {
		observed, ok := reviewedHost(hosts, host.ID)
		if !ok {
			return review, fmt.Errorf("host is absent from review")
		}
		if err := RecheckFreshHost(ctx, c, host, observed, o.SSH); err != nil {
			return review, err
		}
		carrier := host
		carrier.NIC = configuredHostNIC(c, host)
		safety, err := InspectNetworkSafety(ctx, carrier, o.SSH)
		if err != nil {
			return review, fmt.Errorf("host %s: %w", host.ID, err)
		}
		if safety.MachineID != observed.Facts.MachineID {
			return review, fmt.Errorf("host changed between readiness reads")
		}
		network[host.ID] = safety
		if _, ok := artifacts.RKE2[architectureName(observed.Facts.Architecture)]; !ok {
			return review, fmt.Errorf("release lacks artifacts for host %s architecture", host.ID)
		}
	}
	storage, err := InspectStorageIdentities(ctx, c, o.SSH)
	if err != nil {
		return review, err
	}
	bound, err := BoundObjectStorage(c.Init.ObjectStorage(), storage)
	if err != nil {
		return review, err
	}
	// Inputs used by the generators come from the compiled specification. The
	// caller supplies only validated TLS/CA material and discovered CIDR facts.
	scaffold := o.Scaffold
	scaffold.Sets = c.Init.Sets
	scaffold.NodeNICs = c.Init.NodeNICs
	scaffold.ObjectStorage = bound
	scaffold.NodeExternalIP = c.Init.NodeExternalIP
	scaffold.VMStorage = c.Init.VMStorage()
	scaffold.ImageAccel = c.Init.ImageAccel()
	scaffold.GPU = c.Init.GPU()
	scaffold.PAYG = c.Init.PAYG
	// Use measured prefix lengths; never repeat the script's /24 fallback.
	if value := c.Init.Sets["NODE_CIDR"]; value != "" {
		scaffold.NodeCIDR = value
	} else {
		var prefix netip.Prefix
		for _, host := range c.Spec.Hosts {
			ip, err := netip.ParseAddr(host.ManagementAddress)
			if err != nil || !ip.Is4() {
				return review, fmt.Errorf("fresh review requires IPv4 management addresses")
			}
			observed, _ := reviewedHost(hosts, host.ID)
			var selected netip.Prefix
			for _, nic := range observed.Facts.NICs {
				for _, value := range nic.Prefixes {
					p, err := netip.ParsePrefix(value)
					if err == nil && p.Addr() == ip {
						if selected.IsValid() && selected != p.Masked() {
							return review, fmt.Errorf("host management prefix is ambiguous")
						}
						selected = p.Masked()
					}
				}
			}
			if !selected.IsValid() {
				return review, fmt.Errorf("host %s has no measured management prefix", host.ID)
			}
			if prefix.IsValid() && prefix != selected {
				return review, fmt.Errorf("set NODE_CIDR and review routes for hosts on different management networks")
			}
			prefix = selected
		}
		scaffold.NodeCIDR = prefix.String()
	}
	nodeNetwork, err := netip.ParsePrefix(scaffold.NodeCIDR)
	if err != nil || !nodeNetwork.Addr().Is4() || nodeNetwork != nodeNetwork.Masked() {
		return review, fmt.Errorf("NODE_CIDR requires a canonical IPv4 network")
	}
	for _, host := range c.Spec.Hosts {
		ip, err := netip.ParseAddr(host.ManagementAddress)
		if err != nil || !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || !usableInPrefix(nodeNetwork, ip) {
			return review, fmt.Errorf("host %s management address must be usable in NODE_CIDR", host.ID)
		}
	}
	planCopy := *scaffold.Plan
	if c.Init.Sets["INGRESS_HOST_CIDR"] == "" {
		var ingressAddresses []string
		for _, host := range c.Spec.Hosts {
			if host.Ingress {
				ingressAddresses = append(ingressAddresses, host.ManagementAddress)
			}
		}
		if len(ingressAddresses) == 0 {
			ingressAddresses = []string{primaryServer(c.Spec.Hosts).ManagementAddress}
		}
		cidr, err := clusterinit.DeriveIngressHostCIDR(ingressAddresses)
		if err != nil {
			return review, err
		}
		planCopy.IngressHostCIDR = cidr
	}
	scaffold.Plan = &planCopy
	if scaffold.SingleIPNAT || len(scaffold.ControlPlaneNodes) != 0 {
		return review, fmt.Errorf("review requires explicit fresh-host address inputs")
	}
	prepared, err := clusterinit.PrepareScaffold(ctx, scaffold, directory, o.Runner, func(copy string) error {
		source, err := clusterinit.SnapshotScaffoldSource(copy)
		if err != nil || !reflect.DeepEqual(source, sourceBaseline) {
			return fmt.Errorf("Fleet source changed before generation")
		}
		return BindStarterSource(artifacts, c.Spec.Release.StarterRef, copy)
	})
	if err != nil {
		return review, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(directory)
		}
	}()
	env, err := config.LoadEnv(filepath.Join(directory, "clusters", c.Spec.Name, "cluster-config.env"))
	if err != nil {
		return review, err
	}
	values := env.AsMap()
	if err := clusterinit.ValidateInputSpec(values); err != nil {
		return review, fmt.Errorf("generated environment cannot enter review: %w", err)
	}
	findings := ValidateAddressPlan(values, hosts.Hosts)
	if len(findings) != 0 {
		return review, fmt.Errorf("address plan: %s: %s", findings[0].Key, findings[0].Detail)
	}
	// RKE2 and Fleet must use the same networks. Generated defaults must not
	// silently override the host engine's reviewed inputs.
	resolved, err := clusterinit.EnvMapFor(c.Init.Preset, c.Init.Sets)
	if err != nil {
		return review, err
	}
	for _, key := range []string{"POD_CIDR", "SVC_CIDR", "CLUSTER_DNS"} {
		if values[key] != resolved[key] {
			return review, fmt.Errorf("generated %s differs from RKE2 host configuration", key)
		}
	}
	review = SafetyReview{SchemaVersion: SafetyReviewSchemaVersion, InputHash: c.InputHash, ReleaseSHA256: c.ReleaseSHA256, CreatedAt: time.Now().UTC(), Git: git, Artifacts: artifacts, Prepared: prepared, Environment: values, Hosts: hosts, Network: network, Storage: storage, PackageChanges: []string{}, Verification: SpecVerificationEffect{Requested: c.Spec.Verification, Resources: []string{}}, Unresolved: []string{"independent-release-qualification-and-live-pins", "target-claims-and-production-coordinator", "package-acquisition-and-tool-byte-binding", "inter-host-and-external-connectivity", "verification-resource-ownership-and-drivers", "platform-runtime-effects-and-recovery"}}
	hashes := rke2.EmbeddedScriptHashes()
	for _, host := range c.Spec.Hosts {
		script := "install-server.sh"
		if host.Role == "agent" {
			script = "install-agent.sh"
		}
		params := map[string]string{"NODE_NAME": host.ID, "NODE_IP": host.ManagementAddress, "RKE2_VERSION": c.Spec.Release.RKE2Version, "RKE2_DNS_PUBLIC_FALLBACK": "false"}
		if host.Role == "server" {
			params["DOMAIN"] = c.Init.Domain
			params["POD_CIDR"] = values["POD_CIDR"]
			params["SERVICE_CIDR"] = values["SVC_CIDR"]
			params["CLUSTER_DNS"] = values["CLUSTER_DNS"]
		}
		review.HostEffects = append(review.HostEffects, ReviewedHostEffect{Host: host.ID, Script: script, ScriptSHA256: hashes[script], ScriptBody: rke2.EmbeddedScripts()[script], Environment: params, Changes: []string{"private artifact staging and cleanup", "RKE2 binaries and systemd unit from pinned archive", "/etc/rancher/rke2 configuration, mirror and audit policy", "/etc/sysctl.d/99-kube-dc.conf and live sysctl application", "optional reviewed CA trust anchors", "RKE2 service enable and start", "server admin kubeconfig and node join credentials", "conditional /etc/fstab nobarrier removal, backup and root remount"}})
	}
	review.Hash = safetyReviewHash(review)
	if err := RecheckSafetyReview(ctx, c, review, o.SSH, o.CLIPath); err != nil {
		return SafetyReview{}, err
	}
	complete = true
	return review, nil
}

func architectureName(architecture string) string {
	return map[string]string{"x86_64": "amd64", "aarch64": "arm64"}[architecture]
}
func reviewedHost(inventory HostInventory, id string) (HostObservation, bool) {
	for _, host := range inventory.Hosts {
		if host.ID == id {
			return host, true
		}
	}
	return HostObservation{}, false
}
func safetyReviewHash(review SafetyReview) string {
	review.Hash = ""
	body, _ := json.Marshal(review)
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

func validateSafetyReview(c Compiled, review SafetyReview) error {
	if err := checkCompiled(c); err != nil {
		return err
	}
	if review.SchemaVersion != SafetyReviewSchemaVersion || review.Hash == "" || review.Hash != safetyReviewHash(review) || review.ReadyToApply || review.InputHash != c.InputHash || review.ReleaseSHA256 != c.ReleaseSHA256 || review.Hosts.InputHash != c.InputHash || len(review.PackageChanges) != 0 || review.Verification.Implemented || len(review.Verification.Resources) != 0 {
		return fmt.Errorf("safety review binding changed or requests unsupported effects")
	}
	if age := time.Since(review.CreatedAt); age < 0 || age > maxHostInventoryAge {
		return fmt.Errorf("safety review has expired; prepare and review again")
	}
	if len(checkHostInventoryBinding(c.Spec.Hosts, review.Hosts, time.Now().UTC())) != 0 {
		return fmt.Errorf("reviewed host observations have expired or changed")
	}
	if err := clusterinit.VerifyPreparedScaffold(review.Prepared); err != nil {
		return err
	}
	destination, err := filepath.Abs(c.Init.Repo)
	if err != nil {
		return err
	}
	destination, err = filepath.EvalSymlinks(destination)
	if err != nil || destination != review.Git.Directory || destination != review.Prepared.Destination || review.Prepared.Cluster != c.Spec.Name {
		return fmt.Errorf("reviewed publication target changed")
	}
	env, err := config.LoadEnv(filepath.Join(review.Prepared.Directory, "clusters", c.Spec.Name, "cluster-config.env"))
	if err != nil || !reflect.DeepEqual(env.AsMap(), review.Environment) {
		return fmt.Errorf("review environment differs from prepared bytes")
	}
	if err := clusterinit.ValidateInputSpec(review.Environment); err != nil {
		return err
	}
	for _, effect := range review.HostEffects {
		if rke2.EmbeddedScriptHashes()[effect.Script] != effect.ScriptSHA256 || rke2.EmbeddedScripts()[effect.Script] != effect.ScriptBody {
			return fmt.Errorf("reviewed host script changed")
		}
	}
	return nil
}

// RecheckSafetyReview is read-only. Consumers must call it before acquiring a
// write permit. It deliberately does not turn this batch into an install path.
func RecheckSafetyReview(ctx context.Context, c Compiled, review SafetyReview, ssh ports.CappedSSHHostKeyClient, cliPath string) error {
	if err := RecheckReviewFiles(ctx, c, review, cliPath); err != nil {
		return err
	}
	for _, host := range c.Spec.Hosts {
		if err := RecheckSafetyHost(ctx, c, review, host, ssh); err != nil {
			return err
		}
	}
	storage, err := InspectStorageIdentities(ctx, c, ssh)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(storage, review.Storage) {
		return fmt.Errorf("raw storage identity changed since review")
	}
	return ctx.Err()
}

func RecheckSafetyHost(ctx context.Context, c Compiled, review SafetyReview, host Host, ssh ports.CappedSSHHostKeyClient) error {
	if err := validateSafetyReview(c, review); err != nil {
		return err
	}
	observed, ok := reviewedHost(review.Hosts, host.ID)
	if !ok {
		return fmt.Errorf("host is absent from review")
	}
	if err := RecheckFreshHost(ctx, c, host, observed, ssh); err != nil {
		return err
	}
	carrier := host
	carrier.NIC = configuredHostNIC(c, host)
	current, err := InspectNetworkSafety(ctx, carrier, ssh)
	if err != nil {
		return err
	}
	if !sameNetworkSafety(current, review.Network[host.ID]) {
		return fmt.Errorf("host %s management route or persistent network state changed", host.ID)
	}
	for _, binding := range review.Storage {
		if binding.Node == host.ID {
			live, err := inspectStorageIdentity(ctx, host, binding.Device.SelectedPath, ssh)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(live, binding) {
				return fmt.Errorf("host %s storage identity changed", host.ID)
			}
		}
	}
	return nil
}

// Resolve the existing parent before checking scope so a symlink cannot place
// a private review or prepared directory inside the selected checkout.
func privateReviewPath(path, checkout string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil || path == "" || checkout == "" {
		return "", fmt.Errorf("review paths and Git destination are required")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", fmt.Errorf("review path requires an existing parent directory")
	}
	destination, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		return "", fmt.Errorf("review Git destination is unavailable")
	}
	resolved := filepath.Join(parent, filepath.Base(abs))
	rel, err := filepath.Rel(destination, resolved)
	if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return "", fmt.Errorf("keep review paths outside the selected Git checkout")
	}
	return resolved, nil
}

func SaveSafetyReview(path string, review SafetyReview) error {
	if review.Hash == "" || review.Hash != safetyReviewHash(review) || review.ReadyToApply {
		return fmt.Errorf("cannot save an invalid safety review")
	}
	path, err := privateReviewPath(path, review.Git.Directory)
	if err != nil {
		return err
	}
	body, err := json.MarshalIndent(review, "", "  ")
	if err != nil {
		return err
	}
	if len(body) > 8<<20 {
		return fmt.Errorf("safety review exceeds file limit")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(body, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("save safety review failed")
	}
	return nil
}

func LoadSafetyReview(path string) (SafetyReview, error) {
	var review SafetyReview
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 8<<20 {
		return review, fmt.Errorf("safety review must be a private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return review, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, (8<<20)+1))
	d.DisallowUnknownFields()
	if err := d.Decode(&review); err != nil {
		return review, fmt.Errorf("invalid safety review JSON")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return review, fmt.Errorf("safety review has trailing data")
	}
	if review.SchemaVersion != SafetyReviewSchemaVersion || review.Hash == "" || review.Hash != safetyReviewHash(review) || review.ReadyToApply {
		return review, fmt.Errorf("safety review hash or schema changed")
	}
	return review, nil
}

func RecheckReviewFiles(ctx context.Context, c Compiled, review SafetyReview, cliPath string) error {
	if err := validateSafetyReview(c, review); err != nil {
		return err
	}
	proof, err := VerifyArtifactCache(ctx, c, review.Artifacts.CacheDirectory, cliPath)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(proof, review.Artifacts) {
		return fmt.Errorf("release artifact proof changed")
	}
	if err := RecheckGitBinding(ctx, review.Git); err != nil {
		return err
	}
	source, err := clusterinit.SnapshotScaffoldSource(review.Git.Directory)
	if err != nil || !reflect.DeepEqual(source, review.Prepared.SourceFiles) {
		return fmt.Errorf("Fleet source changed since review")
	}
	return ctx.Err()
}
