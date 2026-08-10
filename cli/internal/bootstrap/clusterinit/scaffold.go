package clusterinit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/shalb/kube-dc/cli/internal/bootstrap/config"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/ports"
)

// M4-T10 — scaffold the new cluster overlay.
//
// **What this slice does**:
//
//   1. Preflight: refuse if `<repo>/clusters/<name>/` already exists.
//   2. Run `bootstrap/add-cluster.sh <name> <domain> <node-external-ip>`
//      via the supplied `ports.ScriptRunner`. The script writes
//      cluster-config.env + secrets.enc.yaml + Flux Kustomization
//      manifests under `<repo>/clusters/<name>/`.
//   3. **Redact script stdout** before the lines reach the
//      caller's `Out` writer. The fleet's add-cluster.sh echoes
//      the freshly-generated KEYCLOAK/GRAFANA/MINIO passwords at
//      the end ("==> Generated passwords"); the redactor catches
//      those lines and rewrites the value portion. Without this
//      the operator's terminal + any captured CI log would carry
//      the plaintext credentials.
//   4. **Verify secrets.enc.yaml is SOPS-encrypted**. The script
//      falls back to writing plaintext when sops + age aren't
//      available, exiting 0 with a WARNING. T10's contract is to
//      refuse this fallback: an unencrypted secrets.enc.yaml would
//      be committed to the fleet repo on the apply path, which is
//      a hard "no" per installer-prd §12.3.
//   5. Post-process the generated cluster-config.env: apply
//      preset's resolved env-map (which already includes operator
//      --set deltas) + the plan's InheritedDefaults version pins.
//      Order + comments preserved via `config.Env`'s in-place
//      Set semantics.
//
// **What this slice does NOT do** (deferred to other slices):
//
//   - M4-T11 customInterfaces patch (writes infrastructure.yaml
//     patches when --node-nic is set).
//   - M4-T12 commit + push + flux-install.sh (the post-scaffold
//     fleet transaction).

// ScaffoldOptions is the parameter bundle for Scaffold. Built by
// the cobra layer's apply path from the loaded Plan; tests build
// it directly with a fake ScriptRunner.
type ScaffoldOptions struct {
	// Plan is the previously-validated init plan. Scaffold reads
	// ClusterName, Domain, Preset, InheritedDefaults from it; never
	// re-derives from fleet state (per the apply-plan verbatim
	// contract documented in plan.go).
	Plan *Plan

	// FleetRepo is the absolute path of the fleet repo on disk
	// (e.g. ~/projects/kube-dc-fleet). Scaffold writes under
	// `<FleetRepo>/clusters/<Plan.ClusterName>/`.
	FleetRepo string

	// NodeExternalIP is the IP the script needs as its third
	// positional arg. Not stored on Plan directly so we pass it
	// separately; the cobra layer reads it from o.NodeExternalIP.
	NodeExternalIP string

	// NodeCIDR is the node's LAN prefix (e.g. 192.168.110.0/24), resolved over
	// SSH by DetectNodeCIDR before the fleet is scaffolded.
	//
	// It is the only Tenant Networking v2 value the installer cannot derive from
	// its own inputs: the node's external IP is a different network, a bare
	// internal IP carries no mask, and on a greenfield install there is no
	// cluster kubeconfig yet to ask. Empty means "could not determine", and
	// dual-homing is then written DISABLED rather than guessed — a wrong prefix
	// installs cleanly and misroutes kubelet probe replies, which is worse than
	// not enabling the feature.
	NodeCIDR string

	// Sets is the resolved operator --set overrides from
	// InitOptions. Layered on top of the preset's defaults during
	// post-process; the Plan struct surfaces overrides via
	// FilesToWrite descriptions but doesn't carry the raw KEY=VALUE
	// map, so the caller (cobra layer) passes them separately.
	Sets map[string]string

	// NodeNICs is the operator --node-nic map (cluster-node-name →
	// primary NIC iface). Triggers the M4-T11 customInterfaces
	// patch step when non-empty; no-op when empty (homogeneous-NIC
	// fleets don't need the patch).
	NodeNICs map[string]string

	// ObjectStorage carries the OS-1 mode + companions. Rook modes
	// trigger the OS-2 writer (step 7: overlay + Flux layer +
	// platform dependsOn + env keys); disabled writes nothing.
	ObjectStorage ObjectStorageSpec

	// VMStorage carries the VM root-disk mode + golden subset. shared-rbd
	// triggers the writer (step 9: rbd-vm.yaml + goldens overlay +
	// kustomization resource); local writes nothing. See vmstorage.go.
	VMStorage VMStorageSpec

	// ImageAccel wires the image-acceleration trio (tenant-addons,
	// cdi-os-mirror, registry-depot). Default-on; see imageaccel.go.
	ImageAccel ImageAccelSpec

	// GPU is the public accelerator contract. It contributes only non-secret
	// substitutions; licenses and registry credentials remain SOPS-owned.
	GPU GPUConfig

	// WildcardTLS is the validated byo-wildcard material (nil = acme mode,
	// nothing scaffolded). Loaded + validated by the cobra layer BEFORE any
	// file is written, so a bad certificate fails the run with zero side
	// effects. See wildcardtls.go.
	WildcardTLS *WildcardTLSMaterial

	// DNS01Route53 is the validated acme-dns01-route53 solver config (nil =
	// other TLS modes, nothing scaffolded). Same contract as WildcardTLS:
	// loaded + validated by the cobra layer before any file is written.
	DNS01Route53 *DNS01Route53Material

	// TrustedCA is validated public CA material for manager/backend/OIDC/OpenBao.
	TrustedCA *TrustedCAMaterial

	// SingleIPNAT triggers the findings-17/17b wiring (step 8): the
	// node sits behind a 1:1 NAT with only one IP, so the scaffolded
	// platform.yaml gets a patches entry removing the Gateway's 6443
	// passthrough listener. Set by the cobra layer from
	// DetectArrivingIP; NodeExternalIP already carries the ARRIVING
	// (internal) IP when this is true.
	SingleIPNAT bool

	// ControlPlaneNodes are the real control-plane node NAMES, resolved from
	// the live cluster when one is reachable. Empty means "unknown", which
	// makes the :6443 set-intersection derivation stand down in favour of the
	// address heuristic. It must never be filled with KUBE_OVN_GW_NODES: the
	// gateway set is not the control-plane set (the reference cluster has a
	// gateway node that is an ordinary worker), and substituting it inverts
	// the listener decision for exactly those nodes.
	ControlPlaneNodes []string

	// KubeconfigPath, when non-empty, is passed to add-cluster.sh as its 4th
	// positional arg so the SCRIPT-side discovery (NODE_CIDR for dual-homing,
	// the INGRESS_HOST_CIDR seed) can read the cluster. The script defaults to
	// ~/.kube/<name>_config, which nothing in the default flow creates —
	// fetch-kubeconfig merges into ~/.kube/config — so on the documented path
	// discovery silently found nothing and every fresh install scaffolded
	// Tenant Networking v2 DISABLED (first fresh install, mod 2026-08-09).
	// The caller must only set this to a kubeconfig it has VERIFIED targets
	// this cluster (the apply path already does, via KubeconfigTargetsCluster —
	// the same guard that protects the ingress-label step).
	KubeconfigPath string

	// Runner is the ports.ScriptRunner the engine calls. Real flow
	// uses the script adapter; tests use a fake.
	Runner ports.ScriptRunner

	// Out is where redacted stdout + status lines go. nil = ioutil.Discard.
	Out io.Writer
}

// --- Errors ---

// ErrScaffoldTargetExists is returned by Scaffold when
// `clusters/<name>/cluster-config.env` already exists. Marker file —
// not the dir — is the canonical "already scaffolded" signal so
// operators can pre-place a `docs/` README inside the overlay before
// running bootstrap.
var ErrScaffoldTargetExists = errors.New("init: scaffold target already initialised (cluster-config.env present)")

// ErrFrontDoorChangeOnResume fires when a resume (existing overlay) is asked
// for a different address layer than the overlay declares. See
// CheckFrontDoorMatchesOverlay for why this refuses rather than applying.
var ErrFrontDoorChangeOnResume = errors.New("init: front-door change cannot ride on a resume")

// ErrScaffoldScriptFailed is returned when add-cluster.sh exits
// non-zero. The error wraps the exit code + last few stderr lines.
var ErrScaffoldScriptFailed = errors.New("init: scaffold script failed")

// ErrScaffoldSecretsNotEncrypted is returned when the script
// completes but secrets.enc.yaml is plaintext on disk (the
// script's sops-not-available fallback path). Hard "no" — the
// caller MUST not commit a plaintext credential file to the fleet
// repo.
var ErrScaffoldSecretsNotEncrypted = errors.New("init: secrets.enc.yaml is unencrypted (sops fallback path triggered — refuse to proceed)")

// --- Engine ---

// Scaffold runs the add-cluster.sh script + post-processes the
// generated cluster-config.env. Returns nil on success, a typed
// error otherwise. The new cluster overlay lives at
// `<opts.FleetRepo>/clusters/<opts.Plan.ClusterName>/` when this
// returns.
func Scaffold(ctx context.Context, opts ScaffoldOptions) error {
	if opts.Plan == nil {
		return fmt.Errorf("scaffold: nil Plan")
	}
	if opts.FleetRepo == "" {
		return fmt.Errorf("scaffold: empty FleetRepo")
	}
	if opts.Runner == nil {
		return fmt.Errorf("scaffold: nil ScriptRunner")
	}
	if opts.NodeExternalIP == "" {
		return fmt.Errorf("scaffold: empty NodeExternalIP")
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	// Fail before add-cluster.sh creates any files when a requested GPU
	// variant is not renderable by the fleet packages in this checkout.
	if err := ValidateGPUScaffold(opts.FleetRepo, opts.GPU); err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}

	// (1) Preflight: refuse if the marker file already exists.
	// We check `cluster-config.env` rather than the bare directory
	// because operators sometimes pre-place a `docs/` folder in the
	// overlay before running bootstrap (e.g. topology notes). The
	// canonical "this overlay was already scaffolded" signal is the
	// presence of cluster-config.env.
	// Cluster name can contain a slash (eu/dc1 shape) — filepath.Join
	// handles that correctly without escaping the fleet root.
	clusterDir := filepath.Join(opts.FleetRepo, "clusters", opts.Plan.ClusterName)
	marker := filepath.Join(clusterDir, "cluster-config.env")
	if _, err := os.Stat(marker); err == nil {
		return fmt.Errorf("%w: %s", ErrScaffoldTargetExists, marker)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("scaffold: stat %s: %w", marker, err)
	}

	// (2) Run the script via ScriptRunner. Args mirror the script's
	// `<name> <domain> <node-external-ip> [kubeconfig-path]` shape;
	// kubeconfig-path is optional and defaults to
	// ~/.kube/<name>_config inside the script — we don't pass it
	// because the CLI's apply path manages kubeconfigs through the
	// kubeconfig package, not the script's default.
	// The reviewed address layer MUST reach the script, because the script derives
	// platform.yaml's spec.components from it and nothing downstream rewrites that list.
	// It used to hardcode metallb-l2 while this package rewrote only the SCALAR
	// afterwards, so a reviewed `none` plan produced an overlay that still selected
	// address-metallb with ENVOY_LB_CLASS=null — an explicit null on a typed field, which
	// server-side apply rejects and force: true converts into a DELETION of the
	// EnvoyProxy. Reproduced; see scripts/check_layer_coherence.py, which now fails any
	// overlay whose layer and components disagree.
	scaffoldEnv := map[string]string{}
	if opts.Plan.IngressAddressLayer != "" {
		scaffoldEnv["SCAFFOLD_INGRESS_ADDRESS_LAYER"] = opts.Plan.IngressAddressLayer
	}
	scriptArgs := []string{opts.Plan.ClusterName, opts.Plan.Domain, opts.NodeExternalIP}
	if opts.KubeconfigPath != "" {
		scriptArgs = append(scriptArgs, opts.KubeconfigPath)
	}
	lines, err := opts.Runner.Run(ctx, ports.ScriptAddCluster, scaffoldEnv, scriptArgs...)
	if err != nil {
		return fmt.Errorf("scaffold: start add-cluster.sh: %w", err)
	}

	// (3) Drain + redact. Track the exit code line (StreamExit) and
	// surface non-zero as ErrScaffoldScriptFailed.
	exitCode, drainErr := drainAndRedactAddCluster(lines, out)
	if drainErr != nil {
		return fmt.Errorf("scaffold: drain: %w", drainErr)
	}
	if exitCode != 0 {
		return fmt.Errorf("%w (exit=%d)", ErrScaffoldScriptFailed, exitCode)
	}

	// (4) Verify secrets.enc.yaml is sops-encrypted. The script's
	// fallback path silently leaves plaintext; we refuse before any
	// downstream commit/push.
	secretsPath := filepath.Join(clusterDir, "secrets.enc.yaml")
	if err := verifySOPSEncrypted(secretsPath); err != nil {
		return err
	}

	// (5) Post-process cluster-config.env: apply preset defaults +
	// operator --set overrides + inherited version pins. Order and
	// comments preserved via config.Env's in-place Set.
	envPath := filepath.Join(clusterDir, "cluster-config.env")
	if err := postProcessClusterConfig(envPath, opts.Plan, opts.Sets, opts.NodeCIDR, opts.GPU); err != nil {
		return fmt.Errorf("scaffold: post-process %s: %w", envPath, err)
	}

	// (6) M4-T11 customInterfaces patch — apply the inline Kustomize
	// patch when the operator supplied --node-nic mappings. No-op
	// when NodeNICs is empty (homogeneous-NIC fleets don't need
	// the patch).
	if len(opts.NodeNICs) > 0 {
		infraPath := filepath.Join(clusterDir, "infrastructure.yaml")
		if err := WriteCustomInterfacesPatch(infraPath, opts.NodeNICs); err != nil {
			return fmt.Errorf("scaffold: customInterfaces patch: %w", err)
		}
		fmt.Fprintf(out, "[scaffold] customInterfaces patch applied (%d nodes)\n", len(opts.NodeNICs))
	}

	// (7) OS-2 object-storage wiring — overlay + Flux layer +
	// platform dependsOn + kustomization resources + env keys.
	// No-op for disabled mode.
	if err := WriteObjectStorage(opts.FleetRepo, opts.Plan.ClusterName, opts.Plan.Domain, opts.ObjectStorage, out); err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}

	// (7b) infra-public-network — presets with a routed public VLAN
	// (cloud+public-vlan) list this layer, and the plan prints it, but
	// infrastructure.yaml is written by add-cluster.sh which has no preset
	// awareness. Without this the EXT_PUBLIC_* keys are scaffolded and then
	// consumed by nothing: no ext-public Vlan/Subnet, so no public EIP pool
	// and no tenant public EIP pool (a MetalLB VIP uses this VLAN only when speakers also have a host-facing interface). No-op for other presets.
	if err := WritePublicNetwork(opts.FleetRepo, opts.Plan.ClusterName, opts.Plan.Preset, out); err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}

	// (7c) addons/addons-config — MetalLB. Same shape of omission as (7b):
	// add-cluster.sh writes and preset.go validates INGRESS_MODE +
	// METALLB_*, but nothing emitted the layers that consume them, so the
	// DEFAULT ingress path installed no MetalLB at all. The Envoy Service
	// then never gets a LoadBalancer IP and the Gateway silently falls back
	// to a node address — front door up, configured VIP unused, no failover.
	// No-op for INGRESS_MODE=hostnetwork.
	if err := WriteAddons(opts.FleetRepo, opts.Plan.ClusterName, out); err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}

	// (8) Single-IP NAT wiring (findings 17/17b) — drop the 6443
	// passthrough listener via a platform.yaml spec.patches entry.
	// MUST stay after step 7: the OS-4 disabled writer refuses any
	// pre-existing patches: key, including ours; this writer composes
	// with the OS-4 block when both fire. Apply-time detected (SSH
	// probe), so dry-run plans don't list it — the substitution +
	// patch are logged at apply.
	// Also fire when NODE_EXTERNAL_IP is simply one of the cluster's own node
	// addresses. That is the same collision without any NAT, so the SSH probe
	// above reports nothing — and under --no-ssh it never runs at all.
	// PREFERRED: derive from the ingress-node set. Envoy binds the listener
	// ports on the nodes carrying the ingress label, so "does Envoy share a
	// node with kube-apiserver?" is a set intersection — not a guess about
	// whether NODE_EXTERNAL_IP equals a control-plane address (codex
	// 2026-08-07, P1). Falls back to the address heuristics when the operator
	// declared no ingress nodes, so pre-v2 behaviour is preserved exactly.
	envBody := ""
	if body, err := os.ReadFile(filepath.Join(opts.FleetRepo, "clusters", opts.Plan.ClusterName, "cluster-config.env")); err == nil {
		envBody = string(body)
	}
	collides := opts.SingleIPNAT
	// KUBE_OVN_GW_NODES is NOT the control-plane set and must never be
	// substituted for it. Verified live: the reference cluster has four
	// external-gateway nodes and only three control-plane nodes, so one gateway
	// node is an ordinary worker. Comparing against the gateway set therefore
	// INVERTS the answer for that node — it would report "an ingress node is
	// also a control-plane node" and drop the tenant kube-API listener from a
	// worker where it would have bound fine.
	//
	// Nothing in cluster-config.env carries control-plane node NAMES
	// (KUBE_OVN_MASTER_NODES holds their IPs), so the set-intersection
	// derivation is used only when the caller supplies real names. Otherwise
	// fall back to the address heuristic, which is exactly the pre-v2
	// behaviour — a weaker signal, but not a wrong one.
	if derived, ok := IngressCollidesWithAPIServer(
		opts.Plan.IngressNodes, opts.ControlPlaneNodes,
	); ok {
		collides = derived
		// State which listener this is, because the obvious guess is wrong: the
		// :6443 listener carries kube-api.<domain> — the MANAGEMENT API — and NOT
		// managed tenant clusters, which are on :443 via the wildcard
		// passthrough listener and are unaffected by this decision either way.
		onCP, offCP, _ := ClassifyIngressPlacement(opts.Plan.IngressNodes, opts.ControlPlaneNodes)
		fmt.Fprintf(out, "[scaffold] :6443 management-API listener: %s\n",
			map[bool]string{
				true: fmt.Sprintf("REMOVED — ingress runs on control-plane node(s) %s, where kube-apiserver "+
					"owns :6443, so Envoy could never bind it. Each apiserver serves :6443 itself",
					strings.Join(onCP, ",")),
				false: fmt.Sprintf("KEPT — ingress runs off the control plane (%s), so Envoy can bind :6443",
					strings.Join(offCP, ",")),
			}[derived])
	} else if !collides && envBody != "" {
		collides = GatewayCollidesWithAPIServer(envBody)
	}
	if collides {
		if err := WriteSingleIPNATPatch(opts.FleetRepo, opts.Plan.ClusterName, out); err != nil {
			return fmt.Errorf("scaffold: %w", err)
		}
	}

	// (9) VM root-disk storage wiring — rbd-vm.yaml (base + goldens Flux
	// Kustomizations) + the selected FS golden subset overlay +
	// kustomization resource. No-op for local; fails closed for
	// shared-rbd-live-migration (Validate already refuses it, this is
	// defense in depth). Independent of the object-storage kustomization
	// patch (adds a different resource entry).
	if err := WriteVMStorage(opts.FleetRepo, opts.Plan.ClusterName, opts.VMStorage, out); err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}

	// (9b) Tenant-cluster BASELINE — cilium CNI, coredns, metrics-server.
	// Unconditional: these are the floor a managed cluster stands on, not a
	// feature. Wired before the accelerators so an error here surfaces first.
	if err := WriteTenantBaseline(opts.FleetRepo, opts.Plan.ClusterName, out); err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}

	// (9c) Image-acceleration pair (cdi-os-mirror + registry-depot).
	// Default-on; pieces absent from an older starter are skipped with a
	// warning. tenant-addons deliberately no longer rides here — see (9b)
	// and the WriteTenantBaseline doc comment.
	if err := WriteImageAccel(opts.FleetRepo, opts.Plan.ClusterName, opts.ImageAccel, out); err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}

	// (10) GPU ownership stack — exact-node NFD rules followed by the GPU
	// Operator and optional HAMi Flux layers. No-op for disabled/detect-only;
	// catalog, billing, and both workload-creation gates remain independent.
	if err := WriteGPUInfrastructure(opts.FleetRepo, opts.Plan.ClusterName, opts.GPU, out); err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}

	// (11) byo-wildcard TLS — the SOPS secret set + ACME suppression +
	// TLS_MODE marker. MUST stay after (7) and (9b): the suppression patches
	// target infra-object-storage.yaml and registry-depot.yaml, which those
	// steps write; run earlier, the layers would exist without their
	// Certificates suppressed and cert-manager would fight the operator's
	// material. Nil material = acme mode = no-op.
	if err := WriteWildcardTLS(opts.FleetRepo, opts.Plan.ClusterName, opts.Plan.Domain, opts.WildcardTLS, out); err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}

	// (11b) acme-dns01-route53 — the ClusterIssuer solver patch + SOPS
	// credential. Independent of (11): the two modes are mutually exclusive
	// (ValidateTLSMode), so at most one of the writers fires. Needs only
	// platform.yaml, which add-cluster.sh writes unconditionally.
	if err := WriteDNS01Route53(opts.FleetRepo, opts.Plan.ClusterName, opts.DNS01Route53, out); err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}

	// (12) private-CA trust — one public ConfigMap consumed by manager +
	// backend; manager propagates the same bundle to OIDC and OpenBao.
	if err := WriteTrustedCA(opts.FleetRepo, opts.Plan.ClusterName, opts.TrustedCA, out); err != nil {
		return fmt.Errorf("scaffold: %w", err)
	}

	fmt.Fprintf(out, "[scaffold] cluster overlay created at %s\n", clusterDir)
	return nil
}

// --- Stdout redaction ---

// addClusterPasswordPrefix matches the password lines the script
// echoes ("    KEYCLOAK_ADMIN_PASSWORD: xyz"). The capture group
// is the leading indent + KEY: portion that we keep verbatim; the
// trailing value is replaced with the redaction sentinel. We match
// both single-space and multi-space-after-colon variants ("KEY:
// value" + "KEY:  value") because the script's column-aligned
// output uses one or two spaces.
//
// The pattern intentionally excludes the value to match — matching
// arbitrary trailing content is what makes the redaction safe
// even if the value contains characters that would break a stricter
// pattern.
var addClusterPasswordPrefix = regexp.MustCompile(`^(\s+[A-Z_]+_PASSWORD:\s+)\S.*$`)

// redactAddClusterLine returns the redacted form of `line` when it
// matches the password-echo pattern, or `line` unchanged otherwise.
// Exported for testing.
func redactAddClusterLine(line string) string {
	if loc := addClusterPasswordPrefix.FindStringSubmatchIndex(line); loc != nil {
		// loc[2:4] is the capture group's start/end indices.
		prefix := line[loc[2]:loc[3]]
		return prefix + "[REDACTED — see secrets.enc.yaml]"
	}
	return line
}

// drainAndRedactAddCluster reads `lines` until the channel closes,
// writes each (redacted) line to `out`, and returns the exit code from
// the terminal StreamExit record.
//
// It used to return (0, nil) when the channel closed with no exit line,
// documented as a "defensive fallback; real adapter always emits one".
// That fallback was the failure: a killed script or a dead runner
// produces exactly that shape, and add-cluster would read it as success
// and continue into post-processing on output it never received.
// ports.Drain now enforces the ScriptRunner contract for every engine.
func drainAndRedactAddCluster(lines <-chan ports.Line, out io.Writer) (int, error) {
	return ports.Drain(lines, func(ln ports.Line) {
		streamTag := "stdout"
		if ln.Stream == ports.StreamStderr {
			streamTag = "stderr"
		}
		fmt.Fprintf(out, "[%s] %s\n", streamTag, redactAddClusterLine(ln.Text))
	})
}

// --- SOPS encryption verification ---

// verifySOPSEncrypted returns nil when `path`'s content looks
// SOPS-encrypted; ErrScaffoldSecretsNotEncrypted otherwise.
//
// **Detection strategy**: a SOPS-encrypted YAML file always
// contains a top-level `sops:` mapping with `mac:` and per-recipient
// `enc:` blobs. Plaintext secrets.enc.yaml lacks all three. We
// search for both `sops:` (at column 0) AND `ENC[AES256_GCM,` —
// either being absent means the file isn't encrypted.
func verifySOPSEncrypted(path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("scaffold: read %s: %w", path, err)
	}
	bodyStr := string(body)

	// Top-level `sops:` mapping at column 0 (multi-line check).
	hasSopsBlock := strings.Contains(bodyStr, "\nsops:") || strings.HasPrefix(bodyStr, "sops:")
	hasEncMarker := strings.Contains(bodyStr, "ENC[AES256_GCM,")

	if hasSopsBlock && hasEncMarker {
		return nil
	}
	// Build a helpful error: name the path + remediation.
	return fmt.Errorf("%w: %s — run `sops -e -i %s` after configuring an age key in .sops.yaml",
		ErrScaffoldSecretsNotEncrypted, path, path)
}

// --- cluster-config.env post-processing ---

// postProcessClusterConfig applies the preset's resolved env map +
// the plan's InheritedDefaults to the env file generated by
// add-cluster.sh. Existing keys are updated in-place; new keys are
// appended at the end under a dedicated comment section.
//
// **Why preset defaults flow through too**: add-cluster.sh writes
// CHANGEME placeholders for VLAN_ID + INTERFACE; the preset's
// resolved env map carries the real operator-supplied values via
// --set. Without this step the committed env would still say
// CHANGEME.
//
// **Why inherited defaults flow through**: existing-fleet mode
// derives version pins from siblings (M4-T13). The new cluster's
// env should pin to those values so it joins the fleet at the
// same upgrade-cycle position.
//
// Order: preset+set values WIN over inherited defaults (so operator
// explicit override beats fleet inheritance), which WIN over the
// script's CHANGEME defaults. config.Env.Set preserves the
// original line position when a key already exists; otherwise
// appends.
func postProcessClusterConfig(path string, plan *Plan, sets map[string]string, nodeCIDR string, gpu ...GPUConfig) error {
	env, err := config.LoadEnv(path)
	if err != nil {
		return err
	}

	// Build the merged map: preset defaults → inherited defaults →
	// operator --set deltas (operator wins). EnvMapFor handles
	// preset defaults + --set merge for us; layer inherited on top
	// only when the operator didn't override.
	// The address layer is a DEDICATED plan field (and is rejected via --set,
	// so it is not in `sets`). Inject the REVIEWED plan's value so EnvMapFor
	// derives the Envoy service shape the operator actually approved; without
	// this a reviewed metallb-l2 plan scaffolded as none/ClusterIP (codex
	// 2026-08-07, P1). An explicit --set of a derived ENVOY_* scalar still
	// wins inside EnvMapFor, and the validator reports incoherence.
	layered := sets
	if plan.IngressAddressLayer != "" || plan.IngressHostCIDR != "" {
		layered = make(map[string]string, len(sets)+2)
		for k, v := range sets {
			layered[k] = v
		}
		if plan.IngressAddressLayer != "" {
			layered["INGRESS_ADDRESS_LAYER"] = plan.IngressAddressLayer
		}
		// Derived during Apply from the ingress nodes' InternalIPs. Without it the
		// generated overlay carries an empty INGRESS_HOST_CIDR, and the platform
		// NetworkPolicies then admit nothing from the host-bound Envoy — OpenBao and the
		// Flux UI answer 503 with every manifest correct and Flux green. An explicit
		// --set still wins, because `sets` is copied in first and EnvMapFor prefers it.
		if plan.IngressHostCIDR != "" {
			if _, pinned := sets["INGRESS_HOST_CIDR"]; !pinned {
				layered["INGRESS_HOST_CIDR"] = plan.IngressHostCIDR
			}
		}
		// ENVOY_REPLICAS is one Envoy per ingress node — EQUALITY, not "enough".
		//
		// The host-bind component has REQUIRED pod anti-affinity, so more replicas than
		// labelled nodes strands the surplus Pending forever; fewer, and some labelled node
		// has no Envoy, which on a single-address cluster can be exactly the node that owns
		// the address — a dark front door with every pod Ready. The component defaulted to
		// 3 and the installer never derived it, so a 2-node or 4-node ingress set was wrong
		// out of the box. frontdoor-check.sh preflight already asserts the equality; this
		// is what makes it hold.
		if n := len(plan.IngressNodes); n > 0 {
			if _, pinned := sets["ENVOY_REPLICAS"]; !pinned {
				layered["ENVOY_REPLICAS"] = strconv.Itoa(n)
			}
		}
	}
	merged, err := EnvMapFor(plan.Preset, layered)
	if err != nil {
		return fmt.Errorf("scaffold: EnvMapFor: %w", err)
	}
	for k, v := range plan.InheritedDefaults {
		if _, set := sets[k]; set {
			continue // operator override beats inheritance
		}
		// Use the inherited value only if it's not in the
		// preset's defaults (preset defaults already cover
		// network shape; inherited defaults are version pins).
		merged[k] = v
	}
	if len(gpu) > 0 {
		for k, v := range GPUConfigEnv(gpu[0]) {
			merged[k] = v
		}
	}

	// Walk merged in stable key order so the file written when a
	// new key is appended is deterministic across runs (Go map
	// iteration is randomised — without sorting, two consecutive
	// scaffold runs against the same inputs could produce
	// different file orderings, breaking diff review).
	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env.Set(k, merged[k])
	}

	// OVN_DB_IPS is not an independent topology input: it is the same
	// control-plane address set as KUBE_OVN_MASTER_NODES, with the OVN NB
	// transport and port attached. Derive it unless the operator explicitly
	// overrides it (e.g. ssl endpoints). This removes a duplicated value that
	// the starter previously left as CHANGEME and that failed only when the
	// first Project reconciled.
	if _, overridden := sets["OVN_DB_IPS"]; !overridden {
		masters := strings.TrimSpace(merged["KUBE_OVN_MASTER_NODES"])
		if masters != "" {
			endpoints := make([]string, 0, strings.Count(masters, ",")+1)
			for _, master := range strings.Split(masters, ",") {
				master = strings.TrimSpace(master)
				if master != "" {
					endpoints = append(endpoints, "tcp:"+master+":6641")
				}
			}
			env.Set("OVN_DB_IPS", strings.Join(endpoints, ","))
		}
	}

	// Tenant Networking v2 routes. Written here rather than left to the scaffold
	// script because the CLI is the only party that knows the node prefix: the
	// script tries to read it from a cluster kubeconfig, which does not exist yet
	// on a greenfield install.
	//
	// An operator --set always wins — someone who typed the value explicitly
	// knows their topology better than a route lookup does.
	if _, overridden := sets["INFRA_ATTACHMENT_ROUTES"]; !overridden {
		// The SCRIPT may have discovered the node network itself (it reads the
		// kubeconfig the CLI now hands it). When the CLI-side SSH detection came
		// up empty but the file already carries a discovered NODE_CIDR, blanking
		// it here would throw away a correct answer and scaffold dual-homing
		// DISABLED on the kubeconfig-only path (2026-08-09 review of the mod
		// fixes) — the CLI-side value still wins when both exist, because the
		// route lookup on the node beats the script's /24 assumption.
		fileNodeCIDR := ""
		if v, ok := env.Get("NODE_CIDR"); ok {
			fileNodeCIDR = strings.TrimSpace(v)
		}
		effective := nodeCIDR
		if effective == "" {
			effective = fileNodeCIDR
		}
		if effective != "" {
			env.Set("NODE_CIDR", effective)
			env.Set("INFRA_ATTACHMENT_ROUTES",
				strings.Join([]string{effective, merged["JOIN_CIDR"], merged["POD_CIDR"]}, ","))
			env.Set("INFRA_ATTACHMENT_ENABLED", "true")
		} else {
			// Fail safe, never fatal. A placeholder would pass the chart's
			// non-empty check and then be rejected by net.ParseCIDR in the
			// manager, giving a cluster whose kube-dc-manager CrashLoopBackOffs.
			// Disabled means the cluster boots on the legacy path and dual-homing
			// is turned on afterwards, once the prefix is known.
			env.Set("NODE_CIDR", "")
			env.Set("INFRA_ATTACHMENT_ROUTES", "")
			env.Set("INFRA_ATTACHMENT_ENABLED", "false")
		}
	}

	// Resolve MANAGEMENT_API_MODE=auto now that INFRA_ATTACHMENT_ENABLED and the
	// service-CIDR inputs are final.
	//
	// WHY THIS EXISTS. The mode used to default to "external", which reaches the
	// management API only through kube-api.<domain> — an endpoint that a tenant
	// VPC pod cannot route to on a private/single-ingress cluster (the tenant
	// networks are OVN-isolated from any external address, and on a single-IP
	// cluster the :6443 SNI listener is stripped besides). So a fresh private
	// install came up with NO working tenant->API path at all: CNPG bootstrap,
	// CSI/CCM, the cloud shell and the cluster-autoscaler all failed, because the
	// management-api-client role — the only thing that injects the /32 route to
	// the apiserver ClusterIP over the dual-home NIC — is granted ONLY in service
	// mode (internal/infraattachment/config.go). The service datapath was fully
	// implemented and the PRD picked it as the intended installer default
	// (docs/prd/mgmt-api-connectivity-deep-dive.md); the scaffold default was
	// simply never advanced. Found live on the mod pilot 2026-08-10.
	//
	// Resolution: service is the default WHENEVER it is viable — dual-homing on
	// with a canonical K8S_SERVICE_IP inside SVC_CIDR (the manager's own
	// service-mode precondition). Otherwise external, the only thing that can
	// work without an infra route. An operator who typed external or service
	// explicitly is never overridden; "auto" is resolved before the file is
	// written and never ships.
	if strings.EqualFold(strings.TrimSpace(envGet(env, "MANAGEMENT_API_MODE")), "auto") {
		resolved := "external"
		if strings.TrimSpace(envGet(env, "INFRA_ATTACHMENT_ENABLED")) == "true" &&
			serviceModeViable(envGet(env, "K8S_SERVICE_IP"), envGet(env, "SVC_CIDR")) {
			resolved = "service"
		}
		env.Set("MANAGEMENT_API_MODE", resolved)
	}

	// Under a no-VIP address layer the starter's MetalLB placeholders are DEAD
	// KEYS: addons.go deliberately installs no MetalLB layers and the
	// address-metallb component is not selected, so nothing ever substitutes
	// them. Leaving them as CHANGEME made the blanket placeholder scan below
	// refuse the exact topology layer=none exists for — a site with NO spare
	// address to give (first fresh install, mod 2026-08-09; predicted by the
	// 2026-08-07 review: "the none path aborts because the MetalLB CHANGEME
	// values survive"). Blank them so the file reads "not configured", not
	// "forgot to fill in". Only untouched placeholders are blanked — an
	// operator-supplied real value (a spare reservation to hold, the cs
	// pattern) passes through unchanged.
	deadMetalLBKeys := []string(nil)
	switch {
	case !addressLayerRequiresVIP(plan.IngressAddressLayer):
		// No VIP layer: neither key is ever substituted.
		deadMetalLBKeys = []string{"METALLB_FLOATING_IP", "METALLB_INTERFACE"}
	case strings.EqualFold(strings.TrimSpace(plan.IngressAddressLayer), AddressLayerMetalLBBGP):
		// BGP announces the VIP as a /32 to a peer — there is no L2 interface to
		// bind, so METALLB_INTERFACE is dead there too. Without this the bgp
		// layer aborted on the starter's CHANGEME exactly like none did, and an
		// operator could only "fix" it by supplying a meaningless value
		// (2026-08-09 review of the mod fixes). METALLB_FLOATING_IP stays
		// load-bearing and still refuses as a placeholder.
		deadMetalLBKeys = []string{"METALLB_INTERFACE"}
	}
	for _, k := range deadMetalLBKeys {
		if v, ok := env.Get(k); ok && strings.HasPrefix(strings.TrimSpace(v), "CHANGEME") {
			env.Set(k, "")
		}
	}

	// Re-run semantic validation against the exact file we are about to
	// publish, including derived values. Then fail closed if any starter
	// placeholder survived. Every current CHANGEME is load-bearing
	// (OVN, ProviderNetwork, or MetalLB under a VIP layer); allowing one
	// through merely defers the failure to Flux or the first tenant reconcile.
	finalValues := env.AsMap()
	if err := ValidatePresetValues(plan.Preset, finalValues); err != nil {
		return fmt.Errorf("scaffold: final cluster-config.env: %w", err)
	}
	for key, value := range finalValues {
		if strings.HasPrefix(strings.TrimSpace(value), "CHANGEME") {
			return fmt.Errorf("scaffold: final cluster-config.env still contains placeholder %s=%s; provide it with --set %s=<value>", key, value, key)
		}
	}
	// The auto sentinel must have been resolved above. Shipping "auto" would make
	// the HR pass it to the chart, whose `eq .mode "service"` is false, silently
	// landing external — the exact wrong mode auto exists to avoid. Fail loudly.
	if strings.EqualFold(strings.TrimSpace(finalValues["MANAGEMENT_API_MODE"]), "auto") {
		return fmt.Errorf("scaffold: MANAGEMENT_API_MODE=auto was not resolved before write (internal error)")
	}

	if err := env.Write(""); err != nil {
		return err
	}
	return nil
}

// envGet is a small adapter over config.Env.Get, which returns (value, ok); the
// scaffold's mode resolution only cares about the trimmed value.
func envGet(env *config.Env, key string) string {
	if v, ok := env.Get(key); ok {
		return v
	}
	return ""
}

// serviceModeViable reports whether the management-API service datapath can be
// selected: a canonical IPv4 K8S_SERVICE_IP that lies inside SVC_CIDR. This is
// the same precondition the manager enforces (internal/infraattachment,
// validateManagementAPI) — checked here so `auto` never resolves to a service
// mode the manager would then reject at startup.
func serviceModeViable(serviceIP, serviceCIDR string) bool {
	ipText := strings.TrimSpace(serviceIP)
	ip := net.ParseIP(ipText)
	if ip == nil || ip.To4() == nil || ip.String() != ipText {
		return false
	}
	_, cidr, err := net.ParseCIDR(strings.TrimSpace(serviceCIDR))
	if err != nil || cidr.IP.To4() == nil {
		return false
	}
	return cidr.Contains(ip)
}
