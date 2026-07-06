// Package mock provides fixture-driven implementations of every
// `cli/internal/bootstrap/ports` interface. A scenario YAML
// (`scenarios/<name>.yaml`) describes the world the mocks present —
// what tools are installed, whether the cluster is reachable, what the
// Kustomization graph looks like, which scripts the runner will accept
// and what they print.
//
// The mock layer is a first-class shipped feature, not test-only — see
// agent rule 3 of `docs/prd/installer-agentic-implementation-plan.md`
// and the broader mock-first architecture at §11 of the engineering PRD.
// `KUBE_DC_MOCK=<scenario> kube-dc bootstrap …` works against any
// scenario in `scenarios/` without a cluster, fleet repo, or root.
package mock

import (
	"embed"
	"fmt"
	"path"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed scenarios/*.yaml
var scenariosFS embed.FS

// Scenario is the on-disk YAML shape every mock port reads from. Add
// fields here as new mocks need richer fixtures; existing scenarios
// silently default missing fields (yaml.v3 ignores absent keys).
//
// Keep this struct deliberately permissive: every field is optional so
// a minimal scenario can be 5 lines, and a rich one (cloud-shape) can
// be 200 without forcing the small ones to grow.
type Scenario struct {
	// Name is checked against the scenario filename on Load — catches
	// drift between filename and contents.
	Name string `yaml:"name"`

	// Description is a one-line summary the smoke test can print.
	Description string `yaml:"description"`

	// Tools describes the operator's local tooling installation state.
	// Key = tool name (kubectl, flux, sops, age, git, gh, ssh, bao).
	Tools map[string]ToolFixture `yaml:"tools"`

	// Host describes the operator's local node (only relevant in
	// install mode; doctor's host-probes layer reads this).
	Host *HostFixture `yaml:"host,omitempty"`

	// Cluster describes the K8s cluster behind the operator's
	// kubeconfig. Nil = unreachable / no kubeconfig.
	Cluster *ClusterFixture `yaml:"cluster,omitempty"`

	// OpenBao describes the openbao deployment in the cluster. Nil
	// when the cluster has no OpenBao (e.g. fresh / no-flux scenarios).
	OpenBao *OpenBaoFixture `yaml:"openbao,omitempty"`

	// Fleet describes the fleet repo state on the operator's
	// workstation. Nil = no fleet repo at all.
	Fleet *FleetFixture `yaml:"fleet,omitempty"`

	// DNS maps a hostname → resolved IPs. Used by the DNS port's
	// Resolve method. Random-sub-label wildcards: a scenario can set
	// `*.<domain>: [<ip>]` and the mock matches any randomised label
	// against the wildcard. See dns.go for the matcher.
	DNS map[string][]string `yaml:"dns"`

	// Scripts is the registered fleet-script fixture set. Key is the
	// ScriptKind (matches `cli/internal/bootstrap/ports/script.go`
	// constants).
	Scripts map[string]ScriptFixture `yaml:"scripts"`

	// SSHFetch maps a remote path → fetched bytes. Used by the SSH
	// port's Fetch method when M4-T06 auto-pulls rke2.yaml.
	SSHFetch map[string]string `yaml:"sshFetch"`
}

// ToolFixture describes one tool's presence/version on the operator's
// workstation. An entry with Installed=false signals "missing"; with
// Installed=true and Version="" signals "present but version unknown".
type ToolFixture struct {
	Installed bool   `yaml:"installed"`
	Version   string `yaml:"version"`
	// Scopes is used for `gh` to express OAuth scope coverage.
	// Empty slice = no scopes recorded.
	Scopes []string `yaml:"scopes"`
}

// HostFixture describes the local node's relevant systemd / kernel /
// NIC state.
type HostFixture struct {
	RKE2Active    bool     `yaml:"rke2Active"`
	RKE2Version   string   `yaml:"rke2Version"`
	NICs          []string `yaml:"nics"`
	KernelModules []string `yaml:"kernelModules"`
	IPv6Enabled   bool     `yaml:"ipv6Enabled"`
	FreeDiskGB    int      `yaml:"freeDiskGB"`
	FreeRAMGB     int      `yaml:"freeRAMGB"`
	NetplanFiles  []string `yaml:"netplanFiles"`
}

// ClusterFixture describes the K8s API state. Nil at the scenario
// level means "unreachable"; an empty struct here means "reachable but
// no flux, no kube-dc-manager, etc."
type ClusterFixture struct {
	APIEndpoint    string                 `yaml:"apiEndpoint"`
	K8sVersion     string                 `yaml:"k8sVersion"`
	Nodes          []NodeFixture          `yaml:"nodes"`
	FluxInstalled  bool                   `yaml:"fluxInstalled"`
	Kustomizations []KustomizationFixture `yaml:"kustomizations"`
	HelmReleases   []HelmReleaseFixture   `yaml:"helmReleases"`
	// CRDs present on the cluster, consumed by mock K8sClient.ListCRDs
	// (the `bootstrap adopt` + init --mode=adopt detection path). Empty
	// = no CRDs modelled (the greenfield default for most scenarios).
	CRDs []string `yaml:"crds"`
	// DeploymentImages: namespace → deployment-name → image:tag
	DeploymentImages map[string]map[string]string `yaml:"deploymentImages"`
	// NodeLabels: node-name → label-map. Lets scenarios mimic NFD
	// labels for the M1-T04 consumer.
	NodeLabels map[string]map[string]string `yaml:"nodeLabels"`
	// SOPSAgeSecret: presence + recipient pubkey. Empty = absent.
	SOPSAgeRecipient string `yaml:"sopsAgeRecipient"`
	// ClusterSecretsPresent reflects whether Secret/cluster-secrets
	// exists in flux-system.
	ClusterSecretsPresent bool `yaml:"clusterSecretsPresent"`
}

// NodeFixture is one K8s Node.
type NodeFixture struct {
	Name   string   `yaml:"name"`
	Status string   `yaml:"status"` // Ready | NotReady
	NICs   []string `yaml:"nics"`
}

// KustomizationFixture is one Flux Kustomization in flux-system. Used
// by mock K8sClient.DiscoverFluxGraph + mock FluxClient.WatchKustomizations.
type KustomizationFixture struct {
	Name        string   `yaml:"name"`
	Namespace   string   `yaml:"namespace"` // defaults to flux-system
	Path        string   `yaml:"path"`
	DependsOn   []string `yaml:"dependsOn"`
	Ready       bool     `yaml:"ready"`
	Reconciling bool     `yaml:"reconciling"`
	Reason      string   `yaml:"reason"`
	Message     string   `yaml:"message"`
	Revision    string   `yaml:"revision"`
}

// HelmReleaseFixture is one HelmRelease. Same shape as the
// Kustomization fixture minus dependsOn (HelmReleases don't carry it
// directly).
type HelmReleaseFixture struct {
	Name         string `yaml:"name"`
	Namespace    string `yaml:"namespace"`
	Ready        bool   `yaml:"ready"`
	Reconciling  bool   `yaml:"reconciling"`
	Reason       string `yaml:"reason"`
	Message      string `yaml:"message"`
	ChartName    string `yaml:"chartName"`
	ChartVersion string `yaml:"chartVersion"`
}

// OpenBaoFixture describes the openbao Helm release state.
type OpenBaoFixture struct {
	Initialized                  bool                `yaml:"initialized"`
	Pods                         []OpenBaoPodFixture `yaml:"pods"`
	BootstrapFinalizedAnnotation string              `yaml:"bootstrapFinalizedAnnotation"` // RFC3339
	ControllerAuthAnnotation     string              `yaml:"controllerAuthAnnotation"`     // RFC3339
	// UnsealKeys are the canonical Shamir shares the mock returns when
	// scripts emit a sentinel payload — must be 5 entries (or 0 to
	// signal "scenario doesn't exercise share capture").
	UnsealKeys []string `yaml:"unsealKeys"`
	RootToken  string   `yaml:"rootToken"`
}

// OpenBaoPodFixture is one pod in the openbao StatefulSet.
type OpenBaoPodFixture struct {
	Name      string `yaml:"name"`
	Sealed    bool   `yaml:"sealed"`
	Version   string `yaml:"version"`
	HAMode    string `yaml:"haMode"` // "active" | "standby" | "" (single-node)
	RaftIndex uint64 `yaml:"raftIndex"`
}

// FleetFixture describes the fleet repo state on the operator's disk.
// Used by M3-T02 fleet-repo locator + M0-T03 existing-fleet scenario.
type FleetFixture struct {
	Cloned           bool     `yaml:"cloned"`
	Path             string   `yaml:"path"` // operator-side path; mock uses for parity
	ClusterDirs      []string `yaml:"clusterDirs"`
	AgeRecipients    []string `yaml:"ageRecipients"`
	OperatorEnrolled bool     `yaml:"operatorEnrolled"`

	// Statuses optionally models the per-cluster status rows the
	// M2 `kube-dc bootstrap status` command renders. When empty,
	// `mock.Factory.StatusRows` falls back to producing one
	// "Unknown" row per `ClusterDirs` entry. When populated, each
	// entry overrides the row for the matching cluster name.
	Statuses []StatusFixture `yaml:"statuses,omitempty"`
}

// StatusFixture is one scenario-baked per-cluster status. Status
// values are unmodeled strings (Ready|Reconciling|Drifted|Failed|
// Unreachable|Unknown) so the YAML doesn't depend on the discover
// package's enums; mock.Factory converts to typed values.
type StatusFixture struct {
	Name        string                    `yaml:"name"`
	Status      string                    `yaml:"status"`
	Detail      string                    `yaml:"detail"`
	Domain      string                    `yaml:"domain"`
	APIURL      string                    `yaml:"apiURL"`
	FixHint     string                    `yaml:"fixHint"`
	Reconcilers []StatusReconcilerFixture `yaml:"reconcilers,omitempty"`
	Drifts      []StatusDriftFixture      `yaml:"drifts,omitempty"`
}

// StatusReconcilerFixture mirrors discover.ReconcilerStatus's
// printable subset — enough for the deep view but not every
// transient condition field.
type StatusReconcilerFixture struct {
	Name      string `yaml:"name"`
	Ready     bool   `yaml:"ready"`
	Suspended bool   `yaml:"suspended"`
	Message   string `yaml:"message"`
}

// StatusDriftFixture mirrors discover.ImageDrift.
type StatusDriftFixture struct {
	Deployment string `yaml:"deployment"`
	Namespace  string `yaml:"namespace"`
	EnvVar     string `yaml:"envVar"`
	Expected   string `yaml:"expected"`
	Running    string `yaml:"running"`
}

// ScriptFixture is one entry under Scripts. The mock runner replays
// the Lines with the configured delays between them, then emits a
// final StreamExit Line carrying ExitCode.
//
// Lines is preferred over the older "delays" map shape (which used
// duration-strings as keys and didn't survive YAML key ordering) —
// authors should use Lines; Delays is retained as a parsing fallback
// for early-draft scenarios.
type ScriptFixture struct {
	Lines    []ScriptLine `yaml:"lines"`
	ExitCode int          `yaml:"exitCode"`

	// Sentinel, when populated, makes the runner emit a sentinel-
	// delimited payload at the configured offset. Used by the
	// openbao-init.sh scenario fixtures to exercise the secret-payload
	// diversion path (agent rule 7).
	Sentinel *ScriptSentinel `yaml:"sentinel,omitempty"`
}

// ScriptLine is one output line the mock runner emits.
type ScriptLine struct {
	// After is a Go duration string ("0s", "200ms", "2s"); the runner
	// sleeps After-into-the-script before emitting this line.
	After string `yaml:"after"`

	// Stream is "stdout" or "stderr". Defaults to "stdout".
	Stream string `yaml:"stream"`

	// Text is the line content (no trailing newline).
	Text string `yaml:"text"`
}

// ScriptSentinel describes a sentinel-delimited payload the script
// emits to stdout. The mock runner does NOT include the payload in the
// stream channel — it routes through any registered SentinelCallback.
type ScriptSentinel struct {
	// After is when (relative to script start) the sentinel block is
	// emitted. Duration string.
	After string `yaml:"after"`

	// Marker is the sentinel name (e.g. "KUBE_DC_INIT_JSON"). The mock
	// emits "<Marker>_BEGIN" then the payload then "<Marker>_END".
	Marker string `yaml:"marker"`

	// Payload is the captured bytes. For the openbao-init scenario
	// this is the canonical JSON shape `{"unseal_keys_b64": [...],
	// "root_token": "..."}` — but the scenario YAML can put anything
	// here; the M5-T01 callback validates.
	Payload string `yaml:"payload"`
}

// Load reads a scenario by name from the embedded scenarios/ FS. Name
// matches the filename without .yaml (e.g. "fresh", "cloud",
// "openbao-sealed"). Returns an error pointing at the available
// scenarios if the name doesn't resolve.
func Load(name string) (*Scenario, error) {
	if name == "" {
		return nil, fmt.Errorf("mock: empty scenario name")
	}
	filename := path.Join("scenarios", name+".yaml")
	raw, err := scenariosFS.ReadFile(filename)
	if err != nil {
		available, _ := ListScenarios()
		return nil, fmt.Errorf("mock: scenario %q not found (available: %s)", name, strings.Join(available, ", "))
	}
	var s Scenario
	if err := yaml.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("mock: parse %s: %w", filename, err)
	}
	if s.Name != "" && s.Name != name {
		return nil, fmt.Errorf("mock: scenario %s declares name=%q (filename/content drift)", filename, s.Name)
	}
	if s.Name == "" {
		s.Name = name
	}
	return &s, nil
}

// ListScenarios returns every scenario name available in the embedded
// FS. Sorted; useful for the error message in Load().
func ListScenarios() ([]string, error) {
	entries, err := scenariosFS.ReadDir("scenarios")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	return names, nil
}

// parseDuration is a centralised helper for the After fields. Returns
// 0 for empty input (line emits immediately).
func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	return time.ParseDuration(s)
}
