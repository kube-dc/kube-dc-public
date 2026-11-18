package clusterinit

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

var dnsSubdomainLabel = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?$`)

// ErrManagedServicesReadinessRequired marks a valid selection whose storage
// capacity or custom class still needs live evidence before installation.
var ErrManagedServicesReadinessRequired = errors.New("managed-services storage qualification is unresolved")

// ManagedServicesSelection is the reviewed, non-secret scaffold input. The
// Fleet helper still validates every value before writing an overlay.
type ManagedServicesSelection struct {
	InstallationKind         string
	Mode                     string // on or off
	DatabaseClass            string
	HAStorageClass           string
	HAStorageBudget          string
	StorageBudget            string
	EgressProbeURLs          []string
	NeedsClassExpansionCheck bool
	Warnings                 []string
}

// ResolveManagedServices determines whether a new site can offer managed
// services from its declared storage topology. An unknown physical capacity
// is never guessed: the operator must supply a budget before enabling it.
func ResolveManagedServices(o *InitOptions) (ManagedServicesSelection, error) {
	if o == nil {
		return ManagedServicesSelection{}, fmt.Errorf("managed services: nil install options")
	}
	result := ManagedServicesSelection{InstallationKind: o.InstallationKind, Mode: "off"}
	if o.InstallationKind == "" {
		return result, nil // Older engine callers; the CLI gate requires the kind.
	}
	if o.InstallationKind != "kube-dc" && o.InstallationKind != "cloudsigma" {
		return result, fmt.Errorf("--installation-kind must be kube-dc or cloudsigma")
	}
	mode := o.ManagedServicesMode
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != "on" && mode != "off" {
		return result, fmt.Errorf("--managed-services must be auto, on or off")
	}
	haClass, haBudget := o.ServicesHAStorageClass, o.ServicesHAStorageBudget
	if (haClass == "") != (haBudget == "") {
		return result, fmt.Errorf("local HA storage requires both --services-ha-storage-class and --services-ha-storage-budget")
	}
	if haClass != "" {
		if o.InstallationKind != "kube-dc" || mode == "off" || !validStorageClassName(haClass) {
			return result, fmt.Errorf("local HA storage requires managed services on for a kube-dc installation and a valid class")
		}
		q, err := resource.ParseQuantity(haBudget)
		if err != nil || q.Sign() <= 0 {
			return result, fmt.Errorf("--services-ha-storage-budget must be a positive Kubernetes quantity")
		}
	}
	if o.InstallationKind == "cloudsigma" {
		if mode == "on" {
			return result, fmt.Errorf("managed services are not qualified for CloudSigma installations")
		}
		return result, nil
	}
	class := o.ServicesDatabaseClass
	if class == "" {
		switch {
		case o.RookMode == RookCephLocal || o.RookMode == RookCephPVC:
			class = "ceph-block"
		case o.RookMode == RookCephMultiNode && o.VMStorageMode == VMStorageSharedRBD:
			class = "rbd-vm"
		}
	} else {
		result.NeedsClassExpansionCheck = true
	}
	if class != "" && !validStorageClassName(class) {
		return result, fmt.Errorf("--services-database-class must be a DNS subdomain with labels of at most 63 characters")
	}
	if haClass != "" && haClass == class {
		return result, fmt.Errorf("local HA class must be separate from expandable Development storage")
	}
	budget := o.ServicesStorageBudget
	if budget == "" && o.RookMode == RookCephPVC {
		// The OSDs that hold rbd-pool (ceph-block): the set it is pinned to by
		// CEPH_BLOCK_POOL_DEVICE_CLASS, or every set. Counts follow the
		// scaffold's precedence (flag, then --set, then the Fleet default).
		raw, err := pvcBlockPoolRawGB(o)
		if err != nil {
			return result, err
		}
		replicas := o.ObjectStorage().replication()
		if replicas > 0 && raw > 0 {
			budget = fmt.Sprintf("%dGi", raw/int64(replicas)/4)
		}
	}
	if budget != "" {
		quantity, err := resource.ParseQuantity(budget)
		if err != nil || quantity.Sign() <= 0 {
			return result, fmt.Errorf("--services-storage-budget must be a positive Kubernetes quantity")
		}
	}
	probes := append([]string(nil), o.ServicesEgressProbeURLs...)
	if len(probes) == 0 {
		probes = append(probes, DefaultServicesEgressProbeURLs...)
	}
	hosts := map[string]bool{}
	for _, probe := range probes {
		parsed, err := url.Parse(probe)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" ||
			parsed.User != nil || strings.Contains(strings.ToLower(parsed.Hostname()), "cloudsigma.com") {
			return result, fmt.Errorf("--services-egress-probe must be an HTTPS URL with a host outside CloudSigma")
		}
		hosts[strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")] = true
	}
	// Egress witnesses: one host would make that one domain a dependency of
	// every placement on the plane.
	if len(hosts) < 2 {
		return result, fmt.Errorf("--services-egress-probe needs at least two distinct hosts (stable public endpoints of different operators), so one domain going dark cannot stop the data plane")
	}
	availableBucket := o.RookMode == RookCephLocal || o.RookMode == RookCephPVC || o.RookMode == RookCephMultiNode
	qualified := availableBucket && class != "" && budget != "" && (!result.NeedsClassExpansionCheck || o.ServicesClassExpansionVerified)
	if (mode == "on" || haClass != "") && !qualified {
		if availableBucket && (budget == "" || (result.NeedsClassExpansionCheck && !o.ServicesClassExpansionVerified)) {
			return result, fmt.Errorf("%w: --managed-services=on needs a measured budget and a verified expandable class", ErrManagedServicesReadinessRequired)
		}
		return result, fmt.Errorf("--managed-services=on needs a rook-ceph bucket, an expandable database class and a bounded storage budget")
	}
	if mode == "off" || (mode == "auto" && !qualified) {
		if mode == "auto" {
			result.Warnings = append(result.Warnings, "managed services remain off until an expandable database class, Project bucket provider and bounded storage budget are qualified")
		}
		return result, nil
	}
	result.Mode, result.DatabaseClass, result.StorageBudget, result.EgressProbeURLs = "on", class, budget, probes
	result.HAStorageClass, result.HAStorageBudget = haClass, haBudget
	if amount, _ := resource.ParseQuantity(budget); amount.Cmp(resource.MustParse("52Gi")) < 0 {
		result.Warnings = append(result.Warnings, "managed-services storage budget is below the combined Development defaults (52Gi)")
	} else if amount.Cmp(resource.MustParse("60Gi")) < 0 {
		result.Warnings = append(result.Warnings, "managed-services storage budget is below one PostgreSQL Production allocation (60Gi)")
	}
	return result, nil
}

func validStorageClassName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || !dnsSubdomainLabel.MatchString(label) {
			return false
		}
	}
	return true
}

// DefaultServicesEgressProbeURLs are the data plane's egress witnesses when
// the operator names none: large, stable public endpoints of different
// operators, so that one domain going dark (codeberg.org, 2026-11-18) never
// leaves a new cluster's plane unready. A runner from 0.9.25 passes egress
// while any of them answers.
var DefaultServicesEgressProbeURLs = []string{
	"https://ghcr.io/v2/",
	"https://registry-1.docker.io/v2/",
	"https://www.google.com/generate_204",
}
