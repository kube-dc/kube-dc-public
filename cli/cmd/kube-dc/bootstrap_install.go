package main

import (
	"fmt"
	"net"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shalb/kube-dc/cli/internal/bootstrap"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/clusterinit"
	"github.com/shalb/kube-dc/cli/internal/bootstrap/rke2"
)

// bootstrapInstallCmd registers `kube-dc bootstrap install` (V1 — host
// RKE2 driver). It brings a bare Ubuntu node up as an RKE2 control
// plane with the canonical kube-dc config (cni:none, advertise-address
// = internal IP, preset-matched CIDRs, memory-tiered kubelet reserves +
// max-pods), over SSH. This is step 1 of the one-command install:
//
//	kube-dc bootstrap install --ssh-host root@node --domain acme.com --name dc1
//	kube-dc bootstrap fetch-kubeconfig dc1 --ssh-host root@node --domain acme.com
//	kube-dc bootstrap init --name dc1 --domain acme.com --ssh-host root@node ... --yes
//
// The RKE2 CIDRs come from the SAME preset the operator will pass to
// `init`, so kube-ovn and the fleet never disagree (the class of bug
// behind E2E findings 12/13). See internal/bootstrap/rke2.
func bootstrapInstallCmd(fleetRepo *string) *cobra.Command {
	var (
		sshHost     string
		domain      string
		nodeName    string
		preset      string
		sets        []string
		nodeIP      string
		externalIP  string
		rke2Version string
		force       bool
		dryRun      bool
	)
	cmd := &cobra.Command{
		Use:           "install <cluster-node>",
		Short:         "Install RKE2 on a control-plane node over SSH (V1 — host driver)",
		SilenceErrors: true,
		SilenceUsage:  true,
		Long: `Installs RKE2 on a bare Ubuntu node as a kube-dc control plane, over
SSH, with the canonical config:
  - cni: none            (kube-ovn is installed later by Flux via 'init')
  - advertise-address    = the node's internal IP (never a NAT/floating
                           public IP — E2E finding 13)
  - cluster/service CIDRs = resolved from --preset (the SAME source
                           'bootstrap init' uses, so they can't drift)
  - kubelet system/kube-reserved + max-pods = auto-tiered from node memory

The node comes up NotReady (no CNI yet) — that is expected. Finish the
install with 'bootstrap fetch-kubeconfig' then 'bootstrap init'.

Required flags:
  --ssh-host <endpoint>  Node SSH endpoint: user@host or a ~/.ssh/config alias.
  --domain <fqdn>        Cluster public FQDN (drives tls-san).
  --name <node-name>     RKE2 node-name; use the SAME name in 'init'
                         (--rook-osd-node, KUBE_OVN_MASTER_NODES).

SSH auth: ssh-agent first, then ~/.ssh/config IdentityFile (never a
--ssh-key flag). Passwordless sudo (or a root login) is required on the
node — the installer runs 'sudo -n'.`,
		Example: `  # Review first, then run
  kube-dc bootstrap install dc1 --ssh-host root@203.0.113.10 \
    --domain acme.com --name dc1 --preset cloud+public-vlan --dry-run
  kube-dc bootstrap install dc1 --ssh-host root@203.0.113.10 \
    --domain acme.com --name dc1 --preset cloud+public-vlan

  # Lab single node
  kube-dc bootstrap install lab --ssh-host ubuntu@192.0.2.10 \
    --domain lab.example.com --name lab --preset internal-only`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The positional arg is a convenience label for --name.
			if nodeName == "" && len(args) == 1 {
				nodeName = args[0]
			}
			if sshHost == "" {
				return fmt.Errorf("bootstrap install: --ssh-host is required (user@host or a ~/.ssh/config alias)")
			}
			if domain == "" {
				return fmt.Errorf("bootstrap install: --domain is required")
			}
			if nodeName == "" {
				return fmt.Errorf("bootstrap install: --name (node name) is required")
			}

			// Validate the fields that get written into the RKE2 config,
			// reusing init's field validators so both commands agree on
			// what a valid domain/name/IP is (a typo here would otherwise
			// land in /etc/rancher/rke2/config.yaml on the node).
			if err := clusterinit.ValidateDomainField(domain); err != nil {
				return fmt.Errorf("bootstrap install: --domain: %w", err)
			}
			if err := clusterinit.ValidateK8sNodeNameField(nodeName); err != nil {
				return fmt.Errorf("bootstrap install: --name: %w", err)
			}
			if nodeIP != "" {
				if err := clusterinit.ValidateNodeIPField(nodeIP); err != nil {
					return fmt.Errorf("bootstrap install: --node-ip: %w", err)
				}
			}
			if externalIP != "" {
				if err := clusterinit.ValidateNodeIPField(externalIP); err != nil {
					return fmt.Errorf("bootstrap install: --external-ip: %w", err)
				}
			}

			podCIDR, svcCIDR, clusterDNS, err := resolveInstallCIDRs(preset, sets)
			if err != nil {
				return fmt.Errorf("bootstrap install: %w", err)
			}

			sshClient, err := bootstrap.NewSSHOnly()
			if err != nil {
				return fmt.Errorf("bootstrap install: build ssh adapter: %w", err)
			}

			out := cmd.OutOrStdout()
			return rke2.Install(cmd.Context(), rke2.InstallOptions{
				SSH:         sshClient,
				Host:        parseSSHHostArg(sshHost),
				NodeName:    nodeName,
				Domain:      domain,
				PodCIDR:     podCIDR,
				ServiceCIDR: svcCIDR,
				ClusterDNS:  clusterDNS,
				NodeIP:      nodeIP,
				ExternalIP:  externalIP,
				RKE2Version: rke2Version,
				Force:       force,
				DryRun:      dryRun,
				Out:         out,
			})
		},
	}
	cmd.Flags().StringVar(&sshHost, "ssh-host", "", "Node SSH endpoint — `user@host` or a ~/.ssh/config alias (required)")
	cmd.Flags().StringVar(&domain, "domain", "", "Cluster public FQDN — drives tls-san (required)")
	cmd.Flags().StringVar(&nodeName, "name", "", "RKE2 node-name (required; or pass as the positional arg)")
	cmd.Flags().StringVar(&preset, "preset", "internal-only", "Network preset for CIDR defaults: internal-only|cloud-vlan|cloud+public-vlan|custom")
	cmd.Flags().StringArrayVar(&sets, "set", nil, "Override a preset value, e.g. --set POD_CIDR=10.100.0.0/16 (repeatable)")
	cmd.Flags().StringVar(&nodeIP, "node-ip", "", "Node internal IP + apiserver advertise-address (default: auto-detected over SSH)")
	cmd.Flags().StringVar(&externalIP, "external-ip", "", "RKE2 node-external-ip (default: same as --node-ip)")
	cmd.Flags().StringVar(&rke2Version, "rke2-version", "", "RKE2 version (default: the pinned kube-dc default)")
	cmd.Flags().BoolVar(&force, "force", false, "Re-run even if rke2-server is already active on the node")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Resolve + print the plan (incl. read-only SSH probes); change nothing")
	_ = fleetRepo // install is fleet-independent (self-contained embedded installer); flag accepted for parity
	return cmd
}

// resolveInstallCIDRs pulls POD_CIDR / SERVICE_CIDR / CLUSTER_DNS from
// the preset's defaults with --set overrides layered on. It uses
// SpecFor (not EnvMapFor) deliberately: install only needs the three
// CIDRs, which every preset defines, and must NOT fail on a preset's
// network-required keys (EXT_NET_*) that are irrelevant to RKE2.
func resolveInstallCIDRs(preset string, sets []string) (pod, svc, dns string, err error) {
	spec, ok := clusterinit.SpecFor(clusterinit.Preset(preset))
	if !ok {
		return "", "", "", fmt.Errorf("unknown preset %q (want internal-only|cloud-vlan|cloud+public-vlan|custom)", preset)
	}
	vals := map[string]string{
		"POD_CIDR":     spec.Defaults["POD_CIDR"],
		"SERVICE_CIDR": spec.Defaults["SVC_CIDR"], // fleet env key is SVC_CIDR; RKE2/script expects SERVICE_CIDR
		"CLUSTER_DNS":  spec.Defaults["CLUSTER_DNS"],
	}
	for _, kv := range sets {
		k, v, found := strings.Cut(kv, "=")
		if !found {
			return "", "", "", fmt.Errorf("malformed --set %q (want KEY=VALUE)", kv)
		}
		k = strings.TrimSpace(k)
		switch k {
		case "POD_CIDR":
			vals["POD_CIDR"] = strings.TrimSpace(v)
		case "SVC_CIDR":
			// SVC_CIDR is the ONLY accepted service-CIDR key — it's what
			// `bootstrap init`/the fleet use. SERVICE_CIDR is deliberately
			// rejected below: it works here but is a silent no-op in init,
			// so allowing it would let RKE2 and the fleet drift on the
			// service CIDR — the exact invariant this feature protects.
			vals["SERVICE_CIDR"] = strings.TrimSpace(v)
		case "SERVICE_CIDR":
			return "", "", "", fmt.Errorf("use --set SVC_CIDR=... (not SERVICE_CIDR) to match `bootstrap init` — SERVICE_CIDR is ignored by init and would drift RKE2 from the fleet")
		case "CLUSTER_DNS":
			vals["CLUSTER_DNS"] = strings.TrimSpace(v)
		}
	}
	// Semantic validation — these values are written verbatim into the
	// RKE2 config, so a bad CIDR/IP must fail here, not on the node.
	if _, _, err := net.ParseCIDR(vals["POD_CIDR"]); err != nil {
		return "", "", "", fmt.Errorf("POD_CIDR %q is not a valid CIDR", vals["POD_CIDR"])
	}
	if _, _, err := net.ParseCIDR(vals["SERVICE_CIDR"]); err != nil {
		return "", "", "", fmt.Errorf("SVC_CIDR %q is not a valid CIDR", vals["SERVICE_CIDR"])
	}
	if net.ParseIP(vals["CLUSTER_DNS"]) == nil {
		return "", "", "", fmt.Errorf("CLUSTER_DNS %q is not a valid IP", vals["CLUSTER_DNS"])
	}
	return vals["POD_CIDR"], vals["SERVICE_CIDR"], vals["CLUSTER_DNS"], nil
}
