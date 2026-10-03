// Package setup defines the local, versioned input for guided installation.
// It compiles into the existing init options; execution and live-state checks
// remain with the installer coordinator.
package setup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

const SchemaVersion = 1
const maxSpecBytes = 4 << 20

// Spec is a local input file. CredentialRefs contains locations, never values.
type Spec struct {
	SchemaVersion  int               `json:"schemaVersion"`
	Name           string            `json:"name"`
	Profile        string            `json:"profile"`
	Release        Release           `json:"release"`
	Target         Target            `json:"target"`
	Hosts          []Host            `json:"hosts"`
	Platform       Platform          `json:"platform"`
	Execution      Execution         `json:"execution,omitempty"`
	CredentialRefs map[string]string `json:"credentialRefs,omitempty"`
	Verification   Verification      `json:"verification"`
	sourceDir      string
}

type Release struct {
	RecordFile  string `json:"recordFile"`
	StarterRef  string `json:"starterRef"`
	RKE2Version string `json:"rke2Version"`
}

type Target struct {
	Intent   clusterinit.Mode `json:"intent"`
	Identity string           `json:"identity,omitempty"`
}

type Host struct {
	ID                string `json:"id"`
	SSHAlias          string `json:"sshAlias"`
	Role              string `json:"role"`
	Primary           bool   `json:"primary,omitempty"`
	ManagementAddress string `json:"managementAddress"`
	NIC               string `json:"nic"`
	Disk              string `json:"disk,omitempty"`
	Ingress           bool   `json:"ingress,omitempty"`
	HostKeySHA256     string `json:"hostKeySHA256,omitempty"`
}

type Platform struct {
	ConfigFile string `json:"configFile"`
}

type Execution struct {
	NoPush           bool `json:"noPush,omitempty"`
	NoCreateRepo     bool `json:"noCreateRepo,omitempty"`
	NoInstallPrereqs bool `json:"noInstallPrereqs,omitempty"`
}

type Verification struct {
	Capabilities []string `json:"capabilities"`
	Namespace    string   `json:"namespace,omitempty"`
}

// Compiled binds a spec to the exact non-secret config and release record
// loaded for this run. The coordinator must still verify live state and build
// an effects plan before it changes either a host or a cluster.
type Compiled struct {
	Spec          Spec
	Init          clusterinit.InitOptions
	IgnoredKeys   []string
	InputHash     string
	ReleaseSHA256 string
	seal          string
}

var profileID = regexp.MustCompile(`^[a-z][a-z0-9-]*@v[1-9][0-9]*$`)
var hostID = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
var capabilityID = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
var selectedDevicePath = regexp.MustCompile(`^/dev/[A-Za-z0-9][A-Za-z0-9/_.-]*$`)

// Load reads strict JSON. Paths are resolved against the specification file,
// so running the same file from another directory has the same meaning.
func Load(path string) (Spec, error) {
	var spec Spec
	f, err := os.Open(path)
	if err != nil {
		return spec, fmt.Errorf("open setup spec: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxSpecBytes+1))
	if err != nil {
		return Spec{}, fmt.Errorf("read setup spec %s: %w", path, err)
	}
	if len(data) > maxSpecBytes {
		return Spec{}, fmt.Errorf("setup spec %s exceeds %d bytes", path, maxSpecBytes)
	}
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Spec{}, fmt.Errorf("decode setup spec %s: %w", path, err)
	}
	if unknown := unknownFields(raw, reflect.TypeOf(Spec{}), ""); len(unknown) != 0 {
		return Spec{}, fmt.Errorf("setup spec %s: unknown field paths: %s", path, strings.Join(unknown, ", "))
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return Spec{}, fmt.Errorf("decode setup spec %s: %w", path, err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		if err == nil {
			return Spec{}, fmt.Errorf("setup spec %s: expected one JSON object", path)
		}
		return Spec{}, fmt.Errorf("setup spec %s: %w", path, err)
	}
	base, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return Spec{}, err
	}
	spec.sourceDir = base
	spec.Platform.ConfigFile = resolve(base, spec.Platform.ConfigFile)
	spec.Release.RecordFile = resolve(base, spec.Release.RecordFile)
	for k, ref := range spec.CredentialRefs {
		if strings.HasPrefix(ref, "file:") {
			spec.CredentialRefs[k] = "file:" + resolve(base, strings.TrimPrefix(ref, "file:"))
		}
	}
	return spec, nil
}

// unknownFields reports all unsupported paths, including fields in host list
// entries. CredentialRefs is an open map, so its key names are checked later.
func unknownFields(raw json.RawMessage, typ reflect.Type, path string) []string {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			return nil // the typed decoder reports the shape error
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !f.IsExported() {
				continue
			}
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			fields[name] = f.Type
		}
		var problems []string
		for name, value := range object {
			fieldPath := name
			if path != "" {
				fieldPath = path + "." + name
			}
			fieldType, known := fields[name]
			if !known {
				problems = append(problems, fieldPath)
				continue
			}
			problems = append(problems, unknownFields(value, fieldType, fieldPath)...)
		}
		sort.Strings(problems)
		return problems
	case reflect.Slice:
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
		var problems []string
		for i, item := range items {
			problems = append(problems, unknownFields(item, typ.Elem(), fmt.Sprintf("%s[%d]", path, i))...)
		}
		return problems
	default:
		return nil
	}
}

func resolve(base, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

// Compile checks local inputs before handing options to the existing init
// engine. It does not assert that a release is qualified or a host is ready.
func Compile(spec Spec) (Compiled, error) {
	var out Compiled
	if err := validate(spec); err != nil {
		return out, err
	}
	env, err := config.LoadEnv(spec.Platform.ConfigFile)
	if err != nil {
		return out, fmt.Errorf("platform.configFile: %w", err)
	}
	src := env.AsMap()
	for key := range src {
		if secretKey(key) {
			return out, fmt.Errorf("platform.configFile: %s contains credential material; move it to credentialRefs before setup", key)
		}
	}
	// Init's repo key is a local path. Interpret it relative to the setup
	// file, just like the explicit file references in the specification.
	if repo := strings.TrimSpace(src[clusterinit.KeyRepo]); repo != "" && !filepath.IsAbs(repo) {
		base := spec.sourceDir
		if base == "" {
			base = filepath.Dir(spec.Platform.ConfigFile)
		}
		src[clusterinit.KeyRepo] = filepath.Join(base, repo)
	}
	record, err := readReleaseRecord(spec.Release.RecordFile)
	if err != nil {
		return out, fmt.Errorf("release.recordFile: %w", err)
	}
	var recordObject map[string]json.RawMessage
	if err := json.Unmarshal(record, &recordObject); err != nil || recordObject == nil {
		return out, fmt.Errorf("release.recordFile: expected JSON release record")
	}
	if err := checkHostConflicts(spec, src); err != nil {
		return out, err
	}
	o := clusterinit.InitOptions{}
	if err := clusterinit.ValidateInputSpec(src); err != nil {
		return out, err
	}
	ignored := clusterinit.ImportMap(&o, src, func(string) bool { return false })
	o.Name = spec.Name
	o.RequestedMode = spec.Target.Intent
	o.Mode = spec.Target.Intent
	o.StarterRef = spec.Release.StarterRef
	o.NoPush = spec.Execution.NoPush
	o.NoCreateRepo = spec.Execution.NoCreateRepo
	o.NoInstallPrereqs = spec.Execution.NoInstallPrereqs
	if o.NoCreateRepo && o.FleetMode != clusterinit.FleetNewRepo {
		return out, fmt.Errorf("execution.noCreateRepo: requires KUBE_DC_INIT_FLEET_MODE=new-repo")
	}
	if err := applyCredentialRefs(spec.CredentialRefs, &o); err != nil {
		return out, err
	}
	orderedHosts := append([]Host(nil), spec.Hosts...)
	sort.Slice(orderedHosts, func(i, j int) bool { return orderedHosts[i].ID < orderedHosts[j].ID })
	o.IngressNodes = nil
	o.CephNodes = map[string]string{}
	selectedDisks := 0
	o.SSHHost = primaryServer(orderedHosts).SSHAlias
	o.SSHHostKeySHA256 = primaryServer(orderedHosts).HostKeySHA256
	o.PrimaryNode = primaryServer(orderedHosts).ID
	o.NodeSSHHosts = map[string]string{}
	o.NodeSSHHostKeys = map[string]string{}
	for _, h := range orderedHosts {
		o.NodeSSHHosts[h.ID] = h.SSHAlias
		if h.HostKeySHA256 != "" {
			o.NodeSSHHostKeys[h.ID] = h.HostKeySHA256
		}
		if h.NIC != "" {
			if o.NodeNICs == nil {
				o.NodeNICs = map[string]string{}
			}
			o.NodeNICs[h.ID] = h.NIC
		}
		if h.Disk != "" {
			selectedDisks++
			switch o.RookMode {
			case clusterinit.RookCephLocal:
				o.RookOSDNode = h.ID
				o.RookOSDDevice = strings.TrimPrefix(h.Disk, "/dev/")
			case clusterinit.RookCephMultiNode:
				o.CephNodes[h.ID] = strings.TrimPrefix(h.Disk, "/dev/")
			}
		}
		if h.Ingress {
			o.IngressNodes = append(o.IngressNodes, h.ID)
		}
	}
	if o.RookMode == clusterinit.RookCephLocal && selectedDisks > 1 {
		return out, fmt.Errorf("hosts[].disk: rook-ceph-local accepts one selected OSD device")
	}
	if selectedDisks > 0 && o.RookMode != clusterinit.RookCephLocal && o.RookMode != clusterinit.RookCephMultiNode {
		return out, fmt.Errorf("hosts[].disk: object-storage mode %q does not use host OSD devices", o.RookMode)
	}
	sort.Strings(o.IngressNodes)
	if o.RookMode == "" {
		return out, fmt.Errorf("platform.configFile OBJECT_STORAGE_MODE: required")
	}
	if err := o.ValidateFields(); err != nil {
		return out, fmt.Errorf("platform.configFile: %w", err)
	}
	for _, capability := range spec.Verification.Capabilities {
		if capability == "virtual-machines" && (o.NoKubeVirt || o.AllowNoKubevirtEligible) {
			return out, fmt.Errorf("verification.capabilities: virtual-machines requires KubeVirt and the KVM eligibility gate")
		}
	}
	if o.TLSMode == clusterinit.TLSModeBYOWildcard {
		material, err := clusterinit.LoadWildcardTLS(o.TLSCert, o.TLSKey, o.Domain)
		if err != nil {
			return out, fmt.Errorf("credentialRefs.tls-cert/tls-key: %w", err)
		}
		o.TLSCertFingerprint = material.Fingerprint
	}
	if o.TrustedCABundle != "" {
		material, err := clusterinit.LoadTrustedCABundle(o.TrustedCABundle)
		if err != nil {
			return out, fmt.Errorf("credentialRefs.trusted-ca-bundle: %w", err)
		}
		o.TrustedCAFingerprint = material.Fingerprint
	}
	initHash, err := clusterinit.ComputeInputHash(&o)
	if err != nil {
		return out, err
	}
	recordHash := sha256.Sum256(record)
	// Credential values and paths are excluded. The set of required reference
	// names is included so dropping a credential invalidates the review.
	refNames := make([]string, 0, len(spec.CredentialRefs))
	for name := range spec.CredentialRefs {
		refNames = append(refNames, name)
	}
	sort.Strings(refNames)
	canonicalHosts := orderedHosts
	canonicalCapabilities := append([]string(nil), spec.Verification.Capabilities...)
	sort.Strings(canonicalCapabilities)
	canonicalRelease := spec.Release
	canonicalRelease.RecordFile = ""
	semantic := struct {
		SchemaVersion   int
		Name            string
		Profile         string
		Release         Release
		ReleaseSHA256   string
		Target          Target
		Hosts           []Host
		Verification    Verification
		CredentialNames []string
		InitHash        string
	}{spec.SchemaVersion, spec.Name, spec.Profile, canonicalRelease, hex.EncodeToString(recordHash[:]), spec.Target, canonicalHosts, Verification{canonicalCapabilities, spec.Verification.Namespace}, refNames, initHash}
	data, err := json.Marshal(semantic)
	if err != nil {
		return out, err
	}
	digest := sha256.Sum256(data)
	out = Compiled{Spec: spec, Init: o, IgnoredKeys: ignored, InputHash: hex.EncodeToString(digest[:]), ReleaseSHA256: hex.EncodeToString(recordHash[:])}
	out.seal, err = sealCompiled(out)
	if err != nil {
		return Compiled{}, err
	}
	return out, nil
}

func sealCompiled(c Compiled) (string, error) {
	data, err := json.Marshal(struct {
		Spec          Spec
		Init          clusterinit.InitOptions
		InputHash     string
		ReleaseSHA256 string
	}{c.Spec, c.Init, c.InputHash, c.ReleaseSHA256})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func validate(s Spec) error {
	var problems []string
	if s.SchemaVersion != SchemaVersion {
		problems = append(problems, fmt.Sprintf("schemaVersion: expected %d", SchemaVersion))
	}
	if !clusterinit.ValidClusterName(s.Name) {
		problems = append(problems, "name: use the Fleet cluster-name format")
	}
	if !profileID.MatchString(s.Profile) {
		problems = append(problems, "profile: expected a versioned ID such as evaluation@v1")
	}
	if s.Release.RecordFile == "" {
		problems = append(problems, "release.recordFile: required")
	}
	if err := clusterinit.ValidateStarterOCIRef(s.Release.StarterRef); err != nil {
		problems = append(problems, "release.starterRef: expected an immutable OCI digest reference")
	}
	if s.Release.RKE2Version == "" {
		problems = append(problems, "release.rke2Version: required")
	}
	if s.Target.Intent != clusterinit.ModeAuto && s.Target.Intent != clusterinit.ModeInstall && s.Target.Intent != clusterinit.ModeAdopt && s.Target.Intent != clusterinit.ModeResume {
		problems = append(problems, "target.intent: expected auto, install, adopt, or resume")
	}
	if s.Platform.ConfigFile == "" {
		problems = append(problems, "platform.configFile: required")
	}
	if len(s.Hosts) == 0 {
		problems = append(problems, "hosts: at least one host is required")
	}
	ids, aliases, addresses := map[string]bool{}, map[string]bool{}, map[string]bool{}
	servers, primaries := 0, 0
	for i, h := range s.Hosts {
		p := fmt.Sprintf("hosts[%d]", i)
		if !hostID.MatchString(h.ID) || ids[h.ID] {
			problems = append(problems, p+".id: required DNS-compatible unique ID")
		}
		ids[h.ID] = true
		if _, err := ports.ParseSSHHostTarget(h.SSHAlias); err != nil || aliases[h.SSHAlias] {
			problems = append(problems, p+".sshAlias: required unique SSH alias or [user@]host[:port]")
		}
		aliases[h.SSHAlias] = true
		if net.ParseIP(h.ManagementAddress) == nil || addresses[h.ManagementAddress] {
			problems = append(problems, p+".managementAddress: required unique IP address")
		}
		addresses[h.ManagementAddress] = true
		switch h.Role {
		case "server":
			servers++
		case "agent":
		default:
			problems = append(problems, p+".role: expected server or agent")
		}
		if h.Primary {
			primaries++
			if h.Role != "server" {
				problems = append(problems, p+".primary: only a server can be primary")
			}
		}
		if h.Disk != "" && !validSelectedDevicePath(h.Disk) {
			problems = append(problems, p+".disk: expected a clean /dev/ device path")
		}
		if h.HostKeySHA256 != "" && !ValidHostKeySHA256(h.HostKeySHA256) {
			problems = append(problems, p+".hostKeySHA256: expected an SSH SHA256 fingerprint")
		}
	}
	if servers == 0 {
		problems = append(problems, "hosts: at least one server is required")
	}
	if primaries > 1 || (servers > 1 && primaries != 1) {
		problems = append(problems, "hosts[].primary: select exactly one primary server")
	}
	for name, ref := range s.CredentialRefs {
		if !capabilityID.MatchString(name) || !(strings.HasPrefix(ref, "file:") || strings.HasPrefix(ref, "agent:") || strings.HasPrefix(ref, "provider:")) || strings.HasSuffix(ref, ":") {
			problems = append(problems, "credentialRefs."+name+": expected file:, agent:, or provider: reference")
		}
	}
	seenCapabilities := map[string]bool{}
	for i, c := range s.Verification.Capabilities {
		if !capabilityID.MatchString(c) {
			problems = append(problems, fmt.Sprintf("verification.capabilities[%d]: invalid capability ID", i))
		}
		if seenCapabilities[c] {
			problems = append(problems, fmt.Sprintf("verification.capabilities[%d]: duplicate capability ID", i))
		}
		seenCapabilities[c] = true
	}
	if len(problems) != 0 {
		sort.Strings(problems)
		return fmt.Errorf("invalid setup spec: %s", strings.Join(problems, "; "))
	}
	return nil
}

// ValidHostKeySHA256 reports whether a value is an SSH SHA-256 host-key
// fingerprint. Input screens use the same rule as setup specification checks.
func ValidHostKeySHA256(fingerprint string) bool {
	return fingerprint != "" && clusterinit.ValidateSSHHostKeyField(fingerprint) == nil
}

func validSelectedDevicePath(path string) bool {
	return selectedDevicePath.MatchString(path) && filepath.Clean(path) == path
}

func checkHostConflicts(s Spec, src map[string]string) error {
	if value := strings.TrimSpace(src["CLUSTER_NAME"]); value != "" && value != s.Name {
		return fmt.Errorf("name conflicts with platform.configFile CLUSTER_NAME (%q != %q)", s.Name, value)
	}
	server := primaryServer(s.Hosts).SSHAlias
	if value := strings.TrimSpace(src[clusterinit.KeySSHHostKeySHA256]); value != "" && value != primaryServer(s.Hosts).HostKeySHA256 {
		return fmt.Errorf("primary host fingerprint conflicts with platform.configFile %s", clusterinit.KeySSHHostKeySHA256)
	}

	nics, disks, ingress := map[string]string{}, map[string]string{}, []string{}
	orderedHosts := append([]Host(nil), s.Hosts...)
	sort.Slice(orderedHosts, func(i, j int) bool { return orderedHosts[i].ID < orderedHosts[j].ID })
	hostIDs := map[string]bool{}
	for _, h := range orderedHosts {
		hostIDs[h.ID] = true
		if h.NIC != "" {
			nics[h.ID] = h.NIC
		}
		if h.Disk != "" {
			disks[h.ID] = strings.TrimPrefix(h.Disk, "/dev/")
		}
		if h.Ingress {
			ingress = append(ingress, h.ID)
		}
	}
	if value := strings.TrimSpace(src[clusterinit.KeySSHHost]); value != "" && value != server {
		return fmt.Errorf("hosts[server].sshAlias conflicts with platform.configFile %s", clusterinit.KeySSHHost)
	}
	if value := strings.TrimSpace(src[clusterinit.KeyNodeNICs]); value != "" {
		parsed, err := clusterinit.ParseSetPairs(strings.Split(value, ","))
		if err != nil || !sameMap(parsed, nics) {
			return fmt.Errorf("hosts[].nic conflicts with platform.configFile %s", clusterinit.KeyNodeNICs)
		}
	}
	if value := strings.TrimSpace(src[clusterinit.KeyIngressNodes]); value != "" {
		actual := strings.Split(value, ",")
		for i := range actual {
			actual[i] = strings.TrimSpace(actual[i])
		}
		sort.Strings(actual)
		sort.Strings(ingress)
		if strings.Join(actual, ",") != strings.Join(ingress, ",") {
			return fmt.Errorf("hosts[].ingress conflicts with platform.configFile %s", clusterinit.KeyIngressNodes)
		}
	}
	for i := 1; i <= 3; i++ {
		nameKey := fmt.Sprintf("CEPH_NODE_%d", i)
		deviceKey := nameKey + "_DEVICE"
		if node := strings.TrimSpace(src[nameKey]); node != "" {
			device := strings.TrimPrefix(strings.TrimSpace(src[deviceKey]), "/dev/")
			if device == "" || disks[node] != device {
				return fmt.Errorf("hosts[].disk conflicts with platform.configFile %s/%s", nameKey, deviceKey)
			}
		} else if strings.TrimSpace(src[deviceKey]) != "" {
			return fmt.Errorf("platform.configFile %s requires %s", deviceKey, nameKey)
		}
	}
	if node := strings.TrimSpace(src["CEPH_LOCAL_OSD_NODE"]); node != "" {
		if !hostIDs[node] {
			return fmt.Errorf("hosts[].id conflicts with platform.configFile CEPH_LOCAL_OSD_NODE")
		}
		if len(disks) != 0 && disks[node] == "" {
			return fmt.Errorf("hosts[].disk conflicts with platform.configFile CEPH_LOCAL_OSD_NODE")
		}
	}
	if device := strings.TrimPrefix(strings.TrimSpace(src["CEPH_LOCAL_OSD_DEVICE"]), "/dev/"); device != "" {
		if node := strings.TrimSpace(src["CEPH_LOCAL_OSD_NODE"]); node == "" || disks[node] != device {
			return fmt.Errorf("hosts[].disk conflicts with platform.configFile CEPH_LOCAL_OSD_NODE/CEPH_LOCAL_OSD_DEVICE")
		}
	}
	serverAddresses := map[string]bool{}
	for _, h := range orderedHosts {
		if h.Role == "server" {
			serverAddresses[h.ManagementAddress] = true
		}
	}
	for _, address := range csv(src["KUBE_OVN_MASTER_NODES"]) {
		if !serverAddresses[address] {
			return fmt.Errorf("hosts[].managementAddress conflicts with platform.configFile KUBE_OVN_MASTER_NODES: %s is not a server address", address)
		}
	}
	for _, node := range csv(src["KUBE_OVN_GW_NODES"]) {
		if !hostIDs[node] {
			return fmt.Errorf("hosts[].id conflicts with platform.configFile KUBE_OVN_GW_NODES: %s is absent", node)
		}
	}
	if value := strings.TrimSpace(src["GPU_NODE_MODES"]); value != "" {
		modes, err := clusterinit.ParseGPUNodeModes([]string{value})
		if err != nil {
			return fmt.Errorf("platform.configFile GPU_NODE_MODES: %w", err)
		}
		for node := range modes {
			if !hostIDs[node] {
				return fmt.Errorf("hosts[].id conflicts with platform.configFile GPU_NODE_MODES: %s is absent", node)
			}
		}
	}
	return nil
}

func primaryServer(hosts []Host) Host {
	for _, h := range hosts {
		if h.Primary {
			return h
		}
	}
	for _, h := range hosts {
		if h.Role == "server" {
			return h
		}
	}
	return Host{}
}

func csv(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func secretKey(key string) bool {
	u := strings.ToUpper(key)
	if u == clusterinit.KeySSHHostKeySHA256 {
		return false
	} // public identity pin, validated on import
	if strings.HasSuffix(u, "_SECRET_READY") || strings.HasSuffix(u, "_SECRET_ENABLED") {
		return false
	}
	if strings.HasSuffix(u, "_KEY_ID") || strings.HasSuffix(u, "_PUBLIC_KEY") {
		return false
	}
	for _, part := range []string{"PASSWORD", "PRIVATE_KEY", "SECRET", "TOKEN", "CREDENTIAL", "_KEY", "UNSEAL_KEY", "RECOVERY_KEY"} {
		if strings.Contains(u, part) {
			return true
		}
	}
	return false
}

func applyCredentialRefs(refs map[string]string, o *clusterinit.InitOptions) error {
	knownFiles := map[string]*string{
		"tls-cert":                   &o.TLSCert,
		"tls-key":                    &o.TLSKey,
		"dns01-route53-secret-key":   &o.DNS01Route53SecretKeyFile,
		"dns01-cloudflare-api-token": &o.DNS01CloudflareAPITokenFile,
		"trusted-ca-bundle":          &o.TrustedCABundle,
	}
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		dst, supported := knownFiles[name]
		if !supported {
			return fmt.Errorf("credentialRefs.%s: this credential is not supported by guided setup", name)
		}
		ref := refs[name]
		path, ok := strings.CutPrefix(ref, "file:")
		if !ok || path == "" {
			return fmt.Errorf("credentialRefs.%s: this credential requires a file: reference", name)
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("credentialRefs.%s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("credentialRefs.%s: expected a regular file", name)
		}
		if name != "tls-cert" && name != "trusted-ca-bundle" && info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("credentialRefs.%s: credential file must not be readable by group or others", name)
		}
		*dst = path
	}
	if o.TLSMode == clusterinit.TLSModeBYOWildcard && (o.TLSCert == "" || o.TLSKey == "") {
		return errors.New("credentialRefs.tls-cert and credentialRefs.tls-key are required for TLS_MODE=byo-wildcard")
	}
	return nil
}
